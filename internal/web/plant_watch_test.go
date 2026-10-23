package web

import (
	"strings"
	"testing"
)

func TestWatchStatsState(t *testing.T) {
	for _, c := range []struct {
		s    watchStats
		want string
	}{
		{watchStats{}, "green"},
		{watchStats{Faults: 1}, "amber"},
		{watchStats{MaintOverdue: 1}, "amber"},
		{watchStats{ObligOverdue: 2}, "amber"},
		{watchStats{Faults: 2, Critical: 1}, "red"},
		{watchStats{NOK: 1}, "red"},
	} {
		if got := c.s.State(); got != c.want {
			t.Errorf("%+v: %s statt %s", c.s, got, c.want)
		}
	}
}

func TestPlantWatchPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	board := watchBoard{ID: "b1", Name: "Trockenkammern", Icon: "ti-temperature", CanEdit: true, Shares: 1}
	chamber := []chamberGroup{
		{chamberKind: chamberKinds[0], NOKCount: 1, Slots: []chamberSlot{
			{Position: 1, PartID: "p1", PartLabel: "4711 – Motor 3 kW", LastChange: "01.10.2026"},
			{Position: 2, PartID: "p1", PartLabel: "4711 – Motor 3 kW", NOK: true, Cause: "Lagerschaden"},
		}},
		{chamberKind: chamberKinds[1]}, // ohne Kacheln: nicht anzeigen
	}
	d := PlantWatchData{
		Boards: []watchBoard{board}, Board: &board, CanManage: true,
		Body: watchCards{BoardID: "b1", Total: 2, Red: 1, Cards: []watchCard{
			{ID: "k1", Name: "Kammer 1", Path: "Halle › Kammer 1", Type: chamberType, TypeLabel: "Trockenkammer",
				watchStats: watchStats{NOK: 1}, Chamber: chamber},
			{ID: "a1", Name: "Presse", Path: "Halle › Presse", Type: "plant", TypeLabel: "Anlage",
				Children: []watchChild{{ID: "g1", Name: "Pumpe", watchStats: watchStats{Faults: 1}}}, MoreChildren: 3},
		}},
	}
	out := renderPage(t, tmpl, "plant_watch", d)
	for _, want := range []string{"Kammer 1", "pw-card red", "pw-slot nok", "Ursache: Lagerschaden", "1 NOK", "Motoren",
		"pw-card green", "Pumpe", "pw-dot amber", "+ 3 weitere", `hx-get="/plant-watch/b1/cards"`, "Bearbeiten"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "Klappenmotoren") {
		t.Error("Bauteilgruppe ohne Kacheln wird angezeigt")
	}

	// Formular mit Freigaben
	e := &watchBoard{ID: "b1", Name: "Trockenkammern", Icon: "ti-eye", Items: map[string]bool{"a1": true},
		Types: map[string]bool{chamberType: true}, Users: map[string]bool{}, Groups: map[string]bool{"g1": true}}
	d = PlantWatchData{Boards: []watchBoard{board}, Board: &board, Edit: e, CanManage: true,
		Nodes:    []InfraNodeView{{ID: "a1", Name: "Presse", Path: "Halle › Presse", TypeLabel: "Anlage"}},
		TypeOpts: infraTypes, Groups: []groupView{{ID: "g1", Name: "Schicht A"}}, Users: []UserOption{{ID: "u1", Name: "Max Muster"}}}
	out = renderPage(t, tmpl, "plant_watch", d)
	for _, want := range []string{`action="/plant-watch/b1"`, `value="drying_chamber" checked`, `value="a1" checked`,
		`value="g1" checked`, "Schicht A", "Max Muster", "/plant-watch/b1/delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("Formular enthält %q nicht", want)
		}
	}

	// ohne Übersichten: Einstieg
	out = renderPage(t, tmpl, "plant_watch", PlantWatchData{})
	if !strings.Contains(out, "Noch keine Schnellübersicht") {
		t.Error("leere Seite ohne Hinweis")
	}
}

