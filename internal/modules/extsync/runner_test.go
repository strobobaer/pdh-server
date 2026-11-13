package extsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Aufbau ───────────────────────────────────────────────────────────────

// runFix ist ein Taktgeber über Attrappen: je Verbindung eine Fremdtabelle im
// Speicher, Weg, Last und Wartungssperre sind im Test einstellbar.
type runFix struct {
	t        *testing.T
	r        *Runner
	mu       sync.Mutex
	foreign  map[string]Foreign // nach Verbindung
	dialect  map[string]Dialect
	mine     map[string]bool // Profile dieses Tests
	routeOK  bool
	routeWhy string
	route    func() (bool, string) // ersetzt routeOK/routeWhy
	load     Load
	skip     bool
	interval func(cfg map[string]string, minutes int) time.Duration
	clock    time.Time
}

// newRunFix baut den Taktgeber für die Profile der fixes; out darf nil sein.
func newRunFix(t *testing.T, out *Outbound, fixes ...*inFix) *runFix {
	t.Helper()
	rf := &runFix{t: t, foreign: map[string]Foreign{}, dialect: map[string]Dialect{}, mine: map[string]bool{},
		routeOK: true, clock: time.Now()}
	for _, f := range fixes {
		rf.add(f)
	}
	first := fixes[0]
	if out == nil {
		out = &Outbound{Store: first.s, Targets: first.in.Targets, SystemUser: systemUser, Location: time.UTC}
	}
	rf.r = NewRunner(RunnerConfig{
		Store: first.s, In: first.in, Out: out,
		Conn: func(context.Context, string) (string, map[string]string, error) {
			return "attrappe", map[string]string{"allow_write": "true"}, nil
		},
		Route: func(context.Context, map[string]string) (bool, string) {
			rf.mu.Lock()
			defer rf.mu.Unlock()
			if rf.route != nil {
				return rf.route()
			}
			return rf.routeOK, rf.routeWhy
		},
		Skip: func() bool { rf.mu.Lock(); defer rf.mu.Unlock(); return rf.skip },
		Interval: func(cfg map[string]string, m int) time.Duration {
			if rf.interval != nil {
				return rf.interval(cfg, m)
			}
			return time.Duration(m) * time.Minute
		},
		Load: func(context.Context) Load { rf.mu.Lock(); defer rf.mu.Unlock(); return rf.load },
	})
	rf.r.only = func(id string) bool { rf.mu.Lock(); defer rf.mu.Unlock(); return rf.mine[id] }
	rf.r.now = func() time.Time { rf.mu.Lock(); defer rf.mu.Unlock(); return rf.clock }
	rf.r.testForeign = func(connID string) (Foreign, Dialect, error) {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		f := rf.foreign[connID]
		if f == nil {
			return nil, nil, errors.New("attrappe: Verbindung unbekannt")
		}
		return f, rf.dialect[connID], nil
	}
	return rf
}

func (rf *runFix) add(f *inFix) {
	rf.foreign[f.p.ConnectionID] = f.ff
	rf.dialect[f.p.ConnectionID] = f.d
	rf.mine[f.p.ID] = true
}

func (rf *runFix) set(fn func()) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	fn()
}

// in fährt einen Durchgang hereinkommend und wartet auf die Läufe.
func (rf *runFix) in() { rf.r.inboundPass(context.Background())() }

func (rf *runFix) out() { rf.r.outboundPass(context.Background()) }

var overCPU = Load{CPUPerCore: 2, MemAvailable: 0.5, DBUsed: 0.1, Known: true}

// armed schaltet das Profil scharf und macht es sofort fällig.
func (f *inFix) armed() {
	f.t.Helper()
	f.sf.arm(f.t, f.p.ID)
	f.dueNow()
}

func (f *inFix) dueNow() {
	f.t.Helper()
	_, err := f.sf.pool.Exec(context.Background(), `UPDATE sync_profiles SET next_run_at = NULL WHERE id = $1`, f.p.ID)
	must(f.t, err)
}

type schedRow struct {
	fails int
	next  *time.Time
	state string
}

