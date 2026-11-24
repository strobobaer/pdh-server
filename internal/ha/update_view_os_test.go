package ha

import (
	"reflect"
	"testing"
	"time"
)

func TestViewOSKind(t *testing.T) {
	steps := []string{"Abmelden", "Pakete einspielen", "Neustart", "Prüfen", "Freigabe"}
	cases := []struct {
		name    string
		phase   UpdatePhase
		osStep  string
		step    int
		percent int
		text    string
	}{
		{"Prüfen", UpPrepare, "", 0, 0, "Betriebssystem wird geprüft"},
		{"Countdown", UpCountdown, "stop", 0, 5, "Abmeldung in Kürze – bitte Arbeit speichern"},
		{"Einspielen", UpLocked, "install", 1, 25, "Betriebssystem-Pakete werden eingespielt"},
		{"Stoppen", UpLocked, "stop", 1, 25, "Betriebssystem-Pakete werden eingespielt"},
		{"Neustart", UpLocked, "boot", 2, 55, "Server startet neu"},
		{"Vor dem Neustart", UpLocked, "reboot", 2, 55, "Server startet neu"},
		{"Prüfen danach", UpLocked, "verify", 3, 75, "Server wird geprüft"},
		{"Starten", UpLocked, "start", 3, 75, "Server wird geprüft"},
		{"Freigabe", UpRelease, "done", 4, 90, "Freigabe"},
		{"Fertig", UpDone, "done", 4, 100, "Betriebssystem-Update abgeschlossen"},
		{"Abgebrochen", UpFailed, "done", 4, 100, "Betriebssystem-Update abgebrochen"},
	}
	for _, c := range cases {
		j := UpdateJob{Kind: UpdateOS, Phase: c.phase, OSStep: c.osStep, PhaseAt: upT0, LockedAt: upT0}
		v := j.View(upT0.Add(time.Minute), time.Hour, nil)
		if v.Kind != "os" || !reflect.DeepEqual(v.Steps, steps) || v.Step != c.step || v.Percent != c.percent || v.Text != c.text {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	if n := (UpdateJob{Kind: UpdateOS}).Name(); n != "Betriebssystem-Update" {
		t.Fatalf("Name: %q", n)
	}
}
