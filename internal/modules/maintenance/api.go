package maintenance

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// REST-Schnittstelle /api/v1/maintenance. Bisherige Pfade bleiben gleich
// (Leitstand, Assistent, Zeiterfassung, Anlegen-Assistent nutzen sie).

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	// Plaene
	r.Get("/plans", h.ListPlans)
	r.Post("/plans", h.CreatePlan)
	r.Get("/plans/{id}", h.GetPlan)
	r.Put("/plans/{id}", h.UpdatePlan)
	r.Post("/plans/{id}/active", h.SetPlanActive)

	// Auftraege
	r.Get("/tasks", h.ListTasks)
	r.Post("/tasks", h.CreateTask)
	r.Get("/tasks/due", h.GetDueToday)
	r.Post("/tasks/generate", h.GenerateTasks)
	r.Get("/tasks/{id}", h.GetTask)
	r.Post("/tasks/{id}/due-date", h.UpdateDueDate)
	r.Post("/tasks/{id}/start", h.StartTask)
	r.Post("/tasks/{id}/complete", h.CompleteTask)
	r.Get("/tasks/{id}/steps", h.GetSteps)
	r.Post("/tasks/{id}/steps/{stepID}", h.SaveStep)
	r.Post("/tasks/{id}/checklists", h.AddTaskChecklist)
	r.Post("/tasks/{id}/actions", h.AddAction)
	r.Get("/tasks/{id}/actions", h.GetActions)
	r.Delete("/tasks/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/tasks/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/tasks/{id}/pending-parts", h.AddPendingPart)
	r.Get("/tasks/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/tasks/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

	// Checklisten (Vorlagen)
	r.Get("/checklists", h.ListTemplates)
	r.Post("/checklists", h.CreateTemplate)
	r.Get("/checklists/{id}", h.GetTemplate)
	r.Put("/checklists/{id}", h.RenameTemplate)
	r.Delete("/checklists/{id}", h.DeleteTemplate)
	r.Post("/checklists/{id}/items", h.AddTemplateItem)
	r.Post("/checklists/{id}/order", h.ReorderTemplateItems)
	r.Put("/checklist-items/{itemID}", h.UpdateTemplateItem)
	r.Delete("/checklist-items/{itemID}", h.DeleteTemplateItem)
	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func decode(r *http.Request, v interface{}) bool {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 256<<10)).Decode(v) == nil
}

// fail: Eingabefehler → 400 mit Klartext, sonst 500.
func fail(w http.ResponseWriter, err error) {
	if IsInputError(err) {
		response.Error(w, http.StatusBadRequest, InputMessage(err))
		return
	}
	response.Error(w, http.StatusInternalServerError, err.Error())
}

// ── Plaene ──

func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.svc.repo.ListPlans(r.Context(), r.URL.Query().Get("infrastructure_id"), r.URL.Query().Get("inactive") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	if plans == nil {
		plans = []*MaintenancePlan{}
	}
	response.JSON(w, 200, plans)
}

func (h *Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPlan(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "Wartungsplan nicht gefunden")
		return
	}
	response.JSON(w, 200, p)
}

func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var in PlanInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	id, err := h.svc.CreatePlan(r.Context(), &in, uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	p, _ := h.svc.GetPlan(r.Context(), id)
	response.JSON(w, 201, p)
}

func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	var in PlanInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.svc.UpdatePlan(r.Context(), id, &in, uid(r)); err != nil {
		fail(w, err)
		return
	}
	p, _ := h.svc.GetPlan(r.Context(), id)
	response.JSON(w, 200, p)
}

func (h *Handler) SetPlanActive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Active bool `json:"active"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.SetPlanActive(r.Context(), chi.URLParam(r, "id"), in.Active); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]bool{"active": in.Active})
}

// ── Auftraege ──

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var tasks []*MaintenanceTask
	var err error
	switch q.Get("status") {
	case "active": // offen + in Arbeit + wartet
		tasks, err = h.svc.repo.FindTasks(r.Context(), TaskFilter{Open: true, InfraID: q.Get("infrastructure_id")})
	default:
		tasks, err = h.svc.ListTasks(r.Context(), TaskStatus(q.Get("status")), q.Get("infrastructure_id"))
	}
	if err != nil {
		fail(w, err)
		return
	}
	if tasks == nil {
		tasks = []*MaintenanceTask{}
	}
	response.JSON(w, 200, tasks)
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.GetTaskByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "Auftrag nicht gefunden")
		return
	}
	response.JSON(w, 200, t)
}

func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var in CreateTaskInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	t, err := h.svc.CreateTask(r.Context(), &in, uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 201, t)
}

func (h *Handler) GetDueToday(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.svc.GetDueToday(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if tasks == nil {
		tasks = []*MaintenanceTask{}
	}
	response.JSON(w, 200, tasks)
}

// UpdateDueDate: nur den Termin setzen (Zeitstrahl), JSON {"due_date":"JJJJ-MM-TT"}.
func (h *Handler) UpdateDueDate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DueDate string `json:"due_date"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	due, err := time.ParseInLocation("2006-01-02", in.DueDate, time.Local)
	if err != nil {
		response.Error(w, 400, "ungültiges datum")
		return
	}
	if err := h.svc.UpdateDueDate(r.Context(), chi.URLParam(r, "id"), due); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GenerateTasks(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.GenerateTasks(r.Context(), uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]int{"generated": n})
}