func (f *inFix) sched() schedRow {
	f.t.Helper()
	var s schedRow
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT fail_count, next_run_at, state FROM sync_profiles WHERE id = $1`,
		f.p.ID).Scan(&s.fails, &s.next, &s.state))
	return s
}

// nextIn prüft, dass der nächste Lauf etwa in d fällig ist.
func (f *inFix) nextIn(d time.Duration) {
	f.t.Helper()
	s := f.sched()
	if s.next == nil {
		f.t.Fatalf("nächster Lauf fehlt, erwartet in %v", d)
	}
	if got := time.Until(*s.next); got < d-20*time.Second || got > d+20*time.Second {
		f.t.Fatalf("nächster Lauf in %v, erwartet in %v", got.Round(time.Second), d)
	}
}

func (f *inFix) runs(direction string) []Run {
	f.t.Helper()
	all, err := f.s.ListRuns(context.Background(), f.p.ID, 100)
	must(f.t, err)
	var out []Run
	for _, r := range all {
		if r.Direction == direction {
			out = append(out, r)
		}
	}
	return out
}

// ── Reine Regeln ─────────────────────────────────────────────────────────

func TestRunnerBackoffCapsAt15Min(t *testing.T) {
	cases := []struct {
		interval time.Duration
		fails    int
		want     time.Duration
	}{
		{5 * time.Minute, 1, 10 * time.Minute},
		{5 * time.Minute, 2, 15 * time.Minute},
		{5 * time.Minute, 3, 15 * time.Minute},
		{5 * time.Minute, 500, 15 * time.Minute},
		{7 * time.Minute, 1, 14 * time.Minute},
		{30 * time.Minute, 1, 15 * time.Minute},
		{30 * time.Minute, 0, 15 * time.Minute},
	}
	for _, c := range cases {
		if got := failPause(c.interval, c.fails); got != c.want {
			t.Errorf("failPause(%v, %d) = %v, erwartet %v", c.interval, c.fails, got, c.want)
		}
	}
}

func TestRunnerIntervalBounds5to30(t *testing.T) {
	r := NewRunner(RunnerConfig{})
	for minutes, want := range map[int]time.Duration{0: 5 * time.Minute, 1: 5 * time.Minute, 5: 5 * time.Minute,
		12: 12 * time.Minute, 30: 30 * time.Minute, 31: 30 * time.Minute, 1440: 30 * time.Minute} {
		if got := r.interval(nil, minutes); got != want {
			t.Errorf("Takt %d min = %v, erwartet %v", minutes, got, want)
		}
	}
	r = NewRunner(RunnerConfig{Interval: func(map[string]string, int) time.Duration { return time.Hour }})
	if got := r.interval(nil, 5); got != 30*time.Minute {
		t.Errorf("Takt der Verbindung über 30 min: %v", got)
	}
}

// ── Hereinkommend ────────────────────────────────────────────────────────

func TestRunnerOnlyArmedProfiles(t *testing.T) {
	a := newInFix(t, fixOpt{})
	b := newInFix(t, fixOpt{})
	a.row(1, "scharf", "hoch", "offen")
	b.row(1, "entwurf", "hoch", "offen")
	a.armed()
	rf := newRunFix(t, nil, a, b)
	rf.in()
	if a.records() != 1 || b.records() != 0 {
		t.Fatalf("nur das scharfe Profil läuft: a=%d b=%d", a.records(), b.records())
	}
	if s := a.sched(); s.fails != 0 {
		t.Fatalf("Fehlerzähler: %+v", s)
	}
	a.nextIn(5 * time.Minute)
	if len(b.runs(DirIn)) != 0 {
		t.Fatal("Profil im Entwurf hat einen Lauf")
	}
	// Nicht fällig: der zweite Durchgang tut nichts.
	a.row(2, "später", "hoch", "offen")
	rf.in()
	if a.records() != 1 {
		t.Fatalf("Lauf vor dem Takt: %d Datensätze", a.records())
	}
	// Fortschrittsmarke gespeichert: der nächste fällige Lauf holt nur die neue Zeile.
	a.dueNow()
	rf.in()
	if a.records() != 2 {
		t.Fatalf("zweiter Lauf: %d Datensätze", a.records())
	}
	if rs := a.runs(DirIn); len(rs) != 2 || rs[0].New != 1 {
		t.Fatalf("Läufe: %+v", rs)
	}
}

func TestRunnerSkipsWhenRouteDown(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "weg", "hoch", "offen")
	f.userUpdate(id, `title = $2`, f.title("neu"))
	before := f.openItems()
	if len(before) != 1 {
		t.Fatalf("Ausgangslage: %d Einträge", len(before))
	}
	f.armed()
	rf := newRunFix(t, f.out, f.inFix)
	rf.set(func() { rf.routeOK, rf.routeWhy = false, "Tunnel steht nicht" })
	queries := f.ff.queryCount()
	rf.in()
	rf.out()
	if f.ff.queryCount() != queries || len(f.ff.execStmts()) != 0 {
		t.Fatal("Fremddatenbank wurde trotz fehlendem Weg angesprochen")
	}
	rs := f.runs(DirIn)
	if len(rs) != 2 || rs[0].SkippedReason != "Weg nicht bereit: Tunnel steht nicht" {
		t.Fatalf("Grund im Lauf: %+v", rs)
	}
	if s := f.sched(); s.fails != 1 {
		t.Fatalf("Fehlerzähler: %+v", s)
	}
	f.nextIn(10 * time.Minute)
	// Warteliste unverändert: kein Fehlversuch, nichts erledigt.
	after := f.openItems()
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].Attempts != 0 || after[0].LastError != "" {
		t.Fatalf("Warteliste verändert: %+v", after)
	}
	if rs := f.runs(DirOut); len(rs) != 1 || rs[0].SkippedReason != "Weg nicht bereit: Tunnel steht nicht" {
		t.Fatalf("Grund im Durchgang: %+v", rs)
	}
	// Die Verbindung pausiert für die Warteliste; nach der Pause und mit Weg wird nachgeholt.
	rf.set(func() { rf.routeOK = true })
	rf.out()
	if len(f.openItems()) != 1 {
		t.Fatal("Warteliste lief während der Pause")
	}
	rf.set(func() { rf.clock = rf.clock.Add(6 * time.Second) })
	rf.out()
	if len(f.openItems()) != 0 {
		t.Fatal("Warteliste nach der Pause nicht abgearbeitet")
	}
	if got := f.ff.get(int64(1))["Titel"]; got != f.title("neu") {
		t.Fatalf("Titel im Fremdsystem: %v", got)
	}
}

func TestRunnerUnreachableDatabase(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.armed()
	rf := newRunFix(t, nil, f)
	rf.r.testForeign = func(string) (Foreign, Dialect, error) { return nil, nil, errors.New("Zeitüberschreitung") }
	rf.in()
	if rs := f.runs(DirIn); len(rs) != 1 || rs[0].SkippedReason != "Datenbank nicht erreichbar: Zeitüberschreitung" {
		t.Fatalf("Grund: %+v", rs)
	}
	if s := f.sched(); s.fails != 1 || s.state != StateArmed {
		t.Fatalf("Zeitplan: %+v", s)
	}
}

func TestRunnerRepeatedSkipOneRunRow(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.armed()
	rf := newRunFix(t, nil, f)
	rf.set(func() { rf.routeOK, rf.routeWhy = false, "Tunnel steht nicht" })
	for i := 0; i < 3; i++ {
		f.dueNow()
		rf.in()
	}
	rs := f.runs(DirIn)
	if len(rs) != 1 || rs[0].FinishedAt == nil {
		t.Fatalf("gleicher Grund dreimal: %d Einträge", len(rs))
	}
	if s := f.sched(); s.fails != 3 {
		t.Fatalf("Fehlerzähler: %+v", s)
	}
	f.nextIn(15 * time.Minute) // 5 min · 2^3, gedeckelt
	rf.set(func() { rf.routeWhy = "Tunnel nicht gefunden" })
	f.dueNow()
	rf.in()
	if rs := f.runs(DirIn); len(rs) != 2 {
		t.Fatalf("anderer Grund: %d Einträge", len(rs))
	}
	// Erfolg setzt den Fehlerzähler zurück.
	rf.set(func() { rf.routeOK = true })
	f.dueNow()
	rf.in()
	if s := f.sched(); s.fails != 0 {
		t.Fatalf("Fehlerzähler nach Erfolg: %+v", s)
	}
	if rs := f.runs(DirIn); len(rs) != 3 || rs[0].SkippedReason != "" {
		t.Fatalf("Läufe nach Erfolg: %+v", rs)
	}
}

func TestRunnerSkipInMaintenance(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "sperre", "hoch", "offen")
	f.userUpdate(id, `title = $2`, f.title("neu"))
	f.row(2, "wartet", "hoch", "offen")
	f.armed()
	rf := newRunFix(t, f.out, f.inFix)
	rf.set(func() { rf.skip = true })
	runs := f.runsCount()
	rf.in()
	rf.out()
	if f.records() != 1 || len(f.openItems()) != 1 || f.runsCount() != runs {
		t.Fatal("in der Wartungssperre lief etwas")
	}
	if _, err := rf.r.RunNow(context.Background(), f.p.ID); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("RunNow in der Wartungssperre: %v", err)
	}
	rf.set(func() { rf.skip = false })
	rf.in()
	rf.out()
	if f.records() != 2 || len(f.openItems()) != 0 {
		t.Fatalf("nach der Sperre nicht nachgeholt: %d Datensätze, %d Einträge", f.records(), len(f.openItems()))
	}
}

func TestRunnerVPNIntervalOverridesProfile(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.armed()
	rf := newRunFix(t, nil, f)
	for _, c := range []struct{ conn, want time.Duration }{
		{7 * time.Minute, 7 * time.Minute},
		{time.Minute, 5 * time.Minute},
		{2 * time.Hour, 30 * time.Minute},
	} {
		rf.set(func() { rf.interval = func(map[string]string, int) time.Duration { return c.conn } })
		f.dueNow()
		rf.in()
		f.nextIn(c.want)
	}
}

func TestRunnerRecoversPanic(t *testing.T) {
	a := newInFix(t, fixOpt{})
	b := newInFix(t, fixOpt{})
	a.armed()
	b.armed()
	b.row(1, "läuft", "hoch", "offen")
	rf := newRunFix(t, nil, a, b)
	aConn := a.p.ConnectionID
	rf.r.c.Conn = func(_ context.Context, id string) (string, map[string]string, error) {
		if id == aConn {
			panic("kaputt")
		}
		return "attrappe", map[string]string{}, nil
	}
	rf.in()
	rs := a.runs(DirIn)
	if len(rs) != 1 || !strings.Contains(rs[0].SkippedReason, "interner Fehler") || !strings.Contains(rs[0].SkippedReason, "kaputt") {
		t.Fatalf("Absturz nicht als Grund gespeichert: %+v", rs)
	}
	if s := a.sched(); s.fails != 1 || s.next == nil {
		t.Fatalf("Zeitplan nach Absturz: %+v", s)
	}
	if b.records() != 1 {
		t.Fatal("das andere Profil lief nicht")
	}
}

// countingForeign zählt gleichzeitige Abfragen über alle Verbindungen.
type countingForeign struct {
	Foreign
	active, peak *atomic.Int32
	hold         time.Duration
}

func (c *countingForeign) Describe(ctx context.Context, schema, table string) (*TableInfo, error) {
	n := c.active.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(c.hold)
	defer c.active.Add(-1)
	return c.Foreign.Describe(ctx, schema, table)
}

func TestRunnerOneRunPerConnection(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "eins", "hoch", "offen")
	f.armed()
	// Zweites Profil auf derselben Verbindung.
	ctx := context.Background()
	p2 := &Profile{Name: fmt.Sprintf("extsync-run2-%d", time.Now().UnixNano()), ConnectionID: f.p.ConnectionID,
		TargetType: RefTicket, SourceSchema: f.p.SourceSchema, SourceTable: f.p.SourceTable, KeyColumns: f.p.KeyColumns,
		ChangeKind: f.p.ChangeKind, ChangeColumn: f.p.ChangeColumn, TimeZone: "UTC"}
	must(t, f.s.SaveProfile(ctx, p2, systemUser))
	must(t, f.s.ReplaceMaps(ctx, p2.ID, stdMaps(RefTicket)))
	t.Cleanup(func() { cleanupProfileRecords(f.sf, p2.ID) })
	f.sf.arm(t, p2.ID)

	rf := newRunFix(t, nil, f)
	rf.set(func() { rf.mine[p2.ID] = true })
	var active, peak atomic.Int32
	rf.set(func() {
		rf.foreign[f.p.ConnectionID] = &countingForeign{Foreign: f.ff, active: &active, peak: &peak, hold: 80 * time.Millisecond}
	})
	wait := rf.r.inboundPass(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := rf.r.RunNow(ctx, p2.ID); err != nil {
			t.Errorf("RunNow: %v", err)
		}
	}()
	wait()
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("gleichzeitige Läufe auf einer Verbindung: %d", peak.Load())
	}
	var n int
	must(t, f.sf.pool.QueryRow(ctx, `SELECT count(*) FROM sync_runs WHERE profile_id = ANY($1::uuid[]) AND skipped_reason = ''`,
		[]string{f.p.ID, p2.ID}).Scan(&n))
	if n < 2 {
		t.Fatalf("beide Profile sollten gelaufen sein: %d Läufe", n)
	}
}

func TestRunnerMaxTwoConnections(t *testing.T) {
	var fixes []*inFix
	for i := 0; i < 4; i++ {
		f := newInFix(t, fixOpt{})
		f.armed()
		fixes = append(fixes, f)
	}
	rf := newRunFix(t, nil, fixes...)
	var active, peak atomic.Int32
	rf.set(func() {
		for _, f := range fixes {
			rf.foreign[f.p.ConnectionID] = &countingForeign{Foreign: f.ff, active: &active, peak: &peak, hold: 100 * time.Millisecond}
		}
	})
	rf.in()
	if peak.Load() != 2 {
		t.Fatalf("gleichzeitige Verbindungen: %d, erwartet genau 2", peak.Load())
	}
	for i, f := range fixes {
		if len(f.runs(DirIn)) != 1 {
			t.Fatalf("Profil %d lief nicht", i)
		}
	}
}

func TestRunNowNeedsArmedProfile(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "hand", "hoch", "offen")
	rf := newRunFix(t, nil, f)
	if _, err := rf.r.RunNow(context.Background(), f.p.ID); !errors.Is(err, ErrNotArmed) {
		t.Fatalf("Entwurf: %v", err)
	}
	f.armed()
	// Von Hand läuft es auch bei Überlastung, vollständig.
	rf.set(func() { rf.load = overCPU })
	run, err := rf.r.RunNow(context.Background(), f.p.ID)
	if err != nil || run == nil || run.New != 1 {
		t.Fatalf("RunNow: %+v %v", run, err)
	}
	f.nextIn(5 * time.Minute)
}

// ── Überlastung ──────────────────────────────────────────────────────────

func TestRunnerDefersWhenOverloaded(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "vorher", "hoch", "offen")
	f.armed()
	rf := newRunFix(t, nil, f)
	rf.in() // ein gewöhnlicher Lauf
	f.row(2, "wartet", "hoch", "offen")
	rf.set(func() { rf.load = overCPU })
	queries := f.ff.queryCount()
	for i := 0; i < 3; i++ {
		f.dueNow()
		rf.in()
	}
	if f.ff.queryCount() != queries || f.records() != 1 {
		t.Fatal("trotz Überlastung gelaufen")
	}
	rs := f.runs(DirIn)
	if len(rs) != 2 || rs[0].SkippedReason != "Warteposition wegen hoher Serverlast (Prozessor)" {
		t.Fatalf("Warteposition im Lauf (ein Eintrag): %+v", rs)
	}
	if s := f.sched(); s.fails != 0 || s.state != StateArmed {
		t.Fatalf("Warteposition ist kein Fehler: %+v", s)
	}
	f.nextIn(time.Minute)
	// Last vorbei: der Abgleich holt auf.
	rf.set(func() { rf.load = Load{} })
	f.dueNow()
	rf.in()
	if f.records() != 2 {
		t.Fatal("nach der Überlastung nicht aufgeholt")
	}
}

func TestRunnerRunsAfter30MinDespiteOverload(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "vorher", "hoch", "offen")
	f.armed()
	rf := newRunFix(t, nil, f)
	rf.in()
	for i := int64(2); i <= 61; i++ {
		f.row(i, fmt.Sprintf("zeile %d", i), "hoch", "offen")
	}
	rf.set(func() { rf.load = overCPU })
	ctx := context.Background()
	// 29 Minuten nach dem letzten Lauf: noch Warteposition.
	_, err := f.sf.pool.Exec(ctx, `UPDATE sync_runs SET started_at = now() - interval '29 minutes',
		finished_at = now() - interval '29 minutes' WHERE profile_id = $1`, f.p.ID)
	must(t, err)
	f.dueNow()
	rf.in()
	if f.records() != 1 {
		t.Fatal("vor Ablauf der 30 Minuten gelaufen")
	}
	// Über 30 Minuten (die Warteposition selbst zählt nicht als Lauf): läuft mit höchstens 50 Zeilen.
	_, err = f.sf.pool.Exec(ctx, `UPDATE sync_runs SET started_at = now() - interval '31 minutes',
		finished_at = now() - interval '31 minutes' WHERE profile_id = $1 AND skipped_reason = ''`, f.p.ID)
	must(t, err)
	f.dueNow()
	rf.in()
	if got := f.records(); got != 51 {
		t.Fatalf("erzwungener Lauf: %d Datensätze, erwartet 51 (1 + 50)", got)
	}
	f.nextIn(5 * time.Minute)
	// Gleich danach wieder Warteposition.
	f.dueNow()
	rf.in()
	if got := f.records(); got != 51 {
		t.Fatalf("nach dem erzwungenen Lauf wieder gelaufen: %d", got)
	}
}

func TestRunnerNeverRunForcedAfter30Min(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "erste", "hoch", "offen")
	f.armed()
	rf := newRunFix(t, nil, f)
	rf.set(func() { rf.load = overCPU })
	rf.in()
	if f.records() != 0 {
		t.Fatal("erster Lauf trotz Überlastung")
	}
	rf.set(func() { rf.clock = rf.clock.Add(31 * time.Minute) })
	f.dueNow()
	rf.in()
	if f.records() != 1 {
		t.Fatal("nie gelaufenes Profil blieb über 30 Minuten in Warteposition")
	}
}

func TestRunnerOutboxSlowsWhenOverloaded(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	for i := int64(1); i <= 7; i++ {
		f.row(i, fmt.Sprintf("zeile %d", i), "hoch", "offen")
	}
	f.run(RunOptions{})
	for i := 1; i <= 7; i++ {
		id, _, _, _, _ := f.rec(k(i))
		f.userUpdate(id, `title = $2`, f.title(fmt.Sprintf("neu %d", i)))
	}
	f.armed()
	rf := newRunFix(t, f.out, f.inFix)
	rf.set(func() { rf.load = overCPU })
	rf.out()
	if got := len(f.openItems()); got != 2 {
		t.Fatalf("erster Durchgang bei Überlastung: %d offen, erwartet 2 (höchstens 5 je Durchgang)", got)
	}
	// Reihenfolge: die ältesten fünf zuerst.
	if got := f.ff.get(int64(1))["Titel"]; got != f.title("neu 1") {
		t.Fatalf("ältester Eintrag nicht zuerst: %v", got)
	}
	rf.out()
	if got := len(f.openItems()); got != 2 {
		t.Fatalf("zweiter Durchgang vor Ablauf von 30 s lief: %d offen", got)
	}
	rf.set(func() { rf.clock = rf.clock.Add(31 * time.Second) })
	rf.out()
	if got := len(f.openItems()); got != 0 {
		t.Fatalf("nach 30 s: %d offen", got)
	}
	// Ein gesammelter Eintrag "hinaus" mit allen sieben.
	if rs := f.runs(DirOut); len(rs) != 1 || rs[0].Written != 7 || rs[0].Failed != 0 {
		t.Fatalf("Durchgänge: %+v", rs)
	}
}

// ── Hinausgehend ─────────────────────────────────────────────────────────

func TestRunnerOutboundFailureCountsAndReports(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "fehler", "hoch", "offen")
	f.userUpdate(id, `title = $2`, f.title("neu"))
	f.armed()
	rf := newRunFix(t, f.out, f.inFix)
	f.ff.failExec = func(Stmt) error { return errors.New("Sperre im Fremdsystem") }
	rf.out()
	items := f.openItems()
	if len(items) != 1 || items[0].Attempts != 1 || !strings.Contains(items[0].LastError, "Sperre im Fremdsystem") {
		t.Fatalf("Fehlversuch: %+v", items)
	}
	// Der Eintrag wartet (5 s): der nächste Durchgang fasst ihn nicht an.
	rf.out()
	if it := f.openItems(); it[0].Attempts != 1 {
		t.Fatalf("Eintrag vor Ablauf der Pause erneut versucht: %+v", it)
	}
	rs := f.runs(DirOut)
	if len(rs) != 1 || rs[0].Failed != 1 || rs[0].Written != 0 || len(rs[0].Errors) != 1 || rs[0].Errors[0].ForeignKey != k(1) {
		t.Fatalf("Durchgang: %+v", rs)
	}
	// Nach dem vierten Fehlversuch macht der fünfte den Eintrag "hängend": einmal am Datensatz gemeldet.
	_, err := f.sf.pool.Exec(context.Background(), `UPDATE sync_outbox SET attempts = 4, next_try_at = now() WHERE id = $1`, items[0].ID)
	must(t, err)
	rf.out()
	stuck := 0
	for _, h := range f.history(id, actionSync) {
		if strings.HasPrefix(h.msg, noteOutStuckPrefix) {
			stuck++
		}
	}
	if stuck != 1 {
		t.Fatalf("hängender Eintrag: %d Vermerke am Datensatz", stuck)
	}
	// Fremdsystem wieder da: nachgeholt.
	f.ff.failExec = nil
	_, err = f.sf.pool.Exec(context.Background(), `UPDATE sync_outbox SET next_try_at = now() WHERE id = $1`, items[0].ID)
	must(t, err)
	rf.out()
	if len(f.openItems()) != 0 || f.ff.get(int64(1))["Titel"] != f.title("neu") {
		t.Fatal("nach dem Fehler nicht nachgeholt")
	}
}

// Ist „Schreiben erlaubt“ an der Verbindung aus, bleibt die Warteliste
// unverändert (kein Fehlversuch, keine Meldung am Datensatz); nach dem
// Einschalten geht es binnen 30 s weiter.
func TestRunnerOutboundWriteSwitchOff(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "schalter", "hoch", "offen")
	f.userUpdate(id, `title = $2`, f.title("neu"))
	f.armed()
	rf := newRunFix(t, f.out, f.inFix)
	allow := "false"
	rf.r.c.Conn = func(context.Context, string) (string, map[string]string, error) {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		return "attrappe", map[string]string{"allow_write": allow}, nil
	}
	for i := 0; i < 3; i++ {
		rf.out()
		rf.set(func() { rf.clock = rf.clock.Add(31 * time.Second) })
	}
	items := f.openItems()
	if len(items) != 1 || items[0].Attempts != 0 || len(f.ff.execStmts()) != 0 {
		t.Fatalf("Warteliste bei ausgeschaltetem Schreiben verändert: %+v", items)
	}
	if rs := f.runs(DirOut); len(rs) != 1 || rs[0].SkippedReason != ErrWriteNotAllowed.Error() {
		t.Fatalf("Grund im Durchgang (ein Eintrag): %+v", rs)
	}
	rf.set(func() { allow = "true" })
	rf.out()
	if len(f.openItems()) != 0 || f.ff.get(int64(1))["Titel"] != f.title("neu") {
		t.Fatal("nach dem Einschalten nicht nachgeholt")
	}
}

func TestRunnerOutboundOnlyArmed(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "entwurf", "hoch", "offen")
	f.userUpdate(id, `title = $2`, f.title("neu"))
	rf := newRunFix(t, f.out, f.inFix)
	rf.out()
	if len(f.openItems()) != 1 || len(f.ff.execStmts()) != 0 {
		t.Fatal("Warteliste eines nicht scharfen Profils wurde abgearbeitet")
	}
}

func TestRunnerKickDoesNotBlock(t *testing.T) {
	r := NewRunner(RunnerConfig{})
	for i := 0; i < 5; i++ {
		r.Kick()
	}
	if len(r.kick) != 1 {
		t.Fatalf("Kick: %d wartende Anstöße", len(r.kick))
	}
}

func TestRunnerLoopsEndWithContext(t *testing.T) {
	r := NewRunner(RunnerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.loop(ctx, time.Hour, r.kick, func() { panic("kaputt") })
		close(done)
	}()
	r.Kick() // ein Absturz im Durchgang beendet die Schleife nicht
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Schleife endet nicht mit dem Kontext")
	}
}
