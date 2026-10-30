package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// fakeAgent ersetzt den Update-Agenten und protokolliert die Aufrufe.
type fakeAgent struct {
	mu    sync.Mutex
	calls []string
	err   error
	block chan struct{} // gesetzt: Aufruf wartet darauf oder auf das Ende von ctx
	busy  bool          // Busy meldet einen laufenden Lauf
}

func (f *fakeAgent) wait(ctx context.Context) error {
	f.mu.Lock()
	b := f.block
	f.mu.Unlock()
	if b == nil {
		return nil
	}
	select {
	case <-b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeAgent) add(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return f.err
}

func (f *fakeAgent) Install(ctx context.Context, commit string, noRestart bool) error {
	if err := f.add(fmt.Sprintf("install %s %v", commit, noRestart)); err != nil {
		return err
	}
	return f.wait(ctx)
}

func (f *fakeAgent) Rollback(_ context.Context, commit string, noRestart bool) error {
	return f.add(fmt.Sprintf("rollback %s %v", commit, noRestart))
}

func (f *fakeAgent) Finish(context.Context) error { return f.add("finish") }

func (f *fakeAgent) Busy(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy, nil
}

func (f *fakeAgent) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func agentOf(d *daemon) *fakeAgent { return d.up.agent.(*fakeAgent) }

// upPeerView: Partner b passiv, sieht a.
func upPeerView(commit, pdh string) peerView {
	return seenView(message{Role: "passiv", Term: 1, Ver: "test", Commit: commit, PDH: pdh, Sees: []string{"a"}})
}

// upActive: Knoten a aktiv im Duo, Partner b gesehen.
func upActive(t *testing.T) (*daemon, *fakeNet) {
	t.Helper()
	fn := newFakeNet()
	fn.setView("b", upPeerView("", ""))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}, fn)
	return d, fn
}

func rollingJob(phase ha.UpdatePhase, seq uint64) ha.UpdateJob {
	return ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, Source: "knopf", Target: "x1", TargetVersion: "0.9.1",
		OldVersion: "0.9.0", Old: map[string]string{"a": "o1", "b": "o1"}, First: "b", Second: "a", Phase: phase, Seq: seq}
}

func sentIndex(fn *fakeNet, typ string) int {
	fn.mu.Lock()
	defer fn.mu.Unlock()
	for i, s := range fn.sent {
		if s.m.T == typ {
			return i
		}
	}
	return -1
}

func TestUpdateJobRoundTrip(t *testing.T) {
	st := store{etc: t.TempDir(), state: filepath.Join(t.TempDir(), "neu")}
	if j, err := st.loadJob(); err != nil || j.ID != "" {
		t.Fatalf("ohne Datei: leerer Auftrag erwartet: %+v %v", j, err)
	}
	if f, err := st.loadFaulty(); err != nil || len(f) != 0 {
		t.Fatalf("ohne Datei: keine fehlerhaften Versionen erwartet: %v %v", f, err)
	}
	j := rollingJob(ha.UpVerifyNew, 7)
	j.LockedAt = time.Date(2026, 10, 30, 2, 3, 4, 0, time.UTC)
	j.WaitOp, j.WaitNode, j.WaitSeq = ha.OpThaw, "a", 6
	if err := st.saveJob(j); err != nil {
		t.Fatal(err)
	}
	got, err := st.loadJob()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "j1" || got.Seq != 7 || got.Phase != ha.UpVerifyNew || !got.LockedAt.Equal(j.LockedAt) ||
		got.Old["b"] != "o1" || got.WaitOp != ha.OpThaw || got.WaitSeq != 6 {
		t.Fatalf("Auftrag nicht unverändert gelesen: %+v", got)
	}
	if _, err := os.Stat(st.statePath("update-job.json")); err != nil {
		t.Fatal(err)
	}
	if err := st.saveFaulty([]string{"x1", "x2"}); err != nil {
		t.Fatal(err)
	}
	f, err := st.loadFaulty()
	if err != nil || !slices.Equal(f, []string{"x1", "x2"}) {
		t.Fatalf("fehlerhafte Versionen: %v %v", f, err)
	}
	if _, err := os.Stat(st.statePath("update-faulty.json")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateNodeOpCallsAgent(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"b"}}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1, PartnerVersion: "test"}, fn)
	job := rollingJob(ha.UpPrepare, 3)
	job.WaitOp, job.WaitNode, job.WaitSeq = ha.OpInstall, "b", 3
	msg := message{T: "update_node", Op: ha.OpInstall, Commit: "x1", JobID: "j1", Job: &job, Seq: 77}
	d.handle(context.Background(), inbound{From: "a", Msg: msg})
	waitFor(t, 2*time.Second, "update_ack", func() bool { return len(fn.messages("update_ack")) == 1 })
	acks := fn.messages("update_ack")
	a := acks[0]
	if a.to != "a" || a.m.Ack == nil {
		t.Fatalf("Ergebnis an a erwartet: %+v", a)
	}
	want := ha.UpdateAck{JobID: "j1", Op: ha.OpInstall, Node: "b", OK: true, Seq: 3}
	if *a.m.Ack != want {
		t.Fatalf("Ergebnis: %+v, erwartet %+v", *a.m.Ack, want)
	}
	if got := agentOf(d).list(); !slices.Equal(got, []string{"install x1 false"}) {
		t.Fatalf("Agent: %v", got)
	}
	// Auftrag mitgespiegelt
	if d.up.job.Seq != 3 {
		t.Fatalf("Auftrag aus update_node nicht übernommen: %+v", d.up.job)
	}
	// Wiederholung derselben Anforderung: Ergebnis erneut, kein zweiter Lauf
	msg.Seq = 78
	d.handle(context.Background(), inbound{From: "a", Msg: msg})
	waitFor(t, 2*time.Second, "zweites update_ack", func() bool { return len(fn.messages("update_ack")) == 2 })
	if got := agentOf(d).list(); len(got) != 1 {
		t.Fatalf("Agent doppelt aufgerufen: %v", got)
	}
	// nur vom Verbund-Partner
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "update_node", Op: ha.OpInstall, Commit: "x2", JobID: "j1", Job: &job, Seq: 79}})
	time.Sleep(50 * time.Millisecond)
	if got := agentOf(d).list(); len(got) != 1 {
		t.Fatalf("Anforderung eines Fremden ausgeführt: %v", got)
	}
}

