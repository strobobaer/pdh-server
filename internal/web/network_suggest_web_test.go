package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

const netSuggPW = "Controller-Geheim-4711"

// netSuggEnv: Handler mit Testdatenbank, Hook statt echtem Controller (zählt die Abrufe) und
// aufgeräumten Testdaten. ctl liefert, was „UniFi“ gerade meldet; Omada ist ausgefallen.
type netSuggFix struct {
	h     *Handler
	pool  *pgxpool.Pool
	calls *atomic.Int32
	ctl   *[]netCtlResult
}

func netSuggClean(pool *pgxpool.Pool) {
	c := context.Background()
	_, _ = pool.Exec(c, `DELETE FROM net_wlans WHERE ext_id LIKE 't5-%' OR ssid LIKE 't5w-%'`)
	_, _ = pool.Exec(c, `DELETE FROM net_segments WHERE ext_id LIKE 't5-%' OR name LIKE 'T5 %'`)
	_, _ = pool.Exec(c, `DELETE FROM net_hidden_suggestions WHERE ext_id LIKE 't5-%'`)
}

func netSuggEnv(t *testing.T) *netSuggFix {
	t.Helper()
	h, pool, _ := itNetPageEnv(t)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	netSuggClean(pool)
	t.Cleanup(func() { netSuggClean(pool) })
	f := &netSuggFix{h: h, pool: pool, calls: &atomic.Int32{}}
	var ctl []netCtlResult
	f.ctl = &ctl
	h.netCtlFetchHook = func(context.Context) []netCtlResult {
		f.calls.Add(1)
		return append([]netCtlResult(nil), ctl...)
	}
	ctl = netSuggDefault()
	return f
}

// netSuggDefault: UniFi mit NEU-Netz, ANDERS-Netz, 3 gleichen Netzen, WLAN mit Kennwort (am NEU-Netz) und offenem WLAN;
// Omada nicht erreichbar.
func netSuggDefault() []netCtlResult {
	u := netCtlResult{Source: "unifi", Label: "UniFi", At: time.Now(),
		Nets: []netCtlNet{
			{ExtID: "t5-n-new", Name: "T5 Produktion", VLAN: 3101, Subnet: "10.99.31.0/24"},
			{ExtID: "t5-n-diff", Name: "T5 Büro", VLAN: 3102, Subnet: "10.99.32.0/24"},
			{ExtID: "t5-n-s1", Name: "T5 Gleich 1", VLAN: 3111, Subnet: "10.99.41.0/24"},
			{ExtID: "t5-n-s2", Name: "T5 Gleich 2", VLAN: 3112, Subnet: "10.99.42.0/24"},
			{ExtID: "t5-n-s3", Name: "T5 Gleich 3", VLAN: 3113, Subnet: "10.99.43.0/24"},
		},
		WLANs: []netCtlWLAN{
			{ExtID: "t5-w-pw", SSID: "t5w-iot", NetExtID: "t5-n-new", Security: unifi.SecPersonal, Password: netSuggPW, VLAN: 3101},
			{ExtID: "t5-w-open", SSID: "t5w-gast", Security: unifi.SecOpen},
		}}
	o := netCtlResult{Source: "omada", Label: "Omada", Err: netErrTimeout, At: time.Now()}
	return []netCtlResult{u, o}
}

