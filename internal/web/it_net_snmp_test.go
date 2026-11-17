package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/integrations/snmp"
	"pdh/internal/modules/it/netsync"
)

// ── Nachgebaute Geräte ───────────────────────────────────────

var (
	snmpErrTimeout = &snmp.Error{Kind: snmp.KindTimeout, Msg: "Keine Antwort"}
	snmpErrAuth    = &snmp.Error{Kind: snmp.KindAuth, Msg: "Zugang abgelehnt – Profil prüfen"}
	snmpErrResolve = &snmp.Error{Kind: snmp.KindResolve, Msg: "Name nicht auflösbar"}
	snmpErrOther   = &snmp.Error{Kind: snmp.KindOther, Msg: "Antwort des Geräts unverständlich"}
)

// fakeSNMPDev: ein Gerät im Speicher. Ohne Eintrag im Netz antwortet eine Adresse nicht.
type fakeSNMPDev struct {
	pingErr        error
	detail         snmp.Detail
	detailErr      error
	hang           bool // Ping wartet, bis das Zeitlimit abläuft
	pings, details int
}

type fakeSNMPNet struct {
	mu      sync.Mutex
	devs    map[string]*fakeSNMPDev
	cfgs    []snmp.Config
	delay   time.Duration
	running int
	maxRun  int
}

func (n *fakeSNMPNet) dial(cfg snmp.Config) snmpAPI {
	n.mu.Lock()
	n.cfgs = append(n.cfgs, cfg)
	n.mu.Unlock()
	return fakeSNMPClient{n: n, host: cfg.Host}
}

type fakeSNMPClient struct {
	n    *fakeSNMPNet
	host string
}

func (c fakeSNMPClient) enter() *fakeSNMPDev {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.n.running++
	c.n.maxRun = max(c.n.maxRun, c.n.running)
	return c.n.devs[c.host]
}

func (c fakeSNMPClient) leave() {
	c.n.mu.Lock()
	c.n.running--
	c.n.mu.Unlock()
}

func (c fakeSNMPClient) Ping(ctx context.Context) error {
	d := c.enter()
	defer c.leave()
	if c.n.delay > 0 {
		time.Sleep(c.n.delay)
	}
	if d == nil {
		return snmpErrTimeout
	}
	c.n.mu.Lock()
	d.pings++
	hang, err := d.hang, d.pingErr
	c.n.mu.Unlock()
	if hang {
		<-ctx.Done()
		return &snmp.Error{Kind: snmp.KindTimeout, Msg: "Keine Antwort", Err: ctx.Err()}
	}
	return err
}

func (c fakeSNMPClient) Detail(context.Context) (snmp.Detail, error) {
	d := c.enter()
	defer c.leave()
	if d == nil {
		return snmp.Detail{}, snmpErrTimeout
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	d.details++
	if d.detailErr != nil {
		return snmp.Detail{}, d.detailErr
	}
	out := d.detail
	out.Ports = append([]snmp.Port(nil), d.detail.Ports...)
	return out, nil
}

// snmpEnv: nur die Quelle SNMP eingeschaltet, eigene Quelle in den Assets, Geräte im
// Speicher, stellbare Uhr. Die beiden Tabellen gehören in der Test-Datenbank diesem Test.
type snmpEnv struct {
	t       *testing.T
	h       *Handler
	pool    *pgxpool.Pool
	src     string
	net     *fakeSNMPNet
	now     time.Time
	profile string
}

const (
	snmpTestAuth = "Test-Auth-12345"
	snmpTestPriv = "Test-Priv-12345"
)

func newSNMPEnv(t *testing.T) *snmpEnv {
	t.Helper()
	h, pool := newITNetTestHandler(t)
	ctx := context.Background()
	itNetKeepSettings(t, h, keyITNetUnifiOn, keyITNetOmadaOn, keyITNetSNMPOn, keyITNetSNMPLastErr, keyITNetSNMPLastRun, keyITNetSNMPLastCount)
	for k, v := range map[string]string{keyITNetUnifiOn: "0", keyITNetOmadaOn: "0", keyITNetSNMPOn: "1", keyITNetSNMPLastErr: ""} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	e := &snmpEnv{t: t, h: h, pool: pool, net: &fakeSNMPNet{devs: map[string]*fakeSNMPDev{}},
		now: time.Now().Truncate(time.Second)}
	e.src = "snmptest" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	h.itNet.of(itNetSourceSNMP).name = e.src
	h.itNet.snmpDial = e.net.dial
	wipe := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_snmp_targets`)
		_, _ = pool.Exec(c, `DELETE FROM it_snmp_profiles`)
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE net_source = $1`, e.src)
	}
	wipe()
	t.Cleanup(wipe)
	src, err := h.itNetSNMPOpener(e.src)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	src.(*snmpSource).now = func() time.Time { return e.now }
	e.profile = e.mkProfile("Switches")
	return e
}

func (e *snmpEnv) mkProfile(name string) string {
	e.t.Helper()
	id, err := e.h.snmpProfileCreate(context.Background(), SNMPProfileInput{Name: name, User: "pdh",
		AuthProto: "sha256", AuthPass: snmpTestAuth, PrivProto: "aes128", PrivPass: snmpTestPriv})
	if err != nil {
		e.t.Fatalf("Profil %s: %v", name, err)
	}
	return id
}

// dev legt ein Gerät in die Liste und ins nachgebaute Netz (online, mit Einzelheiten).
func (e *snmpEnv) dev(host string, d snmp.Detail) (string, *fakeSNMPDev) {
	e.t.Helper()
	id, err := e.h.snmpTargetCreate(context.Background(), host, 0, e.profile, pdhSystemUserID)
	if err != nil {
		e.t.Fatalf("Gerät %s: %v", host, err)
	}
	if d.IP == "" {
		d.IP = host
	}
	fd := &fakeSNMPDev{detail: d}
	e.net.mu.Lock()
	e.net.devs[host] = fd
	e.net.mu.Unlock()
	return id, fd
}

func (e *snmpEnv) set(fd *fakeSNMPDev, fn func(*fakeSNMPDev)) {
	e.net.mu.Lock()
	fn(fd)
	e.net.mu.Unlock()
}

// run: ein Lauf zur gestellten Uhrzeit.
func (e *snmpEnv) run() error {
	return e.h.itNetRunOnce(context.Background(), e.now)
}

func (e *snmpEnv) mustRun() {
	e.t.Helper()
	if err := e.run(); err != nil {
		e.t.Fatalf("Lauf: %v", err)
	}
}

