package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// R47: Der Empfänger hat keinen Beleg: Der Lauf löscht nichts, bis er ihn hat oder der
// aktive Server bestätigt.

// recvArgv: was rsync als Server-Aufruf sendet.
func recvArgv(del bool) []string {
	a := []string{"rsync", "--server", "-logDtpre.iLsfxCIvu", "--numeric-ids", "--backup", "--partial-dir", ".pdh-teil", "--delay-updates"}
	if del {
		a = append(a, "--delete")
	}
	return append(a, ".", "b:uploads")
}

func (f *inboxFix) acceptReq(req mirrorRequest) ([]string, func(error), string) {
	return f.in.Accept(context.Background(), "a", "node", req)
}

func uploadsDir(t *testing.T, f *inboxFix) string {
	t.Helper()
	p := f.root + "/repo/uploads"
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInboxEmptyTargetGetsProofWithoutDelete(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	_, done, r := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)})
	if r != "" {
		t.Fatal(r)
	}
	done(nil)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("leeres Ziel: kein Beleg nach dem Lauf")
	}
	if got := f.in.waits(); len(got) != 0 {
		t.Fatalf("wartet: %v", got)
	}
}

// Ein abgebrochener Erstabgleich füllt das Ziel; der nächste Lauf gilt weiter als Lauf auf ein leeres Ziel.
func TestInboxEmptyTargetSurvivesAbortedRun(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	_, done, r := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)})
	if r != "" {
		t.Fatal(r)
	}
	os.WriteFile(p+"/halb.bin", []byte("x"), 0o644)
	done(os.ErrClosed)
	f.rebuild() // auch über einen Neustart
	_, done, r = f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)})
	if r != "" {
		t.Fatal(r)
	}
	done(nil)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg nach dem zweiten Lauf")
	}
}

func TestInboxNonEmptyTargetWaits(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	os.WriteFile(p+"/eigen.txt", []byte("x"), 0o644)
	_, done, r := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)})
	if r != "" {
		t.Fatal(r)
	}
	done(nil)
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("nicht leeres Ziel bekommt einen Beleg")
	}
	if got := f.in.waits(); len(got) != 1 || got[0] != "uploads" {
		t.Fatalf("wartet: %v", got)
	}
	// Löschen ohne Bestätigung wird abgelehnt, die Datei bleibt
	if _, _, r := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(true)}); !strings.Contains(r, "Bestätigung") {
		t.Fatalf("Löschen ohne Beleg: %q", r)
	}
	// Mit Bestätigung: angenommen, danach Beleg, wartet nicht mehr
	_, done, r = f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(true), Confirmed: true})
	if r != "" {
		t.Fatal(r)
	}
	done(nil)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg nach der Bestätigung")
	}
	if got := f.in.waits(); len(got) != 0 {
		t.Fatalf("wartet noch: %v", got)
	}
}

// Pfad-Läufe (Sofort-Übertragung) belegen nichts.
func TestInboxPathRunGivesNoProof(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	argv := append(recvArgv(false)[:2:2], "-logDtpRe.iLsfxCIvu", "--files-from=-", "--from0", ".", "b:uploads")
	_, done, r := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: argv})
	if r != "" {
		t.Fatal(r)
	}
	done(nil)
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Pfad-Lauf gibt einen Beleg")
	}
}

// Ausgeschlossene Ordner (/etc/pdh/ha) und Teil-Ordner zählen nicht als Inhalt.
func TestMirrorTargetEmpty(t *testing.T) {
	d := t.TempDir()
	ex := []string{"--exclude=/ha/"}
	if !mirrorTargetEmpty(d, ex) {
		t.Fatal("leer")
	}
	os.MkdirAll(filepath.Join(d, "ha"), 0o700)
	os.WriteFile(filepath.Join(d, "ha", "key"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(d, ".pdh-teil"), 0o700)
	os.WriteFile(filepath.Join(d, ".pdh-teil", "t"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(d, "unter", "ordner"), 0o755)
	if !mirrorTargetEmpty(d, ex) {
		t.Fatal("nur Ausgeschlossenes und leere Ordner")
	}
	os.WriteFile(filepath.Join(d, "unter", "ordner", "f"), []byte("x"), 0o644)
	if mirrorTargetEmpty(d, ex) {
		t.Fatal("Datei übersehen")
	}
}

func TestInboxDropProofs(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	_, done, _ := f.acceptReq(mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)})
	done(nil)
	f.in.dropProofs()
	f.rebuild()
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Beleg übersteht das Ausschalten")
	}
}

// fixProof: Arbeiter mit Sicht auf den Beleg und die Datenkennung des Partners.
type fixProof struct {
	*fix2
	proven map[string]bool
	wait   map[string]bool
}

func newFixProof(t *testing.T) *fixProof {
	return &fixProof{fix2: newFix2(t), proven: map[string]bool{}, wait: map[string]bool{}}
}

