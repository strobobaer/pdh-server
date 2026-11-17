package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
	"pdh/internal/modules/it/netsync"
	"pdh/internal/modules/tickets"
)

// Omada als zweite Quelle des Netzabgleichs. Einen echten Controller gibt es
// nicht – der Leser ist im Speicher nachgebaut.

type fakeOmadaAPI struct {
	devices    []omada.Device
	clients    []omada.NetClient
	devErr     error
	clientsErr error
	calls      int
}

func (f *fakeOmadaAPI) Devices(context.Context) ([]omada.Device, error) {
	f.calls++
	return f.devices, f.devErr
}

func (f *fakeOmadaAPI) Clients(context.Context) ([]omada.NetClient, error) {
	return f.clients, f.clientsErr
}

func TestOmadaSourceFetch(t *testing.T) {
	f := &fakeOmadaAPI{
		devices: []omada.Device{
			{MAC: "AA-BB-CC-20-00-01", Name: "Gateway", Model: "ER7206", Kind: "gateway", IP: "10.0.0.1", Firmware: "2.1.0", State: omada.StateOnline},
			{MAC: "AA-BB-CC-20-00-02", Name: "Switch", Kind: "switch", Firmware: "1.4.2", State: omada.StateOffline, UplinkMAC: "AA-BB-CC-20-00-01"},
			{MAC: "AA-BB-CC-20-00-03", Name: "AP wartet", Kind: "ap", State: omada.StateUnknown},
			{MAC: "AA-BB-CC-20-00-04", Name: "AP seltsam", Kind: "ap", State: "PENDING"},
			{MAC: "kaputt", Name: "ohne MAC", State: omada.StateOnline},
		},
		clients: []omada.NetClient{
			{MAC: "AA-BB-CC-20-00-11", Name: "PC", IP: "10.0.0.50", UplinkMAC: "AA-BB-CC-20-00-02"},
			{MAC: "AA-BB-CC-20-00-12", Name: "Handy", Wireless: true, UplinkMAC: "AA-BB-CC-20-00-03"},
			{MAC: "", Name: "ohne MAC"},
		},
	}
	s := &omadaSource{api: f}
	if s.Kind() != "omada" {
		t.Errorf("Kind: %q", s.Kind())
	}
	snap, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Devices) != 4 {
		t.Fatalf("Geräte ohne gültige MAC müssen entfallen: %+v", snap.Devices)
	}
	gw, sw := snap.Devices[0], snap.Devices[1]
	if gw.ExternalID != "aa:bb:cc:20:00:01" || gw.Kind != "gateway" || gw.Firmware != "2.1.0" || gw.State != netsync.StateOnline || gw.UplinkExternalID != "" {
		t.Errorf("Gateway: %+v", gw)
	}
	if sw.ExternalID != "aa:bb:cc:20:00:02" || sw.State != netsync.StateOffline || sw.UplinkExternalID != "aa:bb:cc:20:00:01" {
		t.Errorf("Switch: %+v", sw)
	}
	for _, d := range snap.Devices {
		if !d.HasDetail || len(d.Ports) != 0 || d.FirmwareUpdate {
			t.Errorf("%s: Uplink frisch (HasDetail), keine Ports, kein Update-Hinweis erwartet: %+v", d.Name, d)
		}
	}
	// unbekannt bleibt unbekannt – nie offline
	for _, d := range snap.Devices[2:] {
		if d.State != netsync.StateUnknown {
			t.Errorf("%s: Zustand %q, erwartet unbekannt", d.Name, d.State)
		}
	}
	if len(snap.Clients) != 2 {
		t.Fatalf("Clients: %+v", snap.Clients)
	}
	pc, phone := snap.Clients[0], snap.Clients[1]
	if pc.ExternalID != "aa:bb:cc:20:00:11" || pc.Kind != "wired" || pc.UplinkExternalID != "aa:bb:cc:20:00:02" || pc.IP != "10.0.0.50" {
		t.Errorf("PC: %+v", pc)
	}
	if phone.Kind != "wireless" || phone.UplinkExternalID != "aa:bb:cc:20:00:03" {
		t.Errorf("Handy: %+v", phone)
	}

	// Fehler der Geräte- oder der Clientliste: kein halber Stand
	f.clientsErr = errors.New("clients kaputt")
	if snap, err := s.Fetch(context.Background()); err == nil || len(snap.Devices) != 0 {
		t.Errorf("Clientfehler: %+v %v", snap, err)
	}
	f.clientsErr, f.devErr = nil, omada.ErrAuth
	if _, err := s.Fetch(context.Background()); !errors.Is(err, omada.ErrAuth) {
		t.Errorf("Gerätefehler: %v", err)
	}
}

