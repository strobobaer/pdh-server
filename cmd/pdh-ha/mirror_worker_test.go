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
	sizes map[string]int64
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
		size: func(p string) int64 {
			if n, ok := f.sizes[p]; ok {
				return n
			}
			return 100
		},
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

// Fix 1: Platz nur für Quellen, die noch nie fertig waren.
func TestMirrorDiskCheckAfterPartialSuccess(t *testing.T) {
	f := newMirrorFix(t)
	f.sizes = map[string]int64{"/r/uploads": 60, "/r/chat_files": 30, "/etc/pdh": 10}
	f.freeK, f.free = true, 150
	bad := true
	f.rs.fn = func(_ context.Context, argv []string) error {
		if bad && strings.HasSuffix(argv[len(argv)-1], ":chat_files") {
			return context.DeadlineExceeded
		}
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	ctx := context.Background()
	f.w.runAll(ctx) // kopiert uploads und etc-pdh, chat_files scheitert
	bad = false
	f.free = 40 // Partner hält jetzt 70 der 100
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatalf("Platzprüfung blockiert nach Teilerfolg: %v", err)
	}
	if !f.w.snapshot().FirstDone {
		t.Fatal("FirstDone fehlt")
	}
}

// Fix 2: finalSync meldet nur Erfolg, wenn wirklich abgeglichen wurde.
func TestMirrorFinalSyncNotActive(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Passive, 1, mirrorSettings)
	if err := f.w.finalSync(context.Background(), time.Second); err == nil {
		t.Fatal("Fehler erwartet: Worker nicht aktiv")
	}
	f.w.set(false, ha.Active, 1, mirrorSettings)
	if err := f.w.finalSync(context.Background(), time.Second); err == nil {
		t.Fatal("Fehler erwartet: Spiegelung aus")
	}
}

func TestMirrorFinalSyncAborted(t *testing.T) {
	f := newMirrorFix(t)
	started := make(chan struct{})
	f.rs.fn = func(ctx context.Context, _ []string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	done := make(chan error, 1)
	go func() { done <- f.w.finalSync(context.Background(), time.Minute) }()
	<-started
	f.w.set(true, ha.Passive, 2, mirrorSettings)
	select {
	case err := <-done:
		if !errors.Is(err, errMirrorAborted) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("finalSync hängt")
	}
}

// Abbruch speichert keinen Erfolg.
func TestMirrorAbortKeepsState(t *testing.T) {
	f := newMirrorFix(t)
	ctx := context.Background()
	f.w.set(true, ha.Active, 1, mirrorSettings)
	// Abbruch vor dem ersten Erfolg: nichts gespeichert
	started := make(chan struct{}, 1)
	f.rs.fn = func(c context.Context, _ []string) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-c.Done()
		return c.Err()
	}
	done := make(chan error, 1)
	go func() { done <- f.w.runAll(ctx) }()
	<-started
	f.w.set(true, ha.Passive, 2, mirrorSettings)
	<-done
	if st := f.w.snapshot(); st.FirstDone || !st.LastOK.IsZero() {
		t.Fatalf("Erfolg trotz Abbruch: %+v", st)
	}
	if _, err := os.Stat(f.w.d.statePath); err == nil {
		t.Fatal("mirror.json nach Abbruch geschrieben")
	}
	// Abbruch nach einem Erfolg: Datei und Zeiten unverändert
	f.rs.fn = nil
	f.w.set(true, ha.Active, 3, mirrorSettings)
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.w.d.statePath)
	okAt := f.w.snapshot().LastOK
	f.rs.fn = func(c context.Context, _ []string) error { started <- struct{}{}; <-c.Done(); return c.Err() }
	f.advance(time.Minute)
	go func() { done <- f.w.runAll(ctx) }()
	<-started
	f.w.set(true, ha.Passive, 4, mirrorSettings)
	<-done
	after, _ := os.ReadFile(f.w.d.statePath)
	if string(before) != string(after) || !f.w.snapshot().LastOK.Equal(okAt) || !f.w.snapshot().FirstDone {
		t.Fatalf("Zustand nach Abbruch verändert: %+v", f.w.snapshot())
	}
}

// Fix 3: beschädigte Zustandsdatei.
func TestMirrorCorruptState(t *testing.T) {
	f := newMirrorFix(t)
	if len(f.texts("alarm")) != 0 {
		t.Fatal("fehlende Datei darf keinen Alarm auslösen")
	}
	if err := os.WriteFile(f.w.d.statePath, []byte("{kaputt"), 0o600); err != nil {
		t.Fatal(err)
	}
	w2 := newMirrorWorker(f.w.d)
	if w2.snapshot().FirstDone {
		t.Fatal("FirstDone aus beschädigter Datei")
	}
	al := f.texts("alarm")
	if len(al) != 1 || al[0] != "Zustand der Spiegelung beschädigt, Erstabgleich läuft neu" {
		t.Fatalf("alarm: %v", al)
	}
	if _, err := os.Stat(f.w.d.statePath + ".kaputt"); err != nil {
		t.Fatalf("Datei nicht beiseitegelegt: %v", err)
	}
	if _, err := os.Stat(f.w.d.statePath); err == nil {
		t.Fatal("beschädigte Datei liegt noch")
	}
}

