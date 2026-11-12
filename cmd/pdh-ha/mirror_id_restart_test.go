package main

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R63: Kennung bei Übernahme, Neustart, Aus/Ein und Alarmtext ---

func adoptedOf(w *mirrorWorker, label string) bool { return w.snapshot().PerLabel[label].Adopted }

// U3: Die Quelle fehlt bei der Übernahme (Erstabgleich nie bis dorthin gekommen), ohne eigenen Beleg
// und ohne Kennung. Legt PDH den Ordner später an, darf der Beleg des Partners allein kein --delete
// geben: Die Quelle bleibt unbelegt bis zur Bestätigung, mit Alarm.
func TestPromotedAbsentStaysUnproven(t *testing.T) {
	f, _, w := idWorker(t)
	f.proven["uploads"] = true // der Rückkehrer meldet seinen älteren Beleg
	os.RemoveAll(f.repo + "/uploads")
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings) // Übernahme
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.rs.count(); n != 0 {
		t.Fatalf("Lauf ohne Quelle: %d", n)
	}
	// PDH legt den Ordner beim ersten Hochladen an
	os.MkdirAll(f.repo+"/uploads", 0o755)
	os.WriteFile(f.repo+"/uploads/neu.txt", []byte("x"), 0o644)
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); j == "" || strings.Contains(j, "--delete") {
		t.Fatalf("löscht beim Rückkehrer allein aus seinem Beleg: %q", j)
	}
	if !w.unproven("uploads") {
		t.Fatal("Quelle nicht unbelegt")
	}
	// T14 B7: eigener Text – der Ordner kam neu dazu, der Erstabgleich war nicht „unfertig“
	if n := f.alarms("Quelle uploads: Der Ordner ist neu dazugekommen, seit dieser Server aktiv wurde"); n != 1 {
		t.Fatalf("Alarme: %d %v", n, f.ev.readAll())
	}
	if n := f.alarms("bevor der Erstabgleich fertig war"); n != 0 {
		t.Fatalf("falscher Alarmtext für eine neu dazugekommene Quelle: %v", f.ev.readAll())
	}
	// Neustart des Arbeiters: der Stand bleibt
	w2 := newMirrorWorker(w.d)
	if !w2.unproven("uploads") {
		t.Fatal("nach Neustart nicht mehr unbelegt")
	}
	if _, err := w.confirm(nil); err != nil {
		t.Fatal(err)
	}
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
		t.Fatalf("nach der Bestätigung: %s", j)
	}
}

// Kontrolle zu U3: Hat dieser Server bei der Übernahme einen eigenen Beleg, gilt er auch für eine
// Quelle, die erst später auftaucht (kein Alarm).
func TestPromotedAbsentWithOwnProof(t *testing.T) {
	f, in, w := idWorker(t)
	f.proven["uploads"] = true
	_, d, r := in.Accept(context.Background(), "b", "node", mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(true), Confirmed: true})
	if r != "" {
		t.Fatal(r)
	}
	d(nil)
	w.set(true, ha.Passive, 1, fix2Settings)
	w.set(true, ha.Active, 2, fix2Settings)
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); !strings.Contains(j, "--delete") || w.unproven("uploads") {
		t.Fatalf("mit eigenem Beleg: %s unbelegt=%v", j, w.unproven("uploads"))
	}
	if n := f.alarms("bevor der Erstabgleich fertig war"); n != 0 {
		t.Fatalf("Alarm trotz Beleg: %v", f.ev.readAll())
	}
}

// U4: Nach einem Neustart des passiven Servers ist die Kennung noch ungeprüft. Das allein nimmt
// den übernommenen Erstabgleich nicht zurück; erst ein fehlender Beleg oder ein anderer Ordner.
// Sonst sperrte eine Übernahme im Fenster die Spiegelung mit „Zu wenig Platz“.
func TestAdoptKeptWhileIDUnchecked(t *testing.T) {
	f, in, w := idWorker(t)
	_, d, r := in.Accept(context.Background(), "b", "node", mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(true), Confirmed: true})
	if r != "" {
		t.Fatal(r)
	}
	d(nil)
	w.set(true, ha.Passive, 1, fix2Settings)
	w.adoptFirst(true, in.proofOf, in.proofLost)
	if !adoptedOf(w, "uploads") {
		t.Fatal("Erstabgleich nicht übernommen")
	}
	in2 := newMirrorInbox(in.d) // Neustart: Beleg in der Datei, Kennung noch nicht geprüft
	w.adoptFirst(true, in2.proofOf, in2.proofLost)
	if !adoptedOf(w, "uploads") {
		t.Fatal("ungeprüfte Kennung nimmt den Erstabgleich zurück")
	}
	// Übernahme im Fenster, Partner mit wenig Platz: es läuft trotzdem
	w.d.free = func() (int64, bool) { return 1, true }
	w.set(true, ha.Active, 2, fix2Settings)
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatalf("nach der Übernahme: %v", err)
	}
	if lastUploads(f.rs) == "" {
		t.Fatal("kein Lauf nach der Übernahme")
	}
	// anderer Ordner: jetzt fällt die Übernahme
	w.set(true, ha.Passive, 3, fix2Settings)
	swapDir(t, f.repo+"/uploads", "neu.txt")
	in2.recheckIDs(in2.sources(fix2Settings))
	w.adoptFirst(true, in2.proofOf, in2.proofLost)
	if adoptedOf(w, "uploads") {
		t.Fatal("anderer Ordner behält den Erstabgleich")
	}
}