func TestOmadaStateMap(t *testing.T) {
	for in, want := range map[string]netsync.State{
		"online": netsync.StateOnline, "offline": netsync.StateOffline, "unknown": netsync.StateUnknown,
		"": netsync.StateUnknown, "OFFLINE?": netsync.StateUnknown,
	} {
		if got := omadaState(in); got != want {
			t.Errorf("omadaState(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

func TestITNetSourceNameOmada(t *testing.T) {
	if itNetSourceName("omada") != "Omada" || itNetSourceName("unifi") != "UniFi" {
		t.Errorf("Quellnamen: %q %q", itNetSourceName("omada"), itNetSourceName("unifi"))
	}
	h := &Handler{}
	if s, ok := h.itNetSrcByKey(""); !ok || s.Key != "unifi" {
		t.Errorf("ohne Feld: UniFi erwartet, %+v %v", s.Key, ok)
	}
	if s, ok := h.itNetSrcByKey("omada"); !ok || s.Name != "omada" || s.Manufacturer != "TP-Link" || s.notConnected() != "Omada ist noch nicht verbunden" {
		t.Errorf("omada: %+v %v", s, ok)
	}
	for _, bad := range []string{"OMADA", "SNMP", "snmp2", "unifi ", "'; --"} {
		if _, ok := h.itNetSrcByKey(bad); ok {
			t.Errorf("unbekannte Quelle %q angenommen", bad)
		}
	}
}

// itNetKeepSettings merkt sich Einstellungen und stellt sie nach dem Test wieder her.
func itNetKeepSettings(t *testing.T, h *Handler, keys ...string) {
	t.Helper()
	ctx := context.Background()
	for _, k := range keys {
		k, old := k, h.appSetting(ctx, k, "")
		t.Cleanup(func() { _ = h.setAppSetting(context.Background(), k, old) })
	}
}

var itNetOmadaKeys = []string{keyITNetOmadaOn, keyITNetOmadaLastErr, keyITNetOmadaLastRun, keyITNetOmadaLastCount}

// newITNetTwoEnv: beide Quellen eingeschaltet, je eine eigene Quelle und ein Leser im Speicher.
func newITNetTwoEnv(t *testing.T) (h *Handler, pool *pgxpool.Pool, usrc, osrc string, fu *fakeUnifiAPI, fo *fakeOmadaAPI) {
	t.Helper()
	h, pool, usrc, fu = newITNetRunEnv(t)
	ctx := context.Background()
	itNetKeepSettings(t, h, itNetOmadaKeys...)
	for k, v := range map[string]string{keyITNetOmadaOn: "1", keyITNetOmadaLastErr: "", keyITNetLastErr: ""} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	osrc = "omadatest" + strings.TrimPrefix(usrc, "unifitest")
	h.itNet.of(itNetSourceOmada).name = osrc
	fo = &fakeOmadaAPI{}
	h.itNet.of(itNetSourceOmada).open = itNetOmadaOpener(func(context.Context) (omadaAPIReader, error) { return fo, nil })
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE net_source = $1`, osrc)
		_, _ = pool.Exec(c, `DELETE FROM it_net_candidates WHERE source = $1`, osrc)
	})
	return h, pool, usrc, osrc, fu, fo
}

// itNetSeedAsset legt ein überwachtes Netzgerät einer Quelle an.
func itNetSeedAsset(t *testing.T, pool *pgxpool.Pool, src, ext, state string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO it_assets (name, type, status, created_by, net_source, net_external_id, net_role, net_monitored, net_state, net_state_since)
		VALUES ($1, 'network', 'active', $2::uuid, $3, $4, 'device', true, $5, now() - interval '1 hour') RETURNING id::text`,
		"Seed "+ext, pdhSystemUserID, src, ext, state).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func itNetAssetState(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var st string
	if err := pool.QueryRow(context.Background(), `SELECT net_state FROM it_assets WHERE id = $1::uuid`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// Omada-Abgleich: Geräte werden Assets (Hersteller TP-Link, Zustand, Firmware,
// Uplink, keine Ports), Clients werden Vorschläge der Quelle omada.
func TestITNetOmadaRunCreatesAssets(t *testing.T) {
	h, pool, usrc, osrc, _, fo := newITNetTwoEnv(t)
	ctx := context.Background()
	fo.devices = []omada.Device{
		{MAC: "AA-BB-CC-21-00-01", Name: "Gateway Büro", Model: "ER7206", Kind: "gateway", IP: "10.0.0.1", Firmware: "2.1.0", State: omada.StateOnline},
		{MAC: "AA-BB-CC-21-00-02", Name: "Switch Halle", Model: "SG3428", Kind: "switch", Firmware: "1.4.2", State: omada.StateOffline, UplinkMAC: "AA-BB-CC-21-00-01"},
		{MAC: "AA-BB-CC-21-00-03", Name: "AP Lager", Kind: "ap", Firmware: "5.0.9", State: omada.StateUnknown, UplinkMAC: "AA-BB-CC-21-00-02"},
	}
	fo.clients = []omada.NetClient{
		{MAC: "AA-BB-CC-21-00-11", Name: "PC Meister", IP: "10.0.0.50", UplinkMAC: "AA-BB-CC-21-00-02"},
		{MAC: "AA-BB-CC-21-00-12", Name: "Scanner", Wireless: true, UplinkMAC: "AA-BB-CC-21-00-03"},
	}
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	type row struct{ man, state, fw, uplink, mac, typ string }
	got := map[string]row{}
	rows, err := pool.Query(ctx, `SELECT name, COALESCE(manufacturer, ''), net_state, COALESCE(firmware, ''), COALESCE(net_uplink, ''), COALESCE(mac_address, ''), type::text
		FROM it_assets WHERE net_source = $1 AND net_role = 'device' AND NOT firmware_update`, osrc)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		var r row
		if err := rows.Scan(&name, &r.man, &r.state, &r.fw, &r.uplink, &r.mac, &r.typ); err != nil {
			t.Fatal(err)
		}
		got[name] = r
	}
	rows.Close()
	want := map[string]row{
		"Gateway Büro": {"TP-Link", "online", "2.1.0", "", "aa:bb:cc:21:00:01", "network"},
		"Switch Halle": {"TP-Link", "offline", "1.4.2", "Gateway Büro", "aa:bb:cc:21:00:02", "network"},
		"AP Lager":     {"TP-Link", "unknown", "5.0.9", "Switch Halle", "aa:bb:cc:21:00:03", "network"},
	}
	if len(got) != len(want) {
		t.Fatalf("Assets: %+v", got)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: %+v, erwartet %+v", name, got[name], w)
		}
	}
	var ports, uni int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_asset_ports p JOIN it_assets a ON a.id = p.asset_id WHERE a.net_source = $1`, osrc).Scan(&ports)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, usrc).Scan(&uni)
	if ports != 0 || uni != 0 {
		t.Errorf("Ports %d (keine erwartet), UniFi-Assets %d (keine erwartet)", ports, uni)
	}
	var cands int
	var kinds, uplinks string
	_ = pool.QueryRow(ctx, `SELECT count(*), string_agg(kind, ',' ORDER BY mac), string_agg(uplink, ',' ORDER BY mac) FROM it_net_candidates WHERE source = $1`, osrc).Scan(&cands, &kinds, &uplinks)
	if cands != 2 || kinds != "wired,wireless" || uplinks != "Switch Halle,AP Lager" {
		t.Errorf("Vorschläge: %d %q %q", cands, kinds, uplinks)
	}
	if got := h.appSetting(ctx, keyITNetOmadaLastCount, ""); got != "3/2" {
		t.Errorf("it.net.omada.last_count: %q", got)
	}
	if h.appSetting(ctx, keyITNetOmadaLastRun, "") == "" || h.appSetting(ctx, keyITNetOmadaLastErr, "x") != "" {
		t.Error("Marken der Quelle Omada fehlen oder Fehler gesetzt")
	}
	if got := h.appSetting(ctx, keyITNetLastCount, ""); got != "0/0" {
		t.Errorf("UniFi zählt für sich: %q", got)
	}
	if n := h.itNetOpenCandidates(ctx); n != 2 {
		t.Errorf("Zähler offener Vorschläge über beide Quellen: %d", n)
	}

	// Zweiter Lauf: der Uplink bleibt, die Firmware wird nachgezogen, kein Doppel.
	fo.devices[1].Firmware = "1.5.0"
	if err := h.itNetRunOnce(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var n int
	var fw, up string
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1`, osrc).Scan(&n)
	_ = pool.QueryRow(ctx, `SELECT firmware, net_uplink FROM it_assets WHERE net_source = $1 AND name = 'Switch Halle'`, osrc).Scan(&fw, &up)
	if n != 3 || fw != "1.5.0" || up != "Gateway Büro" {
		t.Errorf("zweiter Lauf: %d Assets, Firmware %q, Uplink %q", n, fw, up)
	}
}

// Scheitert Omada, bleibt UniFi unberührt – und umgekehrt.
func TestITNetSourcesFailIndependently(t *testing.T) {
	h, pool, usrc, osrc, fu, fo := newITNetTwoEnv(t)
	ctx := context.Background()
	fu.devices = []unifi.Device{{ID: "u1", Name: "UniFi-Switch", MAC: "AA:BB:CC:22:00:01", State: "ONLINE", Features: []string{"switching"}}}
	fo.devices = []omada.Device{{MAC: "AA-BB-CC-22-00-02", Name: "Omada-Switch", Kind: "switch", State: omada.StateOnline}}
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var uid, oid string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source = $1`, usrc).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source = $1`, osrc).Scan(&oid); err != nil {
		t.Fatal(err)
	}

	// Omada-Controller nicht erreichbar
	fo.devErr = errors.New("controller nicht erreichbar")
	if err := h.itNetRunOnce(ctx, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("Fehler der Quelle Omada erwartet")
	}
	if u, o := itNetAssetState(t, pool, uid), itNetAssetState(t, pool, oid); u != "online" || o != "unknown" {
		t.Errorf("Omada-Fehler: UniFi %q (online erwartet), Omada %q (unbekannt erwartet)", u, o)
	}
	if got := h.appSetting(ctx, keyITNetOmadaLastErr, ""); got != "controller nicht erreichbar" {
		t.Errorf("it.net.omada.last_error: %q", got)
	}
	if got := h.appSetting(ctx, keyITNetLastErr, "x"); got != "" {
		t.Errorf("UniFi darf keinen Fehler tragen: %q", got)
	}
	if h.itNet.of(itNetSourceUnifi).failing || !h.itNet.of(itNetSourceOmada).failing {
		t.Errorf("Fehlerzustand je Quelle: unifi=%v omada=%v", h.itNet.of(itNetSourceUnifi).failing, h.itNet.of(itNetSourceOmada).failing)
	}

	// Anmeldung abgelehnt: gleicher Weg, verständlicher Text
	fo.devErr = omada.ErrAuth
	_ = h.itNetRunOnce(ctx, time.Now().Add(2*time.Minute))
	if got := h.appSetting(ctx, keyITNetOmadaLastErr, ""); got != omada.ErrAuth.Error() {
		t.Errorf("Anmeldung abgelehnt: %q", got)
	}
	if u := itNetAssetState(t, pool, uid); u != "online" {
		t.Errorf("UniFi nach abgelehnter Omada-Anmeldung: %q", u)
	}

	// Umgekehrt: Omada wieder da, UniFi fällt aus
	fo.devErr, fu.devErr = nil, errors.New("verbindung abgelehnt")
	if err := h.itNetRunOnce(ctx, time.Now().Add(3*time.Minute)); err == nil {
		t.Fatal("Fehler der Quelle UniFi erwartet")
	}
	if u, o := itNetAssetState(t, pool, uid), itNetAssetState(t, pool, oid); u != "unknown" || o != "online" {
		t.Errorf("UniFi-Fehler: UniFi %q (unbekannt erwartet), Omada %q (online erwartet)", u, o)
	}
	if h.appSetting(ctx, keyITNetLastErr, "") != "verbindung abgelehnt" || h.appSetting(ctx, keyITNetOmadaLastErr, "x") != "" {
		t.Errorf("Fehlermarken: unifi %q omada %q", h.appSetting(ctx, keyITNetLastErr, ""), h.appSetting(ctx, keyITNetOmadaLastErr, ""))
	}
	if !h.itNet.of(itNetSourceUnifi).failing || h.itNet.of(itNetSourceOmada).failing {
		t.Errorf("Fehlerzustand je Quelle: unifi=%v omada=%v", h.itNet.of(itNetSourceUnifi).failing, h.itNet.of(itNetSourceOmada).failing)
	}
}

// Omada eingeschaltet, aber nicht eingerichtet: Hinweis statt Fehlerlauf; UniFi läuft.
func TestITNetOmadaNotConfigured(t *testing.T) {
	h, pool, usrc, osrc, fu, fo := newITNetTwoEnv(t)
	ctx := context.Background()
	oid := itNetSeedAsset(t, pool, osrc, "aa:bb:cc:23:00:02", "online")
	fu.devices = []unifi.Device{{ID: "u1", Name: "UniFi-Switch", MAC: "AA:BB:CC:23:00:01", State: "ONLINE", Features: []string{"switching"}}}
	h.itNet.of(itNetSourceOmada).open = itNetOmadaOpener(func(context.Context) (omadaAPIReader, error) { return nil, errOmadaOff })
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatalf("nicht eingerichtet: kein Fehler erwartet: %v", err)
	}
	if got := h.appSetting(ctx, keyITNetOmadaLastErr, ""); got != "Omada ist noch nicht verbunden" {
		t.Errorf("it.net.omada.last_error: %q", got)
	}
	if h.itNet.of(itNetSourceOmada).failing || fo.calls != 0 {
		t.Errorf("kein Fehlerzustand, kein Abruf: failing=%v calls=%d", h.itNet.of(itNetSourceOmada).failing, fo.calls)
	}
	if st := itNetAssetState(t, pool, oid); st != "unknown" {
		t.Errorf("Omada-Asset: %q", st)
	}
	var ust string
	if err := pool.QueryRow(ctx, `SELECT net_state FROM it_assets WHERE net_source = $1`, usrc).Scan(&ust); err != nil || ust != "online" {
		t.Errorf("UniFi muss normal laufen: %q %v", ust, err)
	}
	// zweiter Lauf setzt nicht jede Minute neu auf unbekannt
	_, _ = pool.Exec(ctx, `UPDATE it_assets SET net_state = 'online' WHERE id = $1::uuid`, oid)
	_ = h.itNetRunOnce(ctx, time.Now().Add(time.Minute))
	if st := itNetAssetState(t, pool, oid); st != "online" {
		t.Errorf("zweiter Lauf darf den Zustand nicht erneut ändern: %q", st)
	}
	// Fingerabdruck noch nicht bestätigt: das ist ein Fehler mit Text, UniFi bleibt
	h.itNet.of(itNetSourceOmada).open = itNetOmadaOpener(func(context.Context) (omadaAPIReader, error) { return nil, omada.ErrNoPin })
	if err := h.itNetRunOnce(ctx, time.Now().Add(2*time.Minute)); !errors.Is(err, omada.ErrNoPin) {
		t.Errorf("ohne Fingerabdruck: %v", err)
	}
	if st := itNetAssetState(t, pool, oid); st != "unknown" {
		t.Errorf("ohne Fingerabdruck: Omada-Asset %q", st)
	}
}

// Nur UniFi eingeschaltet: Omada wird nicht angefasst – und umgekehrt.
func TestITNetOnlyEnabledSourcesRun(t *testing.T) {
	h, _, _, _, fu, fo := newITNetTwoEnv(t)
	ctx := context.Background()
	opened := 0
	h.itNet.of(itNetSourceOmada).open = itNetOmadaOpener(func(context.Context) (omadaAPIReader, error) { opened++; return fo, nil })
	_ = h.setAppSetting(ctx, keyITNetOmadaOn, "0")
	fu.devices = []unifi.Device{{ID: "u1", Name: "SW", MAC: "AA:BB:CC:24:00:01", State: "ONLINE"}}
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || fo.calls != 0 {
		t.Errorf("Omada aus: kein Abruf erwartet (%d/%d)", opened, fo.calls)
	}
	_ = h.setAppSetting(ctx, keyITNetOmadaOn, "1")
	_ = h.setAppSetting(ctx, keyITNetUnifiOn, "0")
	uni := 0
	h.itNet.of(itNetSourceUnifi).open = h.itNetUnifiOpener(func(context.Context) (unifiAPIReader, error) { uni++; return fu, nil })
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if uni != 0 || fo.calls != 1 {
		t.Errorf("nur Omada an: UniFi %d Abrufe, Omada %d", uni, fo.calls)
	}
}

// Unbekannter Zustand löst kein Ticket aus; offline nach der Karenz schon.
func TestITNetOmadaUnknownOpensNoTicket(t *testing.T) {
	h, pool, _, osrc, _, fo := newITNetTwoEnv(t)
	ctx := context.Background()
	h.tickets = tickets.NewService(tickets.NewRepository(pool))
	itNetKeepSettings(t, h, keyITNetGrace)
	if err := h.setAppSetting(ctx, keyITNetGrace, "5"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE id IN (SELECT s.ticket_id FROM it_net_alarm_state s JOIN it_assets a ON a.id = s.asset_id WHERE a.net_source = $1)`, osrc)
	})
	fo.devices = []omada.Device{
		{MAC: "AA-BB-CC-25-00-01", Name: "AP unbekannt " + osrc, Kind: "ap", State: omada.StateUnknown},
		{MAC: "AA-BB-CC-25-00-02", Name: "Switch aus " + osrc, Kind: "switch", State: omada.StateOffline},
	}
	t0 := time.Now()
	for _, at := range []time.Time{t0, t0.Add(30 * time.Minute), t0.Add(time.Hour)} {
		if err := h.itNetRunOnce(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	ticketOf := func(mac string) (state string, ticket bool) {
		var tk *string
		// ohne Zeile: der Wächter hat für das Asset nie etwas vermerkt
		err := pool.QueryRow(ctx, `SELECT s.state, s.ticket_id::text FROM it_net_alarm_state s JOIN it_assets a ON a.id = s.asset_id
			WHERE a.net_source = $1 AND a.net_external_id = $2`, osrc, mac).Scan(&state, &tk)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s: %v", mac, err)
		}
		return state, tk != nil
	}
	var monitored int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1 AND net_monitored AND status = 'active'`, osrc).Scan(&monitored)
	if monitored != 2 {
		t.Fatalf("beide Geräte müssen überwacht sein, sonst prüft der Test nichts: %d", monitored)
	}
	if st, tk := ticketOf("aa:bb:cc:25:00:01"); st == "alarm" || tk {
		t.Errorf("unbekannter Zustand darf kein Ticket auslösen: %q ticket=%v", st, tk)
	}
	if st, tk := ticketOf("aa:bb:cc:25:00:02"); st != "alarm" || !tk {
		t.Errorf("offline über die Karenz hinaus: Alarm mit Ticket erwartet: %q ticket=%v", st, tk)
	}
	// Fällt der Controller aus, wird aus „unbekannt“ kein weiterer Alarm.
	fo.devErr = errors.New("controller weg")
	_ = h.itNetRunOnce(ctx, t0.Add(2*time.Hour))
	_ = h.itNetRunOnce(ctx, t0.Add(3*time.Hour))
	var tickets int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_net_alarm_state s JOIN it_assets a ON a.id = s.asset_id
		WHERE a.net_source = $1 AND s.ticket_id IS NOT NULL`, osrc).Scan(&tickets)
	if tickets != 1 {
		t.Errorf("Ausfall des Controllers darf keine Tickets erzeugen: %d", tickets)
	}
}

// ── Seite ────────────────────────────────────────────────────

// itNetOmadaPageEnv: Seiten-Umgebung mit eigener Omada-Quelle.
func itNetOmadaPageEnv(t *testing.T) (*Handler, *pgxpool.Pool, string, string) {
	t.Helper()
	h, pool, usrc := itNetPageEnv(t)
	itNetKeepSettings(t, h, itNetOmadaKeys...)
	osrc := "omadaweb" + strings.TrimPrefix(usrc, "unifiweb")
	h.itNet.of(itNetSourceOmada).name = osrc
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE net_source = $1`, osrc)
		_, _ = pool.Exec(c, `DELETE FROM it_net_candidates WHERE source = $1`, osrc)
	})
	return h, pool, usrc, osrc
}

