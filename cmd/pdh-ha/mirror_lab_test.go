package main

import (
	"context"
	"testing"

	"pdh/internal/ha"
)

// Im Labor gefunden: Ein Rollentausch, solange die Spiegelung aus ist, ist kein Wechsel
// „vor dem Erstabgleich“. Wird sie danach am aktiven Server eingeschaltet, ist das ein
// normaler Erstabgleich mit --delete und ohne Alarm.
func TestMirrorSwapWhileOffNotPromoted(t *testing.T) {
	f := newFix2(t)
	w := f.worker()
	off := fix2Settings
	off.MirrorOn = false
	w.set(false, ha.Passive, 1, off)
	w.set(false, ha.Active, 2, off) // Tausch bei ausgeschalteter Spiegelung
	w.set(true, ha.Active, 2, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, nodel := uploadsCalls(f.rs); del != 1 || nodel != 0 {
		t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
	}
	if n := f.alarms("wurde aktiv"); n != 0 {
		t.Fatalf("falscher Alarm: %v", f.ev.readAll())
	}
}

func TestMirrorRTPromotedOnlyWhileOn(t *testing.T) {
	rt := newMirrorRT(mirrorWiring{repo: t.TempDir(), statePath: t.TempDir() + "/mirror.json"}, testEvents(t), nil)
	rt.publish(ha.Passive, &mirrorWant{Role: ha.Passive})
	rt.publish(ha.Passive, &mirrorWant{Role: ha.Active})
	if rt.takePromoted() {
		t.Fatal("Wechsel bei ausgeschalteter Spiegelung zählt")
	}
	rt.publish(ha.Active, &mirrorWant{On: true, Role: ha.Passive})
	rt.publish(ha.Passive, &mirrorWant{On: true, Role: ha.Active})
	if !rt.takePromoted() {
		t.Fatal("Wechsel bei eingeschalteter Spiegelung zählt nicht")
	}
}

