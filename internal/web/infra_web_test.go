package web

import (
	"strings"
	"testing"
)

func TestInfraDetailRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraDetailData{
		Node:          InfraNodeView{ID: "n1", Name: "Presse 3", Type: "plant", TypeLabel: "Anlage", Model: "HP-400", Description: "Hydraulik", InstalledAt: "2021-04-01"},
		Types:         infraTypes,
		ParentOptions: []InfraNodeView{{ID: "h1", Path: "Halle A"}},
		Message:       "Stammdaten gespeichert",
	}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	checkTabs(t, "infra_detail", out, "overview", "master", "events", "fields", "docs", "links", "history")
	for _, want := range []string{
		`action="/infrastructure/n1/edit"`, `value="HP-400"`, ">Hydraulik</textarea>", `value="2021-04-01"`,
		`<option value="plant" selected>`, `<option value="h1" >`, "01.04.2021", "Stammdaten gespeichert",
		`<input type="hidden" name="from" value="detail">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Detailseite ohne %q", want)
		}
	}
	if strings.Contains(out, "hx-post=\"/infrastructure\"") || strings.Contains(out, `hx-post="/infrastructure/n1/edit"`) {
		t.Error("Anlegen/Speichern sollen ohne htmx-Austausch laufen")
	}

	// ohne Bearbeitungsrecht: kein Stammdaten-Reiter, kein Formular
	d.CanEditInfra = false
	out = renderPage(t, tmpl, "infra_detail", d)
	checkTabs(t, "infra_detail (lesen)", out, "overview", "events", "fields", "docs", "links", "history")
	if strings.Contains(out, `data-pane="master"`) || strings.Contains(out, `id="add-child"`) {
		t.Error("ohne Berechtigung keine Bearbeitungsformulare")
	}
}

func TestInfraFlattenAndSort(t *testing.T) {
	tree := []InfraNodeView{
		{ID: "d", Name: "Kompressor", Type: "device"},
		{ID: "h", Name: "Halle A", Type: "building", Children: []InfraNodeView{
			{ID: "l", Name: "Linie 1", Type: "line", Children: []InfraNodeView{{ID: "p", Name: "Presse", Type: "plant"}}},
		}},
	}
	sortInfraNodes(tree)
	if tree[0].ID != "h" {
		t.Fatalf("Gebäude zuerst erwartet, erhalten %q", tree[0].ID)
	}
	all := flattenNodes(tree, "", "")
	if len(all) != 4 || all[2].Path != "Halle A › Linie 1 › Presse" {
		t.Errorf("Pfade falsch: %+v", all)
	}
	// als Übergeordnetes weder das Element selbst noch seine Unteranlagen
	for _, n := range flattenNodes(tree, "", "l") {
		if n.ID == "l" || n.ID == "p" {
			t.Errorf("%q darf nicht als Übergeordnetes angeboten werden", n.ID)
		}
	}
	if _, ok := infraTypeOf("plant"); !ok {
		t.Error("Typ plant unbekannt")
	}
	if _, ok := infraTypeOf("halle"); ok {
		t.Error("ungültiger Typ akzeptiert")
	}
	if got := (InfraNodeView{InstalledAt: "2021-04-01"}).InstalledLabel(); got != "01.04.2021" {
		t.Errorf("InstalledLabel = %q", got)
	}
}

func TestFaultTicketPendingNotice(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := FaultDetailData{Fault: FaultDetailView{ID: "f1", Title: "Band steht", TicketPending: true}}
	if !strings.Contains(renderPage(t, tmpl, "fault_detail", d), "Ticket wird angelegt, sobald die Störung") {
		t.Error("Hinweis „Ticket wartet auf Zuweisung“ fehlt")
	}
	d.Fault.TicketPending = false
	if strings.Contains(renderPage(t, tmpl, "fault_detail", d), "Ticket wird angelegt, sobald") {
		t.Error("Hinweis ohne Vormerkung")
	}
}
