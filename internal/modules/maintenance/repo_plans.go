package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// ErrInput kennzeichnet Eingabefehler (HTTP 400 statt 500).
var ErrInput = errors.New("ungültige eingabe")

func inputErr(msg string) error { return fmt.Errorf("%w: %s", ErrInput, msg) }

const planSelect = `
	SELECT mp.id::text, mp.name, COALESCE(mp.description,''), mp.type, mp.infrastructure_id::text,
	       mp.interval_unit, mp.interval_count, mp.schedule_mode, mp.lead_days, mp.estimated_min, mp.priority,
	       mp.assigned_to::text, mp.responsible_to::text, mp.assigned_group_id::text, mp.cost_center_id::text,
	       mp.active, mp.last_executed_at, mp.next_due_at, mp.created_by::text, mp.created_at,
	       mp.interval_type, mp.interval_days,
	       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,''), COALESCE(ru.first_name||' '||ru.last_name,''),
	       COALESCE(g.name,''), COALESCE(cc.number,''), COALESCE(cc.name,''),
	       COALESCE((SELECT mt.id::text FROM maintenance_tasks mt WHERE mt.plan_id = mp.id
	                 AND mt.status IN ('open','in_progress','pending') ORDER BY mt.due_date LIMIT 1), '')
	FROM maintenance_plans mp
	LEFT JOIN infrastructure i ON i.id = mp.infrastructure_id
	LEFT JOIN users u ON u.id = mp.assigned_to
	LEFT JOIN users ru ON ru.id = mp.responsible_to
	LEFT JOIN user_groups g ON g.id = mp.assigned_group_id
	LEFT JOIN cost_centers cc ON cc.id = mp.cost_center_id`

func scanPlan(row interface{ Scan(...interface{}) error }) (*MaintenancePlan, error) {
	p := &MaintenancePlan{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Type, &p.InfrastructureID,
		&p.IntervalUnit, &p.IntervalCount, &p.ScheduleMode, &p.LeadDays, &p.EstimatedMin, &p.Priority,
		&p.AssignedTo, &p.ResponsibleTo, &p.AssignedGroupID, &p.CostCenterID,
		&p.Active, &p.LastExecutedAt, &p.NextDueAt, &p.CreatedBy, &p.CreatedAt,
		&p.Interval, &p.IntervalDays,
		&p.InfraName, &p.AssigneeName, &p.ResponsibleName, &p.GroupName, &p.CostCenterNumber, &p.CostCenterName,
		&p.OpenTaskID)
	return p, err
}