// asset: Asset der Quelle SNMP zum Gerät ("" = keines).
func (e *snmpEnv) asset(target string) (id, state string) {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id::text, net_state FROM it_assets WHERE net_source = $1 AND net_external_id = $2`, e.src, target)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&id, &state); err != nil {
			e.t.Fatal(err)
		}
	}
	if n > 1 {
		e.t.Fatalf("Gerät %s hat %d Assets", target, n)
	}
	return id, state
}

func (e *snmpEnv) target(id string) SNMPTarget {
	e.t.Helper()
	list, err := e.h.snmpTargets(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, t := range list {
		if t.ID == id {
			return t
		}
	}
	e.t.Fatalf("Gerät %s nicht in der Liste", id)
	return SNMPTarget{}
}

func snmpSwitch(name, mac string) snmp.Detail {
	return snmp.Detail{Name: name, Model: "XS-24", Serial: "SN-" + name, Firmware: "1.2.3", MAC: mac, Kind: snmp.DeviceSwitch,
		Ports: []snmp.Port{{Idx: 1, Name: "Gi0/1", Up: true, SpeedMbps: 1000}, {Idx: 2, Name: "Gi0/2", Up: false}}}
}

// ── Quelle ───────────────────────────────────────────────────

// Die dritte Quelle steht in der Liste, mit denselben Einstellungsnamen wie die anderen;
// Vorschläge gibt es bei SNMP nicht.
func TestITNetSourcesSNMP(t *testing.T) {
	h := &Handler{}
	want := map[string][4]string{
		itNetSourceUnifi: {keyITNetUnifiOn, keyITNetLastRun, keyITNetLastErr, keyITNetLastCount},
		itNetSourceOmada: {keyITNetOmadaOn, keyITNetOmadaLastRun, keyITNetOmadaLastErr, keyITNetOmadaLastCount},
		itNetSourceSNMP:  {keyITNetSNMPOn, keyITNetSNMPLastRun, keyITNetSNMPLastErr, keyITNetSNMPLastCount},
	}
	srcs := h.itNetSources()
	if len(srcs) != 3 || srcs[0].Key != "unifi" || srcs[1].Key != "omada" || srcs[2].Key != "snmp" {
		t.Fatalf("Quellen: %+v", srcs)
	}
	for _, s := range srcs {
		if got := [4]string{s.keyOn, s.keyLastRun, s.keyLastErr, s.keyLastCount}; got != want[s.Key] {
			t.Errorf("%s: Einstellungen %v, erwartet %v", s.Key, got, want[s.Key])
		}
		if s.Name != s.Key || s.st == nil || s.open == nil {
			t.Errorf("%s: unvollständig: %+v", s.Key, s)
		}
	}
	if keyITNetSNMPOn != "it.net.snmp.enabled" || keyITNetSNMPLastErr != "it.net.snmp.last_error" ||
		keyITNetSNMPLastRun != "it.net.snmp.last_run" || keyITNetSNMPLastCount != "it.net.snmp.last_count" {
		t.Error("Einstellungsnamen der Quelle SNMP")
	}
	s, ok := h.itNetSrcByKey("snmp")
	if !ok || s.Label != "SNMP" || s.Manufacturer != "" || !s.NoClients || s.Field != "snmp_enabled" {
		t.Errorf("snmp: %+v %v", s, ok)
	}
	if srcs[0].NoClients || srcs[1].NoClients || srcs[0].Field != "enabled" || srcs[1].Field != "omada_enabled" {
		t.Error("UniFi und Omada: Felder unverändert, mit Vorschlägen")
	}
	if itNetSourceName("snmp") != "SNMP" || h.itNetSourceLabel("snmp") != "SNMP" {
		t.Errorf("Anzeigename: %q", itNetSourceName("snmp"))
	}
	if h.itNet.of("unifi") != h.itNet.of("unifi") || h.itNet.of("unifi") == h.itNet.of("snmp") {
		t.Error("Zustand je Quelle: eigener, bleibender Eintrag erwartet")
	}
	f := url.Values{"snmp_enabled": {"1"}, "interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}}
	if set, bad := itNetSettingsFromForm(f); bad != "" || !set.SNMPEnabled || set.Enabled || set.OmadaEnabled {
		t.Errorf("Formular: %+v %q", set, bad)
	}
	for k, want := range map[snmp.Kind]netsync.State{snmp.KindNone: netsync.StateOnline, snmp.KindTimeout: netsync.StateOffline,
		snmp.KindAuth: netsync.StateUnknown, snmp.KindResolve: netsync.StateUnknown, snmp.KindConfig: netsync.StateUnknown,
		snmp.KindOther: netsync.StateUnknown} {
		if got := snmpState(k); got != want {
			t.Errorf("snmpState(%v) = %q, erwartet %q", k, got, want)
		}
	}
}

// Antwort → online, keine Antwort → offline, alles andere → unbekannt; Angaben und
// Anschlüsse kommen ins Asset, Fehler im Klartext an die Geräteliste.
func TestSNMPSourceStates(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, _ := e.dev("10.77.0.1", snmpSwitch("sw-a", "02:77:00:00:00:01"))
	b, fb := e.dev("10.77.0.2", snmpSwitch("sw-b", "02:77:00:00:00:02"))
	c, fc := e.dev("10.77.0.3", snmpSwitch("sw-c", "02:77:00:00:00:03"))
	d, fd := e.dev("10.77.0.4", snmpSwitch("sw-d", "02:77:00:00:00:04"))
	o, fo := e.dev("10.77.0.5", snmpSwitch("sw-o", "02:77:00:00:00:05"))
	never, _ := e.dev("10.77.0.6", snmp.Detail{})
	e.net.mu.Lock()
	delete(e.net.devs, "10.77.0.6") // hat noch nie geantwortet
	e.net.mu.Unlock()

	e.mustRun()
	id, st := e.asset(a)
	if id == "" || st != "online" {
		t.Fatalf("Gerät a: Asset %q Zustand %q", id, st)
	}
	var name, typ, mac, ip, model, fw, serial, role string
	var man *string
	var monitored bool
	if err := e.pool.QueryRow(ctx, `SELECT name, type::text, mac_address, ip_address, model, firmware, serial_no, net_role, manufacturer, net_monitored
		FROM it_assets WHERE id = $1::uuid`, id).Scan(&name, &typ, &mac, &ip, &model, &fw, &serial, &role, &man, &monitored); err != nil {
		t.Fatal(err)
	}
	if name != "sw-a" || typ != "network" || mac != "02:77:00:00:00:01" || ip != "10.77.0.1" || model != "XS-24" || fw != "1.2.3" ||
		serial != "SN-sw-a" || role != "device" || man != nil || !monitored {
		t.Errorf("Asset: %q %q %q %q %q %q %q %q %v %v", name, typ, mac, ip, model, fw, serial, role, man, monitored)
	}
	rows, err := e.pool.Query(ctx, `SELECT idx, connector, state, speed_mbps, poe_state FROM it_asset_ports WHERE asset_id = $1::uuid ORDER BY idx`, id)
	if err != nil {
		t.Fatal(err)
	}
	var ports []string
	for rows.Next() {
		var idx, speed int
		var conn, state, poe string
		if err := rows.Scan(&idx, &conn, &state, &speed, &poe); err != nil {
			t.Fatal(err)
		}
		ports = append(ports, fmt.Sprintf("%d %s %s/%s %d %q", idx, conn, state, itNetPortState(state), speed, poe))
	}
	rows.Close()
	if got := strings.Join(ports, " | "); got != `1 Gi0/1 UP/aktiv 1000 "" | 2 Gi0/2 DOWN/getrennt 0 ""` {
		t.Errorf("Anschlüsse: %s", got)
	}
	if id, _ := e.asset(never); id != "" {
		t.Error("ein Gerät, das noch nie geantwortet hat, darf kein Asset werden")
	}
	if tn := e.target(never); tn.LastError != snmpNeverText || tn.LastOK != nil || tn.AssetID != "" {
		t.Errorf("nie erreichtes Gerät: %+v", tn)
	}
	if ta := e.target(a); ta.LastError != "" || ta.LastOK == nil || !ta.LastOK.Equal(e.now) || ta.AssetID != id || ta.AssetName != "sw-a" ||
		ta.NetState != "online" || ta.ProfileName != "Switches" || ta.Port != 161 || !ta.Enabled || ta.Note != "" {
		t.Errorf("Zeile der Geräteliste: %+v", ta)
	}
	if got := e.h.appSetting(ctx, keyITNetSNMPLastCount, ""); got != "5/0" {
		t.Errorf("last_count: %q", got)
	}
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, "x"); got != "" {
		t.Errorf("last_error: %q", got)
	}
	// keine Vorschläge bei SNMP
	var cands int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_net_candidates WHERE source = $1`, e.src).Scan(&cands)
	if cands != 0 {
		t.Errorf("%d Vorschläge bei SNMP", cands)
	}
	// Die Abfrage läuft mit dem Zugang des Profils (Kennwörter entschlüsselt, Port 161).
	e.net.mu.Lock()
	cfg := e.net.cfgs[0]
	e.net.mu.Unlock()
	if cfg.Port != 161 || cfg.User != "pdh" || cfg.AuthProto != "sha256" || cfg.PrivProto != "aes128" || cfg.AuthPass != snmpTestAuth || cfg.PrivPass != snmpTestPriv {
		t.Errorf("Zugang der Abfrage: %v", cfg)
	}

	// Zweiter Lauf: je Fehlerart ein Gerät. a antwortet weiter – kein Sturm-Schutz.
	e.now = e.now.Add(time.Minute)
	e.set(fb, func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	e.set(fc, func(f *fakeSNMPDev) { f.pingErr = snmpErrAuth })
	e.set(fd, func(f *fakeSNMPDev) { f.pingErr = snmpErrResolve })
	e.set(fo, func(f *fakeSNMPDev) { f.pingErr = snmpErrOther })
	e.mustRun()
	for tid, want := range map[string]string{a: "online", b: "offline", c: "unknown", d: "unknown", o: "unknown"} {
		if _, st := e.asset(tid); st != want {
			t.Errorf("Gerät %s: Zustand %q, erwartet %q", e.target(tid).Host, st, want)
		}
	}
	for tid, want := range map[string]string{a: "", b: "Keine Antwort", c: "Zugang abgelehnt – Profil prüfen", d: "Name nicht auflösbar",
		o: "Antwort des Geräts unverständlich"} {
		if got := e.target(tid).LastError; got != want {
			t.Errorf("Gerät %s: last_error %q, erwartet %q", e.target(tid).Host, got, want)
		}
	}
	if tb := e.target(b); tb.LastOK == nil || !tb.LastOK.Equal(e.now.Add(-time.Minute)) {
		t.Errorf("last_ok bleibt beim letzten Erfolg: %v", tb.LastOK)
	}
	// Angaben und Anschlüsse bleiben zwischen den Detail-Abrufen und bei einem Ausfall stehen.
	var n int
	bid, _ := e.asset(b)
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_asset_ports WHERE asset_id IN ($1::uuid, $2::uuid)`, id, bid).Scan(&n)
	_ = e.pool.QueryRow(ctx, `SELECT model, firmware, mac_address FROM it_assets WHERE id = $1::uuid`, bid).Scan(&model, &fw, &mac)
	if n != 4 || model != "XS-24" || fw != "1.2.3" || mac != "02:77:00:00:00:02" {
		t.Errorf("zwischen den Abrufen geleert: %d Anschlüsse, %q %q %q", n, model, fw, mac)
	}
}

// Sturm-Schutz: Antwortet keines von mindestens 3 Geräten, liegt es am PDH-Server – alle
// „unbekannt“, Fehler an der Quelle. Ein „Zugang abgelehnt“ beweist, dass das Netz steht.
func TestSNMPStormGuard(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	var ids []string
	var devs []*fakeSNMPDev
	for i := 1; i <= 3; i++ {
		id, fd := e.dev(fmt.Sprintf("10.77.1.%d", i), snmpSwitch(fmt.Sprintf("st-%d", i), fmt.Sprintf("02:77:01:00:00:%02x", i)))
		ids, devs = append(ids, id), append(devs, fd)
	}
	e.mustRun()
	all := func(want ...string) {
		t.Helper()
		for i, id := range ids {
			if _, st := e.asset(id); st != want[i] {
				t.Errorf("Gerät %d: Zustand %q, erwartet %q", i+1, st, want[i])
			}
		}
	}
	all("online", "online", "online")

	// Keines antwortet: alle unbekannt (nicht offline), Fehler an der Quelle.
	e.now = e.now.Add(time.Minute)
	for _, fd := range devs {
		e.set(fd, func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	}
	if err := e.run(); !errors.Is(err, errSNMPStorm) {
		t.Fatalf("Sturm-Schutz: Fehler erwartet, %v", err)
	}
	all("unknown", "unknown", "unknown")
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, ""); got != "Kein Gerät antwortet – Netz des PDH-Servers oder Freischaltung prüfen" {
		t.Errorf("last_error der Quelle: %q", got)
	}
	if !e.h.itNet.of(itNetSourceSNMP).failing {
		t.Error("Fehlerzustand der Quelle erwartet")
	}
	if got := e.target(ids[0]).LastError; got != "Keine Antwort" {
		t.Errorf("last_error am Gerät: %q", got)
	}
	var since time.Time
	aid, _ := e.asset(ids[0])
	_ = e.pool.QueryRow(ctx, `SELECT net_state_since FROM it_assets WHERE id = $1::uuid`, aid).Scan(&since)
	if !since.Equal(e.now) {
		t.Errorf("net_state_since: %v, erwartet %v", since, e.now)
	}
	// Der Wächter sieht kein Gerät als offline: keine Karenz läuft an, kein Ticket.
	var off int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1 AND net_state = 'offline'`, e.src).Scan(&off)
	if off != 0 {
		t.Errorf("%d Geräte offline trotz Sturm-Schutz", off)
	}

	// Ein Gerät lehnt den Zugang ab: Das Netz steht, die beiden anderen sind wirklich offline.
	e.now = e.now.Add(time.Minute)
	e.set(devs[0], func(f *fakeSNMPDev) { f.pingErr = snmpErrAuth })
	e.mustRun()
	all("unknown", "offline", "offline")
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, "x"); got != "" || e.h.itNet.of(itNetSourceSNMP).failing {
		t.Errorf("Fehler der Quelle nicht geleert: %q", got)
	}

	// Eines antwortet wieder: kein Schutz, die anderen bleiben offline.
	e.now = e.now.Add(time.Minute)
	e.set(devs[0], func(f *fakeSNMPDev) { f.pingErr = nil })
	e.mustRun()
	all("online", "offline", "offline")

	// Nur noch 2 eingeschaltet und keines antwortet: kein Schutz – beide offline, die Karenz gilt.
	e.now = e.now.Add(time.Minute)
	if err := e.h.snmpTargetSetEnabled(ctx, ids[0], false); err != nil {
		t.Fatal(err)
	}
	e.mustRun()
	all("unknown", "offline", "offline") // das ausgeschaltete wird nicht gefragt
	if p := devs[0].pings; p != 4 {
		t.Errorf("ausgeschaltetes Gerät wurde gefragt: %d Pings", p)
	}
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, "x"); got != "" {
		t.Errorf("bei 2 Geräten kein Fehler an der Quelle: %q", got)
	}
	var gone bool
	_ = e.pool.QueryRow(ctx, `SELECT net_gone FROM it_assets WHERE id = $1::uuid`, aid).Scan(&gone)
	if gone {
		t.Error("ausgeschaltetes Gerät gilt nicht als „nicht mehr gemeldet“")
	}
}

