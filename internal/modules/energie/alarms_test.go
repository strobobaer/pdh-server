package energie

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// alarmUser ist der Systembenutzer aus den Migrationen (Ersteller der Test-Tickets).
const alarmUser = "00000000-0000-4000-8000-00000000d0d1"

// wNow ist die feste Uhrzeit der Wächter-Tests: 10 s nach dem Sampler-Lauf um 12:00.
var wNow = time.Date(2026, 11, 15, 12, 0, 15, 0, time.UTC)

// fakeSink zählt die Aufrufe je Schwellwert und legt echte Tickets an
// (energy_threshold_state.ticket_id verweist auf tickets).
type fakeSink struct {
	mu      sync.Mutex
	opens   map[string]int
	repeats map[string]int
	afters  map[string]int
	at      map[string][]time.Time
	repTkt  map[string][]string
	fail    map[string]bool
	tickets []string // angelegte Tickets, nach dem Test gelöscht
}

func newFakeSink(t *testing.T, s *Store) *fakeSink {
	f := &fakeSink{opens: map[string]int{}, repeats: map[string]int{}, afters: map[string]int{},
		at: map[string][]time.Time{}, repTkt: map[string][]string{}, fail: map[string]bool{}}
	t.Cleanup(func() {
		f.mu.Lock()
		ids := f.tickets
		f.mu.Unlock()
		if len(ids) > 0 {
			_, _ = s.DB.Exec(context.Background(), `DELETE FROM tickets WHERE id = ANY($1::uuid[])`, ids)
		}
	})
	return f
}

func (f *fakeSink) after(id string) func() {
	return func() { f.mu.Lock(); f.afters[id]++; f.mu.Unlock() }
}