func TestMirrorEmptyRefusedAfterRestart(t *testing.T) {
	f := newMirrorFix(t)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	ctx := context.Background()
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	w2 := newMirrorWorker(f.w.d)
	f.full["/r/uploads"] = false
	n := f.rs.count()
	w2.set(true, ha.Active, 2, mirrorSettings)
	if err := w2.runAll(ctx); err == nil {
		t.Fatal("Fehler erwartet")
	}
	for _, c := range f.rs.calls[n:] {
		if strings.HasSuffix(c[len(c)-1], ":uploads") {
			t.Fatal("leere Quelle nach Neustart gespiegelt")
		}
	}
	if al := f.texts("alarm"); len(al) != 1 || !strings.Contains(al[0], "uploads ist leer") {
		t.Fatalf("alarm: %v", al)
	}
}

// Fix 4: FirstDone gilt für die aktuelle Quellenliste.
func TestMirrorFirstDoneFollowsSources(t *testing.T) {
	f := newMirrorFix(t)
	ctx := context.Background()
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(ctx); err != nil || !f.w.snapshot().FirstDone {
		t.Fatalf("err=%v st=%+v", err, f.w.snapshot())
	}
	s := mirrorSettings
	s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/nc/data", "/nc/app"
	f.files["/nc/data"], f.files["/nc/app"], f.files["/nc/data/.ocdata"] = true, true, true
	f.full["/nc/data"], f.full["/nc/app"] = true, true
	f.w.set(true, ha.Active, 1, s)
	if st := f.w.snapshot(); st.FirstDone || st.FirstPercent == 100 {
		t.Fatalf("FirstDone trotz neuer Quellen: %+v", st)
	}
	// Platzprüfung gilt wieder, nur für die neuen Quellen
	f.freeK, f.free = true, 1
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err == nil || !strings.Contains(err.Error(), "Zu wenig Platz") {
		t.Fatalf("err = %v", err)
	}
	f.freeK = false
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.w.snapshot().FirstDone {
		t.Fatal("FirstDone fehlt nach allen Quellen")
	}
}

// Skipped zählt nur, solange der Quellordner weiter fehlt.
func TestMirrorSkippedOnlyWhileMissing(t *testing.T) {
	f := newMirrorFix(t)
	ctx := context.Background()
	f.files["/r/chat_files"] = false
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(ctx); err != nil || !f.w.snapshot().FirstDone {
		t.Fatalf("err=%v st=%+v", err, f.w.snapshot())
	}
	f.files["/r/chat_files"] = true // taucht später auf
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if f.w.snapshot().FirstDone {
		t.Fatal("FirstDone trotz neu aufgetauchter Quelle")
	}
	f.freeK, f.free = true, 1
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err == nil || !strings.Contains(err.Error(), "Zu wenig Platz") {
		t.Fatalf("Platzprüfung umgangen: %v", err)
	}
	f.freeK = false
	f.advance(time.Minute)
	if err := f.w.runAll(ctx); err != nil || !f.w.snapshot().FirstDone {
		t.Fatalf("err=%v st=%+v", err, f.w.snapshot())
	}
}

// Eine nie fertig gewordene Quelle mit Dateien, die verschwindet, ist ein Fehler.
func TestMirrorVanishedUnfinishedSource(t *testing.T) {
	f := newMirrorFix(t)
	ctx := context.Background()
	f.rs.fn = func(_ context.Context, argv []string) error {
		if strings.HasSuffix(argv[len(argv)-1], ":chat_files") {
			return context.DeadlineExceeded
		}
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.runAll(ctx) // chat_files hat Dateien, scheitert
	f.files["/r/chat_files"] = false
	f.advance(time.Minute)
	err := f.w.runAll(ctx)
	if err == nil || !strings.Contains(err.Error(), "Quelle chat_files fehlt") {
		t.Fatalf("err = %v", err)
	}
	if f.w.snapshot().FirstDone {
		t.Fatal("FirstDone ohne die Daten der verschwundenen Quelle")
	}
	found := false
	for _, a := range f.texts("alarm") {
		found = found || a == "Quelle chat_files fehlt – nicht gespiegelt"
	}
	if !found {
		t.Fatalf("alarm: %v", f.texts("alarm"))
	}
}
