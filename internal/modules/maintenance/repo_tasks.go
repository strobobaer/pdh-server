package maintenance

import (
	"context"
	"strings"
	"time"
)

const taskSelect = `
	SELECT mt.id::text, mt.plan_id::text, mt.title, COALESCE(mt.description,''), mt.type, mt.infrastructure_id::text,
	       mt.priority, mt.status, mt.assigned_to::text, mt.responsible_to::text, mt.assigned_group_id::text,
	       mt.due_date, mt.started_at, mt.completed_at, mt.duration_min, COALESCE(mt.notes,''),
	       mt.created_by::text, mt.created_at, mt.cost_center_id::text,
	       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,''), COALESCE(ru.first_name||' '||ru.last_name,''),
	       COALESCE(g.name,''), COALESCE(mp.name,''), COALESCE(mp.lead_days,0), COALESCE(cc.number,''), COALESCE(cc.name,'')
	FROM maintenance_tasks mt
	LEFT JOIN infrastructure i ON i.id = mt.infrastructure_id
	LEFT JOIN users u ON u.id = mt.assigned_to
	LEFT JOIN users ru ON ru.id = mt.responsible_to
	LEFT JOIN user_groups g ON g.id = mt.assigned_group_id
	LEFT JOIN maintenance_plans mp ON mp.id = mt.plan_id
	LEFT JOIN cost_centers cc ON cc.id = mt.cost_center_id`

func scanTask(row interface{ Scan(...interface{}) error }) (*MaintenanceTask, error) {
	t := &MaintenanceTask{}
	err := row.Scan(&t.ID, &t.PlanID, &t.Title, &t.Description, &t.Type, &t.InfrastructureID,
		&t.Priority, &t.Status, &t.AssignedTo, &t.ResponsibleTo, &t.AssignedGroupID,
		&t.DueDate, &t.StartedAt, &t.CompletedAt, &t.DurationMin, &t.Notes,
		&t.CreatedBy, &t.CreatedAt, &t.CostCenterID,
		&t.InfraName, &t.AssigneeName, &t.ResponsibleName, &t.GroupName, &t.PlanName, &t.LeadDays,
		&t.CostCenterNumber, &t.CostCenterName)
	return t, err
}

// TaskFilter: Auswahl fuer ListTasks (leere Felder = alle).
type TaskFilter struct {
	Status   TaskStatus // einzelner Status
	Open     bool       // offen + in Arbeit + wartet
	Closed   bool       // erledigt + uebersprungen
	InfraID  string
	PlanID   string
	DueUntil *time.Time // Faelligkeit bis einschliesslich (Tag)
	Limit    int
}

func (r *Repository) queryTasks(ctx context.Context, f TaskFilter, order string) ([]*MaintenanceTask, error) {
	var where []string
	args := []interface{}{}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	if f.Status != "" {
		add("mt.status::text = ?", string(f.Status))
	}
	if f.Open {
		where = append(where, "mt.status IN ('open','in_progress','pending')")
	}
	if f.Closed {
		where = append(where, "mt.status IN ('done','skipped')")
	}
	if f.InfraID != "" {
		add("mt.infrastructure_id::text = ?", f.InfraID)
	}
	if f.PlanID != "" {
		add("mt.plan_id::text = ?", f.PlanID)
	}
	if f.DueUntil != nil {
		add("mt.due_date::date <= ?::date", dayStart(*f.DueUntil))
	}
	q := taskSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + order
	if f.Limit > 0 {
		q += " LIMIT " + itoa(f.Limit)
	}
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MaintenanceTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ListTasks: Auftraege nach Status/Anlage (Prioritaet, dann Faelligkeit).
func (r *Repository) ListTasks(ctx context.Context, status TaskStatus, infraID string) ([]*MaintenanceTask, error) {
	return r.queryTasks(ctx, TaskFilter{Status: status, InfraID: infraID},
		"CASE mt.priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, mt.due_date")
}

// FindTasks: Auftraege nach Filter, sortiert nach Faelligkeit (erledigte: neueste zuerst).
func (r *Repository) FindTasks(ctx context.Context, f TaskFilter) ([]*MaintenanceTask, error) {
	order := "mt.due_date, mt.title"
	if f.Closed {
		order = "COALESCE(mt.completed_at, mt.due_date) DESC"
	}
	return r.queryTasks(ctx, f, order)
}

func (r *Repository) GetTaskByID(ctx context.Context, id string) (*MaintenanceTask, error) {
	return scanTask(r.db.QueryRow(ctx, taskSelect+` WHERE mt.id = $1::uuid`, id))
}

// GetDueToday: offene Auftraege, die bis uebermorgen faellig sind.
func (r *Repository) GetDueToday(ctx context.Context) ([]*MaintenanceTask, error) {
	until := time.Now().AddDate(0, 0, 2)
	return r.queryTasks(ctx, TaskFilter{Open: true, DueUntil: &until},
		"CASE mt.priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, mt.due_date")
}

// CreateTask: Auftrag anlegen (Verantwortliche/Gruppe vom Plan, falls vorhanden).
func (r *Repository) CreateTask(ctx context.Context, t *MaintenanceTask) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO maintenance_tasks
		  (plan_id, title, description, type, infrastructure_id, priority, status, assigned_to, due_date, created_by, cost_center_id,
		   responsible_to, assigned_group_id)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, 'open', $7::uuid, $8, $9::uuid, $10::uuid,
		        (SELECT mp.responsible_to FROM maintenance_plans mp WHERE mp.id = $1::uuid),
		        (SELECT mp.assigned_group_id FROM maintenance_plans mp WHERE mp.id = $1::uuid))
		RETURNING id::text, status, created_at`,
		t.PlanID, t.Title, t.Description, t.Type, t.InfrastructureID, t.Priority, nullID(t.AssignedTo), t.DueDate, t.CreatedBy,
		nullID(t.CostCenterID)).Scan(&t.ID, &t.Status, &t.CreatedAt)
}

// UpdateTask: Titel, Beschreibung, Anlage, Prioritaet, Termin, Bemerkung, Kostenstelle.
func (r *Repository) UpdateTask(ctx context.Context, id string, in *UpdateTaskInput) error {
	var due *time.Time
	if d, err := time.ParseInLocation("2006-01-02", in.DueDate, time.Local); err == nil {
		due = &d
	}
	_, err := r.db.Exec(ctx, `UPDATE maintenance_tasks SET title=$2, description=$3,
			infrastructure_id=COALESCE(NULLIF($4,'')::uuid, infrastructure_id), priority=$5, due_date=COALESCE($6, due_date),
			notes=$7, cost_center_id=$8::uuid, updated_at=NOW()
		WHERE id=$1::uuid`, id, in.Title, in.Description, in.InfrastructureID, in.Priority, due, in.Notes, nullID(in.CostCenterID))
	return err
}

// UpdateDueDate setzt ausschliesslich den Termin (z. B. per Ziehen im Zeitstrahl).
func (r *Repository) UpdateDueDate(ctx context.Context, id string, due time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE maintenance_tasks SET due_date=$2, updated_at=NOW() WHERE id=$1::uuid`, id, due)
	return err
}