// Einzelheiten sofort beim ersten Mal, danach höchstens alle 15 min; die Seriennummer wird
// nur gefüllt, solange das Feld am Asset leer ist.
func TestSNMPDetailIntervalAndSerial(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, fa := e.dev("10.77.2.1", snmpSwitch("dt-a", "02:77:02:00:00:01"))
	// zweites Gerät: Einzelheiten scheitern zunächst – es lebt, wird aber noch kein Asset
	b, fb := e.dev("10.77.2.2", snmpSwitch("dt-b", "02:77:02:00:00:02"))
	e.set(fb, func(f *fakeSNMPDev) { f.detailErr = snmpErrOther })

	e.mustRun()
	if fa.pings != 1 || fa.details != 1 {
		t.Fatalf("erster Lauf: %d Pings, %d Detail-Abrufe", fa.pings, fa.details)
	}
	if id, _ := e.asset(b); id != "" {
		t.Error("ohne Einzelheiten noch kein Asset")
	}
	if tb := e.target(b); tb.LastError != "Angaben nicht gelesen: Antwort des Geräts unverständlich" || tb.LastOK == nil {
		t.Errorf("Gerät ohne Einzelheiten: %+v", tb)
	}
	aid, _ := e.asset(a)
	serial := func() string {
		var s *string
		_ = e.pool.QueryRow(ctx, `SELECT serial_no FROM it_assets WHERE id = $1::uuid`, aid).Scan(&s)
		if s == nil {
			return ""
		}
		return *s
	}
	if serial() != "SN-dt-a" {
		t.Fatalf("Seriennummer beim Anlegen: %q", serial())
	}
	// Anwender trägt eine eigene Nummer ein; das Gerät meldet künftig eine andere, neue Firmware.
	if _, err := e.pool.Exec(ctx, `UPDATE it_assets SET serial_no = 'HAND-1' WHERE id = $1::uuid`, aid); err != nil {
		t.Fatal(err)
	}
	e.set(fa, func(f *fakeSNMPDev) {
		f.detail.Serial, f.detail.Firmware = "SN-NEU", "2.0.0"
		f.detail.Ports = f.detail.Ports[:1]
	})
	fwPorts := func() (fw string, ports int) {
		_ = e.pool.QueryRow(ctx, `SELECT firmware, (SELECT count(*) FROM it_asset_ports WHERE asset_id = a.id) FROM it_assets a WHERE id = $1::uuid`, aid).Scan(&fw, &ports)
		return
	}
	for i := 0; i < 3; i++ { // +1, +2, +3 min: kein neuer Detail-Abruf für a
		e.now = e.now.Add(time.Minute)
		e.mustRun()
	}
	if fa.pings != 4 || fa.details != 1 {
		t.Errorf("vor Ablauf der 15 min: %d Pings, %d Detail-Abrufe", fa.pings, fa.details)
	}
	if fw, ports := fwPorts(); fw != "1.2.3" || ports != 2 {
		t.Errorf("zwischen den Abrufen: Firmware %q, %d Anschlüsse", fw, ports)
	}
	// Nach einem Fehlschlag kommt der nächste Versuch nach 3 min (ein Fünftel), nicht jede Minute.
	if fb.details != 2 {
		t.Errorf("Gerät ohne Einzelheiten: %d Versuche in 4 Läufen, erwartet 2", fb.details)
	}
	e.set(fb, func(f *fakeSNMPDev) { f.detailErr = nil })

	e.now = e.now.Add(12 * time.Minute) // +15 min
	e.mustRun()
	if fa.details != 2 {
		t.Errorf("nach 15 min: %d Detail-Abrufe", fa.details)
	}
	if fw, ports := fwPorts(); fw != "2.0.0" || ports != 1 {
		t.Errorf("nach dem Abruf: Firmware %q, %d Anschlüsse", fw, ports)
	}
	if serial() != "HAND-1" {
		t.Errorf("eingetragene Seriennummer überschrieben: %q", serial())
	}
	if id, st := e.asset(b); id == "" || st != "online" || e.target(b).LastError != "" {
		t.Errorf("Gerät b nach gelungenem Abruf: %q %q %q", id, st, e.target(b).LastError)
	}
	// Feld geleert: Der nächste Detail-Abruf trägt die Nummer des Geräts ein.
	if _, err := e.pool.Exec(ctx, `UPDATE it_assets SET serial_no = NULL WHERE id = $1::uuid`, aid); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(15 * time.Minute)
	e.mustRun()
	if serial() != "SN-NEU" {
		t.Errorf("leere Seriennummer nicht gefüllt: %q", serial())
	}
	// Name nur beim Anlegen: ein neuer sysName ändert den Namen des Assets nicht.
	var name string
	_ = e.pool.QueryRow(ctx, `SELECT name FROM it_assets WHERE id = $1::uuid`, aid).Scan(&name)
	if name != "dt-a" {
		t.Errorf("Name: %q", name)
	}
}

