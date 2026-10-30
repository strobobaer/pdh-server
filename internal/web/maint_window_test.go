package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/ha"
)

const testFullCommit = "abcdef0123456789abcdef0123456789abcdef01"

// mwReq: Anfrage eines Administrators (Formular).
func mwReq(target string, form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "a1", Role: users.RoleAdmin}))
}

// mwHandler: Handler mit Rechte-Attrappe (alle Rechte außer den genannten).
func mwHandler(deny ...string) *Handler {
	return &Handler{
		hasPermHook: func(_ *http.Request, perm string) bool {
			for _, d := range deny {
				if d == perm {
					return false
				}
			}
			return true
		},
		latestCommitHook: func(context.Context) string { return testFullCommit },
	}
}

// mwAgent: Attrappe des Update-Agenten; merkt sich Pfade und Rümpfe.
type mwAgent struct {
	mu      sync.Mutex
	calls   []string
	bodies  []string
	replies map[string]fakeReply
}

func startMWAgent(t *testing.T, h *Handler, a *mwAgent) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.calls = append(a.calls, r.Method+" "+r.URL.Path)
		a.bodies = append(a.bodies, string(b))
		rep, ok := a.replies[r.Method+" "+r.URL.Path]
		a.mu.Unlock()
		if !ok {
			rep = fakeReply{200, `{"ok":true}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.code)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(srv.Close)
	h.updateAgentURL, h.updateAgentToken = srv.URL, "tok"
}

func (a *mwAgent) called(call string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, c := range a.calls {
		if c == call {
			return a.bodies[i], true
		}
	}
	return "", false
}

func noHA(t *testing.T) {
	t.Helper()
	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
}

const mwDuoStatus = `{"ok":true,"configured":true,"mode":"duo",
 "self":{"id":"pdh1","kind":"node","role":"aktiv","term":3,"version":"0.77.8","commit":"` + testFullCommit + `","seen":true,"last_seen_ago":0},
 "peers":[{"id":"pdh2","kind":"node","role":"passiv","term":3,"version":"0.77.8","commit":"` + testFullCommit + `","seen":true,"last_seen_ago":0.4}],
 "window":{"mode":"weekly","weekday":3,"begin":"after_files","clock":"02:00","latest":"05:00","install":true,"kind":"sequential","swap":false,"countdown_min":2,"lock_max_min":30},
 "ca_holder":true,"error":""}`

func TestWindowFormSaves(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`}
	startFakeHA(t, f)
	h := mwHandler()
	form := url.Values{"mode": {"weekly"}, "weekday": {"3"}, "begin": {"before_files"}, "clock": {"01:30"}, "latest": {"04:45"},
		"install": {"1"}, "kind": {"sequential"}, "swap": {"1"}, "countdown_min": {"5"}, "lock_max_min": {"45"}}
	rec := httptest.NewRecorder()
	h.HAWindowWeb(rec, mwReq("/admin/server-config/ha/window", form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Status %d: %s", rec.Code, rec.Body)
	}
	c := f.callList()
	if len(c) == 0 || c[len(c)-1] != "PUT /v1/window" {
		t.Fatalf("Aufrufe: %v", c)
	}
	var got ha.Window
	f.mu.Lock()
	err := json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &got)
	f.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	want := ha.Window{Mode: "weekly", Weekday: 3, Begin: "before_files", Clock: "01:30", Latest: "04:45", Install: true,
		Kind: ha.UpdateSequential, Swap: true, CountdownMin: 5, LockMaxMin: 45}
	if got != want {
		t.Fatalf("PUT-Rumpf %+v, erwartet %+v", got, want)
	}
	// ungültig: kein Aufruf
	f.mu.Lock()
	n := len(f.calls)
	f.mu.Unlock()
	form.Set("countdown_min", "99")
	rec = httptest.NewRecorder()
	h.HAWindowWeb(rec, mwReq("/admin/server-config/ha/window", form))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("Fehler nicht gemeldet: %q", loc)
	}
	if len(f.callList()) != n {
		t.Fatal("ungültiges Fenster an pdh-ha geschickt")
	}
}