// U4: Startprüfung am passiven Server: Nach dem Neustart gilt der gespeicherte Beleg wieder, sobald
// die Kennung einmal außerhalb des Takts geprüft ist; ein fehlender Ordner zählt als anderer.
func TestRecheckIDsAfterRestart(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	run(t, f, argvDel(false))
	f.rebuild()
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Beleg gilt ohne Prüfung")
	}
	if !f.in.proofStored("uploads") || f.in.proofLost("uploads") {
		t.Fatal("gespeicherter Beleg gilt als verloren")
	}
	f.in.recheckIDs(f.srcs)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("Beleg nach der Startprüfung nicht zurück")
	}
	if _, err := os.Stat(f.root + "/etc/nginx/" + ha.MirrorIDName); err == nil {
		t.Fatal("Startprüfung legt eine Kennung für eine unbekannte Quelle an")
	}
	os.RemoveAll(f.root + "/repo/uploads")
	f.in.recheckIDs(f.srcs)
	if ok, _ := f.in.proofOf("uploads"); ok || !f.in.proofLost("uploads") {
		t.Fatal("fehlender Ordner gilt als derselbe")
	}
}

// U4 Verdrahtung: Der passive Server prüft nach dem Start die Kennungen (Schleife, nicht im Takt);
// danach meldet der Herzschlag den Beleg wieder, und der übernommene Erstabgleich bleibt.
func TestPassiveStartChecksIDs(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv", MirrorFirst: true})
	repo := m.w.repo
	os.MkdirAll(repo+"/uploads", 0o755)
	const id = "0123456789abcdef0123456789abcdef"
	os.WriteFile(repo+"/uploads/"+ha.MirrorIDName, []byte(id+"\n"), 0o644)
	state := filepath.Dir(m.w.statePath)
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, mirrorInboxFile), []byte(`{"proof":{"uploads":{"at":"2026-11-01T00:00:00Z"}},"id":{"uploads":"`+id+`"},"data_id":"x"}`), 0o600)
	os.WriteFile(m.w.statePath, []byte(`{"first_done":false,"per_label":{"uploads":{"adopted":true}},"partner":"b"}`), 0o600)
	m.tick()
	rt := m.ready()
	waitFor(t, 5*time.Second, "Kennung geprüft", func() bool {
		ok, _ := rt.inbox.Load().proofOf("uploads")
		return ok
	})
	m.tick()
	time.Sleep(100 * time.Millisecond)
	if !adoptedOf(rt.worker.Load(), "uploads") {
		t.Fatal("übernommener Erstabgleich nach dem Neustart zurückgenommen")
	}
}

// U5: Aus nimmt am Absender „Erstabgleich fertig“ je Quelle zurück: Nach dem Einschalten greift die
// Platzprüfung wieder (sonst lief der ganze Bestand auf ein nicht eingehängtes Ziel).
func TestOffResetsFirstDone(t *testing.T) {
	f, _, w := idWorker(t)
	f.proven["uploads"] = true
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !w.snapshot().FirstDone {
		t.Fatal("Erstabgleich nicht fertig")
	}
	w.set(false, ha.Active, 1, fix2Settings)
	w.dropOff()
	if w.snapshot().FirstDone || !w.snapshot().PerLabel["uploads"].LastOK.IsZero() {
		t.Fatalf("nach Aus noch fertig: %+v", w.snapshot().PerLabel["uploads"])
	}
	if !newMirrorWorker(w.d).snapshot().PerLabel["uploads"].LastOK.IsZero() {
		t.Fatal("Rücknahme nicht gespeichert")
	}
	w.d.free = func() (int64, bool) { return 0, true }
	w.set(true, ha.Active, 1, fix2Settings)
	n0 := f.rs.count()
	err := runAgain(w)
	if err == nil || !strings.Contains(err.Error(), "Zu wenig Platz") || f.rs.count() != n0 {
		t.Fatalf("Platzprüfung nach Aus/Ein: err=%v Läufe=%d", err, f.rs.count()-n0)
	}
}

// U5: Der Alarm des Empfängers nennt zuerst das Einhängen, den gewollten Tausch danach und getrennt.
func TestIDOtherAlarmMountFirst(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	run(t, f, argvDel(false))
	swapDir(t, f.root+"/repo/uploads")
	f.acceptReq(argvDel(false))
	ev := f.events()
	i, j := strings.Index(ev, "Nicht eingehängt: einhängen"), strings.Index(ev, "Nur bei gewolltem Tausch: Spiegelung aus- und wieder einschalten")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("Alarm trennt die Fälle nicht: %s", ev)
	}
}

// NEU-3: Aus verwirft auch eine Kennung, die allein im Zustand steht, dauerhaft.
func TestDropProofsSavesIDOnly(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	if f.in.checkID("uploads", path.Join(p, ha.MirrorIDName)) != idOK || !f.in.hasID("uploads") {
		t.Fatal("Kennung nicht übernommen")
	}
	f.in.dropProofs()
	f.rebuild()
	if f.in.hasID("uploads") {
		t.Fatal("Kennung nach Aus und Neustart wieder da")
	}
}