// netSuggSeed legt die PDH-Einträge an: das ANDERS-Netz (mit Zweck, Notiz) und die 3 gleichen.
func netSuggSeed(t *testing.T, f *netSuggFix) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO net_segments (name, vlan_id, subnet, purpose, note, source, ext_id) VALUES ('T5 Büro', 3102, '10.99.2.0/24', 'Zweck-alt', 'Notiz-alt', 'unifi', 't5-n-diff')`,
		`INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id) VALUES ('T5 Gleich 1', 3111, '10.99.41.0/24', 'unifi', 't5-n-s1')`,
		`INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id) VALUES ('T5 Gleich 2', 3112, '10.99.42.0/24', 'unifi', 't5-n-s2')`,
		`INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id) VALUES ('T5 Gleich 3', 3113, '10.99.43.0/24', 'unifi', 't5-n-s3')`,
	} {
		if _, err := f.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func netSuggPost(f *netSuggFix, fn http.HandlerFunc, form url.Values) (int, string) {
	w := itNetDo(fn, "POST", "/x", "", pdhSystemUserID, form)
	return w.Code, w.Header().Get("Location")
}

// netSuggRow: Zeile der aktuellen Hook-Daten aus Sicht des PDH (für Fingerabdruck und Kennung).
func netSuggFp(t *testing.T, f *netSuggFix, kind, ext string) string {
	t.Helper()
	rows, key := f.h.netSuggRows(context.Background(), "unifi")
	if key != "" {
		t.Fatalf("Vorschläge: %s", key)
	}
	for _, r := range rows {
		if r.Kind == kind && r.ExtID == ext {
			return r.Fingerprint
		}
	}
	t.Fatalf("keine Zeile %s %s", kind, ext)
	return ""
}

// netSuggAllForm: Formular „Alle übernehmen“ mit dem Satz der jetzt gezeigten Zeilen.
func netSuggAllForm(t *testing.T, f *netSuggFix) url.Values {
	t.Helper()
	rows, _ := f.h.netSuggRows(context.Background(), "unifi")
	return url.Values{"source": {"unifi"}, "set": {netSetFP(rows)}}
}

func netSuggForm(t *testing.T, f *netSuggFix, kind, ext, fp string) url.Values {
	t.Helper()
	form := url.Values{"source": {"unifi"}, "kind": {kind}, "ext": {ext}, "fp": {fp}}
	// Zustand und PDH-Eintrag, wie sie die Liste jetzt zeigen würde (fehlt die Zeile: leer)
	if rows, key := f.h.netSuggRows(context.Background(), "unifi"); key == "" {
		for _, r := range rows {
			if r.Kind == kind && r.ExtID == ext {
				form.Set("state", r.State)
				form.Set("pdh", r.PDHID)
			}
		}
	}
	return form
}

func TestNetSuggBlockRender(t *testing.T) {
	f := netSuggEnv(t)
	netSuggSeed(t, f)
	w := itNetDo(f.h.NetworkSuggWeb, "GET", "/admin/network/suggestions", "", pdhSystemUserID, nil)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("Status %d", w.Code)
	}
	for _, want := range []string{"Vom Controller gefunden", "UniFi · vor", "NEU", "ANDERS", "Kennwort ✓", "offen",
		"3 Einträge stimmen schon überein.", "Omada · nicht erreichbar: Zeitüberschreitung", "Alle übernehmen",
		"Übernehmen", "Angleichen", "Ausblenden", "(wird mit angelegt)", "Neu abfragen"} {
		if !strings.Contains(body, want) {
			t.Errorf("Block ohne %q", want)
		}
	}
	if strings.Contains(body, netSuggPW) {
		t.Fatal("Kennwort steht im Block")
	}
	// ein am Controller verschwundenes Netz: Marke außerhalb (oob)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO net_segments (name, source, ext_id) VALUES ('T5 Weg', 'unifi', 't5-n-gone')`); err != nil {
		t.Fatal(err)
	}
	f.h.netSuggForget()
	body = itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil).Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) || !strings.Contains(body, "am Controller nicht mehr vorhanden") {
		t.Error("kein oob-Hinweis für Verschwundenes")
	}
}

func TestNetSuggCache(t *testing.T) {
	f := netSuggEnv(t)
	get := func(q string) {
		itNetDo(f.h.NetworkSuggWeb, "GET", "/admin/network/suggestions"+q, "", pdhSystemUserID, nil)
	}
	get("")
	get("")
	if f.calls.Load() != 1 {
		t.Fatalf("zwei Aufrufe: %d Abrufe", f.calls.Load())
	}
	get("?fresh=1")
	if f.calls.Load() != 2 {
		t.Fatalf("fresh: %d Abrufe", f.calls.Load())
	}
	f.h.netSugg.mu.Lock()
	f.h.netSugg.at = time.Now().Add(-6 * time.Minute)
	f.h.netSugg.mu.Unlock()
	get("")
	if f.calls.Load() != 3 {
		t.Fatalf("abgelaufen: %d Abrufe", f.calls.Load())
	}
}

type fakeUnifiReader struct {
	nets []unifi.Network
	wl   []unifi.WifiBroadcast
	err  error
	// block: Leser hängt, bis der Test endet
	block chan struct{}
}

func (f fakeUnifiReader) Networks(ctx context.Context) ([]unifi.Network, error) {
	if f.block != nil {
		<-f.block
	}
	return f.nets, f.err
}
func (f fakeUnifiReader) WifiBroadcasts(context.Context) ([]unifi.WifiBroadcast, error) {
	return f.wl, f.err
}

type fakeOmadaReader struct{ nets []omada.LanNetwork }

func (f fakeOmadaReader) LanNetworks(context.Context) ([]omada.LanNetwork, error) { return f.nets, nil }
func (f fakeOmadaReader) SSIDs(context.Context) ([]omada.SSID, error)             { return nil, nil }

