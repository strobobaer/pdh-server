package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Quelle ssl-pdh (/etc/ssl/pdh): Zertifikate des PDH. Ein Merkmal wie Cloudflare – es muss auch
// ohne laufen: Wo der Ordner fehlt (der Normalfall), verhält sich die Spiegelung wie vorher.

func sslPdhCalls(rs *fakeRsync) (n int, del bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, c := range rs.calls {
		if strings.HasSuffix(c[len(c)-1], ":ssl-pdh") {
			n++
			del = del || strings.Contains(strings.Join(c, " "), "--delete")
		}
	}
	return
}

func kindTexts(f *mirrorFix, kinds ...string) []string {
	var out []string
	for _, k := range kinds {
		out = append(out, f.texts(k)...)
	}
	return out
}

// Kein Ordner /etc/ssl/pdh: kein Aufruf, kein Alarm, keine Warnung, kein Fehler, kein Eintrag im
// Zustand, der Erstabgleich ist wie bisher nach drei Quellen fertig.
func TestMirrorSslPdhAbsentChangesNothing(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := sslPdhCalls(f.rs); n != 0 {
		t.Fatalf("rsync für ssl-pdh ohne Ordner: %d Aufrufe", n)
	}
	st := f.w.snapshot()
	if !st.FirstDone || st.FirstPercent != 100 || st.Err != "" {
		t.Fatalf("Stand: %+v", st)
	}
	if _, ok := st.PerLabel["ssl-pdh"]; ok {
		t.Fatalf("Eintrag für ssl-pdh im Zustand: %+v", st.PerLabel["ssl-pdh"])
	}
	if got := kindTexts(f, "alarm", "warn", "error"); len(got) != 0 {
		t.Fatalf("Meldungen: %v", got)
	}
	for _, l := range []string{"uploads", "chat_files", "etc-pdh"} {
		if st.PerLabel[l].LastOK.IsZero() {
			t.Errorf("%s nicht gelaufen", l)
		}
	}
}

// Ein Stand aus der Zeit vor dieser Quelle (Erstabgleich fertig) bleibt fertig – auch vor dem
// ersten Lauf der neuen Version und nach einem Neustart des Arbeiters.
func TestMirrorSslPdhAbsentKeepsFirstDone(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.runAll(context.Background())
	// neuer Arbeiter mit demselben Stand, Ordner weiter nicht da
	w2 := newMirrorWorker(f.w.d)
	w2.set(true, ha.Active, 1, mirrorSettings)
	w2.refreshFirst()
	if st := w2.snapshot(); !st.FirstDone || st.FirstPercent != 100 {
		t.Fatalf("Stand nach Neustart: %+v", st)
	}
}

// Die Statusliste zeigt für ssl-pdh nur etwas, wenn es etwas zu sagen gibt (Lauf, Fehler,
// unbelegt, übersprungen) – nie eine leere Zeile auf Servern ohne Zertifikate.
func TestMirrorSslPdhQuietRow(t *testing.T) {
	for _, c := range []struct {
		name  string
		label string
		ls    mirrorLabelState
		quiet bool
	}{
		{"leer", "ssl-pdh", mirrorLabelState{}, true},
		{"vorgemerkt bei Übernahme", "ssl-pdh", mirrorLabelState{AbsentAtPromote: true}, true},
		{"gelaufen", "ssl-pdh", mirrorLabelState{LastOK: time.Now()}, false},
		{"Fehler", "ssl-pdh", mirrorLabelState{Err: "x"}, false},
		{"unbelegt", "ssl-pdh", mirrorLabelState{Unproven: true}, false},
		{"übersprungen", "ssl-pdh", mirrorLabelState{Skipped: true}, false},
		{"andere Quelle bleibt sichtbar", "nginx", mirrorLabelState{}, false},
	} {
		if got := mirrorQuietLabel(c.label, c.ls); got != c.quiet {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.quiet)
		}
	}
}