func TestUpdateNodeOpFailureAcked(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	agentOf(d).err = errors.New("Bau fehlgeschlagen")
	job := rollingJob(ha.UpPrepare, 3)
	job.WaitOp, job.WaitNode, job.WaitSeq = ha.OpInstall, "b", 3
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_node", Op: ha.OpInstall, Commit: "x1", JobID: "j1", Job: &job, Seq: 1}})
	waitFor(t, 2*time.Second, "update_ack", func() bool { return len(fn.messages("update_ack")) == 1 })
	a := fn.messages("update_ack")[0].m.Ack
	if a.OK || !strings.Contains(a.Err, "Bau fehlgeschlagen") {
		t.Fatalf("Fehler-Ergebnis erwartet: %+v", a)
	}
}

func TestUpdateJobMirrorHigherSeq(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	j5 := rollingJob(ha.UpCountdown, 5)
	j5.MarkFaulty = true
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_job", Job: &j5, Seq: 1}})
	if d.up.job.Seq != 5 || d.up.job.Phase != ha.UpCountdown {
		t.Fatalf("höherer Seq nicht übernommen: %+v", d.up.job)
	}
	if got, _ := d.st.loadJob(); got.Seq != 5 {
		t.Fatalf("gespiegelter Auftrag nicht gespeichert: %+v", got)
	}
	if cl, _ := d.st.loadCluster(); !slices.Equal(cl.Faulty, []string{"x1"}) {
		t.Fatalf("fehlerhafte Version nicht gemerkt: %v", cl.Faulty)
	}
	for _, seq := range []uint64{4, 5} {
		old := rollingJob(ha.UpPrepare, seq)
		d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_job", Job: &old, Seq: 10 + seq}})
		if d.up.job.Phase != ha.UpCountdown {
			t.Fatalf("Seq %d hat den Auftrag ersetzt: %+v", seq, d.up.job)
		}
	}
	// vom Zeugen nie
	w := rollingJob(ha.UpDone, 9)
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "update_job", Job: &w, Seq: 20}})
	if d.up.job.Seq != 5 {
		t.Fatal("Auftrag vom Zeugen übernommen")
	}
}