func TestNetCtlFetchUnifiOld(t *testing.T) {
	r := netCtlFetchUnifi(context.Background(), fakeUnifiReader{err: unifi.ErrNetUnsupported})
	if r.Err != "UniFi-Network-Anwendung zu alt – Netze und WLANs gibt es ab Version 10.0" || len(r.Nets) != 0 {
		t.Fatalf("Err %q", r.Err)
	}
	ok := netCtlFetchUnifi(context.Background(), fakeUnifiReader{nets: []unifi.Network{{ID: "a", Name: "Büro", VLAN: 10}}})
	if ok.Err != "" || len(ok.Nets) != 1 || ok.Source != "unifi" {
		t.Fatalf("Erfolg: %+v", ok)
	}
}

type fakePanicReader struct{}

func (fakePanicReader) Networks(context.Context) ([]unifi.Network, error) { panic("kaputter Leser") }
func (fakePanicReader) WifiBroadcasts(context.Context) ([]unifi.WifiBroadcast, error) {
	return nil, nil
}

// Frist je Quelle über netCtlFetchAll: UniFi hängt, Omada liefert trotzdem; ein Leser, der abstürzt, gibt nur einen Fehler.
func TestNetCtlFetchTimeout(t *testing.T) {
	old := netCtlTimeout
	netCtlTimeout = 50 * time.Millisecond
	t.Cleanup(func() { netCtlTimeout = old })
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	h := &Handler{}
	h.netUnifiOpen = func(context.Context) (netUnifiReader, error) { return fakeUnifiReader{block: block}, nil }
	h.netOmadaOpen = func(context.Context) (netOmadaReader, error) {
		return fakeOmadaReader{nets: []omada.LanNetwork{{ID: "x", Name: "LAN", VLAN: 1}}}, nil
	}
	start := time.Now()
	res := h.netCtlFetchAll(context.Background())
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Abruf hat die Frist nicht eingehalten: %v", time.Since(start))
	}
	if len(res) != 2 || res[0].Source != "unifi" || res[0].Err != "nicht erreichbar: Zeitüberschreitung" {
		t.Fatalf("UniFi: %+v", res)
	}
	if res[1].Source != "omada" || res[1].Err != "" || len(res[1].Nets) != 1 {
		t.Fatalf("Omada: %+v", res[1])
	}
	if e := netCtlErrText(errors.New("kaputt")); e != "kaputt" {
		t.Errorf("anderer Fehler: %q", e)
	}
}

func TestNetCtlFetchPanic(t *testing.T) {
	// Absturz im Leser
	r := netCtlFetchUnifi(context.Background(), fakePanicReader{})
	if r.Err == "" || len(r.Nets) != 0 {
		t.Fatalf("Leser mit Absturz: %+v", r)
	}
	// Absturz schon beim Öffnen: nur diese Quelle, die andere kommt an
	h := &Handler{}
	h.netUnifiOpen = func(context.Context) (netUnifiReader, error) { panic("kaputt") }
	h.netOmadaOpen = func(context.Context) (netOmadaReader, error) { return fakeOmadaReader{}, nil }
	res := h.netCtlFetchAll(context.Background())
	if len(res) != 2 || res[0].Err == "" || res[1].Err != "" {
		t.Fatalf("Ergebnis: %+v", res)
	}
}

