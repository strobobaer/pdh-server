package web

import (
	"net/http"
	"strings"

	"pdh/internal/core/infrastructure"
)

// Befehlszeile Strg+K des neuen Rahmens (0.90.0 Teil 2, Entwurf 5.2): Aktionen, Seiten
// und Anlagen – nur, was die vorhandenen Rechte erlauben. Vorgänge (Nummer, Titel)
// kommen mit Teil 3 hinzu, wenn die Listen die Rechte je Datensatz liefern.

type paletteItem struct {
	Kind  string `json:"kind"` // action, page, asset
	Label string `json:"label"`
	Href  string `json:"href"`
	Hint  string `json:"hint,omitempty"`
}

const paletteMax = 20

// paletteActions: Anlegen über den vorhandenen Assistenten (#create:<art> → pdhCreate).
// Perm wie die Anlege-Regel: Störung und Ticket darf jeder Angemeldete melden (wie im
// Neu-anlegen-Assistenten), Aufgabe und Wartung nur mit Recht (Gate-Regeln).
var paletteActions = []struct{ Type, Perm, Label string }{
	{"fault", "", "Störung melden"},
	{"ticket", "", "Ticket anlegen"},
	{"task", "tasks.edit", "Aufgabe anlegen"},
	{"maintenance", "maintenance.edit", "Wartung planen"},
}

// paletteAssetMax: höchstens so viele Anlagen je Suche (je Treffer eine Rechteprüfung).
const paletteAssetMax = 10

// paletteAsset: Anlagen-Treffer, schon nach Recht gefiltert, mit Pfad.
type paletteAsset struct{ ID, Name, Path string }

// paletteAssets: Treffer der Anlagensuche, die allow (Recht am Datensatz) erlaubt, in
// der Reihenfolge der Suche, mit Pfad aus paths.
func paletteAssets(nodes []*infrastructure.Infrastructure, allow func(id string) bool, paths map[string]string) []paletteAsset {
	var out []paletteAsset
	for _, n := range nodes {
		if len(out) >= paletteAssetMax {
			break
		}
		if n == nil || (allow != nil && !allow(n.ID)) {
			continue
		}
		out = append(out, paletteAsset{ID: n.ID, Name: n.Name, Path: paths[n.ID]})
	}
	return out
}

// paletteItems: Treffer für q. Unter 2 Zeichen nur Aktionen und Favoriten.

func paletteItems(b *BaseData, v areaView, q string, assets []paletteAsset) []paletteItem {
	q = strings.TrimSpace(q)
	lq := strings.ToLower(q)
	long := len([]rune(q)) >= 2
	match := func(label string) bool { return !long || strings.Contains(strings.ToLower(label), lq) }
	var out []paletteItem
	push := func(it paletteItem) {
		if len(out) < paletteMax {
			out = append(out, it)
		}
	}
	for _, a := range paletteActions {
		label := tr(b.Lang, a.Label)
		if (a.Perm == "" || (b.navCan != nil && b.navCan(a.Perm))) && match(label) {
			push(paletteItem{Kind: "action", Label: label, Href: "#create:" + a.Type})
		}
	}
	if !long {
		for _, it := range v.Favorites {
			push(paletteItem{Kind: "page", Label: it.Label, Href: it.Href, Hint: tr(b.Lang, "Favorit")})
		}
		return out
	}
	for _, a := range v.Areas {
		for _, it := range a.Items {
			if match(it.Label) {
				push(paletteItem{Kind: "page", Label: it.Label, Href: it.Href, Hint: a.Label})
			}
		}
	}
	if b.CanViewInfra {
		for _, n := range assets { // schon von der Anlagensuche gefunden (auch über Seriennummer u. a.)
			hint := n.Path
			if hint == "" {
				hint = tr(b.Lang, "Anlage")
			}
			push(paletteItem{Kind: "asset", Label: n.Name, Href: "/infrastructure/" + n.ID, Hint: hint})
		}
	}
	return out
}

// PaletteWeb: GET /api/palette?q= – Treffer der Befehlszeile als JSON.
func (h *Handler) PaletteWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	if uid == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "nicht angemeldet"})
		return
	}
	q := r.URL.Query().Get("q")
	b := h.baseData(r, "", "", "") // keine eigene Seite (kein Handbuch-Kapitel), nur Rechte und Bereiche
	v := b.Rail
	if !b.NewFrame { // Leiste ist nur im neuen Rahmen gefüllt
		v = buildAreas(&b, h.areaPrefs(r.Context(), uid))
	}
	var nodes []*infrastructure.Infrastructure
	if b.CanViewInfra && len([]rune(strings.TrimSpace(q))) >= 2 && h.infra != nil {
		nodes, _ = h.infra.Search(r.Context(), strings.TrimSpace(q))
	}
	assets := paletteAssets(nodes, func(id string) bool {
		// wie canViewInfra, aber mit der Abteilung der Anlage (wie die Rüstzeit-Seite)
		return h.hasPermRec(r, "infrastructure.view", "infrastructure", id) || h.hasPermRec(r, "infrastructure.edit", "infrastructure", id)
	}, nil)
	if len(assets) > 0 {
		ids := make([]string, len(assets))
		for i, a := range assets {
			ids[i] = a.ID
		}
		paths := h.infraPaths(r.Context(), ids)
		for i := range assets {
			assets[i].Path = paths[assets[i].ID]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": paletteItems(&b, v, q, assets)})
}
