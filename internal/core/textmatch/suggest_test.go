package textmatch

import (
	"testing"
	"time"
)

func TestSuggester(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)
	s := NewSuggester([]Sample{
		{"Filter getauscht", now},
		{"Filter getauscht", now},
		{"Filter gereinigt", old},
		{"Keilriemen nachgespannt", now},
		{"Keilriemen getauscht", old},
		{"Hydraulikpumpe undicht. Dichtung am Flansch getauscht und Öl nachgefüllt.", now},
	}, false, now)

	r := s.Suggest("Fil", 5)
	if r.Ghost != "ter getauscht" || len(r.Items) != 2 || r.Items[0] != "Filter getauscht" {
		t.Fatalf("Präfix: %+v", r)
	}
	// Feedback hebt die seltenere Formulierung nach vorn
	s.Feedback("Filter gereinigt", 5)
	if r = s.Suggest("filter", 5); r.Items[0] != "Filter gereinigt" || r.Ghost != " gereinigt" {
		t.Fatalf("nach Feedback: %+v", r)
	}
	// naechstes Wort nach Leerzeichen
	if r = s.Suggest("Neuer Keilriemen ", 5); r.Ghost != "nachgespannt" && r.Ghost != "getauscht" {
		t.Fatalf("nächstes Wort: %+v", r)
	}
	// Wortende mitten im Wort, keine passende Formulierung
	if r = s.Suggest("Neue Dicht", 5); r.Ghost != "ung" {
		t.Fatalf("Wortende: %+v", r)
	}
	// enthaelt alle Woerter (nicht am Anfang)
	if r = s.Suggest("Dichtung Flansch", 5); len(r.Items) != 1 || r.Items[0] != "Dichtung am Flansch getauscht und Öl nachgefüllt." {
		t.Fatalf("enthält: %+v", r)
	}
	// Umlaute/Grossschreibung egal, Satz-Teilung
	if r = s.Suggest("hydraulikpu", 5); r.Ghost != "mpe undicht." {
		t.Fatalf("Satz: %+v", r)
	}
	if r = s.Suggest("x", 5); r.Ghost != "" || len(r.Items) != 0 {
		t.Fatal("zu kurz")
	}
	// neue Formulierung per Feedback gelernt
	s.Feedback("Lüfter gereinigt", 1)
	if r = s.Suggest("Lüf", 5); r.Ghost != "ter gereinigt" {
		t.Fatalf("Feedback lernt: %+v", r)
	}
}

func TestSuggesterLines(t *testing.T) {
	s := NewSuggester([]Sample{{"- Gefahren beim Rangieren\n- Lastdiagramm lesen\n• Tägliche Sichtprüfung", time.Now()}}, true, time.Now())
	if s.Size() != 3 {
		t.Fatalf("Zeilen: %d", s.Size())
	}
	if r := s.Suggest("- Last", 5); r.Ghost != "diagramm lesen" {
		t.Fatalf("Stichpunkt: %+v", r)
	}
}