func (f *fixProof) worker() *mirrorWorker {
	return newMirrorWorker(mirrorDeps{ev: f.ev, repo: f.repo, node: "b", rsync: f.rs, statePath: f.state, proof: f.proof.fn,
		partnerProof: func(l string) bool { return f.proven[l] },
		partnerWait:  func(l string) bool { return f.wait[l] },
		exists:       func(p string) bool { _, err := os.Stat(p); return err == nil && strings.Contains(p, "/uploads") }})
}

func lastUploads(rs *fakeRsync) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for i := len(rs.calls) - 1; i >= 0; i-- {
		if j := strings.Join(rs.calls[i], " "); strings.HasSuffix(j, ":uploads") {
			return j
		}
	}
	return ""
}

func TestMirrorNoDeleteWithoutPartnerProof(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("löscht ohne Beleg: %s", j)
	}
	f.proven["uploads"] = true
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
		t.Fatalf("löscht mit Beleg nicht: %s", j)
	}
}

// Einmaliger Alarm, auch über einen Neustart; Status zeigt die Quelle; die Bestätigung
// gilt für die Datenkennung und bleibt über einen Neustart.
func TestMirrorConfirmFlow(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("kennung-1")
	f.wait["uploads"] = true
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.noteWaiting()
	w.noteWaiting()
	if n := f.alarms("Quelle uploads: "); n != 1 {
		t.Fatalf("Alarme: %d %v", n, f.ev.readAll())
	}
	if got := w.waiting(); len(got) != 1 || got[0] != "uploads" {
		t.Fatalf("wartet: %v", got)
	}
	w2 := f.worker()
	w2.set(true, ha.Active, 1, fix2Settings)
	w2.noteWaiting()
	if n := f.alarms("Quelle uploads: "); n != 1 {
		t.Fatalf("Alarm nach Neustart wiederholt: %d", n)
	}
	if err := w2.confirm(); err != nil {
		t.Fatal(err)
	}
	if got := w2.waiting(); len(got) != 0 {
		t.Fatalf("wartet nach Bestätigung: %v", got)
	}
	w3 := f.worker()
	w3.set(true, ha.Active, 1, fix2Settings)
	if err := w3.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	j := lastUploads(f.rs)
	if !strings.Contains(j, "--delete") || !strings.Contains(j, "--bestaetigt") {
		t.Fatalf("nach der Bestätigung: %s", j)
	}
	// Neue Datenkennung des Empfängers: die Bestätigung gilt nicht mehr
	w3.observeDataID("kennung-2")
	if err := runAgain(w3); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("Bestätigung gilt für neue Kennung: %s", j)
	}
}

func TestMirrorConfirmNeedsDataID(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.confirm(); err == nil {
		t.Fatal("Bestätigung ohne Kennung des Empfängers angenommen")
	}
}

// R25 mündet in denselben Zustand: Die Bestätigung hebt „unbelegt“ auf, der nächste Lauf löscht.
func TestMirrorPromotedThenConfirmed(t *testing.T) {
	f := newFixProof(t)
	f.proven["uploads"] = true
	w := f.worker()
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings)
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("löscht unbelegt: %s", j)
	}
	if n := f.alarms("sudo pdh-ha mirror confirm"); n != 1 {
		t.Fatalf("Alarm nennt die Bestätigung nicht: %v", f.ev.readAll())
	}
	if f.alarms("Rollen tauschen") != 0 {
		t.Fatal("Alarm verlangt weiter einen Tausch")
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

// Am passiven Server abgelehnt, am aktiven angenommen (Daemon).
func TestDaemonMirrorConfirmOnlyActive(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv", MirrorDataID: "k"})
	m.tick()
	m.ready()
	if err := m.d.mirrorConfirm(); err == nil || !strings.Contains(err.Error(), "aktiven") {
		t.Fatalf("passiv: %v", err)
	}
	a := newMDT(t, ha.Active, true, message{Role: "passiv", MirrorDataID: "k"})
	a.tick()
	rt := a.ready()
	waitFor(t, 5*time.Second, "Datenkennung", func() bool { return rt.worker.Load().snapshot().DataID == "k" })
	if err := a.d.mirrorConfirm(); err != nil {
		t.Fatalf("aktiv: %v", err)
	}
}

// Der Sender gilt selbst als belegt, sobald der Partner den Beleg meldet: Sonst stünde nach
// dem ersten Tausch jede Quelle auf „wartet“.
func TestSenderGetsProofFromPartner(t *testing.T) {
	m, _, _, rt, _ := proofRig(t)
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}, MirrorDataID: "k", MirrorProven: []string{"uploads"}}))
	m.tick()
	waitFor(t, 5*time.Second, "eigener Beleg", func() bool { ok, _ := rt.inbox.Load().proofOf("uploads"); return ok })
	if got := rt.worker.Load().waiting(); len(got) != 0 {
		t.Fatalf("wartet: %v", got)
	}
}
