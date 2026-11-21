package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

func TestCompanyPanelRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(d companyPanelData) string {
		var b bytes.Buffer
		if err := tmpl.ExecuteTemplate(&b, "company-panel", d); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	p := companyProfile{Name: "Muster GmbH", City: "Augsburg", VatID: "DE123456789"}
	view := render(companyPanelData{P: p, Groups: companyGroupsFor(p), Footer: companyFooterHTML(p, nil), Print: companyPrintFor(p)})
	if strings.Contains(view, "<form") || !strings.Contains(view, "Muster GmbH") || !strings.Contains(view, "DE123456789") {
		t.Errorf("Ansicht ohne Recht:\n%s", view)
	}
	edit := render(companyPanelData{P: p, Groups: companyGroupsFor(p), CanEdit: true, Footer: companyFooterHTML(p, nil), Print: companyPrintFor(p), Message: "Firmenstammdaten gespeichert"})
	for _, want := range []string{`hx-post="/infrastructure/company"`, `name="vat_id"`, `value="DE123456789"`, "Firmenstammdaten gespeichert", "Gilt für das ganze Unternehmen"} {
		if !strings.Contains(edit, want) {
			t.Errorf("Formular ohne %q", want)
		}
	}
}

func TestCompanySaveNeedsGlobalRight(t *testing.T) {
	h := &Handler{tmpl: loadTestTemplates(t)}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	post := func(allowed bool, form url.Values) *httptest.ResponseRecorder {
		h.companyEditHook = func(*http.Request) bool { return allowed }
		r := httptest.NewRequest(http.MethodPost, "/infrastructure/company", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = withTestUser(r)
		w := httptest.NewRecorder()
		h.CompanySaveWeb(w, r)
		return w
	}
	if w := post(false, url.Values{"name": {"Muster GmbH"}}); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht überall: %d", w.Code)
	}
	if w := post(true, url.Values{"name": {"Muster GmbH"}, "email": {"kein@"}}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "E-Mail") {
		t.Errorf("Prüfung: %d %s", w.Code, w.Body.String())
	}
}

func TestInfraDetailCompanyTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Werk", Type: "building", TypeLabel: "Gebäude"}, Types: infraTypes}
	if strings.Contains(renderPage(t, tmpl, "infra_detail", d), `data-tab="firma"`) {
		t.Error("Reiter Firma ohne Freigabe sichtbar")
	}
	d.CompanyTab = true
	out := renderPage(t, tmpl, "infra_detail", d)
	if !strings.Contains(out, `data-tab="firma"`) || !strings.Contains(out, `hx-get="/infrastructure/company"`) {
		t.Errorf("Reiter Firma fehlt")
	}
}

func TestCompanyTabOnlyTopLevel(t *testing.T) {
	if !companyTabFor("", true) || companyTabFor("p1", true) || companyTabFor("", false) {
		t.Error("Reiter Firma nur am obersten Knoten und mit Ansichtsrecht")
	}
}

func withTestUser(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "u1", Role: "admin", FirstName: "Anna", LastName: "Beispiel"})) //nolint:staticcheck // Schlüssel wie getUser
}
