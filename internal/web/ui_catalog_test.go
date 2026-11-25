package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Seite „Gestaltung“ (Bausteinkatalog): nur mit system.ui_defaults, zeigt die
// Gestaltungswerte und Grundbausteine; der Menüeintrag erscheint nur mit dem Recht.
func TestUICatalogPerm(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	rec := httptest.NewRecorder()
	h.UICatalogPage(rec, httptest.NewRequest("GET", "/admin/gestaltung", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: HTTP %d", rec.Code)
	}

	out := renderPage(t, loadTestTemplates(t), "ui_catalog", UICatalogData{BaseData: BaseData{Page: "ui-catalog", Title: "Gestaltung"}})
	for _, want := range []string{"<h2>Farben</h2>", "<h2>Abstände</h2>", "<h2>Schrift</h2>", "<h2>Ecken und Schatten</h2>", "<h2>Bausteine</h2>",
		"var(--accent)", "--ok-soft", "var(--sp-2)", "var(--fs-xl)", "var(--r-m)", `class="btn btn-primary"`, `class="badge b-green"`, `class="rec-tabs"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Gestaltung enthält %q nicht", want)
		}
	}

	if strings.Contains(out, "ZgotmplZ") {
		t.Error("Stilangabe von html/template verworfen (ZgotmplZ)")
	}

	d, ok := navDefByKey("ui-catalog")
	if !ok || d.Href != "/admin/gestaltung" || d.Page != "ui-catalog" || d.group != "admin" {
		t.Fatalf("Menüeintrag: %+v", d)
	}
	if !d.show(&BaseData{navCan: func(p string) bool { return p == permUIDefaults }}) || d.show(&BaseData{navCan: func(string) bool { return false }}) {
		t.Error("Menüeintrag hängt nicht am Recht system.ui_defaults")
	}
}
