package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

type inboxFix struct {
	in    *mirrorInbox
	ev    *eventLog
	rs    *fakeRsync
	root  string // mit Schrägstrichen (ha.MirrorTrashDir nutzt path)
	view  mirrorInboxView
	mu    sync.Mutex
	now   time.Time
	srcs  []ha.MirrorSource
	state string
}

func newInboxFix(t *testing.T) *inboxFix {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	f := &inboxFix{
		ev: testEvents(t), rs: &fakeRsync{}, root: root,
		now:   time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local),
		state: filepath.Join(root, "state", mirrorInboxFile),
	}
	s := ha.DefaultSettings()
	s.MirrorOn = true
	f.view = mirrorInboxView{Role: ha.Passive, Term: 3, Settings: s}
	f.srcs = []ha.MirrorSource{
		{Label: "uploads", Path: root + "/repo/uploads"},
		{Label: "nginx", Path: root + "/etc/nginx"},
		{Label: "ssl-pdh-nextcloud", Path: root + "/etc/ssl"},
		{Label: "nextcloud-data", Path: root + "/nc/data"},
	}
	for _, d := range []string{"repo", "etc", "nc"} {
		os.MkdirAll(root+"/"+d, 0o755)
	}
	f.rebuild()
	return f
}

func (f *inboxFix) rebuild() {
	f.in = newMirrorInbox(mirrorInboxDeps{
		ev: f.ev, repo: f.root + "/repo", run: f.rs, statePath: f.state,
		view:    func() mirrorInboxView { f.mu.Lock(); defer f.mu.Unlock(); return f.view },
		now:     func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		sources: func(ha.Settings) []ha.MirrorSource { return f.srcs },
	})
}

func (f *inboxFix) setView(fn func(v *mirrorInboxView)) {
	f.mu.Lock()
	fn(&f.view)
	f.mu.Unlock()
}

func (f *inboxFix) accept(label string, term uint64) ([]string, func(error), string) {
	return f.in.Accept(context.Background(), "a", "node", mirrorRequest{
		Label: label, Term: term, Argv: []string{"rsync", "--server", "-a", ".", "x"},
	})
}

func (f *inboxFix) acceptArgv(label string, extra ...string) ([]string, func(error), string) {
	argv := append([]string{"rsync", "--server", "-a"}, extra...)
	return f.in.Accept(context.Background(), "a", "node", mirrorRequest{Label: label, Term: 3, Argv: append(argv, ".", "x")})
}

func (f *inboxFix) acceptDel(label string, term uint64) ([]string, func(error), string) {
	return f.acceptArgv(label, "--delete")
}

func (f *inboxFix) events() string {
	var sb strings.Builder
	for _, e := range f.ev.since(0, 100) {
		sb.WriteString(e.Kind + ": " + e.Text + "\n")
	}
	return sb.String()
}

func TestInboxAccept(t *testing.T) {
	f := newInboxFix(t)
	target := f.root + "/repo/uploads"
	os.Mkdir(target, 0o755)
	argv, done, refuse := f.accept("uploads", 3)
	if refuse != "" || done == nil {
		t.Fatalf("abgelehnt: %q", refuse)
	}
	trash := f.root + "/repo/.pdh-papierkorb/2026-11-03/uploads"
	want := []string{"rsync", "--server", "-a", "--backup-dir=" + trash, ".", target}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %v, erwartet %v", argv, want)
	}
	fi, err := os.Stat(trash)
	if err != nil || !fi.IsDir() {
		t.Fatalf("Papierkorb fehlt: %v", err)
	}
	if p, _ := os.Stat(f.root + "/repo/.pdh-papierkorb"); p.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("Papierkorb-Elternordner ist für andere lesbar: %v", p.Mode())
	}
	done(nil)
}

func TestInboxOneStreamPerLabel(t *testing.T) {
	f := newInboxFix(t)
	os.Mkdir(f.root+"/repo/uploads", 0o755)
	_, done, refuse := f.accept("uploads", 3)
	if refuse != "" {
		t.Fatal(refuse)
	}
	if _, d2, r := f.accept("uploads", 3); r != "Quelle uploads wird schon übertragen" || d2 != nil {
		t.Fatalf("zweiter Strom: %q", r)
	}
	done(nil)
	if _, _, r := f.accept("uploads", 3); r != "" {
		t.Fatalf("nach done muss frei sein: %q", r)
	}
}

