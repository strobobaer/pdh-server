package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R51: Der Aktor hebt nie selbst auf ---

// Die Rolle wird aktiv (Frist abgelaufen), während demote noch läuft. Bis Takt N+1 bleibt die
// Datenbank nur lesend und ohne Dienste; eine höhere Amtszeit in N+1 verhindert das Einschalten.
func withdrawnRig(t *testing.T) (*mdt, *pgActor, *svcLog) {
	t.Helper()
	m, a, svc, _, inFinal := proofRig(t)
	release := make(chan struct{})
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error {
		inFinal <- struct{}{}
		<-release
		return errors.New("rsync 23")
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
	m.d.lock()
	m.d.carry.HandoverAt -= time.Hour // Frist der Abgabe abgelaufen
	m.d.unlock()
	m.tick() // Takt N
	m.d.lock()
	role, pending := m.d.p.Role, m.d.p.UnfencePending
	m.d.unlock()
	if role != ha.Active || !pending {
		t.Fatalf("Takt N: Rolle %s, Merker %v", role, pending)
	}
	close(release)
	waitFor(t, 10*time.Second, "demote zu Ende", m.d.actionsIdle)
	if svc.active || tunOf(a).active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("vor Takt N+1: svc=%v tun=%v schreibbar=%v", svc.active, tunOf(a).active, !strings.Contains(confText(t, a), "default_transaction_read_only"))
	}
	return m, a, svc
}

func TestWithdrawnWaitsForSecondTick(t *testing.T) {
	m, a, svc := withdrawnRig(t)
	m.tick() // Takt N+1
	waitFor(t, 5*time.Second, "Rücknahme", m.d.actionsIdle)
	if !svc.active || !tunOf(a).active || strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("nach Takt N+1: svc=%v tun=%v", svc.active, tunOf(a).active)
	}
}

func TestWithdrawnHigherTermPreventsUnfence(t *testing.T) {
	m, a, svc := withdrawnRig(t)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 5*time.Second, "Takt", m.d.actionsIdle)
	if svc.active || tunOf(a).active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("eingeschaltet trotz höherer Amtszeit: svc=%v tun=%v", svc.active, tunOf(a).active)
	}
}

// --- R56: Übersprungenes ActRejoin schottet ab und setzt die Marke ---

func TestSkippedRejoinFencesAndMarks(t *testing.T) {
	m, a, svc, _, _ := proofRig(t)
	m.d.lock()
	m.d.up.job = ha.UpdateJob{ID: "auftrag", Phase: ha.UpPrepare}
	m.d.unlock()
	m.d.enqueueActions(m.ctx, []ha.Action{{Kind: ha.ActRejoin, Term: 2}})
	waitFor(t, 5*time.Second, "Aktion", m.d.actionsIdle)
	if svc.active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Errorf("nicht abgeschottet: svc=%v", svc.active)
	}
	m.d.lock()
	marked := m.d.undoRefused
	m.d.unlock()
	if !marked {
		t.Error("keine Marke")
	}
}

// --- R53: Auftrag, den der aktive Partner nicht führt ---

func foreignRig(t *testing.T, partner message) *mdt {
	t.Helper()
	old := upForeignHold
	upForeignHold = 0
	t.Cleanup(func() { upForeignHold = old })
	m := newMDT(t, ha.Passive, true, partner)
	p := partner
	p.Term, p.Ver, p.Sees = 2, "test", []string{"a"}
	m.fn.setView("b", seenView(p))
	m.d.lock()
	m.d.up.job = ha.UpdateJob{ID: "alt", Phase: ha.UpPrepare}
	m.d.p.Term = 2
	m.d.unlock()
	return m
}

func TestForeignJobAlarmAndHandAbort(t *testing.T) {
	m := foreignRig(t, message{Role: "aktiv", UpLead: "-"})
	m.tick()
	m.tick()
	if !hasEvent(m.d, "sudo pdh-ha update abort") {
		t.Fatalf("kein Alarm mit Befehl: %v", eventTexts(m.d))
	}
	n := 0
	for _, s := range eventTexts(m.d) {
		if strings.Contains(s, "sudo pdh-ha update abort") {
			n++
		}
	}
	m.tick()
	if n != 1 {
		t.Fatalf("Alarm %d-mal", n)
	}
	if err := m.d.abortUpdate("Befehlszeile"); err != nil {
		t.Fatalf("Handweg abgelehnt: %v", err)
	}
	m.d.lock()
	ended := upEnded(m.d.up.job)
	m.d.unlock()
	if !ended {
		t.Fatal("Auftrag bleibt offen")
	}
}

