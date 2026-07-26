package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/tasks"
)

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
	AssignedID      string
	ResponsibleID   string
	AssigneeName    string
	ResponsibleName string
	Resolution      string
	RootCause       string
	CreatedAgo      string
	CanResolve      bool
}

func taskView(t *tasks.Task) TaskView {
	v := TaskView{
		ID: t.ID, Title: t.Title, Description: t.Description,
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
		StatusClass: statusClass(string(t.Status)),
		Priority:    string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot: priorityDot(string(t.Priority)),
		ProjectName: t.ProjectName, AssigneeName: t.AssigneeName, ResponsibleName: t.ResponsibleName,
		Resolution: t.Resolution, RootCause: t.RootCause,
		CreatedAgo: timeAgo(t.CreatedAt),
		CanResolve: t.Status == "open" || t.Status == "in_progress",
	}
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
	if t.AssignedTo != nil {
		v.AssignedID = *t.AssignedTo
	}
	if t.ResponsibleTo != nil {
		v.ResponsibleID = *t.ResponsibleTo
	}
	return v
}

type TasksPageData struct {
	BaseData
	Tasks          []TaskView
	Filter         string
	Total          int
	Open           int
	Users          []UserOption
	ProjectOptions []ProjectOption
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
		BaseData: baseData(r, "tasks", "Aufgaben", "Offene Aufgaben"),
		Filter:   filter,
		Users:    h.userOptions(ctx),
	}

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
		BaseData: baseData(r, "tasks", t.Title, "Aufgabe"),
		Task:     taskView(t),
		Users:    h.userOptions(ctx),
	}
	if projs, err := h.projects.List(ctx, ""); err == nil {
		for _, p := range projs {
			data.ProjectOptions = append(data.ProjectOptions, ProjectOption{ID: p.ID, Name: p.Name})
		}
	}

	h.render(w, "task_detail", data)
}
