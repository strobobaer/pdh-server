package web

import (
	"strings"
	"testing"
)

// Menüband des neuen Rahmens (0.90.0 Teil 2): Grundfunktionen, Anpassen, Darstellung.

func ribbonActionsOf(tabs []ribbonTab) map[string]ribbonItem {
	out := map[string]ribbonItem{}
	for _, tab := range tabs {
		for _, g := range tab.Groups {
			for _, it := range g.Items {
				out[it.Key] = it
			}
		}
	}
	return out
}

func TestRibbonForBasics(t *testing.T) {
	b := &BaseData{Page: "network", navCan: func(string) bool { return false }}
	tabs := ribbonFor(b)
	var keys []string
	for _, tab := range tabs {
		keys = append(keys, tab.Key)
	}
	if strings.Join(keys, ",") != "start,ansicht,anpassen,darstellung" {
		t.Fatalf("Reiter: %v", keys)
	}
	items := ribbonActionsOf(tabs)
	if it, ok := items["new"]; !ok || it.Action != "create" {
		t.Errorf("„Neu“ fehlt im Reiter Start: %+v", it)
	}
	if _, ok := items["style-fiori"]; ok {
		t.Error("Stilwahl ohne Erlaubnis der Firma")
	}
	if _, ok := items["areas-firm"]; ok {
		t.Error("Firmenvorgabe ohne Recht")
	}
	if it := items["fav"]; it.Arg != "network" {
		t.Errorf("Favorit für die aktuelle Seite: %q", it.Arg)
	}
	if _, ok := items["dashboard-edit"]; ok {
		t.Error("„Startseite anpassen“ außerhalb der Startseite")
	}
	b2 := &BaseData{Page: "dashboard", navCan: func(p string) bool { return p == permUIDefaults }}
	b2.Look.Allow = true
	items = ribbonActionsOf(ribbonFor(b2))
	for _, k := range []string{"style-fiori", "style-fluent", "areas-firm", "dashboard-edit", "mode-system", "density-touch"} {
		if _, ok := items[k]; !ok {
			t.Errorf("Eintrag %s fehlt", k)
		}
	}
	js := uiFile(t, "pdh-frame.js")
	for _, a := range ribbonActions {
		if !strings.Contains(js, "'"+a+"'") {
			t.Errorf("pdh-frame.js kennt die Aktion %q nicht", a)
		}
	}
	known := map[string]bool{}
	for _, a := range ribbonActions {
		known[a] = true
	}
	for k, it := range items {
		if it.Href == "" && !known[it.Action] {
			t.Errorf("Eintrag %s mit unbekannter Aktion %q", k, it.Action)
		}
	}
}

func TestFrameRibbon(t *testing.T) {
	d := frameTestData(true)
	d.Page = "network"
	d.navCan = func(p string) bool { return p == permNetwork }
	d.Rail = buildAreas(&d.BaseData, uiAreaPrefs{})
	d.Ribbon = ribbonFor(&d.BaseData)
	out := renderFrameForTest(t, "server_config", d)
	for _, want := range []string{`role="tablist" aria-label="Menüband"`, `data-ribbon-tab="start"`, `data-ribbon-panel="darstellung"`,
		`data-ribbon-action="create"`, `aria-label="Neu"`, `title="Neu"`, `data-ribbon-arg="network"`, `data-ribbon-more`,
		`data-order="`} {
		if !strings.Contains(out, want) {
			t.Errorf("Menüband ohne %q", want)
		}
	}
	if strings.Contains(renderFrameForTest(t, "server_config", frameTestData(false)), `class="ribbon"`) {
		t.Error("Menüband im klassischen Rahmen")
	}
}
