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

// Prüfrunde 1: „Erstabgleich fertig“ braucht einen Beleg, Herzschlag nur mit echtem
// Stand, Papierkorb-Auftrag nur vom aktiven Partner, mirror run meldet nur echte Läufe.

// setProof legt den Beleg des Empfängers für Quellen fest (wie nach einem löschenden Lauf).
func setProof(rt *mirrorRT, files bool, labels ...string) {
	ib := rt.inbox.Load()
	ib.mu.Lock()
	for _, l := range labels {
		ib.st.Proof[l] = mirrorProof{At: time.Now(), Files: files}
	}
	ib.mu.Unlock()
}

func adoptedFile(m *mdt) string {
	b, _ := os.ReadFile(m.w.statePath)
	return string(b)
}

// R22: der Empfänger übernimmt „fertig“ des Partners nur mit eigenem Beleg; ein später
// aktiver Server verlangt dann keinen neuen Erstabgleich, schützt sich aber vor leerer Quelle.
func TestMirrorFirstCarriedOver(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv", MirrorFirst: true, MirrorDataID: "A"})
	m.tick()
	rt := m.ready()
	time.Sleep(80 * time.Millisecond) // einige Durchgänge: ohne Beleg wird nichts übernommen
	if rt.worker.Load().snapshot().FirstDone {
		t.Fatal("ohne Beleg übernommen")
	}
	setProof(rt, true, "uploads", "chat_files", "etc-pdh")
	waitFor(t, 3*time.Second, "übernommen", func() bool { return rt.worker.Load().snapshot().FirstDone })
	waitFor(t, 3*time.Second, "in mirror.json festgehalten", func() bool {
		return strings.Contains(adoptedFile(m), `"adopted":true`)
	})
	if got := adoptedFile(m); !strings.Contains(got, `"saw_files":true`) {
		t.Fatalf("SawFiles fehlt: %s", got)
	}
	// dieser Server wird später aktiv (neuer Prozess): kein neuer Erstabgleich ...
	os.MkdirAll(m.w.repo+"/uploads", 0o755) // ... aber eine leere Quelle geht nie mit --delete hinaus
	rs2 := &fakeRsync{}
	w2 := newMirrorWorker(mirrorDeps{ev: m.d.ev, repo: m.w.repo, node: "b", rsync: rs2, statePath: m.w.statePath})
	w2.set(true, ha.Active, 2, m.d.settingsLocked())
	if st := w2.snapshot(); !st.FirstDone || st.FirstPercent != 100 || !st.PerLabel["uploads"].SawFiles {
		t.Fatalf("neuer aktiver Server: %+v", st)
	}
	w2.runAll(context.Background())
	rs2.mu.Lock()
	for _, c := range rs2.calls {
		if strings.HasSuffix(c[len(c)-1], ":uploads") {
			t.Errorf("leere Quelle gesendet: %v", c)
		}
	}
	rs2.mu.Unlock()
	if !strings.Contains(w2.snapshot().PerLabel["uploads"].Err, "leer") {
		t.Errorf("keine Meldung: %+v", w2.snapshot().PerLabel["uploads"])
	}
	// meldet der Partner später „nicht fertig“ (neue Quelle), gilt es nicht mehr
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"a"}, MirrorFirst: false, MirrorDataID: "A"}))
	m.tick()
	waitFor(t, 3*time.Second, "zurückgenommen", func() bool { return !rt.worker.Load().snapshot().FirstDone })
}

// Ein leerer passiver Server (nichts empfangen) übernimmt „fertig“ nie.
func TestMirrorEmptyPassiveDoesNotAdopt(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv", MirrorFirst: true, MirrorDataID: "A"})
	m.tick()
	rt := m.ready()
	for range 5 {
		m.tick()
		time.Sleep(30 * time.Millisecond)
	}
	if st := rt.worker.Load().snapshot(); st.FirstDone || st.FirstPercent == 100 {
		t.Fatalf("leerer Server übernimmt: %+v", st)
	}
	if strings.Contains(adoptedFile(m), "adopted") {
		t.Fatalf("mirror.json: %s", adoptedFile(m))
	}
	// auch ein Beleg nur für einen Teil der Quellen reicht nicht
	setProof(rt, true, "uploads")
	time.Sleep(80 * time.Millisecond)
	if rt.worker.Load().snapshot().FirstDone {
		t.Fatal("Teilbeleg genügt")
	}
}

