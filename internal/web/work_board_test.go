package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestWorkRoute(t *testing.T) {
	cases := map[string][2]string{
		"":                          {"dashboard", ""},
		"tab=board":                 {"board", ""},
		"tab=rules":                 {"rules", ""},
		"tab=items":                 {"items", "open"},
		"view=due":                  {"items", "open"}, // alte Adressen
		"view=list":                 {"items", "open"},
		"view=archive":              {"items", "archive"},
		"view=done":                 {"items", "archive"},
		"status=pending":            {"items", "s:pending"},
		"unassigned=1":              {"items", "free"},
		"unassigned=true":           {"items", "noproject"},
		"mine=1":                    {"items", "mine"},
		"view=due&project=x":        {"items", "open"},
		"tag=abc":                   {"items", "open"},
		"tab=items&chip=overdue":    {"items", "overdue"},
		"tab=dashboard&year=2025":   {"dashboard", ""},
		"tab=board&status=detected": {"board", ""},
	}
	for raw, want := range cases {
		q, _ := url.ParseQuery(raw)
		tab, chip := workRoute(q)
		if tab != want[0] || chip != want[1] {
			t.Errorf("%q: %s/%s, will %s/%s", raw, tab, chip, want[0], want[1])
		}
	}
}

func TestWorkChips(t *testing.T) {
	open := []workCard{
		{ID: "a", Status: "open", Mine: true, Overdue: true},
		{ID: "b", Status: "pending", Unassigned: true, ProjectID: "p1"},
		{ID: "c", Status: "open", Upcoming: true}, // Wartung vor dem Vorlauf
	}
	a := workArea{Kind: workKinds["maintenance"], Tab: "items", Chip: "free"}
	a.countOpen(open)
	a.buildChips(open)
	if a.OpenCount != 2 || a.OverdueCount != 1 || a.UnassignedCount != 1 {
		t.Errorf("Zaehler: %d offen, %d ueberfaellig, %d frei", a.OpenCount, a.OverdueCount, a.UnassignedCount)
	}
	got := map[string]int{}
	for _, c := range a.Chips {
		got[c.Key] = c.Count
		if c.Active != (c.Key == "free") {
			t.Errorf("Chip %s aktiv=%v", c.Key, c.Active)
		}
	}
	want := map[string]int{"open": 2, "mine": 1, "free": 1, "s:open": 1, "s:pending": 1, "overdue": 1, "planned": 1, "archive": -1}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("Chip %s: %d, will %d", k, got[k], n)
		}
	}
	if items := a.filterItems(open); len(items) != 1 || items[0].ID != "b" {
		t.Errorf("Ohne Zuweisung: %+v", items)
	}
	a.Chip, a.Q = "open", "zzz"
	if items := a.filterItems(open); len(items) != 0 {
		t.Errorf("Suche greift nicht: %+v", items)
	}
	a = workArea{Kind: workKinds["task"]}
	a.buildBoard(open)
	if len(a.Board) != 3 || len(a.Board[0].Cards) != 1 || len(a.Board[2].Cards) != 1 {
		t.Errorf("Board: %+v", a.Board)
	}
}

func TestWorkBoardRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	card := workCard{ID: "f1", Title: "Presse steht", InfraName: "Presse 3", Status: "detected", StatusLabel: "Erkannt", Due: "01.10.2026",
		Overdue: true, Priority: "critical", PriorityLabel: "Kritisch"}
	k := workKinds["fault"]

	dash := renderPage(t, tmpl, "work_board", WorkBoardData{Area: workArea{Kind: k, Tab: "dashboard", OpenCount: 1, OverdueCount: 1,
		Dash: workDash{Year: 2026, KPIs: []workKPI{{"Überfällig", "1", "Termin überschritten", "red", "/faults?tab=items&chip=overdue"}},
			Funnel: []workBar{{"Erkannt", "ti-alert-triangle", "b-amber", "/faults?tab=items&chip=s%3Adetected", 1, 100}},
			Months: []workMonth{{"Okt", 3, 2, 100, 66}}, Attention: []workCard{card}}}})
	for _, want := range []string{"Dashboard", "Board", "Regeln &amp; Einstellungen", "Ablauf – wo stehen die Störungen?", "Letzte 12 Monate",
		"Braucht Aufmerksamkeit", "Presse steht", "seit 01.10.2026", `href="/faults/f1"`} {
		if !strings.Contains(dash, want) {
			t.Errorf("Dashboard enthält %q nicht", want)
		}
	}

	items := renderPage(t, tmpl, "work_board", WorkBoardData{Area: workArea{Kind: k, Tab: "items", Chip: "open", Items: []workCard{card},
		Chips: []workChip{{Key: "open", Label: "Alle offenen", Icon: "ti-list", URL: "/faults?tab=items&chip=open", Count: 1, Active: true}}}})
	checkScripts(t, "work_board", items)
	for _, want := range []string{"Alle offenen", "Presse steht", "pdhComplete(", "Beheben", "Auswahl erledigen", `data-complete="fault:f1"`} {
		if !strings.Contains(items, want) {
			t.Errorf("Vorgaenge enthalten %q nicht", want)
		}
	}

	arch := renderPage(t, tmpl, "work_board", WorkBoardData{Area: workArea{Kind: workKinds["task"], Tab: "items", Chip: "archive",
		Extra: []workLink{{"projects", "/projects", "ti-timeline", "Projekte"}},
		Archive: workArchive{Total: 1, Pages: 1, Page: 1, Rows: []workArchiveRow{{ID: "t1", Title: "Filter tauschen", ProjectName: "Umbau Halle 2",
			Status: "resolved", StatusLabel: "Gelöst", Resolution: "getauscht"}}}}})
	for _, want := range []string{"Filter tauschen", "Umbau Halle 2", "Lösung &amp; Ursache", "getauscht", "Projekte"} {
		if !strings.Contains(arch, want) {
			t.Errorf("Archiv enthält %q nicht", want)
		}
	}

	board := renderPage(t, tmpl, "work_board", WorkBoardData{Area: workArea{Kind: k, Tab: "board",
		Board: []workCol{{workStatus: k.Flow[0], Cards: []workCard{card}}}, RecentDone: []workCard{{ID: "f2", Title: "Band läuft wieder"}}}})
	for _, want := range []string{"Erkannt", "frisch gemeldet", "Presse steht", "Band läuft wieder", "letzte 7 Tage"} {
		if !strings.Contains(board, want) {
			t.Errorf("Board enthält %q nicht", want)
		}
	}

	rules := renderPage(t, tmpl, "work_board", WorkBoardData{Area: workArea{Kind: k, Tab: "rules", DueDays: 3, CanSettings: true, Brokers: "Anna Berg"}})
	for _, want := range []string{"<b>Sicherheit zuerst.</b>", `action="/work/fault/settings"`, `value="3"`, "Anna Berg"} {
		if !strings.Contains(rules, want) {
			t.Errorf("Regeln enthalten %q nicht", want)
		}
	}
	for _, kind := range workKinds {
		if len(kind.Flow) == 0 || len(kind.Rules) == 0 || kind.DueKey == "" || kind.BrokerColumn == "" {
			t.Errorf("%s: Ablauf, Regeln, Frist oder Broker fehlen", kind.Key)
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

func TestMaintenanceWorkArea(t *testing.T) {
	tmpl := loadTestTemplates(t)
	k := workKinds["maintenance"]
	extra := []workLink{{"year", "/maintenance?view=year", "ti-calendar-month", "Jahresplan"}, {"plans", "/maintenance?view=plans", "ti-repeat", "Pläne"}}
	out := renderPage(t, tmpl, "maintenance", MaintenancePageData{View: "area", Area: workArea{Kind: k, Tab: "board", Extra: extra, OpenCount: 1,
		Board: []workCol{{workStatus: k.Flow[0], Cards: []workCard{{ID: "m1", Title: "Kompressor prüfen"}}}}}})
	for _, want := range []string{"Aufträge", "Jahresplan", "Pläne", "Kompressor prüfen", "Durchführen", `href="/maintenance/tasks/m1"`, "pdhComplete(&#39;maintenance&#39;"} {
		if !strings.Contains(out, want) && !strings.Contains(out, strings.ReplaceAll(want, "&#39;", "'")) {
			t.Errorf("Wartung enthält %q nicht", want)
		}
	}
	out = renderPage(t, tmpl, "maintenance", MaintenancePageData{View: "archive", Area: workArea{Kind: k, Tab: "items", Chip: "archive", Extra: extra,
		Chips: []workChip{{Key: "archive", Label: "Archiv", Icon: "ti-archive", URL: "/maintenance?tab=items&chip=archive", Count: -1, Active: true}}}})
	for _, want := range []string{`class="kvp-chip on"`, "nur mit Abweichungen", "Noch nichts abgeschlossen."} {
		if !strings.Contains(out, want) {
			t.Errorf("Wartungs-Archiv enthält %q nicht", want)
		}
	}
}