// Mit Ordner: gespiegelt wie jede andere Quelle (mit --delete), in den Zustand eingetragen.
func TestMirrorSslPdhPresentIsMirrored(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, del := sslPdhCalls(f.rs); n != 1 || !del {
		t.Fatalf("ssl-pdh: %d Aufrufe, mit --delete %v", n, del)
	}
	var argv string
	for _, c := range f.rs.calls {
		if strings.HasSuffix(c[len(c)-1], ":ssl-pdh") {
			argv = strings.Join(c, " ")
		}
	}
	if !strings.Contains(argv, "--label ssl-pdh ") || !strings.Contains(argv, " /etc/ssl/pdh/ b:ssl-pdh") ||
		!strings.Contains(argv, "--exclude=/.pdh-mirror-id") {
		t.Fatalf("Aufruf: %s", argv)
	}
	if st := f.w.snapshot(); st.PerLabel["ssl-pdh"].LastOK.IsZero() || !st.FirstDone {
		t.Fatalf("Stand: %+v", st)
	}
}

// Die Zertifikate laufen im letzten Abgleich direkt hinter denen von Nextcloud, vor letsencrypt.
func TestMirrorSslPdhFinalOrder(t *testing.T) {
	var in []ha.MirrorSource
	for _, l := range []string{"ssl-pdh", "letsencrypt", "ssl-pdh-nextcloud", "nginx", "etc-pdh", "nextcloud-app", "uploads"} {
		in = append(in, ha.MirrorSource{Label: l})
	}
	var got []string
	for _, m := range finalOrder(in) {
		got = append(got, m.Label)
	}
	if want := "uploads,nextcloud-app,etc-pdh,nginx,ssl-pdh-nextcloud,ssl-pdh,letsencrypt"; strings.Join(got, ",") != want {
		t.Fatalf("Reihenfolge %s, erwartet %s", strings.Join(got, ","), want)
	}
}

// Alle Zertifikate entfernt: Der Ordner /etc/ssl/pdh bleibt leer zurück (der Agent löscht nur den
// Namensordner). Das ist ein gewöhnlicher Lauf mit --delete – auch das letzte Zertifikat samt Schlüssel
// verschwindet am Partner, sonst käme es nach dem nächsten Tausch zurück. Der Schutz vor einer leeren
// Quelle (R55, Datenträger nicht eingehängt) gilt für Ordner unter /etc nicht; für uploads bleibt er.
func TestMirrorSslPdhLastCertRemovedPropagates(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// eines von zwei Zertifikaten entfernt: Ordner hat noch Inhalt, Lauf mit --delete
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	n1, del1 := sslPdhCalls(f.rs)
	if n1 != 2 || !del1 {
		t.Fatalf("eines von zwei entfernt: %d Aufrufe, mit --delete %v", n1, del1)
	}
	// das letzte entfernt: Ordner da, aber leer
	f.full["/etc/ssl/pdh"] = false
	f.rs.mu.Lock()
	f.rs.calls = nil
	f.rs.mu.Unlock()
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	n, del := sslPdhCalls(f.rs)
	if n != 1 || !del {
		t.Fatalf("leerer Ordner: %d Aufrufe, mit --delete %v (erwartet 1, mit)", n, del)
	}
	if st := f.w.snapshot(); st.Err != "" || st.PerLabel["ssl-pdh"].Err != "" {
		t.Fatalf("Fehler: %+v", st)
	}
	if got := kindTexts(f, "alarm", "warn", "error"); len(got) != 0 {
		t.Fatalf("Meldungen: %v", got)
	}
	// für uploads gilt die Regel unverändert (R55)
	f.full["/r/uploads"] = false
	runAgain(f.w)
	if !strings.Contains(f.w.snapshot().PerLabel["uploads"].Err, "leer") {
		t.Fatalf("uploads leer: %+v", f.w.snapshot().PerLabel["uploads"])
	}
}

// Der Ordner fehlt ganz (letzter Namensordner samt Wurzel weg): weiter völlig still, kein Lauf.
func TestMirrorSslPdhFolderGoneAfterRunIsQuiet(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.runAll(context.Background())
	delete(f.files, "/etc/ssl/pdh")
	before, _ := sslPdhCalls(f.rs)
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	if n, _ := sslPdhCalls(f.rs); n != before {
		t.Fatalf("Lauf ohne Ordner: %d statt %d", n, before)
	}
	if got := kindTexts(f, "alarm", "warn", "error"); len(got) != 0 {
		t.Fatalf("Meldungen: %v", got)
	}
}

