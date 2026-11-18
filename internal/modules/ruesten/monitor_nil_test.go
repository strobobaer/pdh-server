package ruesten

import (
	"context"
	"errors"
	"testing"
)

// Ein fehlender Dienst für Störmeldungen darf die Überwachung nicht zum Absturz
// bringen (Auflage der Prüfung); es kommt ein Fehler, nichts wird angefasst.
func TestMonitorWithoutFaultCreator(t *testing.T) {
	m := &Monitor{} // weder Speicher noch Dienst
	n, err := m.Pass(context.Background())
	if n != 0 || !errors.Is(err, ErrNoFaultCreator) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	m.safePass(context.Background()) // darf nicht abstürzen
}
