package web

import (
	"strings"
	"testing"
)

func TestValidateCompany(t *testing.T) {
	cases := []struct {
		name    string
		p       companyProfile
		wantErr string
		check   func(p companyProfile) bool
	}{
		{name: "leer", p: companyProfile{}},
		{name: "E-Mail", p: companyProfile{Email: "kein@"}, wantErr: "E-Mail"},
		{name: "Website", p: companyProfile{Website: "firma.de"}, wantErr: "Website"},
		{name: "IBAN gut", p: companyProfile{IBAN: "DE89 3704 0044 0532 0130 00"},
			check: func(p companyProfile) bool { return p.IBAN == "DE89370400440532013000" }},
		{name: "IBAN falsch", p: companyProfile{IBAN: "DE89370400440532013001"}, wantErr: "IBAN"},
		{name: "USt-IdNr.", p: companyProfile{VatID: "de 123456789"},
			check: func(p companyProfile) bool { return p.VatID == "DE123456789" }},
		{name: "USt-IdNr. falsch", p: companyProfile{VatID: "123"}, wantErr: "USt-IdNr."},
		{name: "Name zu lang", p: companyProfile{Name: strings.Repeat("x", 201)}, wantErr: "Firma"},
		{name: "Leerzeichen", p: companyProfile{Name: "  Muster GmbH  ", Website: " https://firma.de "},
			check: func(p companyProfile) bool { return p.Name == "Muster GmbH" && p.Website == "https://firma.de" }},
	}
	for _, c := range cases {
		p := c.p
		err := validateCompany(&p)
		switch {
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: Fehler %v, erwartet mit %q", c.name, err, c.wantErr)
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unerwarteter Fehler %v", c.name, err)
		case c.check != nil && !c.check(p):
			t.Errorf("%s: Werte nicht bereinigt: %+v", c.name, p)
		}
	}
}

// Kopf der Startseite: Firma und Ort aus den Firmenstammdaten, nie ein fester Werksname.
func TestCompanyHeadline(t *testing.T) {
	for _, c := range []struct {
		p    companyProfile
		want string
	}{
		{companyProfile{Name: "Muster GmbH", City: "Ulm"}, "Muster GmbH · Ulm"},
		{companyProfile{Name: "Muster GmbH"}, "Muster GmbH"},
		{companyProfile{City: "Ulm"}, "Ulm"},
		{companyProfile{}, ""},
	} {
		if got := companyHeadline(c.p); got != c.want {
			t.Errorf("%+v: %q, erwartet %q", c.p, got, c.want)
		}
	}
}

func TestDashboardHeadShowsCompany(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "dashboard", DashboardData{DateStr: "Sonntag, 22. November 2026", CompanyLine: "Muster GmbH · Ulm"})
	if strings.Contains(out, "Augsburg") || !strings.Contains(out, "Sonntag, 22. November 2026 · Muster GmbH · Ulm") {
		t.Errorf("Kopf der Startseite ohne Firmendaten")
	}
	out = renderPage(t, tmpl, "dashboard", DashboardData{DateStr: "Sonntag, 22. November 2026"})
	if strings.Contains(out, "Augsburg") || strings.Contains(out, "2026 ·") {
		t.Errorf("ohne Firmendaten steht trotzdem ein Zusatz da")
	}
}
