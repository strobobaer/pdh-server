package web

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseInfraHistoryFilterDefaults(t *testing.T) {
	f := parseInfraHistoryFilter(httptest.NewRequest("GET", "/infrastructure/x/history", nil))
	if !f.IncludeChildren {
		t.Error("ohne Formular sollen Unteranlagen einbezogen werden")
	}
	if len(f.Modules) != len(infraHistoryModules) {
		t.Errorf("ohne Formular sollen alle Module aktiv sein, got %v", f.Modules)
	}
}

func TestParseInfraHistoryFilterForm(t *testing.T) {
	r := httptest.NewRequest("GET", "/infrastructure/x/history?filtered=1&module=fault&module=part&module=bogus&q=+Lager+&from=2026-01-01&to=kaputt&state=closed", nil)
	f := parseInfraHistoryFilter(r)
	if f.IncludeChildren {
		t.Error("abgewählte Unteranlagen-Checkbox muss respektiert werden")
	}
	if strings.Join(f.Modules, ",") != "fault,part" {
		t.Errorf("Module: %v", f.Modules)
	}
	if f.Query != "Lager" || f.State != "closed" {
		t.Errorf("Query/State: %q %q", f.Query, f.State)
	}
	if f.From == nil || f.To != nil {
		t.Errorf("Datum: from=%v to=%v", f.From, f.To)
	}
}

func TestLikePatternEscapes(t *testing.T) {
	if got := likePattern(`50%_a\b`); got != `%50\%\_a\\b%` {
		t.Errorf("likePattern: %s", got)
	}
}

func TestInfraHistoryTemplateRenders(t *testing.T) {
	tmpl, err := template.New("base.gohtml").Funcs(TemplateFuncs()).ParseFiles(filepath.Join("..", "..", "web", "templates", "base.gohtml"))
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	if _, err := tmpl.ParseGlob(filepath.Join("..", "..", "web", "templates", "widgets", "*.gohtml")); err != nil {
		t.Fatalf("widgets: %v", err)
	}
	res := &InfraHistoryResult{
		InfraID: "root",
		Filter:  infraHistoryFilter{Modules: []string{"fault", "comment", "part"}},
		Modules: []infraHistoryModuleCount{{infraHistoryModules[0], 1, true}},
		Pinned:  []InfraHistoryEntry{{Module: "comment", ID: "c1", Body: "Achtung <b>", Pinned: true, CanEdit: true}},
		Entries: []InfraHistoryEntry{
			{Module: "fault", ModuleLabel: "Störungen", Icon: "ti-alert-triangle", Title: "Motor heiß", StatusLabel: "Gelöst", DetailURL: "/faults/1", FromChild: true, InfraID: "child", InfraName: "Pumpe 2"},
			{Module: "part", ModuleLabel: "Ersatzteile", Title: "4711 · Lager", QtyLabel: "2 Stück", ParentTitle: "Störung: Motor heiß"},
			{Module: "comment", ID: "c2", Body: "Hinweis", CanEdit: false},
		},
		Total: 3,
		Parts: []InfraPartUsage{{PartID: "p1", PartNumber: "4711", Name: "Lager", Unit: "Stück", QtyLabel: "2", Records: 1, CostLabel: "12,00 €"}},
	}
	detail, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := detail.ParseFiles(filepath.Join("..", "..", "web", "templates", "infra_detail.gohtml")); err != nil {
		t.Fatalf("infra_detail: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "infra-history-results", res); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Motor heiß", "Pumpe 2", "2 Stück", "12,00 €", "/infrastructure/root/comments/c1/pin", "Achtung &lt;b&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("Ausgabe enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/comments/c2/delete") {
		t.Error("fremder Kommentar darf keine Löschen-Aktion haben")
	}
}