func TestITNetPageTwoSources(t *testing.T) {
	h, pool, usrc, osrc := itNetOmadaPageEnv(t)
	ctx := context.Background()
	for _, c := range [][3]string{{usrc, "aa:bb:cc:26:00:01", "Drucker UniFi"}, {osrc, "aa:bb:cc:26:00:02", "Kamera Omada"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, name, ip, kind, last_seen) VALUES ($1, $2, $3, '10.0.0.7', 'wired', now())`, c[0], c[1], c[2]); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{keyITNetUnifiOn: "0", keyITNetOmadaOn: "1", keyITNetOmadaLastErr: "controller nicht erreichbar", keyITNetOmadaLastCount: "4/17", keyITNetLastErr: "alter UniFi-Fehler"} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	// alles außer system.omada
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permOmada }
	w := itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Seite: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Quelle UniFi", "Quelle Omada", `data-source="unifi"`, `data-source="omada"`,
		`name="enabled"`, `name="omada_enabled" value="1" checked`,
		"Abgleich mit UniFi einschalten", "Abgleich mit Omada einschalten",
		"Die Omada-Anbindung ist noch nicht an einem echten Controller erprobt.",
		"controller nicht erreichbar", ">17<", "<th>Quelle</th>",
		"Drucker UniFi", "Kamera Omada",
		`name="source" value="unifi"`, `name="source" value="omada"`,
		`/admin/server-config?tab=unifi`, "overflow-x:auto",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Seite ohne %q", want)
		}
	}
	for _, bad := range []string{`/admin/server-config?tab=omada`, "alter UniFi-Fehler", `name="enabled" value="1" checked`} {
		if strings.Contains(body, bad) {
			t.Errorf("Seite enthält %q (ohne system.omada kein Link; Fehler nur bei eingeschalteter Quelle)", bad)
		}
	}
	if strings.Contains(body, " disabled title=\"Erst bei einer Quelle") {
		t.Error("„Jetzt abgleichen“ muss möglich sein, wenn eine Quelle eingeschaltet ist")
	}
	// mit system.omada: Link zum Reiter
	h.hasPermHook = func(*http.Request, string) bool { return true }
	body = itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil).Body.String()
	if !strings.Contains(body, `/admin/server-config?tab=omada`) {
		t.Error("mit system.omada fehlt der Link zum Reiter „Omada“")
	}
	// nur it.view: Status ja, Schalter und Formulare nein
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == "it.view" }
	body = itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil).Body.String()
	if !strings.Contains(body, "Quelle Omada") || !strings.Contains(body, "Kamera Omada") {
		t.Error("mit it.view fehlt der Status oder der Vorschlag")
	}
	for _, bad := range []string{`name="omada_enabled"`, `name="source"`, `action="/it/net/run"`, `server-config?tab=`} {
		if strings.Contains(body, bad) {
			t.Errorf("nur it.view, trotzdem sichtbar: %s", bad)
		}
	}
	// beide aus: Knopf gesperrt, Lauf meldet „aus“
	h.hasPermHook = func(*http.Request, string) bool { return true }
	_ = h.setAppSetting(ctx, keyITNetOmadaOn, "0")
	body = itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil).Body.String()
	if !strings.Contains(body, "Erst bei einer Quelle den Abgleich einschalten") {
		t.Error("beide aus: Knopf muss gesperrt sein")
	}
	if w := itNetDo(h.ITNetRunWeb, "POST", "/it/net/run", "", "", url.Values{}); !strings.Contains(w.Header().Get("Location"), "n=off") {
		t.Errorf("beide aus: %s", w.Header().Get("Location"))
	}
}

// „Jetzt abgleichen“ läuft über alle eingeschalteten Quellen – auch wenn nur Omada an ist.
func TestITNetRunWebOmadaOnly(t *testing.T) {
	h, pool, _, osrc := itNetOmadaPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	itNetKeepSettings(t, h, keyITNetLastRun, keyITNetLastCount)
	_ = h.setAppSetting(ctx, keyITNetUnifiOn, "0")
	_ = h.setAppSetting(ctx, keyITNetOmadaOn, "1")
	fo := &fakeOmadaAPI{devices: []omada.Device{{MAC: "AA-BB-CC-27-00-01", Name: "Switch", Kind: "switch", State: omada.StateOnline}}}
	h.itNet.of(itNetSourceOmada).open = itNetOmadaOpener(func(context.Context) (omadaAPIReader, error) { return fo, nil })
	h.itNet.of(itNetSourceUnifi).open = h.itNetUnifiOpener(func(context.Context) (unifiAPIReader, error) {
		t.Error("UniFi ist aus und darf nicht abgerufen werden")
		return nil, errUnifiOff
	})
	w := itNetDo(h.ITNetRunWeb, "POST", "/it/net/run", "", "", url.Values{})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=run") {
		t.Fatalf("Lauf: %d %s", w.Code, w.Header().Get("Location"))
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source = $1 AND manufacturer = 'TP-Link'`, osrc).Scan(&n)
	if n != 1 {
		t.Errorf("Omada-Asset nach „Jetzt abgleichen“: %d", n)
	}
	fo.devErr = errors.New("weg")
	if w := itNetDo(h.ITNetRunWeb, "POST", "/it/net/run", "", "", url.Values{}); !strings.Contains(w.Header().Get("Location"), "e=run") {
		t.Errorf("Fehlerlauf: %s", w.Header().Get("Location"))
	}
}