// SetStatus: Statuswechsel innerhalb der offenen Zustaende (Starten, Warten).
func (r *Repository) SetStatus(ctx context.Context, id string, status TaskStatus) error {
	q := `UPDATE maintenance_tasks SET status=$2, updated_at=NOW()`
	if status == TaskInProgress {
		q += `, started_at=COALESCE(started_at, NOW())`
	}
	tag, err := r.db.Exec(ctx, q+` WHERE id=$1::uuid AND status IN ('open','in_progress','pending')`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Der Auftrag ist nicht mehr offen")
	}
	return nil
}

// Assign: Auftrag einer Person zuweisen (Leitstand „Annehmen“) und starten.
func (r *Repository) Assign(ctx context.Context, id, userID string) error {
	tag, err := r.db.Exec(ctx, `UPDATE maintenance_tasks SET assigned_to=$2::uuid, status='in_progress',
			started_at=COALESCE(started_at, NOW()), updated_at=NOW()
		WHERE id=$1::uuid AND status IN ('open','in_progress','pending')`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Der Auftrag wurde zwischenzeitlich geändert")
	}
	return nil
}

// markDone / markSkipped: Abschluss-Zeile (Pruefungen im Service).
func (r *Repository) markDone(ctx context.Context, id, notes string, durationMin int, noParts bool) error {
	tag, err := r.db.Exec(ctx, `UPDATE maintenance_tasks SET status='done', completed_at=NOW(), notes=$2,
			duration_min=NULLIF($3, 0), no_parts_needed=$4, updated_at=NOW()
		WHERE id=$1::uuid AND status IN ('open','in_progress','pending')`, id, notes, durationMin, noParts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Der Auftrag ist bereits abgeschlossen")
	}
	return nil
}

func (r *Repository) markSkipped(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `UPDATE maintenance_tasks SET status='skipped', updated_at=NOW()
		WHERE id=$1::uuid AND status IN ('open','in_progress','pending')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Der Auftrag wurde zwischenzeitlich geändert")
	}
	return nil
}

func (r *Repository) DeleteTask(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_tasks WHERE id=$1::uuid`, id)
	return err
}

// ── Massnahmen ───────────────────────────────────────────────

