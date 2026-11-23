package web

import (
	"testing"
	"time"

	"pdh/internal/modules/calc"
)

// Der Anstoß darf nie blockieren: MQTT liefert Werte im Sturm, und am Spiegel
// oder in Tests läuft gar kein Rechenwerk.
func TestCalcNudgeNeverBlocks(t *testing.T) {
	(&Handler{}).calcNudge() // ohne Rechenwerk wirkungslos

	var none *calc.Engine
	none.Nudge()

	h := &Handler{}
	h.calcEngine.Store(calc.NewEngine(nil, nil)) // nicht gestartet: niemand holt die Anstöße ab
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.calcNudge()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("1000 Anstöße blockieren")
	}
	if (&Handler{}).calcStore() != nil {
		t.Error("ohne Datenbank gibt es keinen Speicher")
	}
}
