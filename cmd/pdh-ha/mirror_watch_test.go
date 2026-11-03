package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"pdh/internal/ha"
)

const watchWait = 15 * time.Second // großzügige Obergrenze; gewartet wird auf Ereignisse

// watchRec sammelt die Aufrufe von emit.
type watchRec struct {
	ch    chan []string
	mu    sync.Mutex
	calls int
	all   map[string]bool
	ack   bool // true = nach dem Aufruf als erfolgreich melden
	mw    *mirrorWatch
}

func newWatchRec() *watchRec { return &watchRec{ch: make(chan []string, 64), all: map[string]bool{}} }

func (r *watchRec) emit(label string, paths []string) {
	r.mu.Lock()
	r.calls++
	for _, p := range paths {
		r.all[label+":"+p] = true
	}
	ack, mw := r.ack, r.mw
	r.mu.Unlock()
	if ack && mw != nil {
		mw.result(label, true)
	}
	r.ch <- append([]string(nil), paths...)
}

func (r *watchRec) seen(label, p string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.all[label+":"+p]
}

// waitFor wartet, bis ein Pfad gemeldet wurde.
func (r *watchRec) waitFor(t *testing.T, label, p string) {
	t.Helper()
	deadline := time.After(watchWait)
	for !r.seen(label, p) {
		select {
		case <-r.ch:
		case <-deadline:
			t.Fatalf("%s:%s nicht gemeldet", label, p)
		}
	}
}

// startWatch startet einen Watch über eine Instant-Quelle "src" und wartet auf
// den ersten Durchlauf.
func startWatch(t *testing.T, root string, ex []string, settle time.Duration, r *watchRec) (*mirrorWatch, context.CancelFunc, func()) {
	t.Helper()
	limited := make(chan struct{}, 4)
	mw := newMirrorWatch([]ha.MirrorSource{{Label: "src", Path: root, Instant: true, Exclude: ex}}, settle, r.emit,
		func() { limited <- struct{}{} })
	r.mw = mw
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mw.run(ctx); close(done) }()
	select {
	case <-mw.initial:
	case <-time.After(watchWait):
		t.Fatal("erster Durchlauf nicht fertig")
	}
	t.Cleanup(func() { cancel(); <-done })
	return mw, cancel, func() {
		select {
		case <-limited:
		case <-time.After(watchWait):
			t.Fatal("limited nicht aufgerufen")
		}
	}
}

func TestMirrorWatchSettles(t *testing.T) {
	root := t.TempDir()
	r := newWatchRec()
	_, _, _ = startWatch(t, root, nil, 300*time.Millisecond, r)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("3"), 0o644) // doppelt
	select {
	case got := <-r.ch:
		sort.Strings(got)
		if strings.Join(got, ",") != "a.txt,b.txt" {
			t.Fatalf("Pfade: %v", got)
		}
	case <-time.After(watchWait):
		t.Fatal("kein Aufruf")
	}
	select {
	case got := <-r.ch:
		t.Fatalf("zweiter Aufruf: %v", got)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestMirrorWatchNewDir(t *testing.T) {
	root := t.TempDir()
	r := newWatchRec()
	mw, _, _ := startWatch(t, root, nil, 50*time.Millisecond, r)
	os.MkdirAll(filepath.Join(root, "x", "y"), 0o755)
	os.WriteFile(filepath.Join(root, "x", "y", "neu.txt"), []byte("n"), 0o644)
	r.waitFor(t, "src", "x/y/neu.txt")
	// auch später im neuen Ordner Angelegtes wird sofort gemeldet
	os.WriteFile(filepath.Join(root, "x", "y", "spaeter.txt"), []byte("s"), 0o644)
	r.waitFor(t, "src", "x/y/spaeter.txt")
	if !mw.hasWatch(filepath.Join(mw.rootOf("src"), "x", "y")) {
		t.Fatal("neuer Ordner wird nicht beobachtet")
	}
}

func TestMirrorWatchExcluded(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "appdata_x", "preview"), 0o755)
	os.MkdirAll(filepath.Join(root, "ha"), 0o755)
	r := newWatchRec()
	mw, _, _ := startWatch(t, root, []string{"--exclude=/appdata_*/preview/", "--exclude=*.part", "--exclude=/nextcloud.log", "--exclude=/ha/"}, 50*time.Millisecond, r)
	for _, d := range []string{filepath.Join(root, "appdata_x", "preview"), filepath.Join(root, "ha")} {
		if mw.hasWatch(filepath.Join(mw.rootOf("src"), mustRel(t, root, d))) {
			t.Fatalf("ausgeschlossener Ordner beobachtet: %s", d)
		}
	}
	os.WriteFile(filepath.Join(root, "appdata_x", "preview", "p.jpg"), []byte("p"), 0o644)
	os.WriteFile(filepath.Join(root, "ha", "key"), []byte("k"), 0o644)
	os.WriteFile(filepath.Join(root, "x.part"), []byte("p"), 0o644)
	os.WriteFile(filepath.Join(root, "nextcloud.log"), []byte("l"), 0o644)
	os.MkdirAll(filepath.Join(root, "d", ".pdh-teil"), 0o755)
	os.WriteFile(filepath.Join(root, "d", ".pdh-teil", "t"), []byte("t"), 0o644)
	os.WriteFile(filepath.Join(root, "d", "y.part"), []byte("p"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644)
	r.waitFor(t, "src", "ok.txt")
	time.Sleep(300 * time.Millisecond) // Nachzügler abwarten
	for k := range r.all {
		if k != "src:ok.txt" && k != "src:d" {
			t.Fatalf("zu Unrecht gemeldet: %s", k)
		}
	}
}

