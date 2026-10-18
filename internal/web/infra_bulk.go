package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/infrastructure"
)

// Infrastruktur → „Sammelanlegen“ (Übergeordnet + Ebene 1 von–bis + optional
// Ebene 2 von–bis) und Kopieren einer Anlage samt Unteranlagen
// (infrastructure/bulk.go). Formularfelder wie beim Lager (storage_bulk.go).

func infraBulkMsg(what string, res infrastructure.BulkResult) string {
	msg := fmt.Sprintf("%s: %d angelegt", what, res.Created)
	if res.Skipped > 0 {
		msg += fmt.Sprintf(", %d gab es schon", res.Skipped)
	}
	return msg
}

// InfraBulkWeb: POST /infrastructure/bulk
func (h *Handler) InfraBulkWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	n1, err := namesFromForm(r, "l1")
	if err == nil && len(n1) == 0 {
		err = errors.New("Ebene 1: Name oder „von–bis“ angeben")
	}
	var n2 []string
	if err == nil {
		n2, err = namesFromForm(r, "l2")
	}
	if err == nil && len(n1)*max(1, len(n2)) > 2000 {
		err = fmt.Errorf("zu viele auf einmal (%d) – höchstens 2000", len(n1)*max(1, len(n2)))
	}
	if err != nil {
		infraRedirect(w, r, "/infrastructure", "", err)
		return
	}
	var parent *string
	if p := r.FormValue("parent_id"); p != "" {
		parent = &p
	}
	res, err := h.infra.BulkCreate(r.Context(), parent,
		infrastructure.BulkLevel{Type: infrastructure.InfraType(r.FormValue("l1_type")), Names: n1},
		infrastructure.BulkLevel{Type: infrastructure.InfraType(r.FormValue("l2_type")), Names: n2},
		r.FormValue("l2_join") == "1", r.FormValue("l2_sep"))
	if err == nil && parent != nil {
		h.addHistory(r.Context(), "infrastructure", *parent, "update", "", "", "", infraBulkMsg("Sammelanlegen", res), getUser(r).ID)
	}
	target := "/infrastructure"
	if parent != nil {
		target += "?open=" + *parent
	}
	infraRedirect(w, r, target, infraBulkMsg("Sammelanlegen", res), err)
}

// InfraCopyWeb: POST /infrastructure/{id}/copy
func (h *Handler) InfraCopyWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	names, err := namesFromForm(r, "c")
	if err == nil && len(names) == 0 {
		err = errors.New("Name oder „von–bis“ für die Kopie angeben")
	}
	if err != nil {
		infraRedirect(w, r, "/infrastructure", "", err)
		return
	}
	var target *string
	if t := r.FormValue("target_id"); t != "" {
		target = &t
	}
	id := chi.URLParam(r, "id")
	res, err := h.infra.Copy(r.Context(), id, target, names, r.FormValue("children") == "1")
	if err == nil {
		h.addHistory(r.Context(), "infrastructure", id, "update", "", "", "", infraBulkMsg("Kopiert", res), getUser(r).ID)
	}
	dest := "/infrastructure"
	if target != nil {
		dest += "?open=" + *target
	}
	infraRedirect(w, r, dest, infraBulkMsg("Kopiert", res), err)
}
