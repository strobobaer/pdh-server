package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

func proofRig(t *testing.T) (*mdt, *pgActor, *svcLog, *mirrorRT, chan struct{}) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	up := filepath.Join(m.dir, "repo", "uploads")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(up, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", stPrimary, nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/100|sync", nil)
	a, svc := testActor(t, r)
	m.d.wireActor(a)
	m.d.act = a
	m.tick()
	rt := m.ready()
	waitFor(t, 5*time.Second, "Erstabgleich fertig", func() bool { return rt.worker.Load().snapshot().FirstDone })
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	return m, a, svc, rt, make(chan struct{}, 32)
}

// A: Partner übernimmt, während demote im letzten Abgleich steht.
func TestSwapPartnerTakesOverDuringFinalSync(t *testing.T) {
	m, a, svc, _, inFinal := proofRig(t)
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error { inFinal <- struct{}{}; <-ctx.Done(); return ctx.Err() }
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
	t.Logf("im letzten Abgleich: svc=%v tun=%v", svc.active, tunOf(a).active)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 5*time.Second, "demote zu Ende", m.d.actionsIdle)
	m.d.lock()
	role, term, pend := m.d.p.Role, m.d.p.Term, m.d.carry.HandoverPending
	m.d.unlock()
	conf := confText(t, a)
	t.Logf("danach: Rolle=%s Amtszeit=%d offen=%d svc=%v tun=%v readonly=%v", role, term, pend, svc.active, tunOf(a).active,
		strings.Contains(conf, "default_transaction_read_only"))
	for _, s := range eventTexts(m.d) {
		t.Log(s)
	}
	if role != ha.Passive {
		t.Fatalf("Rolle %s statt passiv (Partner hat übernommen)", role)
	}
	if role == ha.Passive && (svc.active || tunOf(a).active || !strings.Contains(conf, "default_transaction_read_only")) {
		t.Errorf("SPLIT-BRAIN: Rolle %s (Partner aktiv Amtszeit 2), aber svc=%v tun=%v, Datenbank schreibbar=%v",
			role, svc.active, tunOf(a).active, !strings.Contains(conf, "default_transaction_read_only"))
	}
}

// B: Nach einem gescheiterten demote derselben Amtszeit bricht jeder neue Tausch sofort ab.
func TestSwapRetryAfterFailedDemote(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}, fn)
	n := 0
	d.act = &recActor{fail: func(a ha.Action) error {
		if a.Kind == ha.ActDemote {
			n++
			if n == 1 {
				return errors.New("Letzter Abgleich der Dateien nicht fertig: x")
			}
		}
		return nil
	}}
	ctx := context.Background()
	d.tick(ctx)
	if err := d.command("swap"); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	waitFor(t, 2*time.Second, "1. demote", d.actionsIdle)
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Rücknahme", d.actionsIdle)
	if s := d.statusNow(); s.Self.Role != ha.Active {
		t.Fatalf("nicht zurück: %+v", s.Self)
	}
	if err := d.command("swap"); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx) // Tausch 2 beginnt
	waitFor(t, 2*time.Second, "2. demote", d.actionsIdle)
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Folge", d.actionsIdle)
	ab := 0
	for _, s := range eventTexts(d) {
		if strings.Contains(s, "Rollentausch abgebrochen") {
			ab++
		}
	}
	h := len(fn.messages("handover"))
	t.Logf("demote-Läufe=%d abgebrochen=%d handover=%d Rolle=%s", n, ab, h, d.statusNow().Self.Role)
	if h == 0 {
		t.Errorf("2. Tausch trotz erfolgreichem demote abgebrochen (veraltetes demoteFailed): abgebrochen=%d", ab)
	}
}

// C: Nach gelungenem demote, aber gescheiterter Übernahme geht beim neuen Tausch
// die Übergabe raus, bevor das neue demote fertig ist.
type gateActor struct {
	n       int
	release chan struct{}
}

func (g *gateActor) Do(_ context.Context, a ha.Action) error {
	if a.Kind == ha.ActDemoteFreeze {
		g.n++
		if g.n >= 2 {
			<-g.release
		}
	}
	return nil
}