func mustRel(t *testing.T, root, p string) string {
	t.Helper()
	rel, err := filepath.Rel(root, p)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestMirrorWatchExcludeMatcher(t *testing.T) {
	rules := parseWatchExcludes([]string{"--exclude=/ha/", "--exclude=/appdata_*/preview/", "--exclude=*.part",
		"--exclude=/nextcloud.log", "--exclude=/data/dir/"})
	for _, c := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"ha", true, true}, {"ha/key", false, true}, {"ha", false, false}, {"x/ha", true, false},
		{"appdata_1/preview", true, true}, {"appdata_1/preview/a/b.jpg", false, true}, {"appdata_1/files/x", false, false},
		{"a.part", false, true}, {"d/e/a.part", false, true}, {"a.parts", false, false},
		{"nextcloud.log", false, true}, {"sub/nextcloud.log", false, false},
		{"data/dir", true, true}, {"data/dir/f", false, true}, {"data/other", true, false},
	} {
		if got := watchExcluded(rules, c.rel, c.isDir); got != c.want {
			t.Errorf("%q dir=%v: %v, erwartet %v", c.rel, c.isDir, got, c.want)
		}
	}
	for _, p := range []string{".pdh-teil", "a/.pdh-teil/x", ".pdh-papierkorb/2026-11-03/L/f"} {
		if !watchExcluded(nil, p, false) {
			t.Errorf("%q muss immer ausgeschlossen sein", p)
		}
	}
}

func TestMirrorWatchOutside(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "src")
	outside := filepath.Join(base, "aussen")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(filepath.Join(outside, "sub"), 0o755)
	os.WriteFile(filepath.Join(outside, "geheim.txt"), []byte("g"), 0o644)
	r := newWatchRec()
	// Symlink aus der Quelle heraus (unter Windows ohne Recht: überspringen)
	linkOK := os.Symlink(outside, filepath.Join(root, "link")) == nil
	mw, _, _ := startWatch(t, root, nil, 50*time.Millisecond, r)

	// Ereignisse außerhalb und ".."-Pfade werden verworfen
	ctx := context.Background()
	mw.handle(ctx, fsnotify.Event{Name: filepath.Join(outside, "geheim.txt"), Op: fsnotify.Write})
	mw.handle(ctx, fsnotify.Event{Name: filepath.Join(mw.rootOf("src"), "..", "aussen", "geheim.txt"), Op: fsnotify.Write})
	mw.handle(ctx, fsnotify.Event{Name: filepath.Dir(mw.rootOf("src")), Op: fsnotify.Chmod})
	if p := mw.pending(); len(p) != 0 {
		t.Fatalf("außerhalb gemeldet: %v", p)
	}
	if !linkOK {
		t.Log("Symlink nicht möglich: Teil übersprungen")
		return
	}
	for d := range mw.watchedDirs() {
		if strings.HasPrefix(d, outside) || strings.Contains(d, "link") {
			t.Fatalf("Symlink gefolgt: %s", d)
		}
	}
	// Änderungen hinter dem Symlink erscheinen nicht; der Link selbst schon (als Link)
	os.WriteFile(filepath.Join(outside, "sub", "neu.txt"), []byte("n"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644)
	r.waitFor(t, "src", "ok.txt")
	for k := range r.all {
		if strings.Contains(k, "neu.txt") || strings.Contains(k, "geheim") {
			t.Fatalf("hinter dem Symlink gemeldet: %s", k)
		}
	}
}

