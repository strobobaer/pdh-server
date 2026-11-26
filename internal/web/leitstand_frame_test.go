package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Leitstand im Rahmen (Entwurf 11.4): Band mit Bearbeiten, Als Standard, Vollbild; ohne
// Anmeldung nur der Leitstand und links unten „Anmelden“; eingebettet ohne eigenen Kopf.

func TestLeitstandBandActions(t *testing.T) {
	b := &BaseData{Page: "leitstand", LeitstandID: "lid-1", CanManageLeitstand: true}
	a := pageActions(b)
	if !hasAction(a.Band, "leit-standard") || !hasHref(a.Band, "/global/?vollbild=1") || !hasHref(a.Band, "/leitstaende/lid-1") {
		t.Errorf("Leitstand-Band unvollständig: %+v", a.Band)
	}
	b.CanManageLeitstand = false
	if a := pageActions(b); hasAction(a.Band, "leit-standard") || hasHref(a.Band, "/leitstaende/lid-1") || !hasHref(a.Band, "/global/?vollbild=1") {
		t.Errorf("ohne Recht: %+v", a.Band)
	}
}

func renderGlobal(t *testing.T, d GlobalDashboardPageData) string {
	t.Helper()
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "global_dashboard.gohtml", d); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestGlobalEmbeddedHidesHeader(t *testing.T) {
	out := renderGlobal(t, GlobalDashboardPageData{LoggedIn: true, Embedded: true})
	if !strings.Contains(out, "html.gd-embedded header{display:none}") || strings.Contains(out, "gd-login-corner") {
		t.Error("eingebettet mit eigenem Kopf oder Anmelde-Ecke")
	}
	if !strings.Contains(renderGlobal(t, GlobalDashboardPageData{LoggedIn: true}), "<header>") {
		t.Error("allein ohne Kopf")
	}
}

func TestGlobalLoginCorner(t *testing.T) {
	if !strings.Contains(renderGlobal(t, GlobalDashboardPageData{LoginCorner: true}), `class="gd-login-corner"`) {
		t.Error("ohne Anmeldung fehlt „Anmelden“ links unten")
	}
	if strings.Contains(renderGlobal(t, GlobalDashboardPageData{}), "gd-login-corner") {
		t.Error("Anmelde-Ecke ohne Vorgabe „neu“")
	}
}

// „Als Standard für alle setzen“ nur mit leitstand.manage.
func TestLeitstandDefaultNeedsRight(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	r.Post("/leitstaende/{id}/default", h.LeitstandDefaultWeb)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/leitstaende/01a2a2a2-0000-7000-8000-000000000001/default", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", rec.Code)
	}
}

func TestLeitstandCornerRule(t *testing.T) {
	for _, c := range []struct {
		logged, newFrame, full, embed, want bool
	}{{false, true, false, false, true}, {true, true, false, false, false}, {false, false, false, false, false},
		{false, true, true, false, false}, {false, true, false, true, false}} {
		if got := leitstandLoginCorner(c.logged, c.newFrame, c.full, c.embed); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
