package maintenance

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── Checklisten (Vorlagen) ───────────────────────────────────

var checklistItemTypes = map[string]bool{"checkbox": true, "number": true, "text": true}

// ListTemplates: aktive Checklisten mit Anzahl Punkte und Plaene.
func (r *Repository) ListTemplates(ctx context.Context) ([]*ChecklistTemplate, error) {
	rows, err := r.db.Query(ctx, `SELECT t.id::text, t.name, t.description, t.active, t.created_at,
			(SELECT COUNT(*) FROM maintenance_checklist_template_items i WHERE i.template_id = t.id AND i.active),
			(SELECT COUNT(*) FROM maintenance_plan_checklists pc JOIN maintenance_plans p ON p.id = pc.plan_id
			  WHERE pc.template_id = t.id AND p.active)
		FROM maintenance_checklist_templates t WHERE t.active ORDER BY lower(t.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ChecklistTemplate{}
	for rows.Next() {
		t := &ChecklistTemplate{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Active, &t.CreatedAt, &t.ItemCount, &t.PlanCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTemplate: Checkliste mit ihren Punkten (inkl. Bilder).
func (r *Repository) GetTemplate(ctx context.Context, id string) (*ChecklistTemplate, error) {
	t := &ChecklistTemplate{}
	if err := r.db.QueryRow(ctx, `SELECT id::text, name, description, active, created_at FROM maintenance_checklist_templates WHERE id=$1::uuid`, id).
		Scan(&t.ID, &t.Name, &t.Description, &t.Active, &t.CreatedAt); err != nil {
		return nil, err
	}
	items, err := r.TemplateItems(ctx, id)
	t.Items, t.ItemCount = items, len(items)
	return t, err
}

func (r *Repository) CreateTemplate(ctx context.Context, name, description, userID string) (*ChecklistTemplate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, inputErr("Bitte einen Namen angeben")
	}
	t := &ChecklistTemplate{Name: name, Description: strings.TrimSpace(description), Active: true}
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_checklist_templates (name, description, created_by)
		VALUES ($1, $2, NULLIF($3,'')::uuid) RETURNING id::text, created_at`, t.Name, t.Description, userID).Scan(&t.ID, &t.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "idx_maintenance_checklist_templates_name_active") {
		return nil, inputErr("Eine Checkliste mit diesem Namen gibt es schon")
	}
	return t, err
}

