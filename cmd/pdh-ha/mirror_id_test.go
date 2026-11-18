package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/ha"
)

// --- R57, R58: Kennungsdatei .pdh-mirror-id statt Gerätenummer ---

// idFile: Pfad der Kennungsdatei im uploads-Ziel des Empfänger-Gerüsts.
func idFile(f *inboxFix) string { return filepath.Join(f.root, "repo", "uploads", ha.MirrorIDName) }

// swapDir ersetzt den Ordner durch einen anderen ohne Kennung (Datenträger getauscht oder
// nicht eingehängt), mit den genannten Dateien.
func swapDir(t *testing.T, dir string, files ...string) {
	t.Helper()
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range files {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
}

func hasRefusal(f *inboxFix, sub string) bool { return strings.Contains(f.events(), sub) }

// T3: Das Ziel fehlt beim ersten Lauf (Einhängepunkt ist der Elternordner). Der Lauf legt Ordner und
// Kennung an und gibt den Beleg. Kommt der echte Datenträger ohne Kennung zurück, ist es ein anderer
// Ordner: Beleg gilt nicht, der Lauf wird mit Alarm abgelehnt, auch ohne --delete (R58).
func TestIDTargetAbsentFirstRun(t *testing.T) {
	f := newInboxFix(t)
	p := f.root + "/repo/uploads"
	run(t, f, argvDel(false))
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg nach dem Lauf auf das angelegte Ziel")
	}
	if _, err := os.Stat(idFile(f)); err != nil {
		t.Fatalf("Kennung nicht angelegt: %v", err)
	}
	swapDir(t, p, "eigen.txt")
	for _, del := range []bool{true, false} {
		if _, d, r := f.acceptReq(argvDel(del)); !strings.Contains(r, "Kennung") {
			t.Fatalf("--delete=%v: Ablehnung %q", del, r)
		} else if d != nil {
			t.Fatal("Abschluss trotz Ablehnung")
		}
	}
	if ok, _ := f.in.proofOf("uploads"); ok || len(f.in.proofs()) != 0 {
		t.Fatalf("Beleg gilt auf anderem Ordner: %v", f.in.proofs())
	}
	if !strings.Contains(f.events(), "alarm: Quelle uploads: Der Zielordner ist ein anderer") {
		t.Fatalf("kein Alarm: %s", f.events())
	}
	if _, err := os.Stat(p + "/eigen.txt"); err != nil {
		t.Fatal("eigene Datei weg")
	}
}

// S1: Der Datenträger wird während des Erstlaufs eingehängt: Vor dem Beleg wird die Kennung
// erneut geprüft, der Beleg entsteht nicht.
func TestIDRecheckBeforeProof(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	_, d, r := f.acceptReq(argvDel(false))
	if r != "" {
		t.Fatal(r)
	}
	swapDir(t, p, "eigen.txt")
	d(nil)
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Beleg trotz getauschtem Ordner")
	}
}

// T7: .ocdata wird vor der Kennung geprüft: Ein nicht eingehängter Nextcloud-Datenordner wird mit
// dem bekannten Alarm abgelehnt, nichts läuft auf das Systemlaufwerk.
func TestIDOcdataFirst(t *testing.T) {
	f := newInboxFix(t)
	p := f.root + "/nc/data"
	os.MkdirAll(p, 0o755)
	os.WriteFile(p+"/.ocdata", nil, 0o644)
	req := func(del bool) mirrorRequest {
		return mirrorRequest{Label: "nextcloud-data", Term: 3, Argv: recvArgv(del)}
	}
	run(t, f, mirrorRequest{Label: "nextcloud-data", Term: 3, Argv: recvArgv(true), Confirmed: true})
	if ok, _ := f.in.proofOf("nextcloud-data"); !ok {
		t.Fatal("kein Beleg")
	}
	swapDir(t, p) // nicht eingehängt: leer, ohne .ocdata und ohne Kennung
	_, d, r := f.acceptReq(req(false))
	if r != "Datenordner nicht erreichbar" || d != nil {
		t.Fatalf("Ablehnung %q", r)
	}
	if !strings.Contains(f.events(), ".ocdata fehlt") {
		t.Fatalf("kein ocdata-Alarm: %s", f.events())
	}
	if got := f.in.waits(); len(got) != 0 {
		t.Fatalf("wartet statt abzulehnen: %v", got)
	}
}

