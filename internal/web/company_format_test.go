package web

import (
	"strings"
	"testing"
)

func TestCompanyAddressLine(t *testing.T) {
	cases := []struct {
		p    companyProfile
		want string
	}{
		{companyProfile{Street: "Hauptstr. 1", PostalCode: "86150", City: "Augsburg", Country: "Deutschland"}, "Hauptstr. 1 · 86150 Augsburg"},
		{companyProfile{Street: "Ring 2", PostalCode: "1010", City: "Wien", Country: "Österreich"}, "Ring 2 · 1010 Wien · Österreich"},
		{companyProfile{City: "Augsburg"}, "Augsburg"},
		{companyProfile{}, ""},
	}
	for _, c := range cases {
		if got := companyAddressLine(c.p); got != c.want {
			t.Errorf("companyAddressLine(%+v) = %q, erwartet %q", c.p, got, c.want)
		}
	}
}

func TestCompanyLegalLine(t *testing.T) {
	full := companyProfile{RegisteredOffice: "Augsburg", RegisterCourt: "Augsburg", RegisterNo: "HRB 12345",
		Management: "Anna Beispiel", SupervisoryChair: "Max Muster", VatID: "DE123456789"}
	want := "Sitz: Augsburg · Registergericht: Augsburg HRB 12345 · Geschäftsführung: Anna Beispiel · Vorsitz Aufsichtsrat: Max Muster · USt-IdNr.: DE123456789"
	if got := companyLegalLine(full); got != want {
		t.Errorf("alle Felder:\n%q\nerwartet\n%q", got, want)
	}
	if got := companyLegalLine(companyProfile{RegisterCourt: "Augsburg", RegisterNo: "HRB 12345"}); got != "Registergericht: Augsburg HRB 12345" {
		t.Errorf("nur Register: %q", got)
	}
	if got := companyLegalLine(companyProfile{}); got != "" {
		t.Errorf("leer: %q", got)
	}
}

func TestContactPhone(t *testing.T) {
	cases := []struct{ phone, internal, company, want string }{
		{"+49 821 1", "", "", "+49 821 1"},
		{"+49 821 1", "123", "+49 821 99", "+49 821 1"},
		{"", "123", "+49 821 99", "+49 821 99-123"},
		{"", "123", "", "123"},
		{"", "", "+49 821 99", ""},
	}
	for _, c := range cases {
		if got := contactPhone(c.phone, c.internal, c.company); got != c.want {
			t.Errorf("contactPhone(%q,%q,%q) = %q, erwartet %q", c.phone, c.internal, c.company, got, c.want)
		}
	}
}

func TestCompanyFooterHTML(t *testing.T) {
	if got := companyFooterHTML(companyProfile{City: "Augsburg"}, nil); got != "" {
		t.Errorf("ohne Firmennamen keine Fußzeile: %q", got)
	}
	p := companyProfile{Name: `Muster & Söhne <GmbH>`, Street: "Hauptstr. 1", PostalCode: "86150", City: "Augsburg",
		Phone: "+49 821 99", Email: "info@firma.de", RegisterCourt: "Augsburg", RegisterNo: "HRB 1"}
	got := string(companyFooterHTML(p, &contactPerson{Name: "Anna Beispiel", Title: "Instandhaltung", Email: "anna@firma.de"}))
	for _, want := range []string{"Muster &amp; Söhne &lt;GmbH&gt;", "Ansprechpartner", "Anna Beispiel", "Instandhaltung", "Hauptstr. 1 · 86150 Augsburg", "Registergericht: Augsburg HRB 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("Fußzeile ohne %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<GmbH>") {
		t.Errorf("Firmenname nicht maskiert:\n%s", got)
	}
	noPerson := string(companyFooterHTML(p, nil))
	if !strings.Contains(noPerson, "Kontakt") || strings.Contains(noPerson, "Ansprechpartner") {
		t.Errorf("ohne Person soll der Firmenkontakt stehen:\n%s", noPerson)
	}
	if strings.Contains(noPerson, " ·  · ") || strings.Contains(noPerson, "· </") {
		t.Errorf("leere Trenner:\n%s", noPerson)
	}
}

// Ohne Ansprechpartner stehen Telefon und E-Mail der Firma nur einmal in der Fußzeile.
func TestCompanyFooterNoDuplicateContact(t *testing.T) {
	p := companyProfile{Name: "Muster GmbH", Phone: "+49 821 99", Email: "info@firma.de", Website: "https://firma.de"}
	got := string(companyFooterHTML(p, nil))
	if n := strings.Count(got, "info@firma.de"); n != 1 {
		t.Errorf("E-Mail %d-mal in der Fußzeile:\n%s", n, got)
	}
	if !strings.Contains(got, "https://firma.de") {
		t.Errorf("Website fehlt:\n%s", got)
	}
}