func TestInboxRefuseOldTerm(t *testing.T) {
	f := newInboxFix(t)
	os.Mkdir(f.root+"/repo/uploads", 0o755)
	_, _, r := f.accept("uploads", 2)
	if r != "Absender hat eine alte Amtszeit" {
		t.Fatalf("Ablehnung: %q", r)
	}
	if ev := f.events(); !strings.Contains(ev, "Dateien von a abgelehnt: Absender hat eine alte Amtszeit") {
		t.Fatalf("Ereignis fehlt:\n%s", ev)
	}
	// Strom wurde nicht belegt
	if _, _, r := f.accept("uploads", 3); r != "" {
		t.Fatalf("danach: %q", r)
	}
}

func TestInboxMissingTarget(t *testing.T) {
	f := newInboxFix(t)
	// Elternordner da: Ziel wird angelegt
	if _, _, r := f.accept("uploads", 3); r != "" {
		t.Fatalf("uploads: %q", r)
	}
	if fi, err := os.Stat(f.root + "/repo/uploads"); err != nil || !fi.IsDir() {
		t.Fatalf("Ziel nicht angelegt: %v", err)
	}
}

func TestInboxMissingParent(t *testing.T) {
	f := newInboxFix(t)
	f.srcs = []ha.MirrorSource{{Label: "uploads", Path: f.root + "/gibtsnicht/uploads"}}
	if _, _, r := f.accept("uploads", 3); r != "Zielordner fehlt" {
		t.Fatalf("Ablehnung: %q", r)
	}
	if _, err := os.Stat(f.root + "/gibtsnicht"); err == nil {
		t.Fatal("Elternordner darf nicht angelegt werden")
	}
}

func TestInboxSymlinks(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Links nur unter Unix")
	}
	f := newInboxFix(t)
	out := f.root + "/draussen"
	os.Mkdir(out, 0o755)
	os.Symlink(out, f.root+"/repo/uploads")
	if _, _, r := f.accept("uploads", 3); r != "Zielordner ist ein Link" {
		t.Fatalf("Ziel als Link: %q", r)
	}
	// Papierkorb-Elternordner als Link
	os.Remove(f.root + "/repo/uploads")
	os.Mkdir(f.root+"/repo/uploads", 0o755)
	os.Symlink(out, f.root+"/repo/.pdh-papierkorb")
	if _, _, r := f.accept("uploads", 3); r != "Papierkorb nicht anlegbar" {
		t.Fatalf("Papierkorb als Link: %q", r)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("durch den Link wurde etwas angelegt: %v", ents)
	}
}

func TestInboxDataNotMounted(t *testing.T) {
	f := newInboxFix(t)
	data := f.root + "/nc/data"
	os.Mkdir(data, 0o755)
	// vor dem Erstabgleich ist ein leeres Ziel erlaubt
	if _, d, r := f.accept("nextcloud-data", 3); r != "" {
		t.Fatalf("vor dem Erstabgleich: %q", r)
	} else {
		d(nil)
	}
	f.setView(func(v *mirrorInboxView) { v.FirstDone = true })
	if _, _, r := f.accept("nextcloud-data", 3); r != "Datenordner nicht erreichbar" {
		t.Fatalf("ohne .ocdata: %q", r)
	}
	if ev := f.events(); !strings.Contains(ev, "alarm: ") || !strings.Contains(ev, ".ocdata") {
		t.Fatalf("Alarm fehlt:\n%s", ev)
	}
	os.WriteFile(data+"/.ocdata", nil, 0o644)
	if _, d, r := f.accept("nextcloud-data", 3); r != "" {
		t.Fatalf("mit .ocdata: %q", r)
	} else {
		d(nil)
	}
	// fehlt das ganze Ziel nach dem Erstabgleich, wird es nicht neu angelegt
	os.RemoveAll(data)
	if _, _, r := f.accept("nextcloud-data", 3); r != "Datenordner nicht erreichbar" {
		t.Fatalf("Ziel weg: %q", r)
	}
	if _, err := os.Stat(data); err == nil {
		t.Fatal("Datenordner darf nicht angelegt werden")
	}
}

func (f *inboxFix) nginxRun(t *testing.T, change bool, fail string) {
	t.Helper()
	dir := f.root + "/etc/nginx"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/nginx.conf", []byte("alt"), 0o644)
	f.rs.mu.Lock()
	f.rs.calls = nil
	f.rs.fn = func(_ context.Context, argv []string) error {
		if strings.Join(argv, " ") == fail {
			return errors.New("kaputt")
		}
		return nil
	}
	f.rs.mu.Unlock()
	_, done, r := f.accept("nginx", 3)
	if r != "" {
		t.Fatal(r)
	}
	if change {
		os.WriteFile(dir+"/nginx.conf", []byte("neuer Inhalt"), 0o644)
	}
	done(nil)
}

