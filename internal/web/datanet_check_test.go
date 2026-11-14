package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

// ── Access controls ──────────────────────────────────────────────────────

const dnTestPolicy = `{
  // Kommentar
  "tagOwners": { "tag:pdh": ["autogroup:admin"], "tag:pdh-data": ["autogroup:admin"], },
  "acls": [
    { "action": "accept", "src": ["tag:pdh"], "dst": ["tag:pdh-data:1433,3306", ], },
    { "action": "accept", "src": ["tag:pdh-data"], "dst": ["tag:pdh:8443"] },
  ],
}`

func TestTSPolicyAllows(t *testing.T) {
	p, err := parseTSPolicy([]byte(dnTestPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if !p.hasOwner("tag:pdh") || !p.hasOwner("tag:pdh-data") || p.hasOwner("tag:x") {
		t.Errorf("tagOwners: %v", p.TagOwners)
	}
	for _, c := range []struct {
		src, dst string
		port     int
		want     bool
	}{
		{"tag:pdh", "tag:pdh-data", 1433, true},
		{"tag:pdh", "tag:pdh-data", 3306, true},
		{"tag:pdh", "tag:pdh-data", 5432, false},
		{"tag:pdh-data", "tag:pdh", 8443, true},
		{"tag:pdh-data", "tag:pdh", 8444, false},
		{"tag:pdh-data", "tag:pdh-data", 1433, false},
	} {
		if v, wide := p.allows(c.src, c.dst, c.port); (v == tsYes) != c.want || wide {
			t.Errorf("%s → %s:%d: verdict=%v wide=%v, erwartet %v", c.src, c.dst, c.port, v, wide, c.want)
		}
	}
}

func TestTSPolicyWildcardAndGrants(t *testing.T) {
	p, err := parseTSPolicy([]byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, wide := p.allows("tag:pdh", "tag:pdh-data", 1433); v != tsYes || !wide {
		t.Errorf("alles erlaubt: %v wide=%v", v, wide)
	}
	g, err := parseTSPolicy([]byte(`{"grants":[
		{"src":["tag:pdh"],"dst":["tag:pdh-data"],"ip":["tcp:1433","icmp:*"]},
		{"src":["tag:pdh-data"],"dst":["tag:pdh"],"ip":["8000-9000"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := g.allows("tag:pdh", "tag:pdh-data", 1433); v != tsYes {
		t.Error("Grant tcp:1433 nicht erkannt")
	}
	if v, _ := g.allows("tag:pdh", "tag:pdh-data", 3306); v == tsYes {
		t.Error("Grant erlaubt 3306 zu Unrecht")
	}
	if v, _ := g.allows("tag:pdh-data", "tag:pdh", 8443); v != tsYes {
		t.Error("Portbereich nicht erkannt")
	}
	if _, err := parseTSPolicy([]byte(`{kaputt`)); err == nil {
		t.Error("ungültige Richtlinie ohne Fehler")
	}
}

func TestDnMissingACL(t *testing.T) {
	got := dnMissingACL([]string{"tag:pdh"}, []int{1433, 3306}, true)
	for _, want := range []string{`"tagOwners": { "tag:pdh": ["autogroup:admin"] }`, `"dst": ["tag:pdh-data:1433,3306"]`, `"dst": ["tag:pdh:8443"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q fehlt in\n%s", want, got)
		}
	}
	if strings.Contains(got, "tag:pdh-data\": [") {
		t.Errorf("tagOwners für vorhandenen Tag: %s", got)
	}
	if dnMissingACL(nil, nil, false) != "" {
		t.Error("nichts fehlt, aber Text vorhanden")
	}
}

// ── Tailscale-API ────────────────────────────────────────────────────────

func TestTSAPIDevicesPolicyTagsAndErrors(t *testing.T) {
	var mu sync.Mutex
	var gotTags []byte
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tskey-api-x" {
			http.Error(w, "nein", http.StatusUnauthorized)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "geheimer Fehlertext", status)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/tailnet/-/devices":
			io.WriteString(w, `{"devices":[{"id":"123","name":"pdh-srv.tail.ts.net","hostname":"pdh-srv","addresses":["100.64.0.1"],"tags":["tag:pdh"],"connectedToControl":true}]}`)
		case r.Method == "GET" && r.URL.Path == "/tailnet/-/acl":
			if !strings.Contains(r.Header.Get("Accept"), "hujson") {
				t.Errorf("Accept: %q", r.Header.Get("Accept"))
			}
			io.WriteString(w, dnTestPolicy)
		case r.Method == "POST" && r.URL.Path == "/device/123/tags":
			gotTags, _ = io.ReadAll(r.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &tsAPI{base: srv.URL, token: "tskey-api-x", hc: srv.Client()}
	ctx := context.Background()
	devs, err := a.devices(ctx)
	if err != nil || len(devs) != 1 || devs[0].short() != "pdh-srv" || !devs[0].Online || devs[0].ident() != "123" {
		t.Fatalf("Geräte: %+v %v", devs, err)
	}
	if p, err := a.policy(ctx); err != nil || !p.hasOwner("tag:pdh") {
		t.Fatalf("Access controls: %v", err)
	}
	if err := a.setTags(ctx, "123", []string{"tag:pdh"}); err != nil {
		t.Fatal(err)
	}
	var body struct{ Tags []string }
	json.Unmarshal(gotTags, &body)
	if len(body.Tags) != 1 || body.Tags[0] != "tag:pdh" {
		t.Errorf("gesendet: %s", gotTags)
	}
	// Fehler: feste Texte, nie die Antwort des Servers.
	bad := &tsAPI{base: srv.URL, token: "falsch", hc: srv.Client()}
	_, err = bad.devices(ctx)
	if txt := tsAPIErrText(err); !strings.Contains(txt, "lehnt den API-Token ab") {
		t.Errorf("401: %q", txt)
	}
	for code, want := range map[int]string{403: "darf das nicht", 404: "kennt dieses Gerät nicht", 500: "abgelehnt"} {
		mu.Lock()
		status = code
		mu.Unlock()
		_, err := a.devices(ctx)
		txt := tsAPIErrText(err)
		if !strings.Contains(txt, want) || strings.Contains(txt, "geheim") {
			t.Errorf("%d: %q", code, txt)
		}
	}
	srv.Close()
	_, err = a.devices(ctx)
	if txt := tsAPIErrText(err); !strings.Contains(txt, "nicht erreichbar") {
		t.Errorf("Netz: %q", txt)
	}
	var e *tsAPIError
	if !errors.As(err, &e) || e.Kind != "net" {
		t.Errorf("Fehlerart: %v", err)
	}
}

func TestTSAPIDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	a := newTSAPI(srv.URL, "t")
	if _, err := a.devices(context.Background()); err == nil {
		t.Error("Umleitung ohne Fehler")
	}
	if leaked {
		t.Error("Token an anderen Host gesendet")
	}
}

func TestMatchDevice(t *testing.T) {
	devs := []tsDevice{
		{ID: "1", Hostname: "pdh-a", Addresses: []string{"100.64.0.1"}},
		{ID: "2", Hostname: "Werk-Sued", Name: "werk-sued.x.ts.net", Addresses: []string{"100.64.0.9"}},
	}
	if d := matchDevice(devs, []string{"100.64.0.9"}); d == nil || d.ID != "2" {
		t.Errorf("über Adresse: %v", d)
	}
	if d := matchDevice(devs, []string{"100.64.9.9"}); d != nil {
		t.Errorf("über Name zugeordnet: %v", d)
	}
	if d := guessDevice(devs, []string{"PDH-A"}); d == nil || d.ID != "1" {
		t.Errorf("Name raten: %v", d)
	}
	if d := matchDevice(devs, []string{""}); d != nil {
		t.Errorf("leere Angaben passen: %v", d)
	}
}

// ── Prüflogik mit der Attrappe ───────────────────────────────────────────

func dnSilentListener(t *testing.T, closeAtOnce bool) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if closeAtOnce {
				c.Close()
			} else {
				t.Cleanup(func() { c.Close() })
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func dnFast(t *testing.T) {
	t.Helper()
	oldT, oldS := dnProbeTimeout, dnProbeSettle
	dnProbeTimeout, dnProbeSettle = time.Second, 80*time.Millisecond
	t.Cleanup(func() { dnProbeTimeout, dnProbeSettle = oldT, oldS })
}

func dnLines(c dnCheck) string {
	var b strings.Builder
	for _, g := range c.Groups {
		b.WriteString("## " + g.Title + "\n")
		for _, l := range g.Lines {
			b.WriteString(l.Level + ": " + l.Title + " | " + l.Text + " | " + l.Hint + "\n")
		}
	}
	return b.String()
}

func dnFakeInput(t *testing.T, f *nodetest.Fake) dnInput {
	t.Helper()
	return dnInput{
		Now:    time.Now(),
		Status: datanet.Status{State: datanet.StateRunning, Addrs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}},
		Node:   f,
		Servers: []dnServer{
			{Name: "srv", Addrs: []string{"100.64.0.1"}, Self: true},
			{Name: "spiegel", Addrs: []string{"100.64.0.2"}, Seen: time.Now().Add(-2 * time.Minute)},
			{Name: "nochnie", HasKey: true},
		},
	}
}

func TestCheckReportsWrongTagsAndPorts(t *testing.T) {
	dnFast(t)
	open, closed := dnSilentListener(t, false), dnSilentListener(t, true)
	f := &nodetest.Fake{
		Tags: map[string][]string{
			"100.64.0.1": {"tag:pdh-test1"},
			"100.64.0.2": {datanet.TagPDH},
			"100.64.0.9": {"tag:pdh-test1"},
		},
		PeerErr: map[string]error{"100.64.0.8": errors.New("unbekannt")},
		Routes: map[string]string{
			"100.64.0.9:" + strconv.Itoa(1433): "127.0.0.1:" + strconv.Itoa(open),
			"100.64.0.9:" + strconv.Itoa(1434): "127.0.0.1:" + strconv.Itoa(closed),
		},
	}
	in := dnFakeInput(t, f)
	in.Clients = []dataNetClient{
		{Name: "Werk Süd", Addr: "100.64.0.9", Hostname: "werk", LastSeen: time.Now().Add(-30 * time.Second),
			Targets: []datanet.TargetStatus{{Port: 1433, OK: true}, {Port: 1434, OK: true}, {Port: 1435, OK: false, Error: "Zeitüberschreitung"}}},
		{Name: "Unsichtbar", Addr: "100.64.0.8", LastSeen: time.Now().Add(-time.Minute), Targets: []datanet.TargetStatus{{Port: 1433, OK: true}}},
		{Name: "Alt", Addr: "100.64.0.7", LastSeen: time.Now().Add(-3 * time.Hour)},
		{Name: "Neu"},
		{Name: "Weg", Revoked: true},
	}
	c := in.run(context.Background())
	out := dnLines(c)
	for _, want := range []string{
		"bad: Falscher Tag | Gerät trägt tag:pdh-test1 statt tag:pdh – der Client lässt es nicht herein.",
		"ok: Trägt tag:pdh",                  // der Spiegelserver
		"ok: spiegel: gemeldet",              // meldet sich
		"warn: nochnie: noch nicht gemeldet", // nur Schlüssel hinterlegt
		"Gerät trägt tag:pdh-test1 statt tag:pdh-data",
		"ok: Port 1433 über das Datennetz erreichbar",
		"warn: Port 1434: Verbindung wird sofort geschlossen",
		"bad: Port 1435 nicht erreichbar",
		"warn: Port 1435: Datenbank hinter dem Client nicht erreichbar | Zeitüberschreitung",
		"bad: Im Datennetz nicht zu sehen",
		"bad: Letzte Meldung vor 3 Std.",
		"warn: Noch keine Meldung",
		"info: Gesperrt",
		"info: Kein API-Token hinterlegt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt in\n%s", want, out)
		}
	}
	if c.Bad == 0 || c.Warn == 0 || c.HaveAPI || len(c.Plan) != 0 {
		t.Errorf("Zähler/Plan: bad=%d warn=%d api=%v plan=%v", c.Bad, c.Warn, c.HaveAPI, c.Plan)
	}
}

func TestCheckStatesOfThisServer(t *testing.T) {
	for st, want := range map[datanet.State]string{
		datanet.StateOff:      "warn: Datennetz ausgeschaltet",
		datanet.StateNeedsKey: "bad: Schlüssel fehlt",
		datanet.StateError:    "bad: Anmeldung im Datennetz fehlgeschlagen",
		datanet.StateStarting: "warn: Verbindet noch",
	} {
		in := dnInput{Now: time.Now(), Status: datanet.Status{State: st}, Servers: []dnServer{{Name: "srv", Self: true}}}
		if out := dnLines(in.run(context.Background())); !strings.Contains(out, want) {
			t.Errorf("%s: %q fehlt in\n%s", st, want, out)
		}
	}
	in := dnInput{Now: time.Now(), Status: datanet.Status{State: datanet.StateRunning}, Node: &nodetest.Fake{}, Servers: []dnServer{{Name: "srv", Self: true}}}
	if out := dnLines(in.run(context.Background())); !strings.Contains(out, "bad: Keine Adresse") {
		t.Errorf("keine Adresse:\n%s", out)
	}
	// Gesund: Knoten, Adresse und Tag stimmen.
	f := &nodetest.Fake{Tags: map[string][]string{"100.64.0.1": {datanet.TagPDH}}}
	c := dnFakeInput(t, f).run(context.Background())
	if !strings.Contains(dnLines(c), "ok: Knoten ist im Datennetz") || !strings.Contains(dnLines(c), "ok: Trägt tag:pdh") {
		t.Errorf("gesunder Server:\n%s", dnLines(c))
	}
}

func dnAPIInput(t *testing.T, policy string, devs []tsDevice) dnInput {
	t.Helper()
	f := &nodetest.Fake{Tags: map[string][]string{"100.64.0.1": {"tag:pdh-test1"}, "100.64.0.9": {"tag:pdh-test1"}}}
	in := dnFakeInput(t, f)
	in.Servers = in.Servers[:1]
	in.Clients = []dataNetClient{{Name: "Werk", Addr: "100.64.0.9", Hostname: "werk", LastSeen: time.Now(), Targets: []datanet.TargetStatus{{Port: 1433, OK: true}}}}
	in.HaveAPI, in.Devices = true, devs
	if policy != "" {
		p, err := parseTSPolicy([]byte(policy))
		if err != nil {
			t.Fatal(err)
		}
		in.Policy = p
	}
	return in
}

var dnTestDevices = []tsDevice{
	{ID: "d1", Hostname: "pdh-srv", Addresses: []string{"100.64.0.1"}, Tags: []string{"tag:pdh-test1", "tag:keep"}, Online: true},
	{ID: "d2", Hostname: "werk", Addresses: []string{"100.64.0.9"}, Tags: []string{"tag:pdh-test1"}, Online: true},
}

func TestCheckWithAPIPlansTagFix(t *testing.T) {
	dnFast(t)
	in := dnAPIInput(t, dnTestPolicy, dnTestDevices)
	c := in.run(context.Background())
	if len(c.Plan) != 2 {
		t.Fatalf("Plan: %+v (%v)", c.Plan, c.PlanNotes)
	}
	for _, it := range c.Plan {
		switch it.ID {
		case "d1":
			if it.Want != datanet.TagPDH || strings.Join(it.New, ",") != "tag:pdh-test1,tag:keep,tag:pdh" {
				t.Errorf("Server: %+v", it)
			}
		case "d2":
			if it.Want != datanet.TagClient || strings.Join(it.New, ",") != "tag:pdh-test1,tag:pdh-data" {
				t.Errorf("Client: %+v", it)
			}
		}
	}
	out := dnLines(c)
	for _, want := range []string{"ok: Geräteliste gelesen (2 Geräte)", "ok: tag:pdh steht in tagOwners", "ok: Freigabe tag:pdh → tag:pdh-data erlaubt", "ok: Freigabe tag:pdh-data → tag:pdh:8443 erlaubt", "ok: Laut Tailscale online"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt in\n%s", want, out)
		}
	}
	if c.Missing != "" {
		t.Errorf("Missing: %s", c.Missing)
	}
}

func TestCheckWithAPIMissingTagOwnersBlocksFix(t *testing.T) {
	dnFast(t)
	in := dnAPIInput(t, `{"tagOwners":{"tag:pdh":["autogroup:admin"]},"acls":[]}`, dnTestDevices)
	c := in.run(context.Background())
	if len(c.Plan) != 1 || c.Plan[0].ID != "d1" {
		t.Fatalf("Plan: %+v", c.Plan)
	}
	if len(c.PlanNotes) != 1 || !strings.Contains(c.PlanNotes[0], "tag:pdh-data steht nicht in den tagOwners") {
		t.Errorf("Hinweise: %v", c.PlanNotes)
	}
	out := dnLines(c)
	for _, want := range []string{"bad: tag:pdh-data fehlt in tagOwners", "bad: Freigabe tag:pdh → tag:pdh-data fehlt", "bad: Freigabe tag:pdh-data → tag:pdh:8443 fehlt"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt in\n%s", want, out)
		}
	}
	for _, want := range []string{`"tag:pdh-data": ["autogroup:admin"]`, `tag:pdh-data:1433`, `tag:pdh:8443`} {
		if !strings.Contains(c.Missing, want) {
			t.Errorf("Missing ohne %q: %s", want, c.Missing)
		}
	}
}

