package web

import "strings"

// Menüband des neuen Rahmens (0.90.0, Entwurf 11.3): Start | Bänder des Bereichs |
// Ansicht | Einstellungen | Hilfe. Start steht in jedem Bereich und wächst mit der
// offenen Seite (pageActions); die Unterseiten des Bereichs sind Bänder (Links), das
// Band der offenen Seite trägt deren Funktionen. Aktionen sind Namen, die pdh-frame.js
// kennt – kein JavaScript-Text aus Go.

type ribbonItem struct {
	Key, Label, Icon string
	Href             string // Link statt Aktion
	Post             string // Formular mit POST an diese Adresse (z. B. /override-logout)
	Action, Arg      string // Name in pdh-frame.js und sein Wert (data-ribbon-action/-arg)
	Menu             []ribbonItem
	When             string // "" immer, "idle" nur ohne offene Bearbeitung, "editing" nur währenddessen
	Count            int    // Zahl am Knopf
	Primary          bool
	Disabled         bool
	Tip              string // Tooltip, sonst Label
	Blank            bool   // Link in neuem Fenster
	Since            string // laufende Zeit: Beginn (RFC3339 UTC) für die Dauer am Knopf
}

type ribbonGroup struct {
	Label string
	Items []ribbonItem
}

type ribbonTab struct {
	Key, Label string
	Href       string // Band einer anderen Unterseite: Link
	Band       bool   // Unterseite des Bereichs
	Page       bool   // Band der offenen Seite
	Selected   bool   // beim Laden gewählt
	External   bool
	Groups     []ribbonGroup
}

// Panel: Reiter mit eigenem Inhalt (nicht nur Link).
func (t ribbonTab) Panel() bool { return !t.Band || t.Page }

