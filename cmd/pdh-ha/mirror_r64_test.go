package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R64: hängende Rücknahme ohne Wartung, Alarm einmal je Rücknahme, Skripte nur bei echtem Wechsel ---

// countHooks: Skripte zählen ohne sh (auch unter Windows): Statt des Ordners liegt dort eine Datei,
// jeder Lauf meldet das mit einem Alarm.
func countHooks(t *testing.T, d *daemon) func() int {
	t.Helper()
	d.lock()
	d.cl.Access.HooksOn = true
	d.hooks.dir = filepath.Join(t.TempDir(), "keinOrdner")
	os.WriteFile(d.hooks.dir, nil, 0o644)
	d.unlock()
	return func() int {
		t.Helper()
		waitIdle(t, d.hooks)
		return countSub(d, "Skripte beim Rollenwechsel")
	}
}

func maintOf(d *daemon) (bool, uint64) {
	d.lock()
	defer d.unlock()
	return d.p.Maint, d.p.MaintSeq
}

// U6, U7: ActUnfence scheitert dauerhaft. Keine Wartung, ein Alarm, eine dauerhafte Warnung, die
// Skripte laufen genau einmal (Rolle aktiv), nicht in jeder Wiederholung. Gelingt es danach, ist die
// Warnung weg, es gibt ein Ereignis, die Automatik ist an.
func TestUnfenceHangsWithoutMaint(t *testing.T) {
	m, a, svc, fail := unfenceFailRig(t)
	hooks := countHooks(t, m.d)
	_, seq0 := maintOf(m.d)
	fail.Store(true)
	for i := 0; i < 6; i++ {
		m.tick()
		waitFor(t, 10*time.Second, "unfence", m.d.actionsIdle)
	}
	if maint, seq := maintOf(m.d); maint || seq != seq0 {
		t.Fatalf("Wartung nach gescheiterter Rücknahme: an=%v Zähler %d→%d", maint, seq0, seq)
	}
	if role, pending := pendingOf(m); role != ha.Active || !pending {
		t.Fatalf("Rolle %s, Merker %v", role, pending)
	}
	if n := countSub(m.d, "Aktion unfence fehlgeschlagen"); n != 1 {
		t.Fatalf("Alarm %d-mal: %v", n, eventTexts(m.d))
	}
	if !hasWarning(m.d, "Rücknahme hängt: Datenbank ist nicht schreibbar") {
		t.Fatalf("keine dauerhafte Warnung: %q", m.d.statusNow().Warnings)
	}
	if n := hooks(); n != 1 {
		t.Fatalf("Skripte %d-mal gelaufen (erwartet einmal)", n)
	}
	fail.Store(false)
	m.tick()
	waitFor(t, 10*time.Second, "unfence gelingt", m.d.actionsIdle)
	if !svc.active || !tunOf(a).active {
		t.Fatal("nicht eingeschaltet")
	}
	if hasWarning(m.d, "Rücknahme hängt") {
		t.Fatalf("Warnung nach Erfolg: %q", m.d.statusNow().Warnings)
	}
	if n := countSub(m.d, "Rücknahme gelungen"); n != 1 {
		t.Fatalf("Ereignis zum Erfolg %d-mal: %v", n, eventTexts(m.d))
	}
	if maint, _ := maintOf(m.d); maint {
		t.Fatal("Automatik nach Erfolg aus")
	}
	m.tick()
	if n := hooks(); n != 1 {
		t.Fatalf("Skripte nach Erfolg erneut: %d", n)
	}
}

// U8: Der Grund wechselt (Absturz-Kreislauf): ein Alarm, keine Wartung, die Warnung nennt den
// letzten Grund. Nach der Ablösung durch den Partner ist die Warnung weg; eine spätere zweite
// Rücknahme meldet sich wieder mit einem Alarm (auch mit demselben Grund wie früher).
func TestUnfenceAlarmOncePerRetraction(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	m.tick()
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	_, seq0 := maintOf(m.d)
	a := ha.Action{Kind: ha.ActUnfence, Term: 1}
	for _, why := range []string{"stopped", "error", "stopped", "error"} {
		m.d.unfenceOutcome(a, fmt.Errorf("Datenbank ist nicht schreibbar (%s)", why))
	}
	if n := countSub(m.d, "Aktion unfence fehlgeschlagen"); n != 1 {
		t.Fatalf("Alarm %d-mal bei wechselndem Grund", n)
	}
	if maint, seq := maintOf(m.d); maint || seq != seq0 {
		t.Fatalf("Wartung: an=%v Zähler %d→%d", maint, seq0, seq)
	}
	if !hasWarning(m.d, "Rücknahme hängt: Datenbank ist nicht schreibbar (error)") {
		t.Fatalf("Warnung nennt den letzten Grund nicht: %q", m.d.statusNow().Warnings)
	}
	// Ablösung: der Partner ist aktiv in einer höheren Amtszeit
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	if role, _ := pendingOf(m); role == ha.Active {
		t.Fatal("nicht abgelöst")
	}
	if hasWarning(m.d, "Rücknahme hängt") {
		t.Fatalf("Warnung nach Ablösung: %q", m.d.statusNow().Warnings)
	}
	// zweite Rücknahme, später, in Amtszeit 3
	m.d.lock()
	m.d.p.Role, m.d.p.Term = ha.Active, 3
	m.d.unlock()
	m.d.unfenceOutcome(ha.Action{Kind: ha.ActUnfence, Term: 3}, errors.New("Datenbank ist nicht schreibbar (stopped)"))
	if n := countSub(m.d, "Aktion unfence fehlgeschlagen"); n != 2 {
		t.Fatalf("zweite Rücknahme ohne Alarm: %d", n)
	}
	if maint, _ := maintOf(m.d); maint {
		t.Fatal("Wartung bei der zweiten Rücknahme")
	}
}

