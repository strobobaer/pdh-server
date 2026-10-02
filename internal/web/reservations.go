package web

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Ersatzteil-Reservierung: Rueckbuchung nicht benoetigter Mengen.
// Die Reservierung selbst entsteht beim Vormerken in den Modulen
// (inventory.Service.Reserve), der Verbrauch beim Abschluss.

// reservationKinds: Vorgangsart -> (Art im Lager-Modul, Bearbeitungsrecht)
var reservationKinds = map[string][2]string{
	"fault":            {"fault", "faults.edit"},
	"ticket":           {"ticket", "tickets.edit"},
	"task":             {"task", "tasks.edit"},
	"maintenance":      {"maintenance_task", "maintenance.edit"},
	"maintenance_task": {"maintenance_task", "maintenance.edit"},
}

// ReservationReturnWeb: POST /reservations/{kind}/{ref}/{id}/return
// Body {"qty": n} – n <= 0 oder ohne Angabe: alles zurueckbuchen.
func (h *Handler) ReservationReturnWeb(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) {
		completionJSON(w, status, map[string]any{"success": false, "error": tr(h.requestLang(r), msg)})
	}
	k, ok := reservationKinds[chi.URLParam(r, "kind")]
	if !ok {
		fail(http.StatusNotFound, "Unbekannte Vorgangsart.")
		return
	}
	if !h.canFn(r)(k[1]) {
		fail(http.StatusForbidden, "Keine Berechtigung.")
		return
	}
	ref, id := chi.URLParam(r, "ref"), chi.URLParam(r, "id")
	if !h.recordInScope(r.Context(), h.requestScope(r), k[0], ref) {
		fail(http.StatusForbidden, "Keine Berechtigung.")
		return
	}
	var in struct {
		Qty float64 `json:"qty"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			fail(http.StatusBadRequest, "ungültige Daten")
			return
		}
	}
	if in.Qty < 0 {
		fail(http.StatusBadRequest, "Die Menge darf nicht negativ sein.")
		return
	}
	back, err := h.inv.ReturnReservation(r.Context(), k[0], ref, id, in.Qty, getUser(r).ID)
	if err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
		return
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "returned": back})
}