func (r *Repository) RenameTemplate(ctx context.Context, id, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return inputErr("Bitte einen Namen angeben")
	}
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_templates SET name=$2, description=$3 WHERE id=$1::uuid`, id, name, strings.TrimSpace(description))
	if err != nil && strings.Contains(err.Error(), "idx_maintenance_checklist_templates_name_active") {
		return inputErr("Eine Checkliste mit diesem Namen gibt es schon")
	}
	return err
}

// DeleteTemplate: Checkliste ausblenden und von allen Plaenen loesen (Ergebnisse bleiben).
func (r *Repository) DeleteTemplate(ctx context.Context, id string) error {
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_templates SET active=false WHERE id=$1::uuid`, id); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_plan_checklists WHERE template_id=$1::uuid`, id)
	return err
}

// TemplateItems: Punkte einer Checkliste samt Bildern zur Darstellung.
func (r *Repository) TemplateItems(ctx context.Context, templateID string) ([]*ChecklistTemplateItem, error) {
	rows, err := r.db.Query(ctx, `SELECT id::text, template_id::text, label, description, item_type, required, sort_order,
			unit, target_value::float8, min_value::float8, max_value::float8
		FROM maintenance_checklist_template_items WHERE template_id=$1::uuid AND active ORDER BY sort_order, label`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ChecklistTemplateItem{}
	ids := []string{}
	for rows.Next() {
		it := &ChecklistTemplateItem{}
		if err := rows.Scan(&it.ID, &it.TemplateID, &it.Label, &it.Description, &it.ItemType, &it.Required, &it.SortOrder,
			&it.Unit, &it.TargetValue, &it.MinValue, &it.MaxValue); err != nil {
			return nil, err
		}
		out = append(out, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	imgs, err := r.images(ctx, RefChecklistItemImage, ids)
	for _, it := range out {
		it.Images = nonNil(imgs[it.ID])
	}
	return out, err
}

func normalizeItem(in *ChecklistItemInput) error {
	in.Label = strings.TrimSpace(in.Label)
	in.Description = strings.TrimSpace(in.Description)
	if in.Label == "" {
		return inputErr("Bitte den Punkt benennen")
	}
	if in.ItemType == "" {
		in.ItemType = "checkbox"
	}
	if !checklistItemTypes[in.ItemType] {
		return inputErr("Art muss Abhaken, Messwert oder Freitext sein")
	}
	if in.ItemType != "number" {
		in.Unit, in.TargetValue, in.MinValue, in.MaxValue = "", nil, nil, nil
	}
	in.Unit = strings.TrimSpace(in.Unit)
	if len([]rune(in.Unit)) > 20 {
		return inputErr("Einheit höchstens 20 Zeichen")
	}
	if in.MinValue != nil && in.MaxValue != nil && *in.MinValue > *in.MaxValue {
		return inputErr("Min darf nicht größer als Max sein")
	}
	return nil
}

func (r *Repository) AddTemplateItem(ctx context.Context, templateID string, in *ChecklistItemInput) (string, error) {
	if err := normalizeItem(in); err != nil {
		return "", err
	}
	if in.SortOrder == 0 {
		_ = r.db.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order),0)+10 FROM maintenance_checklist_template_items WHERE template_id=$1::uuid AND active`, templateID).Scan(&in.SortOrder)
	}
	var id string
	err := r.db.QueryRow(ctx, `INSERT INTO maintenance_checklist_template_items
			(template_id, label, description, item_type, required, sort_order, unit, target_value, min_value, max_value)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
		templateID, in.Label, in.Description, in.ItemType, in.Required, in.SortOrder, in.Unit, in.TargetValue, in.MinValue, in.MaxValue).Scan(&id)
	return id, err
}

func (r *Repository) UpdateTemplateItem(ctx context.Context, itemID string, in *ChecklistItemInput) error {
	if err := normalizeItem(in); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET label=$2, description=$3, item_type=$4, required=$5,
			sort_order=CASE WHEN $6 > 0 THEN $6 ELSE sort_order END, unit=$7, target_value=$8, min_value=$9, max_value=$10
		WHERE id=$1::uuid`, itemID, in.Label, in.Description, in.ItemType, in.Required, in.SortOrder, in.Unit, in.TargetValue, in.MinValue, in.MaxValue)
	return err
}

func (r *Repository) DeleteTemplateItem(ctx context.Context, itemID string) error {
	_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET active=false WHERE id=$1::uuid`, itemID)
	return err
}

// ReorderTemplateItems: Reihenfolge der Punkte (ids in neuer Reihenfolge).
func (r *Repository) ReorderTemplateItems(ctx context.Context, templateID string, ids []string) error {
	for i, id := range ids {
		if _, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET sort_order=$3 WHERE id=$1::uuid AND template_id=$2::uuid`,
			id, templateID, (i+1)*10); err != nil {
			return err
		}
	}
	return nil
}

// ── Schritte am Auftrag ──────────────────────────────────────

// dueChecklists: Plan-Checklisten, die zum Faelligkeitstag des Auftrags dran
// sind – Takt „immer“ oder seit der letzten Erledigung abgelaufen.
func (r *Repository) dueChecklists(ctx context.Context, planID string, due time.Time) ([]*PlanChecklist, error) {
	all, err := r.PlanChecklists(ctx, planID)
	if err != nil {
		return nil, err
	}
	end := dayStart(due).AddDate(0, 0, 1) // Ende des Faelligkeitstags
	var out []*PlanChecklist
	for _, c := range all {
		if c.RhythmUnit == RhythmAlways || c.NextFrom == nil || c.NextFrom.Before(end) {
			out = append(out, c)
		}
	}
	return out, nil
}