// Nach Aus und Ein (Zustand verworfen) gilt der Ordner als neu: Die vorhandene Kennung wird
// übernommen, ein nicht leeres Ziel wartet auf die Bestätigung (R58).
func TestIDAfterOffOn(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	run(t, f, argvDel(false))
	old, _ := os.ReadFile(idFile(f))
	os.WriteFile(p+"/eigen.txt", []byte("x"), 0o644)
	f.in.dropProofs()
	f.rebuild()
	run(t, f, argvDel(false))
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet nicht: %v", got)
	}
	if now, _ := os.ReadFile(idFile(f)); string(now) != string(old) {
		t.Fatal("Kennung neu geschrieben statt übernommen")
	}
	r := argvDel(true)
	r.Confirmed = true
	run(t, f, r)
	if ok, _ := f.in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg nach der Bestätigung")
	}
}

// Leer-Prüfung: Kennung und lost+found zählen nicht als Inhalt.
func TestMirrorTargetEmptyIgnoresIDAndLostFound(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ha.MirrorIDName), []byte("abc"), 0o644)
	os.MkdirAll(filepath.Join(d, "lost+found"), 0o700)
	os.WriteFile(filepath.Join(d, "lost+found", "#123"), []byte("x"), 0o600)
	if !mirrorTargetEmpty(d, []string{"--exclude=/" + ha.MirrorIDName}) {
		t.Fatal("Kennung oder lost+found zählt als Inhalt")
	}
	os.WriteFile(filepath.Join(d, "echt.txt"), []byte("x"), 0o644)
	if mirrorTargetEmpty(d, []string{"--exclude=/" + ha.MirrorIDName}) {
		t.Fatal("Datei nicht gezählt")
	}
}

// nextcloud-app: Die Kennung liegt in config/ (Integritätsprüfung von Nextcloud), der Empfänger legt
// den Ordner dafür an.
func TestIDAppDirUnderConfig(t *testing.T) {
	f := newInboxFix(t)
	f.srcs = append(f.srcs, ha.MirrorSource{Label: "nextcloud-app", Path: f.root + "/nc/app", Exclude: []string{"--exclude=/config/" + ha.MirrorIDName}})
	run(t, f, mirrorRequest{Label: "nextcloud-app", Term: 3, Argv: recvArgv(false)})
	if _, err := os.Stat(filepath.Join(f.root, "nc", "app", "config", ha.MirrorIDName)); err != nil {
		t.Fatalf("Kennung fehlt in config/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "nc", "app", ha.MirrorIDName)); err == nil {
		t.Fatal("Kennung im Programmordner selbst")
	}
}

// --- Absender ---

// idWorker: Arbeiter mit echter Ablage: Beleg und Kennung aus demselben Empfänger (ein Eintrag je
// Ordner für Quelle und Ziel, R57).
func idWorker(t *testing.T) (*fixProof, *mirrorInbox, *mirrorWorker) {
	t.Helper()
	f := newFixProof(t)
	s := ha.DefaultSettings()
	s.MirrorOn = true
	in := newMirrorInbox(mirrorInboxDeps{
		ev: f.ev, repo: f.repo, run: f.rs, statePath: filepath.Join(f.dir, mirrorInboxFile),
		view: func() mirrorInboxView { return mirrorInboxView{Role: ha.Passive, Term: 3, Settings: s} },
		sources: func(ha.Settings) []ha.MirrorSource {
			return []ha.MirrorSource{{Label: "uploads", Path: f.repo + "/uploads"}}
		},
	})
	w := newMirrorWorker(mirrorDeps{ev: f.ev, repo: f.repo, node: "b", rsync: f.rs, statePath: f.state,
		proof: in.proofOf, ident: in.checkID, identRenew: in.renewID,
		partnerProof: func(l string) bool { return f.proven[l] },
		partnerWait:  func(l string) bool { return f.wait[l] },
		exists:       func(p string) bool { _, err := os.Stat(p); return err == nil && strings.Contains(p, "/uploads") }})
	return f, in, w
}

// T4: Datenträger im selben Steckplatz getauscht: Die neue Platte hat Dateien, aber keine Kennung.
// Kein --delete, ein Alarm mit der Ursache, Bestätigung nötig; sie schreibt die neue Kennung.
func TestIDSourceSwappedNoDelete(t *testing.T) {
	for _, c := range []string{"eine Datei", "nur leerer Unterordner"} {
		t.Run(c, func(t *testing.T) {
			f, in, w := idWorker(t)
			f.proven["uploads"] = true
			w.set(true, ha.Active, 1, fix2Settings)
			w.observeDataID("k")
			if err := w.runAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
				t.Fatalf("erster Lauf: %s", j)
			}
			if _, err := os.Stat(filepath.Join(f.repo, "uploads", ha.MirrorIDName)); err != nil {
				t.Fatalf("Kennung der Quelle fehlt: %v", err)
			}
			swapDir(t, f.repo+"/uploads")
			if c == "eine Datei" {
				os.WriteFile(f.repo+"/uploads/neu.txt", []byte("x"), 0o644)
			} else {
				os.MkdirAll(f.repo+"/uploads/avatars", 0o755)
			}
			n0 := f.rs.count()
			err := runAgain(w)
			if c == "nur leerer Unterordner" {
				// ohne echte Datei läuft nichts (refusedEmpty), die Kennung gilt trotzdem als geprüft
				if err == nil || !strings.Contains(err.Error(), "leer") || f.rs.count() != n0 {
					t.Fatalf("leere Quelle: err=%v Aufrufe=%d", err, f.rs.count()-n0)
				}
			} else if j := lastUploads(f.rs); err != nil || strings.Contains(j, "--delete") {
				t.Fatalf("löscht nach dem Tausch: err=%v %s", err, j)
			}
			if !w.unproven("uploads") {
				t.Fatal("Quelle nicht unbelegt")
			}
			if n := f.alarms("Quelle uploads: Der Quellordner ist ein anderer"); n != 1 {
				t.Fatalf("Alarme: %d %v", n, f.ev.readAll())
			}
			if got := w.waiting(); len(got) != 1 {
				t.Fatalf("wartet: %v", got)
			}
			if _, err := w.confirm(nil); err != nil {
				t.Fatal(err)
			}
			id, _ := os.ReadFile(filepath.Join(f.repo, "uploads", ha.MirrorIDName))
			if len(id) == 0 || in.checkID("uploads", filepath.Join(f.repo, "uploads", ha.MirrorIDName)) != idOK {
				t.Fatal("Bestätigung schreibt keine passende Kennung")
			}
			if c == "nur leerer Unterordner" {
				os.WriteFile(f.repo+"/uploads/neu.txt", []byte("x"), 0o644) // leer bliebe leer (refusedEmpty)
			}
			if err := runAgain(w); err != nil {
				t.Fatal(err)
			}
			if j := lastUploads(f.rs); !strings.Contains(j, "--delete") {
				t.Fatalf("nach der Bestätigung: %s", j)
			}
		})
	}
}