func (f *inboxFix) calls() []string {
	f.rs.mu.Lock()
	defer f.rs.mu.Unlock()
	var out []string
	for _, c := range f.rs.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func TestInboxNginxReload(t *testing.T) {
	f := newInboxFix(t)
	f.nginxRun(t, true, "")
	if got := strings.Join(f.calls(), ";"); got != "nginx -t;systemctl reload nginx" {
		t.Fatalf("Aufrufe: %q", got)
	}

	f = newInboxFix(t)
	f.nginxRun(t, false, "")
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("ohne Änderung nichts: %v", got)
	}

	f = newInboxFix(t)
	f.nginxRun(t, true, "nginx -t")
	if got := strings.Join(f.calls(), ";"); got != "nginx -t" {
		t.Fatalf("bei Fehler von nginx -t kein Neuladen: %q", got)
	}
	if ev := f.events(); !strings.Contains(ev, "alarm: ") || !strings.Contains(ev, "nicht neu geladen") {
		t.Fatalf("Alarm fehlt:\n%s", ev)
	}

	// andere Quellen laden nginx nie neu
	f = newInboxFix(t)
	_, done, _ := f.accept("uploads", 3)
	os.WriteFile(f.root+"/repo/uploads/x", []byte("x"), 0o644)
	done(nil)
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("uploads: %v", got)
	}
}

func TestInboxDoneErrorLoggedOncePerMinute(t *testing.T) {
	f := newInboxFix(t)
	for i := 0; i < 3; i++ {
		_, done, r := f.accept("uploads", 3)
		if r != "" {
			t.Fatal(r)
		}
		done(errors.New("rsync endete mit Fehler: exit status 23: kaputt"))
	}
	if n := strings.Count(f.events(), "nicht vollständig empfangen"); n != 1 {
		t.Fatalf("Ereignisse = %d\n%s", n, f.events())
	}
	f.mu.Lock()
	f.now = f.now.Add(61 * time.Second)
	f.mu.Unlock()
	_, done, _ := f.accept("uploads", 3)
	done(errors.New("noch einmal"))
	if n := strings.Count(f.events(), "nicht vollständig empfangen"); n != 2 {
		t.Fatalf("nach einer Minute: %d", n)
	}
}

func mkTrashDay(t *testing.T, parent, name string) string {
	t.Helper()
	d := parent + "/.pdh-papierkorb/" + name + "/uploads"
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d+"/datei", []byte("12345"), 0o644)
	return parent + "/.pdh-papierkorb/" + name
}

func TestPruneTrash(t *testing.T) {
	f := newInboxFix(t)
	parent := f.root + "/repo"
	old := mkTrashDay(t, parent, "2026-10-19")  // 15 Tage
	edge := mkTrashDay(t, parent, "2026-10-20") // genau 14 Tage
	young := mkTrashDay(t, parent, "2026-11-02")
	other := parent + "/.pdh-papierkorb/notizen"
	os.MkdirAll(other, 0o700)
	if err := f.in.pruneTrash(f.now, 14); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("15 Tage alt muss weg sein")
	}
	for _, p := range []string{edge, young, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s muss bleiben: %v", p, err)
		}
	}
	// keepDays <= 0: Standard 14
	old2 := mkTrashDay(t, parent, "2026-10-01")
	f.in.pruneTrash(f.now, 0)
	if _, err := os.Stat(old2); err == nil {
		t.Fatal("Standard 14 Tage nicht angewendet")
	}
	if _, err := os.Stat(edge); err != nil {
		t.Fatal("Standard darf 14 Tage alt nicht löschen")
	}
}

func TestTrashInfoAndEmpty(t *testing.T) {
	f := newInboxFix(t)
	a := mkTrashDay(t, f.root+"/repo", "2026-11-02")
	b := mkTrashDay(t, f.root+"/etc", "2026-11-03")
	fremd := f.root + "/nc/.pdh-papierkorb/andere"
	os.MkdirAll(fremd, 0o700)
	os.WriteFile(fremd+"/wichtig", []byte("x"), 0o644)
	// ein Ordner, der nicht zu den Quellen gehört
	os.MkdirAll(f.root+"/sonst/.pdh-papierkorb/2026-11-01", 0o700)
	os.WriteFile(f.root+"/sonst/.pdh-papierkorb/2026-11-01/x", []byte("x"), 0o644)

	if n, c := f.in.trashInfo(); n != 10 || c != 2 {
		t.Fatalf("trashInfo = %d, %d", n, c)
	}
	f.in.st.Orphans = 4
	if err := f.in.emptyTrash(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s muss weg sein", p)
		}
	}
	for _, p := range []string{fremd + "/wichtig", f.root + "/sonst/.pdh-papierkorb/2026-11-01/x"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s muss bleiben: %v", p, err)
		}
	}
	if f.in.orphans() != 0 {
		t.Fatal("Zähler nicht zurückgesetzt")
	}
}