// stepsTouched: am Auftrag wurde schon etwas erfasst (Wert, Haken oder Foto).
func (r *Repository) stepsTouched(ctx context.Context, taskID string) bool {
	var touched bool
	_ = r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_task_steps s WHERE s.task_id=$1::uuid
			AND (s.done OR s.value <> '' OR EXISTS (SELECT 1 FROM attachments a WHERE a.ref_type=$2 AND a.ref_id=s.id)))`,
		taskID, RefChecklistResultImage).Scan(&touched)
	return touched
}

// BuildSteps stellt die Schritte eines offenen Plan-Auftrags aus den faelligen
// Checklisten zusammen. Solange noch nichts erfasst ist, wird neu aufgebaut
// (Aenderungen am Plan wirken sofort); danach bleibt die Liste stabil.
// force=false baut nur, wenn noch gar keine Schritte vorhanden sind.
func (r *Repository) BuildSteps(ctx context.Context, taskID string, force bool) (int, error) {
	var planID *string
	var due time.Time
	var status string
	if err := r.db.QueryRow(ctx, `SELECT plan_id::text, due_date, status::text FROM maintenance_tasks WHERE id=$1::uuid`, taskID).
		Scan(&planID, &due, &status); err != nil {
		return 0, err
	}
	if planID == nil || status == string(TaskDone) || status == string(TaskSkipped) {
		return 0, nil
	}
	var existing int
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_steps WHERE task_id=$1::uuid`, taskID).Scan(&existing)
	if (existing > 0 && !force) || r.stepsTouched(ctx, taskID) {
		return existing, nil
	}
	lists, err := r.dueChecklists(ctx, *planID, due)
	if err != nil {
		return 0, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM maintenance_task_steps WHERE task_id=$1::uuid`, taskID); err != nil {
		return 0, err
	}
	for k, c := range lists {
		// jede Checkliste in ihrem eigenen Tausenderblock (Reihenfolge wie am Plan)
		// Rundgang: jede Station merkt sich ihre Anlage (Name als Momentaufnahme)
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_task_steps (task_id, plan_checklist_id, template_id, template_item_id,
				checklist_name, station_infra_id, station_name, sort_order, label, description, item_type, required, unit,
				target_value, min_value, max_value)
			SELECT $1::uuid, $2::uuid, t.id, i.id, t.name, $5::uuid, $6, $3 + ROW_NUMBER() OVER (ORDER BY i.sort_order, i.label),
				i.label, i.description, CASE WHEN i.item_type IN ('checkbox','number','text') THEN i.item_type ELSE 'checkbox' END,
				i.required, i.unit, i.target_value, i.min_value, i.max_value
			FROM maintenance_checklist_template_items i JOIN maintenance_checklist_templates t ON t.id = i.template_id
			WHERE i.template_id = $4::uuid AND i.active`, taskID, c.ID, (k+1)*1000, c.TemplateID,
			nullID(&c.InfrastructureID), c.InfraName); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	var count int
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_steps WHERE task_id=$1::uuid`, taskID).Scan(&count)
	return count, nil
}

// AddTemplateSteps haengt die Punkte einer Checkliste an einen Auftrag an
// (z. B. Auftrag ohne Plan, oder zusaetzliche Liste vor Ort).
func (r *Repository) AddTemplateSteps(ctx context.Context, taskID, templateID string) (int, error) {
	var already bool
	_ = r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_task_steps WHERE task_id=$1::uuid AND template_id=$2::uuid)`, taskID, templateID).Scan(&already)
	if already {
		return 0, inputErr("Diese Checkliste ist schon am Auftrag")
	}
	var base int
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order),0) FROM maintenance_task_steps WHERE task_id=$1::uuid`, taskID).Scan(&base)
	base = (base/1000 + 1) * 1000
	tag, err := r.db.Exec(ctx, `INSERT INTO maintenance_task_steps (task_id, template_id, template_item_id, checklist_name, sort_order,
			label, description, item_type, required, unit, target_value, min_value, max_value)
		SELECT $1::uuid, t.id, i.id, t.name, $2 + ROW_NUMBER() OVER (ORDER BY i.sort_order, i.label),
			i.label, i.description, CASE WHEN i.item_type IN ('checkbox','number','text') THEN i.item_type ELSE 'checkbox' END,
			i.required, i.unit, i.target_value, i.min_value, i.max_value
		FROM maintenance_checklist_template_items i JOIN maintenance_checklist_templates t ON t.id = i.template_id
		WHERE i.template_id = $3::uuid AND i.active AND t.active`, taskID, base, templateID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// RemoveTemplateSteps entfernt die (noch nicht erfassten) Punkte einer Checkliste vom Auftrag.
func (r *Repository) RemoveTemplateSteps(ctx context.Context, taskID, templateID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_task_steps WHERE task_id=$1::uuid AND template_id=$2::uuid
		AND NOT done AND value = '' AND plan_checklist_id IS NULL`, taskID, templateID)
	return err
}

