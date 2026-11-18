package web

import (
	"context"
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
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Karte in den Server-Einstellungen (esppdh_settings.go): Schalter und
// „Umgebung einrichten“ nur mit canServerConfig, am Spiegel 409, im Docker-Betrieb nur ein
// Hinweis. Das Einrichten läuft beim Update-Agenten im Hintergrund; die Karte fragt alle 2 s
// nach. Die Python-Umgebung fehlt in den Tests (PDH_ESPHOME_DIR auf einen leeren Ordner).

// esppdhTestEnv: keine echte Umgebung, kein Docker; setzt beides nach dem Test zurück.
func esppdhTestEnv(t *testing.T, docker bool) {
	t.Helper()
	t.Setenv("PDH_ESPHOME_DIR", filepath.Join(t.TempDir(), "venv"))
	t.Setenv("PDH_ESPHOME_WORKDIR", t.TempDir())
	old := esppdhDockerHook
	t.Cleanup(func() { esppdhDockerHook = old })
	esppdhDockerHook = func() bool { return docker }
}

// esppdhAgent: Update-Agent im Test. POST /v1/esppdh/install antwortet mit start, GET mit
// status (änderbar); jede Anfrage wird mitgeschrieben.
type esppdhAgent struct {
	mu            sync.Mutex
	start, status string
	calls         []string
}

func (a *esppdhAgent) setStatus(s string) { a.mu.Lock(); a.status = s; a.mu.Unlock() }

func (a *esppdhAgent) log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func esppdhFakeAgent(t *testing.T, start, status string) (*httptest.Server, *esppdhAgent) {
	t.Helper()
	a := &esppdhAgent{start: start, status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.calls = append(a.calls, r.Method+" "+r.URL.Path+" "+string(body)+" "+r.Header.Get("Authorization"))
		answer := a.status
		if r.Method == http.MethodPost {
			answer = a.start
		}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, a
}

const (
	esppdhIdle    = `{"ok":true,"running":false,"done":false}`
	esppdhRunning = `{"ok":true,"running":true,"done":false}`
	esppdhOK      = `{"ok":true,"running":false,"done":true,"version":"2026.10.2"}`
)

// esppdhReq: Anfrage als Benutzer mit Rolle role.
func esppdhReq(method, path string, role users.Role, form url.Values) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "u-esppdh", Role: role}))
}

// esppdhHandler: Handler mit Vorlagen, Agent und Recht system.server_config (perm=true).
func esppdhHandler(t *testing.T, perm bool, agentURL string) *Handler {
	return &Handler{tmpl: loadTestTemplates(t), updateAgentURL: agentURL, updateAgentToken: "tok",
		hasPermHook: func(_ *http.Request, p string) bool { return perm && p == "system.server_config" }}
}

func TestEsppdhRoutesNeedServerConfig(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, ag := esppdhFakeAgent(t, esppdhRunning, esppdhIdle)
	for _, c := range []struct {
		name string
		perm bool
		role users.Role
	}{{"ohne Recht", false, users.RoleAdmin}, {"Recht, aber kein Administrator", true, users.RoleManager}} {
		h := esppdhHandler(t, c.perm, srv.URL)
		for _, rt := range []struct {
			method, path string
			fn           http.HandlerFunc
		}{
			{http.MethodPost, "/admin/server-config/esppdh", h.EsppdhSwitchWeb},
			{http.MethodPost, "/admin/server-config/esppdh/install", h.EsppdhInstallWeb},
			{http.MethodGet, "/admin/server-config/esppdh", h.EsppdhPanelWeb},
		} {
			w := httptest.NewRecorder()
			rt.fn(w, esppdhReq(rt.method, rt.path, c.role, url.Values{"on": {"1"}}))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: Status %d", c.name, rt.method, rt.path, w.Code)
			}
		}
	}
	if calls := ag.log(); len(calls) != 0 {
		t.Errorf("Agent ohne Recht gefragt: %v", calls)
	}
}