// Einstellungsformular mit beiden Schaltern; Ausschalten einer Quelle setzt nur ihre Assets auf „unbekannt“.
func TestITNetSettingsBothSwitches(t *testing.T) {
	base := url.Values{"interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}}
	with := func(keys ...string) url.Values {
		v := url.Values{}
		for k, x := range base {
			v[k] = x
		}
		for _, k := range keys {
			v.Set(k, "1")
		}
		return v
	}
	if s, e := itNetSettingsFromForm(with("enabled", "omada_enabled")); e != "" || !s.Enabled || !s.OmadaEnabled {
		t.Errorf("beide an: %+v %q", s, e)
	}
	if s, _ := itNetSettingsFromForm(with("omada_enabled")); s.Enabled || !s.OmadaEnabled {
		t.Errorf("nur Omada: %+v", s)
	}
	if s, _ := itNetSettingsFromForm(with("enabled")); !s.Enabled || s.OmadaEnabled {
		t.Errorf("nur UniFi (altes Formular): %+v", s)
	}

	h, pool, usrc, osrc := itNetOmadaPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	uid, oid := itNetSeedAsset(t, pool, usrc, "u1", "online"), itNetSeedAsset(t, pool, osrc, "aa:bb:cc:28:00:01", "online")
	post := func(v url.Values) {
		t.Helper()
		w := itNetDo(h.ITNetSettingsWeb, "POST", "/it/net/settings", "", "", v)
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=saved") {
			t.Fatalf("speichern: %d %s", w.Code, w.Header().Get("Location"))
		}
	}
	post(with("enabled", "omada_enabled"))
	if h.appSetting(ctx, keyITNetUnifiOn, "") != "1" || h.appSetting(ctx, keyITNetOmadaOn, "") != "1" {
		t.Errorf("beide an: unifi %q omada %q", h.appSetting(ctx, keyITNetUnifiOn, ""), h.appSetting(ctx, keyITNetOmadaOn, ""))
	}
	if u, o := itNetAssetState(t, pool, uid), itNetAssetState(t, pool, oid); u != "online" || o != "online" {
		t.Errorf("Einschalten ändert keine Zustände: %q %q", u, o)
	}
	// Omada aus: nur Omada-Assets unbekannt
	post(with("enabled"))
	if h.appSetting(ctx, keyITNetUnifiOn, "") != "1" || h.appSetting(ctx, keyITNetOmadaOn, "") != "0" {
		t.Errorf("Omada aus: unifi %q omada %q", h.appSetting(ctx, keyITNetUnifiOn, ""), h.appSetting(ctx, keyITNetOmadaOn, ""))
	}
	if u, o := itNetAssetState(t, pool, uid), itNetAssetState(t, pool, oid); u != "online" || o != "unknown" {
		t.Errorf("Omada aus: UniFi %q (online erwartet), Omada %q (unbekannt erwartet)", u, o)
	}
	// UniFi aus, Omada an: nur UniFi-Assets unbekannt
	_, _ = pool.Exec(ctx, `UPDATE it_assets SET net_state = 'online' WHERE id = $1::uuid`, oid)
	post(with("omada_enabled"))
	if u, o := itNetAssetState(t, pool, uid), itNetAssetState(t, pool, oid); u != "unknown" || o != "online" {
		t.Errorf("UniFi aus: UniFi %q (unbekannt erwartet), Omada %q (online erwartet)", u, o)
	}
}

