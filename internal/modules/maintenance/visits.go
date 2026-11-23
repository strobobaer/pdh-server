package maintenance

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Geplante Termine (Jahresplan): Termine eines Plans für die nächsten 12
// Monate, einzeln verschieb- und besetzbar. Ein geplanter Termin ist kein
// Auftrag; der Auftrag entsteht erst mit dem Vorlauf. Der gerechnete Tag
// (calc_day) bleibt der Rhythmus, auch wenn der Termin verschoben wurde.

const (
	VisitPlanned = "planned"
	VisitOrdered = "ordered"
	VisitDropped = "dropped"
)

// ErrVisitNotFound: den geplanten Termin gibt es nicht.
var ErrVisitNotFound = errors.New("Geplanter Termin nicht gefunden")

// Visit ist ein geplanter Termin; Tage als 0 Uhr Ortszeit.
type Visit struct {
	ID, PlanID, PlanName, InfraID, InfraName string
	CalcDay, Day                             time.Time
	AssignedTo, GroupID, ResponsibleTo       *string
	AssigneeName, GroupName                  string
	DateManual, StaffManual, OffPlan         bool
	State                                    string
	TaskID                                   *string
	EstimatedMin                             int
}

const visitSelect = `
	SELECT v.id::text, v.plan_id::text, mp.name, mp.infrastructure_id::text, COALESCE(i.name, ''),
	       v.calc_day, v.day, v.assigned_to::text, v.assigned_group_id::text, mp.responsible_to::text,
	       COALESCE(u.first_name || ' ' || u.last_name, ''), COALESCE(g.name, ''),
	       v.date_manual, v.staff_manual, v.off_plan, v.state, v.task_id::text, mp.estimated_min
	FROM maintenance_visits v
	JOIN maintenance_plans mp ON mp.id = v.plan_id
	LEFT JOIN infrastructure i ON i.id = mp.infrastructure_id
	LEFT JOIN users u ON u.id = v.assigned_to
	LEFT JOIN user_groups g ON g.id = v.assigned_group_id`