func TestPlantWatchToggleAndEnergy(t *testing.T) {
	tmpl := loadTestTemplates(t)
	body := watchCards{BoardID: "b1", CanToggle: true, Total: 1, Cards: []watchCard{{ID: "k1", Name: "Kammer 1", Type: chamberType,
		Energy:  []watchEnergy{{Name: "Energie gesamt", Value: "1234 kWh", Age: "vor 2 Min."}, {Name: "Leistung", Value: "12 kW", Infra: "Motor 1", Stale: true}},
		Chamber: []chamberGroup{{chamberKind: chamberKinds[2], Slots: []chamberSlot{{Position: 1}, {Position: 2, NOK: true}}}}}}}
	out := renderPage(t, tmpl, "plant_watch", PlantWatchData{Board: &watchBoard{ID: "b1"}, Boards: []watchBoard{{ID: "b1"}}, Body: body})
	for _, want := range []string{`hx-post="/plant-watch/b1/toggle"`, `"pos":"2"`, `class="pw-slot nok"`, `class="pw-slot"`,
		"Klick schaltet auf NOK", "kein Typ eingetragen", "1234 kWh", "pw-en stale", "(Motor 1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	body.CanToggle = false
	out = renderPage(t, tmpl, "plant_watch", PlantWatchData{Board: &watchBoard{ID: "b1"}, Boards: []watchBoard{{ID: "b1"}}, Body: body})
	if strings.Contains(out, "/plant-watch/b1/toggle") || !strings.Contains(out, `href="/infrastructure/k1?tab=chamber#cp-heating_valve"`) {
		t.Error("ohne Bearbeiten-Recht: Link statt Umschalten erwartet")
	}
}

func TestScanTarget(t *testing.T) {
	const id = "0191a2b3-c4d5-7e6f-8a9b-0c1d2e3f4a5b"
	kind := func(x string) string {
		if x == id {
			return "infra"
		}
		return ""
	}
	for code, want := range map[string]string{
		"https://pdh.example/a/" + id:          "/a/" + id,
		"https://pdh.example/e/" + id + "?x=1": "/a/" + id,
		id:                                     "/a/" + id,
		"0191a2b3-c4d5-7e6f-8a9b-000000000000": "/inventory/lookup?code=0191a2b3-c4d5-7e6f-8a9b-000000000000",
		"4006381333931":                        "/inventory/lookup?code=4006381333931",
		"https://pdh.example/inventory/" + id:  "/inventory/lookup?code=https%3A%2F%2Fpdh.example%2Finventory%2F" + id,
		"  ":                                   "/",
	} {
		if got := scanTarget(code, kind); got != want {
			t.Errorf("%q → %s statt %s", code, got, want)
		}
	}
}

func TestWatchAndScanWidgetsRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(typ string, data any) string {
		d, _ := widgetDef(typ)
		c, _ := tmpl.Clone()
		var buf strings.Builder
		if err := bindLang(c, "de").ExecuteTemplate(&buf, "dw-body", widgetBody{Def: d, Data: data, Lang: "de"}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	out := render("scan", nil)
	for _, want := range []string{"pdhScanLive(", `capture="environment"`, `action="/scan"`, `name="code"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Scannen enthält %q nicht", want)
		}
	}
	w := watchWidget{Board: watchBoard{ID: "b1", Name: "Kammern", Icon: "ti-eye"}, watchCards: watchCards{Total: 1, Red: 1, Cards: []watchCard{
		{ID: "k1", Name: "Kammer 1", Type: chamberType, TypeLabel: "Trockenkammer", watchStats: watchStats{NOK: 1},
			Chamber: []chamberGroup{{chamberKind: chamberKinds[0], Slots: []chamberSlot{{Position: 3, NOK: true}}}}}}}}
	out = render("plant_watch", w)
	for _, want := range []string{`href="/plant-watch?b=b1"`, "Kammer 1", "Motor 3", "1 NOK", "dw-dot red"} {
		if !strings.Contains(out, want) {
			t.Errorf("Widget enthält %q nicht", want)
		}
	}
	if out := render("plant_watch", nil); !strings.Contains(out, "Noch keine Schnellübersicht") {
		t.Error("Widget ohne Übersicht ohne Hinweis")
	}
}