// ribbonActions: alle Aktionen, die pdh-frame.js ausführt (Test prüft beide Seiten).
var ribbonActions = []string{"create", "toggle-right", "toggle-narrow", "fullscreen", "fav", "areas-order",
	"dashboard-edit", "areas-firm", "style", "mode", "palette", "density", "search", "share-chat", "override-login",
	"undo", "save-edit", "cancel-edit", "time-start", "time-stop", "time-manual", "dash-standard", "leit-standard", "addw"}

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
	can := func(p string) bool { return b.navCan != nil && b.navCan(p) }
	pa := pageActions(b)

	// ── Start ──
	start := ribbonTab{Key: "start", Label: l("Start")}
	undo := act("undo", "Rückgängig", "ti-arrow-back-up", "undo", "")
	undo.Disabled, undo.Tip = true, l("Letzten Bearbeitungsschritt zurücknehmen – bis zum Speichern (Strg+Z)")
	start.Groups = append(start.Groups, ribbonGroup{Label: l("Rückgängig"), Items: []ribbonItem{undo}})
	newItem := act("new", "Neu", "ti-plus", "create", "")
	newItem.Primary = true
	for _, a := range paletteActions {
		if a.Perm == "" || can(a.Perm) {
			newItem.Menu = append(newItem.Menu, act("new-"+a.Type, a.Label, "ti-plus", "create", a.Type))
		}
	}
	news := []ribbonItem{newItem}
	if b.IsBroker || b.CanManageRoles {
		news = append(news, ribbonItem{Key: "assign", Label: l("Zuweisen"), Icon: "ti-inbox", Href: "/assignments", Tip: l("Offene Vorgänge ohne Zuweisung")})
	}
	start.Groups = append(start.Groups, ribbonGroup{Label: l("Neu"), Items: news})
	if len(pa.Start) > 0 {
		label := b.Title
		if label == "" {
			label = l("Diese Seite")
		}
		start.Groups = append(start.Groups, ribbonGroup{Label: label, Items: pa.Start})
	}
	if g, ok := timeGroup(b); ok {
		start.Groups = append(start.Groups, g)
	}
	cop := act("copilot", "Copilot", "ti-brain", "toggle-right", "copilot")
	search := act("search", "Suchen", "ti-search", "search", "")
	search.Tip = l("Suchen (Strg+K)")
	start.Groups = append(start.Groups, ribbonGroup{Label: l("Finden"), Items: []ribbonItem{search, cop}})
	if b.CanChat && b.Page != "chat" {
		start.Groups = append(start.Groups, ribbonGroup{Label: l("Mitteilungen"), Items: []ribbonItem{
			act("chat", "Chat", "ti-messages", "toggle-right", "chat"),
			act("share", "Im Chat teilen", "ti-share", "share-chat", ""),
		}})
	}
	switch {
	case b.IsBoardVisit:
		start.Groups = append(start.Groups, ribbonGroup{Label: l("Terminal"), Items: []ribbonItem{
			{Key: "board-back", Label: l("Zurück zum Leitstand"), Icon: "ti-corner-up-left", Href: "/logout", Primary: true}}})
	case b.IsOverrideSession:
		start.Groups = append(start.Groups, ribbonGroup{Label: l("Terminal"), Items: []ribbonItem{
			{Key: "override-back", Label: l("Zurück zum Systemnutzer"), Icon: "ti-corner-up-left", Post: "/override-logout", Primary: true}}})
	case b.IsSystemUser:
		start.Groups = append(start.Groups, ribbonGroup{Label: l("Terminal"), Items: []ribbonItem{
			act("override-login", "Anmelden zur Erledigung", "ti-user-plus", "override-login", "")}})
	}
	tabs := []ribbonTab{start}

	// ── Bänder des Bereichs ──
	for _, it := range b.Rail.Tabs {
		t := ribbonTab{Key: it.Key, Label: it.Label, Href: it.Href, Band: true, External: it.External, Page: it.Page != "" && it.Page == b.Page}
		if t.Page {
			t.Groups = pa.Band
			t.Selected = true
		}
		tabs = append(tabs, t)
	}

	// ── Ansicht ──
	look := act("m-look", "Darstellung", "ti-palette", "", "")
	if b.Look.Allow {
		look.Menu = append(look.Menu,
			act("style-fiori", "Fiori", "ti-layout-grid", "style", "fiori"),
			act("style-fluent", "Fluent", "ti-layout-grid", "style", "fluent"),
			act("palette", "Farbschema", "ti-palette", "palette", ""))
	}
	look.Menu = append(look.Menu,
		act("mode-light", "Hell", "ti-sun", "mode", "light"),
		act("mode-dark", "Dunkel", "ti-moon", "mode", "dark"),
		act("mode-system", "Wie System", "ti-device-desktop", "mode", "system"),
		act("density-compact", "Kompakt", "ti-line-height", "density", "90"),
		act("density-normal", "Normal", "ti-line-height", "density", "100"),
		act("density-touch", "Berührung", "ti-hand-finger", "density", "115"))
	viewItems := []ribbonItem{look}
	if !b.IsSystemUser { // Terminal: Bereiche gelten für alle, dort nicht anpassbar
		adjust := act("m-adjust", "Anpassen", "ti-adjustments", "", "")
		if k := navKeyForPage(b.Page); k != "" {
			adjust.Menu = append(adjust.Menu, act("fav", "Als Favorit", "ti-star", "fav", k))
		}
		adjust.Menu = append(adjust.Menu, act("areas-order", "Bereiche ordnen", "ti-arrows-sort", "areas-order", ""),
			act("dashboard-edit", "Startseite anpassen", "ti-layout-dashboard", "dashboard-edit", ""))
		if can(permUIDefaults) {
			adjust.Menu = append(adjust.Menu, act("areas-firm", "Reihenfolge als Firmenvorgabe", "ti-building", "areas-firm", ""))
		}
		viewItems = append(viewItems, adjust)
	}
	tabs = append(tabs, ribbonTab{Key: "ansicht", Label: l("Ansicht"), Groups: []ribbonGroup{
		{Label: l("Ansicht"), Items: viewItems},
		{Label: l("Leisten"), Items: []ribbonItem{
			act("right", "Rechte Leiste", "ti-layout-sidebar-right", "toggle-right", ""),
			act("narrow", "Schmale Leiste", "ti-list", "toggle-narrow", ""),
			act("fullscreen", "Vollbild", "ti-arrows-maximize", "fullscreen", ""),
		}},
	}})

	// ── Einstellungen, Hilfe ──
	if items := areaSettings(b, b.Rail.Current); len(items) > 0 {
		tabs = append(tabs, ribbonTab{Key: "einstellungen", Label: l("Einstellungen"), Groups: []ribbonGroup{{Label: l("Einstellungen"), Items: items}}})
	}
	tabs = append(tabs, ribbonTab{Key: "hilfe", Label: l("Hilfe"), Groups: []ribbonGroup{{Label: l("Hilfe"), Items: []ribbonItem{
		act("help-page", "Hilfe zu dieser Seite", "ti-help", "toggle-right", "help"),
		{Key: "manual", Label: l("Handbuch"), Icon: "ti-book", Href: "/help"},
		{Key: "versions", Label: l("Versionen & Updates"), Icon: "ti-history", Href: "/help#versions"},
	}}}})

	sel := false
	for _, t := range tabs {
		sel = sel || t.Selected
	}
	if !sel {
		tabs[0].Selected = true
	}
	return tabs
}

// areaSettings: Einstellungen des Bereichs (Entwurf 11.3) – nur, wofür das Recht da ist.
// Die übrigen Bereiche bekommen ihre Einstellungen mit Teil 6.
func areaSettings(b *BaseData, area string) []ribbonItem {
	var out []ribbonItem
	navLink := func(navKey, icon string) {
		if d, ok := navDefByKey(navKey); ok && d.show(b) {
			it := navItemFor(b, d)
			out = append(out, ribbonItem{Key: "set-" + navKey, Label: it.Label, Icon: icon, Href: it.Href})
		}
	}
	switch area {
	case "start":
		if b.navCan != nil && b.navCan(permDefaults) {
			out = append(out, ribbonItem{Key: "set-defaults", Label: tr(b.Lang, "Startseite-Vorgaben"), Icon: "ti-layout-dashboard", Href: "/core/settings#defaults"})
		}
		navLink("leitstaende", "ti-device-tv")
	case "verwaltung":
		navLink("core", "ti-settings")
		navLink("serverconfig", "ti-adjustments-cog")
	}
	return out
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