func TestCheckWithAPIEverythingAllowed(t *testing.T) {
	dnFast(t)
	in := dnAPIInput(t, `{"tagOwners":{"tag:pdh":[],"tag:pdh-data":[]},"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`, dnTestDevices)
	c := in.run(context.Background())
	out := dnLines(c)
	if !strings.Contains(out, "ok: Freigabe tag:pdh → tag:pdh-data erlaubt | Ports 1433 – alles erlaubt") || c.Missing != "" {
		t.Errorf("alles erlaubt:\n%s\nMissing %q", out, c.Missing)
	}
}

func TestCheckWithAPIErrorAndOffline(t *testing.T) {
	dnFast(t)
	in := dnAPIInput(t, "", dnTestDevices)
	in.HaveAPI, in.Devices, in.APIErr = true, nil, tsAPIErrText(&tsAPIError{Kind: "auth"})
	c := in.run(context.Background())
	if !strings.Contains(dnLines(c), "bad: Geräteliste nicht lesbar | Tailscale lehnt den API-Token ab") || len(c.Plan) != 0 {
		t.Errorf("API-Fehler:\n%s", dnLines(c))
	}
	in = dnAPIInput(t, dnTestPolicy, []tsDevice{{ID: "d2", Hostname: "werk", Addresses: []string{"100.64.0.9"}, Tags: []string{datanet.TagClient}, Online: false}})
	out := dnLines(in.run(context.Background()))
	if !strings.Contains(out, "bad: Laut Tailscale offline") || !strings.Contains(out, "warn: Nicht in der Geräteliste von Tailscale") {
		t.Errorf("offline/fehlt:\n%s", out)
	}
	in = dnAPIInput(t, dnTestPolicy, dnTestDevices)
	in.Policy, in.PolicyErr = nil, tsAPIErrText(&tsAPIError{Kind: "forbidden"})
	c = in.run(context.Background())
	if !strings.Contains(dnLines(c), "bad: Access controls nicht lesbar") || len(c.Plan) != 0 || len(c.PlanNotes) == 0 {
		t.Errorf("ohne Richtlinie plant das PDH nichts:\n%s %v", dnLines(c), c.Plan)
	}
}

