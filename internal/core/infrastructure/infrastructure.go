package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type InfraType string

const (
	TypeBuilding InfraType = "building"
	TypeLine     InfraType = "line"
	TypePlant    InfraType = "plant"
	TypeDevice   InfraType = "device"
)

type Infrastructure struct {
	ID           string            `json:"id"`
	ParentID     *string           `json:"parent_id,omitempty"`
	Name         string            `json:"name"`
	Type         InfraType         `json:"type"`
	Description  string            `json:"description,omitempty"`
	Location     string            `json:"location,omitempty"`
	SerialNo     string            `json:"serial_no,omitempty"`
	Manufacturer string            `json:"manufacturer,omitempty"`
	Model        string            `json:"model,omitempty"`
	CostCenter   string            `json:"cost_center,omitempty"`
	InstalledAt  *string           `json:"installed_at,omitempty"`
	Active       bool              `json:"active"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Children     []*Infrastructure `json:"children,omitempty"`
}

type CreateInput struct {
	ParentID     *string   `json:"parent_id,omitempty"`
	Name         string    `json:"name"`
	Type         InfraType `json:"type"`
	Description  string    `json:"description,omitempty"`
	Location     string    `json:"location,omitempty"`
	SerialNo     string    `json:"serial_no,omitempty"`
	Manufacturer string    `json:"manufacturer,omitempty"`
	Model        string    `json:"model,omitempty"`
	CostCenter   string    `json:"cost_center,omitempty"`
	InstalledAt  *string   `json:"installed_at,omitempty"`
}

type UpdateInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Location     string `json:"location"`
	SerialNo     string `json:"serial_no"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	CostCenter   string `json:"cost_center"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

const selectCols = `id, parent_id, name, type, COALESCE(description,'') AS description, COALESCE(location,'') AS location,
	COALESCE(serial_no,'') AS serial_no, COALESCE(manufacturer,'') AS manufacturer, COALESCE(model,'') AS model,
	COALESCE(cost_center,'') AS cost_center, installed_at::text,
	active, created_at, updated_at`

// scanItem liest eine Zeile – Reihenfolge muss mit selectCols übereinstimmen
func scanItem(row interface{ Scan(...interface{}) error }) (*Infrastructure, error) {
	i := &Infrastructure{}
	err := row.Scan(
		&i.ID, &i.ParentID, &i.Name, &i.Type,
		&i.Description, &i.Location,
		&i.SerialNo, &i.Manufacturer, &i.Model,
		&i.CostCenter,
		&i.InstalledAt,
		&i.Active, &i.CreatedAt, &i.UpdatedAt,
	)
	return i, err
}

func (r *Repository) Create(ctx context.Context, i *Infrastructure) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO infrastructure
		 (id, parent_id, name, type, description, location, serial_no, manufacturer, model, cost_center, installed_at)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, active, created_at, updated_at`,
		i.ParentID, i.Name, i.Type, i.Description,
		i.Location, i.SerialNo, i.Manufacturer, i.Model, i.CostCenter, i.InstalledAt,
	).Scan(&i.ID, &i.Active, &i.CreatedAt, &i.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Infrastructure, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+selectCols+` FROM infrastructure WHERE id=$1 AND active=true`, id)
	return scanItem(row)
}

func (r *Repository) List(ctx context.Context, parentID *string, infraType InfraType) ([]*Infrastructure, error) {
	query := `SELECT ` + selectCols + ` FROM infrastructure WHERE active=true`
	args := []interface{}{}
	n := 1
	if parentID != nil {
		query += fmt.Sprintf(" AND parent_id=$%d", n)
		args = append(args, *parentID)
		n++
	} else {
		query += " AND parent_id IS NULL"
	}
	if infraType != "" {
		query += fmt.Sprintf(" AND type=$%d", n)
		args = append(args, infraType)
	}
	query += " ORDER BY type, name"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*Infrastructure
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, nil
}

