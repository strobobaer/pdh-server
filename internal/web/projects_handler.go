package web

import (
	"context"
	"net/http"
	"time"

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
	AssignedID       string
	AssigneeName     string
	InfraID          string
	InfraName        string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	TaskCount        int

	// Uebergeordnetes Aufgabenmanagement: Stand der Aufgaben im Projekt
	TasksDone, TasksOpen, TasksOverdue int
	Progress                           int
	NextDue                            string
	NextOverdue                        bool
}

// projectTaskStats: je Projekt erledigte/offene/ueberfaellige Aufgaben und naechster Termin.
func (h *Handler) projectTaskStats(ctx context.Context, views []ProjectView) {
	if h.db == nil || len(views) == 0 {
		return
	}
	idx := map[string]int{}
	ids := make([]string, 0, len(views))
	for i, v := range views {
		idx[v.ID] = i
		ids = append(ids, v.ID)
	}
	rows, err := h.db.Query(ctx, `
		SELECT project_id::text,
		       COUNT(*) FILTER (WHERE status IN ('resolved','closed') OR archived_at IS NOT NULL)::int,
		       COUNT(*) FILTER (WHERE status IN ('open','in_progress','pending') AND archived_at IS NULL)::int,
		       COUNT(*) FILTER (WHERE status IN ('open','in_progress','pending') AND archived_at IS NULL AND due_date < CURRENT_DATE)::int,
		       MIN(due_date) FILTER (WHERE status IN ('open','in_progress','pending') AND archived_at IS NULL)
		  FROM tasks WHERE project_id::text = ANY($1) GROUP BY project_id`, ids)
	if err != nil {
		return
	}
	defer rows.Close()
	today := time.Now().Truncate(24 * time.Hour)
	for rows.Next() {
		var id string
		var done, open, overdue int
		var next *time.Time
		if rows.Scan(&id, &done, &open, &overdue, &next) != nil {
			continue
		}
		v := &views[idx[id]]
		v.TasksDone, v.TasksOpen, v.TasksOverdue = done, open, overdue
		if done+open > 0 {
			v.Progress = done * 100 / (done + open)
		}
		if next != nil {
			v.NextDue, v.NextOverdue = next.Format("02.01.2006"), next.Before(today)
		}
	}
}

// projectTaskGroup: Aufgaben eines Projekts, gruppiert wie Wartungsauftraege.
type projectTaskGroup struct {
	Key, Label, Icon string
	Tasks            []TaskView
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
		AssigneeName:     p.AssigneeName,
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
	if p.AssignedTo != nil {
		v.AssignedID = *p.AssignedTo
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
	Tabs     []ListTab
	Projects []ProjectView
	Total    int
	Users    []UserOption
}

func (h *Handler) ProjectsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	data := ProjectsPageData{
		BaseData: h.baseData(r, "projects", "Projektplanung", "Projekte"),
		Users:    h.userOptions(ctx),
	}
	data.Tabs = h.statusTabs(ctx, "projects", "/projects", filter, []statusTabDef{
		{"planning", "Planung", "ti-pencil"}, {"active", "Aktiv", "ti-player-play"},
		{"paused", "Pausiert", "ti-player-pause"}, {"completed", "Abgeschlossen", "ti-check"},
	}, false)
	list, err := h.projects.List(ctx, projects.Status(filter))
	if err == nil {
		data.Total = len(list)
		tagIDs := h.categoryFilterIDs(r, "project")
		scopeIDs := h.scopeAllowedIDs(r, "project")
		for _, p := range list {
			if tagIDs != nil && !tagIDs[p.ID] || scopeIDs != nil && !scopeIDs[p.ID] {
				continue
			}
			data.Projects = append(data.Projects, projectView(p))
		}
		h.projectTaskStats(ctx, data.Projects)
	}
	h.render(w, "projects", data)
}

type ProjectDetailData struct {
	BaseData
	Project ProjectView
	Tasks   []TaskView
	Groups  []projectTaskGroup
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
	views := []ProjectView{data.Project}
	h.projectTaskStats(ctx, views)
	data.Project = views[0]
	groups := []projectTaskGroup{
		{Key: "overdue", Label: "Überfällig", Icon: "ti-alert-triangle"},
		{Key: "in_progress", Label: "In Arbeit", Icon: "ti-tool"},
		{Key: "open", Label: "Offen", Icon: "ti-circle"},
		{Key: "pending", Label: "Wartet", Icon: "ti-hourglass"},
		{Key: "done", Label: "Erledigt", Icon: "ti-circle-check"},
	}
	gi := map[string]int{"overdue": 0, "in_progress": 1, "open": 2, "pending": 3, "done": 4}
	today := time.Now().Format("2006-01-02")
	for _, t := range data.Tasks {
		key := t.Status
		if !t.CanResolve {
			key = "done"
		} else if t.DueDateISO != "" && t.DueDateISO < today {
			key = "overdue"
		}
		if i, ok := gi[key]; ok {
			groups[i].Tasks = append(groups[i].Tasks, t)
		}
	}
	for _, g := range groups {
		if len(g.Tasks) > 0 {
			data.Groups = append(data.Groups, g)
		}
	}

	h.render(w, "project_detail", data)
}
