package maintenance

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type ChecklistTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

type ChecklistTemplateItem struct {
	ID           string    `json:"id"`
	TemplateID   string    `json:"template_id"`
	Label        string    `json:"label"`
	Description  string    `json:"description"`
	ItemType     string    `json:"item_type"`
	Required     bool      `json:"required"`
	IntervalDays int       `json:"interval_days"`
	SortOrder    int       `json:"sort_order"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	Due          bool      `json:"due"`
	LastDoneAt   *time.Time `json:"last_done_at,omitempty"`
}

type TaskChecklistItem struct {
	ID             string     `json:"id"`
	TemplateItemID string     `json:"template_item_id"`
	Label          string     `json:"label"`
	Description    string     `json:"description"`
	ItemType       string     `json:"item_type"`
	Required       bool       `json:"required"`
	IntervalDays   int        `json:"interval_days"`
	Value          string     `json:"value"`
	Done           bool       `json:"done"`
	LastDoneAt     *time.Time `json:"last_done_at,omitempty"`
}

func (r *Repository) ensureChecklistTemplateTables(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS checklist_template_id UUID NULL;
		ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS default_duration_min INTEGER NOT NULL DEFAULT 0;
		CREATE TABLE IF NOT EXISTS maintenance_checklist_templates (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT true,
			created_by UUID NULL REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_maintenance_checklist_templates_name_active
			ON maintenance_checklist_templates (lower(name)) WHERE active=true;
		CREATE TABLE IF NOT EXISTS maintenance_checklist_template_items (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			template_id UUID NOT NULL REFERENCES maintenance_checklist_templates(id) ON DELETE CASCADE,
			label TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			item_type TEXT NOT NULL DEFAULT 'checkbox',
			required BOOLEAN NOT NULL DEFAULT true,
			interval_days INTEGER NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 100,
			active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT chk_maintenance_template_item_interval_days_positive CHECK (interval_days > 0)
		);
		CREATE INDEX IF NOT EXISTS idx_maintenance_template_items_template
			ON maintenance_checklist_template_items(template_id, active, sort_order);
		CREATE TABLE IF NOT EXISTS maintenance_task_checklist_results (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			task_id UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
			template_item_id UUID NOT NULL REFERENCES maintenance_checklist_template_items(id) ON DELETE CASCADE,
			value TEXT NOT NULL DEFAULT '',
			done BOOLEAN NOT NULL DEFAULT false,
			checked_by UUID NULL REFERENCES users(id) ON DELETE SET NULL,
			checked_at TIMESTAMPTZ NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(task_id, template_item_id)
		);
		CREATE INDEX IF NOT EXISTS idx_maintenance_task_checklist_results_task
			ON maintenance_task_checklist_results(task_id);
		CREATE INDEX IF NOT EXISTS idx_maintenance_task_checklist_results_item_checked
			ON maintenance_task_checklist_results(template_item_id, checked_at);
	`)
	return err
}