// Ein Gerät ohne MAC-Adresse und ohne Namen wird über die ID seiner Zeile zugeordnet.
func TestSNMPDeviceWithoutMAC(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, _ := e.dev("10.77.3.1", snmp.Detail{Model: "USV 3000"})
	e.mustRun()
	e.now = e.now.Add(16 * time.Minute)
	e.mustRun()
	id, st := e.asset(a)
	var n int
	var name string
	var mac *string
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, e.src).Scan(&n)
	_ = e.pool.QueryRow(ctx, `SELECT name, mac_address FROM it_assets WHERE id = $1::uuid`, id).Scan(&name, &mac)
	if n != 1 || st != "online" || name != "10.77.3.1" || mac != nil {
		t.Errorf("ohne MAC: %d Assets, Zustand %q, Name %q, MAC %v", n, st, name, mac)
	}
}

// Gehört die MAC schon zu einem Asset einer anderen Quelle, entsteht kein zweites Asset;
// die Geräteliste nennt den Grund.
func TestSNMPDuplicateMACOtherSource(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	usrc := "unifitest" + strings.TrimPrefix(e.src, "snmptest")
	e.h.itNet.of(itNetSourceUnifi).name = usrc
	var other string
	if err := e.pool.QueryRow(ctx, `INSERT INTO it_assets (name, type, status, created_by, mac_address, net_source, net_external_id, net_role, net_monitored, net_state)
		VALUES ('UniFi-Switch', 'network', 'active', $1::uuid, '02-77-04-00-00-01', $2, 'u1', 'device', true, 'online') RETURNING id::text`,
		pdhSystemUserID, usrc).Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM it_assets WHERE net_source = $1`, usrc) })
	a, _ := e.dev("10.77.4.1", snmpSwitch("dup-a", "02:77:04:00:00:01"))
	b, _ := e.dev("10.77.4.2", snmpSwitch("dup-b", "02:77:04:00:00:02"))

	for i := 0; i < 2; i++ { // auch zwischen den Detail-Abrufen
		e.mustRun()
		if id, _ := e.asset(a); id != "" {
			t.Fatal("zweites Asset für dieselbe MAC angelegt")
		}
		if ta := e.target(a); ta.Note != "schon über UniFi bekannt" || ta.AssetID != "" || ta.LastError != "" || ta.LastOK == nil {
			t.Errorf("Lauf %d: Zeile der Geräteliste: %+v", i+1, ta)
		}
		if tb := e.target(b); tb.Note != "" || tb.AssetID == "" {
			t.Errorf("Lauf %d: anderes Gerät: %+v", i+1, tb)
		}
		e.now = e.now.Add(time.Minute)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE regexp_replace(lower(mac_address), '[^0-9a-f]', '', 'g') = '027704000001'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d Assets mit derselben MAC", n)
	}
	// Das andere Asset entfällt: Jetzt wird das Gerät ein SNMP-Asset, der Hinweis verschwindet.
	if _, err := e.pool.Exec(ctx, `DELETE FROM it_assets WHERE id = $1::uuid`, other); err != nil {
		t.Fatal(err)
	}
	e.mustRun()
	if id, st := e.asset(a); id == "" || st != "online" || e.target(a).Note != "" {
		t.Errorf("nach dem Entfallen: Asset %q %q, Hinweis %q", id, st, e.target(a).Note)
	}
}

// Dasselbe Gerät zweimal in der Liste (Name und IP-Adresse): nur ein Asset, mit Hinweis.
func TestSNMPSameDeviceTwiceInList(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, _ := e.dev("10.77.10.1", snmpSwitch("zw-a", "02:77:0a:00:00:01"))
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_targets SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, a); err != nil {
		t.Fatal(err)
	}
	b, _ := e.dev("zw-a.firma.lan", snmpSwitch("zw-a", "02:77:0a:00:00:01"))
	for i := 0; i < 2; i++ { // auch zwischen den Detail-Abrufen
		e.mustRun()
		var n int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, e.src).Scan(&n)
		ida, _ := e.asset(a)
		idb, _ := e.asset(b)
		if n != 1 || ida == "" || idb != "" {
			t.Fatalf("Lauf %d: %d Assets (a=%q b=%q)", i+1, n, ida, idb)
		}
		if tb := e.target(b); tb.Note != "doppelt in der Liste – dasselbe Gerät wie 10.77.10.1" || tb.LastError != "" {
			t.Errorf("Lauf %d: Hinweis am zweiten Eintrag: %+v", i+1, tb)
		}
		if ta := e.target(a); ta.Note != "" {
			t.Errorf("Lauf %d: Hinweis am ersten Eintrag: %q", i+1, ta.Note)
		}
		e.now = e.now.Add(time.Minute)
	}
}

// Ein von Hand angelegtes Asset mit gleicher MAC wird verknüpft, nicht verdoppelt.
func TestSNMPLinksManualAsset(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	var manual string
	if err := e.pool.QueryRow(ctx, `INSERT INTO it_assets (name, type, status, created_by, mac_address, serial_no)
		VALUES ('Drucker Halle 2', 'printer', 'active', $1::uuid, '02:77:05:00:00:01', 'ALT-7') RETURNING id::text`, pdhSystemUserID).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM it_assets WHERE id = $1::uuid`, manual) })
	a, _ := e.dev("10.77.5.1", snmpSwitch("drucker", "02:77:05:00:00:01"))
	e.mustRun()
	id, st := e.asset(a)
	var name, typ, serial string
	_ = e.pool.QueryRow(ctx, `SELECT name, type::text, serial_no FROM it_assets WHERE id = $1::uuid`, manual).Scan(&name, &typ, &serial)
	if id != manual || st != "online" || name != "Drucker Halle 2" || typ != "printer" || serial != "ALT-7" {
		t.Errorf("verknüpft: %q (erwartet %q) %q %q %q %q", id, manual, st, name, typ, serial)
	}
}

