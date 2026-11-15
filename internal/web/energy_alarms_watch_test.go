package web

import (
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/energie"
)

// Wächter und echte Senke Ende zu Ende, nur für den eigenen Schwellwert
// (PassOne, nie ein voller Pass über die geteilte Datenbank): Ticket mit
// Ursache-Pflicht, Schlagwort und Titel; Zustand alarm; nach zweitem Lauf
// weiter ein Ticket; nach Erledigen und Rückschaltung bringt eine neue
// Überschreitung ein neues Ticket.
func TestAlarmWatchEndToEndWithSink(t *testing.T) {
	e := newAlarmEnv(t)
	st := energie.NewStore(e.pool)
	m := &energie.Meter{Key: "ALP" + e.sfx, Name: "Druck " + e.sfx, InfraID: e.child, Unit: "bar", Kind: energie.KindInstant, Active: true}
	if _, err := st.Save(e.ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	// läuft vor dem Aufräumen von alarmEnv (Cleanups laufen rückwärts); Werte und Zustand folgen per Cascade
	t.Cleanup(func() {
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM tickets WHERE infrastructure_id = $1::uuid`, e.child)
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM energy_meters WHERE id = $1::uuid`, m.ID)
	})
	th := &energie.Threshold{MeterID: m.ID, Name: "Spitze " + e.sfx, Condition: energie.CondAbove, Limit: 50,
		WindowMinutes: 1, ResetMinutes: 2, Priority: "high", Tag: "Energie-Alarm", Active: true}
	e.must(st.SaveThreshold(e.ctx, th))

	base := time.Now().Truncate(time.Minute).Add(-3 * time.Hour)
	now := base
	w := &energie.AlarmWatch{Store: st, Sink: energyAlarmSink{h: e.h}, Now: func() time.Time { return now }}
	put := func(min int, v float64) {
		t.Helper()
		_, err := e.pool.Exec(e.ctx, `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, $3)
			ON CONFLICT (meter_id, ts) DO UPDATE SET value = EXCLUDED.value`, m.ID, base.Add(time.Duration(min)*time.Minute), v)
		e.must(err)
	}
	pass := func(min int) {
		t.Helper()
		now = base.Add(time.Duration(min) * time.Minute)
		e.must(w.PassOne(e.ctx, th.ID))
	}
	state := func() (string, string) {
		t.Helper()
		var s string
		var tid *string
		e.must(e.pool.QueryRow(e.ctx, `SELECT state, ticket_id::text FROM energy_threshold_state WHERE threshold_id = $1::uuid`, th.ID).Scan(&s, &tid))
		if tid == nil {
			return s, ""
		}
		return s, *tid
	}
	ticketCount := func() int {
		t.Helper()
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE infrastructure_id = $1::uuid AND title LIKE '%' || $2 || '%'`,
			e.child, th.Name).Scan(&n))
		return n
	}

	// erster Lauf setzt nur die Marke, kein Rückblick
	pass(0)
	if s, tid := state(); s != "normal" || tid != "" || ticketCount() != 0 {
		t.Fatalf("nach dem ersten Lauf: %s %q, %d Tickets", s, tid, ticketCount())
	}

	// Überschreitung: Alarm und ein Ticket
	put(1, 60)
	put(2, 61)
	pass(2)
	s, first := state()
	if s != "alarm" || first == "" {
		t.Fatalf("nach Überschreitung: %s %q", s, first)
	}
	var title, tag, prio string
	var rc bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT title, requires_root_cause, COALESCE(tags->>0, ''), priority::text FROM tickets WHERE id = $1::uuid`, first).Scan(&title, &rc, &tag, &prio))
	if !rc || tag != "Energie-Alarm" || prio != "high" || !strings.Contains(title, th.Name) || !strings.Contains(title, "überschritten") {
		t.Errorf("Ticket: Titel %q, Ursache-Pflicht %v, Schlagwort %q, Priorität %q", title, rc, tag, prio)
	}
	if n := ticketCount(); n != 1 {
		t.Fatalf("Tickets nach erstem Alarm: %d", n)
	}

	// zweiter Lauf ohne neue Minuten, dann weitere Überschreitung am offenen Ticket: weiter genau ein Ticket
	pass(2)
	put(3, 62)
	pass(3)
	if s, tid := state(); s != "alarm" || tid != first || ticketCount() != 1 {
		t.Fatalf("nach zweitem Lauf: %s %q, %d Tickets", s, tid, ticketCount())
	}

	// Werte normal: nach Rückschaltzeit (2 Minuten) wieder normal
	put(4, 10)
	put(5, 10)
	pass(5)
	if s, tid := state(); s != "normal" || tid != first {
		t.Fatalf("nach Rückschaltung: %s %q", s, tid)
	}

	// Ticket erledigt, neue Überschreitung: neues Ticket
	if _, err := e.pool.Exec(e.ctx, `UPDATE tickets SET status = 'resolved' WHERE id = $1::uuid`, first); err != nil {
		t.Fatal(err)
	}
	put(6, 70)
	pass(6)
	s, second := state()
	if s != "alarm" || second == "" || second == first {
		t.Fatalf("nach neuer Überschreitung: %s erstes %q zweites %q", s, first, second)
	}
	if n := ticketCount(); n != 2 {
		t.Fatalf("Tickets insgesamt: %d, erwartet 2", n)
	}
}