func (r *Repository) scanVisits(ctx context.Context, where string, args ...any) ([]Visit, error) {
	rows, err := r.db.Query(ctx, visitSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Visit
	for rows.Next() {
		var v Visit
		if err := rows.Scan(&v.ID, &v.PlanID, &v.PlanName, &v.InfraID, &v.InfraName, &v.CalcDay, &v.Day,
			&v.AssignedTo, &v.GroupID, &v.ResponsibleTo, &v.AssigneeName, &v.GroupName,
			&v.DateManual, &v.StaffManual, &v.OffPlan, &v.State, &v.TaskID, &v.EstimatedMin); err != nil {
			return nil, err
		}
		v.CalcDay, v.Day = civil(v.CalcDay), civil(v.Day)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVisit liest einen geplanten Termin.
func (r *Repository) GetVisit(ctx context.Context, id string) (*Visit, error) {
	if len(id) != 36 {
		return nil, ErrVisitNotFound
	}
	vs, err := r.scanVisits(ctx, `WHERE v.id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, ErrVisitNotFound
	}
	return &vs[0], nil
}

// PlanVisits: alle Termine eines Plans, nach Tag.
func (r *Repository) PlanVisits(ctx context.Context, planID string) ([]Visit, error) {
	return r.scanVisits(ctx, `WHERE v.plan_id = $1::uuid ORDER BY v.day, v.calc_day`, planID)
}

// VisitsInRange: Termine aktiver Pläne mit Tag zwischen from und to (beide
// eingeschlossen), ohne Angabe die geplanten und entfallenen; nach Tag und Plan.
func (r *Repository) VisitsInRange(ctx context.Context, from, to time.Time, states ...string) ([]Visit, error) {
	if len(states) == 0 {
		states = []string{VisitPlanned, VisitDropped}
	}
	return r.scanVisits(ctx, `WHERE mp.active AND v.day BETWEEN $1::date AND $2::date AND v.state = ANY($3::text[])
		ORDER BY v.day, mp.name, v.id`, from.Format("2006-01-02"), to.Format("2006-01-02"), states)
}

// lockPlan: Sperre je Plan (wie beim Anlegen des Auftrags).
func lockPlan(ctx context.Context, tx pgx.Tx, planID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('maintenance_plan:' || $1::text))`, planID)
	return err
}

// GenerateYearPlan legt die fehlenden Termine bis heute + 12 Monate an.
func (r *Repository) GenerateYearPlan(ctx context.Context, planID, userID string, now time.Time) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := lockPlan(ctx, tx, planID); err != nil {
		return 0, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM maintenance_plans WHERE id = $1::uuid`, planID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, inputErr("Wartungsplan nicht gefunden")
		}
		return 0, err
	}
	if !active {
		return 0, inputErr("Der Plan ruht – bitte zuerst aktivieren")
	}
	n, _, err := r.fillVisits(ctx, tx, planID, userID, now)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE maintenance_plans SET year_plan_at = COALESCE(year_plan_at, NOW()) WHERE id = $1::uuid`, planID); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// fillVisits legt die gerechneten Tage an, die es noch nicht gibt (als Termin
// in beliebigem Zustand oder als offener Auftrag), mit der Besetzung des
// Plans. Liefert die Zahl neuer Termine und alle gerechneten Tage.
func (r *Repository) fillVisits(ctx context.Context, tx pgx.Tx, planID, userID string, now time.Time) (int, []string, error) {
	var unit string
	var count int
	var next time.Time
	if err := tx.QueryRow(ctx, `SELECT interval_unit, interval_count, next_due_at FROM maintenance_plans WHERE id = $1::uuid`, planID).
		Scan(&unit, &count, &next); err != nil {
		return 0, nil, err
	}
	days := PlannedDates(unit, count, next.Local(), now.AddDate(1, 0, 0), 400)
	list := make([]string, 0, len(days))
	for _, d := range days {
		list = append(list, d.Format("2006-01-02"))
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO maintenance_visits (plan_id, calc_day, day, assigned_to, assigned_group_id, created_by, updated_by)
		SELECT mp.id, d::date, d::date, mp.assigned_to, mp.assigned_group_id, NULLIF($3, '')::uuid, NULLIF($3, '')::uuid
		FROM maintenance_plans mp, unnest($2::text[]) AS d
		WHERE mp.id = $1::uuid
		  AND NOT EXISTS (SELECT 1 FROM maintenance_visits v WHERE v.plan_id = mp.id AND v.calc_day = d::date)
		  AND NOT EXISTS (SELECT 1 FROM maintenance_tasks mt WHERE mt.plan_id = mp.id
		                  AND mt.status IN ('open', 'in_progress', 'pending')
		                  AND COALESCE(mt.planned_for, mt.due_date::date) = d::date)`, planID, list, validUserID(userID))
	if err != nil {
		return 0, nil, err
	}
	return int(tag.RowsAffected()), list, nil
}

// validUserID: leer, wenn keine gültige Kennung (Hintergrundlauf).
func validUserID(id string) string {
	if len(id) == 36 {
		return id
	}
	return ""
}

// ExtendYearPlans verlängert die Jahrespläne, deren letzter Termin weniger
// als einen Monat vor „heute + 12 Monate“ liegt, bis heute + 12 Monate.
func (r *Repository) ExtendYearPlans(ctx context.Context, now time.Time) (int, error) {
	limit := now.AddDate(1, -1, 0).Format("2006-01-02")
	rows, err := r.db.Query(ctx, `SELECT mp.id::text FROM maintenance_plans mp
		WHERE mp.active AND mp.year_plan_at IS NOT NULL
		  AND COALESCE((SELECT max(v.calc_day) FROM maintenance_visits v WHERE v.plan_id = mp.id), '0001-01-01') < $1::date`, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := r.GenerateYearPlan(ctx, id, "", now); err != nil && !IsInputError(err) {
			return 0, err
		}
	}
	return len(ids), nil
}

// recalcVisits: geplante Termine ohne Handänderung neu aus dem Rhythmus; von
// Hand geänderte bleiben und zeigen „weicht vom Plan ab“, wenn ihr
// gerechneter Tag nicht mehr im Rhythmus liegt. Läuft in tx mit Sperre.
func (r *Repository) recalcVisits(ctx context.Context, tx pgx.Tx, planID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM maintenance_visits WHERE plan_id = $1::uuid AND state = 'planned'
		AND NOT date_manual AND NOT staff_manual`, planID); err != nil {
		return err
	}
	_, list, err := r.fillVisits(ctx, tx, planID, "", now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE maintenance_visits SET off_plan = NOT (calc_day = ANY($2::date[]))
		WHERE plan_id = $1::uuid AND state = 'planned'`, planID, list)
	return err
}

// RecalcVisits: wie recalcVisits in eigener Transaktion (nur bei erzeugtem Jahresplan).
func (r *Repository) RecalcVisits(ctx context.Context, planID string, now time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockPlan(ctx, tx, planID); err != nil {
		return err
	}
	var on bool
	if err := tx.QueryRow(ctx, `SELECT year_plan_at IS NOT NULL AND active FROM maintenance_plans WHERE id = $1::uuid`, planID).Scan(&on); err != nil {
		return ignoreNoRows(err)
	}
	if !on {
		return nil
	}
	if err := r.recalcVisits(ctx, tx, planID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// plannedVisit: Termin lesen und prüfen, dass er noch geplant ist.
func (r *Repository) plannedVisit(ctx context.Context, id string) (*Visit, error) {
	v, err := r.GetVisit(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.State != VisitPlanned {
		return nil, inputErr("Aus diesem Termin ist schon ein Auftrag geworden")
	}
	return v, nil
}

// MoveVisit verschiebt einen geplanten Termin auf einen anderen Tag.
func (r *Repository) MoveVisit(ctx context.Context, visitID string, day time.Time, userID string, now time.Time) error {
	if _, err := r.plannedVisit(ctx, visitID); err != nil {
		return err
	}
	if civil(day).Before(dayStart(now)) {
		return inputErr("Ein geplanter Termin lässt sich nicht in die Vergangenheit legen")
	}
	_, err := r.db.Exec(ctx, `UPDATE maintenance_visits SET day = $2::date, date_manual = true,
		updated_by = NULLIF($3, '')::uuid, updated_at = NOW() WHERE id = $1::uuid AND state = 'planned'`,
		visitID, civil(day).Format("2006-01-02"), validUserID(userID))
	return err
}

// StaffVisit besetzt einen geplanten Termin (nil = niemand).
func (r *Repository) StaffVisit(ctx context.Context, visitID string, assignedTo, groupID *string, userID string) error {
	if _, err := r.plannedVisit(ctx, visitID); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `UPDATE maintenance_visits SET assigned_to = $2::uuid, assigned_group_id = $3::uuid,
		staff_manual = true, updated_by = NULLIF($4, '')::uuid, updated_at = NOW() WHERE id = $1::uuid AND state = 'planned'`,
		visitID, nullID(assignedTo), nullID(groupID), validUserID(userID))
	return err
}

// ResetVisit: „Zurück zum Plan“ – Handänderungen aufheben und den Plan neu
// rechnen (der Termin entsteht neu bzw. entfällt, wenn sein Tag nicht mehr
// im Rhythmus liegt).
func (r *Repository) ResetVisit(ctx context.Context, visitID, userID string, now time.Time) error {
	v, err := r.plannedVisit(ctx, visitID)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockPlan(ctx, tx, v.PlanID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE maintenance_visits SET date_manual = false, staff_manual = false, off_plan = false,
		updated_by = NULLIF($2, '')::uuid, updated_at = NOW() WHERE id = $1::uuid AND state = 'planned'`, visitID, validUserID(userID)); err != nil {
		return err
	}
	if err := r.recalcVisits(ctx, tx, v.PlanID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Service-Hüllen mit der Uhr des Servers.

func (s *Service) GenerateYearPlan(ctx context.Context, planID, userID string) (int, error) {
	return s.repo.GenerateYearPlan(ctx, planID, userID, time.Now())
}

func (s *Service) MoveVisit(ctx context.Context, visitID string, day time.Time, userID string) error {
	return s.repo.MoveVisit(ctx, visitID, day, userID, time.Now())
}

func (s *Service) StaffVisit(ctx context.Context, visitID string, assignedTo, groupID *string, userID string) error {
	return s.repo.StaffVisit(ctx, visitID, assignedTo, groupID, userID)
}

func (s *Service) ResetVisit(ctx context.Context, visitID, userID string) error {
	return s.repo.ResetVisit(ctx, visitID, userID, time.Now())
}

func (s *Service) GetVisit(ctx context.Context, id string) (*Visit, error) { return s.repo.GetVisit(ctx, id) }
