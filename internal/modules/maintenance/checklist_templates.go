package maintenance

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Anhang-Typen fuer Checklisten-Bilder (Tabelle attachments):
// Darstellung haengt am Vorlagenpunkt, Dokumentation am Ergebnis eines Auftrags.
const (
	RefChecklistItemImage   = "maint_check_item"
	RefChecklistResultImage = "maint_check_result"
)

// ChecklistImage: Bild an einem Checklistenpunkt bzw. -ergebnis.
type ChecklistImage struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

type ChecklistTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

type ChecklistTemplateItem struct {
	ID           string     `json:"id"`
	TemplateID   string     `json:"template_id"`
	Label        string     `json:"label"`
	Description  string     `json:"description"`
	ItemType     string     `json:"item_type"`
	Required     bool       `json:"required"`
	IntervalDays int        `json:"interval_days"`
	SortOrder    int        `json:"sort_order"`
	Active       bool       `json:"active"`
	CreatedAt    time.Time  `json:"created_at"`
	Due          bool       `json:"due"`
	LastDoneAt   *time.Time `json:"last_done_at,omitempty"`
	// Messwert: Einheit sowie Soll/Min/Max (nil = nicht aktiviert)
	Unit        string           `json:"unit"`
	TargetValue *float64         `json:"target_value"`
	MinValue    *float64         `json:"min_value"`
	MaxValue    *float64         `json:"max_value"`
	Images      []ChecklistImage `json:"images"`
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
	Unit           string     `json:"unit"`
	TargetValue    *float64   `json:"target_value"`
	MinValue       *float64   `json:"min_value"`
	MaxValue       *float64   `json:"max_value"`
	InRange        *bool      `json:"in_range"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	CheckedBy      string     `json:"checked_by,omitempty"`
	// RefImages: Darstellung aus der Vorlage; DocImages: Dokumentation dieses Auftrags
	RefImages []ChecklistImage `json:"ref_images"`
	DocImages []ChecklistImage `json:"doc_images"`
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
		CREATE TABLE IF NOT EXISTS maintenance_plan_checklist_templates (
			plan_id UUID NOT NULL REFERENCES maintenance_plans(id) ON DELETE CASCADE,
			template_id UUID NOT NULL REFERENCES maintenance_checklist_templates(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (plan_id, template_id)
		);
		INSERT INTO maintenance_plan_checklist_templates (plan_id, template_id)
		SELECT id, checklist_template_id FROM maintenance_plans
		WHERE checklist_template_id IS NOT NULL
		ON CONFLICT DO NOTHING;
		CREATE INDEX IF NOT EXISTS idx_maintenance_plan_checklist_templates_plan
			ON maintenance_plan_checklist_templates(plan_id);
		CREATE INDEX IF NOT EXISTS idx_maintenance_plan_checklist_templates_template
			ON maintenance_plan_checklist_templates(template_id);
		ALTER TABLE maintenance_checklist_template_items ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT '';
		ALTER TABLE maintenance_checklist_template_items ADD COLUMN IF NOT EXISTS target_value NUMERIC NULL;
		ALTER TABLE maintenance_checklist_template_items ADD COLUMN IF NOT EXISTS min_value NUMERIC NULL;
		ALTER TABLE maintenance_checklist_template_items ADD COLUMN IF NOT EXISTS max_value NUMERIC NULL;
		ALTER TABLE maintenance_task_checklist_results ADD COLUMN IF NOT EXISTS in_range BOOLEAN NULL;
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

func (r *Repository) DeleteChecklistTemplate(ctx context.Context, templateID string) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_templates SET active=false WHERE id=$1`, templateID)
	return err
}

func (r *Repository) CreateChecklistTemplateItem(ctx context.Context, item *ChecklistTemplateItem) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	return r.db.QueryRow(ctx, `INSERT INTO maintenance_checklist_template_items (id, template_id, label, description, item_type, required, interval_days, sort_order, unit, target_value, min_value, max_value) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id, active, created_at`, item.TemplateID, item.Label, item.Description, item.ItemType, item.Required, item.IntervalDays, item.SortOrder, item.Unit, item.TargetValue, item.MinValue, item.MaxValue).Scan(&item.ID, &item.Active, &item.CreatedAt)
}

