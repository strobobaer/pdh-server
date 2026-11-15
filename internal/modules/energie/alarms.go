package energie

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

const (
	// alarmOffset: der Wächter läuft kurz nach dem Sampler, dann liegt der
	// Wert der gerade abgeschlossenen Minute schon in energy_values.
	alarmOffset = samplerOffset + 10*time.Second
	// alarmCatchUp: so weit holt der Wächter verpasste Minuten höchstens nach.
	alarmCatchUp = 24 * time.Hour
)

// AlarmEvent beschreibt eine Überschreitung eines Schwellwerts.
type AlarmEvent struct {
	Threshold Threshold
	Value     float64
	At        time.Time // Minutenende, in dem die Bedingung zuerst erfüllt war
	TicketID  string    // gesetzt bei erneuter Überschreitung am offenen Ticket
}

// AlarmSink setzt Alarme in Tickets und Nachrichten um. Alle Aufrufe laufen in
// der Transaktion des Wächters; die gelieferten after-Funktionen (Nachrichten)
// ruft der Wächter erst nach dem Commit auf.
type AlarmSink interface {
	OpenTicket(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (ticketID string, after func(), err error)
	TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error)       // open/in_progress/pending, nicht archiviert
	Repeat(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (after func(), err error) // Eintrag am Ticket + Nachricht
}

// AlarmWatch prüft jede Minute alle aktiven Schwellwerte aktiver Messstellen.
//
// Je Schwellwert gibt es zwei Zustände: normal und alarm. Ist die Bedingung in
// einer Minute erfüllt, wechselt er in alarm und legt ein Ticket an (oder
// ergänzt das noch offene Ticket des letzten Alarms). Zurück auf normal geht
// es erst nach reset_minutes Minuten am Stück ohne erfüllte Bedingung. Nicht
// bewertbare Minuten (Lücken) ändern den Zustand nicht und unterbrechen eine
// laufende Rückschaltzeit. Verpasste Minuten werden in Reihenfolge nachgeholt,
// höchstens alarmCatchUp weit.
type AlarmWatch struct {
	Store *Store
	Sink  AlarmSink
	Skip  func() bool // wahr: nicht prüfen (Standby, Wartung)
	Now   func() time.Time
}

func (w *AlarmWatch) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return w.Store.now()
}

// Pass prüft alle Schwellwerte bis zur letzten erfassten Minute. Fehler eines
// Schwellwerts werden protokolliert, die übrigen laufen weiter.
func (w *AlarmWatch) Pass(ctx context.Context) error {
	if w.Skip != nil && w.Skip() {
		return nil
	}
	now := w.now()
	// Inaktive Schwellwerte und Messstellen werden nicht überwacht: ein
	// laufender Alarm endet still (ohne Ticket), die Ticket-ID bleibt. Nach
	// dem Wiedereinschalten wird ab dann geprüft, nicht rückwirkend.
	if _, err := w.Store.DB.Exec(ctx, `
		UPDATE energy_threshold_state st SET state = 'normal', alarm_since = NULL, clear_since = NULL, checked_until = NULL
		  FROM energy_thresholds t JOIN energy_meters m ON m.id = t.meter_id
		 WHERE st.threshold_id = t.id AND (st.state = 'alarm' OR st.checked_until IS NOT NULL)
		   AND NOT (t.active AND m.active)`); err != nil {
		return err
	}
	list, err := w.Store.Thresholds(ctx, "")
	if err != nil {
		return err
	}
	for _, th := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.safeOne(ctx, th, now); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Str("schwellwert", th.ID).Str("messstelle", th.MeterKey).Msg("energie: schwellwert prüfen")
		}
	}
	return nil
}

func (w *AlarmWatch) safeOne(ctx context.Context, th Threshold, now time.Time) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("abgestürzt: %v", r)
		}
	}()
	return w.watchOne(ctx, th, now)
}