func TestNetSuggAdoptFlow(t *testing.T) {
	f := netSuggEnv(t)
	netSuggSeed(t, f)
	ctx := context.Background()
	// altes Kennwort am ANDERS-WLAN, Zweck/Notiz/ESPPDH am ANDERS-Netz bzw. WLAN
	enc, _ := encryptSecret(f.h.networkKey(), "Alt-Kennwort-123")
	if _, err := f.pool.Exec(ctx, `INSERT INTO net_wlans (ssid, password_enc, note, esppdh_ok, source, ext_id) VALUES ('t5w-iot', $1, 'WLAN-Notiz', true, 'unifi', 't5-w-pw')`, enc); err != nil {
		t.Fatal(err)
	}
	// 1) NEU-Netz übernehmen
	code, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "segment", "t5-n-new", netSuggFp(t, f, "segment", "t5-n-new")))
	if code != http.StatusSeeOther || !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("Netz übernehmen: %d %s", code, loc)
	}
	var src, ext string
	var adopted *time.Time
	var vlan int
	if err := f.pool.QueryRow(ctx, `SELECT source, ext_id, adopted_at, vlan_id FROM net_segments WHERE name = 'T5 Produktion'`).Scan(&src, &ext, &adopted, &vlan); err != nil {
		t.Fatal(err)
	}
	if src != "unifi" || ext != "t5-n-new" || adopted == nil || vlan != 3101 {
		t.Fatalf("Netz: %s %s %v %d", src, ext, adopted, vlan)
	}
	// 2) ANDERS-Netz angleichen: Zweck und Notiz bleiben
	_, loc = netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "segment", "t5-n-diff", netSuggFp(t, f, "segment", "t5-n-diff")))
	if !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("Netz angleichen: %s", loc)
	}
	var subnet, purpose, note string
	_ = f.pool.QueryRow(ctx, `SELECT subnet, purpose, note FROM net_segments WHERE ext_id = 't5-n-diff'`).Scan(&subnet, &purpose, &note)
	if subnet != "10.99.32.0/24" || purpose != "Zweck-alt" || note != "Notiz-alt" {
		t.Fatalf("angeglichen: %q %q %q", subnet, purpose, note)
	}
	// 3) WLAN ANDERS (Kennwort geändert, Netz neu zugeordnet): Kennwort ersetzt, Notiz und ESPPDH bleiben
	_, loc = netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "wlan", "t5-w-pw", netSuggFp(t, f, "wlan", "t5-w-pw")))
	if !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("WLAN angleichen: %s", loc)
	}
	var esp bool
	var wnote, segName string
	_ = f.pool.QueryRow(ctx, `SELECT w.esppdh_ok, w.note, COALESCE(s.name, '') FROM net_wlans w LEFT JOIN net_segments s ON s.id = w.segment_id WHERE w.ext_id = 't5-w-pw'`).Scan(&esp, &wnote, &segName)
	if !esp || wnote != "WLAN-Notiz" || segName != "T5 Produktion" {
		t.Fatalf("WLAN: esppdh %v notiz %q netz %q", esp, wnote, segName)
	}
	var id string
	_ = f.pool.QueryRow(ctx, `SELECT id::text FROM net_wlans WHERE ext_id = 't5-w-pw'`).Scan(&id)
	if _, pw, err := f.h.networkWLANSecret(ctx, id); err != nil || pw != netSuggPW {
		t.Fatalf("Kennwort nicht übernommen: %v", err)
	}
	// 4) offenes NEU-WLAN ohne Netz: ESPPDH aus, kein Kennwort
	_, loc = netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "wlan", "t5-w-open", netSuggFp(t, f, "wlan", "t5-w-open")))
	if !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("offenes WLAN: %s", loc)
	}
	var enc2 string
	_ = f.pool.QueryRow(ctx, `SELECT password_enc, esppdh_ok FROM net_wlans WHERE ext_id = 't5-w-open'`).Scan(&enc2, &esp)
	if enc2 != "" || esp {
		t.Fatalf("offenes WLAN: pw %q esppdh %v", enc2, esp)
	}
}

// WLAN, dessen Netz im PDH fehlt: das Netz wird mit angelegt (und danach nicht doppelt).
func TestNetSuggAdoptNetCreate(t *testing.T) {
	f := netSuggEnv(t)
	ctx := context.Background()
	fp := netSuggFp(t, f, "wlan", "t5-w-pw")
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "wlan", "t5-w-pw", fp)); !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("WLAN ohne Netz: %s", loc)
	}
	var n int
	var pw string
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE ext_id = 't5-n-new'`).Scan(&n)
	_ = f.pool.QueryRow(ctx, `SELECT s.name FROM net_wlans w JOIN net_segments s ON s.id = w.segment_id WHERE w.ext_id = 't5-w-pw'`).Scan(&pw)
	if n != 1 || pw != "T5 Produktion" {
		t.Fatalf("Netz mit angelegt: %d Stück, WLAN am Netz %q", n, pw)
	}
	// Netz-Zeile und WLAN-Zeile zusammen (Alle übernehmen) – kein zweites Netz, keine Dopplung
	netSuggClean(f.pool)
	f.h.netSuggForget()
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, netSuggAllForm(t, f)); !strings.Contains(loc, "n=sugg-all") {
		t.Fatalf("alle übernehmen: %s", loc)
	}
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE ext_id = 't5-n-new' OR vlan_id = 3101`).Scan(&n)
	if n != 1 {
		t.Fatalf("Netz doppelt: %d", n)
	}
}