func TestWindowFormNeedsPerm(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`}
	startFakeHA(t, f)
	h := mwHandler(permMaintWindow)
	for name, fn := range map[string]http.HandlerFunc{"window": h.HAWindowWeb, "faulty": h.HAFaultyAllowWeb, "single": h.UpdateWindowWeb} {
		rec := httptest.NewRecorder()
		fn(rec, mwReq("/x", url.Values{"mode": {"daily"}, "commit": {"abcdef0"}}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", name, rec.Code)
		}
	}
	if c := f.callList(); len(c) != 0 {
		t.Fatalf("pdh-ha ohne Recht aufgerufen: %v", c)
	}
}

func TestFaultyAllowCallsSocket(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`}
	startFakeHA(t, f)
	h := mwHandler()
	rec := httptest.NewRecorder()
	h.HAFaultyAllowWeb(rec, mwReq("/admin/server-config/ha/faulty-allow", url.Values{"commit": {"abcdef0"}}))
	if c := f.callList(); len(c) != 1 || c[0] != "DELETE /v1/update/faulty/abcdef0" {
		t.Fatalf("Aufrufe: %v (Status %d)", c, rec.Code)
	}
	rec = httptest.NewRecorder()
	h.HAFaultyAllowWeb(rec, mwReq("/admin/server-config/ha/faulty-allow", url.Values{"commit": {"../x"}}))
	if len(f.callList()) != 1 || !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatal("ungültiger Commit angenommen")
	}
}

func TestSingleWindowFormSaves(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{}
	startMWAgent(t, h, a)
	form := url.Values{"mode": {"daily"}, "weekday": {"0"}, "begin": {"after_files"}, "clock": {"02:00"}, "latest": {"05:00"},
		"install": {"1"}, "kind": {"rolling"}, "countdown_min": {"2"}, "lock_max_min": {"30"}}
	rec := httptest.NewRecorder()
	h.UpdateWindowWeb(rec, mwReq("/admin/server-config/update-window", form))
	body, ok := a.called("POST /v1/window")
	if !ok || !strings.Contains(body, `"mode":"daily"`) || !strings.Contains(body, `"swap":false`) {
		t.Fatalf("Agent-Aufrufe %v, Rumpf %q", a.calls, body)
	}
	rec = httptest.NewRecorder()
	h.UpdateWindowWeb(rec, mwReq("/admin/server-config/update-window", url.Values{"allow": {"abcdef0"}}))
	if _, ok := a.called("DELETE /v1/window/faulty/abcdef0"); !ok {
		t.Fatalf("Wieder zulassen nicht an den Agenten: %v", a.calls)
	}
}

func TestMaintWatchNotifiesOnce(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{"GET /v1/window": {200,
		`{"ok":true,"cluster":false,"window":{},"last_run":"2026-10-30T02:00:00Z","last_result":"Update auf 0.77.9 fehlgeschlagen: zurückgesetzt","faulty":[]}`}}}
	startMWAgent(t, h, a)
	store := map[string]string{}
	get := func(k string) string { return store[k] }
	set := func(k, v string) { store[k] = v }
	var msgs []string
	notify := func(m string) { msgs = append(msgs, m) }
	now := time.Date(2026, 10, 30, 2, 10, 0, 0, time.UTC)

	h.maintWatchRun(context.Background(), now, get, set, notify)
	h.maintWatchRun(context.Background(), now.Add(30*time.Second), get, set, notify)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "zurückgesetzt") {
		t.Fatalf("Meldungen: %q", msgs)
	}
	// neues Ergebnis (anderer Lauf) wird wieder gemeldet, auch gleicher Text (nach Ablauf der Drosselung)
	a.mu.Lock()
	a.replies["GET /v1/window"] = fakeReply{200, `{"ok":true,"window":{},"last_run":"2026-10-31T02:00:00Z","last_result":"Update auf 0.77.9 fehlgeschlagen: zurückgesetzt","faulty":[]}`}
	a.mu.Unlock()
	h.maintWatchRun(context.Background(), now.Add(24*time.Hour), get, set, notify)
	if len(msgs) != 2 {
		t.Fatalf("zweiter Lauf nicht gemeldet: %q", msgs)
	}
	// altes Ergebnis beim ersten Lauf nach dem Start: nicht nachmelden
	store2 := map[string]string{}
	var msgs2 []string
	h.maintWatchRun(context.Background(), now.Add(72*time.Hour), func(k string) string { return store2[k] }, func(k, v string) { store2[k] = v }, func(m string) { msgs2 = append(msgs2, m) })
	if len(msgs2) != 0 {
		t.Fatalf("altes Ergebnis nachgemeldet: %q", msgs2)
	}
}