func TestEmptyTrashKeepsLinkTargets(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Links nur unter Unix")
	}
	f := newInboxFix(t)
	out := f.root + "/draussen"
	os.MkdirAll(out, 0o755)
	os.WriteFile(out+"/bleibt", []byte("x"), 0o644)
	day := mkTrashDay(t, f.root+"/repo", "2026-11-02")
	os.Symlink(out, day+"/link")
	os.Symlink(out, f.root+"/repo/.pdh-papierkorb/2026-11-01")
	if _, c := f.in.trashInfo(); c != 2 { // datei + link, nichts aus draussen
		t.Fatalf("Dateien = %d", c)
	}
	f.in.emptyTrash(context.Background())
	if _, err := os.Stat(out + "/bleibt"); err != nil {
		t.Fatalf("Linkziel wurde gelöscht: %v", err)
	}
}

func TestOrphans(t *testing.T) {
	f := newInboxFix(t)
	target := f.root + "/repo/uploads"
	os.Mkdir(target, 0o755)
	trash := f.root + "/repo/.pdh-papierkorb/2026-11-03/uploads"

	run := func(add int) {
		t.Helper()
		_, done, r := f.acceptDel("uploads", 3)
		if r != "" {
			t.Fatal(r)
		}
		for i := 0; i < add; i++ {
			os.WriteFile(filepath.Join(trash, "r"+time.Now().Format("150405.000000000")+string(rune('a'+i))), []byte("x"), 0o600)
		}
		done(nil)
	}

	// ohne Wechsel zählt nichts
	run(1)
	if f.in.orphans() != 0 {
		t.Fatalf("ohne Wechsel: %d", f.in.orphans())
	}
	// Wechsel aktiv -> passiv: erster Lauf zählt
	f.setView(func(v *mirrorInboxView) { v.ReturnedAt = f.now })
	run(2)
	if f.in.orphans() != 2 {
		t.Fatalf("erster Lauf: %d", f.in.orphans())
	}
	// spätere Läufe nicht mehr
	run(3)
	if f.in.orphans() != 2 {
		t.Fatalf("späterer Lauf: %d", f.in.orphans())
	}
	// der Zähler überlebt einen Neustart, ebenso der Vermerk „schon verarbeitet“
	f.rebuild()
	if f.in.orphans() != 2 {
		t.Fatalf("nach Neustart: %d", f.in.orphans())
	}
	run(1)
	if f.in.orphans() != 2 {
		t.Fatalf("nach Neustart gezählt: %d", f.in.orphans())
	}
	// ein neuer Wechsel zählt wieder, der Zähler wächst
	f.setView(func(v *mirrorInboxView) { v.ReturnedAt = f.now.Add(time.Hour) })
	run(4)
	if f.in.orphans() != 6 {
		t.Fatalf("zweiter Wechsel: %d", f.in.orphans())
	}
	if err := f.in.emptyTrash(context.Background()); err != nil || f.in.orphans() != 0 {
		t.Fatalf("Leeren: %v %d", err, f.in.orphans())
	}
}

func TestOrphansFailedRunKeepsPending(t *testing.T) {
	f := newInboxFix(t)
	os.Mkdir(f.root+"/repo/uploads", 0o755)
	f.setView(func(v *mirrorInboxView) { v.ReturnedAt = f.now })
	trash := f.root + "/repo/.pdh-papierkorb/2026-11-03/uploads"
	_, done, _ := f.acceptDel("uploads", 3)
	os.WriteFile(trash+"/x", []byte("x"), 0o600)
	done(errors.New("abgebrochen"))
	if f.in.orphans() != 1 {
		t.Fatalf("Zuwachs eines fehlgeschlagenen Laufs geht nicht verloren: %d", f.in.orphans())
	}
	_, done, _ = f.acceptDel("uploads", 3)
	os.WriteFile(trash+"/y", []byte("y"), 0o600)
	done(nil)
	if f.in.orphans() != 2 { // x vom Abbruch, y vom zweiten Lauf
		t.Fatalf("Zähler = %d", f.in.orphans())
	}
}

func TestInboxCorruptState(t *testing.T) {
	f := newInboxFix(t)
	os.MkdirAll(filepath.Dir(f.state), 0o700)
	os.WriteFile(f.state, []byte("{kaputt"), 0o600)
	f.rebuild()
	if f.in.orphans() != 0 {
		t.Fatal("beschädigter Zustand gilt als leer")
	}
	if n := strings.Count(f.events(), "alarm: "); n != 1 {
		t.Fatalf("Alarme = %d\n%s", n, f.events())
	}
}

