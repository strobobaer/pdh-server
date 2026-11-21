package web

// Handshake auf der Anlagen-Schnellübersicht (0.82.11, „Anlagenstand“): je Karte die
// Lebenszeichen der Anlage samt Unteranlagen – ESPPDH-Geräte (Laufzeit) und
// Heartbeat-Zuordnungen (z. B. TIA-Baustein). Steht einer, wird die Karte gelb. Der Zähler eines
// ESPPDH-Geräts erscheint nur mit esppdh.values/esppdh.view für die Karte (watchUnmask); ohne
// Anfrage (Dashboard-Widget) bleibt er verborgen, „läuft/steht“ ist immer sichtbar. Alle 10 s
// tauscht /plant-watch/{id}/handshakes nur diese Zeilen (hx-swap-oob). Gebündelt: eine Abfrage
// für alle Unteranlagen, je eine für Geräte und Heartbeats aller Karten.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// watchSubtrees: je Karte (Anlage) sie selbst und alle Unteranlagen – eine Abfrage für alle.
func (h *Handler) watchSubtrees(ctx context.Context, roots []string) (map[string][]string, error) {
	out := map[string][]string{}
	if h.db == nil || len(roots) == 0 {
		return out, nil
	}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE t AS (
			SELECT id AS root, id FROM infrastructure WHERE id::text = ANY($1::text[])
			UNION ALL
			SELECT t.root, i.id FROM infrastructure i JOIN t ON i.parent_id = t.id
		) SELECT root::text, id::text FROM t LIMIT 100000`, roots)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var root, id string
		if err := rows.Scan(&root, &id); err != nil {
			return out, err
		}
		out[root] = append(out[root], id)
	}
	return out, rows.Err()
}

// cardHandshakes: Handshakes einer Karte; counts: verborgene Zähler der ESPPDH-Geräte je ID.
type cardHandshakes struct {
	list   []handshakeView
	counts map[string]string
}

// watchHandshakesFor: Handshakes aller Karten roots.
func (h *Handler) watchHandshakesFor(ctx context.Context, roots []string) map[string]cardHandshakes {
	out := map[string]cardHandshakes{}
	if h.db == nil || len(roots) == 0 {
		return out
	}
	trees, err := h.watchSubtrees(ctx, roots)
	if err != nil {
		componentLog("watch").Error().Err(err).Msg("unteranlagen der karten")
		return out
	}
	seen := map[string]bool{}
	var all []string
	for _, ids := range trees {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
	}
	byInfra := map[string][]handshakeView{}
	if devs, err := h.esppdhByInfra(ctx, all); err != nil {
		componentLog("watch").Error().Err(err).Msg("esppdh-geräte")
	} else {
		for _, v := range h.esppdhValues(ctx, devs, time.Now()) {
			hs := v.Handshake
			hs.Label, hs.infra = v.Name, v.infra
			byInfra[v.infra] = append(byInfra[v.infra], hs)
		}
	}
	if hb, err := h.heartbeatHandshakes(ctx, all); err != nil {
		componentLog("watch").Error().Err(err).Msg("heartbeats")
	} else {
		for _, x := range hb {
			byInfra[x.infra] = append(byInfra[x.infra], x)
		}
	}
	for _, root := range roots {
		c := cardHandshakes{counts: map[string]string{}}
		for _, id := range trees[root] {
			for _, hs := range byInfra[id] {
				if strings.HasPrefix(hs.ID, "esp-") {
					c.counts[hs.ID], hs.Count = hs.Count, "" // erst mit Recht sichtbar (watchUnmask)
				}
				c.list = append(c.list, hs)
			}
		}
		out[root] = c
	}
	return out
}

// watchUnmask: Zähler der ESPPDH-Geräte für Karten zeigen, an denen die Person die Werte sehen darf.
func (h *Handler) watchUnmask(r *http.Request, body *watchCards) {
	for i := range body.Cards {
		c := &body.Cards[i]
		if len(c.hsCounts) == 0 || !h.esppdhCanSeeValues(r, c.ID) {
			continue
		}
		for j := range c.Handshakes {
			if n, ok := c.hsCounts[c.Handshakes[j].ID]; ok && strings.HasPrefix(c.Handshakes[j].ID, "esp-") {
				c.Handshakes[j].Count = n
			}
		}
	}
}

// PlantWatchHandshakesWeb: GET /plant-watch/{id}/handshakes – nur die Handshake-Zeilen der Karten.
func (h *Handler) PlantWatchHandshakesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	ctx := r.Context()
	id, lang := chi.URLParam(r, "id"), h.requestLang(r)
	for _, b := range h.watchBoards(ctx, getUser(r).ID, h.canManageWatch(r)) {
		if b.ID != id {
			continue
		}
		h.loadWatchContent(ctx, &b)
		var roots []string
		walkInfra(h.infraTreeViews(ctx), "", func(n InfraNodeView, _ string) {
			if b.Items[n.ID] || b.Types[n.Type] {
				roots = append(roots, n.ID)
			}
		})
		hs := h.watchHandshakesFor(ctx, roots)
		var body watchCards
		for _, root := range roots {
			body.Cards = append(body.Cards, watchCard{ID: root, Handshakes: hs[root].list, hsCounts: hs[root].counts})
		}
		h.watchUnmask(r, &body)
		w.Header().Set("Cache-Control", "no-store")
		h.renderFragment(w, "pw-hs-oob", body)
		return
	}
	http.Error(w, tr(lang, "Übersicht nicht gefunden oder nicht freigegeben"), http.StatusNotFound)
}
