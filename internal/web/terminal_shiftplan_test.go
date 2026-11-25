package web

import "testing"

// Adressen der Schichtplan-Seite: Abteilung, Woche und Plan zusammen; „plan=alle“ nur, wenn ein Grundplan gilt.
func TestTerminalPlanHref(t *testing.T) {
	const d, p = "0190aaaa-0000-7000-8000-000000000001", "0190bbbb-0000-7000-8000-000000000002"
	for _, c := range []struct {
		name, def, dept, plan, want string
		next                        bool
	}{
		{"leer", "", "", "", "/terminal/schichtplan", false},
		{"nur Abteilung", "", d, "", "/terminal/schichtplan?abt=" + d, false},
		{"Abteilung, Woche, Plan", "", d, p, "/terminal/schichtplan?abt=" + d + "&plan=" + p + "&woche=naechste", true},
		{"Plan ohne Woche", "", d, p, "/terminal/schichtplan?abt=" + d + "&plan=" + p, false},
		{"Alle bei Grundplan", p, d, "", "/terminal/schichtplan?abt=" + d + "&plan=alle&woche=naechste", true},
		{"Alle ohne Grundplan, nächste Woche", "", d, "", "/terminal/schichtplan?abt=" + d + "&woche=naechste", true},
	} {
		if got := (terminalPlan{DefaultPlan: c.def}).Href(c.dept, c.next, c.plan); got != c.want {
			t.Errorf("%s: %s, erwartet %s", c.name, got, c.want)
		}
	}
}
