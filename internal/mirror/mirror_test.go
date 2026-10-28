package mirror

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ Prober = (*pgxpool.Pool)(nil)

// reply: eine Antwort des Fake-Probers; err gesetzt = Fehler, sonst Werte.
type reply struct {
	vals []any
	err  error
}

type fakeRow struct{ r reply }

func (f fakeRow) Scan(dest ...any) error {
	if f.r.err != nil {
		return f.r.err
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *bool:
			*p = f.r.vals[i].(bool)
		case *pgtype.Interval:
			*p, _ = f.r.vals[i].(pgtype.Interval)
		case *pgtype.Timestamptz:
			*p, _ = f.r.vals[i].(pgtype.Timestamptz)
		}
	}
	return nil
}

// fakeProber liefert die Antworten der Reihe nach, danach immer die letzte.
type fakeProber struct {
	mu      sync.Mutex
	replies []reply
	n       int
}

func (f *fakeProber) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.n
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	f.n++
	return fakeRow{f.replies[i]}
}

func modeReply(rec, ro bool) reply { return reply{vals: []any{rec, ro}} }

var errDB = errors.New("verbindung weg")

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		r    reply
		want Mode
		err  bool
	}{
		{"normal", modeReply(false, false), Normal, false},
		{"recovery", modeReply(true, false), ReadOnly, false},
		{"readonly", modeReply(false, true), ReadOnly, false},
		{"fehler", reply{err: errDB}, Normal, true},
	}
	for _, c := range cases {
		got, err := Detect(context.Background(), &fakeProber{replies: []reply{c.r}})
		if (err != nil) != c.err {
			t.Fatalf("%s: err=%v", c.name, err)
		}
		if !c.err && got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestModeString(t *testing.T) {
	if Normal.String() != "normal" || ReadOnly.String() != "spiegel" {
		t.Fatal("String()")
	}
}

func TestWatchChange(t *testing.T) {
	p := &fakeProber{replies: []reply{modeReply(true, false), modeReply(true, false), modeReply(false, false)}}
	var reasons []string
	Watch(context.Background(), p, ReadOnly, time.Millisecond, time.Second, func(r string) { reasons = append(reasons, r) })
	if len(reasons) != 1 || !strings.Contains(reasons[0], "spiegel → normal") {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestWatchDBLost(t *testing.T) {
	p := &fakeProber{replies: []reply{{err: errDB}}}
	var reasons []string
	Watch(context.Background(), p, ReadOnly, time.Millisecond, 5*time.Millisecond, func(r string) { reasons = append(reasons, r) })
	if len(reasons) != 1 || !strings.Contains(reasons[0], "Datenbank") {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestWatchShortErrorsNoChange(t *testing.T) {
	p := &fakeProber{replies: []reply{{err: errDB}, {err: errDB}, modeReply(true, false)}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	calls := 0
	Watch(ctx, p, ReadOnly, time.Millisecond, time.Second, func(string) { calls++ })
	if calls != 0 {
		t.Fatalf("onChange %d mal", calls)
	}
}

func TestWatchLostZeroNeverEnds(t *testing.T) {
	p := &fakeProber{replies: []reply{{err: errDB}}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	calls := 0
	Watch(ctx, p, ReadOnly, time.Millisecond, 0, func(string) { calls++ })
	if calls != 0 {
		t.Fatalf("onChange %d mal", calls)
	}
}

func TestReplayLagNull(t *testing.T) {
	p := &fakeProber{replies: []reply{{vals: []any{pgtype.Interval{}, pgtype.Timestamptz{}}}}}
	lag, at, err := ReplayLag(context.Background(), p)
	if err != nil || !at.IsZero() || lag != 0 {
		t.Fatalf("lag=%v at=%v err=%v", lag, at, err)
	}
}

func TestReplayLagValue(t *testing.T) {
	d := 3 * time.Second
	ts := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	p := &fakeProber{replies: []reply{{vals: []any{pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}, pgtype.Timestamptz{Time: ts, Valid: true}}}}}
	lag, at, err := ReplayLag(context.Background(), p)
	if err != nil || lag != d || !at.Equal(ts) {
		t.Fatalf("lag=%v at=%v err=%v", lag, at, err)
	}
}

// hangProber blockiert, bis der Kontext der Abfrage endet (halbtote Verbindung).
type hangProber struct{}

type hangRow struct{ ctx context.Context }

func (r hangRow) Scan(dest ...any) error { <-r.ctx.Done(); return r.ctx.Err() }

func (hangProber) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return hangRow{ctx}
}

func TestWatchHangingProbeCountsAsLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var reasons []string
	Watch(ctx, hangProber{}, ReadOnly, 5*time.Millisecond, 20*time.Millisecond, func(r string) { reasons = append(reasons, r) })
	if len(reasons) != 1 || !strings.Contains(reasons[0], "Datenbank") {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestWatchFailureClockResets(t *testing.T) {
	ok := modeReply(true, false)
	p := &fakeProber{replies: []reply{{err: errDB}, {err: errDB}, ok, {err: errDB}, {err: errDB}, ok}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	calls := 0
	Watch(ctx, p, ReadOnly, 10*time.Millisecond, 25*time.Millisecond, func(string) { calls++ })
	if calls != 0 {
		t.Fatalf("onChange %d mal", calls)
	}
}

func TestWatchEveryZeroNoPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	Watch(ctx, &fakeProber{replies: []reply{modeReply(true, false)}}, ReadOnly, 0, time.Second, func(string) { t.Fatal("onChange") })
}

// sqlProber merkt sich die Abfrage und liefert eine Antwort.
type sqlProber struct {
	r   reply
	sql string
}

func (s *sqlProber) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	s.sql = sql
	return fakeRow{s.r}
}

func TestCurrent(t *testing.T) {
	for _, want := range []bool{true, false} {
		p := &sqlProber{r: reply{vals: []any{want}}}
		got, err := Current(context.Background(), p)
		if err != nil || got != want {
			t.Fatalf("%v: got=%v err=%v", want, got, err)
		}
		// Empfänger streamt, Meldung frisch, alles Empfangene eingespielt; ohne Zeile → false
		for _, part := range []string{"pg_stat_wal_receiver", "'streaming'", "last_msg_receipt_time", "pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn()", "COALESCE("} {
			if !strings.Contains(p.sql, part) {
				t.Errorf("Abfrage ohne %q: %s", part, p.sql)
			}
		}
	}
	if got, err := Current(context.Background(), &sqlProber{r: reply{err: errDB}}); err == nil || got {
		t.Fatalf("Fehler: got=%v err=%v", got, err)
	}
}
