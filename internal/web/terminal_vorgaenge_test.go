package web

import (
	"strings"
	"testing"
)

// Der Schalter „vorgaenge“ steht im Funktionen-Vertrag: Grundwert an, Teil der Antwort; ältere Terminals schicken
// ihn nicht, und unbekannte Schlüssel in einer Anfrage werden übergangen.
func TestTerminalFnVorgaengeInContract(t *testing.T) {
	set := defaultTerminalFns()
	if !set["vorgaenge"].Enabled || !set.flags().Vorgaenge {
		t.Error("Grundwert nicht an")
	}
	m, ok := terminalFnJSON(set)["vorgaenge"].(map[string]any)
	if !ok || m["enabled"] != true || m["updated_at"] == "" {
		t.Errorf("functions.vorgaenge fehlt oder ist unvollständig: %v", m)
	}
	in, err := parseTerminalFnBody([]byte(`{"functions":{"stoerung":{"enabled":false,"updated_at":"2026-01-01T00:00:00.000Z"},"schichtplan":{"enabled":true,"updated_at":"2026-01-01T00:00:00.000Z"}}}`))
	if err != nil {
		t.Fatalf("unbekannter Schlüssel lehnt die Anfrage ab: %v", err)
	}
	if _, has := in["vorgaenge"]; has || len(in) != 1 {
		t.Errorf("angenommene Schlüssel: %v", in)
	}
}

func TestVorgaengeGroupAndLabels(t *testing.T) {
	for st, want := range map[string]string{"detected": "open", "analyzing": "open", "open": "open", "in_progress": "work", "pending": "wait"} {
		if got := vorgGroupOf(st); got != want {
			t.Errorf("%s: %s", st, got)
		}
	}
	it := vorgItem{Type: "task", Group: "wait"}
	if it.StatusLabel() != "Wartet" || it.TypeLabel() != "Aufgabe" || it.Started() {
		t.Error("Beschriftung")
	}
	if !strings.Contains(vorgNames([]terminalPerson{{Name: "A"}, {Name: "B"}}), "A, B") {
		t.Error("Namen")
	}
	if a := (vorgActor{Modus: vorgModusNurKarte}); a.canWrite(true) || a.canWrite(false) {
		t.Error("nur_karte ohne Karte darf nicht schreiben")
	}
	if a := (vorgActor{Modus: vorgModusOhneKarte}); !a.canWrite(true) || a.canWrite(false) {
		t.Error("ohne_karte: nur Gerätesitzung")
	}
}
