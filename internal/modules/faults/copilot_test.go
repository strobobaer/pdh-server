package faults

import "testing"

func TestParseAnalysis(t *testing.T) {
	r, err := parseAnalysis("Hier die Analyse:\n{\"summary\":\"Dichtung\",\"possible_causes\":[\"Dichtung verschlissen\",\" \",\"...\"],\"steps\":[{\"title\":\"Freischalten\"},{\"title\":\"Prüfen\"}],\"confidence\":85}\nViel Erfolg")
	if err != nil {
		t.Fatal(err)
	}
	if r.Confidence != 0.85 || len(r.PossibleCauses) != 1 || r.Steps[1].Order != 2 {
		t.Errorf("parseAnalysis: %+v", r)
	}
	if r, _ := parseAnalysis(`{"summary":"x","confidence":-3}`); r.Confidence != 0 {
		t.Error("Konfidenz nicht begrenzt")
	}
	if _, err := parseAnalysis(`{"summary":"","possible_causes":[],"steps":[]}`); err == nil {
		t.Error("leere Analyse muss Fehler sein")
	}
	if _, err := parseAnalysis("kein json"); err == nil {
		t.Error("kein JSON muss Fehler sein")
	}
}
