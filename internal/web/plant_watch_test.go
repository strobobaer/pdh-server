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
