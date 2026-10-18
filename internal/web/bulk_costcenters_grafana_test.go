package web

import (
	"strings"
	"testing"

	"pdh/internal/core/grafanadb"
)

func mustContain(t *testing.T, what, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s enthält %q nicht", what, w)
		}
	}
}

// Sammelanlegen/Kopieren: Lager und Infrastruktur (storage_bulk.go, infra_bulk.go)
func TestBulkDialogsRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	tree := []StorageNodeView{{ID: "l1", Name: "Hauptlager", Type: "lagerort", ChildType: "regal", Children: []StorageNodeView{
		{ID: "r1", Name: "Regal A", Type: "regal", ChildType: "fach", Children: []StorageNodeView{{ID: "p1", Name: "Platz 1", Type: "platz"}}},
	}}}
	targets := storageTargets(tree)
	if len(targets) != 2 || targets[1].Path != "Hauptlager › Regal A" || targets[1].Depth != 1 {
		t.Fatalf("Ziele: %+v", targets)
	}
	out := renderPage(t, tmpl, "storage", StoragePageData{Tree: tree, Targets: targets, CanEdit: true, Stats: map[string]int{}, Message: "Sammelanlegen: 12 angelegt"})
	mustContain(t, "Lager", out, `action="/storage/bulk"`, `name="l1_from"`, `name="l2_to"`, `name="l2_join"`, `id="storage-copy-form"`,
		`openStorageBulk('r1')`, `openStorageCopy('r1', this.dataset.name, 'regal')`, "pdhRangeNames", "Sammelanlegen: 12 angelegt", `data-depth="1"`)
	if out := renderPage(t, tmpl, "storage", StoragePageData{Tree: tree, Stats: map[string]int{}}); strings.Contains(out, `action="/storage/bulk"`) {
		t.Error("Sammelanlegen ohne Recht sichtbar")
	}

	d := InfraPageData{Types: infraTypes, AllNodes: []InfraNodeView{{ID: "i1", Name: "Halle", Path: "Halle"}},
		Tree: []InfraNodeView{{ID: "i1", Name: "Halle", Type: "building"}}}
	d.CanEditInfra = true
	out = renderPage(t, tmpl, "infrastructure", d)
	mustContain(t, "Infrastruktur", out, `action="/infrastructure/bulk"`, `id="infra-copy-form"`, `openInfraBulk('i1')`, "openInfraCopy('i1'", `name="c_prefix"`, "pdhRangeBind")
}

// Kostenstellen-Dashboard (costcenters_web.go)
func TestCostCentersPageRender(t *testing.T) {
	rows := []ccRow{
		{ID: "a", Number: "4100", Name: "Instandhaltung", Active: true, OwnMinutes: 120, Minutes: 120, Open: 2},
		{ID: "b", Number: "4110", Name: "Elektro", ParentID: "a", Active: true, Minutes: 90, Open: 1},
		{ID: "c", Number: "4200", Name: "Produktion", Active: true},
	}
	tree, mains := buildCCTree(rows)
	if len(tree) != 2 || len(tree[0].Children) != 1 || tree[0].Minutes != 210 || len(mains) != 2 {
		t.Fatalf("Baum: %+v", tree)
	}
	d := CostCentersPageData{Tab: "dashboard", Year: 2026, Years: []int{2026, 2025}, Tree: tree, Mains: mains, All: rows,
		Activities: []ccActivity{{ID: "x", Name: "Rüsten", Active: true, Minutes: 60, Last: true, CostCenterID: "a", CostCenterLabel: "4100 Instandhaltung"}},
		Users:      []UserOption{{ID: "u1", Name: "Eva"}}}
	costDashboard(&d, 210, 30)
	if len(d.KPIs) != 4 || d.KPIs[0].Value != "2" || d.KPIs[0].Sub != "1 Unterstellen" || d.KPIs[3].Value != "3" || !strings.Contains(d.KPIs[2].Sub, "87 %") {
		t.Errorf("Kennzahlen: %+v", d.KPIs)
	}
	if len(d.HoursByCC) != 2 || d.HoursByCC[0].Pct != 100 || d.HoursByCC[1].Label != "ohne Kostenstelle" {
		t.Errorf("Balken: %+v", d.HoursByCC)
	}
	out := renderPage(t, loadTestTemplates(t), "core_costcenters", d)
	mustContain(t, "Kostenstellen", out, `data-tab="activities"`, "4110", "Unterstelle", "3,5 h", `action="/core/costcenters/activities/x"`,
		`action="/core/costcenters/b/active"`, `name="parent_id"`, "Rüsten", "1,0 h", "Stunden je Kostenstelle")
	if hoursText(0) != "0 h" || hoursText(90) != "1,5 h" || hoursText(60*150) != "150 h" {
		t.Error("hoursText")
	}
}

