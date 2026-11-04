package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Prüfrunde 2: R25 (aktiv geworden ohne Beleg: nie --delete), SawFiles am Beleg,
// R26 (Platzprüfung nach Kennungswechsel nur Warnung).

func uploadsCalls(rs *fakeRsync) (del, nodel int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, c := range rs.calls {
		if !strings.HasSuffix(c[len(c)-1], ":uploads") {
			continue
		}
		if strings.Contains(strings.Join(c, " "), "--delete") {
			del++
		} else {
			nodel++
		}
	}
	return
}

type proofSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func (p *proofSet) fn(label string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[label], p.m[label]
}
func (p *proofSet) set(label string) {
	p.mu.Lock()
	p.m[label] = true
	p.mu.Unlock()
}

type fix2 struct {
	dir, repo, state string
	rs               *fakeRsync
	proof            *proofSet
	ev               *eventLog
}

func newFix2(t *testing.T) *fix2 {
	t.Helper()
	dir := t.TempDir()
	f := &fix2{dir: dir, repo: filepath.ToSlash(dir) + "/repo", state: filepath.Join(dir, "mirror.json"),
		rs: &fakeRsync{}, proof: &proofSet{m: map[string]bool{}}, ev: testEvents(t)}
	os.MkdirAll(f.repo+"/uploads", 0o755)
	os.WriteFile(f.repo+"/uploads/a.txt", []byte("x"), 0o644)
	return f
}

func (f *fix2) worker() *mirrorWorker {
	return newMirrorWorker(mirrorDeps{ev: f.ev, repo: f.repo, node: "b", rsync: f.rs, statePath: f.state, proof: f.proof.fn,
		exists: func(p string) bool { _, err := os.Stat(p); return err == nil && strings.Contains(p, "/uploads") }})
}

func (f *fix2) alarms(sub string) int {
	n := 0
	for _, e := range f.ev.readAll() {
		if e.Kind == "alarm" && strings.Contains(e.Text, sub) {
			n++
		}
	}
	return n
}

var fix2Settings = func() ha.Settings {
	s := ha.DefaultSettings()
	s.MirrorOn = true
	return s
}()

func runAgain(w *mirrorWorker) error {
	w.mu.Lock()
	w.lastStart = time.Time{}
	w.mu.Unlock()
	return w.runAll(context.Background())
}

// B1/R25: aktiv geworden, bevor der Erstabgleich fertig war (kein eigener Beleg):
// nie --delete, Alarm einmal je Quelle, Neustart behält es, eigener Beleg hebt es auf.
func TestMirrorPromotedWithoutProofNeverDeletes(t *testing.T) {
	f := newFix2(t)
	w := f.worker()
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings) // Wechsel passiv -> aktiv
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, nodel := uploadsCalls(f.rs); del != 0 || nodel != 1 {
		t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
	}
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if del, _ := uploadsCalls(f.rs); del != 0 {
		t.Fatal("zweiter Lauf löscht")
	}
	if n := f.alarms("Quelle uploads: Dieser Server wurde aktiv, bevor der Erstabgleich fertig war. Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm"); n != 1 {
		t.Fatalf("Alarme: %d (%v)", n, f.ev.readAll())
	}
	st := w.snapshot()
	if !st.PerLabel["uploads"].Unproven || st.PerLabel["uploads"].LastOK.IsZero() {
		t.Fatalf("Stand: %+v", st.PerLabel["uploads"])
	}
	// der Lauf ohne --delete ist durch: Erstabgleich gilt, ein Tausch ist wieder möglich
	if !st.FirstDone {
		t.Fatalf("FirstDone: %+v", st)
	}
	// Neustart in dieser Lage (ohne erneuten Wechsel): bleibt ohne --delete
	w2 := f.worker()
	w2.set(true, ha.Active, 3, fix2Settings)
	if err := w2.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, _ := uploadsCalls(f.rs); del != 0 {
		t.Fatal("nach Neustart löscht der Lauf")
	}
	// Rücktausch: der eigene Beleg hebt es auf
	w2.set(true, ha.Passive, 4, fix2Settings)
	f.proof.set("uploads")
	w2.liftUnproven(f.proof.fn)
	if w2.snapshot().PerLabel["uploads"].Unproven {
		t.Fatal("Beleg hebt nicht auf")
	}
	w3 := f.worker()
	w3.set(true, ha.Active, 5, fix2Settings)
	if err := w3.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, _ := uploadsCalls(f.rs); del != 1 {
		t.Fatalf("nach Aufhebung: mit --delete %d", del)
	}
}

