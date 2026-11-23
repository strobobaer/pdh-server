package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

func TestConnectionInfoNoSecrets(t *testing.T) {
	cfg := map[string]string{
		"host": "10.0.0.5", "username": "maschinen", "password": "geheim123", "ext_password": "geheim123",
		"bearer_token": "geheim123", "password_enc": "v1:abc", "api_key_value": "",
		"base_url": "https://benutzer:geheim123@api.example.org/v1",
	}
	rep := &checkReport{At: "22.11.2026 10:00:00"}
	rep.add("Netz", "Anmeldung", "fail", "Anmeldung mit Kennwort geheim123 abgelehnt", 3)
	got := connectionInfoText("Zähler Halle 2", "mysql", true, cfg, rep, []connQuery{{ID: "Q1", Name: "Zählerstände", Spec: map[string]string{"sql": "SELECT 1"}}})
	if strings.Contains(got, "geheim123") || strings.Contains(got, "v1:abc") {
		t.Errorf("Geheimnis im Text:\n%s", got)
	}
	for _, want := range []string{"Zähler Halle 2", "Passwort: gesetzt", "API-Key: leer", "Benutzer: maschinen", "10.0.0.5", "Zählerstände", "Anmeldung"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q fehlt:\n%s", want, got)
		}
	}
}

func toolNames(direction string, canWrite bool) map[string]bool {
	out := map[string]bool{}
	for _, tl := range copilotConnTools(direction, canWrite) {
		out[tl.Name] = true
	}
	return out
}

func TestCopilotConnToolsWrite(t *testing.T) {
	read := toolNames("import", false)
	if read["propose_mappings"] || !read["connection_info"] || !read["source_values"] || !read["asset_tree"] || !read["mappings"] || read["export_sources"] {
		t.Errorf("Import ohne Schreibrecht: %v", read)
	}
	if w := toolNames("import", true); !w["propose_mappings"] {
		t.Errorf("Import mit Schreibrecht: %v", w)
	}
	if e := toolNames("export", true); !e["export_sources"] || !e["propose_mappings"] || e["source_values"] {
		t.Errorf("Export: %v", e)
	}
	for name := range toolNames("export", true) {
		if copilotToolLabels[name] == "" {
			t.Errorf("Beschriftung für %s fehlt", name)
		}
	}
}

