package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestWidgetCatalogConsistent(t *testing.T) {
	seen := map[string]bool{}
	cats := map[string]bool{}
	for _, c := range widgetCategories {
		cats[c] = true
	}
	for _, d := range widgetDefs {
		if seen[d.Type] {
			t.Errorf("doppelt: %s", d.Type)
		}
		seen[d.Type] = true
		if !cats[d.Category] || d.Size < 1 || d.Size > 4 || d.load == nil || !strings.HasPrefix(d.Icon, "ti-") || d.Desc == "" {
			t.Errorf("unvollständig: %+v", d)
		}
	}
	for _, w := range defaultWidgets() {
		if _, ok := widgetDef(w.Type); !ok {
			t.Errorf("Standard-Widget unbekannt: %s", w.Type)
		}
	}
}

func TestSanitizeWidgets(t *testing.T) {
	in := []WidgetInstance{
		{ID: "a", Type: "note", Size: 9, Config: map[string]string{"note": "Hallo", "evil": "x", "title": "  Mein Zettel  "}},
		{ID: "a", Type: "quick_links", Size: 2, Config: map[string]string{"links": "Intern | /tickets\nBöse | javascript:alert(1)\nProto | //evil.example\nExtern | https://example.com\n/inventory"}},
		{ID: "b", Type: "gibtsnicht"},
		{ID: "c", Type: "stat_tickets", Size: 1, Config: map[string]string{"note": "x"}},
	}
	out, err := sanitizeWidgets(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("erwartet 3, bekommen %d: %+v", len(out), out)
	}
	if out[0].Size != 1 || out[0].Config["note"] != "Hallo" || out[0].Config["evil"] != "" || out[0].Config["title"] != "Mein Zettel" {
		t.Errorf("Notiz: %+v", out[0])
	}
	if out[1].ID == "a" {
		t.Error("doppelte ID nicht ersetzt")
	}
	links := parseLinks(out[1].Config["links"])
	if len(links) != 3 || links[0].URL != "/tickets" || links[1].URL != "https://example.com" || links[2].Name != "/inventory" {
		t.Errorf("Links: %+v", links)
	}
	if out[2].Config != nil {
		t.Errorf("Kennzahl darf keine Einstellungen haben: %+v", out[2].Config)
	}
	many := make([]WidgetInstance, maxWidgets+1)
	if _, err := sanitizeWidgets(many); err == nil {
		t.Error("zu viele Widgets akzeptiert")
	}
}

func renderWidgetBody(t *testing.T, b widgetBody) string {
	t.Helper()
	tmpl := loadTestTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "dw-body", b); err != nil {
		t.Fatalf("%s: %v", b.Def.Type, err)
	}
	return buf.String()
}

func TestWidgetBodies(t *testing.T) {
	def := func(tp string) WidgetDef { d, _ := widgetDef(tp); return d }
	cases := []struct {
		b    widgetBody
		want []string
	}{
		{widgetBody{Def: def("stat_faults"), Data: statData{Value: "7", Sub: "2 neu gemeldet", Color: "red", URL: "/faults", Alert: true}}, []string{`class="dw-stat c-red" href="/faults"`, ">7<", "s alert"}},
		{widgetBody{Def: def("list_mine"), Data: []listItem{{Icon: "ti-ticket", Title: "Presse prüfen", Sub: "Ticket · Offen", URL: "/tickets/1", Badge: "überfällig", BadgeClass: "b-red"}}}, []string{`href="/tickets/1"`, "Presse prüfen", "badge b-red"}},
		{widgetBody{Def: def("list_faults"), Data: []listItem(nil)}, []string{"Nichts offen"}},
		{widgetBody{Def: def("quick_actions"), Data: []quickAction{{"Zeit erfassen", "ti-clock-play", "/time", "green"}}}, []string{`class="q-green" href="/time"`}},
		{widgetBody{Def: def("quick_links"), Data: []widgetLink{{"Intern", "/tickets"}, {"Extern", "https://example.com"}}}, []string{`href="/tickets"><i class="ti ti-arrow-right">`, `href="https://example.com" target="_blank" rel="noopener"`}},
		{widgetBody{Def: def("note"), Data: "<b>Hallo</b>"}, []string{"&lt;b&gt;Hallo&lt;/b&gt;"}},
		{widgetBody{Def: def("chart_trend"), Data: trendData{ShowTickets: true, Days: []trendDay{{Label: "Mo 05.", Tickets: 3, HT: 100}}, SumTickets: 3}}, []string{"height:100%", "Tickets (3)"}},
		{widgetBody{Def: def("my_shifts"), Data: []shiftDayView{{Day: "Mo", Date: "05.10.", Short: "F", Time: "06:00–14:00", Color: "#3b82f6", Today: true}}}, []string{"d today", "color:#3b82f6", "06:00–14:00"}},
		{widgetBody{Err: "Keine Berechtigung."}, []string{"Keine Berechtigung."}},
	}
	for _, c := range cases {
		out := renderWidgetBody(t, c.b)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt in\n%s", c.b.Def.Type, w, out)
			}
		}
	}
}

func TestDashboardWithWidgets(t *testing.T) {
	tmpl := loadTestTemplates(t)
	var views []DashboardWidgetView
	for _, w := range []WidgetInstance{{ID: "d1", Type: "stat_tickets", Size: 1}, {ID: "n1", Type: "note", Size: 2, Config: map[string]string{"note": "Öl nachfüllen", "title": "Schicht"}}} {
		d, _ := widgetDef(w.Type)
		title := d.Name
		if w.Config["title"] != "" {
			title = w.Config["title"]
		}
		views = append(views, DashboardWidgetView{WidgetInstance: w, Def: d, Title: title})
	}
	cat := []WidgetCatalogGroup{{Name: catStats, Items: []WidgetDef{widgetDefs[2]}}}
	out := renderPage(t, tmpl, "dashboard", DashboardData{Widgets: views, WidgetCatalog: cat})
	for _, want := range []string{`id="dw-grid"`, `data-id="d1" data-type="stat_tickets" data-size="1" data-refresh="60"`, `data-type="note" data-size="2"`, ">Schicht<",
		"Öl nachfüllen", "Dashboard anpassen", `data-dw-catalog`, `data-type="stat_tickets" data-name="Offene Tickets"`, "sortablejs", `id="dw-tpl"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Dashboard enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `<div class="stats">`) {
		t.Error("alte feste Kennzahlenzeile noch vorhanden")
	}
	if out := renderPage(t, tmpl, "dashboard", DashboardData{}); !strings.Contains(out, `id="dw-empty"`) {
		t.Error("leeres Dashboard ohne Hinweis")
	}
}