func TestForeignJobPartnerLeads(t *testing.T) {
	m := foreignRig(t, message{Role: "aktiv", UpLead: "alt", UpJob: true})
	m.tick()
	m.tick()
	if hasEvent(m.d, "sudo pdh-ha update abort") {
		t.Fatalf("Alarm, obwohl der Partner führt: %v", eventTexts(m.d))
	}
	if err := m.d.abortUpdate("x"); err == nil {
		t.Fatal("Handweg trotz führendem Partner")
	}
}

// Gemischte Versionen: Das Feld fehlt: Alarm ja, Handweg nein.
func TestForeignJobMixedVersions(t *testing.T) {
	m := foreignRig(t, message{Role: "aktiv"})
	m.d.lock()
	m.d.undoRefused = true
	m.d.unlock()
	m.tick()
	m.tick()
	if !hasEvent(m.d, "Update-Auftrag") {
		t.Fatalf("kein Alarm: %v", eventTexts(m.d))
	}
	if err := m.d.abortUpdate("x"); err == nil {
		t.Fatal("Handweg ohne Feld des Partners")
	}
}

// Auftragskennung im Herzschlag, omitempty für alte Versionen.
func TestHeartbeatUpLeadOptional(t *testing.T) {
	b, _ := json.Marshal(message{T: "hb"})
	if strings.Contains(string(b), "up_lead") {
		t.Fatalf("nicht optional: %s", b)
	}
}

// --- R54: Beleg und Quelle am Datenträger ---

// devFix: Inbox mit austauschbarer Gerätenummer.
func devInbox(t *testing.T) (*inboxFix, *uint64) {
	t.Helper()
	f := newInboxFix(t)
	dev := new(uint64)
	*dev = 7
	old := mirrorDev
	mirrorDev = func(string) (uint64, bool) { return *dev, true }
	t.Cleanup(func() { mirrorDev = old })
	return f, dev
}

func argvDel(del bool) mirrorRequest {
	return mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(del)}
}

func run(t *testing.T, f *inboxFix, r mirrorRequest) {
	t.Helper()
	_, d, ref := f.acceptReq(r)
	if ref != "" {
		t.Fatalf("abgelehnt: %s", ref)
	}
	d(nil)
}

// S1: warten, Ziel leer (Einhängepunkt fehlt), voll: --delete bleibt abgelehnt.
func TestInboxWaitIsBinding(t *testing.T) {
	f, _ := devInbox(t)
	p := uploadsDir(t, f)
	os.WriteFile(p+"/eigen.txt", []byte("x"), 0o644)
	run(t, f, argvDel(false))
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet: %v", got)
	}
	os.Remove(p + "/eigen.txt") // Datenträger nicht eingehängt
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("leeres Ziel hebt das Warten auf")
	}
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet nicht mehr: %v", got)
	}
	os.WriteFile(p+"/eigen.txt", []byte("x"), 0o644)
	if _, _, r := f.acceptReq(argvDel(true)); !strings.Contains(r, "Bestätigung") {
		t.Fatalf("--delete: %q", r)
	}
}

func TestInboxDevChangeDropsProof(t *testing.T) {
	f, dev := devInbox(t)
	p := uploadsDir(t, f)
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg")
	}
	run(t, f, argvDel(true))
	*dev = 8 // anderer Datenträger, leer
	os.RemoveAll(p)
	os.MkdirAll(p, 0o755)
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Beleg gilt auf anderem Datenträger")
	}
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet nicht: %v", got)
	}
	if _, _, r := f.acceptReq(argvDel(true)); !strings.Contains(r, "Bestätigung") {
		t.Fatalf("--delete: %q", r)
	}
	// Bestätigt: neuer Datenträger gilt, Beleg neu
	r := argvDel(true)
	r.Confirmed = true
	run(t, f, r)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg nach der Bestätigung")
	}
	run(t, f, argvDel(true))
}