func TestMaintWatchSkipsCluster(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{"GET /v1/window": {200,
		`{"ok":true,"cluster":true,"window":{},"last_run":"2026-10-30T02:00:00Z","last_result":"x","faulty":[]}`}}}
	startMWAgent(t, h, a)
	var msgs []string
	h.maintWatchRun(context.Background(), time.Date(2026, 10, 30, 2, 1, 0, 0, time.UTC), func(string) string { return "" }, func(string, string) {}, func(m string) { msgs = append(msgs, m) })
	if len(msgs) != 0 {
		t.Fatalf("im Verbund meldet pdh-ha: %q", msgs)
	}
}

// ── Update-Knopf ───────────────────────────────────────────────

const mwDRReply = `{"ok":true,"supported":true,"config":{"enabled":true},"last_base":"2026-10-30T02:00:00Z","last_file_sync":"2026-10-30T03:00:00Z","last_base_ok":true,"last_files_ok":false}`

func TestInstallUpdateClusterConflictAsks(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`, replies: map[string]fakeReply{
		"/v1/update/start": {409, `{"ok":false,"error":"Partner nicht erreichbar","hints":["Freier Platz nicht geprüft"]}`}}}
	startFakeHA(t, f)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{"GET /v1/dr/status": {200, mwDRReply}}}
	startMWAgent(t, h, a)
	rec := httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || loc.Path != "/core/settings" {
		t.Fatalf("Status %d, Ziel %q", rec.Code, rec.Header().Get("Location"))
	}
	if loc.RawQuery != "ask=1" {
		t.Fatalf("Adresszeile trägt Text: %q", loc.RawQuery)
	}
	ak, ok := updateAskGet("a1")
	if !ok || ak.Reason != "Partner nicht erreichbar" || len(ak.Hints) != 1 || ak.Hints[0] != "Freier Platz nicht geprüft" || ak.Token == "" {
		t.Fatalf("Rückfrage im Speicher: %+v", ak)
	}
	// Stand der Sicherung gemeldet (RULING R14)
	var req map[string]any
	f.mu.Lock()
	_ = json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &req)
	f.mu.Unlock()
	if req["source"] != "knopf" || req["commit"] != testFullCommit || req["kind"] != "sequential" || req["force"] != false ||
		req["backup_configured"] != true || req["last_base_ok"] != true || req["last_files_ok"] != false {
		t.Fatalf("Start-Rumpf: %v", req)
	}
}

func TestInstallUpdateForce(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`, replies: map[string]fakeReply{
		"/v1/update/start": {200, `{"ok":true,"hints":[]}`}}}
	startFakeHA(t, f)
	h := mwHandler()
	startMWAgent(t, h, &mwAgent{})
	rec := httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", url.Values{"force": {"1"}, "token": {mwAskToken(t, "a1")}}))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("ask") != "" || !strings.Contains(loc.Query().Get("notice"), "gestartet") {
		t.Fatalf("Ziel %q", rec.Header().Get("Location"))
	}
	var req map[string]any
	f.mu.Lock()
	_ = json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &req)
	f.mu.Unlock()
	if req["force"] != true {
		t.Fatalf("force fehlt: %v", req)
	}
	// Auch mit force nicht möglich: Fehlermeldung statt erneuter Rückfrage
	f.mu.Lock()
	f.replies["/v1/update/start"] = fakeReply{409, `{"ok":false,"error":"Ein anderer Vorgang läuft","hints":[]}`}
	f.mu.Unlock()
	rec = httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", url.Values{"force": {"1"}, "token": {mwAskToken(t, "a1")}}))
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("ask") != "" || !strings.Contains(loc.Query().Get("notice"), "Ein anderer Vorgang läuft") {
		t.Fatalf("Ziel %q", rec.Header().Get("Location"))
	}
}

