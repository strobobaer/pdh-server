package netsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeSink zählt Aufrufe und legt keine Tickets an. Die Ticket-Nummern sind
// echte, minimale Ticketzeilen, damit der Fremdschlüssel hält.
type fakeSink struct {
	f        *fx
	open     map[string]bool // ticketID -> offen
	opened   []AlarmAsset
	repeated []AlarmAsset
	recov    []AlarmAsset
	afters   int
	panicOn  string // Name des Assets, bei dem OpenTicket abstürzt
	made     []string
	prefix   string // gesetzt: Aufrufe für Assets ohne dieses Namenspräfix schlagen fehl
	badID    bool   // OpenTicket liefert eine ungültige Ticket-ID (Fehler beim Speichern)
}

func (s *fakeSink) guard(name string) error {
	if s.prefix != "" && !strings.HasPrefix(name, s.prefix) {
		return errors.New("fremdes Asset " + name)
	}
	return nil
}

func (s *fakeSink) OpenTicket(ctx context.Context, tx pgx.Tx, a AlarmAsset) (string, func(), error) {
	if err := s.guard(a.Name); err != nil {
		return "", nil, err
	}
	if a.Name == s.panicOn {
		panic("Senke abgestürzt")
	}
	if s.badID {
		return "kein-uuid", func() { s.afters++ }, nil
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO tickets (title, created_by) VALUES ($1, $2::uuid) RETURNING id::text`,
		"test-"+a.Name, s.f.user).Scan(&id); err != nil {
		return "", nil, err
	}
	s.made = append(s.made, id)
	s.open[id] = true
	s.opened = append(s.opened, a)
	return id, func() { s.afters++ }, nil
}
func (s *fakeSink) TicketOpen(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	return s.open[id], nil
}
func (s *fakeSink) Repeat(ctx context.Context, tx pgx.Tx, a AlarmAsset) (func(), error) {
	if err := s.guard(a.Name); err != nil {
		return nil, err
	}
	s.repeated = append(s.repeated, a)
	return func() { s.afters++ }, nil
}
func (s *fakeSink) Recovered(ctx context.Context, tx pgx.Tx, a AlarmAsset, at time.Time) (func(), error) {
	if err := s.guard(a.Name); err != nil {
		return nil, err
	}
	s.recov = append(s.recov, a)
	return func() { s.afters++ }, nil
}

func newWatch(t *testing.T) (*fx, *fakeSink, *Watch) {
	f := newFx(t)
	sink := &fakeSink{f: f, open: map[string]bool{}}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.s.DB.Exec(c, `DELETE FROM it_assets WHERE net_source = $1`, f.src)
		for _, id := range sink.made {
			_, _ = f.s.DB.Exec(c, `DELETE FROM tickets WHERE id = $1::uuid`, id)
		}
	})
	return f, sink, &Watch{DB: f.s.DB, Sink: sink, Source: f.src, Grace: func(context.Context) time.Duration { return 5 * time.Minute }}
}

// mk legt ein überwachtes Asset an.
func (f *fx) mk(ext, state string, since time.Time) string {
	f.t.Helper()
	var id string
	must(f.t, f.s.DB.QueryRow(context.Background(), `INSERT INTO it_assets (name, type, status, created_by, net_source, net_external_id, net_role, net_monitored, net_state, net_state_since)
		VALUES ($1, 'network', 'active', $2::uuid, $3, $4, 'device', true, $5, $6) RETURNING id::text`,
		"w-"+f.src+"-"+ext, f.user, f.src, ext, state, since).Scan(&id))
	return id
}

func (f *fx) alarmState(id string) (state string, since *time.Time, ticket *string) {
	f.t.Helper()
	err := f.s.DB.QueryRow(context.Background(), `SELECT state, offline_since, ticket_id::text FROM it_net_alarm_state WHERE asset_id = $1::uuid`, id).Scan(&state, &since, &ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		return "normal", nil, nil
	}
	must(f.t, err)
	return
}

func TestWatchGrace(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(4*time.Minute)))
	if len(sink.opened) != 0 {
		t.Fatal("zu früh")
	}
	must(t, w.Pass(ctx, t0.Add(5*time.Minute)))
	if len(sink.opened) != 1 || sink.afters != 1 {
		t.Fatalf("opened=%d afters=%d", len(sink.opened), sink.afters)
	}
	st, since, tk := f.alarmState(id)
	if st != "alarm" || since == nil || !since.Equal(t0) || tk == nil {
		t.Errorf("Zustand %s %v %v", st, since, tk)
	}
	if a := sink.opened[0]; a.ID != id || a.OfflineSince.IsZero() {
		t.Errorf("AlarmAsset %+v", a)
	}
	must(t, w.Pass(ctx, t0.Add(6*time.Minute)))
	if len(sink.opened) != 1 || len(sink.repeated) != 0 {
		t.Error("zweiter Pass darf nichts tun")
	}
}

func TestWatchUnknownNoAlarm(t *testing.T) {
	f, sink, w := newWatch(t)
	f.mk("a", "unknown", t0)
	must(t, w.Pass(context.Background(), t0.Add(time.Hour)))
	if len(sink.opened) != 0 {
		t.Fatal("unknown löst keinen Alarm aus")
	}
}

func TestWatchRecovered(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_state='online', net_state_since=$2 WHERE id=$1::uuid`, id, t0.Add(12*time.Minute))
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(13*time.Minute)))
	if len(sink.recov) != 1 || sink.recov[0].TicketID == "" {
		t.Fatalf("recov %+v", sink.recov)
	}
	st, _, tk := f.alarmState(id)
	if st != "normal" || tk == nil {
		t.Errorf("Zustand %s ticket %v", st, tk)
	}
	must(t, w.Pass(ctx, t0.Add(14*time.Minute)))
	if len(sink.recov) != 1 {
		t.Error("Meldung nur einmal")
	}
}