func TestCopilotConnPromptRules(t *testing.T) {
	c := &copilotConn{Direction: "import", ID: "c1", Kind: "mqtt", Name: "Halle 2", CanWrite: true}
	p := copilotConnPrompt(c, true)
	for _, want := range []string{"propose_mappings", "Übernehmen", "Kennw", "Websuche", "Halle 2"} {
		if !strings.Contains(p, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	if strings.Contains(copilotConnPrompt(c, false), "Websuche") {
		t.Error("Websuche ohne Internet genannt")
	}
	c.CanWrite = false
	if strings.Contains(copilotConnPrompt(c, false), "propose_mappings") {
		t.Error("propose_mappings ohne Schreibrecht genannt")
	}
}

func TestProposalRowsFromInput(t *testing.T) {
	rows, err := proposalRowsFromInput(map[string]any{"rows": []any{
		map[string]any{"op": " ADD ", "source_ref": "halle/a/temp", "infrastructure_id": "a1", "name": "Temp", "reason": strings.Repeat("x", 900),
			"source_url": "javascript:alert(1)", "valid": true, "applied": true},
		map[string]any{"op": "change", "mapping_id": "m1", "note": "Einheit °C", "source_url": "https://example.org/liste"},
		"kaputt",
	}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0].Op != "add" || rows[0].SourceURL != "" || rows[0].Valid || rows[0].Applied || len([]rune(rows[0].Reason)) > 300 {
		t.Errorf("Zeile 0: %+v", rows[0])
	}
	if rows[1].SourceURL != "https://example.org/liste" || rows[1].MappingID != "m1" {
		t.Errorf("Zeile 1: %+v", rows[1])
	}
	if _, err := proposalRowsFromInput(map[string]any{}); err == nil {
		t.Error("leere Liste angenommen")
	}
	big := make([]any, 501)
	for i := range big {
		big[i] = map[string]any{"op": "keep"}
	}
	if _, err := proposalRowsFromInput(map[string]any{"rows": big}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("501 Zeilen: %v", err)
	}
}

func TestCopilotAskConnParam(t *testing.T) {
	ask := func(h *Handler, conn string) *httptest.ResponseRecorder {
		form := url.Values{"q": {"Was fehlt?"}, "conn": {conn}}
		r := httptest.NewRequest("POST", "/copilot/ask", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.CopilotAskWeb(w, r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "u1", Role: "viewer"})))
		return w
	}
	denied := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	for _, bad := range []string{"import:kaputt", "sync:00000000-0000-0000-0000-000000000001", "import:00000000-0000-0000-0000-000000000001x"} {
		if w := ask(denied, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := ask(denied, "export:00000000-0000-0000-0000-000000000001"); w.Code != http.StatusForbidden {
		t.Errorf("ohne Leserecht: %d %s", w.Code, w.Body.String())
	}
}

func TestCopilotSourceLabels(t *testing.T) {
	got := copilotSourceLabels([]string{"connection_info", "web_search", "web_fetch", "propose_mappings", "fremd"})
	want := []string{"Verbindung", "Internet", "Vorschlag", "fremd"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%v", got)
	}
}

// Seitenleiste auf einer Verbindungsseite: die Frage nennt die Verbindung, die
// Änderungsliste kann gezeichnet und übernommen werden, der Hinweis zum
// Datenschutz steht da – auf anderen Seiten nicht.
func TestCopilotSidebarConn(t *testing.T) {
	tmpl := loadTestTemplates(t)
	const id = "11111111-1111-1111-1111-111111111111"
	const hint = "Auf dieser Seite gehen Quellwerte und Anlagennamen an Anthropic – Kennwörter nie."
	d := ConnectionDetailData{Direction: "import", ID: id, Name: "Broker", Kind: "mqtt", KindLabel: "MQTT", CanWrite: true, Tab: "mappings"}
	d.CopilotConn = "import:" + id
	page := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{"conn: 'import:" + id + "'", "function copRenderProposal", "/copilot/proposals/", "/apply", hint, "Seite neu laden", "Quellen aus dem Internet"} {
		if !strings.Contains(page, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	checkScripts(t, "copilot-sidebar", page)
	d.CopilotConn = ""
	if page := renderPage(t, tmpl, "connection_detail", d); strings.Contains(page, hint) || !strings.Contains(page, "conn: ''") {
		t.Error("ohne Verbindung: kein Hinweis, leere Kennung")
	}
}

// Schlussprüfung I3: Geheimnisse außerhalb der Geheimfelder (Kennwort oder
// Token in einer Adresse, Token in einer Abfrage), kurze Kennwörter und
// Kennwörter mit Sonderzeichen in JSON-Ausgaben dürfen nicht durchrutschen.
func TestRedactSecretsBeyondFields(t *testing.T) {
	cfg := map[string]string{
		"password": `p"a<ss&1`, "ext_password": "abc",
		"base_url": "https://benutzer:AnderesKennwort@api.example.org/v1?apikey=tok-12345&seite=2",
	}
	queries := []connQuery{{ID: "Q1", Name: "Werte", Spec: map[string]string{"path": "/daten?token=abc999&von=heute"}}}
	info := connectionInfoText("API", "rest_api", true, cfg, nil, queries)
	for _, leak := range []string{"AnderesKennwort", "tok-12345", "abc999"} {
		if strings.Contains(info, leak) {
			t.Errorf("%q steht im Text:\n%s", leak, info)
		}
	}
	for _, keep := range []string{"api.example.org", "seite=2", "von=heute", "benutzer"} {
		if !strings.Contains(info, keep) {
			t.Errorf("%q fehlt (zu viel entfernt):\n%s", keep, info)
		}
	}
	out, err := jsonOut(map[string]any{"hinweise": []string{`Anmeldung mit p"a<ss&1 abgelehnt`, "Kennwort abc falsch", "Fabcd bleibt", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc"}})
	if err != nil {
		t.Fatal(err)
	}
	got := redactSecrets(out, cfg)
	for _, leak := range []string{`a<ss`, `a<ss`, "Kennwort abc falsch", "eyJhbGciOiJIUzI1NiJ9"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q steht in der Ausgabe: %s", leak, got)
		}
	}
	if !strings.Contains(got, "Fabcd bleibt") {
		t.Errorf("kurzes Kennwort darf nur als eigenes Wort ersetzt werden: %s", got)
	}
}