func TestInstallUpdateMirrorRefused(t *testing.T) {
	st := strings.Replace(mwDuoStatus, `"role":"aktiv"`, `"role":"passiv"`, 1)
	f := &fakeHA{status: st, events: `[]`}
	startFakeHA(t, f)
	h := mwHandler()
	startMWAgent(t, h, &mwAgent{})
	rec := httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Query().Get("notice"), "wird vom aktiven Server gesteuert") {
		t.Fatalf("Ziel %q", rec.Header().Get("Location"))
	}
	for _, c := range f.callList() {
		if strings.Contains(c, "/v1/update/start") {
			t.Fatal("Update am Spiegel gestartet")
		}
	}
}

func TestInstallUpdateSingleUsesJob(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{"POST /v1/update/job/start": {200, `{"ok":true,"hints":[]}`}}}
	startMWAgent(t, h, a)
	rec := httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", url.Values{"force": {"1"}, "token": {mwAskToken(t, "a1")}}))
	body, ok := a.called("POST /v1/update/job/start")
	if !ok || !strings.Contains(body, `"force":true`) {
		t.Fatalf("Agent-Aufrufe %v, Rumpf %q", a.calls, body)
	}
	if _, old := a.called("POST /v1/update"); old {
		t.Fatal("alter Weg benutzt")
	}
	// nicht grün: Rückfrage
	a.mu.Lock()
	a.replies["POST /v1/update/job/start"] = fakeReply{409, `{"ok":false,"error":"Sicherung ist nicht aktuell","hints":["h1"]}`}
	a.mu.Unlock()
	rec = httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if ak, ok := updateAskGet("a1"); loc.RawQuery != "ask=1" || !ok || ak.Reason != "Sicherung ist nicht aktuell" {
		t.Fatalf("Ziel %q, Rückfrage %+v", rec.Header().Get("Location"), ak)
	}
}

func TestInstallUpdateDockerKeepsOld(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{
		"POST /v1/update/job/start": {200, `{"ok":false,"error":"im Docker-Betrieb nicht verfügbar"}`},
		"POST /v1/update":           {202, `{"ok":true}`},
	}}
	startMWAgent(t, h, a)
	rec := httptest.NewRecorder()
	h.InstallUpdateWeb(rec, mwReq("/core/settings/install-update", nil))
	if _, ok := a.called("POST /v1/update"); !ok {
		t.Fatalf("alter Weg nicht benutzt: %v", a.calls)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Query().Get("notice"), "Update wurde gestartet") {
		t.Fatalf("Ziel %q", rec.Header().Get("Location"))
	}
}

// ── Anzeige ────────────────────────────────────────────────────