// Ersatzserver: neue Datenkennung beim Partner erzwingt einen neuen Erstabgleich; ohne
// Kennung (alte Gegenstelle) wird nie zurückgesetzt.
func TestMirrorReplacementServerForcesFirstSync(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv", MirrorDataID: "A"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Erstabgleich fertig", func() bool {
		wk := rt.worker.Load()
		return wk != nil && wk.snapshot().FirstDone && !wk.snapshot().LastOK.IsZero()
	})
	wk := rt.worker.Load()
	if id := wk.snapshot().DataID; id != "A" {
		t.Fatalf("Kennung nicht gemerkt: %q", id)
	}
	// alte Gegenstelle ohne das Feld: unbekannt, nichts passiert
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	time.Sleep(80 * time.Millisecond)
	if !wk.snapshot().FirstDone || wk.snapshot().DataID != "A" {
		t.Fatalf("leere Kennung setzt zurück: %+v", wk.snapshot())
	}
	// gleiche Kennung: nichts
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}, MirrorDataID: "A"}))
	m.tick()
	time.Sleep(50 * time.Millisecond)
	if !wk.snapshot().FirstDone {
		t.Fatal("gleiche Kennung setzt zurück")
	}
	// Ersatzserver: neue Kennung, bisher nichts übertragen
	m.rs.mu.Lock()
	m.rs.fn = func(c context.Context, _ []string) error { <-c.Done(); return c.Err() }
	m.rs.mu.Unlock()
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}, MirrorDataID: "B"}))
	m.tick()
	waitFor(t, 3*time.Second, "Erstabgleich verlangt", func() bool { return !wk.snapshot().FirstDone })
	st := wk.snapshot()
	if st.DataID != "B" || !st.LastOK.IsZero() || st.FirstPercent == 100 {
		t.Fatalf("Stand: %+v", st)
	}
	// Erst nach dem Speichern lesen: Unter Windows scheitert das Umbenennen, solange der Test die
	// Datei gerade offen hat (nur in diesem Test, Linux kennt das nicht).
	time.Sleep(300 * time.Millisecond)
	waitFor(t, 3*time.Second, "mirror.json mit neuer Kennung", func() bool { return strings.Contains(adoptedFile(m), `"data_id":"B"`) })
	// und der Herzschlag meldet nicht „fertig“
	m.d.mu.Lock()
	hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	m.d.mu.Unlock()
	if hb.MirrorFirst {
		t.Error("Herzschlag meldet fertig")
	}
}

// Neuer Partner (andere Kennung): der gespeicherte Stand eines anderen Partners zählt nicht.
func TestMirrorNewPartnerResetsState(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil || !f.w.snapshot().FirstDone {
		t.Fatalf("Vorlauf: %v %+v", err, f.w.snapshot())
	}
	same := newMirrorWorker(mirrorDeps{ev: f.ev, repo: "/r", node: "b", statePath: f.w.d.statePath})
	same.set(true, ha.Active, 1, mirrorSettings)
	if !same.snapshot().FirstDone {
		t.Fatal("gleicher Partner verliert den Stand")
	}
	other := newMirrorWorker(mirrorDeps{ev: f.ev, repo: "/r", node: "c", statePath: f.w.d.statePath})
	other.set(true, ha.Active, 1, mirrorSettings)
	if st := other.snapshot(); st.FirstDone || !st.LastOK.IsZero() || !st.PerLabel["uploads"].LastOK.IsZero() {
		t.Fatalf("neuer Partner erbt den Stand: %+v", st)
	}
}

// Herzschlag „Erstabgleich fertig“ nur mit echtem Stand der Quellen, auch während eines langen Laufs.
func TestMirrorHeartbeatFirstNotWhileRunning(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(filepath.Dir(m.w.statePath), 0o700)
	os.WriteFile(m.w.statePath, []byte(`{"first_done":true,"per_label":{"uploads":{"skipped":true},"chat_files":{"skipped":true},"etc-pdh":{"skipped":true}}}`), 0o600)
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	m.rs.mu.Lock()
	m.rs.fn = func(c context.Context, _ []string) error { <-c.Done(); return c.Err() }
	m.rs.mu.Unlock()
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "rsync läuft", func() bool { return m.rs.count() >= 1 })
	wk := rt.worker.Load()
	waitFor(t, 3*time.Second, "echter Stand", func() bool { return !wk.snapshot().FirstDone })
	for range 3 {
		m.tick()
		m.d.mu.Lock()
		hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
		m.d.mu.Unlock()
		if hb.MirrorFirst {
			t.Fatal("Herzschlag meldet fertig, obwohl eine Quelle noch läuft")
		}
		time.Sleep(300 * time.Millisecond)
	}
	// vor der ersten echten Prüfung meldet ein frischer Arbeiter nie „fertig“
	w2 := newMirrorWorker(mirrorDeps{ev: m.d.ev, repo: m.w.repo, node: "b", rsync: m.rs, statePath: m.w.statePath})
	w2.set(true, ha.Active, 1, m.d.settingsLocked())
	if st := w2.snapshot(); st.Checked {
		t.Fatalf("ungeprüft: %+v", st)
	}
}

