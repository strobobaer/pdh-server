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

type AssignFieldSetsInput struct {
	FieldSetIDs []string `json:"field_set_ids"`
}

type FieldOption struct {
	ID        string    `json:"id"`
	FieldID   string    `json:"field_id"`
	Value     string    `json:"value"`
	SortOrder int       `json:"sort_order"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateFieldOptionInput struct {
	Value     string `json:"value"`
	SortOrder int    `json:"sort_order"`
}

type CustomFieldValueWithOptions struct {
	FieldID   string         `json:"field_id"`
	Name      string         `json:"name"`
	FieldType string         `json:"field_type"`
	Value     string         `json:"value"`
	Options   []*FieldOption `json:"options,omitempty"`
}

func (r *Repository) ensureMultiFieldSetTables(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS spare_part_field_set_assignments (
			part_id UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
			field_set_id UUID NOT NULL REFERENCES spare_part_field_sets(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (part_id, field_set_id)
		);
		INSERT INTO spare_part_field_set_assignments (part_id, field_set_id)
		SELECT id, custom_field_set_id FROM spare_parts
		WHERE custom_field_set_id IS NOT NULL
		ON CONFLICT DO NOTHING;
		CREATE TABLE IF NOT EXISTS spare_part_field_options (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			field_id UUID NOT NULL REFERENCES spare_part_field_defs(id) ON DELETE CASCADE,
			value TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 100,
			active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_options_unique_active
			ON spare_part_field_options (field_id, lower(value)) WHERE active=true;
		CREATE INDEX IF NOT EXISTS idx_spare_part_field_set_assignments_part ON spare_part_field_set_assignments(part_id);
		CREATE INDEX IF NOT EXISTS idx_spare_part_field_options_field ON spare_part_field_options(field_id);
	`)
	return err
}

func (r *Repository) AssignFieldSetsToPart(ctx context.Context, partID string, fieldSetIDs []string) error {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return err
	}
	if err := r.ensureMultiFieldSetTables(ctx); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM spare_part_field_set_assignments WHERE part_id=$1`, partID); err != nil {
		return err
	}
	var first string
	for _, id := range fieldSetIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if first == "" {
			first = id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO spare_part_field_set_assignments (part_id, field_set_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, partID, id); err != nil {
			return err
		}
	}
	if first == "" {
		if _, err := tx.Exec(ctx, `UPDATE spare_parts SET custom_field_set_id=NULL, updated_at=NOW() WHERE id=$1`, partID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE spare_parts SET custom_field_set_id=$1, updated_at=NOW() WHERE id=$2`, first, partID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) GetAssignedFieldSetIDs(ctx context.Context, partID string) ([]string, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return nil, err
	}
	if err := r.ensureMultiFieldSetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT field_set_id::text FROM spare_part_field_set_assignments WHERE part_id=$1 ORDER BY created_at`, partID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		legacy, err := r.GetAssignedFieldSetID(ctx, partID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(legacy) != "" {
			ids = append(ids, legacy)
		}
	}
	return ids, rows.Err()
}

func (r *Repository) ListOptionsForField(ctx context.Context, fieldID string) ([]*FieldOption, error) {
	if err := r.ensureMultiFieldSetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT id, field_id, value, sort_order, active, created_at FROM spare_part_field_options WHERE active=true AND field_id=$1 ORDER BY sort_order, value`, fieldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FieldOption
	for rows.Next() {
		o := &FieldOption{}
		if err := rows.Scan(&o.ID, &o.FieldID, &o.Value, &o.SortOrder, &o.Active, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repository) CreateOptionForField(ctx context.Context, fieldID string, in *CreateFieldOptionInput) (*FieldOption, error) {
	if err := r.ensureMultiFieldSetTables(ctx); err != nil {
		return nil, err
	}
	if in.SortOrder == 0 {
		in.SortOrder = 100
	}
	o := &FieldOption{FieldID: fieldID, Value: strings.TrimSpace(in.Value), SortOrder: in.SortOrder}
	err := r.db.QueryRow(ctx, `INSERT INTO spare_part_field_options (id, field_id, value, sort_order) VALUES (gen_random_uuid(), $1, $2, $3) RETURNING id, active, created_at`, fieldID, o.Value, o.SortOrder).Scan(&o.ID, &o.Active, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (r *Repository) GetFieldValuesForAssignedSets(ctx context.Context, partID string) ([]*CustomFieldValueWithOptions, error) {
	if err := r.ensureFieldSetTables(ctx); err != nil {
		return nil, err
	}
	if err := r.ensureMultiFieldSetTables(ctx); err != nil {
		return nil, err
	}
	ids, err := r.GetAssignedFieldSetIDs(ctx, partID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []*CustomFieldValueWithOptions{}, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT d.id, d.name, d.field_type, COALESCE(v.value, '')
		FROM spare_part_field_defs d
		LEFT JOIN spare_part_field_values v ON v.field_id=d.id AND v.part_id=$1
		WHERE d.active=true AND d.field_set_id = ANY($2::uuid[])
		ORDER BY d.name`, partID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []*CustomFieldValueWithOptions
	for rows.Next() {
		v := &CustomFieldValueWithOptions{}
		if err := rows.Scan(&v.FieldID, &v.Name, &v.FieldType, &v.Value); err != nil {
			return nil, err
		}
		if v.FieldType == "select" || v.FieldType == "list" || v.FieldType == "choice" {
			opts, err := r.ListOptionsForField(ctx, v.FieldID)
			if err != nil {
				return nil, err
			}
			v.Options = opts
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (h *Handler) AssignFieldSets(w http.ResponseWriter, r *http.Request) {
	var in AssignFieldSetsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.repo.AssignFieldSetsToPart(r.Context(), chi.URLParam(r, "id"), in.FieldSetIDs); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GetAssignedFieldSets(w http.ResponseWriter, r *http.Request) {
	ids, err := h.svc.repo.GetAssignedFieldSetIDs(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string][]string{"field_set_ids": ids})
}

func (h *Handler) GetAssignedFieldValuesMulti(w http.ResponseWriter, r *http.Request) {
	values, err := h.svc.repo.GetFieldValuesForAssignedSets(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, values)
}

func (h *Handler) ListFieldOptions(w http.ResponseWriter, r *http.Request) {
	opts, err := h.svc.repo.ListOptionsForField(r.Context(), chi.URLParam(r, "fieldID"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, opts)
}

func (h *Handler) CreateFieldOption(w http.ResponseWriter, r *http.Request) {
	var in CreateFieldOptionInput
	if err := decodeJSONOrForm(r, &in, func() { in.Value, in.SortOrder = r.FormValue("value"), int(f64(r, "sort_order")) }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	opt, err := h.svc.repo.CreateOptionForField(r.Context(), chi.URLParam(r, "fieldID"), &in)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, opt)
}
