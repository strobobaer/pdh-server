package web

import (
	"strings"
	"testing"
)

// Menüband des neuen Rahmens (0.90.0 Teil 2): Grundfunktionen, Anpassen, Darstellung.

func ribbonActionsOf(tabs []ribbonTab) map[string]ribbonItem {
	out := map[string]ribbonItem{}
	var add func([]ribbonItem)
	add = func(items []ribbonItem) {
		for _, it := range items {
			out[it.Key] = it
			add(it.Menu)
		}
	}
	for _, tab := range tabs {
		for _, g := range tab.Groups {
			add(g.Items)
		}
	}
	return out
}

func tabKeys(tabs []ribbonTab) []string {
	var out []string
	for _, t := range tabs {
		out = append(out, t.Key)
	}
	return out
}

func tab(tabs []ribbonTab, key string) ribbonTab {
	for _, t := range tabs {
		if t.Key == key {
			return t
		}
	}
	return ribbonTab{}
}

func selectedTab(tabs []ribbonTab) string {
	for _, t := range tabs {
		if t.Selected {
			return t.Key
		}
	}
	return ""
}

func groupLabels(t ribbonTab) []string {
	var out []string
	for _, g := range t.Groups {
		out = append(out, g.Label)
	}
	return out
}

func group(t ribbonTab, label string) []ribbonGroup {
	for _, g := range t.Groups {
		if g.Label == label {
			return []ribbonGroup{g}
		}
	}
	return nil
}

func eachItem(groups []ribbonGroup, f func(ribbonItem)) {
	var walk func([]ribbonItem)
	walk = func(items []ribbonItem) {
		for _, it := range items {
			f(it)
			walk(it.Menu)
		}
	}
	for _, g := range groups {
		walk(g.Items)
	}
}

func hasItem(t ribbonTab, label string) bool {
	found := false
	eachItem(t.Groups, func(it ribbonItem) { found = found || it.Label == label })
	return found
}

func hasAction(groups []ribbonGroup, action string) bool {
	found := false
	eachItem(groups, func(it ribbonItem) { found = found || it.Action == action })
	return found
}

func hasHref(groups []ribbonGroup, href string) bool {
	found := false
	eachItem(groups, func(it ribbonItem) { found = found || it.Href == href })
	return found
}

// keysWhen: Aktionen der Einträge, die nur im Zustand when sichtbar sind, in Reihenfolge.
func keysWhen(groups []ribbonGroup, when string) string {
	var out []string
	eachItem(groups, func(it ribbonItem) {
		if it.When == when {
			out = append(out, it.Action)
		}
	})
	return strings.Join(out, ",")
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}

// Reiterfolge (Entwurf 11.3): Start | Bänder des Bereichs | Ansicht | Einstellungen | Hilfe.
func TestRibbonTabsOrder(t *testing.T) {
	d := frameData(t, nil)
	if got := tabKeys(d.Ribbon); strings.Join(got, ",") != "start,time,dashboard,leitstand,ansicht,einstellungen,hilfe" {
		t.Fatalf("Reiter %v", got)
	}
	if sel := selectedTab(d.Ribbon); sel != "dashboard" {
		t.Errorf("gewählt %q", sel)
	}
	if b := tab(d.Ribbon, "time"); !b.Band || b.Href == "" || b.Page {
		t.Errorf("Band Zeiterfassung: %+v", b)
	}
	if b := tab(d.Ribbon, "dashboard"); !b.Band || !b.Page {
		t.Errorf("Band der offenen Seite: %+v", b)
	}
	for _, gone := range []string{"anpassen", "darstellung"} {
		if tab(d.Ribbon, gone).Key != "" {
			t.Errorf("Reiter %q gibt es nicht mehr", gone)
		}
	}
	items := ribbonActionsOf(d.Ribbon)
	for _, k := range []string{"m-look", "m-adjust", "mode-dark", "areas-order"} {
		if _, ok := items[k]; !ok {
			t.Errorf("Ansicht ohne %s", k)
		}
	}
}

func TestRibbonStartGroups(t *testing.T) {
	d := frameData(t, func(b *BaseData) { b.Page = "faults"; b.IsBroker, b.CanManageRoles = false, false })
	st := tab(d.Ribbon, "start")
	labels := groupLabels(st)
	for _, l := range []string{"Rückgängig", "Neu", "Zeit", "Finden"} {
		found := false
		for _, g := range labels {
			found = found || g == l
		}
		if !found {
			t.Errorf("Gruppe %q fehlt: %v", l, labels)
		}
	}
	if hasItem(st, "Zuweisen") {
		t.Error("Zuweisen ohne Recht")
	}
	d = frameData(t, func(b *BaseData) { b.Page = "faults"; b.IsBroker = true })
	if !hasHref(tab(d.Ribbon, "start").Groups, "/assignments") {
		t.Error("Zuweisen fehlt für Zuweisende")
	}
	if !hasAction(group(tab(d.Ribbon, "start"), "Neu"), "create") {
		t.Error("Neu öffnet nicht den Assistenten")
	}
}

func TestRibbonTerminalButtons(t *testing.T) {
	d := frameData(t, func(b *BaseData) { b.IsSystemUser, b.IsOverrideSession = true, true })
	html := renderFrameForTest(t, "server_config", d)
	if !strings.Contains(between(html, `<section class="ribbon"`, `</section>`), `action="/override-logout"`) {
		t.Error("„Zurück zum Systemnutzer“ fehlt im Menüband")
	}
}

func TestRibbonSettingsHiddenWithoutRights(t *testing.T) {
	d := frameData(t, func(b *BaseData) { b.navCan = func(string) bool { return false } })
	for _, k := range tabKeys(d.Ribbon) {
		if k == "einstellungen" {
			t.Error("leerer Reiter Einstellungen sichtbar")
		}
	}
}

func TestFrameHasNoTabBar(t *testing.T) {
	html := renderFrameForTest(t, "server_config", frameData(t, nil))
	if strings.Contains(html, `class="frame-tabs"`) {
		t.Error("Reiterleiste über dem Menüband noch da")
	}
	if !strings.Contains(html, `data-ribbon-band href="/time?bereich=start"`) {
		t.Error("Band Zeiterfassung fehlt")
	}
}

func TestRibbonForBasics(t *testing.T) {
	b := &BaseData{Page: "network", navCan: func(string) bool { return false }}
	tabs := ribbonFor(b)
	var keys []string
	for _, tab := range tabs {
		keys = append(keys, tab.Key)
	}
	if strings.Join(keys, ",") != "start,ansicht,hilfe" { // ohne Bereich keine Bänder, ohne Recht keine Einstellungen
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
	// „Startseite anpassen“ steht seit 0.82.67 immer unter Ansicht › Anpassen (Vorschau 26.11.2026)
	b2 :=&BaseData{Page: "dashboard", navCan: func(p string) bool { return p == permUIDefaults }}
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
		if it.Href == "" && it.Post == "" && it.Menu == nil && !known[it.Action] {
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
	for _, want := range []string{`role="tablist" aria-label="Menüband"`, `data-ribbon-tab="start"`, `data-ribbon-panel="ansicht"`,
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
