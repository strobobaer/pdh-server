package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/core/users"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Karte in den Server-Einstellungen (esppdh_settings.go): Schalter und
// „Umgebung einrichten“ nur mit canServerConfig, am Spiegel 409, im Docker-Betrieb nur ein
// Hinweis. Die Python-Umgebung fehlt in den Tests (PDH_ESPHOME_DIR auf einen leeren Ordner).

// esppdhTestEnv: keine echte Umgebung, kein Docker; setzt beides nach dem Test zurück.
func esppdhTestEnv(t *testing.T, docker bool) {
	t.Helper()
	t.Setenv("PDH_ESPHOME_DIR", filepath.Join(t.TempDir(), "venv"))
	t.Setenv("PDH_ESPHOME_WORKDIR", t.TempDir())
	old := esppdhDockerHook
	t.Cleanup(func() { esppdhDockerHook = old })
	esppdhDockerHook = func() bool { return docker }
}

// esppdhFakeAgent: Update-Agent, der jede Anfrage mitschreibt und mit answer antwortet.
func esppdhFakeAgent(t *testing.T, answer string) (*httptest.Server, *[]string) {
	t.Helper()
	calls := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, r.Method+" "+r.URL.Path+" "+string(body)+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// esppdhReq: POST als Benutzer mit Rolle role.
func esppdhReq(path string, role users.Role, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
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
	srv, calls := esppdhFakeAgent(t, `{"ok":true,"version":"2026.10.2"}`)
	for _, c := range []struct {
		name string
		perm bool
		role users.Role
	}{{"ohne Recht", false, users.RoleAdmin}, {"Recht, aber kein Administrator", true, users.RoleManager}} {
		h := esppdhHandler(t, c.perm, srv.URL)
		for path, fn := range map[string]http.HandlerFunc{
			"/admin/server-config/esppdh":         h.EsppdhSwitchWeb,
			"/admin/server-config/esppdh/install": h.EsppdhInstallWeb,
		} {
			w := httptest.NewRecorder()
			fn(w, esppdhReq(path, c.role, url.Values{"on": {"1"}}))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: Status %d", c.name, path, w.Code)
			}
		}
	}
	if len(*calls) != 0 {
		t.Errorf("Agent ohne Recht gefragt: %v", *calls)
	}
}

// Am Spiegelserver: nichts schalten, nichts einrichten – 409.
func TestEsppdhMirrorConflict(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, calls := esppdhFakeAgent(t, `{"ok":true,"version":"2026.10.2"}`)
	h := esppdhHandler(t, true, srv.URL)
	h.SetMirror(MirrorInfo{On: true})
	for path, fn := range map[string]http.HandlerFunc{
		"/admin/server-config/esppdh":         h.EsppdhSwitchWeb,
		"/admin/server-config/esppdh/install": h.EsppdhInstallWeb,
	} {
		w := httptest.NewRecorder()
		fn(w, esppdhReq(path, users.RoleAdmin, url.Values{"on": {"1"}}))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), mirrorReadOnlyMsg) {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Errorf("Agent am Spiegel gefragt: %v", *calls)
	}
	// die Karte zeigt am Spiegel keinen Knopf
	p := h.esppdhPanelFor(context.Background())
	out := renderSNMPTemplate(t, "esppdh-panel", p)
	if !p.Mirror || !strings.Contains(out, "Spiegelserver") || strings.Contains(out, "hx-post") {
		t.Errorf("Spiegel: Hinweis statt Knopf erwartet:\n%s", out)
	}
}

// Docker-Betrieb: Hinweis statt Knopf; die Aktionen fragen den Agenten nicht.
func TestEsppdhDockerHint(t *testing.T) {
	esppdhTestEnv(t, true)
	srv, calls := esppdhFakeAgent(t, `{"ok":true,"version":"2026.10.2"}`)
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
		fn(w, esppdhReq(path, users.RoleAdmin, url.Values{"on": {"1"}}))
		if !strings.Contains(w.Body.String(), "ESPPDH gibt es im Docker-Betrieb nicht") {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Errorf("Agent im Docker-Betrieb gefragt: %v", *calls)
	}
}

// „Umgebung einrichten“ fragt den Agenten mit {} und meldet die Version.
func TestEsppdhInstallAsksAgent(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, calls := esppdhFakeAgent(t, `{"ok":true,"version":"2026.10.2"}`)
	h := esppdhHandler(t, true, srv.URL)
	w := httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq("/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ESPHome 2026.10.2 ist eingerichtet") || !strings.Contains(w.Body.String(), `id="esppdh-panel"`) {
		t.Fatalf("Antwort %d:\n%s", w.Code, w.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /v1/esppdh/install {} Bearer tok" {
		t.Errorf("Agent: %v", *calls)
	}
	// Fehler des Agenten erscheint in der Karte
	srv2, _ := esppdhFakeAgent(t, `{"ok":false,"error":"pip: kein Internet"}`)
	h.updateAgentURL = srv2.URL
	w = httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq("/admin/server-config/esppdh/install", users.RoleAdmin, nil))
	if !strings.Contains(w.Body.String(), "pip: kein Internet") {
		t.Errorf("Fehler fehlt:\n%s", w.Body.String())
	}
	// ohne Update-Agent: kein Aufruf, Knopf gesperrt
	h.updateAgentURL = ""
	w = httptest.NewRecorder()
	h.EsppdhInstallWeb(w, esppdhReq("/admin/server-config/esppdh/install", users.RoleAdmin, nil))
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
		h.EsppdhSwitchWeb(w, esppdhReq("/admin/server-config/esppdh", users.RoleAdmin, url.Values{"on": {c.on}}))
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
	if strings.Contains(out, "Freier Platz") {
		t.Error("unbekannter Platz wird angezeigt")
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