func (r *Repository) ListChecklistTemplateItems(ctx context.Context, templateID string) ([]*ChecklistTemplateItem, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `SELECT id, template_id, label, description, item_type, required, interval_days, sort_order, active, created_at, unit, target_value::float8, min_value::float8, max_value::float8 FROM maintenance_checklist_template_items WHERE active=true AND template_id=$1 ORDER BY sort_order, label`, templateID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*ChecklistTemplateItem
	ids := []string{}
	for rows.Next() {
		item := &ChecklistTemplateItem{}
		if err := rows.Scan(&item.ID, &item.TemplateID, &item.Label, &item.Description, &item.ItemType, &item.Required, &item.IntervalDays, &item.SortOrder, &item.Active, &item.CreatedAt, &item.Unit, &item.TargetValue, &item.MinValue, &item.MaxValue); err != nil { return nil, err }
		out = append(out, item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil { return nil, err }
	imgs, err := r.checklistImages(ctx, RefChecklistItemImage, ids)
	if err != nil { return nil, err }
	for _, item := range out { item.Images = nonNilImages(imgs[item.ID]) }
	return out, nil
}

func (r *Repository) DeleteChecklistTemplateItem(ctx context.Context, itemID string) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET active=false WHERE id=$1`, itemID)
	return err
}

func (r *Repository) UpdateChecklistTemplateItem(ctx context.Context, itemID string, item *ChecklistTemplateItem) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET label=$1, description=$2, item_type=$3, required=$4, interval_days=$5, sort_order=$6, unit=$7, target_value=$8, min_value=$9, max_value=$10 WHERE id=$11`,
		item.Label, item.Description, item.ItemType, item.Required, item.IntervalDays, item.SortOrder, item.Unit, item.TargetValue, item.MinValue, item.MaxValue, itemID)
	return err
}

func (r *Repository) AssignChecklistTemplateToPlan(ctx context.Context, planID, templateID string, defaultDurationMin int) error {
	ids := []string{}
	if templateID != "" { ids = append(ids, templateID) }
	return r.AssignChecklistTemplatesToPlan(ctx, planID, ids, defaultDurationMin)
}

func (r *Repository) AssignChecklistTemplatesToPlan(ctx context.Context, planID string, templateIDs []string, defaultDurationMin int) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	tx, err := r.db.Begin(ctx)
	if err != nil { return err }
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM maintenance_plan_checklist_templates WHERE plan_id=$1`, planID); err != nil { return err }
	first := ""
	for _, id := range templateIDs {
		if id == "" { continue }
		if first == "" { first = id }
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_plan_checklist_templates (plan_id, template_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, planID, id); err != nil { return err }
	}
	if _, err := tx.Exec(ctx, `UPDATE maintenance_plans SET checklist_template_id=NULLIF($1,'')::uuid, default_duration_min=$2 WHERE id=$3`, first, defaultDurationMin, planID); err != nil { return err }
	return tx.Commit(ctx)
}

func (r *Repository) GetAssignedChecklistTemplateIDs(ctx context.Context, planID string) ([]string, int, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, 0, err }
	rows, err := r.db.Query(ctx, `SELECT template_id::text FROM maintenance_plan_checklist_templates WHERE plan_id=$1 ORDER BY created_at`, planID)
	if err != nil { return nil, 0, err }
	defer rows.Close()
	ids := []string{}
	for rows.Next() { var id string; if err := rows.Scan(&id); err != nil { return nil, 0, err }; ids = append(ids, id) }
	var legacy string
	var duration int
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(checklist_template_id::text,''), COALESCE(default_duration_min,0) FROM maintenance_plans WHERE id=$1`, planID).Scan(&legacy, &duration)
	if len(ids) == 0 && legacy != "" { ids = append(ids, legacy) }
	return ids, duration, rows.Err()
}

func (r *Repository) DeletePlanSoft(ctx context.Context, planID string) error {
	_, err := r.db.Exec(ctx, `UPDATE maintenance_plans SET active=false WHERE id=$1`, planID)
	return err
}

