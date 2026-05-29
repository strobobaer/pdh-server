package maintenance

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type createChecklistTemplateInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type createChecklistTemplateItemInput struct {
	Label        string `json:"label"`
	Description  string `json:"description"`
	ItemType     string `json:"item_type"`
	Required     bool   `json:"required"`
	IntervalDays int    `json:"interval_days"`
	SortOrder    int    `json:"sort_order"`
}

type assignChecklistTemplateInput struct {
	TemplateID         string `json:"template_id"`
	DefaultDurationMin int    `json:"default_duration_min"`
}

type saveTaskChecklistInput struct {
	Values map[string]string `json:"values"`
	Done   map[string]bool   `json:"done"`
}

func (s *Service) ListChecklistTemplatesForAPI(r *http.Request) ([]*ChecklistTemplate, error) {
	return s.repo.ListChecklistTemplates(r.Context())
}

func (s *Service) CreateChecklistTemplateForAPI(r *http.Request, in createChecklistTemplateInput, userID string) (*ChecklistTemplate, error) {
	return s.repo.CreateChecklistTemplate(r.Context(), strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), userID)
}

func (s *Service) ListChecklistTemplateItemsForAPI(r *http.Request, templateID string) ([]*ChecklistTemplateItem, error) {
	return s.repo.ListChecklistTemplateItems(r.Context(), templateID)
}

func (s *Service) CreateChecklistTemplateItemForAPI(r *http.Request, templateID string, in createChecklistTemplateItemInput) (*ChecklistTemplateItem, error) {
	item := &ChecklistTemplateItem{
		TemplateID: templateID,
		Label: strings.TrimSpace(in.Label), Description: strings.TrimSpace(in.Description),
		ItemType: in.ItemType, Required: in.Required, IntervalDays: in.IntervalDays, SortOrder: in.SortOrder,
	}
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.IntervalDays <= 0 { item.IntervalDays = 1 }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	if err := s.repo.CreateChecklistTemplateItem(r.Context(), item); err != nil { return nil, err }
	return item, nil
}

func (s *Service) AssignChecklistTemplateForAPI(r *http.Request, planID string, in assignChecklistTemplateInput) error {
	return s.repo.AssignChecklistTemplateToPlan(r.Context(), planID, in.TemplateID, in.DefaultDurationMin)
}

func (s *Service) DueChecklistItemsForTaskForAPI(r *http.Request, taskID string) ([]*TaskChecklistItem, error) {
	return s.repo.DueChecklistItemsForTask(r.Context(), taskID)
}

func (s *Service) SaveTaskChecklistForAPI(r *http.Request, taskID, userID string, in saveTaskChecklistInput) error {
	if in.Values == nil { in.Values = map[string]string{} }
	if in.Done == nil { in.Done = map[string]bool{} }
	return s.repo.SaveTaskChecklistResults(r.Context(), taskID, userID, in.Values, in.Done)
}

func (s *Service) DefaultDurationForTaskForAPI(r *http.Request, taskID string) int {
	return s.repo.DefaultDurationForTask(r.Context(), taskID)
}

func (h *Handler) ChecklistRoutes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/templates", h.ListChecklistTemplates)
	r.Post("/templates", h.CreateChecklistTemplate)
	r.Get("/templates/{templateID}/items", h.ListChecklistTemplateItems)
	r.Post("/templates/{templateID}/items", h.CreateChecklistTemplateItem)
	r.Put("/plans/{planID}/template", h.AssignChecklistTemplate)
	r.Get("/tasks/{taskID}/due-checklist", h.DueChecklistItemsForTask)
	r.Post("/tasks/{taskID}/checklist-results", h.SaveTaskChecklistResults)
	r.Get("/tasks/{taskID}/default-duration", h.DefaultDurationForTask)
	return r
}

func (h *Handler) ListChecklistTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListChecklistTemplatesForAPI(r)
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, items)
}

func (h *Handler) CreateChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	var in createChecklistTemplateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	if strings.TrimSpace(in.Name) == "" { response.Error(w, 400, "name ist pflicht"); return }
	item, err := h.svc.CreateChecklistTemplateForAPI(r, in, uid(r))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 201, item)
}

func (h *Handler) ListChecklistTemplateItems(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListChecklistTemplateItemsForAPI(r, chi.URLParam(r, "templateID"))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, items)
}

func (h *Handler) CreateChecklistTemplateItem(w http.ResponseWriter, r *http.Request) {
	var in createChecklistTemplateItemInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	if strings.TrimSpace(in.Label) == "" { response.Error(w, 400, "label ist pflicht"); return }
	if in.IntervalDays <= 0 { response.Error(w, 400, "intervall ist pflicht"); return }
	item, err := h.svc.CreateChecklistTemplateItemForAPI(r, chi.URLParam(r, "templateID"), in)
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 201, item)
}

func (h *Handler) AssignChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	var in assignChecklistTemplateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	if err := h.svc.AssignChecklistTemplateForAPI(r, chi.URLParam(r, "planID"), in); err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, map[string]string{"status":"gespeichert"})
}

func (h *Handler) DueChecklistItemsForTask(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.DueChecklistItemsForTaskForAPI(r, chi.URLParam(r, "taskID"))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, items)
}

func (h *Handler) SaveTaskChecklistResults(w http.ResponseWriter, r *http.Request) {
	var in saveTaskChecklistInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	if err := h.svc.SaveTaskChecklistForAPI(r, chi.URLParam(r, "taskID"), uid(r), in); err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, map[string]string{"status":"gespeichert"})
}

func (h *Handler) DefaultDurationForTask(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, 200, map[string]int{"default_duration_min": h.svc.DefaultDurationForTaskForAPI(r, chi.URLParam(r, "taskID"))})
}
