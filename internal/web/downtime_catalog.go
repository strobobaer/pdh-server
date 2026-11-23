package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/stillstand"
)

// Stillstandsgründe – Stamm: Katalog der Gründe (Karte auf /infrastructure) und Zuordnung an der Anlage
// (Reiter „Stillstandsgründe“), geerbt nach unten wie die Rüsttasten (setup_catalog.go). Rechte
// downtime.view / downtime.edit / downtime.admin (Migration 199); die Rechte-Schranke (perm_gate.go)
// prüft zusätzlich.

const (
	permDowntimeView  = "downtime.view"
	permDowntimeEdit  = "downtime.edit"
	permDowntimeAdmin = "downtime.admin"
)

// downtimeCatalogBack: Rücksprung auf den Katalog (Karte aufgeklappt).
const downtimeCatalogBack = "/infrastructure#downtime-catalog"

// downtimeStore: Speicher der Stillstände; ohne Datenbank (Tests) nil.
func (h *Handler) downtimeStore() *stillstand.Store {
	if h.db == nil {
		return nil
	}
	return stillstand.NewStore(h.db)
}

// downtimeReasonRow: Grund im Katalog bzw. im Reiter der Anlage.
type downtimeReasonRow struct {
	stillstand.Reason
	Uses     int  // Katalog: an wie vielen Anlagen direkt zugeordnet
	Assigned bool // Reiter: direkt an dieser Anlage
	Last     bool // Katalog: letzter Eintrag (▼ aus)
}

// downtimeCatalogView: Gründe für die Karte auf /infrastructure.
type downtimeCatalogView struct{ Reasons []downtimeReasonRow }

func (h *Handler) downtimeCatalog(ctx context.Context) downtimeCatalogView {
	var v downtimeCatalogView
	st := h.downtimeStore()
	if st == nil {
		return v
	}
	reasons, err := st.Reasons(ctx)
	if err != nil {
		componentLog("stillstand").Warn().Err(err).Msg("gründe laden")
	}
	uses, err := st.Uses(ctx)
	if err != nil {
		componentLog("stillstand").Warn().Err(err).Msg("zuordnungen zählen")
	}
	for i, r := range reasons {
		v.Reasons = append(v.Reasons, downtimeReasonRow{Reason: r, Uses: uses[r.ID], Last: i == len(reasons)-1})
	}
	return v
}

// downtimeInfraTab: Daten des Reiters „Stillstandsgründe“ einer Anlage.
type downtimeInfraTab struct {
	Reasons []downtimeReasonRow // Katalog (aktive und die an dieser Anlage zugeordneten)
	Count   int                 // davon an dieser Anlage wirksam
}

func (h *Handler) infraDowntimeTab(ctx context.Context, infraID string) downtimeInfraTab {
	var t downtimeInfraTab
	st := h.downtimeStore()
	if st == nil {
		return t
	}
	catalog, err := st.Reasons(ctx)
	if err != nil {
		componentLog("stillstand").Warn().Err(err).Str("anlage", infraID).Msg("gründe laden")
		return t
	}
	eff, err := st.InfraReasons(ctx, infraID)
	if err != nil {
		componentLog("stillstand").Warn().Err(err).Str("anlage", infraID).Msg("gründe der anlage")
	}
	inherited, effective := map[string]string{}, map[string]bool{}
	for _, r := range eff {
		effective[r.ID], inherited[r.ID] = true, r.InheritedFrom
	}
	own, err := st.OwnReasons(ctx, infraID)
	if err != nil {
		componentLog("stillstand").Warn().Err(err).Str("anlage", infraID).Msg("eigene zuordnung")
	}
	for _, r := range catalog {
		if !r.Active && !own[r.ID] {
			continue
		}
		r.InheritedFrom = inherited[r.ID]
		t.Reasons = append(t.Reasons, downtimeReasonRow{Reason: r, Assigned: own[r.ID]})
		if effective[r.ID] {
			t.Count++
		}
	}
	return t
}

// ── Aktionen im Katalog ──────────────────────────────────────────────────

