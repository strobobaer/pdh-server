package web

// Reiter „ESPPDH“ an der Anlage (0.82.11): die ESPPDH-Geräte genau dieser Anlage mit Zustand,
// Handshake, letzten Werten und Links. Sichtbar mit esppdh.values oder esppdh.view für die
// Anlage; der Link zur Geräteseite nur mit esppdh.view. Inhalt per htmx alle 10 s.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// esppdhByInfra: Geräte der Anlagen infraIDs samt Bausteinen (für die Werte-Ansicht).
func (h *Handler) esppdhByInfra(ctx context.Context, infraIDs []string) ([]esppdhDevice, error) {
	if h.db == nil || len(infraIDs) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, esppdhSelect+` WHERE d.infrastructure_id = ANY($1::uuid[]) ORDER BY lower(d.name), d.slug`, infraIDs)
	if err != nil {
		return nil, err
	}
	var list []esppdhDevice
	var ids []string
	for rows.Next() {
		d, err := esppdhScan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, d)
		ids = append(ids, d.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}
	brows, err := h.db.Query(ctx, `SELECT device_id::text, id::text, kind, nr, params FROM esppdh_blocks
		WHERE device_id::text = ANY($1::text[]) ORDER BY device_id, position, kind, nr`, ids)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	byID := map[string]*esppdhDevice{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	for brows.Next() {
		var dev string
		var b esppdhBlock
		var raw []byte
		if err := brows.Scan(&dev, &b.ID, &b.Kind, &b.Nr, &raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &b.Params) != nil || b.Params == nil {
			b.Params = map[string]string{}
		}
		if d := byID[dev]; d != nil {
			d.Blocks = append(d.Blocks, b)
		}
	}
	// kein infraPaths: baut den ganzen Anlagenbaum, die Anzeigen (Reiter, Terminal, Schnellübersicht)
	// brauchen den Pfad nicht – alle 10 s je Bildschirm wäre das teuer (Schlussprüfung)
	return list, brows.Err()
}

// esppdhCanSeeValues: Werte der Geräte an dieser Anlage sehen (esppdh.values oder esppdh.view).
func (h *Handler) esppdhCanSeeValues(r *http.Request, infraID string) bool {
	return h.esppdhCanInfra(r, permESPValues, infraID) || h.esppdhCanInfra(r, permESPView, infraID)
}

// infraESPPDHTab: Reiter „ESPPDH“ zeigen – Gerät an genau dieser Anlage und Recht dafür.
func (h *Handler) infraESPPDHTab(r *http.Request, infraID string) bool {
	if h.db == nil || !meterUUIDRe.MatchString(infraID) || !h.esppdhCanSeeValues(r, infraID) {
		return false
	}
	var n int
	_ = h.db.QueryRow(r.Context(), `SELECT count(*) FROM esppdh_devices WHERE infrastructure_id = $1::uuid`, infraID).Scan(&n)
	return n > 0
}

// InfraESPPDHWeb: GET /infrastructure/{id}/esppdh – Inhalt des Reiters (htmx).
func (h *Handler) InfraESPPDHWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.canViewInfra(r) || !h.esppdhCanSeeValues(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	devs, err := h.esppdhByInfra(r.Context(), []string{id})
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("geräte der anlage lesen")
		http.Error(w, "Die Geräte ließen sich nicht lesen.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderFragment(w, "infra-esppdh", map[string]any{
		"Views": h.esppdhValues(r.Context(), devs, time.Now()),
		"Link":  h.esppdhCanInfra(r, permESPView, id),
	})
}
