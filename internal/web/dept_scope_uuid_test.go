package web

import (
	"strings"
	"testing"
)

// recordInScope vergleicht als UUID: Groß-/Klammerschreibweise einer ID aus Formular, JSON oder
// Abfrage (z. B. das Copilot-Feld fault) umgeht den Abteilungsfilter nicht mehr; was keine UUID ist,
// gilt als nicht sichtbar; eine gültige, aber unbekannte ID bleibt erlaubt (der Handler meldet 404).
func TestRecordInScopeUUIDSpellings(t *testing.T) {
	p := newPlacesFx(t)
	foreign := p.named("SCOPEB", p.infraB, "")
	own := p.named("SCOPEA", p.infraA, "")
	s := &deptScope{UserID: p.none.ID, Departments: []string{p.deptA}}
	ctx := t.Context()
	for _, id := range []string{foreign, strings.ToUpper(foreign), "{" + foreign + "}", strings.ReplaceAll(foreign, "-", ""), "urn:uuid:" + foreign} {
		if p.h.recordInScope(ctx, s, "project", id) {
			t.Errorf("fremdes Projekt %q gilt als sichtbar", id)
		}
	}
	for _, id := range []string{"keine-uuid", "", "x' OR '1'='1"} {
		if p.h.recordInScope(ctx, s, "project", id) {
			t.Errorf("ungültige ID %q gilt als sichtbar", id)
		}
	}
	if !p.h.recordInScope(ctx, s, "project", own) || !p.h.recordInScope(ctx, s, "project", strings.ToUpper(own)) {
		t.Errorf("eigenes Projekt gilt als unsichtbar")
	}
	if !p.h.recordInScope(ctx, s, "project", "01a18200-0000-7000-8000-0000000000ff") {
		t.Errorf("unbekannte gültige ID muss erlaubt bleiben")
	}
}