func TestNetSuggAdoptStale(t *testing.T) {
	f := netSuggEnv(t)
	ctx := context.Background()
	count := func() int {
		var n int
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE ext_id = 't5-n-new'`).Scan(&n)
		return n
	}
	// alter Fingerabdruck (Controller hat das Netz inzwischen geändert)
	old := netSuggFp(t, f, "segment", "t5-n-new")
	(*f.ctl)[0].Nets[0].Name = "T5 Produktion neu"
	f.h.netSuggForget()
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "segment", "t5-n-new", old)); !strings.Contains(loc, "e=sugg-stale") || count() != 0 {
		t.Fatalf("alter fp: %s, %d Netze", loc, count())
	}
	// Kennung gibt es nicht
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "segment", "t5-gibtsnicht", "x")); !strings.Contains(loc, "e=sugg-stale") {
		t.Fatalf("unbekannt: %s", loc)
	}
	// leerer Speicher, Quelle mit Fehler
	f.h.netSuggForget()
	*f.ctl = []netCtlResult{{Source: "unifi", Label: "UniFi", Err: netErrTimeout}}
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, netSuggForm(t, f, "segment", "t5-n-new", old)); !strings.Contains(loc, "e=sugg-ctl") || count() != 0 {
		t.Fatalf("Quelle mit Fehler: %s", loc)
	}
	// unbekannte Quelle und Art
	f.h.netSuggForget()
	*f.ctl = netSuggDefault()
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, url.Values{"source": {"x"}, "kind": {"segment"}, "ext": {"t5-n-new"}}); !strings.Contains(loc, "e=sugg-stale") {
		t.Fatalf("Quelle x: %s", loc)
	}
	if networkErrors["sugg-stale"] == "" || networkErrors["sugg-ctl"] == "" {
		t.Fatal("Meldungen fehlen")
	}
}

func TestNetSuggAdoptIgnoresPostedValues(t *testing.T) {
	f := netSuggEnv(t)
	ctx := context.Background()
	fp := netSuggFp(t, f, "wlan", "t5-w-pw")
	form := netSuggForm(t, f, "wlan", "t5-w-pw", fp)
	for k, v := range map[string]string{"name": "Gefälscht", "ssid": "t5w-fake", "password": "Gefaelscht-1234", "vlan": "77", "subnet": "1.2.3.0/24", "esppdh_ok": "on", "note": "x"} {
		form.Set(k, v)
	}
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, form); !strings.Contains(loc, "n=sugg-adopted") {
		t.Fatalf("übernehmen: %s", loc)
	}
	var id, ssid string
	var esp bool
	if err := f.pool.QueryRow(ctx, `SELECT id::text, ssid, esppdh_ok FROM net_wlans WHERE ext_id = 't5-w-pw'`).Scan(&id, &ssid, &esp); err != nil {
		t.Fatal(err)
	}
	if _, pw, _ := f.h.networkWLANSecret(ctx, id); ssid != "t5w-iot" || pw != netSuggPW || esp {
		t.Fatalf("gespeichert: %q esppdh %v – gesendete Werte wurden verwendet", ssid, esp)
	}
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE vlan_id = 77 OR name = 'Gefälscht'`).Scan(&n)
	if n != 0 {
		t.Fatal("gesendetes Netz angelegt")
	}
}

func TestNetSuggAdoptAll(t *testing.T) {
	f := netSuggEnv(t)
	netSuggSeed(t, f)
	ctx := context.Background()
	// ein Vorschlag ist ausgeblendet
	fpOpen := netSuggFp(t, f, "wlan", "t5-w-open")
	if _, loc := netSuggPost(f, f.h.NetworkSuggHideWeb, netSuggForm(t, f, "wlan", "t5-w-open", fpOpen)); !strings.Contains(loc, "n=sugg-hidden") {
		t.Fatalf("ausblenden: %s", loc)
	}
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, netSuggAllForm(t, f)); !strings.Contains(loc, "n=sugg-all") {
		t.Fatalf("alle: %s", loc)
	}
	var segs, wl, open int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE ext_id IN ('t5-n-new','t5-n-diff')`).Scan(&segs)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_wlans WHERE ext_id = 't5-w-pw'`).Scan(&wl)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_wlans WHERE ext_id = 't5-w-open'`).Scan(&open)
	if segs != 2 || wl != 1 || open != 0 {
		t.Fatalf("Netze %d WLAN %d ausgeblendetes %d", segs, wl, open)
	}
	// Dopplung: zwei Netze mit derselben VLAN-Nummer – nichts wird gespeichert (alles oder nichts)
	netSuggClean(f.pool)
	f.h.netSuggForget()
	(*f.ctl)[0].Nets = append((*f.ctl)[0].Nets, netCtlNet{ExtID: "t5-n-dup", Name: "T5 Doppelt", VLAN: 3101, Subnet: "10.99.99.0/24"})
	_, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, netSuggAllForm(t, f))
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM net_segments WHERE ext_id LIKE 't5-%' OR name LIKE 'T5 %'`).Scan(&n)
	if !strings.Contains(loc, "e=seg-dup") || n != 0 {
		t.Fatalf("Dopplung: %s, %d Netze gespeichert", loc, n)
	}
}

