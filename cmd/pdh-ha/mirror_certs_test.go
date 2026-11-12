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

// Alle Zertifikate gelöscht: der Ordner bleibt leer zurück. Das ist kein Fehler und kein Alarm;
// der Partner behält seinen Stand (nichts wird gelöscht), bis wieder ein Zertifikat da ist.
func TestMirrorSslPdhEmptiedAfterFilesIsQuiet(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.full["/etc/ssl/pdh"] = false
	before, _ := sslPdhCalls(f.rs)
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	if n, _ := sslPdhCalls(f.rs); n != before {
		t.Fatalf("leere Quelle gesendet (%d statt %d Aufrufe)", n, before)
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

// Das erste Zertifikat auf einem Verbund, der schon einmal getauscht hat (Ordner fehlte bei der
// Übernahme): R63 bleibt in Kraft – kein --delete am Partner, ein Alarm, der sagt, was zu tun ist.
// Vorher (Ordner fehlt) kein Alarm und keine Zeile in der Statusliste.
func TestMirrorSslPdhFirstCertAfterPromotion(t *testing.T) {
	f := newMirrorFix(t)
	f.w.d.proof = func(l string) (bool, bool) { return l != "ssl-pdh", true } // die anderen Quellen sind belegt
	f.w.d.partnerProof = func(string) bool { return true }
	f.w.set(true, ha.Passive, 1, mirrorSettings)
	f.w.set(true, ha.Active, 2, mirrorSettings) // Tausch: ssl-pdh fehlt, kein eigener Beleg
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kindTexts(f, "alarm", "warn", "error"); len(got) != 0 {
		t.Fatalf("Meldungen ohne Zertifikate: %v", got)
	}
	if ls := f.w.snapshot().PerLabel["ssl-pdh"]; !mirrorQuietLabel("ssl-pdh", ls) {
		t.Fatalf("Zeile ohne Inhalt sichtbar: %+v", ls)
	}
	// das erste Zertifikat entsteht
	f.files["/etc/ssl/pdh"], f.full["/etc/ssl/pdh"] = true, true
	if err := runAgain(f.w); err != nil {
		t.Fatal(err)
	}
	n, del := sslPdhCalls(f.rs)
	if n != 1 || del {
		t.Fatalf("ssl-pdh: %d Aufrufe, mit --delete %v (erwartet 1, ohne)", n, del)
	}
	want := "Quelle ssl-pdh: Der Ordner ist neu dazugekommen, seit dieser Server aktiv wurde – einen eigenen Beleg gibt es dafür noch nicht. Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm"
	got := f.texts("alarm")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Alarm: %q", got)
	}
	// Bestätigung hebt es auf
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