// Nicht wiederholbar: Ein Fehlschlag an der Rolle oder an einem schon abgelösten Server sagt nicht
// „wird wiederholt“ und schaltet keine Wartung ein.
func TestUnfenceNotRepeatedSaysSo(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	m.tick()
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	m.d.unfenceOutcome(ha.Action{Kind: ha.ActUnfence, Term: 1}, unfenceRoleError{})
	m.d.unfenceOutcome(ha.Action{Kind: ha.ActUnfence, Term: 0}, errors.New("Datenbank ist nicht schreibbar (stopped)")) // andere Amtszeit
	for _, s := range eventTexts(m.d) {
		if strings.Contains(s, "unfence") && strings.Contains(s, "wiederholt") {
			t.Fatalf("nicht wiederholbarer Fall sagt „wird wiederholt“: %s", s)
		}
	}
	if n := countSub(m.d, "Aktion unfence fehlgeschlagen"); n != 2 {
		t.Fatalf("Alarme: %d %v", n, eventTexts(m.d))
	}
	if maint, _ := maintOf(m.d); maint {
		t.Fatal("Wartung am abgelösten Server")
	}
	if _, pending := pendingOf(m); pending {
		t.Fatal("Merker gesetzt, obwohl nicht wiederholt wird")
	}
}

// NEU-2, U7 Fall 2: Neustart von pdh-ha am aktiven Server mit sichtbarem Partner: kein Skriptlauf.
func TestHooksNotOnRestartActive(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	hooks := countHooks(t, m.d)
	for i := 0; i < 4; i++ {
		m.tick()
		waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	}
	if role, _ := pendingOf(m); role != ha.Active {
		t.Fatalf("Rolle %s", role)
	}
	if n := hooks(); n != 0 {
		t.Fatalf("Skripte beim Neustart ohne Rollenwechsel: %d", n)
	}
}

// Rücknahme aus abgeschottet: genau ein Lauf („aktiv“ in Takt N+1).
func TestHooksOnceFromFenced(t *testing.T) {
	m := newMDT(t, ha.Fenced, true, message{Role: "passiv"})
	hooks := countHooks(t, m.d)
	for i := 0; i < 4; i++ {
		m.tick()
		waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	}
	if role, _ := pendingOf(m); role != ha.Active {
		t.Fatalf("Rolle %s", role)
	}
	if n := hooks(); n != 1 {
		t.Fatalf("Skripte %d-mal (erwartet einmal)", n)
	}
}

// Die Adresse kommt erst nach gelungenem ActUnfence, auch nicht beim Wiederholen.
func TestVIPWaitsForUnfenceSuccess(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
	var failing atomic.Bool
	failing.Store(true)
	ra := &recActor{fail: func(a ha.Action) error {
		if a.Kind == ha.ActUnfence && failing.Load() {
			return errors.New("Datenbank ist nicht schreibbar (stopped)")
		}
		return nil
	}}
	d.lock()
	d.act = ra
	d.p.Role = ha.Fenced
	d.p.PartnerVersion = "test"
	d.unlock()
	ctx := stopAccess(t, d)
	for i := 0; i < 4; i++ { // Takt N, N+1 (scheitert), zwei Wiederholungen (scheitern)
		d.tick(ctx)
		waitFor(t, 5*time.Second, "idle", d.actionsIdle)
	}
	time.Sleep(300 * time.Millisecond)
	if n := f.count(vipAdd); n != 0 {
		t.Fatalf("Adresse vor gelungenem ActUnfence gesetzt (%d)", n)
	}
	failing.Store(false)
	d.tick(ctx)
	waitFor(t, 5*time.Second, "idle", d.actionsIdle)
	waitFor(t, 2*time.Second, "Adresse nach Erfolg", func() bool { return f.count(vipAdd) == 1 })
}

// countSub: Ereignisse, deren Text sub enthält.
func countSub(d *daemon, sub string) int {
	n := 0
	for _, s := range eventTexts(d) {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}