// Statistik + Core → Grafana (grafana.go)
func TestGrafanaHrefAndPages(t *testing.T) {
	for _, c := range []struct {
		d    grafanaDashboard
		want string
	}{
		{grafanaDashboard{URL: "/d/abc/x", Kiosk: true}, "http://srv:3000/d/abc/x?kiosk"},
		{grafanaDashboard{URL: "d/abc/x?from=now-7d", Kiosk: true, Refresh: "5m"}, "http://srv:3000/d/abc/x?from=now-7d&refresh=5m&kiosk"},
		{grafanaDashboard{URL: "https://g.firma.de/d/z?kiosk"}, "https://g.firma.de/d/z?kiosk"},
		{grafanaDashboard{URL: "https://g.firma.de/d/z"}, "https://g.firma.de/d/z"},
	} {
		if got := grafanaHref("http://srv:3000", c.d); got != c.want {
			t.Errorf("%+v: %s, erwartet %s", c.d, got, c.want)
		}
	}
	for s, ok := range map[string]bool{"30s": true, "5m": true, "1h": true, "x": false, "5": false, "5x": false, "m5": false} {
		if validGrafanaRefresh(s) != ok {
			t.Errorf("refresh %q", s)
		}
	}
	tp := loadTestTemplates(t)
	list := []grafanaDashboard{{ID: "g1", Title: "PDH Übersicht", Icon: "ti-chart-bar", Color: "green", OpenMode: "embed", Href: "http://srv:3000/d/pdh-overview?kiosk", Active: true},
		{ID: "g2", Title: "Energie", Icon: "ti-bolt", Color: "amber", OpenMode: "tab", Href: "https://g/d/e", Active: true, Last: true}}
	out := renderPage(t, tp, "statistics", StatisticsPageData{Dashboards: list, BaseURL: "http://srv:3000", Auto: true, CanManage: true})
	mustContain(t, "Statistik", out, `href="/statistics/g1"`, `href="https://g/d/e" target="_blank"`, "#10b981", "PDH Übersicht", "Port 3000")
	out = renderPage(t, tp, "statistics", StatisticsPageData{Dashboards: list, Current: &list[0]})
	mustContain(t, "Statistik-Ansicht", out, `<iframe id="st-frame"`, `src="http://srv:3000/d/pdh-overview?kiosk"`, `st-chip on`)
	out = renderPage(t, tp, "core_grafana", GrafanaSettingsData{Dashboards: list, EffectiveBase: "http://srv:3000", Auto: true, Health: "ok", HealthOK: true,
		Colors: grafanaColors, Icons: grafanaIcons, Edit: &list[1]})
	mustContain(t, "Core → Grafana", out, `action="/core/settings/grafana/base"`, `action="/core/settings/grafana/g2"`, `/core/settings/grafana/g1/up`,
		`data-icon="ti-bolt"`, `value="tab" selected`, "„Energie“ bearbeiten", "gf-status ok")
}

func TestGrafanaSensitiveColumns(t *testing.T) {
	for col, want := range map[string]bool{"password_hash": true, "client_secret": true, "token": true, "p256dh": true, "auth_key": true,
		"title": false, "duration_min": false, "cost_center_id": false, "name": false} {
		if grafanadb.Sensitive(col) != want {
			t.Errorf("%s: %v", col, !want)
		}
	}
}

// Zeiterfassung: Auswahl „Tätigkeit“
func TestTimeActivitySelectRender(t *testing.T) {
	tp := loadTestTemplates(t)
	d := TimePageData{Entries: []TimeEntryView{{ID: "e1", Description: "Rüsten Linie 2", ActivityID: "a1", ActivityName: "Rüsten"}},
		Activities: []activityGroup{{Label: "Allgemein", Items: []activityOption{{ID: "a1", Label: "Rüsten"}}}}}
	out := renderPage(t, tp, "timetracking", d)
	mustContain(t, "Zeiterfassung", out, `id="start-activity-id"`, `id="manual-activity-id"`, `id="edit-activity-id"`, `<optgroup label="Allgemein">`,
		"teAppendActivity(fd, 'start')", `"e1": "a1"`, "ti-tool")
}