// T5: Nie aktiver Server: Beleg und Kennung stammen aus dem Empfang. Neustart ohne Einhängepunkt,
// der Partner ist weg (kein Accept), Übernahme: derselbe Eintrag zählt, die Quelle ist unbelegt.
func TestIDNeverActiveTakeover(t *testing.T) {
	f, in, w := idWorker(t)
	f.proven["uploads"] = true
	// Empfang als passiver Server: Lauf auf das Ziel gibt Beleg und Kennung
	_, d, r := in.Accept(context.Background(), "b", "node", mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(true), Confirmed: true})
	if r != "" {
		t.Fatal(r)
	}
	d(nil)
	if ok, _ := in.proofOf("uploads"); !ok {
		t.Fatal("kein Beleg aus dem Empfang")
	}
	w.set(true, ha.Passive, 1, fix2Settings)
	swapDir(t, f.repo+"/uploads", "neu.txt") // nicht eingehängt, PDH schreibt auf die Systemplatte
	w.set(true, ha.Active, 2, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") || !w.unproven("uploads") {
		t.Fatalf("löscht beim Rückkehrer: %s", j)
	}
	if n := f.alarms("Quelle uploads: Der Quellordner ist ein anderer"); n != 1 {
		t.Fatalf("Alarme: %d %v", n, f.ev.readAll())
	}
	if n := f.alarms("bevor der Erstabgleich fertig war"); n != 0 {
		t.Fatalf("falsche Ursache gemeldet: %v", f.ev.readAll())
	}
}