func TestUpdateTickConducts(t *testing.T) {
	d, fn := upActive(t)
	d.up.job = rollingJob(ha.UpPrepare, 4)
	d.tick(context.Background())
	if d.up.job.Seq != 5 || d.up.job.WaitOp != ha.OpInstall {
		t.Fatalf("Schritt nicht ausgeführt: %+v", d.up.job)
	}
	if got, _ := d.st.loadJob(); got.Seq != 5 {
		t.Fatalf("Auftrag nicht gespeichert: %+v", got)
	}
	ij, in := sentIndex(fn, "update_job"), sentIndex(fn, "update_node")
	if ij < 0 || in < 0 || ij > in {
		t.Fatalf("update_job vor update_node erwartet: %d %d", ij, in)
	}
	n := fn.messages("update_node")[0]
	if n.to != "b" || n.m.Op != ha.OpInstall || n.m.Commit != "x1" || n.m.JobID != "j1" || n.m.Job == nil || n.m.Job.Seq != 5 {
		t.Fatalf("update_node: %+v", n)
	}
	// Ergebnis des Partners → nächster Schritt
	d.handle(context.Background(), inbound{From: "b", Msg: message{T: "update_ack", Seq: 3,
		Ack: &ha.UpdateAck{JobID: "j1", Op: ha.OpInstall, Node: "b", OK: true, Seq: 5}}})
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpVerifyPrep {
		t.Fatalf("nach dem Ergebnis Prüfung erwartet: %+v", d.up.job)
	}
}

func TestUpdateTickPassiveDoesNotConduct(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"b"}}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1, PartnerVersion: "test"}, fn)
	d.up.job = rollingJob(ha.UpPrepare, 4)
	d.tick(context.Background())
	if d.up.job.Seq != 4 || len(fn.messages("update_node")) != 0 || len(fn.messages("update_job")) != 0 {
		t.Fatalf("passiver Server dirigiert: %+v", d.up.job)
	}
}

func TestUpdateLocalOpAck(t *testing.T) {
	d, _ := upActive(t)
	j := rollingJob(ha.UpPrepare, 1)
	j.Kind, j.First, j.Second = ha.UpdateSequential, "a", "b"
	d.up.job = j
	d.tick(context.Background())
	waitFor(t, 2*time.Second, "Agent", func() bool { return len(agentOf(d).list()) == 1 })
	if got := agentOf(d).list(); got[0] != "install x1 true" {
		t.Fatalf("Agent: %v", got)
	}
	waitFor(t, 2*time.Second, "lokales Ergebnis", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.up.acks) == 1
	})
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpVerifyPrep {
		t.Fatalf("nach dem lokalen Ergebnis Prüfung erwartet: %+v", d.up.job)
	}
}

func TestUpdateRunOpMapping(t *testing.T) {
	d, _ := upActive(t)
	d.commit = "cur"
	j := rollingJob(ha.UpPrepare, 1)
	cases := []struct {
		op, commit string
		kind       ha.UpdateKind
		want       string // Agent-Aufruf, "" = keiner
		fail       bool
	}{
		{ha.OpInstall, "x1", ha.UpdateRolling, "install x1 false", false},
		{ha.OpInstallNoRestart, "x1", ha.UpdateSequential, "install x1 true", false},
		{ha.OpInstall, "x1", ha.UpdateSequential, "finish", false}, // gebaut in der Vorbereitung
		{ha.OpRollback, "o1", ha.UpdateRolling, "rollback o1 false", false},
		{ha.OpRollbackNoRestart, "o1", ha.UpdateSequential, "rollback o1 true", false},
		{ha.OpInstall, "cur", ha.UpdateRolling, "", false}, // läuft schon
		{ha.OpFreeze, "", ha.UpdateSequential, "", true},   // ohne Datenbank-Spiegel
		{ha.OpThaw, "", ha.UpdateRolling, "", true},
	}
	for _, c := range cases {
		before := len(agentOf(d).list())
		j.Kind, j.First, j.Second = c.kind, "b", "a"
		if c.kind == ha.UpdateSequential {
			j.First, j.Second = "a", "b" // a führt X zuerst aus
		}
		err := d.runUpdateOp(context.Background(), j, c.op, c.commit)
		calls := agentOf(d).list()[before:]
		if (err != nil) != c.fail {
			t.Errorf("%s %s: Fehler %v", c.op, c.commit, err)
		}
		if c.want == "" && len(calls) != 0 || c.want != "" && !slices.Equal(calls, []string{c.want}) {
			t.Errorf("%s %s (%s): Agent %v, erwartet %q", c.op, c.commit, c.kind, calls, c.want)
		}
	}
}