func TestMirrorWatchLimit(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		os.MkdirAll(filepath.Join(root, d, "x"), 0o755)
	}
	r := newWatchRec()
	limited := make(chan struct{}, 8)
	mw := newMirrorWatch([]ha.MirrorSource{{Label: "src", Path: root, Instant: true}}, 50*time.Millisecond, r.emit,
		func() { limited <- struct{}{} })
	var mu sync.Mutex
	adds := 0
	mw.add = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		adds++
		return syscall.ENOSPC
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mw.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-limited:
	case <-time.After(watchWait):
		t.Fatal("limited nicht aufgerufen")
	}
	select {
	case <-mw.initial:
	case <-time.After(watchWait):
		t.Fatal("Durchlauf hängt nach ENOSPC")
	}
	select {
	case <-limited:
		t.Fatal("limited zweimal aufgerufen")
	case <-time.After(300 * time.Millisecond):
	}
	if !mw.isLimited() {
		t.Fatal("nicht als begrenzt vermerkt")
	}
	if !mw.walkedSince("src").IsZero() {
		t.Fatal("begrenzter Watch darf nicht als abgedeckt gelten")
	}
	mu.Lock()
	defer mu.Unlock()
	if adds != 1 {
		t.Fatalf("Add nach ENOSPC weiter versucht: %d", adds)
	}
}

func TestMirrorWatchPendingCap(t *testing.T) {
	r := newWatchRec()
	mw := newMirrorWatch([]ha.MirrorSource{{Label: "src", Path: t.TempDir(), Instant: true}}, time.Hour, r.emit, func() {})
	mw.maxPend = 3
	mw.mu.Lock()
	mw.walkedAt["src"] = time.Now()
	mw.mu.Unlock()
	mw.record("src", "a")
	mw.record("src", "b")
	mw.record("src", "c")
	mw.record("src", "a") // schon bekannt: kein Überlauf
	if len(mw.pending()["src"]) != 3 {
		t.Fatalf("vor dem Überlauf: %v", mw.pending())
	}
	mw.record("src", "d")
	if len(mw.pending()["src"]) != 0 {
		t.Fatalf("Liste nicht verworfen: %v", mw.pending())
	}
	if !mw.walkedSince("src").IsZero() {
		t.Fatal("Überlauf muss den vollen Abgleich erzwingen")
	}
}

func TestMirrorWatchRetryKeepsPending(t *testing.T) {
	root := t.TempDir()
	r := newWatchRec() // meldet nie Erfolg
	mw, _, _ := startWatch(t, root, nil, 30*time.Millisecond, r)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("1"), 0o644)
	r.waitFor(t, "src", "f.txt")
	if got := mw.pending()["src"]; len(got) != 1 || got[0] != "f.txt" {
		t.Fatalf("pending nach Fehlschlag: %v", got)
	}
	// nach der Wiederholpause wird erneut gemeldet; diesmal mit Erfolg
	r.mu.Lock()
	r.ack = true
	r.mu.Unlock()
	select {
	case got := <-r.ch:
		if len(got) != 1 || got[0] != "f.txt" {
			t.Fatalf("Wiederholung: %v", got)
		}
	case <-time.After(watchWait):
		t.Fatal("keine Wiederholung")
	}
	deadline := time.Now().Add(watchWait)
	for len(mw.pending()["src"]) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending nach Erfolg nicht leer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Arbeiter ---

func TestMirrorRunPathsNames(t *testing.T) {
	f := newMirrorFix(t)
	f.w.d.lexists = func(p string) bool { return !strings.HasSuffix(p, "weg.txt") }
	f.w.set(true, ha.Active, 3, mirrorSettings)
	names := []string{"a b.txt", "ä.pdf", "zeile\num.txt", "-rf", "weg.txt", "../aussen", "/abs", "x/../y"}
	if err := f.w.runPaths(context.Background(), "uploads", names); err != nil {
		t.Fatal(err)
	}
	if f.rs.count() != 1 {
		t.Fatalf("Aufrufe: %d", f.rs.count())
	}
	argv, stdin := f.rs.calls[0], f.rs.stdins[0]
	want := "a b.txt\x00ä.pdf\x00zeile\num.txt\x00-rf\x00"
	if string(stdin) != want {
		t.Fatalf("stdin = %q, erwartet %q", stdin, want)
	}
	joined := " " + strings.Join(argv, " ") + " "
	for _, must := range []string{" --files-from=- ", " --from0 ", " -a ", " --numeric-ids ", " --backup ", " --partial-dir=.pdh-teil ", " --delay-updates ",
		" -e pdh-ha mirror-pipe --label uploads --term 3 --node b ", " /r/uploads/ b:uploads "} {
		if !strings.Contains(joined, must) {
			t.Fatalf("fehlt %q in %v", must, argv)
		}
	}
	for _, a := range argv {
		if a == "--delete" || a == "--copy-links" || a == "-L" || strings.HasPrefix(a, "--delete") {
			t.Fatalf("unerwartet %q", a)
		}
	}
	// nur verschwundene Pfade: kein Aufruf
	f.w.d.lexists = func(string) bool { return false }
	if err := f.w.runPaths(context.Background(), "uploads", []string{"x"}); err != nil || f.rs.count() != 1 {
		t.Fatalf("err %v, Aufrufe %d", err, f.rs.count())
	}
}

