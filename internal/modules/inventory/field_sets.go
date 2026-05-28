package inventory

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/response"
)

type FieldSet struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

type FieldDefWithSet struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	FieldType  string  `json:"field_type"`
	SortOrder  int     `json:"sort_order"`
	Active     bool    `json:"active"`
	FieldSetID *string `json:"field_set_id,omitempty"`
}

type CreateFieldSetInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type AssignFieldSetInput struct {
	FieldSetID string `json:"field_set_id"`
}

func (r *Repository) ensureFieldSetTables(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS spare_part_field_sets (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name VARCHAR(100) NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_sets_name_active
			ON spare_part_field_sets (lower(name)) WHERE active=true;
		ALTER TABLE spare_part_field_defs
			ADD COLUMN IF NOT EXISTS field_set_id UUID REFERENCES spare_part_field_sets(id) ON DELETE SET NULL;
		ALTER TABLE spare_parts
			ADD COLUMN IF NOT EXISTS custom_field_set_id UUID REFERENCES spare_part_field_sets(id) ON DELETE SET NULL;
		ALTER TABLE spare_parts
			ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_spare_part_field_defs_set ON spare_part_field_defs(field_set_id);
		CREATE INDEX IF NOT EXISTS idx_spare_parts_custom_field_set ON spare_parts(custom_field_set_id);
	`)
	return err
}

func (r *Repository) ListFieldSets(ctx context.Context) ([]*FieldSet, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT id, name, description, active, created_at FROM spare_part_field_sets WHERE active=true ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FieldSet
	for rows.Next() {
		fs := &FieldSet{}
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.Description, &fs.Active, &fs.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

func (r *Repository) CreateFieldSet(ctx context.Context, fs *FieldSet) error {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return err
	}
	return r.db.QueryRow(ctx, `
		INSERT INTO spare_part_field_sets (id, name, description)
		VALUES (gen_random_uuid(), $1, $2)
		RETURNING id, active, created_at`, fs.Name, fs.Description).Scan(&fs.ID, &fs.Active, &fs.CreatedAt)
}

func (r *Repository) ListFieldsForSet(ctx context.Context, fieldSetID string) ([]*FieldDefWithSet, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, name, field_type, sort_order, active, field_set_id
		FROM spare_part_field_defs
		WHERE active=true AND field_set_id=$1
		ORDER BY sort_order, name`, fieldSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FieldDefWithSet
	for rows.Next() {
		f := &FieldDefWithSet{}
		if err := rows.Scan(&f.ID, &f.Name, &f.FieldType, &f.SortOrder, &f.Active, &f.FieldSetID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) CreateFieldInSet(ctx context.Context, fieldSetID string, d *CustomFieldDef) error {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return err
	}
	if d.FieldType == "" {
		d.FieldType = "text"
	}
	if d.SortOrder == 0 {
		d.SortOrder = 100
	}
	return r.db.QueryRow(ctx, `
		INSERT INTO spare_part_field_defs (id, name, field_type, sort_order, field_set_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
		RETURNING id, active, created_at`, d.Name, d.FieldType, d.SortOrder, fieldSetID).Scan(&d.ID, &d.Active, &d.CreatedAt)
}

func (r *Repository) AssignFieldSetToPart(ctx context.Context, partID, fieldSetID string) error {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(fieldSetID) == "" {
		_, err := r.db.Exec(ctx, `UPDATE spare_parts SET custom_field_set_id=NULL, updated_at=NOW() WHERE id=$1`, partID)
		return err
	}
	_, err := r.db.Exec(ctx, `UPDATE spare_parts SET custom_field_set_id=$1, updated_at=NOW() WHERE id=$2`, fieldSetID, partID)
	return err
}

func (r *Repository) GetAssignedFieldSetID(ctx context.Context, partID string) (string, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return "", err
	}
	var id string
	_ = r.db.QueryRow(ctx, `SELECT COALESCE(custom_field_set_id::text, '') FROM spare_parts WHERE id=$1`, partID).Scan(&id)
	return id, nil
}

func (r *Repository) GetFieldValuesForAssignedSet(ctx context.Context, partID string) ([]*CustomFieldValue, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return nil, err
	}
	fieldSetID, err := r.GetAssignedFieldSetID(ctx, partID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(fieldSetID) == "" {
		return []*CustomFieldValue{}, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT d.id, d.name, d.field_type, COALESCE(v.value, '')
		FROM spare_part_field_defs d
		LEFT JOIN spare_part_field_values v ON v.field_id=d.id AND v.part_id=$1
		WHERE d.active=true AND d.field_set_id=$2
		ORDER BY d.sort_order, d.name`, partID, fieldSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []*CustomFieldValue
	for rows.Next() {
		v := &CustomFieldValue{}
		if err := rows.Scan(&v.FieldID, &v.Name, &v.FieldType, &v.Value); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (h *Handler) ListFieldSets(w http.ResponseWriter, r *http.Request) {
	sets, err := h.svc.repo.ListFieldSets(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, sets)
}

func (h *Handler) CreateFieldSet(w http.ResponseWriter, r *http.Request) {
	var in CreateFieldSetInput
	if err := decodeJSONOrForm(r, &in, func() { in.Name, in.Description = r.FormValue("name"), r.FormValue("description") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	fs := &FieldSet{Name: in.Name, Description: in.Description}
	if err := h.svc.repo.CreateFieldSet(r.Context(), fs); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, fs)
}

func (h *Handler) ListFieldsForSet(w http.ResponseWriter, r *http.Request) {
	fields, err := h.svc.repo.ListFieldsForSet(r.Context(), chi.URLParam(r, "setID"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, fields)
}

func (h *Handler) CreateFieldInSet(w http.ResponseWriter, r *http.Request) {
	var in CreateFieldDefInput
	if err := decodeJSONOrForm(r, &in, func() { in.Name, in.FieldType, in.SortOrder = r.FormValue("name"), r.FormValue("field_type"), int(f64(r, "sort_order")) }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	d := &CustomFieldDef{Name: in.Name, FieldType: in.FieldType, SortOrder: in.SortOrder}
	if err := h.svc.repo.CreateFieldInSet(r.Context(), chi.URLParam(r, "setID"), d); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, d)
}

func (h *Handler) AssignFieldSet(w http.ResponseWriter, r *http.Request) {
	var in AssignFieldSetInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.repo.AssignFieldSetToPart(r.Context(), chi.URLParam(r, "id"), in.FieldSetID); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GetAssignedFieldSet(w http.ResponseWriter, r *http.Request) {
	id, err := h.svc.repo.GetAssignedFieldSetID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"field_set_id": id})
}

func (h *Handler) GetAssignedFieldValues(w http.ResponseWriter, r *http.Request) {
	values, err := h.svc.repo.GetFieldValuesForAssignedSet(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, values)
}
