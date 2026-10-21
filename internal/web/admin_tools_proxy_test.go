package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/pkg/config"
)

func TestAdminToolProxy(t *testing.T) {
	var gotPath, gotScript, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotScript, gotCookie = r.URL.Path, r.Header.Get("X-Script-Name"), r.Header.Get("Cookie")
		if r.URL.Path == "/login" {
			http.Redirect(w, r, "/browser/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	t.Setenv("PDH_N8N_INTERNAL_URL", up.URL)
	t.Setenv("PDH_PGADMIN_INTERNAL_URL", up.URL)
	h := &Handler{}

	// n8n-Webhook ohne Anmeldung, Präfix weg, PDH-Cookie nicht weitergeben
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/n8n/webhook/abc", nil)
	req.Header.Set("Cookie", "pdh_token=x; n8n-auth=y")
	h.AdminToolProxy("n8n").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/webhook/abc" || gotCookie != "n8n-auth=y" {
		t.Errorf("Webhook: %d %q %q", rec.Code, gotPath, gotCookie)
	}

	// Editor ohne Sitzung → Anmeldung
	gotPath = ""
	rec = httptest.NewRecorder()
	h.AdminToolProxy("n8n").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n8n/workflows", nil))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") || gotPath != "" {
		t.Errorf("ohne Anmeldung: %d %q (upstream %q)", rec.Code, rec.Header().Get("Location"), gotPath)
	}

	// pgAdmin: X-Script-Name, Weiterleitung bleibt unter /pgadmin/
	tool := adminTools[1]
	target := mustURL(t, up.URL)
	rec = httptest.NewRecorder()
	newPrefixProxy(target, tool).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pgadmin/login", nil))
	if gotScript != "/pgadmin" || gotPath != "/login" || rec.Header().Get("Location") != "/pgadmin/browser/" {
		t.Errorf("pgAdmin: script %q path %q location %q", gotScript, gotPath, rec.Header().Get("Location"))
	}
	if len(enabledAdminTools()) != 2 {
		t.Error("eingerichtete Werkzeuge nicht erkannt")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestServerConfigNextcloudAdminCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "nextcloud"}}, NCAdmin: ncAdminView{Managed: true, User: "pdhNCadmin", Checked: "21.10.2026 08:00 – angemeldet", AgentReady: true, Configured: true, DefaultUser: "pdhNCadmin"}}
	d.Tab = "nextcloud"
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"PDH-Administrator in Nextcloud", `action="/admin/server-config/nextcloud/admin"`, `value="rotate"`, "zuletzt geprüft: 21.10.2026 08:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("Server-Einstellungen enthalten %q nicht", want)
		}
	}
}

func TestAgentTokenNotEditable(t *testing.T) {
	var sys envGroup
	for _, g := range envGroups {
		if g.Key == "system" {
			sys = g
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/server-config/save", strings.NewReader("PDH_UPDATE_AGENT_TOKEN=fremd&PDH_UPDATE_AGENT_URL=http://x&PDH_BACKUP_DIR=backups"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	upd, err := collectEnvUpdates(req, sys)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := upd["PDH_UPDATE_AGENT_TOKEN"]; ok {
		t.Error("Agent-Token darf nicht über die Server-Einstellungen gespeichert werden")
	}
	if _, ok := upd["PDH_UPDATE_AGENT_URL"]; ok {
		t.Error("Agent-Adresse darf nicht über die Server-Einstellungen gespeichert werden")
	}
	if !config.AgentKey("PDH_UPDATE_AGENT_TOKEN") || config.AgentKey("PDH_BACKUP_DIR") {
		t.Error("AgentKey falsch")
	}
}
