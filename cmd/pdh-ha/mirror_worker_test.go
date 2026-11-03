package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// fakeRsync zeichnet die rsync-Aufrufe auf; fn darf blockieren oder scheitern.
type fakeRsync struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(ctx context.Context, argv []string) error
}

func (f *fakeRsync) Run(ctx context.Context, argv, _ []string, _ []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return nil, fn(ctx, argv)
	}
	return nil, nil
}

func (f *fakeRsync) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type mirrorFix struct {
	w     *mirrorWorker
	rs    *fakeRsync
	ev    *eventLog
	clock *time.Time
	files map[string]bool // Pfad -> vorhanden
	full  map[string]bool // Pfad -> hat Dateien
	free  int64
	freeK bool
	mu    sync.Mutex
}

func newMirrorFix(t *testing.T) *mirrorFix {
	t.Helper()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := &mirrorFix{
		rs: &fakeRsync{}, clock: &now, freeK: false,
		files: map[string]bool{"/r/uploads": true, "/r/chat_files": true, "/etc/pdh": true},
		full:  map[string]bool{"/r/uploads": true, "/r/chat_files": true, "/etc/pdh": true},
	}
	dir := t.TempDir()
	f.ev = openEventLog(filepath.Join(dir, "events.jsonl"), 1<<20, 2)
	f.w = newMirrorWorker(mirrorDeps{
		ev: f.ev, repo: "/r", node: "b", exe: "pdh-ha", rsync: f.rs,
		free:      func() (int64, bool) { return f.free, f.freeK },
		statePath: filepath.Join(dir, "mirror.json"),
		now:       func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return *f.clock },
		exists:    func(p string) bool { return f.files[p] },
		hasFiles:  func(p string) bool { return f.full[p] },
		size:      func(string) int64 { return 100 },
	})
	return f
}

func (f *mirrorFix) advance(d time.Duration) {
	f.mu.Lock()
	*f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *mirrorFix) texts(kind string) []string {
	var out []string
	for _, e := range f.ev.readAll() {
		if e.Kind == kind {
			out = append(out, e.Text)
		}
	}
	return out
}

var mirrorSettings = func() ha.Settings {
	s := ha.DefaultSettings()
	s.MirrorEvery = time.Minute
	s.MirrorAlarmAfter = 5 * time.Minute
	return s
}()

func TestMirrorRsyncArgv(t *testing.T) {
	f := newMirrorFix(t)
	s := mirrorSettings
	f.w.set(true, ha.Active, 7, s)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.rs.calls {
		if strings.HasSuffix(c[len(c)-1], ":etc-pdh") {
			got = c
		}
	}
	want := `rsync -a --numeric-ids --delete --backup --partial-dir=.pdh-teil --delay-updates --exclude=/ha/ ` +
		`-e pdh-ha mirror-pipe --label etc-pdh --term 7 --node b /etc/pdh/ b:etc-pdh`
	if strings.Join(got, " ") != want {
		t.Fatalf("argv:\n got %q\nwant %q", strings.Join(got, " "), want)
	}
	for _, c := range f.rs.calls {
		for _, a := range c {
			if strings.HasPrefix(a, "--bwlimit") || a == "--copy-links" {
				t.Fatalf("unerwartetes Argument %q", a)
			}
		}
	}
	// -e bleibt ein einziges Argument
	if got[len(got)-3] != "pdh-ha mirror-pipe --label etc-pdh --term 7 --node b" {
		t.Fatalf("-e: %q", got[len(got)-3])
	}

	f2 := newMirrorFix(t)
	s.MirrorBWLimitKB = 500
	f2.w.set(true, ha.Active, 1, s)
	f2.w.runAll(context.Background())
	if !strings.Contains(strings.Join(f2.rs.calls[0], " "), "--bwlimit=500") {
		t.Fatalf("bwlimit fehlt: %v", f2.rs.calls[0])
	}
}

func TestMirrorOnlyActive(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Passive, 1, mirrorSettings)
	f.w.runAll(context.Background())
	f.w.set(false, ha.Active, 1, mirrorSettings)
	f.w.runAll(context.Background())
	if f.rs.count() != 0 {
		t.Fatalf("rsync lief: %v", f.rs.calls)
	}

	// Wechsel weg von aktiv bricht einen laufenden Lauf ab
	started := make(chan struct{})
	cancelled := make(chan struct{})
	f.rs.fn = func(ctx context.Context, _ []string) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	done := make(chan error, 1)
	go func() { done <- f.w.runAll(context.Background()) }()
	<-started
	f.w.set(true, ha.Passive, 2, mirrorSettings)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Lauf nicht abgebrochen")
	}
	if err := <-done; err != nil {
		t.Fatalf("Abbruch ist kein Fehler: %v", err)
	}
	if len(f.texts("alarm")) != 0 {
		t.Fatal("Alarm beim Abbruch")
	}
}

