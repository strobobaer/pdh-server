package web

import "strings"

// Menüband des neuen Rahmens (0.90.0 Teil 2, Entwurf 5.2/10.1): Reiter Start, Ansicht,
// Anpassen, Darstellung mit den allgemeinen Funktionen. Seiteneigene Aktionen kommen
// mit Teil 3 hinzu; bis dahin behalten die Seiten ihre Knöpfe im Inhalt. Aktionen
// sind Namen, die pdh-frame.js kennt – kein JavaScript-Text aus Go.

type ribbonItem struct {
	Key, Label, Icon string
	Href             string // Link statt Aktion
	Action, Arg      string // Name in pdh-frame.js und sein Wert (data-ribbon-action/-arg)
}

type ribbonGroup struct {
	Label string
	Items []ribbonItem
}

type ribbonTab struct {
	Key, Label string
	Groups     []ribbonGroup
}

// ribbonActions: alle Aktionen, die pdh-frame.js ausführt (Test prüft beide Seiten).
var ribbonActions = []string{"create", "toggle-right", "toggle-narrow", "fullscreen", "fav", "areas-order",
	"dashboard-edit", "areas-firm", "style", "mode", "palette", "density"}

// navKeyForPage: Menüeintrag der Seite (für „Als Favorit anheften“).
func navKeyForPage(page string) string {
	if page == "" {
		return ""
	}
	for _, d := range navDefs {
		if d.Page == page {
			return d.Key
		}
	}
	return ""
}

func ribbonFor(b *BaseData) []ribbonTab {
	l := func(s string) string { return tr(b.Lang, s) }
	act := func(key, label, icon, action, arg string) ribbonItem {
		return ribbonItem{Key: key, Label: l(label), Icon: icon, Action: action, Arg: arg}
	}
	start := ribbonTab{Key: "start", Label: l("Start"), Groups: []ribbonGroup{
		{Label: l("Vorgang"), Items: []ribbonItem{act("new", "Neu", "ti-plus", "create", "")}},
	}}
	view := ribbonTab{Key: "ansicht", Label: l("Ansicht"), Groups: []ribbonGroup{
		{Label: l("Leisten"), Items: []ribbonItem{
			act("right", "Rechte Leiste", "ti-layout-sidebar-right", "toggle-right", ""),
			act("narrow", "Schmale Leiste", "ti-list", "toggle-narrow", ""),
			act("fullscreen", "Vollbild", "ti-arrows-maximize", "fullscreen", ""),
		}},
	}}
	areas := []ribbonItem{act("areas-order", "Bereiche ordnen", "ti-arrows-sort", "areas-order", "")}
	if k := navKeyForPage(b.Page); k != "" {
		areas = append([]ribbonItem{act("fav", "Favorit", "ti-star", "fav", k)}, areas...)
	}
	if b.navCan != nil && b.navCan(permUIDefaults) {
		areas = append(areas, act("areas-firm", "Als Firmenvorgabe speichern", "ti-building", "areas-firm", ""))
	}
	adjust := ribbonTab{Key: "anpassen", Label: l("Anpassen"), Groups: []ribbonGroup{{Label: l("Bereiche"), Items: areas}}}
	if b.Page == "dashboard" {
		adjust.Groups = append(adjust.Groups, ribbonGroup{Label: l("Startseite"), Items: []ribbonItem{
			act("dashboard-edit", "Startseite anpassen", "ti-layout-dashboard", "dashboard-edit", ""),
		}})
	}
	look := ribbonTab{Key: "darstellung", Label: l("Darstellung")}
	if b.Look.Allow {
		look.Groups = append(look.Groups,
			ribbonGroup{Label: l("Stil"), Items: []ribbonItem{
				act("style-fiori", "Fiori", "ti-layout-grid", "style", "fiori"),
				act("style-fluent", "Fluent", "ti-layout-grid", "style", "fluent"),
			}},
			ribbonGroup{Label: l("Farbschema"), Items: []ribbonItem{act("palette", "Farbschema", "ti-palette", "palette", "")}})
	}
	look.Groups = append(look.Groups,
		ribbonGroup{Label: l("Modus"), Items: []ribbonItem{
			act("mode-light", "Hell", "ti-sun", "mode", "light"),
			act("mode-dark", "Dunkel", "ti-moon", "mode", "dark"),
			act("mode-system", "Wie System", "ti-device-desktop", "mode", "system"),
		}},
		ribbonGroup{Label: l("Dichte"), Items: []ribbonItem{
			act("density-compact", "Kompakt", "ti-line-height", "density", "90"),
			act("density-normal", "Normal", "ti-line-height", "density", "100"),
			act("density-touch", "Berührung", "ti-hand-finger", "density", "115"),
		}})
	return []ribbonTab{start, view, adjust, look}
}

// OrderKeys: aktuelle Reihenfolge als Liste mit Leerzeichen (data-order an der Leiste,
// für pdh-frame.js: „Als Firmenvorgabe speichern“).
func (v areaView) OrderKeys() string {
	keys := make([]string, 0, len(v.Areas))
	for _, a := range v.Areas {
		keys = append(keys, a.Key)
	}
	return strings.Join(keys, " ")
}

