package web

// Handshake auf der Anlagen-Schnellübersicht (0.82.11, „Anlagenstand“): je Karte die
// Lebenszeichen der Anlage samt Unteranlagen – ESPPDH-Geräte (Laufzeit) und
// Heartbeat-Zuordnungen (z. B. TIA-Baustein). Steht einer, wird die Karte gelb. Der Zähler eines
// ESPPDH-Geräts erscheint nur mit esppdh.values/esppdh.view für die Karte (watchUnmask); ohne
// Anfrage (Dashboard-Widget) bleibt er verborgen, „läuft/steht“ ist immer sichtbar. Alle 10 s
// tauscht /plant-watch/{id}/handshakes nur diese Zeilen (hx-swap-oob).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// watchHandshakes: Handshakes einer Karte; counts: verborgene Zähler der ESPPDH-Geräte je ID.
func (h *Handler) watchHandshakes(ctx context.Context, infraID string) ([]handshakeView, map[string]string) {
	if h.db == nil {
		return nil, nil
	}
	ids := h.infraSubtreeIDs(ctx, infraID)
	if len(ids) == 0 {
		ids = []string{infraID}
	}
	var out []handshakeView
	counts := map[string]string{}
	if devs, err := h.esppdhByInfra(ctx, ids); err != nil {
		componentLog("watch").Error().Err(err).Msg("esppdh-geräte")
	} else {
		for _, v := range h.esppdhValues(ctx, devs, time.Now()) {
			hs := v.Handshake
			hs.Label = v.Name
			counts[hs.ID], hs.Count = hs.Count, "" // erst mit Recht sichtbar (watchUnmask)
			out = append(out, hs)
		}
	}
	if hb, err := h.heartbeatHandshakes(ctx, ids); err != nil {
		componentLog("watch").Error().Err(err).Msg("heartbeats")
	} else {
		out = append(out, hb...)
	}
	return out, counts
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
		var body watchCards
		walkInfra(h.infraTreeViews(ctx), "", func(n InfraNodeView, _ string) {
			if b.Items[n.ID] || b.Types[n.Type] {
				c := watchCard{ID: n.ID}
				c.Handshakes, c.hsCounts = h.watchHandshakes(ctx, n.ID)
				body.Cards = append(body.Cards, c)
			}
		})
		h.watchUnmask(r, &body)
		w.Header().Set("Cache-Control", "no-store")
		h.renderFragment(w, "pw-hs-oob", body)
		return
	}
	http.Error(w, tr(lang, "Übersicht nicht gefunden oder nicht freigegeben"), http.StatusNotFound)
}