func TestMirrorNoOverlap(t *testing.T) {
	f := newMirrorFix(t)
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	f.rs.fn = func(ctx context.Context, _ []string) error {
		started <- struct{}{}
		<-release
		f.advance(3 * time.Minute) // länger als der Abstand
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	done := make(chan error, 1)
	go func() { done <- f.w.runAll(context.Background()) }()
	<-started
	if err := f.w.runAll(context.Background()); err != nil { // zweiter Aufruf: sofort zurück
		t.Fatal(err)
	}
	if !f.w.snapshot().Running {
		t.Fatal("Running fehlt")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := f.w.snapshot()
	if !st.SlowerThanInterval {
		t.Fatal("SlowerThanInterval fehlt")
	}
	if st.Running {
		t.Fatal("Running bleibt")
	}
	if n := len(f.texts("alarm")); n != 0 {
		t.Fatalf("Alarm: %v", f.texts("alarm"))
	}
}

func TestMirrorAlarmAfter(t *testing.T) {
	f := newMirrorFix(t)
	fail := true
	f.rs.fn = func(context.Context, []string) error {
		if fail {
			return context.DeadlineExceeded
		}
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	ctx := context.Background()
	f.w.runAll(ctx)
	f.advance(2 * time.Minute)
	f.w.runAll(ctx)
	if n := len(f.texts("alarm")); n != 0 {
		t.Fatalf("zu früh gemeldet: %v", f.texts("alarm"))
	}
	f.advance(4 * time.Minute)
	f.w.runAll(ctx)
	f.advance(time.Minute)
	f.w.runAll(ctx)
	if n := len(f.texts("alarm")); n != 1 {
		t.Fatalf("Alarme = %d: %v", n, f.texts("alarm"))
	}
	fail = false
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.texts("info"); len(got) != 1 || got[0] != "Spiegelung läuft wieder" {
		t.Fatalf("info: %v", got)
	}
	if f.w.snapshot().Err != "" {
		t.Fatal("Err bleibt")
	}
}

func TestMirrorZeroSettingsUseDefaults(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, ha.Settings{}) // MirrorEvery/AlarmAfter = 0
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.w.step(context.Background()) // darf nicht in Panik geraten, Abstand gilt als Standard
	if mirrorEvery(ha.Settings{}) != time.Minute || mirrorAlarmAfter(ha.Settings{}) != 5*time.Minute {
		t.Fatal("Standardwerte")
	}
	if n := f.rs.count(); n != 3 {
		t.Fatalf("Aufrufe = %d (Schritt zu früh?)", n)
	}
}

func TestMirrorMissingSource(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/r/chat_files"] = false
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.rs.count() != 2 || f.w.snapshot().Err != "" || !f.w.snapshot().FirstDone {
		t.Fatalf("calls=%d st=%+v", f.rs.count(), f.w.snapshot())
	}
}

func TestMirrorSourceEmptyRefused(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	ctx := context.Background()
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	n := f.rs.count()
	f.full["/r/uploads"] = false
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err == nil {
		t.Fatal("Fehler erwartet")
	}
	for _, c := range f.rs.calls[n:] {
		if strings.HasSuffix(c[len(c)-1], ":uploads") {
			t.Fatal("leere Quelle gespiegelt")
		}
	}
	al := f.texts("alarm")
	if len(al) != 1 || al[0] != "Quelle uploads ist leer – nicht gespiegelt" {
		t.Fatalf("alarm: %v", al)
	}

	// Nextcloud-Daten ohne .ocdata
	g := newMirrorFix(t)
	s := mirrorSettings
	s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/nc/data", "/nc/app"
	g.files["/nc/data"], g.files["/nc/app"] = true, true
	g.w.set(true, ha.Active, 1, s)
	g.w.runAll(ctx)
	for _, c := range g.rs.calls {
		if strings.HasSuffix(c[len(c)-1], ":nextcloud-data") {
			t.Fatal("nextcloud-data ohne .ocdata gespiegelt")
		}
	}
	if al := g.texts("alarm"); len(al) != 1 || !strings.Contains(al[0], "nextcloud-data ist leer") {
		t.Fatalf("alarm: %v", al)
	}
}

func TestMirrorFirstDoneOnlyAfterAll(t *testing.T) {
	f := newMirrorFix(t)
	bad := true
	f.rs.fn = func(_ context.Context, argv []string) error {
		if bad && strings.HasSuffix(argv[len(argv)-1], ":chat_files") {
			return context.DeadlineExceeded
		}
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	ctx := context.Background()
	f.w.runAll(ctx)
	if st := f.w.snapshot(); st.FirstDone || st.FirstPercent == 100 {
		t.Fatalf("FirstDone zu früh: %+v", st)
	}
	bad = false
	f.advance(time.Minute)
	f.w.runAll(ctx)
	st := f.w.snapshot()
	if !st.FirstDone || st.FirstPercent != 100 || st.LastOK.IsZero() {
		t.Fatalf("st: %+v", st)
	}
	// Neustart: Zustand aus der Datei
	w2 := newMirrorWorker(f.w.d)
	if !w2.snapshot().FirstDone || !w2.snapshot().PerLabel["uploads"].SawFiles {
		t.Fatalf("nach Neustart: %+v", w2.snapshot())
	}
}

func TestMirrorDiskCheck(t *testing.T) {
	f := newMirrorFix(t)
	f.freeK, f.free = true, 50 // gebraucht: 3 x 100
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if f.rs.count() != 0 {
		t.Fatal("rsync trotz zu wenig Platz")
	}
	al := f.texts("alarm")
	if len(al) != 1 || !strings.HasPrefix(al[0], "Zu wenig Platz am Partner") {
		t.Fatalf("alarm: %v", al)
	}
	// Platz unbekannt: kein Hindernis
	f.freeK = false
	f.advance(time.Minute)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorFinalSyncDeadline(t *testing.T) {
	f := newMirrorFix(t)
	f.rs.fn = func(ctx context.Context, _ []string) error { <-ctx.Done(); return ctx.Err() }
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.finalSync(context.Background(), 50*time.Millisecond); err == nil {
		t.Fatal("Fehler bei Fristablauf erwartet")
	}
	f.rs.fn = nil
	if err := f.w.finalSync(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}
