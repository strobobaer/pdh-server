package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// Im Labor gefunden: Eine neue Bandbreitengrenze galt erst für den nächsten Lauf. Ein
// Lauf mit der alten, kleinen Grenze hielt den Abgleich (und den nächsten Tausch)
// minutenlang auf, obwohl die Grenze schon aufgehoben war.
func TestMirrorNewBandwidthRestartsRun(t *testing.T) {
	f := newFix2(t)
	w := f.worker()
	slow := fix2Settings
	slow.MirrorBWLimitKB = 200
	started := make(chan struct{}, 8)
	f.rs.mu.Lock()
	f.rs.fn = func(ctx context.Context, argv []string) error {
		if !strings.Contains(strings.Join(argv, " "), "--bwlimit=200") {
			return nil
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	f.rs.mu.Unlock()
	w.set(true, ha.Active, 1, slow)
	done := make(chan error, 1)
	go func() { done <- w.runAll(context.Background()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Lauf beginnt nicht")
	}

	// andere Einstellung: der Lauf läuft weiter
	other := slow
	other.MirrorTrashDays = slow.MirrorTrashDays + 1
	w.set(true, ha.Active, 1, other)
	select {
	case err := <-done:
		t.Fatalf("Lauf endet ohne neue Grenze: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	free := other
	free.MirrorBWLimitKB = 0
	w.set(true, ha.Active, 1, free)
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, errMirrorAborted) {
			t.Fatalf("Abbruch gilt als Fehler: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Lauf mit der alten Grenze läuft weiter")
	}
	w.mu.Lock()
	again := w.lastStart.IsZero()
	w.mu.Unlock()
	if !again {
		t.Fatal("Lauf mit der neuen Grenze ist nicht sofort fällig")
	}
	if st := w.snapshot(); st.Err != "" || st.Running {
		t.Fatalf("Stand nach dem Abbruch: %+v", st)
	}
}

// Im Labor gefunden (spiegel-uebernahme-im-abgleich): Übernimmt der Partner, endet sein
// Dateiempfang sofort, der letzte Abgleich scheitert also vor dem nächsten Takt. Damals
// galt der Tausch noch als laufend, die Abschottung wurde zurückgenommen, und zwei
// Datenbanken waren schreibbar. Die Rücknahme wartet jetzt den Herzschlag des Partners ab.
func TestSwapFinalSyncFailsBeforePartnerHeartbeat(t *testing.T) {
	m, a, svc, _, inFinal := proofRig(t)
	env := a.env
	a.env = func() pgEnv { e := env(); e.Tick = 500 * time.Millisecond; return e }
	release := make(chan struct{})
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error {
		inFinal <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("rsync: Verbindung vom Partner beendet")
	}
	m.rs.mu.Unlock()
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	select {
	case <-inFinal:
	case <-time.After(10 * time.Second):
		t.Fatal("letzter Abgleich nicht gestartet")
	}
	close(release)                     // der Empfang endet …
	time.Sleep(100 * time.Millisecond) // … und erst danach kommt der Herzschlag
	if !strings.Contains(confText(t, a), "default_transaction_read_only") || svc.active || tunOf(a).active {
		t.Fatal("Abschottung vor dem Herzschlag des Partners zurückgenommen")
	}
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 5*time.Second, "demote zu Ende", m.d.actionsIdle)
	m.tick()
	waitFor(t, 5*time.Second, "Takt zu Ende", m.d.actionsIdle)
	if !strings.Contains(confText(t, a), "default_transaction_read_only") || svc.active || tunOf(a).active {
		t.Errorf("zwei schreibbare Server: svc=%v tun=%v, Einstellungen %q", svc.active, tunOf(a).active, confText(t, a))
	}
	m.d.lock()
	marked, maint := m.d.undoRefused, m.d.p.Maint
	m.d.unlock()
	if maint {
		t.Error("Wartung nach der Übernahme des Partners")
	}
	if !marked && !hasEvent(m.d, "wieder angebunden") {
		t.Errorf("keine Wiederanbindung vorgemerkt: %v", eventTexts(m.d))
	}
}