func TestWatchRepeatOnOpenTicket(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_state='online', net_state_since=$2 WHERE id=$1::uuid`, id, t0.Add(11*time.Minute))
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(12*time.Minute)))
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_state='offline', net_state_since=$2 WHERE id=$1::uuid`, id, t0.Add(20*time.Minute))
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(26*time.Minute)))
	if len(sink.opened) != 1 || len(sink.repeated) != 1 || sink.repeated[0].TicketID == "" {
		t.Fatalf("opened=%d repeated=%d", len(sink.opened), len(sink.repeated))
	}
	if st, _, _ := f.alarmState(id); st != "alarm" {
		t.Error("Zustand alarm erwartet")
	}
}

func TestWatchNewTicketWhenClosed(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	first := sink.made[0]
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_state='online', net_state_since=$2 WHERE id=$1::uuid`, id, t0.Add(11*time.Minute))
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(12*time.Minute)))
	sink.open[first] = false
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_state='offline', net_state_since=$2 WHERE id=$1::uuid`, id, t0.Add(20*time.Minute))
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(26*time.Minute)))
	if len(sink.opened) != 2 || len(sink.repeated) != 0 {
		t.Fatalf("opened=%d repeated=%d", len(sink.opened), len(sink.repeated))
	}
	if _, _, tk := f.alarmState(id); tk == nil || *tk != sink.made[1] {
		t.Errorf("neue Ticket-ID erwartet: %v", tk)
	}
}

func TestWatchRetiredSilencesAlarm(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET status='retired' WHERE id=$1::uuid`, id)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(11*time.Minute)))
	st, since, tk := f.alarmState(id)
	if st != "normal" || since != nil || tk == nil {
		t.Errorf("Zustand %s %v %v", st, since, tk)
	}
	if len(sink.recov) != 0 {
		t.Error("kein Wieder-erreichbar-Eintrag")
	}
}

func TestWatchMonitoringOff(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_monitored=false WHERE id=$1::uuid`, id)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(time.Hour)))
	if len(sink.opened) != 0 {
		t.Fatal("ohne Überwachung kein Alarm")
	}
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_monitored=true WHERE id=$1::uuid`, id)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(time.Hour)))
	if st, _, _ := f.alarmState(id); len(sink.opened) != 1 || st != "alarm" {
		t.Fatalf("Alarm erwartet: opened=%d state=%s", len(sink.opened), st)
	}
	afters := sink.afters
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_monitored=false WHERE id=$1::uuid`, id)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(time.Hour)))
	if st, _, _ := f.alarmState(id); st != "normal" {
		t.Error("Alarm muss still enden")
	}
	if len(sink.opened) != 1 || len(sink.repeated) != 0 || len(sink.recov) != 0 || sink.afters != afters {
		t.Error("Stilllegen darf die Senke nicht aufrufen")
	}
}