// R23: Papierkorb leeren nur auf dem passiven Server, nur vom aktiven Partner, nur mit passender Amtszeit.
func TestMirrorTrashEmptyRoleChecks(t *testing.T) {
	type tc struct {
		name    string
		role    ha.Role
		partner string // Rolle des Partners im Herzschlag
		from    string
		term    uint64
		want    bool
	}
	for _, c := range []tc{
		{"passiver Empfänger, aktiver Partner", ha.Passive, "aktiv", "b", 1, true},
		{"passiver Absender", ha.Passive, "passiv", "b", 1, false},
		{"aktiver Empfänger", ha.Active, "passiv", "b", 1, false},
		{"Zeuge als Absender", ha.Passive, "aktiv", "w", 1, false},
		{"veraltete Amtszeit", ha.Passive, "aktiv", "b", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMDT(t, c.role, true, message{Role: c.partner})
			m.d.lock()
			m.d.cl.Nodes = append(m.d.cl.Nodes, clusterNode{ID: "w", Kind: "witness", Addrs: []string{"127.0.0.3:7480"}})
			m.d.unlock()
			m.tick()
			m.tick()
			m.ready()
			day := m.w.repo + "/.pdh-papierkorb/2026-11-02/uploads"
			os.MkdirAll(day, 0o755)
			os.WriteFile(day+"/f", []byte("x"), 0o644)
			exists := func() bool { _, err := os.Stat(day); return err == nil }
			m.d.handle(m.ctx, inbound{From: c.from, Msg: message{T: "mirror_trash_empty", ID: c.from, Seq: 9, Term: c.term}})
			if c.want {
				waitFor(t, 3*time.Second, "geleert", func() bool { return !exists() })
				waitFor(t, 3*time.Second, "Ereignis", func() bool { return hasEvent(m.d, "Papierkorb der Spiegelung geleert") })
				return
			}
			time.Sleep(150 * time.Millisecond)
			if !exists() {
				t.Fatal("Papierkorb gelöscht")
			}
			if c.from != "w" && !hasEvent(m.d, "abgelehnt") {
				t.Errorf("kein Ereignis: %v", eventTexts(m.d))
			}
		})
	}
}

// mirror run: wartet auf einen laufenden Abgleich und läuft dann selbst; ein Abbruch ist ein Fehler.
func TestMirrorRunWaitsAndReportsAbort(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	m.tick()
	rt := m.ready()
	wk := rt.worker.Load()
	waitFor(t, 3*time.Second, "erster Lauf", func() bool { return wk.snapshot().LastOK.After(time.Time{}) })
	// belegt: mirrorRun wartet, meldet nicht vorschnell Erfolg
	wk.sem <- struct{}{}
	n := m.rs.count()
	res := make(chan error, 1)
	go func() { res <- m.d.mirrorRun(m.ctx) }()
	select {
	case err := <-res:
		t.Fatalf("kehrt bei belegtem Lauf zurück: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if m.rs.count() != n {
		t.Fatal("rsync ohne Sperre")
	}
	<-wk.sem
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if m.rs.count() <= n {
		t.Fatal("kein eigener Lauf")
	}
	// Abbruch durch Rollenwechsel: Fehler statt Erfolg
	m.rs.mu.Lock()
	m.rs.fn = func(c context.Context, _ []string) error { <-c.Done(); return c.Err() }
	m.rs.mu.Unlock()
	n = m.rs.count()
	go func() { res <- m.d.mirrorRun(m.ctx) }()
	waitFor(t, 3*time.Second, "Lauf blockiert", func() bool { return m.rs.count() > n })
	wk.set(true, ha.Passive, 1, m.d.settingsLocked())
	select {
	case err := <-res:
		if err == nil || !strings.Contains(err.Error(), "abgebrochen") {
			t.Fatalf("Abbruch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mirror run hängt")
	}
}

// R24: dauert das Abschalten länger als die Wartezeit, wird die Sofort-Übertragung beim
// Wiedereinschalten trotzdem neu aufgebaut.
func TestMirrorRestartRebuildsWatchAfterSlowStop(t *testing.T) {
	old := mirrorStopWait
	mirrorStopWait = 30 * time.Millisecond
	t.Cleanup(func() { mirrorStopWait = old })
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	release := make(chan struct{})
	m.rs.mu.Lock()
	m.rs.fn = func(context.Context, []string) error { <-release; return nil } // ignoriert den Abbruch
	m.rs.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m.tick()
	rt := m.ready()
	wk := rt.worker.Load()
	waitFor(t, 3*time.Second, "rsync hängt", func() bool { return m.rs.count() >= 1 })
	waitFor(t, 3*time.Second, "Watch", func() bool { return hasWatch(wk) })
	wk.mu.Lock()
	w1 := wk.watch
	wk.mu.Unlock()
	m.setOn(false)
	m.tick()
	time.Sleep(200 * time.Millisecond) // Abschalten läuft in die Wartezeit
	close(release)
	waitFor(t, 3*time.Second, "Lauf endet", func() bool { return !rt.running.Load() })
	m.setOn(true)
	m.tick()
	waitFor(t, 3*time.Second, "Arbeiter läuft wieder", func() bool { return rt.running.Load() })
	waitFor(t, 3*time.Second, "neuer Watch", func() bool {
		wk.mu.Lock()
		defer wk.mu.Unlock()
		return wk.watch != nil && wk.watch != w1
	})
}
