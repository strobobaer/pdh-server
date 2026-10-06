package maintenance

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// Terminplanung der Wartungsplaene.
//
// Grundsatz: Jeder aktive Plan hat genau EINEN offenen Auftrag (offen, in
// Arbeit oder wartet) – den naechsten Termin. Wird er erledigt oder
// uebersprungen, berechnet ScheduleNext den Folgetermin und legt sofort den
// naechsten Auftrag an. EnsurePlanTasks (beim Start und stuendlich) legt fuer
// Plaene ohne offenen Auftrag den fehlenden an – z. B. nach dem Anlegen eines
// Plans oder wenn ein Auftrag geloescht wurde. Doppelte Auftraege verhindert
// eine Sperre je Plan.

// Berechnungsarten fuer den naechsten Termin (maintenance_plans.schedule_mode).
const (
	ScheduleFromCompletion = "completion" // ab Durchfuehrung: Abschluss + Intervall
	ScheduleFixed          = "fixed"      // fester Rhythmus: Faelligkeit + Intervall
)

// dayStart: Mitternacht (Ortszeit) – Termine sind Tage, keine Uhrzeiten.
func dayStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// NextDue rechnet ein Intervall kalendergenau weiter (Monat, Quartal, Jahr
// wirklich als Monat usw. statt 30/90/365 Tage).
func NextDue(interval Interval, intervalDays int, from time.Time) time.Time {
	from = dayStart(from)
	switch interval {
	case IntervalDaily:
		return from.AddDate(0, 0, 1)
	case IntervalWeekly:
		return from.AddDate(0, 0, 7)
	case IntervalMonthly:
		return from.AddDate(0, 1, 0)
	case IntervalQuarterly:
		return from.AddDate(0, 3, 0)
	case IntervalYearly:
		return from.AddDate(1, 0, 0)
	}
	if intervalDays <= 0 {
		intervalDays = 30
	}
	return from.AddDate(0, 0, intervalDays)
}

// nextDueFor: Folgetermin nach Abschluss/Ueberspringen eines Auftrags.
// Fester Rhythmus: ab dem Faelligkeitstag; liegt der Termin dann schon in der
// Vergangenheit (stark verspaetet erledigt), wird bis heute aufgeholt, damit
// kein Rueckstau ueberfaelliger Termine entsteht.
func nextDueFor(mode string, interval Interval, intervalDays int, due, done, now time.Time) time.Time {
	if mode != ScheduleFixed {
		return NextDue(interval, intervalDays, done)
	}
	next := NextDue(interval, intervalDays, due)
	today := dayStart(now)
	for i := 0; next.Before(today) && i < 2000; i++ {
		next = NextDue(interval, intervalDays, next)
	}
	return next
}