func TestUpdateRestartAbortBeforeLock(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	for _, err := range []error{
		st.saveNode(nodeConfig{ID: "a", Kind: "node", CAHolder: true}), st.saveCluster(duoCluster()),
		st.saveState(ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	j := rollingJob(ha.UpVerifyPrep, 6)
	j.PhaseAt = time.Now().Add(-time.Minute)
	if err := st.saveJob(j); err != nil {
		t.Fatal(err)
	}
	fn := newFakeNet()
	fn.setView("b", upPeerView("", ""))
	d := loadFakeDaemon(t, st, fn)
	d.booting = false
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpRollback || d.up.job.AbortedBy != "Neustart" {
		t.Fatalf("Neustart vor der Sperre: Zurückdrehen erwartet: %+v", d.up.job)
	}
	n := fn.messages("update_node")
	if len(n) != 1 || n[0].m.Op != ha.OpRollback || n[0].m.Commit != "o1" {
		t.Fatalf("rollback an b erwartet: %+v", n)
	}
	// in der Sperre geladen: kein Abbruch
	j2 := rollingJob(ha.UpVerifyNew, 6)
	j2.First, j2.Second = "a", "b"
	j2.LockedAt, j2.PhaseAt = time.Now(), time.Now()
	if err := st.saveJob(j2); err != nil {
		t.Fatal(err)
	}
	d2 := loadFakeDaemon(t, st, newFakeNet())
	d2.booting = false
	d2.tick(context.Background())
	if d2.up.job.Phase == ha.UpRollback || d2.up.job.AbortedBy != "" {
		t.Fatalf("Neustart in der Sperre darf nicht abbrechen: %+v", d2.up.job)
	}
}

func TestUpdateAbortNextStep(t *testing.T) {
	d, fn := upActive(t)
	if err := d.abortUpdate("Max"); err == nil {
		t.Fatal("Abbruch ohne Auftrag muss scheitern")
	}
	j := rollingJob(ha.UpCountdown, 3)
	j.PhaseAt, j.CountdownUntil = time.Now(), time.Now().Add(time.Hour)
	d.up.job = j
	if err := d.abortUpdate("Max"); err != nil {
		t.Fatal(err)
	}
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpRollback || d.up.job.AbortedBy != "Max" {
		t.Fatalf("Abbruch nicht ausgeführt: %+v", d.up.job)
	}
	if len(fn.messages("update_node")) != 1 {
		t.Fatal("Zurückdrehen an b erwartet")
	}
	d.tick(context.Background())
	if d.up.abort != "" {
		t.Fatal("Abbruch gilt nur für einen Schritt")
	}
}

func TestUpdateMarkFaultySaved(t *testing.T) {
	d, _ := upActive(t)
	j := rollingJob(ha.UpVerifyPrep, 3)
	j.PhaseAt = time.Now().Add(-11 * time.Minute) // Prüffrist 10 min überschritten
	d.up.job = j
	d.tick(context.Background())
	if !d.up.job.MarkFaulty {
		t.Fatalf("X fehlerhaft erwartet: %+v", d.up.job)
	}
	if !slices.Equal(d.cl.Faulty, []string{"x1"}) {
		t.Fatalf("Verbund-Einstellungen: %v", d.cl.Faulty)
	}
	if st := d.statusNow(); !slices.Equal(st.Faulty, []string{"x1"}) || st.Update.Phase != string(ha.UpRollback) {
		t.Fatalf("Status: %+v %+v", st.Faulty, st.Update)
	}
}

func TestUpdateVerifyFromEnv(t *testing.T) {
	t.Setenv("PDH_HA_UPDATE_VERIFY", "60s")
	d, _ := upActive(t)
	j := rollingJob(ha.UpVerifyPrep, 3)
	j.PhaseAt = time.Now().Add(-61 * time.Second)
	d.up.job = j
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpRollback {
		t.Fatalf("verkürzte Prüffrist nicht beachtet: %+v", d.up.job)
	}
}

func TestUpdateNodeOpResend(t *testing.T) {
	d, fn := upActive(t)
	wall := time.Now()
	d.wall = func() time.Time { return wall }
	d.up.job = rollingJob(ha.UpPrepare, 1)
	d.tick(context.Background())
	if len(fn.messages("update_node")) != 1 {
		t.Fatal("update_node erwartet")
	}
	wall = wall.Add(90 * time.Second)
	d.tick(context.Background())
	if len(fn.messages("update_node")) != 1 {
		t.Fatal("vor 2 min nicht wiederholen")
	}
	wall = wall.Add(31 * time.Second)
	d.tick(context.Background())
	n := fn.messages("update_node")
	if len(n) != 2 || n[1].m.Op != ha.OpInstall || n[1].m.Job.WaitSeq != d.up.job.WaitSeq {
		t.Fatalf("Wiederholung nach 2 min erwartet: %+v", n)
	}
}

func TestUpdateTakeoverOnlyForeign(t *testing.T) {
	locked := rollingJob(ha.UpLocked, 3)
	locked.LockedAt = time.Now()
	prep := rollingJob(ha.UpVerifyPrep, 3)
	cases := []struct {
		name          string
		job           ha.UpdateJob
		before, after uint64
		role          ha.Role
		swapped, want bool
	}{
		{"eigener Tausch in der Sperre", locked, 1, 2, ha.Active, true, false},
		{"Rücktausch im Zurücksetzen", func() ha.UpdateJob { j := locked; j.Phase = ha.UpRollback; return j }(), 2, 3, ha.Active, true, false},
		{"Übernahme per Stimme", locked, 1, 2, ha.Active, false, true},
		{"Tausch vor der Sperre (Hand)", prep, 1, 2, ha.Active, true, true},
		{"Übernahme vor der Sperre", prep, 1, 2, ha.Active, false, true},
		{"passiv bleibt", prep, 1, 2, ha.Passive, false, false},
		{"keine neue Amtszeit", prep, 2, 2, ha.Active, false, false},
		{"kein Auftrag", ha.UpdateJob{}, 1, 2, ha.Active, false, false},
		{"Auftrag beendet", func() ha.UpdateJob { j := prep; j.Phase = ha.UpDone; return j }(), 1, 2, ha.Active, false, false},
	}
	for _, c := range cases {
		if got := updateTakeover(c.job, c.before, c.after, c.role, c.swapped); got != c.want {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

func TestFrozenNodeDataCurrent(t *testing.T) {
	d, _ := upActive(t)
	d.dataCurrent = false
	j := rollingJob(ha.UpRollback, 3) // a ist Second: eingefroren
	j.LockedAt, j.FrozenSynced = time.Now(), true
	pg := &pgCtl{}
	for _, c := range []struct {
		phase ha.UpdatePhase
		self  string
		want  bool
	}{
		{ha.UpLocked, "a", true}, {ha.UpVerifyNew, "a", true}, {ha.UpRollback, "a", true},
		{ha.UpRelease, "a", false}, {ha.UpRollback, "b", false},
	} {
		j.Phase, j.Second = c.phase, c.self
		d.mu.Lock()
		d.up.job = j
		got := d.pgEnvLocked(pg).DataCurrent
		d.mu.Unlock()
		if got != c.want {
			t.Errorf("%s (Second %s): DataCurrent %v, erwartet %v", c.phase, c.self, got, c.want)
		}
	}
}

func TestForceAsyncWhileLocked(t *testing.T) {
	d, _, pa, now := replDaemon(t, trioCluster(), "pdh_ha_b|streaming|0/100|0/100|sync")
	tickFor(t, d, now, 3)
	if pa.syncTarget() != "pdh_ha_b" {
		t.Fatal("ohne Auftrag synchron erwartet")
	}
	j := rollingJob(ha.UpLocked, 3)
	j.LockedAt, j.PhaseAt, j.CmdAt = time.Now(), time.Now(), time.Now() // Tausch-Befehl gerade gesendet
	d.mu.Lock()
	d.up.job = j
	d.mu.Unlock()
	tickFor(t, d, now, 2)
	if pa.syncTarget() != "" {
		t.Fatal("während der Sperre asynchron erwartet")
	}
	d.mu.Lock()
	j = d.up.job
	j.Phase, j.ReleasedAt = ha.UpFollow, time.Now()
	d.up.job = j
	d.mu.Unlock()
	tickFor(t, d, now, 2)
	if pa.syncTarget() != "pdh_ha_b" {
		t.Fatal("nach der Freigabe wieder synchron erwartet")
	}
}

func TestStartUpdateLocked(t *testing.T) {
	d, fn := upActive(t)
	d.up.job = rollingJob(ha.UpDone, 9)
	j := rollingJob("", 0)
	j.ID = "j2"
	d.mu.Lock()
	err := d.startUpdateLocked(j)
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if d.up.job.ID != "j2" || d.up.job.Seq != 10 || d.up.job.Phase != ha.UpPrepare {
		t.Fatalf("neuer Auftrag: %+v", d.up.job)
	}
	if got, _ := d.st.loadJob(); got.ID != "j2" {
		t.Fatal("neuer Auftrag nicht gespeichert")
	}
	d.mu.Lock()
	err = d.startUpdateLocked(j)
	d.mu.Unlock()
	if err == nil {
		t.Fatal("zweiter Auftrag während eines offenen muss scheitern")
	}
	d.tick(context.Background())
	if m := fn.messages("update_job"); len(m) == 0 || m[0].m.Job.ID != "j2" {
		t.Fatalf("neuer Auftrag nicht gespiegelt: %+v", m)
	}
}

// --- Agent-Aufruf ---

// TestAgentHelperProcess ist das „Skript“ für TestAgentClientCmd.
func TestAgentHelperProcess(t *testing.T) {
	log := os.Getenv("PDH_TEST_AGENT_LOG")
	if log == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(3)
	}
	fmt.Fprintln(f, strings.Join(args, " "))
	f.Close()
	if len(args) > 1 && args[1] == "bad" {
		fmt.Println("Ziel nicht im Update-Zweig")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestAgentClientCmd(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	t.Setenv("PDH_TEST_AGENT_LOG", log)
	t.Setenv("PDH_HA_AGENT_CMD", "/usr/local/bin/lab-agent")
	if c, ok := newAgentClient().(cmdAgent); !ok || !slices.Equal(c.argv, []string{"/usr/local/bin/lab-agent"}) {
		t.Fatalf("PDH_HA_AGENT_CMD: Befehl erwartet: %#v", newAgentClient())
	}
	c := cmdAgent{argv: []string{os.Args[0], "-test.run=^TestAgentHelperProcess$", "--"}}
	ctx := context.Background()
	for _, err := range []error{
		c.Install(ctx, "x1", false), c.Install(ctx, "x1", true), c.Rollback(ctx, "o1", false),
		c.Rollback(ctx, "o1", true), c.Finish(ctx),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(log)
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r", "")), "\n")
	want := []string{"install x1", "install_norestart x1", "rollback o1", "rollback_norestart o1", "finish"}
	if !slices.Equal(got, want) {
		t.Fatalf("Argumente: %q, erwartet %q", got, want)
	}
	if err := c.Install(ctx, "bad", false); err == nil || !strings.Contains(err.Error(), "Ziel nicht im Update-Zweig") {
		t.Fatalf("Fehler mit Ausgabe erwartet: %v", err)
	}
}

func writeEnvFile(t *testing.T, lines ...string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_HA_PDH_ENV", p)
	t.Setenv("PDH_UPDATE_AGENT_URL", "")
	t.Setenv("PDH_UPDATE_AGENT_TOKEN", "")
	t.Setenv("PDH_SERVER_PORT", "")
	t.Setenv("PDH_HA_AGENT_CMD", "")
}

func TestAgentClientHTTP(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer geheim-0123456789" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/update":
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			bodies = append(bodies, b)
			polls = 0
			if b["commit"] == "busy" {
				http.Error(w, "update already running", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "GET /v1/status":
			polls++
			last := bodies[len(bodies)-1]
			_ = json.NewEncoder(w).Encode(map[string]any{"running": polls < 3, "success": last["commit"] != "fail", "output": "Bau fehlgeschlagen"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	writeEnvFile(t, "# Kommentar", "PDH_UPDATE_AGENT_URL="+srv.URL, `PDH_UPDATE_AGENT_TOKEN="geheim-0123456789"`)
	c, ok := newAgentClient().(*httpAgent)
	if !ok {
		t.Fatalf("HTTP-Agent erwartet: %#v", newAgentClient())
	}
	c.poll = 5 * time.Millisecond
	ctx := context.Background()
	if err := c.Install(ctx, "x1", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Rollback(ctx, "o1", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Install(ctx, "busy", false); err != nil {
		t.Fatalf("laufendes Update abwarten statt Fehler: %v", err)
	}
	if err := c.Install(ctx, "fail", false); err == nil || !strings.Contains(err.Error(), "Bau fehlgeschlagen") {
		t.Fatalf("Fehler des Agenten erwartet: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []map[string]any{
		{"commit": "x1", "no_restart": true},
		{"rollback": "o1", "no_restart": true},
		{"finish": true},
		{"commit": "busy"},
		{"commit": "fail"},
	}
	if len(bodies) != len(want) {
		t.Fatalf("Rümpfe: %v", bodies)
	}
	for i := range want {
		if fmt.Sprint(bodies[i]) != fmt.Sprint(want[i]) {
			t.Errorf("Rumpf %d: %v, erwartet %v", i, bodies[i], want[i])
		}
	}
	// ohne Token: Fehler ohne Anfrage
	writeEnvFile(t, "PDH_UPDATE_AGENT_URL="+srv.URL)
	if err := newAgentClient().Install(ctx, "x1", false); err == nil {
		t.Fatal("ohne Token muss der Aufruf scheitern")
	}
}

func TestPDHHealthOK(t *testing.T) {
	var resp string
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, resp)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	writeEnvFile(t, "PDH_SERVER_PORT="+u.Port())
	ctx := context.Background()
	cases := []struct {
		code int
		body string
		want bool
	}{
		{200, `{"status":"ok","version":"0.9.1","migrations_ok":true}`, true},
		{200, `{"status":"ok","version":"0.9.1"}`, true}, // ohne Feld zählt nur die Version
		{200, `{"status":"ok","version":"0.9.1","migrations_ok":false}`, false},
		{200, `{"status":"ok","version":"0.9.0","migrations_ok":true}`, false},
		{503, `db error`, false},
	}
	for _, c := range cases {
		code, resp = c.code, c.body
		if got := pdhHealthOK(ctx, "0.9.1"); got != c.want {
			t.Errorf("%d %s: %v, erwartet %v", c.code, c.body, got, c.want)
		}
	}
}

func TestHeartbeatPDHField(t *testing.T) {
	d, _ := upActive(t)
	d.up.job = rollingJob(ha.UpPrepare, 1)
	d.up.healthFn = func(context.Context) (pdhHealth, error) {
		return pdhHealth{Status: 200, Version: "0.9.1"}, nil
	}
	d.tick(context.Background())
	waitFor(t, 2*time.Second, "Abfrage /health", func() bool { return d.up.health.Load() != nil && !d.up.healthBusy.Load() })
	d.mu.Lock()
	hb := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if hb.PDH != "ok:0.9.1" {
		t.Fatalf("Heartbeat pdh: %q", hb.PDH)
	}
}

func TestUpdateDBOpsOnPassiveOnly(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 2}, fn)
	r := (&fakeRunner{}).on("runuser", statusKey, stStandby, nil)
	pa, _ := testActor(t, r)
	d.act = pa
	ctx := context.Background()
	j := rollingJob(ha.UpLocked, 3)
	j.Kind = ha.UpdateSequential
	if err := d.runUpdateOp(ctx, j, ha.OpFreeze, ""); err != nil {
		t.Fatal(err)
	}
	if !has(callStrings(r), "pg_ctlcluster 18 main stop") {
		t.Fatalf("Einfrieren stoppt PostgreSQL: %v", callStrings(r))
	}
	// Zurückdrehen: erst Wiedereingliederung (folgt schon: nur Dienste aus), dann Agent
	if err := d.runUpdateOp(ctx, j, ha.OpRollback, "o1"); err != nil {
		t.Fatal(err)
	}
	if got := agentOf(d).list(); !slices.Equal(got, []string{"rollback o1 false"}) {
		t.Fatalf("Agent: %v", got)
	}
	// am aktiven Server nie
	d.mu.Lock()
	d.p.Role = ha.Active
	d.mu.Unlock()
	before := len(callStrings(r))
	for _, op := range []string{ha.OpFreeze, ha.OpThaw, ha.OpReinit} {
		if err := d.runUpdateOp(ctx, j, op, "x1"); err == nil || !strings.Contains(err.Error(), "aktiv") {
			t.Errorf("%s am aktiven Server: %v", op, err)
		}
	}
	if after := callStrings(r); len(after) != before {
		t.Fatalf("Datenbank am aktiven Server angefasst: %v", after[before:])
	}
}

func callStrings(r *fakeRunner) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, c.String())
	}
	return out
}

// Beendet sich der Dienst während eines Auftrags (das eigene Einspielen
// startet pdh-ha neu), ist das kein Ergebnis: kein update_ack, nichts gemerkt.
func TestUpdateNodeOpCancelledNoAck(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	agentOf(d).block = make(chan struct{})
	job := rollingJob(ha.UpPrepare, 3)
	job.WaitOp, job.WaitNode, job.WaitSeq = ha.OpInstall, "b", 3
	ctx, cancel := context.WithCancel(context.Background())
	d.handle(ctx, inbound{From: "a", Msg: message{T: "update_node", Op: ha.OpInstall, Commit: "x1", JobID: "j1", Job: &job, Seq: 1}})
	waitFor(t, 2*time.Second, "Agent-Aufruf", func() bool { return len(agentOf(d).list()) == 1 })
	cancel()
	waitFor(t, 2*time.Second, "Ende des Laufs", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.up.running) == 0
	})
	time.Sleep(50 * time.Millisecond)
	if n := len(fn.messages("update_ack")); n != 0 {
		t.Fatalf("abgebrochener Lauf als Ergebnis gemeldet: %+v", fn.messages("update_ack"))
	}
	d.mu.Lock()
	cached := len(d.up.done)
	d.mu.Unlock()
	if cached != 0 {
		t.Fatal("abgebrochener Lauf gemerkt")
	}
	if hasEvent(d, "fehlgeschlagen") {
		t.Fatal("Abbruch beim Beenden als Fehler gemeldet")
	}
	// Wiederholung nach dem Neustart läuft erneut
	agentOf(d).block = nil
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_node", Op: ha.OpInstall, Commit: "x1", JobID: "j1", Job: &job, Seq: 2}})
	waitFor(t, 2*time.Second, "update_ack nach Wiederholung", func() bool { return len(fn.messages("update_ack")) == 1 })
	if a := fn.messages("update_ack")[0].m.Ack; !a.OK {
		t.Fatalf("Wiederholung: %+v", a)
	}
}

func TestUpdateJobMirrorNewJobReplacesEnded(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	old := rollingJob(ha.UpDone, 50)
	old.ID = "alt"
	d.up.job = old
	j := rollingJob(ha.UpPrepare, 3) // neuer Auftrag, Seq des Partners liegt zurück
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_job", Job: &j, Seq: 1}})
	if d.up.job.ID != "j1" || d.up.job.Seq != 3 {
		t.Fatalf("neuer offener Auftrag nicht übernommen: %+v", d.up.job)
	}
	if got, _ := d.st.loadJob(); got.ID != "j1" {
		t.Fatal("neuer Auftrag nicht gespeichert")
	}
	// ein anderer, beendeter Auftrag mit kleinerem Seq ersetzt den offenen nicht
	other := rollingJob(ha.UpFailed, 2)
	other.ID = "anders"
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_job", Job: &other, Seq: 2}})
	if d.up.job.ID != "j1" {
		t.Fatalf("offener Auftrag ersetzt: %+v", d.up.job)
	}
	// ein anderer, offener Auftrag ersetzt einen offenen nur mit höherem Seq
	open2 := rollingJob(ha.UpPrepare, 2)
	open2.ID = "zwei"
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "update_job", Job: &open2, Seq: 3}})
	if d.up.job.ID != "j1" {
		t.Fatalf("offener Auftrag durch kleineren Seq ersetzt: %+v", d.up.job)
	}
}

func TestStartUpdateSeqAbovePartner(t *testing.T) {
	d, _ := upActive(t)
	d.up.job = rollingJob(ha.UpDone, 4)
	in := rollingJob(ha.UpDone, 40) // Partner schickte einmal Seq 40
	d.mu.Lock()
	d.mirrorInLocked(&plan{}, in)
	d.up.job = rollingJob(ha.UpDone, 4)
	j := rollingJob("", 0)
	j.ID = "neu"
	err := d.startUpdateLocked(j)
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if d.up.job.Seq != 41 {
		t.Fatalf("Seq über dem des Partners erwartet: %d", d.up.job.Seq)
	}
}

// Zurücksetzen/Prüfen warten auf die eigene hochgestufte Datenbank: ohne
// Spiegel nie, sonst bis die letzte Abfrage einen schreibbaren Primary zeigt.
func TestSelfDBPending(t *testing.T) {
	d, _ := upActive(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.selfDBPendingLocked() {
		t.Fatal("ohne Datenbank-Spiegel darf nichts warten")
	}
	pa, _ := testActor(t, &fakeRunner{})
	d.act = pa
	for _, c := range []struct {
		st   *pgStatus
		want bool
	}{
		{nil, true},
		{&pgStatus{State: "stopped"}, true},
		{&pgStatus{State: "standby"}, true},
		{&pgStatus{State: "primary", ReadOnly: true}, true},
		{&pgStatus{State: "primary"}, false},
	} {
		d.pgSt.Store(c.st)
		if got := d.selfDBPendingLocked(); got != c.want {
			t.Errorf("%+v: %v, will %v", c.st, got, c.want)
		}
	}
}