// watchOne prüft einen Schwellwert in einer Transaktion mit gesperrter
// Zustandszeile. th stammt aus der Liste und kann veraltet sein; maßgeblich
// ist der in der Transaktion gelesene Stand.
func (w *AlarmWatch) watchOne(ctx context.Context, listed Threshold, now time.Time) error {
	var afters []func()
	tx, err := w.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Zustandszeile anlegen, falls sie fehlt – nur solange der Schwellwert besteht.
	if _, err := tx.Exec(ctx, `INSERT INTO energy_threshold_state (threshold_id)
		SELECT id FROM energy_thresholds WHERE id = $1::uuid ON CONFLICT (threshold_id) DO NOTHING`, listed.ID); err != nil {
		return err
	}
	var checked *time.Time
	err = tx.QueryRow(ctx, `SELECT checked_until FROM energy_threshold_state WHERE threshold_id = $1::uuid FOR UPDATE`,
		listed.ID).Scan(&checked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // inzwischen gelöscht
	}
	if err != nil {
		return err
	}
	th, err := scanThreshold(tx.QueryRow(ctx, thresholdSelect+" WHERE t.id = $1::uuid AND t.active AND m.active", listed.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // gelöscht oder deaktiviert; Pass setzt den Alarm zurück
	}
	if err != nil {
		return err
	}

	lastFull := now.Truncate(time.Minute)
	if checked == nil { // erster Lauf: ab jetzt prüfen, kein Rückblick
		if _, err := tx.Exec(ctx, `UPDATE energy_threshold_state SET checked_until = $2 WHERE threshold_id = $1::uuid`,
			th.ID, lastFull); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	var latest *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(ts) FROM energy_values WHERE meter_id = $1::uuid`, th.MeterID).Scan(&latest); err != nil {
		return err
	}
	if latest == nil {
		return nil
	}
	end := latest.Truncate(time.Minute)
	if end.After(lastFull) {
		end = lastFull
	}
	start := checked.Truncate(time.Minute).Add(time.Minute)
	if start.After(end) {
		return nil
	}

	state, alarmSince, clearSince, ticketID, lastValue := th.State, th.AlarmSince, th.ClearSince, th.TicketID, th.LastValue
	if floor := lastFull.Add(-alarmCatchUp); start.Before(floor) {
		// Übersprungene Minuten zählen wie Lücken.
		start, clearSince = floor, nil
		if start.After(end) {
			return nil
		}
	}

	window := time.Duration(th.WindowMinutes) * time.Minute
	vals, err := minuteValues(ctx, tx, th.MeterID, start.Add(-window), end)
	if err != nil {
		return err
	}

	for t := start; !t.After(end); t = t.Add(time.Minute) {
		// nur die Werte (t − Zeitraum, t] bzw. für Zählerstände ab t − Zeitraum
		lo := sort.Search(len(vals), func(i int) bool { return !vals[i].TS.Before(t.Add(-window)) })
		hi := sort.Search(len(vals), func(i int) bool { return vals[i].TS.After(t) })
		v, ok := WindowValue(th.Kind, vals[lo:hi], t, th.WindowMinutes)
		if !ok {
			// Lücke: Zustand bleibt, eine laufende Rückschaltzeit beginnt neu.
			clearSince = nil
			continue
		}
		v0 := v
		lastValue = &v0
		hit := Breached(th.Condition, v, th.Limit)
		switch {
		case state != "alarm" && hit:
			ts := t
			state, alarmSince, clearSince = "alarm", &ts, nil
			cur := *th
			cur.State, cur.AlarmSince, cur.ClearSince, cur.LastValue = state, alarmSince, nil, lastValue
			ev := AlarmEvent{Threshold: cur, Value: v, At: t}
			open := false
			if ticketID != nil {
				if open, err = w.Sink.TicketOpen(ctx, tx, *ticketID); err != nil {
					return err
				}
			}
			var after func()
			if open {
				ev.TicketID = *ticketID
				ev.Threshold.TicketID = ticketID
				if after, err = w.Sink.Repeat(ctx, tx, ev); err != nil {
					return err
				}
			} else {
				id, a, err := w.Sink.OpenTicket(ctx, tx, ev)
				if err != nil {
					return err
				}
				ticketID, after = &id, a
			}
			if after != nil {
				afters = append(afters, after)
			}
		case state == "alarm" && hit:
			clearSince = nil
		case state == "alarm":
			if clearSince == nil {
				ts := t
				clearSince = &ts
			}
			if int(t.Sub(*clearSince)/time.Minute)+1 >= th.ResetMinutes {
				state, alarmSince, clearSince = "normal", nil, nil
			}
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE energy_threshold_state SET state = $2, alarm_since = $3, clear_since = $4,
		checked_until = $5, ticket_id = $6::uuid, last_value = $7 WHERE threshold_id = $1::uuid`,
		th.ID, state, alarmSince, clearSince, end, ticketID, lastValue); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, f := range afters {
		f()
	}
	return nil
}

// minuteValues liest die Minutenwerte einer Messstelle von from bis to (beide
// einschließlich), zeitlich sortiert.
func minuteValues(ctx context.Context, tx pgx.Tx, meterID string, from, to time.Time) ([]MinuteValue, error) {
	rows, err := tx.Query(ctx, `SELECT ts, value FROM energy_values
		WHERE meter_id = $1::uuid AND ts >= $2 AND ts <= $3 ORDER BY ts`, meterID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MinuteValue
	for rows.Next() {
		var v MinuteValue
		if err := rows.Scan(&v.TS, &v.Value); err != nil {
			return nil, err
		}
		v.TS = v.TS.Truncate(time.Minute)
		out = append(out, v)
	}
	return out, rows.Err()
}

// nextAlarmTick liefert den nächsten Takt: alarmOffset nach der nächsten vollen Minute.
func nextAlarmTick(now time.Time) time.Time {
	t := now.Truncate(time.Minute).Add(alarmOffset)
	if !t.After(now) {
		t = t.Add(time.Minute)
	}
	return t
}

// Start prüft jede Minute kurz nach dem Sampler, bis ctx endet. Eine Panik in
// einem Durchlauf wird protokolliert, die Schleife läuft weiter.
func (w *AlarmWatch) Start(ctx context.Context) {
	for {
		w.safePass(ctx)
		now := time.Now()
		t := time.NewTimer(nextAlarmTick(now).Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (w *AlarmWatch) safePass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("energie: schwellwerte abgestürzt")
		}
	}()
	if err := w.Pass(ctx); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: schwellwerte")
	}
}