// ScheduleNext: nach Abschluss (executed=true) oder Ueberspringen eines
// Auftrags den naechsten Termin des Plans setzen und den Folgeauftrag
// anlegen. Liefert den neuen Termin (nil = Auftrag ohne aktiven Plan).
func (r *Repository) ScheduleNext(ctx context.Context, taskID, userID string, executed bool) (*time.Time, error) {
	var planID *string
	var due time.Time
	var completed *time.Time
	if err := r.db.QueryRow(ctx, `SELECT plan_id::text, due_date, completed_at FROM maintenance_tasks WHERE id = $1::uuid`, taskID).
		Scan(&planID, &due, &completed); err != nil {
		return nil, err
	}
	if planID == nil {
		return nil, nil
	}
	var interval Interval
	var days int
	var mode string
	var active bool
	if err := r.db.QueryRow(ctx, `SELECT interval_type, interval_days, schedule_mode, active FROM maintenance_plans WHERE id = $1::uuid`, *planID).
		Scan(&interval, &days, &mode, &active); err != nil || !active {
		return nil, err
	}
	done := time.Now()
	if completed != nil {
		done = *completed
	}
	next := nextDueFor(mode, interval, days, due, done, time.Now())
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_plans SET next_due_at = $2,
			last_executed_at = CASE WHEN $3 THEN NOW() ELSE last_executed_at END
		WHERE id = $1::uuid`, *planID, next, executed); err != nil {
		return nil, err
	}
	if _, err := r.ensureTaskForPlan(ctx, *planID, userID); err != nil {
		return &next, err
	}
	return &next, nil
}

// scheduleNextLogged: nach dem Abschluss – der Abschluss selbst ist schon
// gespeichert, ein Fehler bei der Folgeplanung wird nur protokolliert (der
// stuendliche Abgleich legt den fehlenden Auftrag spaeter an).
func (r *Repository) scheduleNextLogged(ctx context.Context, taskID, userID string) {
	if _, err := r.ScheduleNext(ctx, taskID, userID, true); err != nil {
		log.Warn().Err(err).Str("bereich", "wartung").Str("task", taskID).Msg("folgetermin nicht geplant")
	}
}

// ensureTaskForPlan legt den naechsten Auftrag an, falls der Plan keinen
// offenen hat. Die Sperre je Plan verhindert Doppelte bei gleichzeitigem
// Abschluss und stuendlichem Abgleich.
func (r *Repository) ensureTaskForPlan(ctx context.Context, planID, createdBy string) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('maintenance_plan:' || $1::text))`, planID); err != nil {
		return false, err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_tasks WHERE plan_id = $1::uuid AND status IN ('open','in_progress','pending'))`, planID).Scan(&open); err != nil {
		return false, err
	}
	if open {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO maintenance_tasks
		  (id, plan_id, title, description, type, infrastructure_id, priority, status, assigned_to, due_date,
		   created_by, cost_center_id, responsible_to, assigned_group_id)
		SELECT gen_random_uuid(), mp.id, mp.name, COALESCE(mp.description, ''), mp.type, mp.infrastructure_id, mp.priority, 'open',
		       mp.assigned_to, mp.next_due_at, COALESCE(NULLIF($2, '')::uuid, mp.created_by), mp.cost_center_id, mp.responsible_to, mp.assigned_group_id
		FROM maintenance_plans mp WHERE mp.id = $1::uuid AND mp.active`, planID, createdBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, tx.Commit(ctx)
}

// EnsurePlanTasks: fuer jeden aktiven Plan ohne offenen Auftrag den
// naechsten anlegen. Liefert die Zahl neu angelegter Auftraege.
func (r *Repository) EnsurePlanTasks(ctx context.Context) (int, error) {
	rows, err := r.db.Query(ctx, `SELECT mp.id::text FROM maintenance_plans mp
		WHERE mp.active AND NOT EXISTS (SELECT 1 FROM maintenance_tasks mt
			WHERE mt.plan_id = mp.id AND mt.status IN ('open','in_progress','pending'))`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	created := 0
	for _, id := range ids {
		ok, err := r.ensureTaskForPlan(ctx, id, "")
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// NextDueForTask: aktueller naechster Termin des Plans eines Auftrags.
func (r *Repository) NextDueForTask(ctx context.Context, taskID string) *time.Time {
	var next *time.Time
	_ = r.db.QueryRow(ctx, `SELECT mp.next_due_at FROM maintenance_tasks mt JOIN maintenance_plans mp ON mp.id = mt.plan_id
		WHERE mt.id = $1::uuid AND mp.active`, taskID).Scan(&next)
	return next
}

// ── Service ──

// TaskSkipped: Auftrag wurde uebersprungen (z. B. am Leitstand verworfen) –
// der Plan bekommt trotzdem seinen naechsten Termin.
func (s *Service) TaskSkipped(ctx context.Context, taskID, userID string) (*time.Time, error) {
	return s.repo.ScheduleNext(ctx, taskID, userID, false)
}

// EnsurePlanTask: Auftrag zum Plan anlegen, falls keiner offen ist (z. B. direkt nach dem Anlegen).
func (s *Service) EnsurePlanTask(ctx context.Context, planID, userID string) error {
	_, err := s.repo.ensureTaskForPlan(ctx, planID, userID)
	return err
}

func (s *Service) NextDueForTask(ctx context.Context, taskID string) *time.Time {
	return s.repo.NextDueForTask(ctx, taskID)
}

// StartScheduler gleicht beim Start und danach stuendlich ab, dass jeder
// aktive Plan seinen naechsten Auftrag hat.
func (s *Service) StartScheduler(ctx context.Context) {
	run := func() {
		n, err := s.repo.EnsurePlanTasks(ctx)
		if err != nil {
			log.Warn().Err(err).Str("bereich", "wartung").Msg("wartungsplaene: abgleich fehlgeschlagen")
			return
		}
		if n > 0 {
			log.Info().Str("bereich", "wartung").Int("angelegt", n).Msg("wartungsplaene: naechste auftraege angelegt")
		}
	}
	go func() {
		run()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