func TestSwapStaleDemoteDone(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}, fn)
	g := &gateActor{release: make(chan struct{})}
	d.act = g
	d.lock()
	s := d.settingsLocked()
	s.TakeoverDuo = 400 * time.Millisecond // nur über fixed, Validate lässt so wenig nicht zu
	d.fixed = &s
	d.unlock()
	defer close(g.release)
	ctx := context.Background()
	swap := func() {
		d.lock()
		d.cmd = ha.CmdSwapFreeze
		d.unlock()
		d.tick(ctx)
	}
	d.tick(ctx)
	swap()
	waitFor(t, 2*time.Second, "1. demote", d.actionsIdle)
	end := time.Now().Add(6 * time.Second)
	for time.Now().Before(end) {
		d.tick(ctx)
		time.Sleep(50 * time.Millisecond)
		if d.statusNow().Self.Role == ha.Active {
			break
		}
	}
	if s := d.statusNow(); s.Self.Role != ha.Active || s.Self.Term != 1 {
		t.Fatalf("nicht zurück nach aktiv: %s %v n=%d carry=%+v", s.Self.Role, eventTexts(d), g.n, d.carry)
	}
	before := len(fn.messages("handover"))
	swap()
	d.tick(ctx)
	time.Sleep(100 * time.Millisecond)
	after := len(fn.messages("handover"))
	t.Logf("demote-Aufrufe=%d, Übergaben vorher=%d nachher=%d, Rolle=%s", g.n, before, after, d.statusNow().Self.Role)
	if after > before {
		t.Errorf("Übergabe gesendet, während das neue demote noch blockiert")
	}
}

// E: gescheiterter letzter Abgleich: Rücknahme, Abgleich der Dienste in der Warteschlange, Endzustand, Arbeiter.
func TestSwapFailedFinalSyncRestores(t *testing.T) {
	m, a, svc, rt, inFinal := proofRig(t)
	release := make(chan struct{})
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error {
		inFinal <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("rsync 23")
	}
	m.rs.mu.Unlock()
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	<-inFinal
	m.d.lock()
	m.d.reconcileAt = m.d.mono() - 31*time.Second
	m.d.unlock()
	m.tick() // reiht den 30-s-Abgleich hinter demote ein
	time.Sleep(50 * time.Millisecond)
	close(release)
	waitFor(t, 5*time.Second, "demote und Abgleich zu Ende", m.d.actionsIdle)
	falseAlarm := false
	for _, e := range a.ev.since(0, 0) {
		t.Log("actor: ", e.Kind, " ", e.Text)
		falseAlarm = falseAlarm || strings.Contains(e.Text, "Nicht aktiver Server mit schreibbarer Datenbank")
	}
	t.Logf("vor dem nächsten Takt: svc=%v tun=%v readonly=%v falscherAlarm=%v", svc.active, tunOf(a).active,
		strings.Contains(confText(t, a), "default_transaction_read_only"), falseAlarm)
	m.tick()
	waitFor(t, 5*time.Second, "Rücknahme", m.d.actionsIdle)
	m.d.lock()
	role, pend, maint := m.d.p.Role, m.d.carry.HandoverPending, m.d.p.Maint
	m.d.unlock()
	wk := rt.worker.Load()
	wk.mu.Lock()
	active := wk.active()
	wk.mu.Unlock()
	t.Logf("Ende: Rolle=%s offen=%d Wartung=%v svc=%v tun=%v readonly=%v Arbeiter aktiv=%v Running=%v", role, pend, maint, svc.active, tunOf(a).active,
		strings.Contains(confText(t, a), "default_transaction_read_only"), active, wk.snapshot().Running)
	if maint {
		t.Error("ein gescheiterter letzter Abgleich darf die Automatik nicht in Wartung setzen (R29)")
	}
	if role != ha.Active || !svc.active || !tunOf(a).active || strings.Contains(confText(t, a), "default_transaction_read_only") || !active {
		t.Errorf("nicht wiederhergestellt")
	}
	// Arbeiter gleicht wieder ab
	m.rs.mu.Lock()
	m.rs.fn = nil
	m.rs.mu.Unlock()
	c0 := m.rs.count()
	m.clk.add(2 * time.Minute)
	m.tick()
	waitFor(t, 5*time.Second, "neuer Lauf", func() bool { return m.rs.count() > c0 })
}

