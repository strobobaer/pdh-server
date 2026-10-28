package web

import "testing"

func TestUIStyleEffective(t *testing.T) {
	// Firmenstandard: leer oder unbekannt → Fluent
	if (Branding{}).StyleID() != "fluent" || (Branding{Style: "gibtsnicht"}).StyleID() != "fluent" {
		t.Error("Standard muss Fluent sein")
	}
	b := Branding{Style: "fiori"}
	if b.StyleID() != "fiori" {
		t.Errorf("Firmenstandard: %s", b.StyleID())
	}
	// eigene Wahl nur, wenn freigegeben und bekannt
	a := Appearance{Brand: Branding{}, Allow: true, User: UserAppearance{Style: "fiori"}}
	if a.StyleID() != "fiori" {
		t.Errorf("eigene Wahl: %s", a.StyleID())
	}
	a.Allow = false
	if a.StyleID() != "fluent" {
		t.Error("Sperre greift nicht")
	}
	a.Allow, a.User.Style = true, "gibtsnicht"
	if a.StyleID() != "fluent" {
		t.Error("unbekannter Stil übernommen")
	}
	if len(b.Styles()) != 2 || !validStyle("fluent") || !validStyle("fiori") || validStyle("") {
		t.Error("Stil-Liste")
	}
}