// Wer von Anfang an aktiv ist, macht einen normalen Erstabgleich mit --delete; ebenso
// ein Server mit eigenem Beleg nach dem Wechsel.
func TestMirrorNormalFirstSyncKeepsDelete(t *testing.T) {
	f := newFix2(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, nodel := uploadsCalls(f.rs); del != 1 || nodel != 0 {
		t.Fatalf("Erstabgleich: mit --delete %d, ohne %d", del, nodel)
	}
	if w.snapshot().PerLabel["uploads"].Unproven || f.alarms("wurde aktiv") != 0 {
		t.Fatal("unbelegt ohne Wechsel")
	}
	// Wechsel mit Beleg: ebenfalls --delete
	g := newFix2(t)
	g.proof.set("uploads")
	w2 := g.worker()
	w2.set(true, ha.Passive, 1, fix2Settings)
	w2.set(true, ha.Active, 2, fix2Settings)
	if err := w2.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, _ := uploadsCalls(g.rs); del != 1 || g.alarms("wurde aktiv") != 0 {
		t.Fatalf("mit Beleg: mit --delete %d", del)
	}
}

// Absturz nach dem Wechsel, vor dem ersten Lauf: gemerkte Rolle passiv genügt.
func TestMirrorPromotedAcrossRestartViaPersistedRole(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	os.MkdirAll(filepath.Dir(m.w.statePath), 0o700)
	os.WriteFile(filepath.Join(filepath.Dir(m.w.statePath), mirrorInboxFile), []byte(`{"last_role":"passiv","data_id":"X"}`), 0o600)
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "erster Lauf", func() bool { return rt.worker.Load().snapshot().PerLabel["uploads"].Unproven })
	waitFor(t, 3*time.Second, "rsync für uploads", func() bool { del, nodel := uploadsCalls(m.rs); return del+nodel > 0 })
	if del, _ := uploadsCalls(m.rs); del != 0 {
		t.Fatal("--delete nach Wechsel ohne Beleg")
	}
	// im Status sichtbar
	st := m.d.statusNow().Mirror
	if len(st.Unproven) != 1 || st.Unproven[0] != "uploads" || !st.Sources[len(st.Sources)-1].Unproven {
		t.Fatalf("Status: %+v", st)
	}
	var b bytes.Buffer
	printMirrorStatus(&b, st)
	if !strings.Contains(b.String(), "uploads") || !strings.Contains(b.String(), "Wartet auf Bestätigung") {
		t.Errorf("Zeile fehlt: %q", b.String())
	}
}

// B2: SawFiles folgt dem eigenen Beleg – auch nach "nicht fertig" des Partners und bei eigenem LastOK.
func TestMirrorSawFilesFollowsOwnProof(t *testing.T) {
	f := newFix2(t)
	f.proof.set("uploads")
	w := f.worker()
	w.set(true, ha.Passive, 1, fix2Settings)
	w.adoptFirst(true, f.proof.fn)
	if ls := w.snapshot().PerLabel["uploads"]; !ls.Adopted || !ls.SawFiles {
		t.Fatalf("Übernahme: %+v", ls)
	}
	w.adoptFirst(false, f.proof.fn) // Partner meldet kurz "nicht fertig"
	if ls := w.snapshot().PerLabel["uploads"]; ls.Adopted || !ls.SawFiles {
		t.Fatalf("Rücknahme verliert SawFiles: %+v", ls)
	}
	// eigenes LastOK: der Beleg setzt SawFiles trotzdem
	g := newFix2(t)
	g.proof.set("uploads")
	w2 := g.worker()
	w2.mu.Lock()
	w2.st.PerLabel["uploads"] = mirrorLabelState{LastOK: time.Now()}
	w2.mu.Unlock()
	w2.set(true, ha.Passive, 1, fix2Settings)
	w2.adoptFirst(true, g.proof.fn)
	if ls := w2.snapshot().PerLabel["uploads"]; !ls.SawFiles || ls.Adopted {
		t.Fatalf("eigenes LastOK: %+v", ls)
	}
	// neue Datenkennung: SawFiles gehört der eigenen Quelle und bleibt
	w2.observeDataID("A")
	w2.observeDataID("B")
	if ls := w2.snapshot().PerLabel["uploads"]; !ls.SawFiles || !ls.LastOK.IsZero() {
		t.Fatalf("nach Zurücksetzen: %+v", ls)
	}
	// und eine jetzt leere Quelle geht nicht mit --delete hinaus
	os.Remove(g.repo + "/uploads/a.txt")
	w2.set(true, ha.Active, 2, fix2Settings)
	w2.runAll(context.Background())
	if del, nodel := uploadsCalls(g.rs); del+nodel != 0 {
		t.Fatalf("leere Quelle gesendet: %d %d", del, nodel)
	}
}

