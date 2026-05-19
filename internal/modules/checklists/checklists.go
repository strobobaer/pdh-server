package checklists

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

type ItemType string

const (
	TypeCheckbox ItemType = "checkbox" // Abhaken
	TypeText     ItemType = "text"     // Freier Text
	TypeNumber   ItemType = "number"   // Zahlenwert
	TypeCompare  ItemType = "compare"  // Vergleichswert (Soll/Ist)
	TypeSelect   ItemType = "select"   // Auswahl
	TypeImage    ItemType = "image"    // Foto-Dokumentation
)

// ── Modelle ──────────────────────────────────────────────────

type Checklist struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	Items       []Item    `json:"items,omitempty"`
}

type Item struct {
	ID               string   `json:"id"`
	ChecklistID      string   `json:"checklist_id"`
	SortOrder        int      `json:"sort_order"`
	Type             ItemType `json:"type"`
	Label            string   `json:"label"`
	Description      string   `json:"description,omitempty"`
	Required         bool     `json:"required"`
	CompareValue     *float64 `json:"compare_value,omitempty"`
	CompareUnit      string   `json:"compare_unit,omitempty"`
	CompareTolerance *float64 `json:"compare_tolerance,omitempty"`
	Options          []string `json:"options,omitempty"`
	MinValue         *float64 `json:"min_value,omitempty"`
	MaxValue         *float64 `json:"max_value,omitempty"`
}

type TaskLog struct {
	ID          string   `json:"id"`
	TaskID      string   `json:"task_id"`
	ItemID      string   `json:"item_id"`
	Checked     *bool    `json:"checked,omitempty"`
	TextValue   string   `json:"text_value,omitempty"`
	NumberValue *float64 `json:"number_value,omitempty"`
	Selected    string   `json:"selected,omitempty"`
	OK          *bool    `json:"ok,omitempty"`
	Note        string   `json:"note,omitempty"`
	CreatedBy   string   `json:"created_by"`
	// Joined
	ItemLabel string `json:"item_label,omitempty"`
	ItemType  string `json:"item_type,omitempty"`
}

// ── Inputs ───────────────────────────────────────────────────

type CreateChecklistInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Items       []CreateItemInput `json:"items"`
}

type CreateItemInput struct {
	Type             ItemType `json:"type"`
	Label            string   `json:"label"`
	Description      string   `json:"description"`
	Required         bool     `json:"required"`
	CompareValue     *float64 `json:"compare_value,omitempty"`
	CompareUnit      string   `json:"compare_unit,omitempty"`
	CompareTolerance *float64 `json:"compare_tolerance,omitempty"`
	Options          []string `json:"options,omitempty"`
	MinValue         *float64 `json:"min_value,omitempty"`
	MaxValue         *float64 `json:"max_value,omitempty"`
}

type SaveLogInput struct {
	ItemID      string   `json:"item_id"`
	Checked     *bool    `json:"checked,omitempty"`
	TextValue   string   `json:"text_value,omitempty"`
	NumberValue *float64 `json:"number_value,omitempty"`
	Selected    string   `json:"selected,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, c *Checklist) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO maintenance_checklists (id, name, description, category, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
		RETURNING id, created_at`,
		c.Name, c.Description, c.Category, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return err
	}

	for i, item := range c.Items {
		opts, _ := json.Marshal(item.Options)
		var itemID string
		r.db.QueryRow(ctx, `
			INSERT INTO checklist_items
			  (id, checklist_id, sort_order, item_type, label, description,
			   required, compare_value, compare_unit, compare_tolerance,
			   options, min_value, max_value)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id`,
			c.ID, i, item.Type, item.Label, item.Description,
			item.Required, item.CompareValue, item.CompareUnit,
			item.CompareTolerance, opts, item.MinValue, item.MaxValue,
		).Scan(&itemID)
		c.Items[i].ID = itemID
	}
	return nil
}

func (r *Repository) List(ctx context.Context, category string) ([]*Checklist, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(category,''), created_by, created_at
		FROM maintenance_checklists WHERE 1=1`
	args := []interface{}{}
	if category != "" {
		query += " AND category=$1"
		args = append(args, category)
	}
	query += " ORDER BY name"
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Checklist
	for rows.Next() {
		c := &Checklist{}
		rows.Scan(&c.ID, &c.Name, &c.Description, &c.Category, &c.CreatedBy, &c.CreatedAt)
		list = append(list, c)
	}
	return list, nil
}

func (r *Repository) GetWithItems(ctx context.Context, id string) (*Checklist, error) {
	c := &Checklist{}
	err := r.db.QueryRow(ctx, `
		SELECT id, name, COALESCE(description,''), COALESCE(category,''), created_by, created_at
		FROM maintenance_checklists WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.Description, &c.Category, &c.CreatedBy, &c.CreatedAt)
	if err != nil {
		return nil, err
	}

	rows, _ := r.db.Query(ctx, `
		SELECT id, checklist_id, sort_order, item_type, label, COALESCE(description,''),
		       required, compare_value, COALESCE(compare_unit,''), compare_tolerance,
		       COALESCE(options,'[]'::jsonb), min_value, max_value
		FROM checklist_items WHERE checklist_id=$1 ORDER BY sort_order`, id)
	defer rows.Close()

	for rows.Next() {
		item := Item{}
		var opts []byte
		rows.Scan(&item.ID, &item.ChecklistID, &item.SortOrder, &item.Type,
			&item.Label, &item.Description, &item.Required,
			&item.CompareValue, &item.CompareUnit, &item.CompareTolerance,
			&opts, &item.MinValue, &item.MaxValue)
		json.Unmarshal(opts, &item.Options)
		c.Items = append(c.Items, item)
	}
	return c, nil
}

