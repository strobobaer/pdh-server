package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	tabRe  = regexp.MustCompile(`class="rec-tab" data-tab="([a-z]+)"`)
	paneRe = regexp.MustCompile(`class="rec-pane[^"]*" data-pane="([a-z]+)"`)
)

// checkTabs: jeder Reiter hat genau einen Bereich, keine doppelten Bereiche.
func checkTabs(t *testing.T, name, out string, want ...string) {
	t.Helper()
	tabs := map[string]int{}
	for _, m := range tabRe.FindAllStringSubmatch(out, -1) {
		tabs[m[1]]++
	}
	panes := map[string]int{}
	for _, m := range paneRe.FindAllStringSubmatch(out, -1) {
		panes[m[1]]++
	}
	for k, n := range panes {
		if n != 1 {
			t.Errorf("%s: Bereich %q %d-mal", name, k, n)
		}
		if tabs[k] != 1 {
			t.Errorf("%s: Bereich %q ohne (eindeutigen) Reiter", name, k)
		}
	}
	for k := range tabs {
		if panes[k] != 1 {
			t.Errorf("%s: Reiter %q ohne Bereich", name, k)
		}
	}
	for _, w := range want {
		if panes[w] != 1 {
			t.Errorf("%s: Reiter %q fehlt", name, w)
		}
	}
}

func TestDetailPagesTabs(t *testing.T) {
	tmpl := loadTestTemplates(t)
	cases := []struct {
		page string
		data interface{}
		want []string
	}{
		{"ticket_detail", TicketDetailData{}, []string{"overview", "comments", "work", "fields", "docs", "links", "history"}},
		{"fault_detail", FaultDetailData{}, []string{"overview", "copilot", "work", "fields", "docs", "links", "history"}},
		{"task_detail", TaskDetailData{}, []string{"overview", "fields", "docs", "links", "history"}},
		{"project_detail", ProjectDetailData{}, []string{"overview", "fields", "docs", "links", "history"}},
		{"maintenance_detail", MaintenanceTaskDetailData{}, []string{"overview", "fields", "links", "history"}},
		{"storage_detail", StorageDetailData{ID: "n1", Name: "Regal A"}, []string{"overview", "stock", "moves", "fields", "docs", "links", "history"}},
	}
	for _, c := range cases {
		out := renderPage(t, tmpl, c.page, c.data)
		checkTabs(t, c.page, out, c.want...)
	}
	// Infrastruktur nutzt einen lokalen Datentyp - Aufbau per Quelltext pruefen
	c, _ := tmpl.Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "infra_detail.gohtml")); err != nil {
		t.Fatalf("infra_detail: %v", err)
	}
}

func TestListTabsWidget(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, page := range []struct {
		name string
		data interface{}
	}{
		{"tickets", TicketsPageData{Tabs: []ListTab{{Label: "Alle", URL: "/tickets", Active: true, Count: 3}, {Key: "open", Label: "Offen", URL: "/tickets?status=open", Count: 2}}}},
		{"faults", FaultsPageData{Tabs: []ListTab{{Label: "Alle", URL: "/faults", Active: true}}}},
		{"tasks", TasksPageData{Tabs: []ListTab{{Label: "Alle", URL: "/tasks", Active: true}}}},
		{"projects", ProjectsPageData{Tabs: []ListTab{{Label: "Alle", URL: "/projects", Active: true}}}},
	} {
		out := renderPage(t, tmpl, page.name, page.data)
		if !strings.Contains(out, `class="list-tabs"`) || !strings.Contains(out, `class="active"`) {
			t.Errorf("%s: Listen-Reiter fehlen", page.name)
		}
	}
}