// partnerRig: Tausch ohne Zertifikate (Ordner fehlt bei der Übernahme, kein eigener Beleg); die
// übrigen Quellen sind belegt. proven/waits steuern, was der Partner meldet.
type partnerRig struct {
	f      *mirrorFix
	proven map[string]bool
	waits  map[string]bool
}

func newPartnerRig(t *testing.T) *partnerRig {
	t.Helper()
	r := &partnerRig{f: newMirrorFix(t), proven: map[string]bool{"uploads": true, "chat_files": true, "etc-pdh": true}, waits: map[string]bool{}}
	r.f.w.d.proof = func(l string) (bool, bool) { return l != "ssl-pdh", true }
	r.f.w.d.partnerProof = func(l string) bool { return r.proven[l] }
	r.f.w.d.partnerWait = func(l string) bool { return r.waits[l] }
	r.f.w.set(true, ha.Passive, 1, mirrorSettings)
	r.f.w.set(true, ha.Active, 2, mirrorSettings)
	if err := r.f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kindTexts(r.f, "alarm", "warn", "error"); len(got) != 0 {
		t.Fatalf("Meldungen ohne Zertifikate: %v", got)
	}
	if ls := r.f.w.snapshot().PerLabel["ssl-pdh"]; !mirrorQuietLabel("ssl-pdh", ls) {
		t.Fatalf("Zeile ohne Inhalt sichtbar: %+v", ls)
	}
	return r
}

// Nicht-optionale Quellen ändern sich nicht: Erscheint uploads nach der Übernahme, gilt R63 sofort.
func TestMirrorNonOptionalAppearingStillUnproven(t *testing.T) {
	f := newMirrorFix(t)
	delete(f.files, "/r/uploads")
	f.w.d.proof = func(l string) (bool, bool) { return l != "uploads", true }
	f.w.d.partnerProof = func(string) bool { return true }
	f.w.set(true, ha.Passive, 1, mirrorSettings)
	f.w.set(true, ha.Active, 2, mirrorSettings)
	f.w.runAll(context.Background())
	f.files["/r/uploads"] = true
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	if !f.w.snapshot().PerLabel["uploads"].Unproven || len(f.texts("alarm")) != 1 {
		t.Fatalf("uploads: %+v, Alarme %v", f.w.snapshot().PerLabel["uploads"], f.texts("alarm"))
	}
}

// Empfänger: ssl-pdh wird wie jede bekannte Quelle angenommen, legt den Zielordner an (Eltern da)
// und lädt nginx neu, wenn ein früherer Lauf Einstellungen geändert hat und nginx -t damals scheiterte.
func TestInboxSslPdhReloadsNginxWhenDirty(t *testing.T) {
	f := newInboxFix(t)
	f.srcs = append(f.srcs, ha.MirrorSource{Label: "ssl-pdh", Path: f.root + "/etc/ssl/pdh"})
	f.rebuild()
	f.nginxRun(t, true, "nginx -t") // Zertifikate fehlen noch
	f.rs.mu.Lock()
	f.rs.calls, f.rs.fn = nil, nil
	f.rs.mu.Unlock()
	os.MkdirAll(f.root+"/etc/ssl", 0o755)
	_, done, refuse := f.accept("ssl-pdh", 3)
	if refuse != "" {
		t.Fatalf("abgelehnt: %q", refuse)
	}
	if fi, err := os.Stat(f.root + "/etc/ssl/pdh"); err != nil || !fi.IsDir() {
		t.Fatalf("Zielordner nicht angelegt: %v", err)
	}
	done(nil)
	if got := strings.Join(f.calls(), ";"); got != "nginx -t;systemctl reload nginx" {
		t.Fatalf("Zertifikate-Lauf muss nginx neu laden: %q", got)
	}
	_, done, _ = f.accept("ssl-pdh", 3)
	done(nil)
	if len(f.calls()) != 2 {
		t.Fatalf("ohne Merker kein Neuladen: %v", f.calls())
	}
}

