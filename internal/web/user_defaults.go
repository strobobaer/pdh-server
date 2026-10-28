package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Standards für alle (Core-Einstellungen): Wer das Recht system.defaults hat,
// richtet Darstellung, Menü und Dashboard bei sich ein und übernimmt sie mit
// einem Klick als Firmenstandard. Wer nichts Eigenes gespeichert hat, sieht
// den Firmenstandard; jeder kann danach weiter für sich anpassen.
// Reihenfolge: eigene Einstellung → Firmenstandard → Vorgabe im Code.
// Die Darstellung liegt wie bisher in branding.* (theme.go, appearance.go,
// ui_style.go), Menü und Dashboard als JSON in app_settings.

const (
	keyDefaultNav     = "defaults.nav_layout"
	keyDefaultWidgets = "defaults.dashboard_widgets"
	permDefaults      = "system.defaults"
)

// defaultsInfoKey: wann und von wem ein Standard zuletzt gesetzt wurde.
func defaultsInfoKey(what string) string { return "defaults." + what + ".info" }

var defaultsCache struct {
	sync.RWMutex
	loaded  bool
	nav     []navLayoutGroup
	widgets []WidgetInstance
}

func resetDefaultsCache() {
	defaultsCache.Lock()
	defaultsCache.loaded = false
	defaultsCache.Unlock()
}

func (h *Handler) loadDefaults(ctx context.Context) {
	defaultsCache.RLock()
	ok := defaultsCache.loaded
	defaultsCache.RUnlock()
	if ok || h.db == nil {
		return
	}
	var nav []navLayoutGroup
	if raw := h.appSetting(ctx, keyDefaultNav, ""); raw != "" && json.Unmarshal([]byte(raw), &nav) == nil {
		nav, _ = sanitizeNavLayout(nav)
	} else {
		nav = nil
	}
	var widgets []WidgetInstance
	if raw := h.appSetting(ctx, keyDefaultWidgets, ""); raw != "" && json.Unmarshal([]byte(raw), &widgets) == nil {
		if widgets == nil {
			widgets = []WidgetInstance{}
		}
	} else {
		widgets = nil
	}
	defaultsCache.Lock()
	defaultsCache.nav, defaultsCache.widgets, defaultsCache.loaded = nav, widgets, true
	defaultsCache.Unlock()
}

// companyNavLayout: Menü-Standard der Firma (nil = Vorgabe im Code).
func (h *Handler) companyNavLayout(ctx context.Context) []navLayoutGroup {
	h.loadDefaults(ctx)
	defaultsCache.RLock()
	defer defaultsCache.RUnlock()
	return defaultsCache.nav
}

// companyWidgets: Dashboard-Standard der Firma (nil = Vorgabe im Code).
func (h *Handler) companyWidgets(ctx context.Context) []WidgetInstance {
	h.loadDefaults(ctx)
	defaultsCache.RLock()
	defer defaultsCache.RUnlock()
	if defaultsCache.widgets == nil {
		return nil
	}
	return append([]WidgetInstance(nil), defaultsCache.widgets...)
}

func (h *Handler) canSetDefaults(r *http.Request) bool { return h.hasPerm(r, permDefaults) }

// DefaultsView: Anzeige der Karte „Standards für alle“.
type DefaultsView struct {
	Allowed                 bool
	DesignInfo, NavInfo     string // leer = Vorgabe
	DashboardInfo           string
	NavSet, DashboardSet    bool
	DesignName, DesignStyle string
}

func (h *Handler) defaultsView(r *http.Request) DefaultsView {
	ctx := r.Context()
	b := h.branding()
	p, _ := b.palette(b.ThemeID())
	style := ""
	for _, s := range uiStyles {
		if s.ID == b.StyleID() {
			style = s.Name
		}
	}
	return DefaultsView{
		Allowed:       h.canSetDefaults(r),
		DesignInfo:    h.appSetting(ctx, defaultsInfoKey("design"), ""),
		NavInfo:       h.appSetting(ctx, defaultsInfoKey("nav"), ""),
		DashboardInfo: h.appSetting(ctx, defaultsInfoKey("dashboard"), ""),
		NavSet:        h.companyNavLayout(ctx) != nil,
		DashboardSet:  h.companyWidgets(ctx) != nil,
		DesignName:    p.Name,
		DesignStyle:   style,
	}
}

