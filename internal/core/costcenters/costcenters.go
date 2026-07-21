package costcenters

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Modelle ──────────────────────────────────────────────────

type CostCenter struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	CreatedBy string `json:"created_by"`
}

type CreateInput struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

type UpdateInput struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, c *CostCenter) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO cost_centers (id, number, name, created_by)
		 VALUES (gen_random_uuid(), $1, $2, $3)
		 RETURNING id, active`,
		c.Number, c.Name, c.CreatedBy,
	).Scan(&c.ID, &c.Active)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*CostCenter, error) {
	c := &CostCenter{}
	err := r.db.QueryRow(ctx,
		`SELECT id, number, name, active, created_by FROM cost_centers WHERE id=$1`, id,
	).Scan(&c.ID, &c.Number, &c.Name, &c.Active, &c.CreatedBy)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *Repository) List(ctx context.Context, includeInactive bool) ([]*CostCenter, error) {
	query := `SELECT id, number, name, active, created_by FROM cost_centers`
	if !includeInactive {
		query += ` WHERE active=true`
	}
	query += ` ORDER BY number`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*CostCenter
	for rows.Next() {
		c := &CostCenter{}
		if err := rows.Scan(&c.ID, &c.Number, &c.Name, &c.Active, &c.CreatedBy); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, nil
}

func (r *Repository) Update(ctx context.Context, id, number, name string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE cost_centers SET number=$1, name=$2, updated_at=NOW() WHERE id=$3`,
		number, name, id)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE cost_centers SET active=false, updated_at=NOW() WHERE id=$1`, id)
	return err
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateInput, userID string) (*CostCenter, error) {
	c := &CostCenter{Number: in.Number, Name: in.Name, CreatedBy: userID}
	return c, s.repo.Create(ctx, c)
}
func (s *Service) GetByID(ctx context.Context, id string) (*CostCenter, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *Service) List(ctx context.Context, includeInactive bool) ([]*CostCenter, error) {
	return s.repo.List(ctx, includeInactive)
}
func (s *Service) Update(ctx context.Context, id string, in *UpdateInput) error {
	return s.repo.Update(ctx, id, in.Number, in.Name)
}
func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

// ── Handler (JSON API unter /api/v1/costcenters) ──────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}", h.Update) // POST statt PUT (Cloudflare/Nginx blockiert PUT)
	r.Delete("/{id}", h.Deactivate)
	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	includeInactive := r.URL.Query().Get("all") == "true"
	items, err := h.svc.List(r.Context(), includeInactive)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, items)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
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

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, c)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
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
