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

// navAreaDefs: feste Zuordnung aller Menüeinträge (Schlüssel aus navDefs). Die
// Reihenfolge ist die Vorgabe; die ersten vier stehen am Handy unten (Entwurf 10.10).
var navAreaDefs = []struct {
	Key, Label, Icon string
	Items            []string
}{
	{"start", "Start", "ti-home", []string{"dashboard", "leitstand", "assignments"}},
	{"vorgaenge", "Vorgänge", "ti-list-details", []string{"faults", "tickets", "tasks", "maintenance", "setup", "downtime", "kvp", "projects"}},
	{"anlagen", "Anlagen", "ti-building-factory-2", []string{"infrastructure", "plantwatch", "it", "vpn", "esppdh", "energy"}},
	{"material", "Material", "ti-package", []string{"inventory", "storage", "procurement", "purchases", "directory"}},
	{"planung", "Planung", "ti-calendar", []string{"shifts", "calendar", "time", "attendance"}},
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

// uiAreaPrefs: Reihenfolge der Bereiche und Favoriten. Gespeichert je Benutzer
// (users.ui_areas): Order fehlt = Reihenfolge der Firma; Favoriten sind immer eigene.
// Die Firmenvorgabe (defaults.ui_areas) trägt nur die Reihenfolge.
type uiAreaPrefs struct {
	Order     []string `json:"order,omitempty"`
	Favorites []string `json:"favorites,omitempty"`
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

// firmAreaPrefs: Vorgabe der Firma – nur die Reihenfolge (leer = Reihenfolge im Code).
func (h *Handler) firmAreaPrefs(ctx context.Context) uiAreaPrefs {
	var p uiAreaPrefs
	if h.db == nil { // nur Tests ohne Datenbank
		p, _ = decodeAreaPrefs([]byte(h.getUpdateSetting(ctx, keyDefaultUIAreas, "")))
	} else {
		h.loadDefaults(ctx)
		defaultsCache.RLock()
		p = defaultsCache.uiAreas
		defaultsCache.RUnlock()
	}
	return uiAreaPrefs{Order: p.Order}
}

// areaPrefs: eigene Reihenfolge, sonst die der Firma; Favoriten immer die eigenen.
func (h *Handler) areaPrefs(ctx context.Context, uid string) uiAreaPrefs {
	p := h.ownAreaPrefs(ctx, uid)
	if len(p.Order) == 0 {
		p.Order = h.firmAreaPrefs(ctx).Order
	}
	return p
}

// ownAreaPrefs: gespeicherte eigene Werte, ungefiltert nach Rechten (ausgeblendete
// Favoriten und Bereiche bleiben erhalten, bis das Recht wiederkommt).
func (h *Handler) ownAreaPrefs(ctx context.Context, uid string) uiAreaPrefs {
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
	p, _ := decodeAreaPrefs(raw) // ungültig oder null = nichts Eigenes
	return p
}

// mergeAreaOrder: neue Reihenfolge der sichtbaren Bereiche; gespeicherte, die nicht
// mitkamen (ohne Recht ausgeblendet), behalten ihren Platz.
func mergeAreaOrder(stored, submitted []string) []string {
	out := append([]string(nil), submitted...)
	in := map[string]bool{}
	for _, k := range submitted {
		in[k] = true
	}
	for i, k := range stored {
		if in[k] {
			continue
		}
		if i > len(out) {
			i = len(out)
		}
		out = append(out[:i], append([]string{k}, out[i:]...)...)
	}
	return out
}

// areaPrefsRequest: {"fav":"<Eintrag>"} heftet an oder löst, {"order":[…]} ordnet,
// {"reset_order":true} folgt wieder der Firma, {"reset":true} löscht alles Eigene.
type areaPrefsRequest struct {
	Order      []string `json:"order"`
	Fav        string   `json:"fav"`
	ResetOrder bool     `json:"reset_order"`
	Reset      bool     `json:"reset"`
}

// readAreaPrefs: Anfrage lesen (höchstens 8 KB).
func readAreaPrefs(r *http.Request) (areaPrefsRequest, bool) {
	var req areaPrefsRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10+1))
	if err != nil || len(body) > 8<<10 || json.Unmarshal(body, &req) != nil {
		return req, false
	}
	return req, true
}

func (h *Handler) setOwnAreaPrefs(ctx context.Context, uid string, p uiAreaPrefs) error {
	p = sanitizeAreaPrefs(p)
	val := ""
	if len(p.Order) > 0 || len(p.Favorites) > 0 {
		b, _ := json.Marshal(p)
		val = string(b)
	}
	if h.db == nil {
		return h.setUpdateSetting(ctx, "users."+uid+".ui_areas", val)
	}
	if val == "" {
		_, err := h.db.Exec(ctx, `UPDATE users SET ui_areas = NULL WHERE id = $1::uuid`, uid)
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE users SET ui_areas = $2::jsonb WHERE id = $1::uuid`, uid, val)
	return err
}

// UIAreasSaveWeb: POST /account/ui-areas – eigene Favoriten und Reihenfolge.
func (h *Handler) UIAreasSaveWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	if uid == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "nicht angemeldet"})
		return
	}
	req, ok := readAreaPrefs(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Anordnung"})
		return
	}
	p := h.ownAreaPrefs(r.Context(), uid)
	switch {
	case req.Reset:
		p = uiAreaPrefs{}
	case req.ResetOrder:
		p.Order = nil
	case req.Fav != "":
		if _, known := navDefByKey(req.Fav); !known {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "unbekannter Eintrag"})
			return
		}
		var favs []string
		found := false
		for _, k := range p.Favorites {
			if k == req.Fav {
				found = true
				continue
			}
			favs = append(favs, k)
		}
		if !found {
			if len(favs) >= maxAreaFavorites {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "zu viele Favoriten"})
				return
			}
			favs = append(favs, req.Fav)
		}
		p.Favorites = favs
	case len(req.Order) > 0:
		p.Order = mergeAreaOrder(p.Order, sanitizeAreaPrefs(uiAreaPrefs{Order: req.Order}).Order)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Anordnung"})
		return
	}
	if err := h.setOwnAreaPrefs(r.Context(), uid, p); err != nil {
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
	req, ok := readAreaPrefs(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Anordnung"})
		return
	}
	val := ""
	if order := sanitizeAreaPrefs(uiAreaPrefs{Order: req.Order}).Order; !req.Reset && len(order) > 0 {
		b, _ := json.Marshal(uiAreaPrefs{Order: order}) // nur die Reihenfolge, keine Favoriten
		val = string(b)
	}
	if err := h.setUpdateSetting(r.Context(), keyDefaultUIAreas, val); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "nicht gespeichert"})
		return
	}
	resetDefaultsCache()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