// G: abandon: der Arbeiter bleibt aktiv und startet einen normalen löschenden Lauf.
func TestSwapAbandonStopsWorker(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	up := filepath.Join(m.dir, "repo", "uploads")
	os.MkdirAll(up, 0o755)
	os.WriteFile(filepath.Join(up, "x"), []byte("x"), 0o644)
	rel := make(chan struct{})
	defer close(rel)
	m.d.act = blockActor{release: rel}
	m.tick()
	rt := m.ready()
	waitFor(t, 5*time.Second, "Erstabgleich fertig", func() bool { return rt.worker.Load().snapshot().FirstDone })
	c0 := m.rs.count()
	m.d.lock()
	m.d.cmd = ha.CmdSwapAbandon
	m.d.unlock()
	m.tick()
	m.clk.add(2 * time.Minute)
	m.tick()
	wk := rt.worker.Load()
	wk.mu.Lock()
	active := wk.active()
	wk.mu.Unlock()
	time.Sleep(1500 * time.Millisecond)
	t.Logf("abandon läuft: Rolle=%s, Arbeiter aktiv=%v, rsync-Aufrufe %d -> %d", m.d.statusNow().Self.Role, active, c0, m.rs.count())
	if active && m.rs.count() > c0 {
		t.Errorf("rsync läuft während abandon weiter (Quelle wird aufgegeben)")
	}
}

// R28: Solange ein demote läuft oder wartet, beginnt kein neuer Tausch
// (auch nicht über die Wiederholung des Updates).
func TestSwapNotWhilePreviousDemotePending(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}, fn)
	rel := make(chan struct{})
	d.act = blockActor{release: rel}
	ctx := context.Background()
	d.tick(ctx)
	if err := d.command("swap"); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx) // Tausch 1 beginnt, demote hängt
	d.lock()
	seq1 := d.carry.SwapSeq
	d.cmd = ha.CmdSwapFreeze // Wiederholung des Updates
	d.unlock()
	d.tick(ctx)
	d.lock()
	seq2 := d.carry.SwapSeq
	d.unlock()
	if seq1 != 1 || seq2 != seq1 {
		t.Fatalf("zweiter Tausch während des laufenden demote begonnen: Versuch %d -> %d", seq1, seq2)
	}
	close(rel)
	waitFor(t, 2*time.Second, "demote fertig", d.actionsIdle)
}

// R29: Ein gescheiterter letzter Abgleich ist nur Alarm und Rücknahme, keine Wartung;
// jeder andere Fehler setzt sie wie bisher.
func TestActionFailedFinalSyncNoMaintenance(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	act := ha.Action{Kind: ha.ActDemote, Term: 1}
	d.actionFailed(act, &finalSyncError{err: errors.New("rsync 23")})
	d.lock()
	maint := d.p.Maint
	d.unlock()
	if maint {
		t.Fatal("Wartung nach gescheitertem letztem Abgleich")
	}
	if !hasEvent(d, "Letzter Abgleich der Dateien nicht fertig: rsync 23") {
		t.Fatalf("Alarm fehlt: %v", eventTexts(d))
	}
	d.actionFailed(act, errors.New("Partner hat nicht aufgeholt"))
	d.lock()
	maint = d.p.Maint
	d.unlock()
	if !maint {
		t.Fatal("andere Fehler müssen die Wartung wie bisher setzen")
	}
}

// R30: Während der eigenen Übergabe meldet der Herzschlag „Übergabe läuft“.
func TestHeartbeatGivingDuringHandover(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	m.tick()
	hb := func() message {
		m.d.lock()
		defer m.d.unlock()
		return m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	}
	if hb().Giving {
		t.Fatal("Übergabe gemeldet ohne Tausch")
	}
	m.d.lock()
	m.d.p.Role, m.d.carry.HandoverPending = ha.Passive, 2
	m.d.unlock()
	if !hb().Giving {
		t.Fatal("Übergabe läuft, aber nicht gemeldet")
	}
}

// R25/M2: Der 30-s-Abgleich fasst einen passiven Server mit offener Übergabe nicht an.
func TestReconcileSkipsOpenHandover(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	m.d.act = a
	m.tick()
	waitFor(t, 2*time.Second, "idle", m.d.actionsIdle)
	n0 := len(r.list())
	m.d.lock()
	m.d.p.Role, m.d.carry.HandoverPending = ha.Passive, 2
	m.d.reconcileAt = m.d.mono() - 31*time.Second
	m.d.maybeReconcileLocked(m.ctx, m.d.mono())
	m.d.unlock()
	time.Sleep(100 * time.Millisecond)
	waitFor(t, 2*time.Second, "idle", m.d.actionsIdle)
	if l := r.list(); len(l) != n0 {
		t.Fatalf("Abgleich fasst die Übergabe an: %v", l[n0:])
	}
}