// B3/R26: nach einem Kennungswechsel (Ablage verloren, Daten beim Partner noch da) sperrt
// die Platzprüfung nicht, sie warnt nur; ohne Kennungswechsel bleibt sie eine Sperre.
func TestMirrorDataIDResetOnlyWarnsAboutSpace(t *testing.T) {
	f := newFix2(t)
	free := int64(500)
	mk := func() *mirrorWorker {
		return newMirrorWorker(mirrorDeps{ev: f.ev, repo: f.repo, node: "b", rsync: f.rs, statePath: f.state, proof: f.proof.fn,
			size: func(string) int64 { return 1000 }, free: func() (int64, bool) { return free, true },
			exists: func(p string) bool { _, err := os.Stat(p); return err == nil && strings.Contains(p, "/uploads") }})
	}
	// ohne Kennungswechsel: gesperrt
	w := mk()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err == nil || !strings.Contains(err.Error(), "Zu wenig Platz") {
		t.Fatalf("ohne Wechsel: %v", err)
	}
	// Kennungswechsel: nur Warnung, der Lauf geht durch, danach gilt der Erstabgleich
	w.observeDataID("A")
	w.observeDataID("B")
	if err := runAgain(w); err != nil {
		t.Fatalf("nach Kennungswechsel: %v", err)
	}
	if del, _ := uploadsCalls(f.rs); del != 1 {
		t.Fatalf("kein Lauf: %d", del)
	}
	if f.alarms("Zu wenig Platz") < 1 {
		t.Error("keine Warnung")
	}
	if st := w.snapshot(); !st.FirstDone || st.DataReset {
		t.Fatalf("Stand: %+v", st)
	}
	// der Zustand überlebt einen Neustart, solange der Erstabgleich nicht fertig ist
	g := newFix2(t)
	w2 := newMirrorWorker(mirrorDeps{ev: g.ev, repo: g.repo, node: "b", rsync: g.rs, statePath: g.state,
		size: func(string) int64 { return 1000 }, free: func() (int64, bool) { return 500, true }})
	w2.set(true, ha.Active, 1, fix2Settings)
	w2.observeDataID("A")
	w2.observeDataID("B")
	w3 := newMirrorWorker(mirrorDeps{ev: g.ev, repo: g.repo, node: "b", statePath: g.state})
	if !w3.snapshot().DataReset {
		t.Fatal("DataReset geht beim Neustart verloren")
	}
}

// Runde 3: der Wechsel muss auf der Platte stehen, bevor "aktiv" gemerkt wird – auch wenn
// `set` das Flag im Speicher schon gesetzt hat. Neustart direkt danach, ohne Lauf: kein --delete.
func TestMirrorPromotedSavedBeforeRunSurvivesRestart(t *testing.T) {
	f := newFix2(t)
	w := f.worker()
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings) // Promoted nur im Speicher
	w.markPromoted()
	b, _ := os.ReadFile(f.state)
	if !strings.Contains(string(b), `"promoted":true`) {
		t.Fatalf("mirror.json ohne promoted: %s", b)
	}
	// Absturz vor dem ersten Lauf, Neustart als aktiver Server
	w2 := f.worker()
	w2.set(true, ha.Active, 3, fix2Settings)
	if err := w2.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if del, nodel := uploadsCalls(f.rs); del != 0 || nodel != 1 {
		t.Fatalf("nach Neustart: mit --delete %d, ohne %d", del, nodel)
	}
}

// Scheitert das Speichern, meldet markPromoted den Fehler (reconcile merkt dann "aktiv" nicht).
func TestMirrorMarkPromotedReportsSaveError(t *testing.T) {
	f := newFix2(t)
	blocker := filepath.Join(f.dir, "datei")
	os.WriteFile(blocker, []byte("x"), 0o644)
	f.state = filepath.Join(blocker, "unterordner", "mirror.json") // Elternordner nicht anlegbar
	w := f.worker()
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings)
	if err := w.markPromoted(); err == nil {
		t.Fatal("Speicherfehler nicht gemeldet")
	}
}

// Reihenfolge in reconcile: steht last_role=aktiv in der Inbox, steht der Wechsel schon in mirror.json.
func TestMirrorReconcileSavesPromotionBeforeLastRole(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	os.MkdirAll(filepath.Dir(m.w.statePath), 0o700)
	inbox := filepath.Join(filepath.Dir(m.w.statePath), mirrorInboxFile)
	os.WriteFile(inbox, []byte(`{"last_role":"passiv","data_id":"X"}`), 0o600)
	m.tick()
	m.ready()
	waitFor(t, 3*time.Second, "last_role aktiv", func() bool {
		b, _ := os.ReadFile(inbox)
		return strings.Contains(string(b), `"last_role":"aktiv"`)
	})
	b, _ := os.ReadFile(m.w.statePath)
	if !strings.Contains(string(b), `"promoted":true`) && !strings.Contains(string(b), `"unproven":true`) {
		t.Fatalf("Wechsel nicht gespeichert, obwohl aktiv gemerkt: %s", b)
	}
}
