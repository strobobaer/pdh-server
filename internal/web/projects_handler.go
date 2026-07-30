package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/projects"
)

type ProjectView struct {
	ID               string
	Name             string
	Description      string
	StartDate        string
	StartDateISO     string
	EndDate          string
	EndDateISO       string
	Status           string
	StatusLabel      string
	StatusClass      string
	ResponsibleID    string
	ResponsibleName  string
	InfraID          string
	InfraName        string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	TaskCount        int
}

func projectStatusLabel(s string) string {
	labels := map[string]string{
		"planning": "In Planung", "active": "Aktiv",
		"paused": "Pausiert", "completed": "Abgeschlossen",
	}
	if l, ok := labels[s]; ok {
		return l
	}
	return s
}

func projectStatusClass(s string) string {
	switch s {
	case "active":
		return "b-blue"
	case "completed":
		return "b-green"
	case "paused":
		return "b-amber"
	default:
		return "b-gray"
	}
}

func projectView(p *projects.Project) ProjectView {
	v := ProjectView{
		ID: p.ID, Name: p.Name, Description: p.Description,
		Status: string(p.Status), StatusLabel: projectStatusLabel(string(p.Status)),
		StatusClass:      projectStatusClass(string(p.Status)),
		ResponsibleName:  p.ResponsibleName,
		InfraName:        p.InfraName,
		CostCenterNumber: p.CostCenterNumber, CostCenterName: p.CostCenterName,
		TaskCount: p.TaskCount,
	}
	if p.StartDate != nil {
		v.StartDate = p.StartDate.Format("02.01.2006")
		v.StartDateISO = p.StartDate.Format("2006-01-02")
	}
	if p.EndDate != nil {
		v.EndDate = p.EndDate.Format("02.01.2006")
		v.EndDateISO = p.EndDate.Format("2006-01-02")
	}
	if p.ResponsibleTo != nil {
		v.ResponsibleID = *p.ResponsibleTo
	}
	if p.InfrastructureID != nil {
		v.InfraID = *p.InfrastructureID
	}
	if p.CostCenterID != nil {
		v.CostCenterID = *p.CostCenterID
	}
	return v
}

type ProjectsPageData struct {
	BaseData
	Projects []ProjectView
	Total    int
	Users    []UserOption
}

func (h *Handler) ProjectsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := ProjectsPageData{
		BaseData: h.baseData(r, "projects", "Projektplanung", "Projekte"),
		Users:    h.userOptions(ctx),
	}
	list, err := h.projects.List(ctx, "")
	if err == nil {
		data.Total = len(list)
		for _, p := range list {
			data.Projects = append(data.Projects, projectView(p))
		}
	}
	h.render(w, "projects", data)
}

type ProjectDetailData struct {
	BaseData
	Project ProjectView
	Tasks   []TaskView
	Users   []UserOption
}

func (h *Handler) ProjectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	p, err := h.projects.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/projects", http.StatusFound)
		return
	}

	data := ProjectDetailData{
		BaseData: h.baseData(r, "projects", p.Name, "Projekt"),
		Project:  projectView(p),
		Users:    h.userOptions(ctx),
	}

	if tl, err := h.tasks.List(ctx, "", id, false); err == nil {
		for _, t := range tl {
			data.Tasks = append(data.Tasks, taskView(t))
		}
	}

	h.render(w, "project_detail", data)
}