func (r *Repository) GetTree(ctx context.Context) ([]*Infrastructure, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+selectCols+` FROM infrastructure WHERE active=true ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all := map[string]*Infrastructure{}
	var roots []*Infrastructure

	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		i.Children = []*Infrastructure{}
		all[i.ID] = i
	}

	for _, item := range all {
		if item.ParentID == nil {
			roots = append(roots, item)
		} else if parent, ok := all[*item.ParentID]; ok {
			parent.Children = append(parent.Children, item)
		}
	}
	return roots, nil
}

func (r *Repository) Update(ctx context.Context, i *Infrastructure) error {
	_, err := r.db.Exec(ctx,
		`UPDATE infrastructure SET name=$1, description=$2, location=$3,
		 serial_no=$4, manufacturer=$5, model=$6, cost_center=$7, updated_at=NOW() WHERE id=$8`,
		i.Name, i.Description, i.Location,
		i.SerialNo, i.Manufacturer, i.Model, i.CostCenter, i.ID)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE infrastructure SET active=false, updated_at=NOW() WHERE id=$1`, id)
	return err
}

func (r *Repository) Search(ctx context.Context, q string) ([]*Infrastructure, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+selectCols+` FROM infrastructure WHERE active=true AND (
		 name ILIKE $1 OR description ILIKE $1 OR serial_no ILIKE $1 OR
		 manufacturer ILIKE $1 OR model ILIKE $1 OR location ILIKE $1 OR cost_center ILIKE $1)
		 ORDER BY type, name LIMIT 50`, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*Infrastructure
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, nil
}

func (r *Repository) GetStats(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx,
		`SELECT type, COUNT(*) FROM infrastructure WHERE active=true GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]int{}
	for rows.Next() {
		var t string
		var c int
		rows.Scan(&t, &c)
		stats[t] = c
	}
	return stats, nil
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateInput) (*Infrastructure, error) {
	i := &Infrastructure{ParentID: in.ParentID, Name: in.Name, Type: in.Type,
		Description: in.Description, Location: in.Location,
		SerialNo: in.SerialNo, Manufacturer: in.Manufacturer,
		Model: in.Model, CostCenter: in.CostCenter, InstalledAt: in.InstalledAt}
	return i, s.repo.Create(ctx, i)
}
func (s *Service) GetByID(ctx context.Context, id string) (*Infrastructure, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *Service) List(ctx context.Context, p *string, t InfraType) ([]*Infrastructure, error) {
	return s.repo.List(ctx, p, t)
}
func (s *Service) GetTree(ctx context.Context) ([]*Infrastructure, error) { return s.repo.GetTree(ctx) }
func (s *Service) Search(ctx context.Context, q string) ([]*Infrastructure, error) {
	return s.repo.Search(ctx, q)
}
func (s *Service) GetStats(ctx context.Context) (map[string]int, error) { return s.repo.GetStats(ctx) }
func (s *Service) Deactivate(ctx context.Context, id string) error      { return s.repo.Deactivate(ctx, id) }
func (s *Service) Update(ctx context.Context, id string, in *UpdateInput) error {
	return s.repo.Update(ctx, &Infrastructure{ID: id, Name: in.Name,
		Description: in.Description, Location: in.Location,
		SerialNo: in.SerialNo, Manufacturer: in.Manufacturer, Model: in.Model, CostCenter: in.CostCenter})
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/tree", h.GetTree)
	r.Get("/search", h.Search)
	r.Get("/stats", h.Stats)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}", h.Update) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Delete("/{id}", h.Deactivate)
	r.Get("/{id}/children", h.GetChildren)
	return r
}

func decode(r *http.Request, v interface{}) error { return json.NewDecoder(r.Body).Decode(v) }

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	item, err := h.svc.Create(r.Context(), &in)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, item)
}
func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	item, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, item)
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	var p *string
	if v := r.URL.Query().Get("parent_id"); v != "" {
		p = &v
	}
	items, err := h.svc.List(r.Context(), p, InfraType(r.URL.Query().Get("type")))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, items)
}
func (h *Handler) GetTree(w http.ResponseWriter, r *http.Request) {
	tree, err := h.svc.GetTree(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, tree)
}
func (h *Handler) GetChildren(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	items, err := h.svc.List(r.Context(), &id, InfraType(r.URL.Query().Get("type")))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, items)
}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "aktualisiert"})
}
func (h *Handler) Deactivate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Deactivate(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "deaktiviert"})
}
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		response.Error(w, 400, "suchbegriff fehlt")
		return
	}
	items, err := h.svc.Search(r.Context(), q)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, items)
}
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.GetStats(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, stats)
}
