package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

// terminalTestHandler: Handler ohne Datenbank, nur mit Vorlagen.
func terminalTestHandler(t *testing.T) (*Handler, chi.Router) {
	t.Helper()
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	h := &Handler{tmpl: loadTestTemplates(t), jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.terminalRoutes(r)
	return h, r
}

func terminalGet(r http.Handler, target string, u *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTerminalRequiresSystemUser(t *testing.T) {
	_, r := terminalTestHandler(t)
	normal := &users.User{ID: "11111111-1111-1111-1111-111111111111", Role: users.Role("admin"), FirstName: "A", LastName: "B"}
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu", "/terminal/info.json"} {
		rec := terminalGet(r, p, normal)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur für Terminal-Benutzer") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestTerminalWithoutInfra(t *testing.T) {
	_, r := terminalTestHandler(t)
	sys := &users.User{ID: "22222222-2222-2222-2222-222222222222", Role: users.Role("viewer"), IsSystemUser: true}
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		rec := terminalGet(r, p, sys)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Diesem Terminal ist keine Anlage zugeordnet") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	// JSON: klare Antwort statt 500
	rec := terminalGet(r, "/terminal/info.json", sys)
	if rec.Code != http.StatusNotFound {
		t.Errorf("info.json ohne Anlage: %d", rec.Code)
	}
}

func TestTerminalTemplateIdleAndEscaping(t *testing.T) {
	h, _ := terminalTestHandler(t)
	d := terminalPage{Mode: "info", Idle: 45, CanFaults: true, CanTickets: true, EasyMode: true, InfraID: "33333333-3333-3333-3333-333333333333", Name: `<b>Presse</b>`, Path: `A › "B"`,
		Info: terminalInfo{Faults: []terminalItem{{ID: "f1", Title: `<script>alert(1)</script>`, Path: "Unter", StatusLabel: "Offen", Age: "vor 3 h"}}, FaultsTotal: 60}}
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, d)
	body := rec.Body.String()
	if !strings.Contains(body, `data-idle="45"`) {
		t.Error("data-idle fehlt")
	}
	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<b>Presse</b>") {
		t.Error("nicht maskiert")
	}
	if !strings.Contains(body, "59 weitere") {
		t.Errorf("Hinweis auf weitere Einträge fehlt")
	}
	// Schoner: kein Inaktivitäts-Timer
	d.Mode, d.Idle = "schoner", 0
	rec = httptest.NewRecorder()
	h.renderTerminal(rec, d)
	if !strings.Contains(rec.Body.String(), `data-idle="0"`) {
		t.Error("Schoner darf keinen Timer haben")
	}
}

func TestTerminalAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "gerade eben", 12 * time.Minute: "vor 12 min", 3 * time.Hour: "vor 3 h",
		24 * time.Hour: "vor 1 Tag", 48 * time.Hour: "vor 2 Tagen", -time.Minute: "gerade eben",
	} {
		if got := terminalAge(now, now.Add(-d)); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestTerminalRelPath(t *testing.T) {
	nodes := map[string]terminalNode{
		"a": {Name: "Halle", Parent: ""}, "b": {Name: "Linie", Parent: "a"}, "c": {Name: "Presse", Parent: "b"},
	}
	if got := terminalRelPath(nodes, "c", "b"); got != "Presse" {
		t.Errorf("rel: %q", got)
	}
	if got := terminalRelPath(nodes, "b", "b"); got != "" {
		t.Errorf("Wurzel: %q", got)
	}
	if got := terminalRelPath(nodes, "c", "a"); got != "Linie › Presse" {
		t.Errorf("tief: %q", got)
	}
}
