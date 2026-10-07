package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkViewRouting(t *testing.T) {
	cases := map[string]string{
		"/tickets":                  "due",
		"/tickets?view=archive":     "archive",
		"/tickets?view=list":        "list",
		"/tickets?status=open":      "list", // alte Links behalten die Tabelle
		"/faults?unassigned=1":      "list",
		"/tasks?view=due&project=x": "due",
		"/tasks?tag=abc":            "due",
	}
	for u, want := range cases {
		if got := workView(httptest.NewRequest("GET", u, nil)); got != want {
			t.Errorf("%s: %s, will %s", u, got, want)
		}
	}
}

func TestWorkBoardRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := WorkBoardData{Kind: workKinds["fault"], View: "due", Groups: []workGroup{{Key: "overdue", Label: "Überfällig", Icon: "ti-alert-triangle",
		Cards: []workCard{{ID: "f1", Title: "Presse steht", InfraName: "Presse 3", Status: "detected", Due: "01.10.2026", Overdue: true, Priority: "critical", PriorityLabel: "kritisch"}}}},
		OpenCount: 1, OverdueCount: 1}
	out := renderPage(t, tmpl, "work_board", d)
	if !strings.Contains(out, "pdhComplete(") || !strings.Contains(out, "f1") {
		t.Error("Knopf fuer den Abschluss-Assistenten fehlt")
	}
	for _, want := range []string{"Presse steht", "Überfällig", "Beheben", "neu gemeldet", `href="/faults?view=archive"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Anstehend enthält %q nicht", want)
		}
	}
	d = WorkBoardData{Kind: workKinds["task"], View: "archive", Archive: workArchive{Total: 1, Pages: 1, Page: 1,
		Rows: []workArchiveRow{{ID: "t1", Title: "Filter tauschen", ProjectName: "Umbau Halle 2", Status: "resolved", StatusLabel: "Gelöst", Resolution: "getauscht"}}}}
	out = renderPage(t, tmpl, "work_board", d)
	for _, want := range []string{"Filter tauschen", "Umbau Halle 2", "Lösung &amp; Ursache", "getauscht", "Projekte"} {
		if !strings.Contains(out, want) {
			t.Errorf("Archiv enthält %q nicht", want)
		}
	}
}

func TestProjectsRenderProgress(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "projects", ProjectsPageData{Projects: []ProjectView{{ID: "p1", Name: "Umbau", Status: "active", StatusLabel: "Aktiv",
		TaskCount: 4, TasksDone: 1, TasksOpen: 3, TasksOverdue: 2, Progress: 25, NextDue: "03.10.2026", NextOverdue: true}}})
	for _, want := range []string{"Umbau", "1/4 erledigt", "2 überfällig", "width:25%", "Anstehende Aufgaben"} {
		if !strings.Contains(out, want) {
			t.Errorf("Projekte enthält %q nicht", want)
		}
	}
	det := renderPage(t, tmpl, "project_detail", ProjectDetailData{Project: ProjectView{ID: "p1", Name: "Umbau", TaskCount: 1},
		Groups: []projectTaskGroup{{Key: "open", Label: "Offen", Icon: "ti-circle", Tasks: []TaskView{{ID: "t1", Title: "Kabel ziehen", CanResolve: true}}}}})
	for _, want := range []string{"Kabel ziehen", "Erledigen", "Aufgaben im Projekt"} {
		if !strings.Contains(det, want) {
			t.Errorf("Projektdetail enthält %q nicht", want)
		}
	}
}