// Seeded gilt nur auf demselben Datenträger.
func TestInboxSeededBoundToDev(t *testing.T) {
	f, dev := devInbox(t)
	p := uploadsDir(t, f)
	_, d, r := f.acceptReq(argvDel(false))
	if r != "" {
		t.Fatal(r)
	}
	os.WriteFile(p+"/halb.bin", []byte("x"), 0o644)
	d(os.ErrClosed)
	*dev = 8
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Seeded gilt auf anderem Datenträger")
	}
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet nicht: %v", got)
	}
}

// Alte Zustandsdatei ohne Gerätenummer: Beim ersten Lauf nachtragen, kein Fehlalarm.
func TestInboxOldStateGetsDev(t *testing.T) {
	f, dev := devInbox(t)
	uploadsDir(t, f)
	os.MkdirAll(filepath.Dir(f.state), 0o755)
	os.WriteFile(f.state, []byte(`{"proof":{"uploads":{"at":"2026-11-01T00:00:00Z","files":true}},"data_id":"k"}`), 0o600)
	f.rebuild()
	run(t, f, argvDel(true))
	if got := f.in.waits(); len(got) != 0 {
		t.Fatalf("Fehlalarm: %v", got)
	}
	*dev = 9
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("nachgetragene Nummer gilt nicht")
	}
}

// Ein Lesefehler im Ziel zählt als nicht leer.
func TestMirrorTargetReadErrorNotEmpty(t *testing.T) {
	old := mirrorWalk
	mirrorWalk = func(root string, fn fs.WalkDirFunc) error { return fn(root, nil, errors.New("EIO")) }
	t.Cleanup(func() { mirrorWalk = old })
	if mirrorTargetEmpty(t.TempDir(), nil) {
		t.Fatal("Lesefehler gilt als leer")
	}
}

// Absender: Anderer Datenträger der Quelle: kein --delete, ein Alarm, Bestätigung nötig.
func TestMirrorSourceDevChangeNoDelete(t *testing.T) {
	f := newFixProof(t)
	f.proven["uploads"] = true
	dev := uint64(7)
	old := mirrorDev
	mirrorDev = func(string) (uint64, bool) { return dev, true }
	t.Cleanup(func() { mirrorDev = old })
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
		t.Fatalf("erster Lauf: %s", j)
	}
	dev = 8
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("löscht nach Datenträgertausch: %s", j)
	}
	if n := f.alarms("Quelle uploads: Der Datenträger"); n != 1 {
		t.Fatalf("Alarme: %d %v", n, f.ev.readAll())
	}
	if got := w.waiting(); len(got) != 1 {
		t.Fatalf("wartet: %v", got)
	}
	if err := w.confirm(); err != nil {
		t.Fatal(err)
	}
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
		t.Fatalf("nach der Bestätigung: %s", j)
	}
}

// Alte mirror.json ohne Nummer: nachtragen, kein Fehlalarm.
func TestMirrorOldStateGetsSourceDev(t *testing.T) {
	f := newFixProof(t)
	f.proven["uploads"] = true
	os.WriteFile(f.state, []byte(`{"first_done":true,"per_label":{"uploads":{"last_ok":"2026-11-01T00:00:00Z","saw_files":true}}}`), 0o600)
	old := mirrorDev
	mirrorDev = func(string) (uint64, bool) { return 7, true }
	t.Cleanup(func() { mirrorDev = old })
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") || f.alarms("Datenträger") != 0 {
		t.Fatalf("Fehlalarm: %s %v", j, f.ev.readAll())
	}
}

// S2: lost+found zählt nicht als Inhalt der Quelle.
func TestMirrorLostFoundNotFiles(t *testing.T) {
	f := newFixProof(t)
	f.proven["uploads"] = true
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.Remove(f.repo + "/uploads/a.txt")
	os.MkdirAll(f.repo+"/uploads/lost+found", 0o700)
	c0 := f.rs.count()
	if err := runAgain(w); err == nil || !strings.Contains(err.Error(), "leer") {
		t.Fatalf("Fehler: %v", err)
	}
	if j := strings.Join(f.rs.calls[c0-1], " "); c0 < f.rs.count() && strings.Contains(j, ":uploads") {
		t.Fatalf("Lauf trotz leerer Quelle: %s", j)
	}
}

