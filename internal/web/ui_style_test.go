package web

import "testing"

func TestUIStyleEffective(t *testing.T) {
	// Firmenstandard: leer oder unbekannt → Fiori
	if (Branding{}).StyleID() != "fiori" || (Branding{Style: "gibtsnicht"}).StyleID() != "fiori" {
		t.Error("Standard muss Fiori sein")
	}
	b := Branding{Style: "fluent"}
	if b.StyleID() != "fluent" {
		t.Errorf("Firmenstandard: %s", b.StyleID())
	}
	// eigene Wahl nur, wenn freigegeben und bekannt
	a := Appearance{Brand: Branding{}, Allow: true, User: UserAppearance{Style: "fluent"}}
	if a.StyleID() != "fluent" {
		t.Errorf("eigene Wahl: %s", a.StyleID())
	}
	a.Allow = false
	if a.StyleID() != "fiori" {
		t.Error("Sperre greift nicht")
	}
	a.Allow, a.User.Style = true, "gibtsnicht"
	if a.StyleID() != "fiori" {
		t.Error("unbekannter Stil übernommen")
	}
	if len(b.Styles()) != 2 || !validStyle("fluent") || !validStyle("fiori") || validStyle("") {
		t.Error("Stil-Liste")
	}
}
