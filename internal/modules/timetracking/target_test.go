package timetracking

import "testing"

func sp(s string) *string { return &s }

func TestDisplayTitleUndTypeLabel(t *testing.T) {
	cases := []struct {
		name        string
		e           TimeEntry
		title, kind string
	}{
		{"alter Eintrag ohne Ziel", TimeEntry{RefType: RefProduction, Description: "Rüsten"}, "Rüsten", "production"},
		{"Vorgang", TimeEntry{RefType: RefFault, Description: "Störung 12"}, "Störung 12", "fault"},
		{"Anlage mit Vorspann", TimeEntry{RefType: RefProduction, TargetKind: sp("plant"), Description: "Anlage: Halle › Presse"}, "Anlage: Halle › Presse", "Anlage"},
		{"Anlage ohne Vorspann (bearbeitet)", TimeEntry{RefType: RefProduction, TargetKind: sp("plant"), Description: "Halle › Presse"}, "Anlage: Halle › Presse", "Anlage"},
		{"IT-Asset", TimeEntry{RefType: RefProduction, TargetKind: sp("it_asset"), Description: "IT-Asset: Router"}, "IT-Asset: Router", "IT-Asset"},
		{"Sonstiges mit Freitext", TimeEntry{RefType: RefProduction, TargetKind: sp("other"), Description: "Halle kehren"}, "Sonstiges: Halle kehren", "Sonstiges"},
		{"Sonstiges leer", TimeEntry{RefType: RefProduction, TargetKind: sp("other")}, "Sonstiges", "Sonstiges"},
		{"unbekannte Art", TimeEntry{RefType: RefProduction, TargetKind: sp("zz"), Description: "x"}, "x", "production"},
	}
	for _, c := range cases {
		if got := c.e.DisplayTitle(); got != c.title {
			t.Errorf("%s: Titel %q, erwartet %q", c.name, got, c.title)
		}
		if got := c.e.TypeLabel(); got != c.kind {
			t.Errorf("%s: Typ %q, erwartet %q", c.name, got, c.kind)
		}
	}
}

func TestValidTarget(t *testing.T) {
	for _, k := range []string{"plant", "it_asset", "other"} {
		if !ValidTarget(k) {
			t.Errorf("%q soll gültig sein", k)
		}
	}
	for _, k := range []string{"", "production", "asset", "Plant"} {
		if ValidTarget(k) {
			t.Errorf("%q soll ungültig sein", k)
		}
	}
}