func (r *Repository) SaveLog(ctx context.Context, taskID, userID string, in *SaveLogInput) error {
	// Vergleichswert-OK berechnen
	var ok *bool
	if in.NumberValue != nil {
		var compareVal, tolerance float64
		r.db.QueryRow(ctx, `SELECT COALESCE(compare_value,0), COALESCE(compare_tolerance,0) FROM checklist_items WHERE id=$1`, in.ItemID).
			Scan(&compareVal, &tolerance)
		if compareVal != 0 {
			diff := *in.NumberValue - compareVal
			if diff < 0 {
				diff = -diff
			}
			okVal := diff <= tolerance
			ok = &okVal
		}
	}

	_, err := r.db.Exec(ctx, `
		INSERT INTO task_checklist_logs
		  (id, task_id, item_id, checked, text_value, number_value, selected, ok, note, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (task_id, item_id) DO UPDATE SET
		  checked=$3, text_value=$4, number_value=$5, selected=$6, ok=$7, note=$8`,
		taskID, in.ItemID, in.Checked, in.TextValue,
		in.NumberValue, in.Selected, ok, in.Note, userID)
	return err
}

func (r *Repository) GetLogs(ctx context.Context, taskID string) ([]*TaskLog, error) {
	rows, err := r.db.Query(ctx, `
		SELECT l.id, l.task_id, l.item_id, l.checked, COALESCE(l.text_value,''),
		       l.number_value, COALESCE(l.selected,''), l.ok, COALESCE(l.note,''),
		       l.created_by, ci.label, ci.item_type
		FROM task_checklist_logs l
		JOIN checklist_items ci ON l.item_id = ci.id
		WHERE l.task_id=$1
		ORDER BY ci.sort_order`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*TaskLog
	for rows.Next() {
		l := &TaskLog{}
		rows.Scan(&l.ID, &l.TaskID, &l.ItemID, &l.Checked,
			&l.TextValue, &l.NumberValue, &l.Selected, &l.OK,
			&l.Note, &l.CreatedBy, &l.ItemLabel, &l.ItemType)
		logs = append(logs, l)
	}
	return logs, nil
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateChecklistInput, userID string) (*Checklist, error) {
	c := &Checklist{Name: in.Name, Description: in.Description, Category: in.Category, CreatedBy: userID}
	for _, i := range in.Items {
		c.Items = append(c.Items, Item{
			Type: i.Type, Label: i.Label, Description: i.Description,
			Required: i.Required, CompareValue: i.CompareValue,
			CompareUnit: i.CompareUnit, CompareTolerance: i.CompareTolerance,
			Options: i.Options, MinValue: i.MinValue, MaxValue: i.MaxValue,
		})
	}
	return c, s.repo.Create(ctx, c)
}

func (s *Service) List(ctx context.Context, category string) ([]*Checklist, error) {
	return s.repo.List(ctx, category)
}
func (s *Service) Get(ctx context.Context, id string) (*Checklist, error) {
	return s.repo.GetWithItems(ctx, id)
}
func (s *Service) SaveLog(ctx context.Context, taskID, userID string, in *SaveLogInput) error {
	return s.repo.SaveLog(ctx, taskID, userID, in)
}
func (s *Service) GetLogs(ctx context.Context, taskID string) ([]*TaskLog, error) {
	return s.repo.GetLogs(ctx, taskID)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Get("/task/{taskID}/logs", h.GetLogs)
	r.Post("/task/{taskID}/log", h.SaveLog)
	return r
}

func decode(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateChecklistInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	c, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, c)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context(), r.URL.Query().Get("category"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, list)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, c)
}

func (h *Handler) SaveLog(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	var in SaveLogInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.SaveLog(r.Context(), taskID, userID, &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GetLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := h.svc.GetLogs(r.Context(), chi.URLParam(r, "taskID"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, logs)
}

func floatPtr(s string) *float64 {
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

var _ = fmt.Sprintf
var _ = floatPtr
