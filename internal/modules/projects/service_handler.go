package projects

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateProjectInput, userID string) (*Project, error) {
	p := &Project{
		Name: in.Name, Description: in.Description,
		ResponsibleTo: in.ResponsibleTo, InfrastructureID: in.InfrastructureID,
		CostCenterID: in.CostCenterID, CreatedBy: userID,
	}
	if in.StartDate != "" {
		if d, err := parseDate(in.StartDate); err == nil {
			p.StartDate = &d
		}
	}
	if in.EndDate != "" {
		if d, err := parseDate(in.EndDate); err == nil {
			p.EndDate = &d
		}
	}
	return p, s.repo.Create(ctx, p)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Project, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status Status) ([]*Project, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) Update(ctx context.Context, id string, in *UpdateProjectInput) error {
	if in.Status == "" {
		in.Status = StatusPlanning
	}
	return s.repo.Update(ctx, id, in)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/edit", h.Update)
	r.Delete("/{id}", h.Delete)

	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := Status(r.URL.Query().Get("status"))
	list, err := h.svc.List(r.Context(), status)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, list)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if in.Name == "" {
		response.Error(w, 400, "name ist pflicht")
		return
	}
	p, err := h.svc.Create(r.Context(), &in, uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "projekt nicht gefunden")
		return
	}
	response.JSON(w, 200, p)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}