// Steps: Schritte eines Auftrags mit Ergebnissen und Bildern.
func (r *Repository) Steps(ctx context.Context, taskID string) ([]*TaskStep, error) {
	rows, err := r.db.Query(ctx, `SELECT s.id::text, s.task_id::text, COALESCE(s.plan_checklist_id::text,''), COALESCE(s.template_id::text,''),
			COALESCE(s.template_item_id::text,''), s.checklist_name, COALESCE(s.station_infra_id::text,''), s.station_name, s.sort_order, s.label, s.description, s.item_type, s.required,
			s.unit, s.target_value::float8, s.min_value::float8, s.max_value::float8, s.value, s.done, s.in_range, s.checked_at,
			COALESCE(TRIM(u.first_name||' '||u.last_name),'')
		FROM maintenance_task_steps s LEFT JOIN users u ON u.id = s.checked_by
		WHERE s.task_id=$1::uuid ORDER BY s.sort_order, s.label`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskStep{}
	itemIDs, stepIDs := []string{}, []string{}
	for rows.Next() {
		s := &TaskStep{}
		if err := rows.Scan(&s.ID, &s.TaskID, &s.PlanChecklistID, &s.TemplateID, &s.TemplateItemID, &s.ChecklistName,
			&s.StationInfraID, &s.StationName, &s.SortOrder,
			&s.Label, &s.Description, &s.ItemType, &s.Required, &s.Unit, &s.TargetValue, &s.MinValue, &s.MaxValue,
			&s.Value, &s.Done, &s.InRange, &s.CheckedAt, &s.CheckedBy); err != nil {
			return nil, err
		}
		out = append(out, s)
		stepIDs = append(stepIDs, s.ID)
		if s.TemplateItemID != "" {
			itemIDs = append(itemIDs, s.TemplateItemID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs, err := r.images(ctx, RefChecklistItemImage, itemIDs)
	if err != nil {
		return nil, err
	}
	docs, err := r.images(ctx, RefChecklistResultImage, stepIDs)
	if err != nil {
		return nil, err
	}
	for _, s := range out {
		s.RefImages = nonNil(refs[s.TemplateItemID])
		s.DocImages = nonNil(docs[s.ID])
	}
	return out, nil
}

// SaveStep: Wert eines Schritts speichern (Zahl pruefen, Min/Max bewerten).
// Leerer Wert / nicht abgehakt setzt den Schritt zurueck.
func (r *Repository) SaveStep(ctx context.Context, taskID, stepID, value string, done bool, userID string) (*TaskStep, error) {
	var typ string
	var min, max *float64
	if err := r.db.QueryRow(ctx, `SELECT item_type, min_value::float8, max_value::float8 FROM maintenance_task_steps
		WHERE id=$1::uuid AND task_id=$2::uuid`, stepID, taskID).Scan(&typ, &min, &max); err != nil {
		return nil, inputErr("Punkt nicht gefunden")
	}
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 4000 {
		return nil, inputErr("Text zu lang")
	}
	if typ == "checkbox" {
		value = ""
		if done {
			value = "erledigt"
		}
	} else {
		done = value != ""
	}
	var inRange *bool
	if typ == "number" && value != "" {
		if _, ok := ParseMeasure(value); !ok {
			return nil, inputErr(fmt.Sprintf("%q ist keine Zahl", value))
		}
		inRange = MeasureInRange(value, min, max)
	}
	if _, err := r.db.Exec(ctx, `UPDATE maintenance_task_steps SET value=$3, done=$4, in_range=$5,
			checked_by=CASE WHEN $4 THEN NULLIF($6,'')::uuid ELSE NULL END, checked_at=CASE WHEN $4 THEN NOW() ELSE NULL END, updated_at=NOW()
		WHERE id=$1::uuid AND task_id=$2::uuid`, stepID, taskID, value, done, inRange, userID); err != nil {
		return nil, err
	}
	steps, err := r.Steps(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if s.ID == stepID {
			return s, nil
		}
	}
	return nil, inputErr("Punkt nicht gefunden")
}

// MissingRequired: Bezeichnungen der noch offenen Pflichtpunkte.
func (r *Repository) MissingRequired(ctx context.Context, taskID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT label FROM maintenance_task_steps WHERE task_id=$1::uuid AND required
		AND NOT (CASE WHEN item_type='checkbox' THEN done ELSE value <> '' END) ORDER BY sort_order`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if rows.Scan(&l) == nil {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

// StepSummary: Anzahl Schritte, erfasst und Checklisten eines Auftrags (fuer Listen).
type StepSummary struct {
	Total, Filled, Lists, OutOfRange, Unchecked int
}

// StepSummaries je Auftrag; Plan-Auftraege ohne Schritte zaehlen die faelligen Checklisten.
func (r *Repository) StepSummaries(ctx context.Context, taskIDs []string) (map[string]StepSummary, error) {
	out := map[string]StepSummary{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `SELECT task_id::text, COUNT(*), COUNT(*) FILTER (WHERE CASE WHEN item_type='checkbox' THEN done ELSE value <> '' END),
			COUNT(DISTINCT COALESCE(template_id::text, checklist_name)),
			COUNT(*) FILTER (WHERE in_range = false),
			COUNT(*) FILTER (WHERE item_type = 'checkbox' AND NOT done)
		FROM maintenance_task_steps WHERE task_id::text = ANY($1) GROUP BY task_id`, taskIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s StepSummary
		if rows.Scan(&id, &s.Total, &s.Filled, &s.Lists, &s.OutOfRange, &s.Unchecked) == nil {
			out[id] = s
		}
	}
	return out, rows.Err()
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

// MeasureInRange: nil, wenn weder Min noch Max gesetzt sind oder der Wert keine Zahl ist.
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

// images liefert die Bilder je ref_id (aelteste zuerst).
func (r *Repository) images(ctx context.Context, refType string, refIDs []string) (map[string][]ChecklistImage, error) {
	out := map[string][]ChecklistImage{}
	if len(refIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `SELECT id::text, ref_id::text, filepath, filename FROM attachments
		WHERE ref_type=$1 AND ref_id::text = ANY($2) AND lower(mimetype) LIKE 'image/%' ORDER BY created_at`, refType, refIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var img ChecklistImage
		var refID, path string
		if err := rows.Scan(&img.ID, &refID, &path, &img.Filename); err != nil {
			return nil, err
		}
		img.URL = "/uploads/" + strings.ReplaceAll(path, `\`, "/")
		out[refID] = append(out[refID], img)
	}
	return out, rows.Err()
}

func nonNil(in []ChecklistImage) []ChecklistImage {
	if in == nil {
		return []ChecklistImage{}
	}
	return in
}
