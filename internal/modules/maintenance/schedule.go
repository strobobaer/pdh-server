package maintenance

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// Terminplanung.
//
// Grundsatz: Jeder aktive Plan hat genau EINEN offenen Auftrag (offen, in
// Arbeit oder wartet) – den naechsten Termin. Wird er erledigt oder
// uebersprungen, berechnet ScheduleNext den Folgetermin und legt sofort den
// naechsten Auftrag an. EnsurePlanTasks (beim Start und stuendlich) legt fuer
// Plaene ohne offenen Auftrag den fehlenden an. Doppelte Auftraege verhindert
// eine Sperre je Plan.

// dayStart: Mitternacht (Ortszeit) – Termine sind Tage, keine Uhrzeiten.
func dayStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// AddInterval rechnet „alle N Tage/Wochen/Monate/Jahre“ kalendergenau weiter.
func AddInterval(unit string, count int, from time.Time) time.Time {
	if count < 1 {
		count = 1
	}
	from = dayStart(from)
	switch unit {
	case UnitDay:
		return from.AddDate(0, 0, count)
	case UnitWeek:
		return from.AddDate(0, 0, 7*count)
	case UnitYear:
		return from.AddDate(count, 0, 0)
	}
	return from.AddDate(0, count, 0)
}

// nextDueFor: Folgetermin nach Abschluss/Ueberspringen eines Auftrags.
// Fester Rhythmus: ab dem Faelligkeitstag; liegt der Termin dann schon in der
// Vergangenheit (stark verspaetet erledigt), wird bis heute aufgeholt, damit
// kein Rueckstau ueberfaelliger Termine entsteht.
func nextDueFor(mode, unit string, count int, due, done, now time.Time) time.Time {
	if mode != ScheduleFixed {
		return AddInterval(unit, count, done)
	}
	next := AddInterval(unit, count, due)
	today := dayStart(now)
	for i := 0; next.Before(today) && i < 5000; i++ {
		next = AddInterval(unit, count, next)
	}
	return next
}

// PlannedDates: kuenftige Termine eines Plans ab next (einschliesslich) bis
// einschliesslich until – fuer Jahresplan und Vorschau. Bei „ab Durchfuehrung“
// ist das eine Prognose unter der Annahme, dass puenktlich erledigt wird.
func PlannedDates(unit string, count int, next, until time.Time, max int) []time.Time {
	var out []time.Time
	d := dayStart(next)
	until = dayStart(until)
	for !d.After(until) && len(out) < max {
		out = append(out, d)
		d = AddInterval(unit, count, d)
	}
	return out
}

// LegacyInterval: Altfelder interval_type/interval_days zu Einheit/Anzahl
// (andere Programmteile lesen sie weiterhin).
func LegacyInterval(unit string, count int) (Interval, int) {
	if count < 1 {
		count = 1
	}
	switch unit {
	case UnitDay:
		if count%7 == 0 {
			return IntervalWeekly, count
		}
		return IntervalDaily, count
	case UnitWeek:
		return IntervalWeekly, 7 * count
	case UnitYear:
		return IntervalYearly, 365 * count
	}
	if count%3 == 0 && count < 12 {
		return IntervalQuarterly, 30 * count
	}
	if count%12 == 0 {
		return IntervalYearly, 30 * count
	}
	return IntervalMonthly, 30 * count
}

// UnitFromLegacy: Altform der API (daily…yearly) zu Einheit/Anzahl.
func UnitFromLegacy(iv Interval) (string, int) {
	switch iv {
	case IntervalDaily:
		return UnitDay, 1
	case IntervalWeekly:
		return UnitWeek, 1
	case IntervalQuarterly:
		return UnitMonth, 3
	case IntervalYearly:
		return UnitYear, 1
	}
	return UnitMonth, 1
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
	var unit, mode string
	var count int
	var active bool
	if err := r.db.QueryRow(ctx, `SELECT interval_unit, interval_count, schedule_mode, active FROM maintenance_plans WHERE id = $1::uuid`, *planID).
		Scan(&unit, &count, &mode, &active); err != nil || !active {
		return nil, err
	}
	done := time.Now()
	if completed != nil {
		done = *completed
	}
	next := nextDueFor(mode, unit, count, due, done, time.Now())
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
func (r *Repository) scheduleNextLogged(ctx context.Context, taskID, userID string, executed bool) {
	if _, err := r.ScheduleNext(ctx, taskID, userID, executed); err != nil {
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