func TestMirrorRunPathsVanishedIsOK(t *testing.T) {
	f := newMirrorFix(t)
	f.w.d.lexists = func(string) bool { return true }
	f.rs.fn = func(context.Context, []string) error { return exitErr(t, rsyncVanished) }
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runPaths(context.Background(), "uploads", []string{"a"}); err != nil {
		t.Fatalf("Code 24 muss als Erfolg zählen: %v", err)
	}
}

func TestMirrorRunPathsNotActive(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Passive, 1, mirrorSettings)
	if err := f.w.runPaths(context.Background(), "uploads", []string{"a"}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if f.rs.count() != 0 {
		t.Fatal("rsync lief auf einem passiven Server")
	}
}

// wFix: Arbeiter mit echtem Wurzelordner, damit der Watch etwas beobachten kann.
func newWatchFix(t *testing.T) (*mirrorFix, string) {
	t.Helper()
	f := newMirrorFix(t)
	repo := t.TempDir()
	for _, d := range []string{"uploads", "chat_files"} {
		os.MkdirAll(filepath.Join(repo, d), 0o755)
		os.WriteFile(filepath.Join(repo, d, "start.txt"), []byte("s"), 0o644)
	}
	f.w.d.repo = repo
	f.w.d.settle = 30 * time.Millisecond
	f.w.d.exists = func(p string) bool {
		if p == "/etc/pdh" {
			return true
		}
		_, err := os.Stat(p)
		return err == nil
	}
	f.w.d.hasFiles = func(string) bool { return true }
	f.w.d.lexists = func(p string) bool { _, err := os.Lstat(p); return err == nil }
	return f, repo
}