// Am Spiegelserver: Schalter gesperrt (409, Datenbank nur lesend); die Umgebung einrichten geht –
// jeder Server braucht sie selbst, und im Verbund tauschen die Server bei jedem Update die Rollen.
func TestEsppdhMirror(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, ag := esppdhFakeAgent(t, esppdhRunning, esppdhRunning)
	h := esppdhHandler(t, true, srv.URL)
	h.SetMirror(MirrorInfo{On: true})
	w := httptest.NewRecorder()
	h.EsppdhSwitchWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh", users.RoleAdmin, url.Values{"on": {"1"}}))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), mirrorReadOnlyMsg) {
		t.Errorf("Schalter am Spiegel: %d %q", w.Code, w.Body.String())
	}
	if calls := ag.log(); len(calls) != 0 {
		t.Errorf("Schalter am Spiegel fragt den Agenten: %v", calls)
	}
	w = httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Umgebung wird eingerichtet") || !strings.Contains(w.Body.String(), `hx-trigger="every 2s"`) {
		t.Errorf("Einrichten am Spiegel: %d\n%s", w.Code, w.Body.String())
	}
	if calls := ag.log(); len(calls) == 0 || calls[0] != "POST /v1/esppdh/install {} Bearer tok" {
		t.Errorf("Einrichten am Spiegel startet nicht beim Agenten: %v", calls)
	}
	// Die Spiegel-Sperre davor lässt nur das Einrichten durch, nicht den Schalter.
	hd, passed := mirrorTestHandler(true)
	if rec := mirrorServe(hd, http.MethodPost, "/admin/server-config/esppdh/install", nil); rec.Code != http.StatusOK {
		t.Errorf("Spiegel-Sperre blockiert das Einrichten: %d", rec.Code)
	}
	for _, p := range []string{"/admin/server-config/esppdh", "/admin/server-config/esppdh/installx", "/admin/server-config/esppdh/install/x"} {
		if rec := mirrorServe(hd, http.MethodPost, p, nil); rec.Code != http.StatusConflict {
			t.Errorf("POST %s am Spiegel: %d, erwartet 409", p, rec.Code)
		}
	}
	if *passed != 1 {
		t.Errorf("durchgereicht: %d, erwartet 1", *passed)
	}
	// Karte am Spiegel: Hinweis, kein Schalter, Knopf „Umgebung einrichten“
	ag.setStatus(esppdhIdle)
	p := h.esppdhPanelFor(context.Background())
	out := renderSNMPTemplate(t, "esppdh-panel", p)
	if !p.Mirror || !strings.Contains(out, "Die Umgebung braucht jeder Server selbst") || strings.Contains(out, `hx-post="/admin/server-config/esppdh"`) ||
		strings.Contains(out, "Einschalten") || !strings.Contains(out, `hx-post="/admin/server-config/esppdh/install"`) || !strings.Contains(out, "Umgebung einrichten") {
		t.Errorf("Karte am Spiegel:\n%s", out)
	}
}