// Ohne Zertifikate und ohne nginx-Änderung rührt ein Lauf von ssl-pdh nginx nie an.
func TestInboxSslPdhLeavesNginxAloneWhenClean(t *testing.T) {
	f := newInboxFix(t)
	f.srcs = append(f.srcs, ha.MirrorSource{Label: "ssl-pdh", Path: f.root + "/etc/ssl/pdh"})
	f.rebuild()
	os.MkdirAll(f.root+"/etc/ssl", 0o755)
	_, done, refuse := f.accept("ssl-pdh", 3)
	if refuse != "" {
		t.Fatal(refuse)
	}
	done(nil)
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("Aufrufe: %v", got)
	}
}

const sslR63Alarm = "Quelle ssl-pdh: Der Ordner ist neu dazugekommen, seit dieser Server aktiv wurde – einen eigenen Beleg gibt es dafür noch nicht. Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm"

// Das erste Zertifikat nach einem Tausch (Ordner fehlte bei der Übernahme, kein eigener Beleg): R63 gilt für die
// optionale Quelle unverändert – der Beleg des Partners allein gibt nie --delete. Ein Alarm sagt, was zu tun ist;
// bestätigt der Administrator, läuft alles normal. Vorher (Ordner fehlt) kein Alarm und keine Zeile in der Statusliste.
func TestMirrorSslPdhFirstCertAfterPromotion(t *testing.T) {
	r := newPartnerRig(t)
	f := r.f
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	n, del := sslPdhCalls(f.rs)
	if n != 1 || del {
		t.Fatalf("ssl-pdh: %d Aufrufe, mit --delete %v (erwartet 1, ohne)", n, del)
	}
	if got := f.texts("alarm"); len(got) != 1 || got[0] != sslR63Alarm {
		t.Fatalf("Alarm: %q", got)
	}
	f.w.observeDataID("X")
	if labels, err := f.w.confirm(nil); err != nil || strings.Join(labels, ",") != "ssl-pdh" {
		t.Fatalf("confirm: %v %v", labels, err)
	}
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	if _, del := sslPdhCalls(f.rs); !del {
		t.Fatal("nach der Bestätigung weiter ohne --delete")
	}
}

// (a)/(b) Der Partner hat schon einen Beleg für ssl-pdh (aus einer früheren Rolle, etwa über noteSource) und
// meldet nie „wartet“ (mit Beleg maskiert). Dieser Server wurde ohne Ordner und ohne Zustand wiederhergestellt,
// übernimmt, stellt ein Zertifikat aus: Der Beleg des Partners allein erlaubt kein --delete – R63 unverändert.
func TestMirrorSslPdhFirstCertPartnerAlreadyProvenKeepsR63(t *testing.T) {
	r := newPartnerRig(t)
	f := r.f
	r.proven["ssl-pdh"] = true // alter Beleg, nicht von einem Lauf nach dem Erscheinen
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	for i := 0; i < 3; i++ {
		if err := runAgain(f.w); err != nil {
			t.Fatal(err)
		}
	}
	if n, del := sslPdhCalls(f.rs); n != 3 || del {
		t.Fatalf("%d Aufrufe, mit --delete %v (erwartet 3, nie)", n, del)
	}
	if got := f.texts("alarm"); len(got) != 1 || got[0] != sslR63Alarm {
		t.Fatalf("Alarm: %q", got)
	}
	if ls := f.w.snapshot().PerLabel["ssl-pdh"]; !ls.Unproven {
		t.Fatalf("Stand: %+v", ls)
	}
	f.w.observeDataID("X")
	if labels, err := f.w.confirm(nil); err != nil || strings.Join(labels, ",") != "ssl-pdh" {
		t.Fatalf("confirm: %v %v", labels, err)
	}
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	if _, del := sslPdhCalls(f.rs); !del {
		t.Fatal("nach der Bestätigung weiter ohne --delete")
	}
}