func mwWitnessStatus(witnessCommit string, active bool, ended string) string {
	act := "false"
	if active {
		act = "true"
	}
	return `{"ok":true,"configured":true,"mode":"trio",
 "self":{"id":"pdh1","kind":"node","role":"aktiv","version":"0.77.9","commit":"` + testFullCommit + `","seen":true},
 "peers":[{"id":"pdh2","kind":"node","role":"passiv","version":"0.77.9","commit":"` + testFullCommit + `","seen":true},
  {"id":"zeuge1","kind":"witness","role":"zeuge","version":"0.77.8","commit":"` + witnessCommit + `","seen":true}],
 "update":{"active":` + act + `,"result":"Update auf 0.77.9 abgeschlossen","ended_at":"` + ended + `"},
 "ca_holder":true}`
}

func TestWitnessStaleHint(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	cases := []struct {
		name, status string
		stale        bool
	}{
		{"veraltet", mwWitnessStatus("1111111111111111111111111111111111111111", false, old), true},
		{"gleicher Stand", mwWitnessStatus(testFullCommit, false, old), false},
		{"Auftrag läuft", mwWitnessStatus("1111111111111111111111111111111111111111", true, old), false},
		{"gerade erst fertig", mwWitnessStatus("1111111111111111111111111111111111111111", false, fresh), false},
	}
	for _, c := range cases {
		startFakeHA(t, &fakeHA{status: c.status, events: `[]`})
		v := (&Handler{}).haLoadView(context.Background())
		if got := v.WitnessStale != ""; got != c.stale {
			t.Errorf("%s: WitnessStale=%q", c.name, v.WitnessStale)
		}
		if c.stale {
			if v.WitnessStale != "Zeuge veraltet (0.77.8, Server 0.77.9)" {
				t.Errorf("Text: %q", v.WitnessStale)
			}
			if !strings.Contains(v.WitnessCmd, "pdh-ha-join.sh --update "+testFullCommit) {
				t.Errorf("Befehl: %q", v.WitnessCmd)
			}
			out := renderDR(t, drView{HA: v})
			if !strings.Contains(out, "Zeuge veraltet (0.77.8, Server 0.77.9)") || !strings.Contains(out, "Notweg: von Hand aktualisieren") ||
				!strings.Contains(out, "pdh-ha-join.sh --update") {
				t.Error("Hinweis oder Notweg fehlt in der Anzeige")
			}
		}
	}
}

func TestHAWidgetRendersWindow(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`}
	startFakeHA(t, f)
	v := (&Handler{}).haLoadView(context.Background())
	if v.Window.Mode != "weekly" || v.Window.Kind != ha.UpdateSequential {
		t.Fatalf("Fenster: %+v", v.Window)
	}
	v.NextWindow = time.Date(2026, 11, 4, 2, 0, 0, 0, time.Local)
	v.Faulty = []string{"deadbee1234"}
	out := renderDR(t, drView{HA: v})
	for _, want := range []string{"Wartungsfenster", `action="/admin/server-config/ha/window"`, `name="mode"`, `name="weekday"`, `name="begin"`,
		`name="clock"`, `name="latest"`, `name="install"`, `name="kind"`, `name="swap"`, `name="countdown_min"`, `name="lock_max_min"`,
		`value="sequential" selected`, `action="/admin/server-config/ha/faulty-allow"`, `value="deadbee1234"`, "04.11.2026 02:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte ohne %q", want)
		}
	}
	// Laufender Auftrag: Fortschritt
	v.Update = ha.UpdateView{Active: true, Kind: "rolling", Steps: []string{"Abmelden", "Rollentausch"}, Step: 1, Percent: 20, Text: "Rollentausch läuft"}
	out = renderDR(t, drView{HA: v})
	for _, want := range []string{"Rollentausch läuft", `id="ha-update-progress"`, `value="20"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Fortschritt ohne %q", want)
		}
	}
}