func (h *Handler) StartTask(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.StartTask(r.Context(), chi.URLParam(r, "id"), uid(r)); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": string(TaskInProgress)})
}

func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	var in CompleteTaskInput
	_ = decode(r, &in)
	if err := h.svc.Complete(r.Context(), chi.URLParam(r, "id"), uid(r), &in); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": string(TaskDone)})
}

// ── Schritte ──

func (h *Handler) GetSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := h.svc.Steps(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, steps)
}

func (h *Handler) SaveStep(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Value string `json:"value"`
		Done  bool   `json:"done"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	s, err := h.svc.SaveStep(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "stepID"), in.Value, in.Done, uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, s)
}

func (h *Handler) AddTaskChecklist(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TemplateID string `json:"template_id"`
		Remove     bool   `json:"remove"`
	}
	if !decode(r, &in) || in.TemplateID == "" {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	id := chi.URLParam(r, "id")
	var err error
	if in.Remove {
		err = h.svc.repo.RemoveTemplateSteps(r.Context(), id, in.TemplateID)
	} else {
		_, err = h.svc.repo.AddTemplateSteps(r.Context(), id, in.TemplateID)
	}
	if err != nil {
		fail(w, err)
		return
	}
	h.GetSteps(w, r)
}

// ── Massnahmen & Ersatzteile ──

func (h *Handler) AddAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string `json:"description"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	a, err := h.svc.AddAction(r.Context(), chi.URLParam(r, "id"), in.Description, uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) GetActions(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.GetActions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, a)
}

func (h *Handler) DeleteAction(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteAction(r.Context(), chi.URLParam(r, "actionID")); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) AddPendingPart(w http.ResponseWriter, r *http.Request) {
	var in AddPendingPartInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	p, err := h.svc.AddPendingPart(r.Context(), chi.URLParam(r, "id"), &in, uid(r))
	if err != nil {
		response.Error(w, 400, InputMessage(err))
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) GetPendingParts(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPendingParts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, p)
}

func (h *Handler) DeletePendingPart(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePendingPart(r.Context(), chi.URLParam(r, "partItemID")); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "entfernt"})
}

func (h *Handler) GetPartsUsage(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.GetPartsUsage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, u)
}

// ── Checklisten ──

func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.repo.ListTemplates(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, t)
}

func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.repo.GetTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "Checkliste nicht gefunden")
		return
	}
	response.JSON(w, 200, t)
}

func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	t, err := h.svc.repo.CreateTemplate(r.Context(), in.Name, in.Description, uid(r))
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 201, t)
}

func (h *Handler) RenameTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.repo.RenameTemplate(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description); err != nil {
		fail(w, err)
		return
	}
	h.GetTemplate(w, r)
}

func (h *Handler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.repo.DeleteTemplate(r.Context(), chi.URLParam(r, "id")); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) AddTemplateItem(w http.ResponseWriter, r *http.Request) {
	var in ChecklistItemInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if _, err := h.svc.repo.AddTemplateItem(r.Context(), chi.URLParam(r, "id"), &in); err != nil {
		fail(w, err)
		return
	}
	h.GetTemplate(w, r)
}

func (h *Handler) ReorderTemplateItems(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.repo.ReorderTemplateItems(r.Context(), chi.URLParam(r, "id"), in.IDs); err != nil {
		fail(w, err)
		return
	}
	h.GetTemplate(w, r)
}

func (h *Handler) UpdateTemplateItem(w http.ResponseWriter, r *http.Request) {
	var in ChecklistItemInput
	if !decode(r, &in) {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.repo.UpdateTemplateItem(r.Context(), chi.URLParam(r, "itemID"), &in); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) DeleteTemplateItem(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.repo.DeleteTemplateItem(r.Context(), chi.URLParam(r, "itemID")); err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}