// ── Seiten, Rechte, Speicherung ──────────────────────────────────────────

func TestDataNetCheckRoutesRequirePermission(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	if w := dnDo(h.DataNetCheck, "GET", "/datanet/check", nil, nil); w.Code != http.StatusForbidden || asked[len(asked)-1] != permDataNetView {
		t.Errorf("Prüfung: %d %v", w.Code, asked)
	}
	if w := dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{}); w.Code != http.StatusForbidden || asked[len(asked)-1] != permDataNetAdmin {
		t.Errorf("Korrigieren: %d %v", w.Code, asked)
	}
	// datanet.view und datanet.edit genügen zum Korrigieren nicht.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permDataNetAdmin }
	if w := dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Korrigieren ohne admin: %d", w.Code)
	}
	if _, perm, ref, _, hit := matchGate("POST", "/datanet/check/fix"); !hit || perm != permDataNetAdmin || ref != "" {
		t.Errorf("gateRules: Treffer %v Recht %q Typ %q", hit, perm, ref)
	}
	if _, _, _, _, hit := matchGate("GET", "/datanet/check"); hit {
		t.Error("Lesen der Prüfung steht in gateRules")
	}
}

// dnRunningHandler: Handler mit laufendem Knoten (Attrappe) und der Tailscale-Attrappe.
func dnRunningHandler(t *testing.T, f *nodetest.Fake, api http.Handler) (*Handler, *httptest.Server) {
	t.Helper()
	h := dnPageHandler(t)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	h.dataNetAPIBase = srv.URL
	h.dataNetActive = true
	m := datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) { return f, nil }, time.Minute)
	m.Start(datanet.NodeConfig{Dir: t.TempDir(), AuthKey: "tskey-auth-x"}, nil)
	t.Cleanup(m.Stop)
	for i := 0; m.Status().State != datanet.StateRunning; i++ {
		if i > 200 {
			t.Fatal("Knoten läuft nicht")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.dataNet = m
	ctx := context.Background()
	if err := h.dataNetSetAPIToken(ctx, "tskey-api-geheim"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, dataNetKeyAPIToken)
		h.db.Exec(context.Background(), `DELETE FROM datanet_events WHERE action = 'fix_tags' AND note LIKE '%pdh-checktest%'`)
	})
	return h, srv
}