func TestSingleServerWindowBox(t *testing.T) {
	noHA(t)
	h := mwHandler()
	a := &mwAgent{replies: map[string]fakeReply{
		"GET /v1/window": {200, `{"ok":true,"cluster":false,"window":{"mode":"daily","weekday":0,"begin":"after_files","clock":"02:00","latest":"05:00","install":true,"kind":"rolling","countdown_min":2,"lock_max_min":30},"last_run":"2026-10-30T02:00:00Z","last_result":"Update auf 0.77.9 abgeschlossen","faulty":["deadbee1234"],"next":"2026-10-31T02:00:00Z"}`},
		"GET /v1/update/job": {200, `{"ok":true,"active":true,"percent":40,"text":"Server startet mit der neuen Version neu","steps":["a","b"],"step":1}`},
	}}
	startMWAgent(t, h, a)
	s := h.maintLoadSingle(context.Background())
	if !s.Available || s.Window.Mode != "daily" || len(s.Faulty) != 1 || s.LastResult == "" || !s.Job.Active {
		t.Fatalf("Einzelserver-Sicht: %+v", s)
	}
	out := renderDR(t, drView{Single: s})
	for _, want := range []string{`action="/admin/server-config/update-window"`, "Wartungsfenster", "Update auf 0.77.9 abgeschlossen", `name="allow"`, "Server startet mit der neuen Version neu"} {
		if !strings.Contains(out, want) {
			t.Errorf("Einzelserver-Karte ohne %q", want)
		}
	}
}

// mwAskToken legt eine Rückfrage für den Benutzer an und liefert ihr Token.
func mwAskToken(t *testing.T, user string) string {
	t.Helper()
	updateAskPut(user, "Test", nil)
	a, ok := updateAskGet(user)
	if !ok {
		t.Fatal("Rückfrage fehlt")
	}
	return a.Token
}

func forceBody(t *testing.T, f *fakeHA) (force bool, n int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == "POST /v1/update/start" {
			var req map[string]any
			_ = json.Unmarshal([]byte(f.bodies[i]), &req)
			force, _ = req["force"].(bool)
			n++
		}
	}
	return
}

func TestInstallUpdateForceNeedsToken(t *testing.T) {
	f := &fakeHA{status: mwDuoStatus, events: `[]`, replies: map[string]fakeReply{
		"/v1/update/start": {200, `{"ok":true,"hints":[]}`}}}
	startFakeHA(t, f)
	h := mwHandler()
	startMWAgent(t, h, &mwAgent{})
	post := func(user, token string) {
		req := mwReq("/core/settings/install-update", url.Values{"force": {"1"}, "token": {token}})
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: user, Role: users.RoleAdmin}))
		h.InstallUpdateWeb(httptest.NewRecorder(), req)
	}
	// ohne Token: normaler Start
	post("a1", "")
	post("a1", "erfunden")
	if force, n := forceBody(t, f); n != 2 || force {
		t.Fatalf("ohne Token erzwungen: force=%v n=%d", force, n)
	}
	// Token eines anderen Benutzers
	tok := mwAskToken(t, "a1")
	post("b2", tok)
	f.mu.Lock()
	var last map[string]any
	_ = json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &last)
	f.mu.Unlock()
	if last["force"] != false {
		t.Fatal("fremdes Token erzwingt")
	}
	// gültig: einmal
	post("a1", tok)
	f.mu.Lock()
	_ = json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &last)
	f.mu.Unlock()
	if last["force"] != true {
		t.Fatal("gültiges Token erzwingt nicht")
	}
	post("a1", tok)
	f.mu.Lock()
	_ = json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &last)
	f.mu.Unlock()
	if last["force"] != false {
		t.Fatal("Token zweimal verwendbar")
	}
}

func TestUpdateAskExpires(t *testing.T) {
	updateAskPut("x9", "Grund", nil)
	updateAsks.Lock()
	a := updateAsks.m["x9"]
	a.Expires = time.Now().Add(-time.Second)
	updateAsks.m["x9"] = a
	updateAsks.Unlock()
	if updateAskConsume("x9", a.Token) {
		t.Fatal("abgelaufenes Token angenommen")
	}
	if _, ok := updateAskGet("x9"); ok {
		t.Fatal("abgelaufene Rückfrage nicht entfernt")
	}
}