func (h *Handler) downtimeCatalogGuard(w http.ResponseWriter, r *http.Request, perm string) bool {
	if !h.setupCan(r, perm, "") {
		setupDenied(w, r)
		return false
	}
	_ = r.ParseForm()
	return true
}

// DowntimeReasonsWeb: POST /downtime/reasons-web (action=save|delete, id, name, active=1|0).
func (h *Handler) DowntimeReasonsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.downtimeCatalogGuard(w, r, permDowntimeEdit) {
		return
	}
	if r.FormValue("action") == "delete" && !h.setupCan(r, permDowntimeAdmin, "") { // Löschen nur Bereichsadministrator
		setupDenied(w, r)
		return
	}
	st, ctx, id := h.downtimeStore(), r.Context(), strings.TrimSpace(r.FormValue("id"))
	if id != "" && !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("action") == "delete" {
		switch err := st.DeleteReason(ctx, id); {
		case errors.Is(err, stillstand.ErrReasonInUse):
			setupCatalogRedirect(w, r, "", errors.New("Der Grund wird noch verwendet – bitte stattdessen ausblenden."))
		case errors.Is(err, stillstand.ErrNotFound):
			setupCatalogRedirect(w, r, "", errors.New("Grund nicht gefunden."))
		case err != nil:
			componentLog("stillstand").Error().Err(err).Msg("grund löschen")
			setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht gelöscht werden."))
		default:
			setupCatalogRedirect(w, r, "Grund gelöscht.", nil)
		}
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	if name == "" || utf8.RuneCountInString(name) > 80 {
		setupCatalogRedirect(w, r, "", errors.New("Stillstandsgrund: Name von 1 bis 80 Zeichen."))
		return
	}
	err := st.SaveReason(ctx, &stillstand.Reason{ID: id, Name: name, Active: r.FormValue("active") != "0"})
	switch {
	case errors.Is(err, stillstand.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Grund nicht gefunden."))
	case err != nil:
		componentLog("stillstand").Error().Err(err).Msg("grund speichern")
		setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht gespeichert werden."))
	default:
		setupCatalogRedirect(w, r, "Grund gespeichert.", nil)
	}
}

// DowntimeReasonMoveWeb: POST /downtime/reasons/{id}/move-web (dir=up|down).
func (h *Handler) DowntimeReasonMoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.downtimeCatalogGuard(w, r, permDowntimeEdit) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	switch err := h.downtimeStore().MoveReason(r.Context(), id, r.FormValue("dir") == "up"); {
	case errors.Is(err, stillstand.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Grund nicht gefunden."))
	case err != nil:
		componentLog("stillstand").Error().Err(err).Msg("grund verschieben")
		setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht verschoben werden."))
	default:
		setupCatalogRedirect(w, r, "", nil)
	}
}

// InfraDowntimeReasonsWeb: POST /infrastructure/{id}/downtime-reasons-web (reason_id …) – die direkt an
// dieser Anlage zugeordneten Gründe setzen (gilt auch für alle Unteranlagen).
func (h *Handler) InfraDowntimeReasonsWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.setupInfraGuard(w, r)
	if !ok {
		return
	}
	var ids []string
	for _, b := range r.Form["reason_id"] {
		if uuidInPathRe.MatchString(b) {
			ids = append(ids, b)
		}
	}
	back := "/infrastructure/" + id + "?tab=downtime"
	if err := h.downtimeStore().SetInfraReasons(r.Context(), id, ids); err != nil {
		componentLog("stillstand").Error().Err(err).Str("anlage", id).Msg("gründe zuordnen")
		infraRedirect(w, r, back, "", errors.New("Die Stillstandsgründe konnten nicht gespeichert werden."))
		return
	}
	h.addHistory(r.Context(), "infrastructure", id, "update", "Stillstandsgründe", "", "", "Stillstandsgründe zugeordnet", getUser(r).ID)
	infraRedirect(w, r, back, "Stillstandsgründe gespeichert", nil)
}