// --- R55 ---

func TestMirrorConfirmOnlyWaiting(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	if err := w.confirm(); err == nil {
		t.Fatal("Bestätigung ohne wartende Quelle angenommen")
	}
	f.wait["uploads"] = true
	if err := w.confirm(); err != nil {
		t.Fatal(err)
	}
	st := w.snapshot()
	if st.PerLabel["uploads"].ConfirmedFor != "k" {
		t.Fatalf("uploads: %+v", st.PerLabel["uploads"])
	}
	if st.PerLabel["chat_files"].ConfirmedFor != "" || st.PerLabel["etc-pdh"].ConfirmedFor != "" {
		t.Fatalf("nicht wartende Quellen freigegeben: %+v", st.PerLabel)
	}
	// Wird später eine andere Quelle wartend, meldet sie sich einzeln
	f.wait["chat_files"] = true
	w.noteWaiting()
	if n := f.alarms("Quelle chat_files: "); n != 1 {
		t.Fatalf("Alarme: %d", n)
	}
}

// Die Bestätigung ist nach dem Beleg verbraucht: Geht der Beleg verloren, gilt sie nicht weiter.
func TestMirrorConfirmConsumedByProof(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	f.wait["uploads"] = true
	if err := w.confirm(); err != nil {
		t.Fatal(err)
	}
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait["uploads"], f.proven["uploads"] = false, true
	w.noteOwnProof([]string{"uploads"}, func(string, bool) {})
	f.proven["uploads"] = false
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("alte Bestätigung gilt weiter: %s", j)
	}
}

// S3: leere Quelle, Hin- und Rücktausch, kein Fehler: Der Absender-Beleg nimmt den echten Stand.
func TestMirrorEmptySourceRoundTrip(t *testing.T) {
	f := newFixProof(t)
	os.Remove(f.repo + "/uploads/a.txt")
	f.proven["uploads"] = true
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	w.noteOwnProof([]string{"uploads"}, func(l string, files bool) { got[l] = files })
	if v, ok := got["uploads"]; !ok || v {
		t.Fatalf("Beleg: %v", got)
	}
	f.proof.set("uploads")
	w.set(true, ha.Passive, 2, fix2Settings)
	w.adoptFirst(true, func(string) (bool, bool) { return true, got["uploads"] })
	w.set(true, ha.Active, 3, fix2Settings)
	if err := runAgain(w); err != nil {
		t.Fatalf("leere Quelle nach dem Rücktausch: %v", err)
	}
}

// R55: passiv mit Spiegelung, aus, Tausch, ein: kein Übernahmefall; der Beleg des Empfängers zählt.
func TestMirrorStaleRoleDoesNotDecide(t *testing.T) {
	m := offRig(t, ha.Passive)
	m.setOn(true)
	m.tick()
	rt := m.ready()
	waitFor(t, 5*time.Second, "Rolle gemerkt", func() bool { return rt.inbox.Load().lastRole() == ha.Passive })
	m.setOn(false)
	m.tick()
	waitFor(t, 5*time.Second, "Rolle vergessen", func() bool { return rt.inbox.Load().lastRole() == "" })
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}, MirrorDataID: "k", MirrorProven: []string{"uploads"}}))
	m.d.lock()
	m.d.p.Role = ha.Active
	m.d.unlock()
	m.tick()
	if del, nodel := m.mirrorAfterOn(); del != 1 || nodel != 0 {
		t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
	}
	time.Sleep(100 * time.Millisecond)
	if hasEvent(m.d, "wurde aktiv") {
		t.Fatalf("falscher Alarm: %s", fmt.Sprint(eventTexts(m.d)))
	}
}

// touchUpload: eine neue Datei vor dem Tausch. Sonst ruft der letzte Abgleich rsync je nach
// Zeitablauf gar nicht auf: Deckt der Watch die Quelle ab und ist nichts vorgemerkt, gibt es nichts
// zu tun (unter Linux der Regelfall).
func (m *mdt) touchUpload() {
	_ = os.WriteFile(filepath.Join(m.dir, "repo", "uploads", "vor-dem-tausch"), []byte("x"), 0o644)
}
