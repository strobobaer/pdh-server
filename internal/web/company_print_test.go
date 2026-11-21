package web

import (
	"strings"
	"testing"
	"time"
)

func TestMaintFooterLines(t *testing.T) {
	at := time.Date(2026, 11, 21, 14, 30, 0, 0, time.Local)
	l1, l2 := maintFooterLines(&maintProtocol{}, at)
	if !strings.HasSuffix(l1, "· PDH") || l2 != "" {
		t.Errorf("ohne Firma: %q / %q", l1, l2)
	}
	p := &maintProtocol{Company: "Muster GmbH", Legal: "Registergericht: Augsburg HRB 1", Contact: "Anna Beispiel · Instandhaltung"}
	l1, l2 = maintFooterLines(p, at)
	if !strings.HasPrefix(l1, "Erstellt 21.11.2026 14:30") || !strings.HasSuffix(l1, "· Muster GmbH") {
		t.Errorf("Zeile 1: %q", l1)
	}
	if !strings.Contains(l2, "Registergericht: Augsburg HRB 1") || !strings.Contains(l2, "Ansprechpartner: Anna Beispiel") {
		t.Errorf("Zeile 2: %q", l2)
	}
}

func TestCompanyPrintFor(t *testing.T) {
	if got := companyPrintFor(companyProfile{}); got.Name != "" || got.Address != "" {
		t.Errorf("ohne Firma leer: %+v", got)
	}
	got := companyPrintFor(companyProfile{Name: "Muster GmbH", Street: "Hauptstr. 1", PostalCode: "86150", City: "Augsburg", Country: "Deutschland"})
	if got.Name != "Muster GmbH" || got.Address != "Hauptstr. 1 · 86150 Augsburg" {
		t.Errorf("Druckkopf: %+v", got)
	}
}

func TestContactLine(t *testing.T) {
	if got := contactLine(nil); got != "" {
		t.Errorf("ohne Person: %q", got)
	}
	got := contactLine(&contactPerson{Name: "Anna Beispiel", Title: "Instandhaltung", Phone: "+49 821 99-123", Email: "anna@firma.de"})
	if got != "Anna Beispiel · Instandhaltung · Tel. +49 821 99-123 · anna@firma.de" {
		t.Errorf("Ansprechpartner: %q", got)
	}
}

// Die Trennlinie im Kopf des Wartungsprotokolls liegt unter Text und Logo (vorher schnitt sie
// mit Logo die Anschriftszeile).
func TestMaintHeaderLineY(t *testing.T) {
	cases := []struct{ y, logoH, textBottom, want float64 }{
		{12, 0, 25, 27},  // ohne Logo, ohne Anschrift: wie bisher y+15
		{12, 14, 29, 30}, // Logo 14 mm, Text bis y+17: unter den Text
		{12, 30, 29, 44}, // hohes Logo: unter das Logo
	}
	for _, c := range cases {
		if got := maintHeaderLineY(c.y, c.logoH, c.textBottom); got != c.want {
			t.Errorf("maintHeaderLineY(%v,%v,%v) = %v, erwartet %v", c.y, c.logoH, c.textBottom, got, c.want)
		}
	}
}