func TestWatchInactiveSilencesAlarm(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET status='inactive' WHERE id=$1::uuid`, id)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(11*time.Minute)))
	if st, _, _ := f.alarmState(id); st != "normal" || len(sink.recov) != 0 {
		t.Errorf("Zustand %s recov %d", st, len(sink.recov))
	}
}

// Ein vom Netz gelöstes Asset (Quelle NULL) wird nur bei leerer Source erfasst.
// Die Senke verweigert fremde Assets, damit der Durchlauf über alle Quellen
// keine fremden Zeilen verändert (Fehler wird je Asset isoliert).
func TestWatchDetachedSilencesAlarm(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_source=NULL, net_external_id=NULL WHERE id=$1::uuid`, id)
	must(t, err)
	w.Source = f.src
	must(t, w.Pass(ctx, t0.Add(11*time.Minute)))
	if st, _, _ := f.alarmState(id); st != "alarm" {
		t.Fatal("mit Source darf das gelöste Asset nicht erfasst werden")
	}
	w.Source = ""
	sink.prefix = "w-" + f.src
	must(t, w.Pass(ctx, t0.Add(12*time.Minute)))
	if st, _, tk := f.alarmState(id); st != "normal" || tk == nil {
		t.Errorf("Zustand %s %v", st, tk)
	}
	if len(sink.recov) != 0 {
		t.Error("kein Wieder-erreichbar-Eintrag")
	}
}

func TestWatchGraceFallback(t *testing.T) {
	for name, g := range map[string]func(context.Context) time.Duration{
		"null":  func(context.Context) time.Duration { return 0 },
		"nil":   nil,
		"minus": func(context.Context) time.Duration { return -time.Minute },
	} {
		t.Run(name, func(t *testing.T) {
			f, sink, w := newWatch(t)
			w.Grace = g
			f.mk("a", "offline", t0)
			must(t, w.Pass(context.Background(), t0.Add(4*time.Minute)))
			if len(sink.opened) != 0 {
				t.Fatal("vor 5 Minuten kein Alarm")
			}
			must(t, w.Pass(context.Background(), t0.Add(5*time.Minute)))
			if len(sink.opened) != 1 {
				t.Fatalf("nach 5 Minuten ein Alarm, war %d", len(sink.opened))
			}
		})
	}
}

func TestWatchAfterNotRunOnRollback(t *testing.T) {
	f, sink, w := newWatch(t)
	sink.badID = true
	id := f.mk("a", "offline", t0)
	must(t, w.Pass(context.Background(), t0.Add(10*time.Minute)))
	if sink.afters != 0 {
		t.Error("after darf nach Rollback nicht laufen")
	}
	if st, _, _ := f.alarmState(id); st != "normal" {
		t.Error("Zustand muss normal bleiben")
	}
}

func TestWatchSinkPanicIsolated(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	bad := f.mk("bad", "offline", t0)
	good := f.mk("good", "offline", t0)
	sink.panicOn = "w-" + f.src + "-bad"
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	if len(sink.opened) != 1 || sink.opened[0].ID != good {
		t.Fatalf("opened=%v", sink.opened)
	}
	if st, _, _ := f.alarmState(bad); st != "normal" {
		t.Error("abgestürztes Asset bleibt normal (Rollback)")
	}
	if st, _, _ := f.alarmState(good); st != "alarm" {
		t.Error("anderes Asset muss alarmieren")
	}
}