// ListPlans: aktive (oder mit inactive=true alle) Plaene, optional je Anlage.
func (r *Repository) ListPlans(ctx context.Context, infraID string, inactive bool) ([]*MaintenancePlan, error) {
	q := planSelect + ` WHERE ($1 = '' OR mp.infrastructure_id::text = $1) AND (mp.active OR $2)
		ORDER BY COALESCE(i.name,''), mp.next_due_at, mp.name`
	rows, err := r.db.Query(ctx, q, infraID, inactive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MaintenancePlan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlan: ein Plan (auch inaktiv) samt Checklisten.
func (r *Repository) GetPlan(ctx context.Context, id string) (*MaintenancePlan, error) {
	p, err := scanPlan(r.db.QueryRow(ctx, planSelect+` WHERE mp.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	p.Checklists, err = r.PlanChecklists(ctx, id)
	return p, err
}

// normalizePlan prueft und vervollstaendigt die Eingabe.
func normalizePlan(in *PlanInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || len([]rune(in.Name)) > 255 {
		return inputErr("Bitte einen Namen angeben (höchstens 255 Zeichen)")
	}
	if strings.TrimSpace(in.InfrastructureID) == "" {
		return inputErr("Bitte eine Anlage wählen")
	}
	if in.IntervalUnit == "" && in.Interval != "" {
		in.IntervalUnit, in.IntervalCount = UnitFromLegacy(in.Interval)
	}
	switch in.IntervalUnit {
	case UnitDay, UnitWeek, UnitMonth, UnitYear:
	case "":
		in.IntervalUnit = UnitMonth
	default:
		return inputErr("Intervall: Tage, Wochen, Monate oder Jahre")
	}
	if in.IntervalCount < 1 {
		in.IntervalCount = 1
	}
	if in.IntervalCount > 1000 {
		return inputErr("Intervall zu groß")
	}
	if in.ScheduleMode != ScheduleFixed {
		in.ScheduleMode = ScheduleFromCompletion
	}
	if in.LeadDays < 0 || in.LeadDays > 365 {
		return inputErr("Vorlauf: 0 bis 365 Tage")
	}
	if in.EstimatedMin < 0 || in.EstimatedMin > 24*60*7 {
		return inputErr("Geplante Dauer ungültig")
	}
	switch in.Type {
	case PlanPreventive, PlanInspection, PlanCalibration, PlanCleaning:
	case "":
		in.Type = PlanPreventive
	default:
		return inputErr("Unbekannte Wartungsart")
	}
	switch in.Priority {
	case PrioLow, PrioMedium, PrioHigh, PrioCritical:
	case "":
		in.Priority = PrioMedium
	default:
		return inputErr("Unbekannte Priorität")
	}
	if in.NextDueAt == "" {
		in.NextDueAt = in.FirstDueAt
	}
	for i := range in.Checklists {
		c := &in.Checklists[i]
		switch c.RhythmUnit {
		case RhythmAlways, UnitDay, UnitWeek, UnitMonth, UnitYear:
		case "":
			c.RhythmUnit = RhythmAlways
		default:
			return inputErr("Takt der Checkliste ungültig")
		}
		if c.RhythmCount < 1 {
			c.RhythmCount = 1
		}
	}
	return nil
}

func nullID(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// CreatePlan legt einen Plan samt Checklisten an.
func (r *Repository) CreatePlan(ctx context.Context, in *PlanInput, userID string) (string, error) {
	if err := normalizePlan(in); err != nil {
		return "", err
	}
	next := dayStart(time.Now())
	if d, err := time.ParseInLocation("2006-01-02", in.NextDueAt, time.Local); err == nil {
		next = d
	}
	iv, days := LegacyInterval(in.IntervalUnit, in.IntervalCount)
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO maintenance_plans (name, description, type, infrastructure_id, interval_type, interval_days,
			interval_unit, interval_count, schedule_mode, lead_days, estimated_min, priority,
			assigned_to, responsible_to, assigned_group_id, cost_center_id, active, next_due_at, created_by)
		VALUES ($1, $2, $3::maintenance_type, $4::uuid, $5::maintenance_interval, $6,
			$7, $8, $9, $10, $11, $12::maintenance_priority,
			$13::uuid, $14::uuid, $15::uuid, $16::uuid, true, $17, $18::uuid)
		RETURNING id::text`,
		in.Name, in.Description, in.Type, in.InfrastructureID, iv, days,
		in.IntervalUnit, in.IntervalCount, in.ScheduleMode, in.LeadDays, in.EstimatedMin, in.Priority,
		nullID(in.AssignedTo), nullID(in.ResponsibleTo), nullID(in.AssignedGroupID), nullID(in.CostCenterID), next, userID).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, r.SetPlanChecklists(ctx, id, in.Checklists)
}

// UpdatePlan aendert alle Felder eines Plans samt Checklisten. Ein neuer
// naechster Termin wird auf den noch nicht begonnenen Auftrag uebertragen.
func (r *Repository) UpdatePlan(ctx context.Context, id string, in *PlanInput) error {
	if err := normalizePlan(in); err != nil {
		return err
	}
	iv, days := LegacyInterval(in.IntervalUnit, in.IntervalCount)
	var next *time.Time
	if d, err := time.ParseInLocation("2006-01-02", in.NextDueAt, time.Local); err == nil {
		next = &d
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE maintenance_plans SET name=$2, description=$3, type=$4::maintenance_type, infrastructure_id=$5::uuid,
			interval_type=$6::maintenance_interval, interval_days=$7, interval_unit=$8, interval_count=$9,
			schedule_mode=$10, lead_days=$11, estimated_min=$12, priority=$13::maintenance_priority,
			assigned_to=$14::uuid, responsible_to=$15::uuid, assigned_group_id=$16::uuid, cost_center_id=$17::uuid,
			next_due_at=COALESCE($18, next_due_at)
		WHERE id=$1::uuid`,
		id, in.Name, in.Description, in.Type, in.InfrastructureID, iv, days, in.IntervalUnit, in.IntervalCount,
		in.ScheduleMode, in.LeadDays, in.EstimatedMin, in.Priority,
		nullID(in.AssignedTo), nullID(in.ResponsibleTo), nullID(in.AssignedGroupID), nullID(in.CostCenterID), next)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Wartungsplan nicht gefunden")
	}
	// Stammdaten und Termin auf den offenen, noch nicht begonnenen Auftrag uebertragen
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_tasks mt SET title = mp.name, description = COALESCE(mp.description,''),
			type = mp.type, infrastructure_id = mp.infrastructure_id, priority = mp.priority, assigned_to = mp.assigned_to,
			responsible_to = mp.responsible_to, assigned_group_id = mp.assigned_group_id, cost_center_id = mp.cost_center_id,
			due_date = mp.next_due_at, updated_at = NOW()
		FROM maintenance_plans mp WHERE mp.id = $1::uuid AND mt.plan_id = mp.id AND mt.status = 'open'`, id); err != nil {
		return err
	}
	if err := r.SetPlanChecklists(ctx, id, in.Checklists); err != nil {
		return err
	}
	// noch unberuehrte Schritte des offenen Auftrags an die neuen Checklisten anpassen
	var taskID string
	if r.db.QueryRow(ctx, `SELECT id::text FROM maintenance_tasks WHERE plan_id=$1::uuid AND status='open' LIMIT 1`, id).Scan(&taskID) == nil {
		_, err = r.BuildSteps(ctx, taskID, true)
	}
	return err
}

// SetPlanActive: Plan aktivieren/deaktivieren. Beim Deaktivieren wird der
// offene, noch nicht begonnene Auftrag entfernt (der Plan ruht).
func (r *Repository) SetPlanActive(ctx context.Context, id string, active bool) error {
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_plans SET active=$2 WHERE id=$1::uuid`, id, active); err != nil {
		return err
	}
	if !active {
		_, err := r.db.Exec(ctx, `DELETE FROM maintenance_tasks mt WHERE mt.plan_id=$1::uuid AND mt.status='open'
			AND NOT EXISTS (SELECT 1 FROM maintenance_task_steps s WHERE s.task_id = mt.id AND (s.done OR s.value <> ''))
			AND NOT EXISTS (SELECT 1 FROM maintenance_task_pending_parts p WHERE p.task_id = mt.id)
			AND NOT EXISTS (SELECT 1 FROM maintenance_task_actions a WHERE a.task_id = mt.id)`, id)
		return err
	}
	_, err := r.ensureTaskForPlan(ctx, id, "")
	return err
}

// DuplicatePlan: Kopie eines Plans (inkl. Checklisten) als „… Kopie“.
func (r *Repository) DuplicatePlan(ctx context.Context, id, userID string) (string, error) {
	p, err := r.GetPlan(ctx, id)
	if err != nil {
		return "", err
	}
	in := &PlanInput{Name: p.Name + " Kopie", Description: p.Description, Type: p.Type, InfrastructureID: p.InfrastructureID,
		IntervalUnit: p.IntervalUnit, IntervalCount: p.IntervalCount, ScheduleMode: p.ScheduleMode, LeadDays: p.LeadDays,
		EstimatedMin: p.EstimatedMin, Priority: p.Priority, AssignedTo: p.AssignedTo, ResponsibleTo: p.ResponsibleTo,
		AssignedGroupID: p.AssignedGroupID, CostCenterID: p.CostCenterID, NextDueAt: p.NextDueAt.Local().Format("2006-01-02")}
	for _, c := range p.Checklists {
		in.Checklists = append(in.Checklists, PlanChecklistInput{TemplateID: c.TemplateID, RhythmUnit: c.RhythmUnit, RhythmCount: c.RhythmCount})
	}
	return r.CreatePlan(ctx, in, userID)
}

// PlanChecklists: Checklisten eines Plans mit Takt und „wieder faellig ab“.
func (r *Repository) PlanChecklists(ctx context.Context, planID string) ([]*PlanChecklist, error) {
	rows, err := r.db.Query(ctx, `SELECT pc.id::text, pc.plan_id::text, pc.template_id::text, t.name, pc.sort_order,
			pc.rhythm_unit, pc.rhythm_count, pc.last_done_at,
			(SELECT COUNT(*) FROM maintenance_checklist_template_items i WHERE i.template_id = t.id AND i.active)
		FROM maintenance_plan_checklists pc JOIN maintenance_checklist_templates t ON t.id = pc.template_id
		WHERE pc.plan_id = $1::uuid ORDER BY pc.sort_order, t.name`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PlanChecklist{}
	for rows.Next() {
		c := &PlanChecklist{}
		if err := rows.Scan(&c.ID, &c.PlanID, &c.TemplateID, &c.TemplateName, &c.SortOrder,
			&c.RhythmUnit, &c.RhythmCount, &c.LastDoneAt, &c.ItemCount); err != nil {
			return nil, err
		}
		if c.RhythmUnit != RhythmAlways && c.LastDoneAt != nil {
			n := AddInterval(c.RhythmUnit, c.RhythmCount, *c.LastDoneAt)
			c.NextFrom = &n
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetPlanChecklists ersetzt die Checklisten eines Plans (Reihenfolge = Liste).
// „Zuletzt erledigt“ bleibt fuer weiter zugeordnete Vorlagen erhalten.
func (r *Repository) SetPlanChecklists(ctx context.Context, planID string, list []PlanChecklistInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	keep := []string{}
	for i, c := range list {
		if strings.TrimSpace(c.TemplateID) == "" {
			continue
		}
		keep = append(keep, c.TemplateID)
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_plan_checklists (plan_id, template_id, sort_order, rhythm_unit, rhythm_count)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5)
			ON CONFLICT (plan_id, template_id) DO UPDATE SET sort_order=EXCLUDED.sort_order,
				rhythm_unit=EXCLUDED.rhythm_unit, rhythm_count=EXCLUDED.rhythm_count`,
			planID, c.TemplateID, (i+1)*10, c.RhythmUnit, c.RhythmCount); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM maintenance_plan_checklists WHERE plan_id=$1::uuid AND NOT (template_id::text = ANY($2))`, planID, keep); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
