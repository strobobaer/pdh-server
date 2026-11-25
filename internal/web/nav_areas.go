package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Bereichsleiste des neuen Rahmens (0.90.0 Teil 2, Entwurf 5.2): 9 Bereiche für alle
// gleich, die Unterseiten eines Bereichs sind die Reiter. Jeder ändert nur Reihenfolge
// und Favoriten (Entscheidung „Bereiche gleich, Favoriten“); die Firma legt die
// Vorgabe fest (system.ui_defaults). Sichtbar ist nur, was navDefs mit dem Recht zeigt.

type navArea struct {
	Key, Label, Icon string
	Items            []navItem
}

// navAreaDefs: feste Zuordnung aller Menüeinträge (Schlüssel aus navDefs).
var navAreaDefs = []struct {
	Key, Label, Icon string
	Items            []string
}{
	{"start", "Start", "ti-home", []string{"dashboard", "leitstand", "assignments"}},
	{"vorgaenge", "Vorgänge", "ti-list-details", []string{"faults", "tickets", "tasks", "maintenance", "setup", "downtime", "kvp", "projects"}},
	{"planung", "Planung", "ti-calendar", []string{"shifts", "calendar", "time", "attendance"}},
	{"anlagen", "Anlagen", "ti-building-factory-2", []string{"infrastructure", "plantwatch", "it", "vpn", "esppdh", "energy"}},
	{"material", "Material", "ti-package", []string{"inventory", "storage", "procurement", "purchases", "directory"}},
	{"wissen", "Wissen", "ti-books", []string{"ms", "ms-handbook", "ms-process", "ms-instruction", "ms-form", "m365", "trainings"}},
	{"daten", "Daten", "ti-chart-bar", []string{"statistics", "import", "export", "sync", "datanet"}},
	{"verwaltung", "Verwaltung", "ti-settings", []string{"users", "orgunits", "orgchart", "roles", "core", "serverconfig", "network", "backup", "cleanup", "printers", "appdevices", "kiosk", "leitstaende", "terminal-slides", "costcenters", "categories", "icons", "ui-catalog"}},
	{"dienste", "Dienste", "ti-apps", []string{"nextcloud"}},
}

// navOutsideAreas: Einträge außerhalb der Bereiche – „Neu anlegen“ (Menüband, Strg+K),
// Chat und Handbuch (rechte Leiste, Benutzermenü).
var navOutsideAreas = map[string]bool{"assign": true, "chat": true, "help": true}

const (
	keyDefaultUIAreas = "defaults.ui_areas"
	maxAreaFavorites  = 12
)

// uiAreaPrefs: eigene oder vorgegebene Reihenfolge der Bereiche und Favoriten.
type uiAreaPrefs struct {
	Order     []string `json:"order"`
	Favorites []string `json:"favorites"`
}

// areaView: was der neue Rahmen zeigt (BaseData.Rail).
type areaView struct {
	Areas     []navArea
	Current   string    // Bereich der aktuellen Seite
	Tabs      []navItem // Unterseiten des aktuellen Bereichs
	Favorites []navItem
}

// sanitizeAreaPrefs: nur bekannte Bereiche (ohne Doppel, höchstens 9) und bekannte
// Menüeinträge als Favoriten (höchstens 12).
func sanitizeAreaPrefs(p uiAreaPrefs) uiAreaPrefs {
	known := map[string]bool{}
	for _, a := range navAreaDefs {
		known[a.Key] = true
	}
	var out uiAreaPrefs
	seen := map[string]bool{}
	for _, k := range p.Order {
		if known[k] && !seen[k] && len(out.Order) < len(navAreaDefs) {
			seen[k] = true
			out.Order = append(out.Order, k)
		}
	}
	seen = map[string]bool{}
	for _, k := range p.Favorites {
		if _, ok := navDefByKey(k); ok && !seen[k] && len(out.Favorites) < maxAreaFavorites {
			seen[k] = true
			out.Favorites = append(out.Favorites, k)
		}
	}
	return out
}

