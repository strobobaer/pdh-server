package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/timetracking"
	"pdh/pkg/appsettings"
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
		CanResolve: t.Status == "open" || t.Status == "in_progress",
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

type TasksPageData struct {
	BaseData
	Tabs           []ListTab
	Tasks          []TaskView
	Filter         string
	Total          int
	Open           int
	Users          []UserOption
	ProjectOptions []ProjectOption
	DefaultDueDays int
}

type ProjectOption struct {
	ID   string
	Name string
}

func (h *Handler) TasksPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	unassigned := r.URL.Query().Get("unassigned") == "true"

	data := TasksPageData{
		BaseData:       h.baseData(r, "tasks", "Aufgaben", "Offene Aufgaben"),
		Filter:         filter,
		Users:          h.userOptions(ctx),
		DefaultDueDays: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
	}
	data.Tabs = h.statusTabs(ctx, "tasks", "/tasks", filter, []statusTabDef{
		{"open", "Offen", "ti-circle"}, {"in_progress", "In Arbeit", "ti-tool"}, {"resolved", "Gelöst", "ti-check"}, {"closed", "Geschlossen", "ti-lock"},
	}, false, listTabExtra{Key: "unassigned", Label: "Ohne Projekt", Icon: "ti-folder-off", Query: "unassigned=true",
		Cond: "project_id IS NULL", Active: unassigned})

	list, err := h.tasks.List(ctx, tasks.Status(filter), "", unassigned)
	if err == nil {
		data.Total = len(list)
		for _, t := range list {
			if t.Status == "open" || t.Status == "in_progress" {
				data.Open++
			}
			data.Tasks = append(data.Tasks, taskView(t))
		}
	}

	if projs, err := h.projects.List(ctx, ""); err == nil {
		for _, p := range projs {
			data.ProjectOptions = append(data.ProjectOptions, ProjectOption{ID: p.ID, Name: p.Name})
		}
	}

	h.render(w, "tasks", data)
}

type TaskDetailData struct {
	BaseData
	Task           TaskView
	Users          []UserOption
	ProjectOptions []ProjectOption
	TimeEntries    []*timetracking.TimeEntry
	RunningTime    *timetracking.TimeEntry
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
	if projs, err := h.projects.List(ctx, ""); err == nil {
		for _, p := range projs {
			data.ProjectOptions = append(data.ProjectOptions, ProjectOption{ID: p.ID, Name: p.Name})
		}
	}

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
