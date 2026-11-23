package maintenance

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rs/zerolog/log"
)

// Terminplanung.
//
// Grundsatz: Jeder aktive Plan hat hoechstens EINEN offenen Auftrag (offen, in
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
	// fester Rhythmus: ab dem gerechneten Tag, nicht dem verschobenen (planned_for)
	if err := r.db.QueryRow(ctx, `SELECT plan_id::text, COALESCE(planned_for, due_date::date)::timestamp, completed_at FROM maintenance_tasks WHERE id = $1::uuid`, taskID).
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
	// vergangene geplante Termine sind überholt (kein Rückstau); „ab Durchführung“ verschiebt den Rhythmus
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_visits SET state = 'dropped', updated_at = NOW()
		WHERE plan_id = $1::uuid AND state = 'planned' AND day < $2::date`, *planID, dayStart(time.Now()).Format("2006-01-02")); err != nil {
		return &next, err
	}
	if mode != ScheduleFixed {
		if err := r.RecalcVisits(ctx, *planID, time.Now()); err != nil {
			return &next, err
		}
	}
	if _, err := r.ensureTaskForPlan(ctx, *planID, userID, time.Now()); err != nil {
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

// candidate: der nächste Termin, aus dem der Auftrag entsteht – der früheste
// geplante Termin (bei Gleichstand der frühere gerechnete Tag), ohne
// geplanten Termin next_due_at mit der Besetzung des Plans.
type candidate struct {
	VisitID                 string
	CalcDay, Day            time.Time
	AssignedTo, GroupID     *string
	DateManual, StaffManual bool
}

func (r *Repository) nextCandidate(ctx context.Context, q querier, planID string) (*candidate, *int, error) {
	var lead *int
	c := &candidate{}
	var next time.Time
	if err := q.QueryRow(ctx, `SELECT lead_workdays, next_due_at, assigned_to::text, assigned_group_id::text
		FROM maintenance_plans WHERE id = $1::uuid AND active`, planID).Scan(&lead, &next, &c.AssignedTo, &c.GroupID); err != nil {
		return nil, nil, err
	}
	c.CalcDay, c.Day = dayStart(next), dayStart(next)
	var v candidate
	err := q.QueryRow(ctx, `SELECT id::text, calc_day, day, assigned_to::text, assigned_group_id::text, date_manual, staff_manual
		FROM maintenance_visits WHERE plan_id = $1::uuid AND state = 'planned' ORDER BY day, calc_day LIMIT 1`, planID).
		Scan(&v.VisitID, &v.CalcDay, &v.Day, &v.AssignedTo, &v.GroupID, &v.DateManual, &v.StaffManual)
	if err == nil {
		v.CalcDay, v.Day = civil(v.CalcDay), civil(v.Day)
		return &v, lead, nil
	}
	return c, lead, ignoreNoRows(err)
}

// querier: Pool oder Transaktion.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	planTaskHooksMu sync.RWMutex
	planTaskHooks   []func(ctx context.Context, taskID, actorID string)
)

// OnPlanTaskCreated meldet eine Funktion an, die läuft, wenn aus einem Plan
// ein Auftrag entstanden ist (z. B. Nachricht an die zugewiesene Person).
func OnPlanTaskCreated(fn func(ctx context.Context, taskID, actorID string)) {
	planTaskHooksMu.Lock()
	defer planTaskHooksMu.Unlock()
	planTaskHooks = append(planTaskHooks, fn)
}

func runPlanTaskCreatedHooks(ctx context.Context, taskID, actorID string) {
	planTaskHooksMu.RLock()
	hooks := append([]func(context.Context, string, string){}, planTaskHooks...)
	planTaskHooksMu.RUnlock()
	ctx = context.WithoutCancel(ctx)
	for _, h := range hooks {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error().Interface("panic", rec).Str("bereich", "wartung").Msg("hook nach neuem auftrag abgebrochen")
				}
			}()
			h(ctx, taskID, actorID)
		}()
	}
}

// ensureTaskForPlan legt den Auftrag für den nächsten Termin an, wenn der
// Plan keinen offenen hat und der Vorlauf (Arbeitstage) erreicht ist.
// Liefert die Kennung des neuen Auftrags (leer = nichts angelegt). Die Sperre
// je Plan verhindert Doppelte bei gleichzeitigem Abschluss und Abgleich.
func (r *Repository) ensureTaskForPlan(ctx context.Context, planID, createdBy string, now time.Time) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err := lockPlan(ctx, tx, planID); err != nil {
		return "", err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_tasks WHERE plan_id = $1::uuid AND status IN ('open','in_progress','pending'))`, planID).Scan(&open); err != nil {
		return "", err
	}
	if open {
		return "", nil
	}
	c, lead, err := r.nextCandidate(ctx, tx, planID)
	if err != nil {
		return "", ignoreNoRows(err) // ruhender oder unbekannter Plan
	}
	orderOn := r.OrderDay(ctx, c.Day, lead)
	if orderOn.After(dayStart(now)) {
		return "", nil // Vorlauf noch nicht erreicht
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO maintenance_tasks
		  (id, plan_id, title, description, type, infrastructure_id, priority, status, assigned_to, due_date,
		   created_by, cost_center_id, responsible_to, assigned_group_id, planned_for, upcoming_from, due_manual, staff_manual)
		SELECT pdh_uuid_v7(), mp.id, mp.name, COALESCE(mp.description, ''), mp.type, mp.infrastructure_id, mp.priority, 'open',
		       $3::uuid, $4, COALESCE(NULLIF($2, '')::uuid, mp.created_by), mp.cost_center_id, mp.responsible_to, $5::uuid,
		       $6::date, $7::date, $8, $9
		FROM maintenance_plans mp WHERE mp.id = $1::uuid AND mp.active
		RETURNING id::text`, planID, validUserID(createdBy), c.AssignedTo, c.Day, c.GroupID,
		c.CalcDay.Format("2006-01-02"), orderOn.Format("2006-01-02"), c.DateManual, c.StaffManual).Scan(&id); err != nil {
		return "", ignoreNoRows(err)
	}
	if c.VisitID != "" {
		if _, err := tx.Exec(ctx, `UPDATE maintenance_visits SET state = 'ordered', task_id = $2::uuid, updated_at = NOW() WHERE id = $1::uuid`, c.VisitID, id); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	runPlanTaskCreatedHooks(ctx, id, createdBy)
	return id, nil
}

// EnsurePlanTasks: wie EnsurePlanTasksAt mit der Uhr des Servers.
func (r *Repository) EnsurePlanTasks(ctx context.Context) (int, error) {
	return r.EnsurePlanTasksAt(ctx, time.Now())
}

// NextTerm: nächster Termin eines Plans nach einem Auftrag. TaskID leer =
// noch kein Auftrag; OrderOn = Tag, an dem der Auftrag entsteht (frühestens heute).
type NextTerm struct {
	Day     time.Time
	TaskID  string
	OrderOn time.Time
}

// NextTermForTask: nächster Termin des Plans eines Auftrags (nil ohne aktiven Plan).
func (r *Repository) NextTermForTask(ctx context.Context, taskID string, now time.Time) *NextTerm {
	var planID string
	if err := r.db.QueryRow(ctx, `SELECT mt.plan_id::text FROM maintenance_tasks mt JOIN maintenance_plans mp ON mp.id = mt.plan_id
		WHERE mt.id = $1::uuid AND mp.active`, taskID).Scan(&planID); err != nil {
		return nil
	}
	var otherID string
	var otherDue time.Time
	if r.db.QueryRow(ctx, `SELECT id::text, due_date FROM maintenance_tasks WHERE plan_id = $1::uuid AND id <> $2::uuid
		AND status IN ('open','in_progress','pending') ORDER BY due_date LIMIT 1`, planID, taskID).Scan(&otherID, &otherDue) == nil {
		return &NextTerm{Day: dayStart(otherDue), TaskID: otherID, OrderOn: dayStart(now)}
	}
	c, lead, err := r.nextCandidate(ctx, r.db, planID)
	if err != nil || c == nil {
		return nil
	}
	orderOn := r.OrderDay(ctx, c.Day, lead)
	if orderOn.Before(dayStart(now)) {
		orderOn = dayStart(now)
	}
	return &NextTerm{Day: c.Day, OrderOn: orderOn}
}

// EnsurePlanTasksAt: für jeden aktiven Plan ohne offenen Auftrag den
// Auftrag anlegen, wenn sein Vorlauf erreicht ist. Liefert die Zahl neuer Aufträge.
func (r *Repository) EnsurePlanTasksAt(ctx context.Context, now time.Time) (int, error) {
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
		tid, err := r.ensureTaskForPlan(ctx, id, "", now)
		if err != nil {
			return created, err
		}
		if tid != "" {
			created++
		}
	}
	return created, nil
}

// StartScheduler gleicht beim Start und danach stuendlich ab, dass jeder
// aktive Plan seinen naechsten Auftrag hat. skip (darf nil sein) lässt einen
// Lauf aus, z. B. während der Wartungssperre eines Updates.
func (s *Service) StartScheduler(ctx context.Context, skip func() bool) {
	var extended string // Tag des letzten Verlängerns der Jahrespläne
	run := func() {
		if skip != nil && skip() {
			return
		}
		now := time.Now()
		if d := now.Format("2006-01-02"); d != extended {
			if _, err := s.repo.ExtendYearPlans(ctx, now); err != nil {
				log.Warn().Err(err).Str("bereich", "wartung").Msg("jahrespläne: verlängern fehlgeschlagen")
			} else {
				extended = d
			}
		}
		n, err := s.repo.EnsurePlanTasksAt(ctx, now)
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
