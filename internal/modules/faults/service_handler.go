package faults

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// Service
type Service struct {
	repo    *Repository
	copilot *Copilot
}

func NewService(repo *Repository, copilot *Copilot) *Service {
	return &Service{repo: repo, copilot: copilot}
}

func (s *Service) Create(ctx context.Context, in *CreateFaultInput, userID string) (*Fault, error) {
	f := &Fault{
		Title:            in.Title,
		Description:      in.Description,
		Symptoms:         in.Symptoms,
		Severity:         in.Severity,
		InfrastructureID: in.InfrastructureID,
		CreatedBy:        userID,
	}
	return f, s.repo.Create(ctx, f)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Fault, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status FaultStatus) ([]*Fault, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) Analyze(ctx context.Context, faultID string) (*CopilotAnalysis, error) {
	fault, err := s.repo.GetByID(ctx, faultID)
	if err != nil {
		return nil, err
	}
	s.repo.UpdateStatus(ctx, faultID, StatusAnalyzing)
	analysis, err := s.copilot.Analyze(ctx, fault)
	if err != nil {
		s.repo.UpdateStatus(ctx, faultID, StatusDetected)
		return nil, err
	}
	s.repo.UpdateStatus(ctx, faultID, StatusInProgress)
	return analysis, nil
}

func (s *Service) GetAnalysis(ctx context.Context, faultID string) (*CopilotAnalysis, error) {
	return s.repo.GetAnalysis(ctx, faultID)
}

func (s *Service) Chat(ctx context.Context, faultID, userID, message string, history []anthropicMessage) (string, error) {
	fault, err := s.repo.GetByID(ctx, faultID)
	if err != nil {
		return "", err
	}
	return s.copilot.Chat(ctx, fault, history, message)
}

func (s *Service) Resolve(ctx context.Context, faultID, resolution, rootCause string) error {
	return s.repo.Resolve(ctx, faultID, resolution, rootCause)
}

// Handler
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/analyze", h.Analyze)
	r.Get("/{id}/analysis", h.GetAnalysis)
	r.Post("/{id}/chat", h.Chat)
	r.Post("/{id}/resolve", h.Resolve)

	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := FaultStatus(r.URL.Query().Get("status"))
	faults, err := h.svc.List(r.Context(), status)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, faults)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateFaultInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	fault, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, fault)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	fault, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusNotFound, "störung nicht gefunden")
		return
	}
	response.JSON(w, http.StatusOK, fault)
}

func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	analysis, err := h.svc.Analyze(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "copilot analyse fehlgeschlagen: "+err.Error())
		return
	}
	response.JSON(w, http.StatusOK, analysis)
}

func (h *Handler) GetAnalysis(w http.ResponseWriter, r *http.Request) {
	analysis, err := h.svc.GetAnalysis(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusNotFound, "keine analyse vorhanden")
		return
	}
	response.JSON(w, http.StatusOK, analysis)
}

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Message string              `json:"message"`
		History []anthropicMessage  `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	reply, err := h.svc.Chat(r.Context(), id, userID, in.Message, in.History)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"reply": reply})
}

func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in ResolveInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	if err := h.svc.Resolve(r.Context(), id, in.Resolution, in.RootCause); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