func (r *Repository) CreateChecklistTemplate(ctx context.Context, name, description, userID string) (*ChecklistTemplate, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	t := &ChecklistTemplate{Name: name, Description: description}
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_checklist_templates (id, name, description, created_by) VALUES (gen_random_uuid(), $1, $2, NULLIF($3,'')::uuid) RETURNING id, active, created_at`, name, description, userID).Scan(&t.ID, &t.Active, &t.CreatedAt)
	return t, err
}

func (r *Repository) ListChecklistTemplates(ctx context.Context) ([]*ChecklistTemplate, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `SELECT id, name, description, active, created_at FROM maintenance_checklist_templates WHERE active=true ORDER BY name`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*ChecklistTemplate
	for rows.Next() { t := &ChecklistTemplate{}; if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Active, &t.CreatedAt); err != nil { return nil, err }; out = append(out, t) }
	return out, rows.Err()
}

func (r *Repository) CreateChecklistTemplateItem(ctx context.Context, item *ChecklistTemplateItem) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	return r.db.QueryRow(ctx, `INSERT INTO maintenance_checklist_template_items (id, template_id, label, description, item_type, required, interval_days, sort_order) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7) RETURNING id, active, created_at`, item.TemplateID, item.Label, item.Description, item.ItemType, item.Required, item.IntervalDays, item.SortOrder).Scan(&item.ID, &item.Active, &item.CreatedAt)
}

func (r *Repository) ListChecklistTemplateItems(ctx context.Context, templateID string) ([]*ChecklistTemplateItem, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `SELECT id, template_id, label, description, item_type, required, interval_days, sort_order, active, created_at FROM maintenance_checklist_template_items WHERE active=true AND template_id=$1 ORDER BY sort_order, label`, templateID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*ChecklistTemplateItem
	for rows.Next() { item := &ChecklistTemplateItem{}; if err := rows.Scan(&item.ID, &item.TemplateID, &item.Label, &item.Description, &item.ItemType, &item.Required, &item.IntervalDays, &item.SortOrder, &item.Active, &item.CreatedAt); err != nil { return nil, err }; out = append(out, item) }
	return out, rows.Err()
}

func (r *Repository) AssignChecklistTemplateToPlan(ctx context.Context, planID, templateID string, defaultDurationMin int) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	_, err := r.db.Exec(ctx, `UPDATE maintenance_plans SET checklist_template_id=NULLIF($1,'')::uuid, default_duration_min=$2 WHERE id=$3`, templateID, defaultDurationMin, planID)
	return err
}

func (r *Repository) DueChecklistItemsForTask(ctx context.Context, taskID string) ([]*TaskChecklistItem, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `
		WITH task_plan AS (
			SELECT mt.id AS task_id, mt.plan_id, mp.checklist_template_id
			FROM maintenance_tasks mt
			JOIN maintenance_plans mp ON mt.plan_id=mp.id
			WHERE mt.id=$1 AND mp.checklist_template_id IS NOT NULL
		), last_done AS (
			SELECT template_item_id, max(checked_at) AS last_done_at
			FROM maintenance_task_checklist_results
			WHERE done=true AND checked_at IS NOT NULL
			GROUP BY template_item_id
		)
		SELECT COALESCE(r.id::text,''), i.id::text, i.label, i.description, i.item_type, i.required, i.interval_days,
		       COALESCE(r.value,''), COALESCE(r.done,false), ld.last_done_at
		FROM task_plan tp
		JOIN maintenance_checklist_template_items i ON i.template_id=tp.checklist_template_id AND i.active=true
		LEFT JOIN maintenance_task_checklist_results r ON r.task_id=tp.task_id AND r.template_item_id=i.id
		LEFT JOIN last_done ld ON ld.template_item_id=i.id
		WHERE ld.last_done_at IS NULL OR ld.last_done_at + (i.interval_days || ' days')::interval <= NOW()
		ORDER BY i.sort_order, i.label`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*TaskChecklistItem
	for rows.Next() {
		item := &TaskChecklistItem{}
		var resultID string
		if err := rows.Scan(&resultID, &item.TemplateItemID, &item.Label, &item.Description, &item.ItemType, &item.Required, &item.IntervalDays, &item.Value, &item.Done, &item.LastDoneAt); err != nil { return nil, err }
		item.ID = resultID
		out = append(out, item)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows { return nil, err }
	return out, nil
}

func (r *Repository) SaveTaskChecklistResults(ctx context.Context, taskID, userID string, values map[string]string, done map[string]bool) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	for itemID, value := range values {
		isDone := done[itemID] || value != ""
		_, err := r.db.Exec(ctx, `INSERT INTO maintenance_task_checklist_results (task_id, template_item_id, value, done, checked_by, checked_at, updated_at) VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid, CASE WHEN $4 THEN NOW() ELSE NULL END, NOW()) ON CONFLICT (task_id, template_item_id) DO UPDATE SET value=EXCLUDED.value, done=EXCLUDED.done, checked_by=EXCLUDED.checked_by, checked_at=EXCLUDED.checked_at, updated_at=NOW()`, taskID, itemID, value, isDone, userID)
		if err != nil { return err }
	}
	return nil
}

func (r *Repository) DefaultDurationForTask(ctx context.Context, taskID string) int {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return 0 }
	var min int
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(mp.default_duration_min, mp.estimated_min, 0) FROM maintenance_tasks mt JOIN maintenance_plans mp ON mt.plan_id=mp.id WHERE mt.id=$1`, taskID).Scan(&min)
	return min
}
