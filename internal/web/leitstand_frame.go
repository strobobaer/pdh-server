package web

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Leitstand im neuen Rahmen (0.90.0, Entwurf 11.4): Band „Leitstand“ im Bereich Start. Der
// Leitstand erscheint im Mittelfeld (eingebettet, ohne eigenen Kopf); Bearbeiten, Als Standard
// für alle setzen und Vollbild stehen im Menüband (page_actions.go).

// LeitstandFramePage: GET /leitstand – im klassischen Rahmen weiter der Leitstand selbst.
func (h *Handler) LeitstandFramePage(w http.ResponseWriter, r *http.Request) {
	b := h.baseData(r, "leitstand", "Leitstand", "")
	if !b.NewFrame {
		http.Redirect(w, r, "/global/", http.StatusFound)
		return
	}
	if board := h.resolveLeitstand(r); board != nil {
		b.LeitstandID = board.ID
		if board.Name != "" {
			b.Title = board.Name
		}
	}
	b.Ribbon = ribbonFor(&b) // jetzt mit dem angezeigten Leitstand
	h.render(w, "leitstand_frame", b)
}

// LeitstandDefaultWeb: POST /leitstaende/{id}/default – dieser Leitstand wird der Standard
// für alle (Recht leitstand.manage). Er wird dabei auch aktiv.
func (h *Handler) LeitstandDefaultWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageLeitstand(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !isUUIDString(id) {
		http.Error(w, "unbekannter Leitstand", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM leitstaende WHERE id = $1::uuid`, id).Scan(&n); err != nil || n == 0 {
		http.Error(w, "unbekannter Leitstand", http.StatusNotFound)
		return
	}
	// erst den alten Standard lösen, dann den neuen setzen (Eindeutigkeit gilt je Zeile)
	if _, err := tx.Exec(ctx, `UPDATE leitstaende SET is_default = false WHERE is_default AND id <> $1::uuid`, id); err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE leitstaende SET is_default = true, active = true WHERE id = $1::uuid`, id); err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	u := getUser(r)
	_ = h.setUIMeta(ctx, "leitstand", strings.TrimSpace(u.FirstName+" "+u.LastName))
	w.WriteHeader(http.StatusNoContent)
}