func (r *Repository) DueChecklistItemsForTask(ctx context.Context, taskID string) ([]*TaskChecklistItem, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `
		WITH task_plan AS (
			SELECT mt.id AS task_id, mt.plan_id
			FROM maintenance_tasks mt
			WHERE mt.id=$1 AND mt.plan_id IS NOT NULL
		), template_ids AS (
			SELECT pct.template_id
			FROM task_plan tp
			JOIN maintenance_plan_checklist_templates pct ON pct.plan_id=tp.plan_id
			UNION
			SELECT mp.checklist_template_id
			FROM task_plan tp
			JOIN maintenance_plans mp ON mp.id=tp.plan_id
			WHERE mp.checklist_template_id IS NOT NULL
		), last_done AS (
			SELECT template_item_id, max(checked_at) AS last_done_at
			FROM maintenance_task_checklist_results
			WHERE done=true AND checked_at IS NOT NULL
			GROUP BY template_item_id
		)
		SELECT COALESCE(r.id::text,''), i.id::text, i.label, i.description, i.item_type, i.required, i.interval_days,
		       COALESCE(r.value,''), COALESCE(r.done,false), ld.last_done_at,
		       i.unit, i.target_value::float8, i.min_value::float8, i.max_value::float8, r.in_range
		FROM template_ids ti
		JOIN maintenance_checklist_template_items i ON i.template_id=ti.template_id AND i.active=true
		LEFT JOIN maintenance_task_checklist_results r ON r.task_id=$1 AND r.template_item_id=i.id
		LEFT JOIN last_done ld ON ld.template_item_id=i.id
		WHERE ld.last_done_at IS NULL OR ld.last_done_at + (i.interval_days || ' days')::interval <= NOW()
		ORDER BY i.sort_order, i.label`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*TaskChecklistItem
	for rows.Next() {
		item := &TaskChecklistItem{}
		var resultID string
		if err := rows.Scan(&resultID, &item.TemplateItemID, &item.Label, &item.Description, &item.ItemType, &item.Required, &item.IntervalDays, &item.Value, &item.Done, &item.LastDoneAt, &item.Unit, &item.TargetValue, &item.MinValue, &item.MaxValue, &item.InRange); err != nil { return nil, err }
		item.ID = resultID
		out = append(out, item)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows { return nil, err }
	if err := r.attachTaskChecklistImages(ctx, out); err != nil { return nil, err }
	return out, nil
}

// ParseMeasure liest einen Messwert; Komma und Punkt sind als Dezimaltrenner erlaubt.
func ParseMeasure(v string) (float64, bool) {
	v = strings.TrimSpace(strings.ReplaceAll(v, ",", "."))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// MeasureInRange: nil, wenn weder Min noch Max aktiviert sind oder der Wert keine Zahl ist.
func MeasureInRange(value string, min, max *float64) *bool {
	if min == nil && max == nil {
		return nil
	}
	f, ok := ParseMeasure(value)
	if !ok {
		return nil
	}
	in := (min == nil || f >= *min) && (max == nil || f <= *max)
	return &in
}

func (r *Repository) SaveTaskChecklistResults(ctx context.Context, taskID, userID string, values map[string]string, done map[string]bool) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	type def struct {
		label, typ string
		required   bool
		min, max   *float64
	}
	defs := map[string]def{}
	ids := make([]string, 0, len(values))
	for id := range values { ids = append(ids, id) }
	if len(ids) > 0 {
		rows, err := r.db.Query(ctx, `SELECT id::text, label, item_type, required, min_value::float8, max_value::float8 FROM maintenance_checklist_template_items WHERE id::text = ANY($1)`, ids)
		if err != nil { return err }
		for rows.Next() {
			var id string
			var d def
			if err := rows.Scan(&id, &d.label, &d.typ, &d.required, &d.min, &d.max); err != nil { rows.Close(); return err }
			defs[id] = d
		}
		rows.Close()
		if err := rows.Err(); err != nil { return err }
	}
	// Pflichtpunkte und Zahlenformat pruefen, bevor etwas gespeichert wird
	var missing []string
	for itemID, value := range values {
		d, ok := defs[itemID]
		if !ok { return fmt.Errorf("checklistenpunkt %s nicht gefunden", itemID) }
		value = strings.TrimSpace(value)
		if d.typ == "number" && value != "" {
			if _, ok := ParseMeasure(value); !ok { return fmt.Errorf("„%s“: %q ist keine Zahl", d.label, value) }
		}
		filled := value != ""
		if d.typ == "checkbox" { filled = done[itemID] }
		if d.required && !filled { missing = append(missing, d.label) }
	}
	if len(missing) > 0 { return fmt.Errorf("Pflichtpunkte fehlen: %s", strings.Join(missing, ", ")) }
	for itemID, value := range values {
		d := defs[itemID]
		value = strings.TrimSpace(value)
		isDone := done[itemID] || (d.typ != "checkbox" && value != "")
		if d.typ == "checkbox" && !isDone { value = "" }
		var inRange *bool
		if d.typ == "number" { inRange = MeasureInRange(value, d.min, d.max) }
		_, err := r.db.Exec(ctx, `INSERT INTO maintenance_task_checklist_results (task_id, template_item_id, value, done, in_range, checked_by, checked_at, updated_at) VALUES ($1, $2, $3, $4, $5, NULLIF($6,'')::uuid, CASE WHEN $4 THEN NOW() ELSE NULL END, NOW()) ON CONFLICT (task_id, template_item_id) DO UPDATE SET value=EXCLUDED.value, done=EXCLUDED.done, in_range=EXCLUDED.in_range, checked_by=EXCLUDED.checked_by, checked_at=EXCLUDED.checked_at, updated_at=NOW()`, taskID, itemID, value, isDone, inRange, userID)
		if err != nil { return err }
	}
	return nil
}

// EnsureTaskChecklistResult legt (falls noetig) die Ergebniszeile eines Punkts an,
// damit Dokumentationsfotos schon vor dem Abschluss daran haengen koennen.
func (r *Repository) EnsureTaskChecklistResult(ctx context.Context, taskID, itemID string) (string, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return "", err }
	var id string
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_task_checklist_results (task_id, template_item_id) VALUES ($1, $2) ON CONFLICT (task_id, template_item_id) DO UPDATE SET updated_at=maintenance_task_checklist_results.updated_at RETURNING id::text`, taskID, itemID).Scan(&id)
	return id, err
}

// TaskChecklistResults: Protokoll eines Auftrags – alle erfassten Punkte samt Bildern.
func (r *Repository) TaskChecklistResults(ctx context.Context, taskID string) ([]*TaskChecklistItem, error) {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return nil, err }
	rows, err := r.db.Query(ctx, `
		SELECT r.id::text, i.id::text, i.label, i.description, i.item_type, i.required, i.interval_days,
		       r.value, r.done, i.unit, i.target_value::float8, i.min_value::float8, i.max_value::float8, r.in_range,
		       r.checked_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name),'')
		FROM maintenance_task_checklist_results r
		JOIN maintenance_checklist_template_items i ON i.id=r.template_item_id
		LEFT JOIN users u ON u.id=r.checked_by
		WHERE r.task_id=$1
		ORDER BY i.sort_order, i.label`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*TaskChecklistItem
	for rows.Next() {
		item := &TaskChecklistItem{}
		if err := rows.Scan(&item.ID, &item.TemplateItemID, &item.Label, &item.Description, &item.ItemType, &item.Required, &item.IntervalDays, &item.Value, &item.Done, &item.Unit, &item.TargetValue, &item.MinValue, &item.MaxValue, &item.InRange, &item.CheckedAt, &item.CheckedBy); err != nil { return nil, err }
		out = append(out, item)
	}
	if err := rows.Err(); err != nil { return nil, err }
	if err := r.attachTaskChecklistImages(ctx, out); err != nil { return nil, err }
	// Zeilen ohne Ergebnis und ohne Foto (nur fuer ein Foto angelegt) weglassen
	kept := out[:0]
	for _, it := range out {
		if it.Done || it.Value != "" || len(it.DocImages) > 0 { kept = append(kept, it) }
	}
	return kept, nil
}

func (r *Repository) attachTaskChecklistImages(ctx context.Context, items []*TaskChecklistItem) error {
	itemIDs, resultIDs := []string{}, []string{}
	for _, it := range items {
		itemIDs = append(itemIDs, it.TemplateItemID)
		if it.ID != "" { resultIDs = append(resultIDs, it.ID) }
	}
	refs, err := r.checklistImages(ctx, RefChecklistItemImage, itemIDs)
	if err != nil { return err }
	docs, err := r.checklistImages(ctx, RefChecklistResultImage, resultIDs)
	if err != nil { return err }
	for _, it := range items {
		it.RefImages = nonNilImages(refs[it.TemplateItemID])
		it.DocImages = nonNilImages(docs[it.ID])
	}
	return nil
}

// checklistImages liefert die Bilder je ref_id (aelteste zuerst).
func (r *Repository) checklistImages(ctx context.Context, refType string, refIDs []string) (map[string][]ChecklistImage, error) {
	out := map[string][]ChecklistImage{}
	if len(refIDs) == 0 { return out, nil }
	rows, err := r.db.Query(ctx, `SELECT id::text, ref_id::text, filepath, filename FROM attachments WHERE ref_type=$1 AND ref_id::text = ANY($2) AND lower(mimetype) LIKE 'image/%' ORDER BY created_at`, refType, refIDs)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var img ChecklistImage
		var refID, path string
		if err := rows.Scan(&img.ID, &refID, &path, &img.Filename); err != nil { return nil, err }
		img.URL = "/uploads/" + strings.ReplaceAll(path, `\`, "/")
		out[refID] = append(out[refID], img)
	}
	return out, rows.Err()
}

func nonNilImages(in []ChecklistImage) []ChecklistImage {
	if in == nil { return []ChecklistImage{} }
	return in
}

func (r *Repository) DefaultDurationForTask(ctx context.Context, taskID string) int {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return 0 }
	var min int
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(mp.default_duration_min,0), mp.estimated_min, 0) FROM maintenance_tasks mt JOIN maintenance_plans mp ON mt.plan_id=mp.id WHERE mt.id=$1`, taskID).Scan(&min)
	return min
}