// Entfernen aus der Liste: Das Asset bleibt, wird wie bei „Quelle trennen“ nicht mehr
// abgeglichen und nicht mehr überwacht.
func TestSNMPRemoveDetaches(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, fa := e.dev("10.77.6.1", snmpSwitch("rm-a", "02:77:06:00:00:01"))
	b, _ := e.dev("10.77.6.2", snmpSwitch("rm-b", "02:77:06:00:00:02"))
	e.mustRun()
	aid, _ := e.asset(a)
	if _, err := e.pool.Exec(ctx, `INSERT INTO it_net_alarm_state (asset_id, state, offline_since) VALUES ($1::uuid, 'alarm', now())`, aid); err != nil {
		t.Fatal(err)
	}
	if err := e.h.snmpTargetRemove(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := e.h.snmpTargetRemove(ctx, a); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("zweites Entfernen: %v", err)
	}
	check := func(when string) {
		t.Helper()
		var detached, monitored, gone bool
		var state, name string
		var ports, alarms int
		if err := e.pool.QueryRow(ctx, `SELECT net_detached, net_monitored, net_gone, net_state, name,
				(SELECT count(*) FROM it_asset_ports WHERE asset_id = a.id), (SELECT count(*) FROM it_net_alarm_state WHERE asset_id = a.id)
			FROM it_assets a WHERE id = $1::uuid`, aid).Scan(&detached, &monitored, &gone, &state, &name, &ports, &alarms); err != nil {
			t.Fatalf("%s: Asset fehlt: %v", when, err)
		}
		if !detached || monitored || gone || state != "unknown" || name != "rm-a" || ports != 0 || alarms != 0 {
			t.Errorf("%s: detached=%v monitored=%v gone=%v state=%q ports=%d alarms=%d", when, detached, monitored, gone, state, ports, alarms)
		}
	}
	check("nach dem Entfernen")
	pings := fa.pings
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	check("nach dem nächsten Lauf")
	if fa.pings != pings {
		t.Error("entferntes Gerät wurde weiter gefragt")
	}
	if id, st := e.asset(b); id == "" || st != "online" {
		t.Errorf("das andere Gerät läuft weiter: %q %q", id, st)
	}
	list, _ := e.h.snmpTargets(ctx)
	if len(list) != 1 || list[0].ID != b {
		t.Errorf("Geräteliste nach dem Entfernen: %+v", list)
	}
	// Dasselbe Gerät neu aufgenommen: Das getrennte Asset bleibt getrennt, kein Doppel –
	// und der neue Eintrag sagt, warum kein Asset entsteht (K2).
	a2, _ := e.dev("10.77.6.1", snmpSwitch("rm-a", "02:77:06:00:00:01"))
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, e.src).Scan(&n)
	if id, _ := e.asset(a2); id != aid || n != 2 {
		t.Errorf("neu aufgenommen: Asset %q (erwartet das getrennte %q), %d Assets der Quelle", id, aid, n)
	}
	if tg := e.target(a2); tg.AssetID != "" || tg.DetachedAssetID != aid || tg.Note != snmpDetachedNote {
		t.Errorf("neu aufgenommen: Zeile der Geräteliste: %+v", tg)
	}
	check("nach dem erneuten Aufnehmen")
}

// Höchstens 8 Geräte gleichzeitig.
func TestSNMPParallelLimit(t *testing.T) {
	e := newSNMPEnv(t)
	for i := 1; i <= 24; i++ {
		e.dev(fmt.Sprintf("10.77.7.%d", i), snmpSwitch(fmt.Sprintf("p-%d", i), fmt.Sprintf("02:77:07:00:00:%02x", i)))
	}
	e.net.delay = 15 * time.Millisecond
	e.mustRun()
	if e.net.maxRun > snmpParallel || e.net.maxRun < 2 {
		t.Errorf("gleichzeitig gefragt: %d, erlaubt höchstens %d", e.net.maxRun, snmpParallel)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM it_assets WHERE net_source = $1 AND net_state = 'online'`, e.src).Scan(&n)
	if n != 24 {
		t.Errorf("%d von 24 Geräten online", n)
	}
}

// Reicht das Zeitlimit der Quelle nicht, gilt ein nicht fertig gefragtes Gerät als
// „unbekannt“ – nie als offline –, und die übrigen Ergebnisse zählen.
func TestSNMPDeadlineIsNotOffline(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, _ := e.dev("10.77.8.1", snmpSwitch("dl-a", "02:77:08:00:00:01"))
	b, fb := e.dev("10.77.8.2", snmpSwitch("dl-b", "02:77:08:00:00:02"))
	e.mustRun()
	e.set(fb, func(f *fakeSNMPDev) { f.hang = true })
	e.now = e.now.Add(time.Minute)
	src, _ := e.h.itNetSNMPOpener(e.src)(ctx)
	fctx, cancel := context.WithTimeout(ctx, snmpFetchReserve+300*time.Millisecond)
	defer cancel()
	start := time.Now()
	snap, err := src.Fetch(fctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Fetch hält die Reserve für den Abgleich nicht ein: %v", d)
	}
	got := map[string]netsync.State{}
	for _, d := range snap.Devices {
		got[d.ExternalID] = d.State
	}
	if got[a] != netsync.StateOnline || got[b] != netsync.StateUnknown {
		t.Errorf("Zustände: %v", got)
	}
	for _, d := range snap.Devices { // das nicht gefragte Gerät behält seinen Zustand am Asset
		if d.KeepState != (d.ExternalID == b) {
			t.Errorf("KeepState von %s: %v", d.ExternalID, d.KeepState)
		}
	}
	if tb := e.target(b); tb.LastError != snmpSkippedText {
		t.Errorf("last_error: %q", tb.LastError)
	}
}

// Sind die Kennwörter eines Profils nicht lesbar (z. B. Schlüssel des Servers gewechselt),
// wird das Gerät nicht gefragt und gilt als „unbekannt“ – mit klarem Hinweis.
func TestSNMPUnreadableSecret(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, fa := e.dev("10.77.9.1", snmpSwitch("ks-a", "02:77:09:00:00:01"))
	e.mustRun()
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_profiles SET priv_secret_enc = 'v1:kaputt' WHERE id = $1::uuid`, e.profile); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	if _, st := e.asset(a); st != "unknown" || fa.pings != 1 {
		t.Errorf("Zustand %q, %d Pings", st, fa.pings)
	}
	if got := e.target(a).LastError; !strings.Contains(got, "Kennwörter nicht lesbar") || strings.Contains(got, snmpTestAuth) {
		t.Errorf("last_error: %q", got)
	}
}

// ── Schlussprüfung ───────────────────────────────────────────

// restart: wie ein Neustart des Programms (oder der andere Server des Verbunds übernimmt) –
// die Quelle beginnt ohne Gedächtnis.
func (e *snmpEnv) restart() *snmpSource {
	e.t.Helper()
	e.h.itNet.of(itNetSourceSNMP).kept = nil
	return e.source()
}

