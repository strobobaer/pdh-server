package ha

import (
	"testing"
	"time"
)

// PDH-Auftrag mit Betriebssystem-Teil: in der Freigabe zeigt die Anzeige den Schritt der Pakete.
func TestViewSequentialWithOSStep(t *testing.T) {
	cases := []struct {
		osStep  string
		percent int
		text    string
	}{
		{"", 90, "Freigabe – Spiegel holt auf"},
		{"stop", 92, "Betriebssystem-Pakete werden eingespielt"},
		{"install", 92, "Betriebssystem-Pakete werden eingespielt"},
		{"reboot", 92, "Server startet neu"},
		{"boot", 92, "Server startet neu"},
		{"start", 92, "Server wird geprüft"},
		{"verify", 92, "Server wird geprüft"},
	}
	for _, c := range cases {
		j := UpdateJob{Kind: UpdateSequential, Phase: UpRelease, OSStep: c.osStep, PhaseAt: upT0, LockedAt: upT0}
		v := j.View(upT0.Add(time.Minute), time.Hour, nil)
		if v.Step != 4 || v.Percent != c.percent || v.Text != c.text {
			t.Errorf("OSStep %q: %+v", c.osStep, v)
		}
	}
}