func (r *Repository) AddAction(ctx context.Context, taskID, description, userID string) (*TaskAction, error) {
	a := &TaskAction{TaskID: taskID, Description: description, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_task_actions (task_id, description, created_by)
		VALUES ($1::uuid, $2, $3::uuid) RETURNING id::text, created_at`, taskID, description, userID).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (r *Repository) GetActions(ctx context.Context, taskID string) ([]*TaskAction, error) {
	rows, err := r.db.Query(ctx, `SELECT a.id::text, a.task_id::text, a.description, a.created_by::text,
			COALESCE(u.first_name||' '||u.last_name,''), a.created_at
		FROM maintenance_task_actions a LEFT JOIN users u ON u.id = a.created_by
		WHERE a.task_id=$1::uuid ORDER BY a.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskAction{}
	for rows.Next() {
		a := &TaskAction{}
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Description, &a.CreatedBy, &a.CreatedByName, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) CountActions(ctx context.Context, taskID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_actions WHERE task_id=$1::uuid`, taskID).Scan(&n)
	return n, err
}

func (r *Repository) DeleteAction(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_task_actions WHERE id=$1::uuid`, id)
	return err
}

// ── Ersatzteile ──────────────────────────────────────────────

func (r *Repository) AddPendingPart(ctx context.Context, taskID, partID, storageNodeID string, qty float64, userID string) (*PendingPart, error) {
	p := &PendingPart{TaskID: taskID, PartID: partID, StorageNodeID: storageNodeID, Qty: qty, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_task_pending_parts (task_id, part_id, storage_node_id, qty, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid) RETURNING id::text, created_at`,
		taskID, partID, storageNodeID, qty, userID).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetPendingParts(ctx context.Context, taskID string) ([]*PendingPart, error) {
	rows, err := r.db.Query(ctx, `SELECT tp.id::text, tp.task_id::text, tp.part_id::text, sp.name, sp.part_number, tp.reserved,
			tp.storage_node_id::text, sn.name, tp.qty, tp.created_by::text, COALESCE(u.first_name||' '||u.last_name,''), tp.created_at
		FROM maintenance_task_pending_parts tp
		JOIN spare_parts sp ON sp.id = tp.part_id
		JOIN storage_nodes sn ON sn.id = tp.storage_node_id
		LEFT JOIN users u ON u.id = tp.created_by
		WHERE tp.task_id=$1::uuid ORDER BY tp.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PendingPart{}
	for rows.Next() {
		p := &PendingPart{}
		if err := rows.Scan(&p.ID, &p.TaskID, &p.PartID, &p.PartName, &p.PartNumber, &p.Reserved,
			&p.StorageNodeID, &p.StorageName, &p.Qty, &p.CreatedBy, &p.CreatedByName, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) DeletePendingPart(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_task_pending_parts WHERE id=$1::uuid`, id)
	return err
}

func (r *Repository) GetPartsUsage(ctx context.Context, taskID string) ([]*PartUsage, error) {
	rows, err := r.db.Query(ctx, `SELECT sm.id::text, sm.part_id::text, sp.name, sp.part_number, sm.qty, COALESCE(sm.created_by::text, ''),
			COALESCE(u.first_name||' '||u.last_name,''), sm.created_at
		FROM stock_movements sm JOIN spare_parts sp ON sp.id = sm.part_id LEFT JOIN users u ON u.id = sm.created_by
		WHERE sm.maintenance_task_id=$1::uuid ORDER BY sm.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PartUsage{}
	for rows.Next() {
		u := &PartUsage{}
		if err := rows.Scan(&u.ID, &u.PartID, &u.PartName, &u.PartNumber, &u.Qty, &u.CreatedBy, &u.CreatedByName, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PlannedMinutes: geplante Dauer laut Plan (Vorschlag im Abschluss-Assistenten).
func (r *Repository) PlannedMinutes(ctx context.Context, taskID string) int {
	var min int
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(mp.estimated_min, 0) FROM maintenance_tasks mt JOIN maintenance_plans mp ON mp.id = mt.plan_id
		WHERE mt.id = $1::uuid`, taskID).Scan(&min)
	return min
}

// StepBelongsToTask: Schritt gehoert zum Auftrag (fuer Foto-Upload).
func (r *Repository) StepBelongsToTask(ctx context.Context, taskID, stepID string) bool {
	var ok bool
	_ = r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_task_steps WHERE id=$1::uuid AND task_id=$2::uuid)`, stepID, taskID).Scan(&ok)
	return ok
}

// TaskMark: Auftrag eines Plans im Zeitraum (Jahresplan).
type TaskMark struct {
	TaskID    string
	PlanID    string
	Status    TaskStatus
	Due       time.Time
	Completed *time.Time
}

// PlanTaskMarks: Plan-Auftraege, die im Zeitraum faellig waren oder erledigt wurden.
func (r *Repository) PlanTaskMarks(ctx context.Context, from, to time.Time) ([]TaskMark, error) {
	rows, err := r.db.Query(ctx, `SELECT id::text, plan_id::text, status, due_date, completed_at FROM maintenance_tasks
		WHERE plan_id IS NOT NULL AND (
			(status IN ('open','in_progress','pending') AND due_date >= $1 AND due_date < $2)
			OR (status IN ('done','skipped') AND COALESCE(completed_at, due_date) >= $1 AND COALESCE(completed_at, due_date) < $2))`,
		dayStart(from), dayStart(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskMark
	for rows.Next() {
		var m TaskMark
		if err := rows.Scan(&m.TaskID, &m.PlanID, &m.Status, &m.Due, &m.Completed); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