// Docker-Betrieb: Hinweis statt Knopf; die Aktionen fragen den Agenten nicht.
func TestEsppdhDockerHint(t *testing.T) {
	esppdhTestEnv(t, true)
	srv, ag := esppdhFakeAgent(t, esppdhRunning, esppdhIdle)
	h := esppdhHandler(t, true, srv.URL)
	if !inDocker() || h.esppdhEnabled(context.Background()) {
		t.Fatal("Haken für den Docker-Betrieb wirkt nicht")
	}
	out := renderSNMPTemplate(t, "esppdh-panel", h.esppdhPanelFor(context.Background()))
	if !strings.Contains(out, "ESPPDH gibt es im Docker-Betrieb nicht") || strings.Contains(out, "hx-post") ||
		strings.Contains(out, "Umgebung einrichten") || strings.Contains(out, "Einschalten") {
		t.Errorf("Docker: Hinweis statt Knopf erwartet:\n%s", out)
	}
	for path, fn := range map[string]http.HandlerFunc{
		"/admin/server-config/esppdh":         h.EsppdhSwitchWeb,
		"/admin/server-config/esppdh/install": h.EsppdhInstallWeb,
	} {
		w := httptest.NewRecorder()
		fn(w, esppdhReq(http.MethodPost, path, users.RoleAdmin, url.Values{"on": {"1"}}))
		if !strings.Contains(w.Body.String(), "ESPPDH gibt es im Docker-Betrieb nicht") {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	if calls := ag.log(); len(calls) != 0 {
		t.Errorf("Agent im Docker-Betrieb gefragt: %v", calls)
	}
}

// „Umgebung einrichten“ startet beim Agenten (mit {}) und kehrt sofort zurück; die Karte
// fragt alle 2 s nach, bis der Lauf fertig ist, und zeigt dann Version bzw. Fehler.
func TestEsppdhInstallStartsAndPolls(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, ag := esppdhFakeAgent(t, esppdhRunning, esppdhRunning)
	h := esppdhHandler(t, true, srv.URL)
	start := time.Now()
	w := httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Start wartet %s", took)
	}
	out := w.Body.String()
	for _, want := range []string{`id="esppdh-panel"`, "Umgebung wird eingerichtet", `hx-get="/admin/server-config/esppdh"`, `hx-trigger="every 2s"`} {
		if w.Code != http.StatusOK || !strings.Contains(out, want) {
			t.Errorf("nach dem Start %d: %q fehlt", w.Code, want)
		}
	}
	if strings.Contains(out, "Umgebung einrichten</button>") {
		t.Error("während des Laufs ist der Knopf zu sehen")
	}
	if calls := ag.log(); len(calls) == 0 || calls[0] != "POST /v1/esppdh/install {} Bearer tok" {
		t.Errorf("Agent: %v", calls)
	}

	// Abfrage der Karte (GET) während des Laufs: weiter nachfragen
	poll := func() string {
		w := httptest.NewRecorder()
		h.EsppdhPanelWeb(w, esppdhReq(http.MethodGet, "/admin/server-config/esppdh", users.RoleAdmin, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("Abfrage: %d", w.Code)
		}
		return w.Body.String()
	}
	if out := poll(); !strings.Contains(out, `hx-trigger="every 2s"`) || !strings.Contains(out, "Umgebung wird eingerichtet") {
		t.Errorf("läuft: Abfrage hört auf:\n%s", out)
	}
	// fertig: Version, kein Nachfragen mehr
	ag.setStatus(esppdhOK)
	if out := poll(); strings.Contains(out, "every 2s") || !strings.Contains(out, "Einrichtung abgeschlossen: ESPHome 2026.10.2") ||
		!strings.Contains(out, "Umgebung einrichten") {
		t.Errorf("fertig:\n%s", out)
	}
	// fehlgeschlagen: Meldung des Skripts, kein Nachfragen mehr
	ag.setStatus(`{"ok":true,"done":true,"error":"Einrichtung fehlgeschlagen: ESPHome 2026.10.2 braucht Python 3.12 oder neuer. Vorhanden: Python 3.10."}`)
	if out := poll(); strings.Contains(out, "every 2s") || !strings.Contains(out, "braucht Python 3.12 oder neuer. Vorhanden: Python 3.10.") {
		t.Errorf("fehlgeschlagen:\n%s", out)
	}
}

// Fehler beim Start und fehlender Agent erscheinen in der Karte.
func TestEsppdhInstallErrors(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, _ := esppdhFakeAgent(t, `{"ok":false,"error":"Der Agent kann gerade nicht"}`, esppdhIdle)
	h := esppdhHandler(t, true, srv.URL)
	w := httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if !strings.Contains(w.Body.String(), "Der Agent kann gerade nicht") || strings.Contains(w.Body.String(), "every 2s") {
		t.Errorf("Fehler fehlt:\n%s", w.Body.String())
	}
	// ohne Update-Agent: kein Aufruf, Knopf gesperrt
	h.updateAgentURL = ""
	w = httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if !strings.Contains(w.Body.String(), "Update-Agent") || !strings.Contains(w.Body.String(), "disabled") {
		t.Errorf("ohne Agent:\n%s", w.Body.String())
	}
}

// Schalter: speichert esppdh.enabled ("1" = an) – braucht die Labor-Datenbank.
func TestEsppdhSwitchStores(t *testing.T) {
	esppdhTestEnv(t, false)
	pool := connTestPool(t)
	ctx := context.Background()
	var before string
	had := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, esppdhSetEnabled).Scan(&before) == nil
	t.Cleanup(func() {
		if had {
			_, _ = pool.Exec(ctx, `UPDATE app_settings SET value=$2 WHERE key=$1`, esppdhSetEnabled, before)
		} else {
			_, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key=$1`, esppdhSetEnabled)
		}
	})
	h := esppdhHandler(t, true, "")
	h.db = pool
	for _, c := range []struct {
		on   string
		want bool
		text string
	}{{"1", true, "ESPPDH ist eingeschaltet"}, {"0", false, "ESPPDH ist ausgeschaltet"}} {
		w := httptest.NewRecorder()
		h.EsppdhSwitchWeb(w, esppdhReq(http.MethodPost, "/admin/server-config/esppdh", users.RoleAdmin, url.Values{"on": {c.on}}))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.text) {
			t.Errorf("on=%s: %d\n%s", c.on, w.Code, w.Body.String())
		}
		if got := h.esppdhEnabled(ctx); got != c.want {
			t.Errorf("on=%s: esppdhEnabled=%v", c.on, got)
		}
		var v string
		_ = pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, esppdhSetEnabled).Scan(&v)
		if v != c.on {
			t.Errorf("on=%s: gespeichert %q", c.on, v)
		}
	}
	// im Docker-Betrieb zählt der gespeicherte Schalter nicht
	esppdhDockerHook = func() bool { return true }
	if _, err := pool.Exec(ctx, `UPDATE app_settings SET value='1' WHERE key=$1`, esppdhSetEnabled); err != nil {
		t.Fatal(err)
	}
	if h.esppdhEnabled(ctx) {
		t.Error("im Docker-Betrieb eingeschaltet")
	}
}

func TestEsppdhEnabledWithoutDB(t *testing.T) {
	esppdhTestEnv(t, false)
	if (&Handler{}).esppdhEnabled(context.Background()) {
		t.Error("ohne Datenbank eingeschaltet")
	}
}

// Zustand in der Karte: nicht eingerichtet, eingerichtet, veraltet, gestört, freier Platz.
func TestEsppdhPanelStates(t *testing.T) {
	base := esppdhPanel{AgentReady: true, Pinned: "2026.10.2", Env: esppdh.EnvState{FreeMB: -1}}
	out := renderSNMPTemplate(t, "esppdh-panel", base)
	for _, want := range []string{`id="esppdh-panel"`, "ESPPDH ist ausgeschaltet", `hx-post="/admin/server-config/esppdh"`, `name="on" value="1"`,
		"Einschalten", "noch nicht eingerichtet", `hx-post="/admin/server-config/esppdh/install"`, `hx-target="#esppdh-panel"`, "Umgebung einrichten",
		"Der erste Bau je Platinentyp lädt mehrere hundert MB aus dem Internet", "ESPHome"} {
		if !strings.Contains(out, want) {
			t.Errorf("nicht eingerichtet: %q fehlt", want)
		}
	}
	if strings.Contains(out, "Freier Platz") || strings.Contains(out, "every 2s") {
		t.Error("unbekannter Platz wird angezeigt oder die Karte fragt ohne Lauf nach")
	}

	on := base
	on.Enabled = true
	on.Env = esppdh.EnvState{Installed: true, Version: "2026.10.2", FreeMB: 12595}
	out = renderSNMPTemplate(t, "esppdh-panel", on)
	for _, want := range []string{"ESPPDH ist eingeschaltet", `name="on" value="0"`, "Ausschalten", "eingerichtet", "ESPHome 2026.10.2", "Freier Platz: 12,3 GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("eingerichtet: %q fehlt", want)
		}
	}
	if strings.Contains(out, "veraltet") || strings.Contains(out, "noch nicht eingerichtet") {
		t.Error("eingerichtet: falscher Zustand")
	}

	old := on
	old.Env = esppdh.EnvState{Installed: true, Version: "2026.9.1", Outdated: true, FreeMB: 800}
	out = renderSNMPTemplate(t, "esppdh-panel", old)
	for _, want := range []string{"ESPHome 2026.9.1", "veraltet – wird mit dem nächsten Update angehoben", "2026.10.2", "Freier Platz: 800 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("veraltet: %q fehlt", want)
		}
	}

	bad := base
	bad.Env = esppdh.EnvState{Problem: "ESPHome startet nicht: kaputt <b>", FreeMB: -1}
	out = renderSNMPTemplate(t, "esppdh-panel", bad)
	if !strings.Contains(out, "ESPHome startet nicht: kaputt &lt;b&gt;") || strings.Contains(out, "noch nicht eingerichtet") {
		t.Errorf("gestört:\n%s", out)
	}

	noAgent := base
	noAgent.AgentReady = false
	if out = renderSNMPTemplate(t, "esppdh-panel", noAgent); !strings.Contains(out, "Update-Agent") || !strings.Contains(out, "disabled") {
		t.Errorf("ohne Agent:\n%s", out)
	}
}

// Server-Einstellungen: Reiter „ESPPDH“; die Karte prüft die Umgebung nur, wenn der Reiter offen ist.
func TestServerConfigEsppdhTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "server_config", ServerConfigData{})
	for _, want := range []string{`data-tab="esppdh" href="?tab=esppdh"`, `data-pane="esppdh"`, "ESPPDH laden"} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter zu: %q fehlt", want)
		}
	}
	if strings.Contains(out, `id="esppdh-panel"`) {
		t.Error("Karte ohne geöffneten Reiter gezeichnet")
	}
	checkTabs(t, "server_config", out, "esppdh")
	out = renderPage(t, tmpl, "server_config", ServerConfigData{Tab: "esppdh", ESPPDH: esppdhPanel{Pinned: "2026.10.2", Env: esppdh.EnvState{FreeMB: -1}}})
	if !strings.Contains(out, `id="esppdh-panel"`) || strings.Contains(out, "ESPPDH laden") {
		t.Error("Reiter offen: Karte fehlt")
	}
}