// T6: Absender-Beleg (noteSource) gilt nur mit passender Kennung: Nach dem Tausch der Quelle hebt
// liftUnproven nichts auf, auch ohne Empfang; ein Accept lehnt ab; der Rücktausch löscht nicht.
func TestIDLiftNeedsMatchingID(t *testing.T) {
	f, in, w := idWorker(t)
	f.proven["uploads"] = true
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.noteOwnProof([]string{"uploads"}, in.noteSource)
	if ok, _ := in.proofOf("uploads"); !ok {
		t.Fatal("kein Absender-Beleg")
	}
	swapDir(t, f.repo+"/uploads", "neu.txt")
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if !w.unproven("uploads") {
		t.Fatal("Quelle nicht unbelegt nach dem Tausch")
	}
	w.set(true, ha.Passive, 2, fix2Settings)
	w.liftUnproven(in.proofOf)
	if !w.unproven("uploads") {
		t.Fatal("passiv ohne Empfang: unbelegt aufgehoben")
	}
	if _, d, r := in.Accept(context.Background(), "b", "node", mirrorRequest{Label: "uploads", Term: 3, Argv: recvArgv(false)}); !strings.Contains(r, "Kennung") || d != nil {
		t.Fatalf("Empfang auf anderem Ordner: %q", r)
	}
	w.set(true, ha.Active, 3, fix2Settings)
	if err := runAgain(w); err != nil {
		t.Fatal(err)
	}
	if j := lastUploads(f.rs); strings.Contains(j, "--delete") {
		t.Fatalf("Rücktausch löscht: %s", j)
	}
}

// noteSource schreibt keinen Beleg, solange die Kennung dieses Ordners nicht geprüft ist (nach einem
// Neustart), und proofs() meldet im Herzschlag nur geprüfte Belege.
func TestIDProofsNeedCheckedID(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	f.in.noteSource("uploads", true)
	if ok, _ := f.in.proofOf("uploads"); ok {
		t.Fatal("Beleg ohne geprüfte Kennung")
	}
	run(t, f, argvDel(false))
	if got := f.in.proofs(); len(got) != 1 {
		t.Fatalf("Herzschlag: %v", got)
	}
	f.rebuild() // Neustart: der Beleg steht in der Datei, die Kennung ist noch nicht geprüft
	if got := f.in.proofs(); len(got) != 0 {
		t.Fatalf("Herzschlag nach Neustart ohne Prüfung: %v", got)
	}
	run(t, f, argvDel(true))
	if got := f.in.proofs(); len(got) != 1 {
		t.Fatalf("Herzschlag nach Prüfung: %v", got)
	}
}

// Die Sofort-Übertragung schickt die Kennung nie, auch nicht über die Pfadliste.
func TestIDWatchPathSkipped(t *testing.T) {
	f := newFix2(t)
	os.WriteFile(f.repo+"/uploads/"+ha.MirrorIDName, []byte("x"), 0o644)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	if err := w.runPaths(context.Background(), "uploads", []string{ha.MirrorIDName}); err != nil {
		t.Fatal(err)
	}
	if n := f.rs.count(); n != 0 {
		t.Fatalf("rsync für die Kennung gerufen: %d", n)
	}
	if err := w.runPaths(context.Background(), "uploads", []string{"a.txt"}); err != nil || f.rs.count() != 1 {
		t.Fatalf("echter Pfad: %v %d", err, f.rs.count())
	}
}

// Minor: liftUnproven nimmt den Alarm-Merker zurück, die nächste wartende Quelle meldet sich wieder.
func TestLiftUnprovenClearsWaitAlarmed(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	w.mu.Lock()
	ls := w.st.PerLabel["uploads"]
	ls.Unproven, ls.WaitAlarmed = true, true
	w.st.PerLabel["uploads"] = ls
	w.mu.Unlock()
	w.liftUnproven(func(string) (bool, bool) { return true, true })
	f.wait["uploads"] = true
	w.noteWaiting()
	if n := f.alarms("Quelle uploads: "); n != 1 {
		t.Fatalf("Alarme nach dem Aufheben: %d", n)
	}
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

// S1: Wer wartet, kommt nur über die Bestätigung heraus, auch wenn das Ziel später leer ist
// (Einhängepunkt fehlt); --delete bleibt abgelehnt.
func TestInboxWaitIsBinding(t *testing.T) {
	f := newInboxFix(t)
	p := uploadsDir(t, f)
	os.WriteFile(p+"/eigen.txt", []byte("x"), 0o644)
	run(t, f, argvDel(false))
	if got := f.in.waits(); len(got) != 1 {
		t.Fatalf("wartet: %v", got)
	}
	os.Remove(p + "/eigen.txt")
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
