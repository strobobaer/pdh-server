package ruesten

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overdueEntry startet eine Rüstzeit und rückt den Beginn um ago zurück.
// Höchstdauer 10 min, Karenz 5 min.
func (f *fx) overdueEntry(ago time.Duration) (*Entry, string) {
	f.t.Helper()
	infra, btn, usr := f.infra("mon", nil), f.button("M"), f.user(false)
	e, err := f.s.Start(f.ctx, infra, btn.ID, usr, OriginEasy)
	must(f.t, err)
	_, err = f.s.DB.Exec(f.ctx, `UPDATE setup_times SET max_minutes = 10, grace_minutes = 5,
		started_at = now() - make_interval(secs => $2) WHERE id = $1`, e.ID, ago.Seconds())
	must(f.t, err)
	return e, usr
}

// creator ist eine Attrappe, die Aufrufe zählt und echte Störungen anlegt.
func (f *fx) creator(usr string, calls *int32, fail bool) FaultCreator {
	return func(_ context.Context, e Entry) (string, error) {
		atomic.AddInt32(calls, 1)
		if fail {
			return "", errors.New("kaputt")
		}
		var id string
		must(f.t, f.s.DB.QueryRow(f.ctx, `INSERT INTO faults (title, created_by) VALUES ('Störung durch Rüsten', $1) RETURNING id`, usr).Scan(&id))
		f.t.Cleanup(func() { f.s.DB.Exec(f.ctx, `DELETE FROM faults WHERE id = $1`, id) })
		return id, nil
	}
}

func TestMonitorBeforeGraceNothing(t *testing.T) {
	f := newFx(t)
	e, usr := f.overdueEntry(14 * time.Minute) // < 15 min
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, false), SystemUser: usr}
	n, err := m.Pass(f.ctx)
	must(t, err)
	g, _ := f.s.Get(f.ctx, e.ID)
	if n != 0 || calls != 0 || g.Status != StatusRunning {
		t.Fatalf("n=%d calls=%d status=%s", n, calls, g.Status)
	}
}

func TestMonitorCreatesOneFault(t *testing.T) {
	f := newFx(t)
	e, usr := f.overdueEntry(16 * time.Minute)
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, false), SystemUser: usr}
	before := time.Now()
	n, err := m.Pass(f.ctx)
	must(t, err)
	g, err := f.s.Get(f.ctx, e.ID)
	must(t, err)
	if n != 1 || calls != 1 || g.Status != StatusFault || g.FaultID == nil || g.EndedAt == nil {
		t.Fatalf("n=%d calls=%d entry=%+v", n, calls, g)
	}
	if g.EndedAt.Before(before.Add(-time.Second)) || g.EndedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("Ende %v nicht beim Erzeugen", g.EndedAt)
	}
	var msg string
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT message FROM record_history WHERE ref_type = 'setup' AND ref_id = $1 AND action = 'fault'`, e.ID).Scan(&msg))
	if !strings.Contains(msg, "Störung durch Rüsten") || !strings.Contains(msg, "10 min") || !strings.Contains(msg, "5 min") {
		t.Fatalf("Verlauf: %q", msg)
	}
}

func TestMonitorTwoPassesOneFault(t *testing.T) {
	f := newFx(t)
	_, usr := f.overdueEntry(16 * time.Minute)
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, false), SystemUser: usr}
	_, err := m.Pass(f.ctx)
	must(t, err)
	n, err := m.Pass(f.ctx)
	must(t, err)
	if n != 0 || calls != 1 {
		t.Fatalf("zweiter Durchlauf n=%d calls=%d", n, calls)
	}
}

func TestMonitorConcurrentPassesOneFault(t *testing.T) {
	f := newFx(t)
	e, usr := f.overdueEntry(16 * time.Minute)
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, false), SystemUser: usr}
	var wg sync.WaitGroup
	var total int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := m.Pass(f.ctx)
			if err != nil {
				t.Errorf("Pass: %v", err)
			}
			atomic.AddInt32(&total, int32(n))
		}()
	}
	wg.Wait()
	var faults int
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'setup' AND ref_id = $1 AND action = 'fault'`, e.ID).Scan(&faults))
	if calls != 1 || total != 1 || faults != 1 {
		t.Fatalf("calls=%d angelegt=%d Verlauf=%d", calls, total, faults)
	}
}

func TestMonitorCreatorFailsStaysRunning(t *testing.T) {
	f := newFx(t)
	e, usr := f.overdueEntry(16 * time.Minute)
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, true), SystemUser: usr}
	n, err := m.Pass(f.ctx)
	if err == nil || n != 0 {
		t.Fatalf("Fehler erwartet: n=%d err=%v", n, err)
	}
	g, _ := f.s.Get(f.ctx, e.ID)
	if g.Status != StatusRunning || g.EndedAt != nil || g.FaultID != nil {
		t.Fatalf("muss laufen: %+v", g)
	}
	// Beim nächsten Durchlauf wird erneut versucht.
	m.CreateFault = f.creator(usr, &calls, false)
	n, err = m.Pass(f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("zweiter Versuch n=%d", n)
	}
}

func TestMonitorSkip(t *testing.T) {
	f := newFx(t)
	e, usr := f.overdueEntry(16 * time.Minute)
	var calls int32
	m := &Monitor{Store: f.s, CreateFault: f.creator(usr, &calls, false), Skip: func() bool { return true }, SystemUser: usr}
	n, err := m.Pass(f.ctx)
	must(t, err)
	g, _ := f.s.Get(f.ctx, e.ID)
	if n != 0 || calls != 0 || g.Status != StatusRunning {
		t.Fatalf("n=%d calls=%d status=%s", n, calls, g.Status)
	}
}
