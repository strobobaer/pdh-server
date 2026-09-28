// Package directory verwaltet das systemweite Hersteller-/
// Lieferantenverzeichnis (business_partners) - ein Eintrag kann
// Hersteller, Lieferant oder beides sein. Ersatzteile, Infrastruktur und
// IT-Geraete koennen einen Eintrag ueber manufacturer_id referenzieren.
package directory

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

type Kind string

const (
	KindManufacturer Kind = "manufacturer"
	KindSupplier     Kind = "supplier"
	KindBoth         Kind = "both"
)

type Partner struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	ContactName string `json:"contact_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Website     string `json:"website"`
	Address     string `json:"address"`
	Notes       string `json:"notes"`
	Active      bool   `json:"active"`
	CreatedBy   string `json:"created_by"`
}

type CreateInput struct {
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	ContactName string `json:"contact_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Website     string `json:"website"`
	Address     string `json:"address"`
	Notes       string `json:"notes"`
}

type UpdateInput struct {
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	ContactName string `json:"contact_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Website     string `json:"website"`
	Address     string `json:"address"`
	Notes       string `json:"notes"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, p *Partner) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO business_partners (id, name, kind, contact_name, email, phone, website, address, notes, created_by)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, active`,
		p.Name, p.Kind, p.ContactName, p.Email, p.Phone, p.Website, p.Address, p.Notes, p.CreatedBy,
	).Scan(&p.ID, &p.Active)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Partner, error) {
	p := &Partner{}
	err := r.db.QueryRow(ctx,
		`SELECT id, name, kind, contact_name, email, phone, website, address, notes, active, COALESCE(created_by::text,'')
		 FROM business_partners WHERE id=$1`, id,
	).Scan(&p.ID, &p.Name, &p.Kind, &p.ContactName, &p.Email, &p.Phone, &p.Website, &p.Address, &p.Notes, &p.Active, &p.CreatedBy)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Repository) List(ctx context.Context, includeInactive bool, kind Kind) ([]*Partner, error) {
	query := `SELECT id, name, kind, contact_name, email, phone, website, address, notes, active, COALESCE(created_by::text,'') FROM business_partners WHERE 1=1`
	args := []interface{}{}
	n := 1
	if !includeInactive {
		query += " AND active=true"
	}
	if kind != "" {
		if kind == KindManufacturer || kind == KindSupplier {
			query += " AND (kind=$" + itoa(n) + " OR kind='both')"
			args = append(args, kind)
			n++
		} else {
			query += " AND kind=$" + itoa(n)
			args = append(args, kind)
			n++
		}
	}
	query += " ORDER BY name"
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Partner
	for rows.Next() {
		p := &Partner{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.ContactName, &p.Email, &p.Phone, &p.Website, &p.Address, &p.Notes, &p.Active, &p.CreatedBy); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func itoa(n int) string {
	digits := "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return itoa(n/10) + string(digits[n%10])
}

func (r *Repository) Update(ctx context.Context, id string, in *UpdateInput) error {
	_, err := r.db.Exec(ctx,
		`UPDATE business_partners SET name=$1, kind=$2, contact_name=$3, email=$4, phone=$5, website=$6, address=$7, notes=$8, updated_at=NOW() WHERE id=$9`,
		in.Name, in.Kind, in.ContactName, in.Email, in.Phone, in.Website, in.Address, in.Notes, id)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE business_partners SET active=false, updated_at=NOW() WHERE id=$1`, id)
	return err
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateInput, userID string) (*Partner, error) {
	kind := in.Kind
	if kind == "" {
		kind = KindManufacturer
	}
	p := &Partner{
		Name: in.Name, Kind: kind, ContactName: in.ContactName, Email: in.Email,
		Phone: in.Phone, Website: in.Website, Address: in.Address, Notes: in.Notes,
		CreatedBy: userID,
	}
	return p, s.repo.Create(ctx, p)
}
func (s *Service) GetByID(ctx context.Context, id string) (*Partner, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *Service) List(ctx context.Context, includeInactive bool, kind Kind) ([]*Partner, error) {
	return s.repo.List(ctx, includeInactive, kind)
}
func (s *Service) Update(ctx context.Context, id string, in *UpdateInput) error {
	return s.repo.Update(ctx, id, in)
}
func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

// ── Handler (JSON API unter /api/v1/directory) ────────────────

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
	kind := Kind(r.URL.Query().Get("kind"))
	items, err := h.svc.List(r.Context(), includeInactive, kind)
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
	p, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, p)
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