func (e *snmpEnv) source() *snmpSource {
	e.t.Helper()
	src, err := e.h.itNetSNMPOpener(e.src)(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	s := src.(*snmpSource)
	s.now = func() time.Time { return e.now }
	return s
}

// setting stellt eine Einstellung für die Dauer des Tests.
func (e *snmpEnv) setting(key, value string) {
	e.t.Helper()
	itNetKeepSettings(e.t, e.h, key)
	if err := e.h.setAppSetting(context.Background(), key, value); err != nil {
		e.t.Fatal(err)
	}
}

// snmpCountSink zählt, was der Wächter meldet.
type snmpCountSink struct{ opened, repeated, recovered int }

func (s *snmpCountSink) OpenTicket(context.Context, pgx.Tx, netsync.AlarmAsset) (string, func(), error) {
	s.opened++
	return "", nil, errors.New("im Test wird kein Ticket angelegt")
}
func (s *snmpCountSink) TicketOpen(context.Context, pgx.Tx, string) (bool, error) { return false, nil }
func (s *snmpCountSink) Repeat(context.Context, pgx.Tx, netsync.AlarmAsset) (func(), error) {
	s.repeated++
	return nil, nil
}
func (s *snmpCountSink) Recovered(context.Context, pgx.Tx, netsync.AlarmAsset, time.Time) (func(), error) {
	s.recovered++
	return nil, nil
}

// W1: Nach einer Umschaltung antworten nur die Geräte, die den zweiten Server als Abfrager
// erlauben. Die übrigen gelten nicht als offline, sondern als „unbekannt“ – bis sie diesem
// Server einmal geantwortet haben. Ein laufender Alarm wird dadurch nicht still beendet.
func TestSNMPOfflineOnlyAfterAnswer(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	e.setting(keyITNetInterval, "60")
	var ids []string
	var devs []*fakeSNMPDev
	for i := 1; i <= 5; i++ {
		id, fd := e.dev(fmt.Sprintf("10.77.11.%d", i), snmpSwitch(fmt.Sprintf("um-%d", i), fmt.Sprintf("02:77:0b:00:00:%02x", i)))
		ids, devs = append(ids, id), append(devs, fd)
	}
	states := func(when string, want ...string) {
		t.Helper()
		for i, id := range ids {
			if _, st := e.asset(id); st != want[i] {
				t.Errorf("%s: Gerät %d: Zustand %q, erwartet %q", when, i+1, st, want[i])
			}
		}
	}
	step := func() {
		t.Helper()
		e.now = e.now.Add(time.Minute)
		e.mustRun()
	}
	e.mustRun()
	states("vor der Umschaltung", "online", "online", "online", "online", "online")

	// Gerät 5 fällt wirklich aus und ist im Alarm, bevor umgeschaltet wird.
	e.set(devs[4], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	step()
	states("echter Ausfall", "online", "online", "online", "online", "offline")
	a5, _ := e.asset(ids[4])
	if _, err := e.pool.Exec(ctx, `INSERT INTO it_net_alarm_state (asset_id, state, offline_since) VALUES ($1::uuid, 'alarm', $2)
		ON CONFLICT (asset_id) DO UPDATE SET state = 'alarm', offline_since = EXCLUDED.offline_since`, a5, e.now); err != nil {
		t.Fatal(err)
	}
	sink := &snmpCountSink{}
	watch := &netsync.Watch{DB: e.pool, Sink: sink, Source: e.src, Grace: func(context.Context) time.Duration { return 2 * time.Minute }}
	alarm := func() string {
		t.Helper()
		var st string
		if err := e.pool.QueryRow(ctx, `SELECT state FROM it_net_alarm_state WHERE asset_id = $1::uuid`, a5).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	// Umschaltung: Der zweite Server fragt. Geräte 3 und 4 erlauben ihn nicht.
	e.restart()
	e.set(devs[2], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	e.set(devs[3], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	for i := 0; i < 4; i++ { // länger als die Karenz
		step()
		states("nach der Umschaltung", "online", "online", "unknown", "unknown", "unknown")
		if err := watch.Pass(ctx, e.now); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.target(ids[2]).LastError; got != snmpNeverText {
		t.Errorf("Text am Gerät: %q", got)
	}
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, "x"); got != "" {
		t.Errorf("kein Fehler an der Quelle erwartet: %q", got)
	}
	// Kein Ticket für die nicht freigeschalteten Geräte; der laufende Alarm bleibt bestehen.
	if sink.opened != 0 || sink.repeated != 0 || sink.recovered != 0 || alarm() != "alarm" {
		t.Errorf("Wächter: %+v, Alarm von Gerät 5: %q", *sink, alarm())
	}

	// Gerät 3 wird freigeschaltet und antwortet: ab jetzt kann es wieder „offline“ werden.
	e.set(devs[2], func(f *fakeSNMPDev) { f.pingErr = nil })
	step()
	states("freigeschaltet", "online", "online", "online", "unknown", "unknown")
	e.set(devs[2], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	step()
	states("fällt danach aus", "online", "online", "offline", "unknown", "unknown")
	if got := e.target(ids[2]).LastError; got != "Keine Antwort" {
		t.Errorf("Text nach echtem Ausfall: %q", got)
	}
	// Ein abgelehnter Zugang zählt als Antwort an diesen Server.
	e.set(devs[3], func(f *fakeSNMPDev) { f.pingErr = snmpErrAuth })
	step()
	e.set(devs[3], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	step()
	states("nach abgelehntem Zugang", "online", "online", "offline", "offline", "unknown")

	// Gerät 5 kommt zurück: Der Alarm endet jetzt regulär mit „wieder erreichbar“.
	e.set(devs[4], func(f *fakeSNMPDev) { f.pingErr = nil })
	step()
	if err := watch.Pass(ctx, e.now); err != nil {
		t.Fatal(err)
	}
	if sink.recovered != 1 || alarm() != "normal" {
		t.Errorf("nach der Rückkehr: %+v, Alarm %q", *sink, alarm())
	}

	// Lange Pause ohne Neustart (Wartungssperre, Quelle aus, Rollenwechsel bei weiterlaufendem
	// Programm): Die Abfrage-Strecke beginnt neu. Mehr als 3 Intervalle, mindestens 5 min.
	src := e.source()
	if got := src.streakGap(ctx); got != 5*time.Minute {
		t.Errorf("Schwelle bei 60 s Intervall: %v", got)
	}
	e.now = e.now.Add(5 * time.Minute) // genau die Schwelle: noch dieselbe Strecke
	e.mustRun()
	states("an der Schwelle", "online", "online", "offline", "offline", "online")
	e.now = e.now.Add(5*time.Minute + time.Second)
	e.mustRun()
	states("nach langer Pause", "online", "online", "unknown", "unknown", "online")
	e.setting(keyITNetInterval, "600")
	if got := src.streakGap(ctx); got != 30*time.Minute {
		t.Errorf("Schwelle bei 600 s Intervall: %v", got)
	}
	// Geänderte Adresse: Das Gerät muss diesem Server unter der neuen Adresse erst antworten.
	e.setting(keyITNetInterval, "60")
	if err := e.h.snmpTargetUpdate(ctx, ids[0], "10.77.11.99", 161, e.profile); err != nil {
		t.Fatal(err)
	}
	step()
	states("neue Adresse ohne Antwort", "unknown", "online", "unknown", "unknown", "online")
}

// W3: Antwortet länger als die allgemeine Karenz kein Gerät, bekommen die Verantwortlichen
// genau eine Nachricht – kein Ticket je Gerät. Die nächste erst, nachdem wieder ein Gerät
// geantwortet hat.
func TestSNMPStormNotice(t *testing.T) {
	e := newSNMPEnv(t)
	e.setting(keyITNetInterval, "60")
	e.setting(keyITNetGrace, "5")
	var resp string // Verantwortlicher des Netz-Abgleichs
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@example.invalid', 'x', 'Ines', 'Netz', 'viewer', '', '') RETURNING id::text`, "snmpsturm"+strings.TrimPrefix(e.src, "snmptest")).Scan(&resp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, resp) })
	e.setting(keyITNetRespUser, resp)
	type note struct {
		to   []string
		body string
	}
	var notes []note
	e.h.itNet.snmpStormNotify = func(to []string, body string) { notes = append(notes, note{to, body}) }
	var devs []*fakeSNMPDev
	for i := 1; i <= 3; i++ {
		_, fd := e.dev(fmt.Sprintf("10.77.12.%d", i), snmpSwitch(fmt.Sprintf("ta-%d", i), fmt.Sprintf("02:77:0c:00:00:%02x", i)))
		devs = append(devs, fd)
	}
	all := func(err error) {
		for _, fd := range devs {
			e.set(fd, func(f *fakeSNMPDev) { f.pingErr = err })
		}
	}
	storm := func(minutes int) {
		t.Helper()
		for i := 0; i < minutes; i++ {
			e.now = e.now.Add(time.Minute)
			if err := e.run(); !errors.Is(err, errSNMPStorm) {
				t.Fatalf("Sturm-Schutz erwartet: %v", err)
			}
		}
	}
	e.mustRun()
	all(snmpErrTimeout)
	storm(5) // Beginn bei +1 min; bei +5 min sind erst 4 min vergangen
	if len(notes) != 0 {
		t.Fatalf("vor Ablauf der Karenz gemeldet: %+v", notes)
	}
	storm(1)
	want := "SNMP: Seit 5 min antwortet kein Gerät. Entweder ist das Netz des PDH-Servers gestört oder alle SNMP-Geräte sind ausgefallen."
	if len(notes) != 1 || !strings.Contains(notes[0].body, want) || len(notes[0].to) != 1 || notes[0].to[0] != resp {
		t.Fatalf("Meldung nach der Karenz: %+v", notes)
	}
	storm(10)
	if len(notes) != 1 {
		t.Errorf("mehr als eine Meldung für denselben Ausfall: %d", len(notes))
	}
	var off int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM it_assets WHERE net_source = $1 AND net_state <> 'unknown'`, e.src).Scan(&off)
	if off != 0 {
		t.Errorf("%d Geräte nicht „unbekannt“", off)
	}
	// Ein Gerät antwortet wieder, danach fällt erneut alles aus: neue Zählung, neue Meldung.
	e.set(devs[0], func(f *fakeSNMPDev) { f.pingErr = nil })
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	all(snmpErrTimeout)
	storm(5)
	if len(notes) != 1 {
		t.Errorf("zweiter Ausfall zu früh gemeldet: %d", len(notes))
	}
	storm(1)
	if len(notes) != 2 {
		t.Errorf("zweiter Ausfall nicht gemeldet: %d Meldungen", len(notes))
	}
}

// K4: Wurde kein Gerät wirklich gefragt (Profile unlesbar, Namen nicht auflösbar), nennt
// die Quelle genau das – nicht „Netz des PDH-Servers prüfen“ – und es geht keine Meldung
// über einen Totalausfall hinaus.
func TestSNMPNoneAskedText(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	e.setting(keyITNetInterval, "60")
	e.setting(keyITNetGrace, "1")
	told := 0
	e.h.itNet.snmpStormNotify = func([]string, string) { told++ }
	var devs []*fakeSNMPDev
	for i := 1; i <= 3; i++ {
		_, fd := e.dev(fmt.Sprintf("10.77.13.%d", i), snmpSwitch(fmt.Sprintf("kg-%d", i), fmt.Sprintf("02:77:0d:00:00:%02x", i)))
		devs = append(devs, fd)
	}
	e.mustRun()
	// alle Namen nicht auflösbar
	for _, fd := range devs {
		e.set(fd, func(f *fakeSNMPDev) { f.pingErr = snmpErrResolve })
	}
	for i := 0; i < 3; i++ {
		e.now = e.now.Add(time.Minute)
		if err := e.run(); !errors.Is(err, errSNMPNoneAsked) || errors.Is(err, errSNMPStorm) {
			t.Fatalf("eigener Fehler erwartet: %v", err)
		}
	}
	if got := e.h.appSetting(ctx, keyITNetSNMPLastErr, ""); got != "Kein Gerät konnte gefragt werden – Zugangsprofile und Adressen prüfen" {
		t.Errorf("last_error der Quelle: %q", got)
	}
	var known int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1 AND net_state <> 'unknown'`, e.src).Scan(&known)
	if known != 0 || told != 0 {
		t.Errorf("%d Geräte nicht „unbekannt“, %d Meldungen", known, told)
	}
	// Profil unlesbar: ebenso, ohne dass eine Anfrage hinausgeht
	for _, fd := range devs {
		e.set(fd, func(f *fakeSNMPDev) { f.pingErr = nil })
	}
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_profiles SET priv_secret_enc = 'v1:kaputt' WHERE id = $1::uuid`, e.profile); err != nil {
		t.Fatal(err)
	}
	pings := devs[0].pings
	e.now = e.now.Add(time.Minute)
	if err := e.run(); !errors.Is(err, errSNMPNoneAsked) || devs[0].pings != pings {
		t.Errorf("Profil unlesbar: %v, %d Pings", err, devs[0].pings-pings)
	}
	// Ging an ein Gerät wirklich eine Anfrage hinaus, bleibt es beim Sturm-Text.
	p2 := e.mkProfile("Lesbar")
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_targets SET profile_id = $1::uuid WHERE host = '10.77.13.1'`, p2); err != nil {
		t.Fatal(err)
	}
	e.set(devs[0], func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	e.now = e.now.Add(time.Minute)
	if err := e.run(); !errors.Is(err, errSNMPStorm) {
		t.Errorf("mit einer echten Anfrage: %v", err)
	}
}

// W4: Der Ping-Teil hat ein eigenes Zeitbudget. Reicht es nicht für alle, beginnt der nächste
// Lauf bei den Geräten, die leer ausgingen; ein nicht gefragtes Gerät behält seinen Zustand
// und gilt nicht als „nicht mehr gemeldet“.
func TestSNMPPingBudgetRotation(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	e.setting(keyITNetInterval, "60")
	var ids []string
	var devs []*fakeSNMPDev
	for i := 1; i <= 10; i++ {
		id, fd := e.dev(fmt.Sprintf("10.77.14.%d", i), snmpSwitch(fmt.Sprintf("zb-%d", i), fmt.Sprintf("02:77:0e:00:00:%02x", i)))
		ids, devs = append(ids, id), append(devs, fd)
		// feste Reihenfolge der Liste
		if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_targets SET created_at = now() - make_interval(mins => $2) WHERE id = $1::uuid`, id, 60-i); err != nil {
			t.Fatal(err)
		}
	}
	if snmpPingBudget != 30*time.Second || e.source().pingBudget != snmpPingBudget {
		t.Errorf("Budget für den Ping-Teil: %v / %v", snmpPingBudget, e.source().pingBudget)
	}
	// Jede Antwort braucht 300 ms, das Budget reicht nur für die ersten 8 gleichzeitig.
	e.net.delay = 300 * time.Millisecond
	e.source().pingBudget = 100 * time.Millisecond
	pings := func() string {
		e.net.mu.Lock()
		defer e.net.mu.Unlock()
		var b strings.Builder
		for _, fd := range devs {
			fmt.Fprintf(&b, "%d", fd.pings)
		}
		return b.String()
	}
	t0 := e.now
	e.mustRun()
	if got := pings(); got != "1111111100" {
		t.Fatalf("Lauf 1: gefragt %s", got)
	}
	if id, _ := e.asset(ids[8]); id != "" || e.target(ids[8]).LastError != snmpSkippedText {
		t.Errorf("nicht gefragtes neues Gerät: Asset %q, %q", id, e.target(ids[8]).LastError)
	}
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	if got := pings(); got != "2222221111" {
		t.Fatalf("Lauf 2 beginnt nicht bei den leer ausgegangenen: gefragt %s", got)
	}
	// Geräte 7 und 8 wurden in Lauf 2 nicht gefragt: Zustand und „seit“ bleiben.
	for _, i := range []int{6, 7} {
		var state string
		var since time.Time
		var gone bool
		aid, _ := e.asset(ids[i])
		if err := e.pool.QueryRow(ctx, `SELECT net_state, net_state_since, net_gone FROM it_assets WHERE id = $1::uuid`, aid).Scan(&state, &since, &gone); err != nil {
			t.Fatal(err)
		}
		if state != "online" || !since.Equal(t0) || gone {
			t.Errorf("Gerät %d: Zustand %q seit %v (erwartet online seit %v), nicht mehr gemeldet %v", i+1, state, since, t0, gone)
		}
		if got := e.target(ids[i]).LastError; got != snmpSkippedText {
			t.Errorf("Gerät %d: Hinweis %q", i+1, got)
		}
	}
	if _, st := e.asset(ids[9]); st != "online" {
		t.Errorf("Gerät 10 nach Lauf 2: %q", st)
	}
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	if got := pings(); got != "3333222222" {
		t.Errorf("Lauf 3: gefragt %s", got)
	}
	// Reicht die Zeit wieder für alle, bleibt keines mehr aus.
	e.net.delay = 0
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	if got := pings(); got != "4444333333" {
		t.Errorf("Lauf 4: gefragt %s", got)
	}
}

// K1: Der ältere Eintrag hatte bisher kein Asset, der jüngere (dasselbe Gerät) hat eines.
// Wird der ältere erreichbar, entsteht kein zweites Asset – auch nicht direkt nach einem
// Neustart, wenn die MAC-Adresse nur noch am Asset steht.
func TestSNMPDuplicateOfEntryWithAsset(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, fa := e.dev("sw-doppelt.firma.lan", snmpSwitch("dp-a", "02:77:0f:00:00:01"))
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_targets SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, a); err != nil {
		t.Fatal(err)
	}
	e.set(fa, func(f *fakeSNMPDev) { f.pingErr = snmpErrResolve }) // Name noch nicht im DNS
	b, fb := e.dev("10.77.15.1", snmpSwitch("dp-a", "02:77:0f:00:00:01"))
	e.mustRun()
	bid, _ := e.asset(b)
	if id, _ := e.asset(a); id != "" || bid == "" {
		t.Fatalf("Ausgangslage: a=%q b=%q", id, bid)
	}
	count := func() int {
		var n int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, e.src).Scan(&n)
		return n
	}
	check := func(when string) {
		t.Helper()
		if id, _ := e.asset(a); id != "" || count() != 1 {
			t.Errorf("%s: zweites Asset angelegt (a=%q, %d Assets)", when, id, count())
		}
		if ta := e.target(a); ta.Note != "doppelt in der Liste – dasselbe Gerät wie 10.77.15.1" {
			t.Errorf("%s: Hinweis am älteren Eintrag: %q", when, ta.Note)
		}
		if tb := e.target(b); tb.Note != "" || tb.AssetID != bid {
			t.Errorf("%s: Eintrag mit Asset: %+v", when, tb)
		}
	}
	e.set(fa, func(f *fakeSNMPDev) { f.pingErr = nil })
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	check("älterer Eintrag wird erreichbar")

	// Neustart, der Eintrag mit Asset antwortet gerade nicht: Seine MAC kommt vom Asset.
	e.restart()
	e.set(fb, func(f *fakeSNMPDev) { f.pingErr = snmpErrTimeout })
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	check("nach Neustart")
}

// K2: Ist das Asset eines Eintrags vom Abgleich getrennt, steht das am Eintrag – mit dem
// getrennten Asset für den Link. „Wieder verbinden“ am Asset führt den Eintrag weiter.
func TestSNMPDetachedAssetHint(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	a, _ := e.dev("10.77.16.1", snmpSwitch("gt-a", "02:77:10:00:00:01"))
	e.mustRun()
	aid, _ := e.asset(a)
	// „Quelle trennen“ am Asset
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := itNetDetachTx(ctx, tx, aid); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	hint := func(when, target string) {
		t.Helper()
		e.now = e.now.Add(time.Minute)
		e.mustRun()
		tg := e.target(target)
		if tg.Note != "Das IT-Asset ist vom Abgleich getrennt – am Asset ‚Wieder verbinden‘ wählen" || tg.DetachedAssetID != aid ||
			tg.DetachedAssetName != "gt-a" || tg.AssetID != "" || tg.NetState != "" || tg.anyAssetID() != aid {
			t.Errorf("%s: %+v", when, tg)
		}
		if label, _ := itNetSNMPState(tg); label != "Asset getrennt" {
			t.Errorf("%s: Abzeichen %q", when, label)
		}
		var n int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, e.src).Scan(&n)
		if n != 1 {
			t.Errorf("%s: %d Assets", when, n)
		}
	}
	hint("am Asset getrennt", a)

	// Entfernen und neu aufnehmen: Der neue Eintrag zeigt auf dasselbe getrennte Asset.
	if err := e.h.snmpTargetRemove(ctx, a); err != nil {
		t.Fatal(err)
	}
	a2, _ := e.dev("10.77.16.1", snmpSwitch("gt-a", "02:77:10:00:00:01"))
	hint("neu aufgenommen", a2)
	hint("ein Lauf später", a2)

	// „Wieder verbinden“ am Asset (wie ITNetReattachWeb): Der Eintrag hat sein Asset wieder.
	if _, err := e.pool.Exec(ctx, `UPDATE it_assets SET net_detached = false, net_monitored = true WHERE id = $1::uuid`, aid); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	e.mustRun()
	if tg := e.target(a2); tg.AssetID != aid || tg.NetState != "online" || tg.Note != "" || tg.DetachedAssetID != "" {
		t.Errorf("wieder verbunden: %+v", tg)
	}
}

// ── Migration 167 ────────────────────────────────────────────

func TestMigration167Text(t *testing.T) {
	b, err := os.ReadFile("../../migrations/167_it_snmp.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS it_snmp_profiles", "CREATE TABLE IF NOT EXISTS it_snmp_targets",
		"DEFAULT pdh_uuid_v7()", "REFERENCES it_snmp_profiles(id) ON DELETE RESTRICT", "(lower(name))", "(lower(host), port)",
		"'system.snmp'", "'SNMP-Zugangsprofile verwalten'", "'System & Sicherheit'", "o.key = 'system.unifi'",
		"rp.department_id", "up.department_id", "up.granted", "INSERT INTO user_permissions", "ON CONFLICT (key) DO NOTHING",
		"ON CONFLICT (role_id, permission_id) DO NOTHING", "ON CONFLICT (user_id, permission_id) DO NOTHING"} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 167 enthält %q nicht", want)
		}
	}
	// „ON DELETE RESTRICT“ ist erlaubt, ein DELETE FROM nicht.
	if up := strings.ToUpper(s); strings.Contains(up, "DELETE FROM") || strings.Contains(up, "TRUNCATE") || strings.Contains(up, "DROP ") || strings.Contains(up, "DO UPDATE") {
		t.Error("Migration 167 darf nichts löschen und kein vorhandenes Recht ändern")
	}
}

// Migration 167 an der Datenbank: Tabellen, Eindeutigkeit, Schutz des Profils; wer
// system.unifi hat, bekommt system.snmp (Rollen samt Abteilung, Einzelrechte samt Entzug);
// wiederholbar.
func TestMigration167(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../migrations/167_it_snmp.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var cat, unifiCat string
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'system.snmp'`).Scan(&cat); err != nil || cat != "System & Sicherheit" {
		t.Fatalf("Recht system.snmp: %q %v", cat, err)
	}
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'system.unifi'`).Scan(&unifiCat); err != nil || unifiCat != cat {
		t.Fatalf("Kategorie wie system.unifi: %q / %q %v", cat, unifiCat, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'it_snmp_targets'::regclass AND contype = 'f' AND confdeltype = 'r'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Fremdschlüssel mit ON DELETE RESTRICT: %d %v", n, err)
	}
	// Niemand ohne system.unifi hat das neue Recht aus der Migration bekommen – außer über
	// eine eigene Vergabe; geprüft wird an frischen Rollen und Benutzern.
	stamp := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	mkRole := func(suffix string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'SNMP 167', 40) RETURNING id`, "sn167_"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, id) })
		return id
	}
	var dept string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ($1) RETURNING id`, "SNMP167 "+stamp).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1`, dept) })
	mkUser := func(suffix string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
			VALUES ($1::text, $1::text || '@example.invalid', 'x', 'SNMP', '167') RETURNING id`, "sn167-"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	rAll, rDept, rNone := mkRole("a"), mkRole("d"), mkRole("n")
	uYes, uNo, uDept, uNone := mkUser("y"), mkUser("x"), mkUser("d"), mkUser("n")
	exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT $1, id FROM permissions WHERE key = 'system.unifi'`, rAll)
	exec(`INSERT INTO role_permissions (role_id, permission_id, department_id) SELECT $1, id, $2 FROM permissions WHERE key = 'system.unifi'`, rDept, dept)
	exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT $1, id FROM permissions WHERE key IN ('system.certs', 'system.omada')`, rNone)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.unifi'`, uYes)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, false FROM permissions WHERE key = 'system.unifi'`, uNo)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted, department_id) SELECT $1, id, true, $2 FROM permissions WHERE key = 'system.unifi'`, uDept, dept)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.omada'`, uNone)

	exec(sql)

	role := func(id string) string {
		t.Helper()
		var out []string
		rows, err := pool.Query(ctx, `SELECT COALESCE(rp.department_id::text, 'überall') FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = $1 AND p.key = 'system.snmp'`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			out = append(out, d)
		}
		return strings.Join(out, ",")
	}
	user := func(id string) string {
		t.Helper()
		var out []string
		rows, err := pool.Query(ctx, `SELECT up.granted::text || ' ' || COALESCE(up.department_id::text, 'überall') FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
			WHERE up.user_id = $1 AND p.key = 'system.snmp'`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			out = append(out, d)
		}
		return strings.Join(out, ",")
	}
	check := func(when string) {
		t.Helper()
		if role(rAll) != "überall" || role(rDept) != dept || role(rNone) != "" {
			t.Errorf("%s: Rollen: %q %q %q", when, role(rAll), role(rDept), role(rNone))
		}
		if user(uYes) != "true überall" || user(uNo) != "false überall" || user(uDept) != "true "+dept || user(uNone) != "" {
			t.Errorf("%s: Einzelrechte: %q %q %q %q", when, user(uYes), user(uNo), user(uDept), user(uNone))
		}
	}
	check("erster Lauf")
	// Später geändert: Ein erneuter Lauf (Update, Wiederherstellung) dreht nichts zurück.
	exec(`UPDATE user_permissions SET granted = true WHERE user_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.snmp')`, uNo)
	exec(`DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.snmp')`, rDept)
	exec(`UPDATE role_permissions SET department_id = NULL WHERE role_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.unifi')`, rDept)
	exec(sql)
	if user(uNo) != "true überall" || role(rDept) != "überall" || role(rAll) != "überall" {
		t.Errorf("zweiter Lauf: %q %q %q", user(uNo), role(rDept), role(rAll))
	}
}