func TestNetSuggHide(t *testing.T) {
	f := netSuggEnv(t)
	fp := netSuggFp(t, f, "segment", "t5-n-new")
	if _, loc := netSuggPost(f, f.h.NetworkSuggHideWeb, netSuggForm(t, f, "segment", "t5-n-new", fp)); !strings.Contains(loc, "n=sugg-hidden") {
		t.Fatalf("ausblenden: %s", loc)
	}
	body := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil).Body.String()
	if strings.Contains(body, "Netz T5 Produktion</b>") {
		t.Fatal("ausgeblendeter Vorschlag steht noch im Block")
	}
	// am Controller geändert → wieder da
	(*f.ctl)[0].Nets[0].Name = "T5 Produktion 2"
	body = itNetDo(f.h.NetworkSuggWeb, "GET", "/x?fresh=1", "", pdhSystemUserID, nil).Body.String()
	if !strings.Contains(body, "Netz T5 Produktion 2</b>") {
		t.Fatal("geänderter Vorschlag fehlt")
	}
}

// Kennwort nie in Seite, Weiterleitung oder Protokoll – beim Abruf, Übernehmen und Fehlschlagen.
func TestNetSuggNeverLeaksPassword(t *testing.T) {
	f := netSuggEnv(t)
	var buf bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&buf).Level(zerolog.DebugLevel)
	// ein WLAN mit zu langem Namen und bekanntem Kennwort: das Übernehmen scheitert und wird protokolliert
	(*f.ctl)[0].WLANs = append((*f.ctl)[0].WLANs, netCtlWLAN{ExtID: "t5-w-long", SSID: "t5w-" + strings.Repeat("x", 40), Security: unifi.SecPersonal, Password: netSuggPW})
	f.h.netSuggForget()
	t.Cleanup(func() { log.Logger = old })
	var seen []string
	body := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil)
	seen = append(seen, body.Body.String(), body.Header().Get("Location"))
	for k, v := range body.Header() {
		seen = append(seen, k+strings.Join(v, ","))
	}
	// Übernehmen (mit Kennwort) und ein Fehlschlag (alter Fingerabdruck)
	for _, form := range []url.Values{netSuggForm(t, f, "wlan", "t5-w-pw", netSuggFp(t, f, "wlan", "t5-w-pw")), netSuggForm(t, f, "wlan", "t5-w-pw", "alt")} {
		w := itNetDo(f.h.NetworkSuggAdoptWeb, "POST", "/x", "", pdhSystemUserID, form)
		seen = append(seen, w.Body.String(), w.Header().Get("Location"))
	}
	// „Alle übernehmen“ scheitert am zu langen Namen (Fehlerweg im Protokoll)
	w := itNetDo(f.h.NetworkSuggAdoptAllWeb, "POST", "/x", "", pdhSystemUserID, netSuggAllForm(t, f))
	seen = append(seen, w.Body.String(), w.Header().Get("Location"))
	seen = append(seen, buf.String())
	for i, s := range seen {
		if strings.Contains(s, netSuggPW) {
			t.Fatalf("Kennwort in Ausgabe %d", i)
		}
	}
	if !strings.Contains(buf.String(), "vom controller übernommen") {
		t.Fatalf("Protokoll ohne Übernahme: %s", buf.String())
	}
	if !strings.Contains(w.Header().Get("Location"), "e=ssid") || !strings.Contains(buf.String(), "\"level\":\"error\"") {
		t.Fatalf("Fehlerweg nicht durchlaufen: %s %s", w.Header().Get("Location"), buf.String())
	}
}

func TestNetSuggMirror(t *testing.T) {
	f := netSuggEnv(t)
	f.h.SetMirror(MirrorInfo{On: true})
	f.h.mirrorPeerFetch = func(context.Context) (haStatus, error) {
		return haStatus{Configured: true, Peers: []haNode{{Role: "aktiv", Seen: true, Paths: []haPath{{Addr: "10.10.0.17:7946"}}}}}, nil
	}
	body := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil).Body.String()
	if f.calls.Load() != 0 {
		t.Fatal("Spiegel fragt den Controller ab")
	}
	if !strings.Contains(body, "Vorschläge übernehmen am aktiven Server") || !strings.Contains(body, `href="http://10.10.0.17/admin/network"`) {
		t.Fatalf("Text oder Verweis fehlt: %s", body)
	}
	hd := f.h.MirrorMiddleware(http.HandlerFunc(f.h.NetworkSuggAdoptWeb))
	if rec := mirrorServe(hd, http.MethodPost, "/admin/network/suggestions/adopt", nil); rec.Code != http.StatusConflict {
		t.Fatalf("POST am Spiegel: %d", rec.Code)
	}
}

