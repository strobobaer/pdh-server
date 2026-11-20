package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/timetracking"
)

type AssigneeView struct {
	ID   string
	Name string
}

type TaskView struct {
	ID              string
	Title           string
	Description     string
	Status          string
	StatusLabel     string
	StatusClass     string
	Priority        string
	PriorityClass   string
	PriorityDot     string
	DueDate         string
	DueDateISO      string
	StartDate       string
	StartDateISO    string
	ProjectID       string
	ProjectName     string
	Assignees       []AssigneeView
	AssigneeNames   string
	AssignedIDSet   map[string]bool
	ResponsibleID   string
	ResponsibleName string
	InfraID         string
	InfraName       string
	Resolution      string
	RootCause       string
	CreatedAgo      string
	CanResolve      bool
	Color           string
}

func taskView(t *tasks.Task) TaskView {
	v := TaskView{
		ID: t.ID, Title: t.Title, Description: t.Description,
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
		StatusClass: statusClass(string(t.Status)),
		Priority:    string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot: priorityDot(string(t.Priority)),
		ProjectName: t.ProjectName, ResponsibleName: t.ResponsibleName, InfraName: t.InfrastructureName,
		Resolution: t.Resolution, RootCause: t.RootCause,
		CreatedAgo: timeAgo(t.CreatedAt),
		CanResolve: t.Status == "open" || t.Status == "in_progress" || t.Status == "pending",
		Color:      t.Color,
	}
	names := make([]string, 0, len(t.Assignees))
	v.AssignedIDSet = make(map[string]bool, len(t.Assignees))
	for _, a := range t.Assignees {
		v.Assignees = append(v.Assignees, AssigneeView{ID: a.ID, Name: a.Name})
		names = append(names, a.Name)
		v.AssignedIDSet[a.ID] = true
	}
	v.AssigneeNames = strings.Join(names, ", ")
	if t.DueDate != nil {
		v.DueDate = t.DueDate.Format("02.01.2006")
		v.DueDateISO = t.DueDate.Format("2006-01-02")
	}
	if t.StartDate != nil {
		v.StartDate = t.StartDate.Format("02.01.2006")
		v.StartDateISO = t.StartDate.Format("2006-01-02")
	}
	if t.ProjectID != nil {
		v.ProjectID = *t.ProjectID
	}
	if t.InfrastructureID != nil {
		v.InfraID = *t.InfrastructureID
	}
	if t.ResponsibleTo != nil {
		v.ResponsibleID = *t.ResponsibleTo
	}
	return v
}

type ProjectOption struct {
	ID   string
	Name string
}

func (h *Handler) TasksPage(w http.ResponseWriter, r *http.Request) {
	h.WorkBoardPage(w, r, "task") // Arbeitsbereich wie der KVP (work_board.go)
}

type TaskDetailData struct {
	BaseData
	Task           TaskView
	Users          []UserOption
	ProjectOptions []ProjectOption
	TimeEntries    []*timetracking.TimeEntry
	RunningTime    *timetracking.TimeEntry
}

// projectOptions: Projekte zur Auswahl im Aufgaben-Formular – nur die, die die
// Person sehen darf (Ansichtsrecht oder beteiligt). Das aktuell gewählte
// Projekt der Aufgabe (keep) bleibt in der Auswahl, damit Speichern es nicht
// stillschweigend löst; sein Name steht ohnehin an der Aufgabe.
func (h *Handler) projectOptions(r *http.Request, keep string) []ProjectOption {
	projs, err := h.projects.List(r.Context(), "")
	if err != nil {
		return nil
	}
	canView := h.viewFilter(r, "project")
	var out []ProjectOption
	for _, p := range projs {
		if p.ID == keep || canView(p.ID) {
			out = append(out, ProjectOption{ID: p.ID, Name: p.Name})
		}
	}
	return out
}

func (h *Handler) TaskDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	t, err := h.tasks.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/tasks", http.StatusFound)
		return
	}

	data := TaskDetailData{
		BaseData: h.baseData(r, "tasks", t.Title, "Aufgabe"),
		Task:     taskView(t),
		Users:    h.userOptions(ctx),
	}
	data.ProjectOptions = h.projectOptions(r, data.Task.ProjectID)

	if entries, err := h.time.ListByRef(ctx, timetracking.RefTask, id); err == nil {
		data.TimeEntries = entries
	}
	if running, err := h.time.GetRunning(ctx, getUser(r).ID); err == nil && running != nil {
		data.RunningTime = running
	}

	h.render(w, "task_detail", data)
}

func (h *Handler) TaskStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefTask,
		RefID:       id,
		Description: "Bearbeitung",
	}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;margin-top:8px">Fehler: %s</div>`, err.Error())
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;margin-top:8px"><i class="ti ti-check"></i> Zeit gestartet (ID: %s...)</div>`, entry.ID[:8])
}
