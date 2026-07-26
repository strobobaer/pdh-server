package tasks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service {
	globalTaskRepo = repo
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, in *CreateTaskInput, userID string) (*Task, error) {
	t := &Task{
		Title: in.Title, Description: in.Description, Priority: in.Priority,
		AssignedTo: in.AssignedTo, ResponsibleTo: in.ResponsibleTo, ProjectID: in.ProjectID,
		CreatedBy: userID, Color: in.Color,
	}
	if in.DueDate != "" {
		if due, err := parseDate(in.DueDate); err == nil {
			t.DueDate = &due
		}
	}
	if in.StartDate != "" {
		if start, err := parseDate(in.StartDate); err == nil {
			t.StartDate = &start
		}
	}
	if in.Priority == "" {
		t.Priority = PrioMedium
	}
	return t, s.repo.Create(ctx, t)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Task, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status Status, projectID string, unassignedOnly bool) ([]*Task, error) {
	return s.repo.List(ctx, status, projectID, unassignedOnly)
}

func (s *Service) Update(ctx context.Context, id string, in *UpdateTaskInput) error {
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
	r.Post("/{id}/status", h.UpdateStatus)
	r.Post("/{id}/resolve", h.Resolve)
	r.Post("/{id}/actions", h.AddAction)
	r.Get("/{id}/actions", h.GetActions)
	r.Delete("/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/{id}/pending-parts", h.AddPendingPart)
	r.Get("/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := Status(r.URL.Query().Get("status"))
	projectID := r.URL.Query().Get("project_id")
	unassigned := r.URL.Query().Get("unassigned") == "true"
	tasks, err := h.svc.List(r.Context(), status, projectID, unassigned)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, tasks)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateTaskInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	t, err := h.svc.Create(r.Context(), &in, uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, t)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "aufgabe nicht gefunden")
		return
	}
	response.JSON(w, 200, t)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateTaskInput
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

func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status Status `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdateStatus(r.Context(), chi.URLParam(r, "id"), in.Status, uid(r)); err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": string(in.Status)})
}

func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolution    string `json:"resolution"`
		RootCause     string `json:"root_cause"`
		NoPartsNeeded bool   `json:"no_parts_needed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.Resolve(r.Context(), chi.URLParam(r, "id"), in.Resolution, in.RootCause, uid(r), in.NoPartsNeeded); err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "resolved"})
}

func (h *Handler) AddAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	a, err := h.svc.AddAction(r.Context(), chi.URLParam(r, "id"), in.Description, uid(r))
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) GetActions(w http.ResponseWriter, r *http.Request) {
	actions, err := h.svc.GetActions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, actions)
}

func (h *Handler) DeleteAction(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteAction(r.Context(), chi.URLParam(r, "actionID")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) AddPendingPart(w http.ResponseWriter, r *http.Request) {
	var in AddPendingPartInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	p, err := h.svc.AddPendingPart(r.Context(), chi.URLParam(r, "id"), &in, uid(r))
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) GetPendingParts(w http.ResponseWriter, r *http.Request) {
	parts, err := h.svc.GetPendingParts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, parts)
}

func (h *Handler) DeletePendingPart(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePendingPart(r.Context(), chi.URLParam(r, "partItemID")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "entfernt"})
}

func (h *Handler) GetPartsUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := h.svc.GetPartsUsage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, usage)
}