func TestNetSuggRights(t *testing.T) {
	called := false
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	h.netCtlFetchHook = func(context.Context) []netCtlResult { called = true; return nil }
	for name, fn := range map[string]http.HandlerFunc{"Block": h.NetworkSuggWeb, "übernehmen": h.NetworkSuggAdoptWeb,
		"alle": h.NetworkSuggAdoptAllWeb, "ausblenden": h.NetworkSuggHideWeb} {
		method := "POST"
		if name == "Block" {
			method = "GET"
		}
		if w := itNetDo(fn, method, "/x", "", pdhSystemUserID, url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", name, w.Code)
		}
	}
	if called {
		t.Fatal("Controller ohne Recht abgefragt")
	}
}

// Gefälschte Formulare ohne Art, Kennung oder mit unbekannten Werten: „veraltet“, kein Absturz, nichts gespeichert.
func TestNetSuggForgedForm(t *testing.T) {
	f := netSuggEnv(t)
	for name, fn := range map[string]http.HandlerFunc{"übernehmen": f.h.NetworkSuggAdoptWeb, "ausblenden": f.h.NetworkSuggHideWeb} {
		for _, form := range []url.Values{
			{"source": {"unifi"}},
			{"source": {"unifi"}, "kind": {"segment"}},
			{"source": {"unifi"}, "kind": {"x"}, "ext": {"t5-n-new"}},
			{"source": {"zzz"}, "kind": {"segment"}, "ext": {"t5-n-new"}},
			{},
		} {
			if _, loc := netSuggPost(f, fn, form); !strings.Contains(loc, "e=sugg-stale") {
				t.Errorf("%s %v: %s", name, form, loc)
			}
		}
	}
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, url.Values{"source": {"unifi"}}); !strings.Contains(loc, "e=sugg-stale") {
		t.Errorf("alle ohne Satz: %s", loc)
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM net_segments WHERE ext_id LIKE 't5-%') + (SELECT count(*) FROM net_hidden_suggestions WHERE ext_id LIKE 't5-%')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d Einträge gespeichert", n)
	}
}

// „Alle übernehmen“ gilt nur für den gezeigten Satz: hat sich der Controller seither geändert, wird nichts übernommen.
func TestNetSuggAdoptAllSetChanged(t *testing.T) {
	f := netSuggEnv(t)
	body := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil).Body.String()
	shown := netSuggAllForm(t, f)
	if !strings.Contains(body, `name="set" value="`+shown.Get("set")+`"`) {
		t.Fatal("Satz-Fingerabdruck fehlt im Block")
	}
	// ein weiteres Netz erscheint am Controller (z. B. „Neu abfragen“ eines anderen Benutzers)
	(*f.ctl)[0].Nets = append((*f.ctl)[0].Nets, netCtlNet{ExtID: "t5-n-extra", Name: "T5 Extra", VLAN: 3150})
	f.h.netSuggForget()
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, shown); !strings.Contains(loc, "e=sugg-stale") {
		t.Fatalf("alter Satz: %s", loc)
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM net_segments WHERE ext_id LIKE 't5-%') + (SELECT count(*) FROM net_wlans WHERE ext_id LIKE 't5-%')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d Einträge gespeichert", n)
	}
	// mit dem neuen Satz geht es
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptAllWeb, netSuggAllForm(t, f)); !strings.Contains(loc, "n=sugg-all") {
		t.Fatalf("neuer Satz: %s", loc)
	}
}