func (f *fakeSink) OpenTicket(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[ev.Threshold.ID] {
		return "", nil, errors.New("Ticket kaputt")
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO tickets (title, description, created_by) VALUES ('Alarm-Test', '', $1) RETURNING id::text`,
		alarmUser).Scan(&id); err != nil {
		return "", nil, err
	}
	f.tickets = append(f.tickets, id)
	f.opens[ev.Threshold.ID]++
	f.at[ev.Threshold.ID] = append(f.at[ev.Threshold.ID], ev.At)
	return id, f.after(ev.Threshold.ID), nil
}

func (f *fakeSink) TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT status IN ('open','in_progress','pending') AND archived_at IS NULL
		FROM tickets WHERE id = $1::uuid`, ticketID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return open, err
}

func (f *fakeSink) Repeat(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repeats[ev.Threshold.ID]++
	f.at[ev.Threshold.ID] = append(f.at[ev.Threshold.ID], ev.At)
	f.repTkt[ev.Threshold.ID] = append(f.repTkt[ev.Threshold.ID], ev.TicketID)
	return f.after(ev.Threshold.ID), nil
}

// watchEnv legt eine Messstelle (Momentanwert) mit einem Schwellwert „über 10“
// an, Zeitraum 1 min, Rückschaltzeit 15 min.
func watchEnv(t *testing.T) (*Store, *Threshold, *fakeSink, *AlarmWatch) {
	t.Helper()
	samplerEnv(t) // Reste früherer Läufe weg
	s, meter := thresholdMeter(t)
	th := okThreshold(meter)
	th.WindowMinutes = 1
	must(t, s.SaveThreshold(context.Background(), th))
	sink := newFakeSink(t, s)
	return s, th, sink, &AlarmWatch{Store: s, Sink: sink, Now: func() time.Time { return wNow }}
}

// series legt Minutenwerte ab: der erste unter dem Minutenende from, dann je Minute weiter.
func series(t *testing.T, s *Store, meter string, from time.Time, vals ...float64) {
	t.Helper()
	for i, v := range vals {
		_, err := s.DB.Exec(context.Background(), `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, $3)
			ON CONFLICT (meter_id, ts) DO UPDATE SET value = EXCLUDED.value`, meter, from.Add(time.Duration(i)*time.Minute), v)
		must(t, err)
	}
}

func repeatV(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func cat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func setChecked(t *testing.T, s *Store, id string, ts time.Time) {
	t.Helper()
	_, err := s.DB.Exec(context.Background(), `INSERT INTO energy_threshold_state (threshold_id, checked_until) VALUES ($1::uuid, $2)
		ON CONFLICT (threshold_id) DO UPDATE SET checked_until = EXCLUDED.checked_until`, id, ts)
	must(t, err)
}

type wState struct {
	state                        string
	alarmSince, clearSince, chkd *time.Time
	ticket                       *string
	last                         *float64
	exists                       bool
}

func stateOf(t *testing.T, s *Store, id string) wState {
	t.Helper()
	var w wState
	err := s.DB.QueryRow(context.Background(), `SELECT state, alarm_since, clear_since, checked_until, ticket_id::text, last_value
		FROM energy_threshold_state WHERE threshold_id = $1::uuid`, id).
		Scan(&w.state, &w.alarmSince, &w.clearSince, &w.chkd, &w.ticket, &w.last)
	if errors.Is(err, pgx.ErrNoRows) {
		return w
	}
	must(t, err)
	w.exists = true
	return w
}

func pass(t *testing.T, w *AlarmWatch, now time.Time) {
	t.Helper()
	w.Now = func() time.Time { return now }
	must(t, w.Pass(context.Background()))
}

func at(h, m int) time.Time { return time.Date(2026, 11, 15, h, m, 0, 0, time.UTC) }

func eqTime(p *time.Time, want time.Time) bool { return p != nil && p.Equal(want) }

func TestNextAlarmTick(t *testing.T) {
	off := samplerOffset + 10*time.Second
	for _, c := range []struct{ now, want time.Time }{
		{m0, m0.Add(off)},
		{m0.Add(off - time.Second), m0.Add(off)},
		{m0.Add(off), m0.Add(time.Minute + off)},
		{m0.Add(59 * time.Second), m0.Add(time.Minute + off)},
	} {
		if got := nextAlarmTick(c.now); !got.Equal(c.want) {
			t.Errorf("nextAlarmTick(%s) = %s, erwartet %s", c.now.Format("15:04:05"), got.Format("15:04:05"), c.want.Format("15:04:05"))
		}
	}
}

func TestWatchOpensTicketOnce(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	series(t, s, th.MeterID, at(11, 50), repeatV(20, 11)...) // 11:50 … 12:00 über der Grenze

	// Erster Lauf: kein Rückblick, nur Startpunkt setzen.
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); !st.exists || !eqTime(st.chkd, at(12, 0)) || st.state != "normal" {
		t.Fatalf("erster Lauf: %+v", st)
	}
	if sink.opens[th.ID] != 0 {
		t.Fatalf("erster Lauf darf nicht zurückschauen: %d Tickets", sink.opens[th.ID])
	}

	series(t, s, th.MeterID, at(12, 1), 20)
	pass(t, w, wNow.Add(time.Minute))
	st := stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 1 || sink.afters[th.ID] != 1 || st.state != "alarm" || !eqTime(st.alarmSince, at(12, 1)) ||
		st.ticket == nil || st.last == nil || *st.last != 20 || !eqTime(st.chkd, at(12, 1)) {
		t.Fatalf("Alarm: opens=%d afters=%d %+v", sink.opens[th.ID], sink.afters[th.ID], st)
	}

	series(t, s, th.MeterID, at(12, 2), 25, 30)
	pass(t, w, wNow.Add(3*time.Minute))
	pass(t, w, wNow.Add(3*time.Minute))
	if st := stateOf(t, s, th.ID); sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 0 || *st.last != 30 {
		t.Fatalf("weiter im Alarm: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}
}

func TestWatchFlappingOneTicket(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), cat(repeatV(20, 3), repeatV(5, 5), repeatV(20, 3))...)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 0 || st.state != "alarm" || st.clearSince != nil ||
		!eqTime(st.alarmSince, at(11, 50)) {
		t.Fatalf("Pendeln: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}
}

func TestWatchResetAfter15(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(11, 44))
	series(t, s, th.MeterID, at(11, 45), cat([]float64{20}, repeatV(5, 14))...) // bis 11:59
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if st.state != "alarm" || !eqTime(st.clearSince, at(11, 46)) || !eqTime(st.chkd, at(11, 59)) {
		t.Fatalf("nach 14 min unter der Grenze: %+v", st)
	}
	ticket := *st.ticket

	series(t, s, th.MeterID, at(12, 0), 5)
	pass(t, w, wNow)
	st = stateOf(t, s, th.ID)
	if st.state != "normal" || st.alarmSince != nil || st.clearSince != nil || st.ticket == nil || *st.ticket != ticket {
		t.Fatalf("nach 15 min unter der Grenze: %+v", st)
	}
	if sink.opens[th.ID] != 1 {
		t.Errorf("opens = %d", sink.opens[th.ID])
	}
}

func TestWatchRepeatWhileTicketOpen(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(11, 39))
	// 11:40 über, 11:41–11:55 unter (Rückschaltung um 11:55), 11:56 wieder über.
	series(t, s, th.MeterID, at(11, 40), cat([]float64{20}, repeatV(5, 15), []float64{20})...)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 1 || st.state != "alarm" || !eqTime(st.alarmSince, at(11, 56)) {
		t.Fatalf("Ticket offen: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}
	if got := sink.repTkt[th.ID]; len(got) != 1 || got[0] != *st.ticket {
		t.Errorf("Repeat ohne Ticket-ID: %v (Ticket %s)", got, *st.ticket)
	}
	first := *st.ticket

	// 11:57–12:11 unter (Rückschaltung), Ticket wird geschlossen, 12:12 wieder über.
	series(t, s, th.MeterID, at(11, 57), cat(repeatV(5, 15), []float64{20})...)
	_, err := s.DB.Exec(context.Background(), `UPDATE tickets SET status = 'closed' WHERE id = $1::uuid`, first)
	must(t, err)
	pass(t, w, at(12, 12).Add(15*time.Second))
	st = stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 2 || sink.repeats[th.ID] != 1 || st.state != "alarm" || *st.ticket == first {
		t.Fatalf("Ticket zu: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}
}

func TestWatchGapKeepsState(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(11, 44))
	// 11:45 über, 11:46–11:50 unter, 11:51–11:59 keine Werte, 12:00 unter.
	series(t, s, th.MeterID, at(11, 45), cat([]float64{20}, repeatV(5, 5))...)
	series(t, s, th.MeterID, at(12, 0), 5)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if st.state != "alarm" || !eqTime(st.chkd, at(12, 0)) || !eqTime(st.clearSince, at(12, 0)) {
		t.Fatalf("Lücke darf nicht zurückschalten: %+v", st)
	}

	// Zeitraum 5 min mit fehlender Minute: kein Alarm.
	_, meter2 := thresholdMeter(t)
	th3 := okThreshold(meter2) // Zeitraum 5 min
	must(t, s.SaveThreshold(context.Background(), th3))
	setChecked(t, s, th3.ID, at(11, 50))
	series(t, s, meter2, at(11, 56), 20, 20)
	series(t, s, meter2, at(11, 59), 20, 20) // 11:58 fehlt
	pass(t, w, wNow)
	if st := stateOf(t, s, th3.ID); sink.opens[th3.ID] != 0 || st.state != "normal" || !eqTime(st.chkd, at(12, 0)) {
		t.Fatalf("unvollständiger Zeitraum: opens=%d %+v", sink.opens[th3.ID], st)
	}
}

func TestWatchCatchUp(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(10, 0))
	// 120 verpasste Minuten: 10:01–10:30 über, 10:31–11:00 unter, 11:01–12:00 über.
	series(t, s, th.MeterID, at(10, 1), cat(repeatV(20, 30), repeatV(5, 30), repeatV(20, 60))...)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 1 || st.state != "alarm" || !eqTime(st.chkd, at(12, 0)) {
		t.Fatalf("Nachholen: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}
	if got := sink.at[th.ID]; len(got) != 2 || !got[0].Equal(at(10, 1)) || !got[1].Equal(at(11, 1)) {
		t.Errorf("Reihenfolge: %v", got)
	}
	pass(t, w, wNow)
	if sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 1 {
		t.Errorf("zweiter Lauf doppelt: opens=%d repeats=%d", sink.opens[th.ID], sink.repeats[th.ID])
	}
}

func TestWatchCatchUpLimit24h(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	cut := at(12, 0).Add(-24 * time.Hour)
	setChecked(t, s, th.ID, cut.Add(-24*time.Hour))
	// Älter als 24 h über der Grenze, danach unter.
	_, err := s.DB.Exec(context.Background(), `INSERT INTO energy_values (meter_id, ts, value)
		SELECT $1::uuid, g, CASE WHEN g < $2 THEN 20 ELSE 5 END
		  FROM generate_series($3::timestamptz, $4::timestamptz, interval '1 minute') g`,
		th.MeterID, cut, cut.Add(-24*time.Hour).Add(time.Minute), at(12, 0))
	must(t, err)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if sink.opens[th.ID] != 0 || st.state != "normal" || !eqTime(st.chkd, at(12, 0)) || st.last == nil || *st.last != 5 {
		t.Fatalf("älter als 24 h ausgewertet: opens=%d %+v", sink.opens[th.ID], st)
	}
}

func TestWatchThresholdRemoved(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	ctx := context.Background()
	setChecked(t, s, th.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), 20)
	pass(t, w, wNow)
	if sink.opens[th.ID] != 1 {
		t.Fatalf("opens = %d", sink.opens[th.ID])
	}
	stale, err := s.Threshold(ctx, th.ID)
	must(t, err)
	must(t, s.DeleteThreshold(ctx, th.ID))
	series(t, s, th.MeterID, at(11, 51), 20, 5, 20)
	pass(t, w, wNow)
	// Zwischen Liste und Prüfung gelöscht: kein Fehler, kein Zustand.
	must(t, w.watchOne(ctx, *stale, wNow))
	if st := stateOf(t, s, th.ID); st.exists || sink.opens[th.ID] != 1 || sink.repeats[th.ID] != 0 {
		t.Fatalf("nach Löschen: opens=%d repeats=%d %+v", sink.opens[th.ID], sink.repeats[th.ID], st)
	}

	// Messstelle gelöscht: Wächter läuft weiter.
	_, err = s.DB.Exec(ctx, `DELETE FROM energy_meters WHERE id = $1::uuid`, th.MeterID)
	must(t, err)
	pass(t, w, wNow)
}

func TestWatchInactiveIgnored(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	ctx := context.Background()
	th.Active = false
	must(t, s.SaveThreshold(ctx, th))
	setChecked(t, s, th.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), 20, 20)
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); sink.opens[th.ID] != 0 || st.chkd != nil {
		t.Fatalf("inaktiver Schwellwert: opens=%d %+v", sink.opens[th.ID], st)
	}

	// Wieder eingeschaltet: nicht rückwirkend prüfen.
	th.Active = true
	must(t, s.SaveThreshold(ctx, th))
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); sink.opens[th.ID] != 0 || !eqTime(st.chkd, at(12, 0)) {
		t.Fatalf("wieder aktiv: opens=%d %+v", sink.opens[th.ID], st)
	}

	_, err := s.DB.Exec(ctx, `UPDATE energy_meters SET active = false WHERE id = $1::uuid`, th.MeterID)
	must(t, err)
	pass(t, w, wNow)
	if sink.opens[th.ID] != 0 {
		t.Fatalf("inaktive Messstelle: opens=%d", sink.opens[th.ID])
	}
	// Ein veralteter Eintrag aus der Liste wird in der Transaktion erkannt.
	must(t, w.watchOne(ctx, *th, wNow))
	if sink.opens[th.ID] != 0 {
		t.Fatalf("inaktive Messstelle (watchOne): opens=%d", sink.opens[th.ID])
	}
}

func TestWatchInactiveResetsAlarm(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	ctx := context.Background()
	setChecked(t, s, th.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), 20)
	pass(t, w, wNow)
	st := stateOf(t, s, th.ID)
	if st.state != "alarm" || st.ticket == nil {
		t.Fatalf("Alarm erwartet: %+v", st)
	}
	ticket := *st.ticket
	sink = newFakeSink(t, s)
	w.Sink = sink

	_, err := s.DB.Exec(ctx, `UPDATE energy_meters SET active = false WHERE id = $1::uuid`, th.MeterID)
	must(t, err)
	pass(t, w, wNow)
	st = stateOf(t, s, th.ID)
	if st.state != "normal" || st.alarmSince != nil || st.clearSince != nil || st.ticket == nil || *st.ticket != ticket {
		t.Fatalf("nach Deaktivieren: %+v", st)
	}
	if sink.opens[th.ID]+sink.repeats[th.ID]+sink.afters[th.ID] != 0 {
		t.Errorf("Sink aufgerufen: %+v", sink)
	}
}