// Ein Sofort-Strom (--files-from, ohne --delete) verbraucht die Vormerkung nicht.
func TestOrphansPathsStreamKeepsPending(t *testing.T) {
	f := newInboxFix(t)
	os.Mkdir(f.root+"/repo/uploads", 0o755)
	f.setView(func(v *mirrorInboxView) { v.ReturnedAt = f.now })
	trash := f.root + "/repo/.pdh-papierkorb/2026-11-03/uploads"
	_, done, r := f.acceptArgv("uploads", "--files-from=-", "--from0")
	if r != "" {
		t.Fatal(r)
	}
	os.WriteFile(trash+"/p", []byte("x"), 0o600)
	done(nil)
	if f.in.orphans() != 0 {
		t.Fatalf("Strom ohne --delete zählt nicht: %d", f.in.orphans())
	}
	_, done, _ = f.acceptDel("uploads", 3)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		os.WriteFile(trash+"/"+n, []byte("x"), 0o600)
	}
	done(nil)
	if f.in.orphans() != 5 {
		t.Fatalf("löschender Lauf: %d", f.in.orphans())
	}
}

func TestInboxNginxFailedStreamRetried(t *testing.T) {
	f := newInboxFix(t)
	dir := f.root + "/etc/nginx"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/nginx.conf", []byte("alt"), 0o644)
	_, done, _ := f.accept("nginx", 3)
	os.WriteFile(dir+"/nginx.conf", []byte("neuer Inhalt"), 0o644)
	done(errors.New("rsync endete mit Fehler"))
	if len(f.calls()) != 0 {
		t.Fatalf("nach Abbruch kein Neuladen: %v", f.calls())
	}
	f.rebuild() // der Merker überlebt einen Neustart
	_, done, _ = f.accept("nginx", 3)
	done(nil)
	if got := strings.Join(f.calls(), ";"); got != "nginx -t;systemctl reload nginx" {
		t.Fatalf("Folgelauf ohne Änderung muss nachholen: %q", got)
	}
	_, done, _ = f.accept("nginx", 3)
	done(nil)
	if len(f.calls()) != 2 {
		t.Fatalf("danach nicht erneut: %v", f.calls())
	}
}

func TestInboxNginxTestFailRetriedAfterCerts(t *testing.T) {
	f := newInboxFix(t)
	f.nginxRun(t, true, "nginx -t") // Zertifikate fehlen noch
	f.rs.mu.Lock()
	f.rs.calls, f.rs.fn = nil, nil
	f.rs.mu.Unlock()
	os.MkdirAll(f.root+"/etc/ssl", 0o755)
	_, done, _ := f.accept("ssl-pdh-nextcloud", 3) // Zertifikate sind da
	done(nil)
	if got := strings.Join(f.calls(), ";"); got != "nginx -t;systemctl reload nginx" {
		t.Fatalf("Zertifikate-Lauf muss nginx neu laden: %q", got)
	}
	_, done, _ = f.accept("ssl-pdh-nextcloud", 3)
	done(nil)
	if len(f.calls()) != 2 {
		t.Fatalf("ohne Merker kein Neuladen: %v", f.calls())
	}
}

func TestInboxNginxReloadFailRetried(t *testing.T) {
	f := newInboxFix(t)
	f.nginxRun(t, true, "systemctl reload nginx")
	f.rs.mu.Lock()
	f.rs.calls, f.rs.fn = nil, nil
	f.rs.mu.Unlock()
	_, done, _ := f.accept("nginx", 3)
	done(nil)
	if got := strings.Join(f.calls(), ";"); got != "nginx -t;systemctl reload nginx" {
		t.Fatalf("Wiederholung: %q", got)
	}
}

// Alarme: höchstens einer je Stunde, auch wenn andere Meldungen häufiger kommen.
func TestInboxAlarmOncePerHour(t *testing.T) {
	f := newInboxFix(t)
	os.MkdirAll(f.root+"/nc/data", 0o755)
	f.setView(func(v *mirrorInboxView) { v.FirstDone = true })
	for i := 0; i < 30; i++ {
		f.accept("nextcloud-data", 3)
		f.mu.Lock()
		f.now = f.now.Add(61 * time.Second)
		f.mu.Unlock()
	}
	if n := strings.Count(f.events(), "alarm: "); n != 1 {
		t.Fatalf("Alarme = %d", n)
	}
}