// DefaultsSaveWeb: POST /core/settings/defaults
//
//	what   = design | nav | dashboard
//	action = set (eigene Einstellung übernehmen) | reset (Werksvorgabe)
//	reset_users = on → eigene Einstellungen aller Benutzer verwerfen
func (h *Handler) DefaultsSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canSetDefaults(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	u := getUser(r)
	what, action := r.FormValue("what"), r.FormValue("action")
	resetUsers := r.FormValue("reset_users") == "on"
	names := map[string]string{"design": "Darstellung", "nav": "Menü", "dashboard": "Dashboard"}
	name, ok := names[what]
	if !ok || (action != "set" && action != "reset") {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	var err error
	switch what {
	case "design":
		err = h.saveDesignDefault(r, action == "set", resetUsers)
	case "nav":
		err = h.saveJSONDefault(ctx, u.ID, action == "set", resetUsers, keyDefaultNav, "nav_layout",
			func() any {
				if l := h.userNavLayout(ctx, u.ID); l != nil {
					return l
				}
				return defaultNavLayout()
			})
	case "dashboard":
		err = h.saveJSONDefault(ctx, u.ID, action == "set", resetUsers, keyDefaultWidgets, "dashboard_widgets",
			func() any { return h.userWidgets(ctx, u.ID) })
	}
	if err != nil {
		http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(name+": nicht gespeichert – "+err.Error())+"#defaults", http.StatusSeeOther)
		return
	}
	info := ""
	if action == "set" {
		info = time.Now().Format("02.01.2006 15:04") + " von " + strings.TrimSpace(u.FirstName+" "+u.LastName)
	}
	_ = h.setAppSetting(ctx, defaultsInfoKey(what), info)
	resetDefaultsCache()
	msg := name + ": deine Einstellung ist jetzt Standard für alle"
	if action == "reset" {
		msg = name + ": Werksvorgabe gilt wieder für alle"
	}
	if resetUsers {
		msg += " – eigene Einstellungen aller Benutzer wurden verworfen"
	}
	componentLog("konfiguration").Info().Str("standard", what).Str("aktion", action).Bool("alle_zurueck", resetUsers).Str("user", u.ID).Msg("standard für alle gesetzt")
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(msg)+"#defaults", http.StatusSeeOther)
}

// saveDesignDefault: wirksame Darstellung der handelnden Person (Farbschema,
// Design-Stil, Schrift, Größe) wird Firmenstandard; danach folgt die Person
// selbst wieder dem Standard.
func (h *Handler) saveDesignDefault(r *http.Request, set, resetUsers bool) error {
	ctx := r.Context()
	theme, style, font, scale := defaultTheme, defaultStyle, defaultFont, defaultScale
	if set {
		a := h.appearance(r, h.branding())
		theme, style, font, scale = a.PaletteID(), a.StyleID(), a.FontID, a.Scale
	}
	for k, v := range map[string]string{keyBrandTheme: theme, keyBrandStyle: style, keyBrandFont: font, keyBrandScale: strconv.Itoa(scale)} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			return err
		}
	}
	resetBrandingCache()
	q := `UPDATE users SET ui_palette='', ui_font='', ui_style='', ui_scale=0 WHERE id=$1::uuid`
	if resetUsers {
		q = `UPDATE users SET ui_palette='', ui_font='', ui_style='', ui_scale=0 WHERE $1::text IS NOT NULL`
	}
	_, err := h.db.Exec(ctx, q, getUser(r).ID)
	return err
}

// saveJSONDefault: Menü oder Dashboard der handelnden Person als Standard
// (set) bzw. Standard löschen (Werksvorgabe im Code). Die eigene Spalte der
// Person wird geleert, damit sie selbst wieder dem Standard folgt.
func (h *Handler) saveJSONDefault(ctx context.Context, uid string, set, resetUsers bool, key, column string, current func() any) error {
	value := ""
	if set {
		data, err := json.Marshal(current())
		if err != nil {
			return err
		}
		value = string(data)
	}
	if err := h.setAppSetting(ctx, key, value); err != nil {
		return err
	}
	// column ist eine feste Konstante der Aufrufer (nav_layout, dashboard_widgets)
	q := `UPDATE users SET ` + column + ` = NULL WHERE id = $1::uuid`
	if resetUsers {
		q = `UPDATE users SET ` + column + ` = NULL WHERE $1::text IS NOT NULL`
	}
	_, err := h.db.Exec(ctx, q, uid)
	return err
}