// watchOf wartet, bis der Arbeiter einen Watch gestartet und durchlaufen hat.
func watchOf(t *testing.T, w *mirrorWorker) *mirrorWatch {
	t.Helper()
	deadline := time.Now().Add(watchWait)
	for {
		w.mu.Lock()
		mw := w.watch
		w.mu.Unlock()
		if mw != nil {
			select {
			case <-mw.initial:
				return mw
			case <-time.After(time.Until(deadline)):
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Watch nicht bereit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func filesFromCalls(f *mirrorFix) (paths [][]string, full []string) {
	f.rs.mu.Lock()
	defer f.rs.mu.Unlock()
	for i, c := range f.rs.calls {
		if strings.Contains(strings.Join(c, " "), "--files-from=-") {
			paths = append(paths, append([]string{c[len(c)-1]}, strings.Split(strings.TrimSuffix(string(f.rs.stdins[i]), "\x00"), "\x00")...))
		} else {
			full = append(full, c[len(c)-1])
		}
	}
	return
}

func TestMirrorWatchWorkerFlow(t *testing.T) {
	f, repo := newWatchFix(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.step(ctx) // startet den Watch und gleicht einmal voll ab
	mw := watchOf(t, f.w)
	// einen weiteren vollen Lauf nach dem Durchlauf des Watch erzwingen
	f.advance(2 * time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}

	// 1. Änderung wird sofort (ohne --delete) übertragen
	os.WriteFile(filepath.Join(repo, "uploads", "neu.txt"), []byte("n"), 0o644)
	deadline := time.Now().Add(watchWait)
	for {
		p, _ := filesFromCalls(f)
		if len(p) > 0 {
			if p[0][0] != "b:uploads" || p[0][1] != "neu.txt" {
				t.Fatalf("Sofort-Aufruf: %v", p)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keine Sofort-Übertragung")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Erfolg quittiert, pending leer
	for len(mw.pending()["uploads"]) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending bleibt")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 2. Fehlschlag: Pfad bleibt in pending, finalSync überträgt ihn zuerst und gleicht Instant-Quellen nicht voll ab
	f.rs.mu.Lock()
	f.rs.fn = func(_ context.Context, argv []string) error {
		if strings.Contains(strings.Join(argv, " "), "--files-from=-") {
			return context.DeadlineExceeded
		}
		return nil
	}
	f.rs.mu.Unlock()
	os.WriteFile(filepath.Join(repo, "chat_files", "offen.txt"), []byte("o"), 0o644)
	for len(mw.pending()["chat_files"]) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Pfad nicht vorgemerkt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// warten, bis der Fehlversuch geschehen ist, dann Erfolg zulassen
	for {
		_, _ = filesFromCalls(f)
		f.rs.mu.Lock()
		n := len(f.rs.calls)
		f.rs.mu.Unlock()
		p, _ := filesFromCalls(f)
		if len(p) >= 2 || (n > 0 && len(p) >= 1 && len(mw.pending()["chat_files"]) > 0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kein Fehlversuch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.rs.mu.Lock()
	f.rs.fn = nil
	before := len(f.rs.calls)
	f.rs.mu.Unlock()
	if err := f.w.finalSync(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	f.rs.mu.Lock()
	after := append([][]string(nil), f.rs.calls[before:]...)
	stdins := append([][]byte(nil), f.rs.stdins[before:]...)
	f.rs.mu.Unlock()
	if len(after) == 0 {
		t.Fatal("finalSync hat nichts übertragen")
	}
	first := strings.Join(after[0], " ")
	if !strings.Contains(first, "--files-from=-") || !strings.HasSuffix(first, "b:chat_files") || !strings.Contains(string(stdins[0]), "offen.txt") {
		t.Fatalf("pending muss zuerst gehen: %s / %q", first, stdins[0])
	}
	for _, c := range after[1:] {
		j := strings.Join(c, " ")
		if strings.HasSuffix(j, "b:uploads") || strings.HasSuffix(j, "b:chat_files") {
			t.Fatalf("Instant-Quelle voll abgeglichen trotz bereitem Watch: %s", j)
		}
	}
	if len(mw.pending()["chat_files"]) != 0 {
		t.Fatal("pending nach finalSync nicht quittiert")
	}
}

func TestMirrorFinalSyncFullWhenWatchNotReady(t *testing.T) {
	f, _ := newWatchFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	// kein Watch gestartet (step nie aufgerufen): alles voll
	if err := f.w.finalSync(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_, full := filesFromCalls(f)
	got := strings.Join(full, ",")
	for _, l := range []string{"b:uploads", "b:chat_files", "b:etc-pdh"} {
		if !strings.Contains(got, l) {
			t.Fatalf("%s fehlt: %s", l, got)
		}
	}
}

func TestMirrorWatchLimitedKeepsFullRuns(t *testing.T) {
	f, _ := newWatchFix(t)
	f.w.d.watchAdd = func(string) error { return syscall.ENOSPC }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.step(ctx)
	watchOf(t, f.w)
	deadline := time.Now().Add(watchWait)
	for !f.w.snapshot().WatchLimited {
		if time.Now().After(deadline) {
			t.Fatal("WatchLimited nicht gesetzt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(f.texts("alarm")); n != 1 {
		t.Fatalf("Alarme: %v", f.texts("alarm"))
	}
	// der Abgleich läuft weiter, auch in finalSync für Instant-Quellen voll
	f.advance(2 * time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	f.rs.mu.Lock()
	before := len(f.rs.calls)
	f.rs.mu.Unlock()
	if err := f.w.finalSync(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	f.rs.mu.Lock()
	after := f.rs.calls[before:]
	f.rs.mu.Unlock()
	var seen []string
	for _, c := range after {
		seen = append(seen, c[len(c)-1])
	}
	if !strings.Contains(strings.Join(seen, ","), "b:uploads") {
		t.Fatalf("begrenzter Watch: Instant-Quelle muss voll abgeglichen werden: %v", seen)
	}
}

func TestMirrorWatchStopsWhenPassive(t *testing.T) {
	f, _ := newWatchFix(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.step(ctx)
	watchOf(t, f.w)
	f.w.set(true, ha.Passive, 2, mirrorSettings)
	f.w.step(ctx)
	f.w.mu.Lock()
	mw := f.w.watch
	f.w.mu.Unlock()
	if mw != nil {
		t.Fatal("Watch läuft auf passivem Server weiter")
	}
}

// exitErr liefert einen echten *exec.ExitError mit dem Code.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/c", "exit", strconv.Itoa(code))
	} else {
		c = exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	}
	err := c.Run()
	if err == nil {
		t.Fatal("Prozess endete ohne Fehler")
	}
	return fmt.Errorf("rsync: %w", err)
}