// Übernehmen und Ausblenden mit dem Feld „source“; unbekannte Quellen werden abgelehnt.
func TestITNetAdoptIgnoreBySource(t *testing.T) {
	h, pool, usrc, osrc := itNetOmadaPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	const mac = "aa:bb:cc:29:00:01"
	// dieselbe MAC als Vorschlag beider Quellen
	for _, s := range []string{usrc, osrc} {
		if _, err := pool.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, name, ip, kind, last_seen) VALUES ($1, $2, $3, '10.0.0.9', 'wireless', now())`, s, mac, "Tablet "+s); err != nil {
			t.Fatal(err)
		}
	}
	ignored := func(src string) bool {
		var ig bool
		if err := pool.QueryRow(ctx, `SELECT ignored FROM it_net_candidates WHERE source = $1 AND mac = $2`, src, mac).Scan(&ig); err != nil {
			t.Fatalf("Vorschlag %s: %v", src, err)
		}
		return ig
	}
	do := func(fn http.HandlerFunc, path string, v url.Values) string {
		t.Helper()
		w := itNetDo(fn, "POST", path, "", pdhSystemUserID, v)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s: Status %d", path, w.Code)
		}
		return w.Header().Get("Location")
	}
	// unbekannte Quelle: abgelehnt, nichts geändert
	for _, bad := range []string{"snmp", usrc, osrc, "OMADA"} {
		if loc := do(h.ITNetIgnoreWeb, "/it/net/candidates/ignore", url.Values{"source": {bad}, "mac": {mac}, "ignored": {"1"}}); !strings.Contains(loc, "e=source") {
			t.Errorf("Ausblenden mit Quelle %q: %s", bad, loc)
		}
		if loc := do(h.ITNetAdoptWeb, "/it/net/candidates/adopt", url.Values{"source": {bad}, "mac": {mac}, "type": {"printer"}}); !strings.Contains(loc, "e=source") {
			t.Errorf("Übernehmen mit Quelle %q: %s", bad, loc)
		}
	}
	var assets int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source IN ($1, $2)`, usrc, osrc).Scan(&assets)
	if ignored(usrc) || ignored(osrc) || assets != 0 {
		t.Fatalf("unbekannte Quelle darf nichts ändern: %v %v %d", ignored(usrc), ignored(osrc), assets)
	}
	// source=omada blendet nur den Omada-Vorschlag aus
	if loc := do(h.ITNetIgnoreWeb, "/it/net/candidates/ignore", url.Values{"source": {"omada"}, "mac": {mac}, "ignored": {"1"}}); !strings.Contains(loc, "n=ignored") {
		t.Errorf("Ausblenden omada: %s", loc)
	}
	if ignored(usrc) || !ignored(osrc) {
		t.Errorf("nur der Omada-Vorschlag ausgeblendet erwartet: unifi %v omada %v", ignored(usrc), ignored(osrc))
	}
	// ohne Feld: UniFi (alte Aufrufe)
	if loc := do(h.ITNetIgnoreWeb, "/it/net/candidates/ignore", url.Values{"mac": {mac}, "ignored": {"1"}}); !strings.Contains(loc, "n=ignored") || !ignored(usrc) {
		t.Errorf("ohne Feld source muss UniFi gelten: %s", loc)
	}
	do(h.ITNetIgnoreWeb, "/it/net/candidates/ignore", url.Values{"source": {"omada"}, "mac": {mac}, "ignored": {"0"}, "tab": {"hidden"}})
	if ignored(osrc) {
		t.Error("Zurückholen mit source=omada hat nicht gewirkt")
	}
	// übernehmen aus Omada
	if loc := do(h.ITNetAdoptWeb, "/it/net/candidates/adopt", url.Values{"source": {"omada"}, "mac": {"AA-BB-CC-29-00-01"}, "type": {"printer"}}); !strings.Contains(loc, "n=adopted") {
		t.Fatalf("Übernehmen omada: %s", loc)
	}
	var src, role, typ string
	if err := pool.QueryRow(ctx, `SELECT net_source, net_role, type::text FROM it_assets WHERE net_source IN ($1, $2)`, usrc, osrc).Scan(&src, &role, &typ); err != nil {
		t.Fatalf("Asset: %v", err)
	}
	if src != osrc || role != "client" || typ != "printer" {
		t.Errorf("Asset: Quelle %q Rolle %q Typ %q", src, role, typ)
	}
	// derselbe Client aus UniFi: kein zweites Asset, der Vorschlag verschwindet aus Liste und Zähler
	if loc := do(h.ITNetAdoptWeb, "/it/net/candidates/adopt", url.Values{"source": {"unifi"}, "mac": {mac}, "type": {"printer"}}); !strings.Contains(loc, "e=othersrc") {
		t.Errorf("gleiche MAC aus der anderen Quelle: %s", loc)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE net_source IN ($1, $2)`, usrc, osrc).Scan(&assets)
	if assets != 1 {
		t.Errorf("kein Doppel erwartet: %d Assets", assets)
	}
	if n := h.itNetOpenCandidates(ctx); n != 0 {
		t.Errorf("Zähler: %d", n)
	}
	for _, tab := range []string{"/it/net", "/it/net?tab=hidden"} {
		if body := itNetDo(h.ITNetPage, "GET", tab, "", "", nil).Body.String(); strings.Contains(body, "Tablet "+usrc) {
			t.Errorf("%s: Vorschlag der anderen Quelle zu einer schon übernommenen MAC darf nicht erscheinen", tab)
		}
	}
}

// Rechte: die geänderten Handler verlangen it.netsync – auch mit source=omada.
func TestITNetOmadaPerms(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return p == "it.view" }}
	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		form url.Values
	}{
		{"Einstellungen", h.ITNetSettingsWeb, "/it/net/settings", url.Values{"omada_enabled": {"1"}, "interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}}},
		{"Abgleich", h.ITNetRunWeb, "/it/net/run", url.Values{}},
		{"Übernehmen", h.ITNetAdoptWeb, "/it/net/candidates/adopt", url.Values{"source": {"omada"}, "mac": {"aa:bb:cc:dd:ee:ff"}, "type": {"printer"}}},
		{"Ausblenden", h.ITNetIgnoreWeb, "/it/net/candidates/ignore", url.Values{"source": {"omada"}, "mac": {"aa:bb:cc:dd:ee:ff"}, "ignored": {"1"}}},
	} {
		asked = nil
		if w := itNetDo(c.fn, "POST", c.path, "", "", c.form); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne it.netsync: Status %d", c.name, w.Code)
		}
		if len(asked) != 1 || asked[0] != "it.netsync" {
			t.Errorf("%s: geprüft %v, erwartet it.netsync", c.name, asked)
		}
	}
	h.hasPermHook = func(*http.Request, string) bool { return false }
	if w := itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil); w.Code != http.StatusForbidden {
		t.Errorf("Seite ohne it.view: %d", w.Code)
	}
}
