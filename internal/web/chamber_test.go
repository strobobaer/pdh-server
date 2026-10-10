package web

import (
	"strings"
	"testing"
)

func TestChamberTypeAndKinds(t *testing.T) {
	ty, ok := infraTypeOf("drying_chamber")
	if !ok || ty.Label != "Trockenkammer" || ty.Icon != "ti-temperature" {
		t.Errorf("Typ Trockenkammer: %+v %v", ty, ok)
	}
	if infraTypeLabels["drying_chamber"] != "Trockenkammer" {
		t.Error("Bezeichnung fehlt in infraTypeLabels")
	}
	want := []string{"motor", "flap_motor", "heating_valve", "heating_pump", "door_roller", "door_seal"}
	if len(chamberKinds) != len(want) {
		t.Fatalf("%d Gruppen", len(chamberKinds))
	}
	for i, k := range want {
		if chamberKinds[i].Key != k {
			t.Errorf("Gruppe %d: %s, erwartet %s", i, chamberKinds[i].Key, k)
		}
	}
	if _, ok := chamberKindOf("quatsch"); ok {
		t.Error("unbekannte Gruppe angenommen")
	}
}

func TestResolveChamberPart(t *testing.T) {
	parts := []chamberPartOption{{ID: "p1", Label: "M-100 – Lüftermotor 2,2 kW"}, {ID: "p2", Label: "Dichtung ohne Nummer"}}
	numbers := map[string]string{"m-100": "p1"}
	for in, want := range map[string]string{
		"":                           "",
		"M-100 – Lüftermotor 2,2 kW": "p1",
		"m-100":                      "p1",
		"M-100 – alter Name":         "p1",
		"Dichtung ohne Nummer":       "p2",
	} {
		if got, err := resolveChamberPart(in, parts, numbers); err != nil || got != want {
			t.Errorf("%q → %q (%v), erwartet %q", in, got, err, want)
		}
	}
	if _, err := resolveChamberPart("gibt es nicht", parts, numbers); err == nil {
		t.Error("unbekanntes Ersatzteil angenommen")
	}
}

func TestChamberChangeNote(t *testing.T) {
	k, _ := chamberKindOf("motor")
	old := chamberSlot{Position: 2}
	if got := chamberChangeNote(k, old, true, "2026-10-01", "Lagerschaden", true); got != "Motor 2: NOK, Wechsel 01.10.2026, Typ geändert, Ursache: Lagerschaden" {
		t.Errorf("Notiz: %s", got)
	}
	old.NOK = true
	if got := chamberChangeNote(k, old, false, "", "", false); got != "Motor 2: wieder OK" {
		t.Errorf("Notiz: %s", got)
	}
}

func TestChamberTabRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	groups := []chamberGroup{}
	for _, k := range chamberKinds {
		g := chamberGroup{chamberKind: k}
		if k.Key == "motor" {
			g.Slots = []chamberSlot{{Position: 1, PartLabel: "M-100 – Lüftermotor"}, {Position: 2, NOK: true, LastChangeISO: "2026-10-01", Cause: "Lager"}, {Position: 3}}
			g.NOKCount = 1
		}
		groups = append(groups, g)
	}
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "TK 1", Type: "drying_chamber"}, Types: infraTypes, Chamber: groups, ChamberNOK: 1,
		ChamberParts: []chamberPartOption{{ID: "p1", Label: "M-100 – Lüftermotor"}}, Today: "2026-10-10"}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	for _, want := range []string{`data-tab="chamber"`, "1 NOK", `data-pane="chamber"`, `id="cp-motor"`, `id="cp-door_seal"`, "Klappenmotoren", "Heizungsstellventile",
		"Heizungspumpen", "Torrollen", "Tordichtungen", `action="/infrastructure/n1/components/motor/count"`, `action="/infrastructure/n1/components/motor"`,
		`name="part_1" value="M-100 – Lüftermotor"`, `class="cp-tile nok"`, `name="nok_2" value="1" checked`, `name="last_2" value="2026-10-01"`,
		`name="cause_2" value="Lager"`, "Motor 3", `<datalist id="cp-parts">`, "Noch keine Klappenmotoren"} {
		if !strings.Contains(out, want) {
			t.Errorf("Bauteile ohne %q", want)
		}
	}
	// andere Typen: kein Reiter
	d.Node.Type = "building"
	if strings.Contains(renderPage(t, tmpl, "infra_detail", d), `data-tab="chamber"`) {
		t.Error("Reiter „Bauteile“ bei einem Gebäude")
	}
	// ohne Recht: nur lesen
	d.Node.Type, d.CanEditInfra = "drying_chamber", false
	ro := renderPage(t, tmpl, "infra_detail", d)
	if strings.Contains(ro, "/components/motor/count") || !strings.Contains(ro, `name="cause_2" value="Lager" maxlength="300" placeholder="z. B. Lagerschaden, Verschleiß" disabled`) {
		t.Error("Leserechte: Formular nicht gesperrt")
	}
}
