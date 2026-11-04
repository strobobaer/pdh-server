package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Der Arbeiter selbst zählt einen Wechsel bei ausgeschalteter Spiegelung nicht; ob er als
// Wechsel gilt, entscheidet reconcile anhand der gemerkten Rolle (R44, Tests offRig unten).
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
	m.touchUpload() // der letzte Abgleich überträgt in jedem Fall etwas
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

// R42: Solange der Tausch als laufend gilt, nimmt die Abgabe nichts zurück.
func TestUndoFenceGivingLeavesFenced(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, svc := testActor(t, r)
	a.phase = func(uint64) swapPhase { return phaseGiving }
	a.mirrorFinal = func(context.Context) error { return errors.New("Frist") }
	if err := a.demote(context.Background(), 1); err == nil {
		t.Fatal("kein Fehler")
	}
	if svc.active || tunOf(a).active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("zurückgenommen: svc=%v tun=%v Einstellungen %q", svc.active, tunOf(a).active, confText(t, a))
	}
}

// roleSvc merkt die Rolle des Daemons beim Start des Dienstes.
type roleSvc struct {
	serviceCtl
	d    *daemon
	seen []ha.Role
}

func (s *roleSvc) Start(ctx context.Context) error {
	s.d.mu.Lock()
	s.seen = append(s.seen, s.d.p.Role)
	s.d.mu.Unlock()
	return s.serviceCtl.Start(ctx)
}

// finalFails: Rig, dessen letzter Abgleich sofort scheitert; der Tausch ist gestartet, die Abgabe zu Ende.
func finalFails(t *testing.T) (*mdt, *pgActor, *svcLog) {
	t.Helper()
	m, a, svc, _, _ := proofRig(t)
	m.rs.mu.Lock()
	m.rs.fn = func(context.Context, []string) error { return errors.New("rsync 23") }
	m.rs.mu.Unlock()
	m.touchUpload() // der letzte Abgleich überträgt in jedem Fall etwas
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	waitFor(t, 10*time.Second, "demote zu Ende", m.d.actionsIdle)
	if svc.active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("nach der gescheiterten Abgabe: svc=%v, Einstellungen %q", svc.active, confText(t, a))
	}
	return m, a, svc
}

// R42 (B1, B7): Dienste starten nur in der Rolle aktiv.
func TestSwapRollbackOnlyAsActive(t *testing.T) {
	m, a, svc := finalFails(t)
	rs := &roleSvc{serviceCtl: svc, d: m.d}
	a.svc = rs
	m.tick()
	m.tick() // R48: erst im zweiten Takt schreibbar
	waitFor(t, 5*time.Second, "Rücknahme", m.d.actionsIdle)
	m.d.mu.Lock()
	role := m.d.p.Role
	seen := append([]ha.Role(nil), rs.seen...)
	m.d.mu.Unlock()
	if role != ha.Active || !svc.active || strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("nicht zurückgenommen: Rolle %s svc=%v", role, svc.active)
	}
	for _, r := range seen {
		if r != ha.Active {
			t.Fatalf("pdh startet in der Rolle %s", r)
		}
	}
}

// R42: Die Abgabe ist gescheitert, der nächste Takt sieht eine höhere Amtszeit: keine
// Rücknahme, abgeschottet, Wiederanbindung vorgemerkt.
func TestSwapFailedThenPartnerTakesOver(t *testing.T) {
	m, a, svc := finalFails(t)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 5*time.Second, "Abschotten", m.d.actionsIdle)
	m.d.mu.Lock()
	role, marked := m.d.p.Role, m.d.undoRefused
	m.d.mu.Unlock()
	if role != ha.Passive || !marked {
		t.Errorf("Rolle %s, Marke %v", role, marked)
	}
	if svc.active || tunOf(a).active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Errorf("zwei schreibende Server: svc=%v tun=%v", svc.active, tunOf(a).active)
	}
}

// R43: Ohne frische gegenseitige Sicht ist der Stand des Tauschs ungewiss.
func TestSwapPhaseUnknownWithoutContact(t *testing.T) {
	m, _, _, _, inFinal := proofRig(t)
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error { inFinal <- struct{}{}; <-ctx.Done(); return ctx.Err() }
	m.rs.mu.Unlock()
	m.touchUpload() // der letzte Abgleich überträgt in jedem Fall etwas
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	<-inFinal
	m.d.mu.Lock()
	term := m.d.p.Term
	m.d.mu.Unlock()
	if p := m.d.swapPhase(term); p != phaseGiving {
		t.Fatalf("mit Sicht: %v", p)
	}
	m.d.mu.Lock()
	m.d.carry.LastMutual = m.d.mono() - m.d.settingsLocked().FenceAfter
	m.d.mu.Unlock()
	if p := m.d.swapPhase(term); p != phaseUnknown {
		t.Fatalf("ohne Sicht: %v", p)
	}
}

// lostSvc: Während unfence wechselt die Rolle (höhere Amtszeit gesehen).
type lostSvc struct {
	serviceCtl
	d *daemon
}

func (s *lostSvc) Start(ctx context.Context) error {
	s.d.mu.Lock()
	s.d.p.Role = ha.Passive
	s.d.mu.Unlock()
	return s.serviceCtl.Start(ctx)
}

// R48: Rolle wechselt während unfence: keine weiteren Dienste, nur lesend, keine Wartung.
func TestUnfenceRoleLostDuringStart(t *testing.T) {
	m, a, svc := finalFails(t)
	a.svc = &lostSvc{serviceCtl: svc, d: m.d}
	m.tick()
	m.tick()
	waitFor(t, 5*time.Second, "Rücknahme", m.d.actionsIdle)
	if svc.active || tunOf(a).active {
		t.Errorf("Dienste an: svc=%v tun=%v", svc.active, tunOf(a).active)
	}
	if !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Errorf("schreibbar: %q", confText(t, a))
	}
	m.d.mu.Lock()
	maint := m.d.p.Maint
	m.d.mu.Unlock()
	if maint {
		t.Error("Wartung nach dem Rollenwechsel in der Rücknahme")
	}
}

// R48: Rolle schon vor dem Reload weg: Die Einstellungen bleiben nur lesend.
func TestUnfenceRoleLostBeforeReload(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", "f|0/100|1|on|pdh_ha_b|", nil)
	a, svc := testActor(t, r)
	svc.active = false
	tunOf(a).active = false
	a.stillActive = func() bool { return false }
	err := a.unfence(context.Background())
	if err == nil {
		t.Fatal("kein Fehler")
	}
	if has(r.list(), "pg_ctlcluster 18 main reload") || svc.active || tunOf(a).active {
		t.Fatalf("Aufrufe: %v, svc=%v", r.list(), svc.active)
	}
}