func TestWatchErrorOthersContinue(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	ctx := context.Background()
	th2 := okThreshold(th.MeterID)
	th2.Name, th2.WindowMinutes = "Zweiter", 1
	must(t, s.SaveThreshold(ctx, th2))
	sink.fail[th.ID] = true
	setChecked(t, s, th.ID, at(11, 49))
	setChecked(t, s, th2.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), 20)
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); st.state != "normal" || !eqTime(st.chkd, at(11, 49)) {
		t.Errorf("fehlgeschlagener Schwellwert muss unverändert bleiben: %+v", st)
	}
	if st := stateOf(t, s, th2.ID); sink.opens[th2.ID] != 1 || st.state != "alarm" {
		t.Errorf("anderer Schwellwert: opens=%d %+v", sink.opens[th2.ID], st)
	}
}

func TestWatchCounterWindow(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `UPDATE energy_meters SET kind = 'counter' WHERE id = $1::uuid`, th.MeterID)
	must(t, err)
	th.WindowMinutes = 5
	must(t, s.SaveThreshold(ctx, th))
	setChecked(t, s, th.ID, at(11, 59))
	// Zählerstand 11:55 = 100, 12:00 = 111: Menge 11 > 10.
	series(t, s, th.MeterID, at(11, 55), 100, 102, 104, 106, 108, 111)
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); sink.opens[th.ID] != 1 || st.last == nil || *st.last != 11 {
		t.Fatalf("Zähler: opens=%d %+v", sink.opens[th.ID], st)
	}
}

func TestWatchSkip(t *testing.T) {
	s, th, sink, w := watchEnv(t)
	setChecked(t, s, th.ID, at(11, 49))
	series(t, s, th.MeterID, at(11, 50), 20)
	w.Skip = func() bool { return true }
	pass(t, w, wNow)
	if st := stateOf(t, s, th.ID); sink.opens[th.ID] != 0 || !eqTime(st.chkd, at(11, 49)) {
		t.Fatalf("Skip: opens=%d %+v", sink.opens[th.ID], st)
	}
}