func TestDataNetCheckPageAndFixTags(t *testing.T) {
	dnFast(t)
	f := &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.7.1")},
		Tags: map[string][]string{"100.64.7.1": {"tag:pdh-test1"}}}
	var mu sync.Mutex
	posted := map[string]string{}
	var h *Handler
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tskey-api-geheim" {
			http.Error(w, "nein", 401)
			return
		}
		switch {
		case r.URL.Path == "/tailnet/-/devices":
			io.WriteString(w, `{"devices":[{"id":"dev-1","hostname":"pdh-checktest","addresses":["100.64.7.1"],"tags":["tag:pdh-test1"],"connectedToControl":true}]}`)
		case r.URL.Path == "/tailnet/-/acl":
			io.WriteString(w, dnTestPolicy)
		case r.Method == "POST" && r.URL.Path == "/device/dev-1/tags":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			posted["dev-1"] = string(b)
			mu.Unlock()
		default:
			http.NotFound(w, r)
		}
	})
	h, _ = dnRunningHandler(t, f, api)

	w := dnDo(h.DataNetCheck, "GET", "/datanet/check", nil, nil)
	if w.Code != 200 {
		t.Fatalf("Prüfung: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Falscher Tag", "tag:pdh-test1 statt tag:pdh", "der Client lässt es nicht herein", "Tags korrigieren", "hx-confirm"} {
		if !strings.Contains(body, want) {
			t.Errorf("Prüfseite ohne %q", want)
		}
	}
	if strings.Contains(body, "tskey-api-geheim") {
		t.Error("Token auf der Seite")
	}

	// Nur ansehen: Prüfung ja, Korrigieren weder angeboten noch erlaubt.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permDataNetView }
	if w := dnDo(h.DataNetCheck, "GET", "/datanet/check", nil, nil); w.Code != 200 || strings.Contains(w.Body.String(), "hx-post") {
		t.Errorf("nur view: %d, Knopf sichtbar=%v", w.Code, strings.Contains(w.Body.String(), "hx-post"))
	}
	if w := dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("view darf nicht korrigieren: %d", w.Code)
	}
	// Spiegelserver: gesperrt.
	h.hasPermHook = func(*http.Request, string) bool { return true }
	h.mirror = MirrorInfo{On: true}
	if w := dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{}); !strings.Contains(w.Body.String(), "Spiegelserver") {
		t.Errorf("Spiegel: %s", w.Body.String())
	}
	if w := dnDo(h.DataNetCheck, "GET", "/datanet/check", nil, nil); strings.Contains(w.Body.String(), "hx-post") || !strings.Contains(w.Body.String(), "Spiegelserver") {
		t.Error("Spiegel: Knopf sichtbar oder Hinweis fehlt")
	}
	mu.Lock()
	if len(posted) != 0 {
		t.Errorf("Tags trotz Sperre geändert: %v", posted)
	}
	mu.Unlock()
	h.mirror = MirrorInfo{}

	// Admin: die Tags werden gesetzt, nichts aus der Anfrage zählt.
	w = dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{"id": {"fremd"}, "tag": {"tag:evil"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pdh-checktest") || !strings.Contains(w.Body.String(), "tag:pdh ergänzt") {
		t.Fatalf("Korrigieren: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	got := posted["dev-1"]
	mu.Unlock()
	if got != `{"tags":["tag:pdh-test1","tag:pdh"]}` {
		t.Errorf("an Tailscale gesendet: %s", got)
	}
	var n int
	h.db.QueryRow(context.Background(), `SELECT count(*) FROM datanet_events WHERE action = 'fix_tags' AND note LIKE '%pdh-checktest → tag:pdh%'`).Scan(&n)
	if n != 1 {
		t.Errorf("Ereignisse: %d", n)
	}
}

func TestDataNetCheckFixWithoutTokenOrOnError(t *testing.T) {
	f := &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.7.1")}}
	h, _ := dnRunningHandler(t, f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nein", 401) }))
	w := dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{})
	if !strings.Contains(w.Body.String(), "lehnt den API-Token ab") || strings.Contains(w.Body.String(), "nein") {
		t.Errorf("401: %s", w.Body.String())
	}
	if w := dnDo(h.DataNetCheck, "GET", "/datanet/check", nil, nil); !strings.Contains(w.Body.String(), "Geräteliste nicht lesbar") {
		t.Errorf("Prüfung bei 401: %s", w.Body.String())
	}
	if err := h.dataNetSetAPIToken(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	w = dnDo(h.DataNetFixTags, "POST", "/datanet/check/fix", nil, url.Values{})
	if !strings.Contains(w.Body.String(), "kein API-Token hinterlegt") {
		t.Errorf("ohne Token: %s", w.Body.String())
	}
}

func TestDataNetSettingsAPIToken(t *testing.T) {
	h := dnPageHandler(t)
	ctx := context.Background()
	t.Cleanup(func() {
		h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key = ANY($1)`, []string{dataNetKeyAPIToken, dataNetKeyEnabled})
	})
	t.Cleanup(h.StopDataNet)
	post := func(v url.Values) *httptest.ResponseRecorder {
		return dnDo(h.DataNetSettings, "POST", "/datanet/settings", nil, v)
	}
	// Ein falscher Wert ändert nichts.
	if w := post(url.Values{"apitoken": {"geheim"}}); !strings.Contains(w.Header().Get("Location"), "e=apitoken") {
		t.Errorf("falscher Token: %d %q", w.Code, w.Header().Get("Location"))
	}
	if h.dataNetAPIToken(ctx) != "" {
		t.Error("ungültiger Token gespeichert")
	}
	if w := post(url.Values{"apitoken": {"tskey-api-abc123"}}); !strings.Contains(w.Header().Get("Location"), "n=saved") {
		t.Fatalf("speichern: %d %q", w.Code, w.Header().Get("Location"))
	}
	if h.dataNetAPIToken(ctx) != "tskey-api-abc123" {
		t.Error("Token nicht lesbar")
	}
	if raw := h.dataNetSetting(ctx, dataNetKeyAPIToken); raw == "" || strings.Contains(raw, "abc123") {
		t.Errorf("Token nicht verschlüsselt: %q", raw)
	}
	// Leeres Feld lässt ihn stehen; die Seite zeigt nur „hinterlegt“.
	post(url.Values{})
	if h.dataNetAPIToken(ctx) != "tskey-api-abc123" {
		t.Error("leeres Feld löschte den Token")
	}
	page := dnDo(h.DataNetPage, "GET", "/datanet", nil, nil).Body.String()
	if strings.Contains(page, "abc123") || !strings.Contains(page, "hinterlegt – leer lassen zum Behalten") || !strings.Contains(page, "Token löschen") {
		t.Error("Seite zeigt Token oder „hinterlegt“ fehlt")
	}
	var note string
	h.db.QueryRow(ctx, `SELECT note FROM datanet_events WHERE action = 'settings' ORDER BY at DESC LIMIT 1`).Scan(&note)
	if strings.Contains(note, "abc123") {
		t.Errorf("Token im Ereignisprotokoll: %q", note)
	}
	post(url.Values{"apitoken_delete": {"on"}})
	if h.dataNetAPIToken(ctx) != "" || h.dataNetSetting(ctx, dataNetKeyAPIToken) != "" {
		t.Error("Token nicht gelöscht")
	}
}