// Ein NEU-Klick darf keinen PDH-Eintrag ändern, der inzwischen entstanden ist (Zustand oder PDH-Eintrag anders).
func TestNetSuggAdoptStateChanged(t *testing.T) {
	f := netSuggEnv(t)
	form := netSuggForm(t, f, "segment", "t5-n-new", netSuggFp(t, f, "segment", "t5-n-new"))
	if form.Get("state") != netStateNew {
		t.Fatalf("Zustand %q", form.Get("state"))
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO net_segments (name, vlan_id, subnet) VALUES ('T5 Zwischen', 3101, '10.1.1.0/24')`); err != nil {
		t.Fatal(err)
	}
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, form); !strings.Contains(loc, "e=sugg-stale") {
		t.Fatalf("Zustand geändert: %s", loc)
	}
	var name, src string
	_ = f.pool.QueryRow(context.Background(), `SELECT name, source FROM net_segments WHERE vlan_id = 3101`).Scan(&name, &src)
	if name != "T5 Zwischen" || src != "" {
		t.Fatalf("PDH-Eintrag verändert: %q %q", name, src)
	}
	// gefälschter Zustand
	form = netSuggForm(t, f, "segment", "t5-n-new", netSuggFp(t, f, "segment", "t5-n-new"))
	form.Set("state", netStateNew)
	form.Set("pdh", "")
	if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, form); !strings.Contains(loc, "e=sugg-stale") {
		t.Fatalf("Zustand gefälscht: %s", loc)
	}
}

// Ändern sich die Einstellungen von UniFi oder Omada, gilt der Speicher nicht mehr.
func TestNetSuggCacheConfigChange(t *testing.T) {
	f := netSuggEnv(t)
	ctx := context.Background()
	old := f.h.certSetting(ctx, keyUnifiSite)
	t.Cleanup(func() { _ = f.h.certPutSetting(context.Background(), keyUnifiSite, old) })
	get := func() { itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil) }
	get()
	get()
	if f.calls.Load() != 1 {
		t.Fatalf("Speicher: %d Abrufe", f.calls.Load())
	}
	if err := f.h.certPutSetting(ctx, keyUnifiSite, old+"-anders"); err != nil {
		t.Fatal(err)
	}
	get()
	if f.calls.Load() != 2 {
		t.Fatalf("nach Änderung der Einstellungen: %d Abrufe", f.calls.Load())
	}
}

// Kennwort beim Angleichen: ohne Kennwort vom Controller bleibt das gespeicherte, wird das WLAN offen, entfällt es.
func TestNetSuggAdoptPasswordCases(t *testing.T) {
	f := netSuggEnv(t)
	ctx := context.Background()
	enc, _ := encryptSecret(f.h.networkKey(), "Alt-Kennwort-123")
	for _, q := range [][2]string{{"t5w-a", "t5-w-a"}, {"t5w-b", "t5-w-b"}} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO net_wlans (ssid, password_enc, source, ext_id) VALUES ($1, $2, 'unifi', $3)`, q[0], enc, q[1]); err != nil {
			t.Fatal(err)
		}
	}
	*f.ctl = []netCtlResult{{Source: "unifi", Label: "UniFi", At: time.Now(), WLANs: []netCtlWLAN{
		{ExtID: "t5-w-a", SSID: "t5w-a", Security: unifi.SecPersonal, Hidden: true}, // Kennwort nicht herausgegeben
		{ExtID: "t5-w-b", SSID: "t5w-b", Security: unifi.SecOpen},
	}}}
	f.h.netSuggForget()
	for _, ext := range []string{"t5-w-a", "t5-w-b"} {
		form := netSuggForm(t, f, "wlan", ext, netSuggFp(t, f, "wlan", ext))
		if form.Get("state") != netStateDiff {
			t.Fatalf("%s: Zustand %q", ext, form.Get("state"))
		}
		if _, loc := netSuggPost(f, f.h.NetworkSuggAdoptWeb, form); !strings.Contains(loc, "n=sugg-adopted") {
			t.Fatalf("%s: %s", ext, loc)
		}
	}
	var a, b string
	var hidden bool
	_ = f.pool.QueryRow(ctx, `SELECT password_enc, hidden FROM net_wlans WHERE ext_id = 't5-w-a'`).Scan(&a, &hidden)
	_ = f.pool.QueryRow(ctx, `SELECT password_enc FROM net_wlans WHERE ext_id = 't5-w-b'`).Scan(&b)
	if a != enc || !hidden {
		t.Errorf("ohne Kennwort vom Controller: Kennwort unverändert %v, versteckt %v", a == enc, hidden)
	}
	if b != "" {
		t.Errorf("offen gewordenes WLAN hat noch ein Kennwort")
	}
}

// Ein gleicher Eintrag heißt „1 Eintrag …“ (Einzahl); ein Lesefehler der Datenbank ergibt eine Zeile statt Fehlerseite.
func TestNetSuggSingularAndDBError(t *testing.T) {
	f := netSuggEnv(t)
	*f.ctl = []netCtlResult{{Source: "unifi", Label: "UniFi", At: time.Now(), Nets: []netCtlNet{{ExtID: "t5-n-s1", Name: "T5 Gleich 1", VLAN: 3111, Subnet: "10.99.41.0/24"}}}}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id) VALUES ('T5 Gleich 1', 3111, '10.99.41.0/24', 'unifi', 't5-n-s1')`); err != nil {
		t.Fatal(err)
	}
	f.h.netSuggForget()
	body := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil).Body.String()
	if !strings.Contains(body, "1 Eintrag stimmt schon überein.") || strings.Contains(body, "1 Einträge") {
		t.Errorf("Einzahl fehlt: %s", body)
	}
	bad, err := pgxpool.New(context.Background(), os.Getenv("PDH_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Close()
	f.h.db = bad
	w := itNetDo(f.h.NetworkSuggWeb, "GET", "/x", "", pdhSystemUserID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "konnten nicht gelesen werden") {
		t.Fatalf("DB-Fehler: %d %s", w.Code, w.Body.String())
	}
}