// buildAreas: Bereiche mit sichtbaren Einträgen in der gewünschten Reihenfolge, der
// Bereich der aktuellen Seite mit seinen Reitern und die sichtbaren Favoriten.
func buildAreas(b *BaseData, prefs uiAreaPrefs) areaView {
	prefs = sanitizeAreaPrefs(prefs)
	visible := func(key string) (navItem, bool) {
		d, ok := navDefByKey(key)
		if !ok || !d.show(b) {
			return navItem{}, false
		}
		return navItemFor(b, d), true
	}
	byKey := map[string]navArea{}
	for _, def := range navAreaDefs {
		a := navArea{Key: def.Key, Label: tr(b.Lang, def.Label), Icon: def.Icon}
		for _, k := range def.Items {
			if it, ok := visible(k); ok {
				a.Items = append(a.Items, it)
			}
		}
		if len(a.Items) > 0 {
			byKey[def.Key] = a
		}
	}
	var v areaView
	placed := map[string]bool{}
	add := func(k string) {
		if a, ok := byKey[k]; ok && !placed[k] {
			placed[k] = true
			v.Areas = append(v.Areas, a)
		}
	}
	for _, k := range prefs.Order {
		add(k)
	}
	for _, def := range navAreaDefs {
		add(def.Key)
	}
	for _, a := range v.Areas {
		for _, it := range a.Items {
			if it.Page != "" && it.Page == b.Page && v.Current == "" {
				v.Current, v.Tabs = a.Key, a.Items
			}
		}
	}
	for _, k := range prefs.Favorites {
		if it, ok := visible(k); ok {
			v.Favorites = append(v.Favorites, it)
		}
	}
	return v
}

func decodeAreaPrefs(raw []byte) (uiAreaPrefs, bool) {
	var p uiAreaPrefs
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return uiAreaPrefs{}, false
	}
	return sanitizeAreaPrefs(p), true
}

// firmAreaPrefs: Vorgabe der Firma (leer = Reihenfolge im Code, keine Favoriten).
func (h *Handler) firmAreaPrefs(ctx context.Context) uiAreaPrefs {
	if h.db == nil { // nur Tests ohne Datenbank
		p, _ := decodeAreaPrefs([]byte(h.getUpdateSetting(ctx, keyDefaultUIAreas, "")))
		return p
	}
	h.loadDefaults(ctx)
	defaultsCache.RLock()
	defer defaultsCache.RUnlock()
	return defaultsCache.uiAreas
}

// areaPrefs: eigene Anordnung, sonst die der Firma.
func (h *Handler) areaPrefs(ctx context.Context, uid string) uiAreaPrefs {
	var raw []byte
	switch {
	case uid == "":
	case h.db == nil:
		raw = []byte(h.getUpdateSetting(ctx, "users."+uid+".ui_areas", ""))
	default:
		if row := userRowFrom(ctx, uid); row != nil {
			raw = row.uiAreas
		} else {
			_ = h.db.QueryRow(ctx, `SELECT COALESCE(ui_areas::text, '') FROM users WHERE id = $1::uuid`, uid).Scan(&raw)
		}
	}
	if p, ok := decodeAreaPrefs(raw); ok {
		return p
	}
	return h.firmAreaPrefs(ctx)
}

// readAreaPrefs: JSON {order, favorites} oder {reset:true} (höchstens 8 KB).
func readAreaPrefs(r *http.Request) (p uiAreaPrefs, reset bool, ok bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10+1))
	if err != nil || len(body) > 8<<10 {
		return p, false, false
	}
	var req struct {
		uiAreaPrefs
		Reset bool `json:"reset"`
	}
	if json.Unmarshal(body, &req) != nil {
		return p, false, false
	}
	return sanitizeAreaPrefs(req.uiAreaPrefs), req.Reset, true
}

// UIAreasSaveWeb: POST /account/ui-areas – eigene Reihenfolge und Favoriten.
func (h *Handler) UIAreasSaveWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	if uid == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "nicht angemeldet"})
		return
	}
	p, reset, ok := readAreaPrefs(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Anordnung"})
		return
	}
	val := ""
	if !reset {
		b, _ := json.Marshal(p)
		val = string(b)
	}
	var err error
	if h.db == nil {
		err = h.setUpdateSetting(r.Context(), "users."+uid+".ui_areas", val)
	} else if reset {
		_, err = h.db.Exec(r.Context(), `UPDATE users SET ui_areas = NULL WHERE id = $1::uuid`, uid)
	} else {
		_, err = h.db.Exec(r.Context(), `UPDATE users SET ui_areas = $2::jsonb WHERE id = $1::uuid`, uid, val)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "nicht gespeichert"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// UIAreasDefaultSaveWeb: POST /admin/gestaltung/areas – Vorgabe der Firma (system.ui_defaults).
func (h *Handler) UIAreasDefaultSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permUIDefaults) {
		writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "keine berechtigung"})
		return
	}
	p, reset, ok := readAreaPrefs(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Anordnung"})
		return
	}
	val := ""
	if !reset {
		b, _ := json.Marshal(p)
		val = string(b)
	}
	if err := h.setUpdateSetting(r.Context(), keyDefaultUIAreas, val); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "nicht gespeichert"})
		return
	}
	resetDefaultsCache()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
