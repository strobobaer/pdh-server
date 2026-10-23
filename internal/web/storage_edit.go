package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/storage"
)

// Lagerverwaltung → Bearbeiten (Name, Art, Lage im Baum) und Löschen eines
// Knotens (storage/structure.go). Antworten für htmx: Erfolg = 204 bzw. 200 mit
// Meldung (die Seite lädt neu), Fehler = 4xx mit Text für das Formular.

func storageFormError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<span style="color:var(--red)"><i class="ti ti-alert-circle"></i> %s</span>`, esc(msg))
}

// StorageEditWeb: POST /storage/{id}/edit-web (name, description, capacity, location, type, parent_id)
func (h *Handler) StorageEditWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditStorage(r) {
		storageFormError(w, http.StatusForbidden, "Keine Berechtigung (Ersatzteile bearbeiten)")
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	old, err := h.storage.GetSubtree(ctx, id)
	if err != nil || old == nil {
		storageFormError(w, http.StatusNotFound, "Knoten nicht gefunden")
		return
	}
	in := storage.StructureInput{
		Name: r.FormValue("name"), Description: strings.TrimSpace(r.FormValue("description")),
		Location: strings.TrimSpace(r.FormValue("location")), Capacity: strings.TrimSpace(r.FormValue("capacity")),
		Type: storage.NodeType(r.FormValue("type")),
	}
	if in.Type == "" {
		in.Type = old.Type
	}
	if _, sent := r.Form["parent_id"]; !sent {
		in.ParentID = old.ParentID // Formular ohne „Liegt in“: Lage unverändert
	} else if p := r.FormValue("parent_id"); p != "" {
		in.ParentID = &p
	}
	if err := h.storage.ChangeStructure(ctx, id, in); err != nil {
		storageFormError(w, http.StatusBadRequest, err.Error())
		return
	}
	var notes []string
	if in.Type != old.Type {
		notes = append(notes, fmt.Sprintf("Art: %s → %s", storageTypeLabels[old.Type], storageTypeLabels[in.Type]))
	}
	oldParent, newParent := "", ""
	if old.ParentID != nil {
		oldParent = *old.ParentID
	}
	if in.ParentID != nil {
		newParent = *in.ParentID
	}
	if oldParent != newParent {
		where := "ganz oben"
		if n, err := h.storage.GetSubtree(ctx, newParent); err == nil && n != nil {
			where = "in „" + n.Name + "“"
		}
		notes = append(notes, "verschoben "+where)
	}
	if strings.TrimSpace(in.Name) != old.Name {
		notes = append(notes, fmt.Sprintf("umbenannt: %s → %s", old.Name, strings.TrimSpace(in.Name)))
	}
	msg := "bearbeitet"
	if len(notes) > 0 {
		msg = strings.Join(notes, ", ")
	}
	h.addHistory(ctx, "storage", id, "update", "", "", "", msg, getUser(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

// StorageDeleteWeb: POST /storage/{id}/delete-web – löschen (ohne Bestand) bzw.
// ausblenden, wenn Buchungen darauf verweisen.
func (h *Handler) StorageDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditStorage(r) {
		storageFormError(w, http.StatusForbidden, "Keine Berechtigung (Ersatzteile bearbeiten)")
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	res, stock, err := h.storage.DeleteNode(ctx, id)
	if len(stock) > 0 {
		var parts []string
		for _, s := range stock {
			parts = append(parts, s.Part+" an „"+s.Node+"“")
		}
		storageFormError(w, http.StatusConflict, "Nicht gelöscht – hier liegt noch Bestand: "+strings.Join(parts, ", ")+
			". Zuerst umlagern oder ausbuchen.")
		return
	}
	if err != nil {
		storageFormError(w, http.StatusBadRequest, err.Error())
		return
	}
	what := fmt.Sprintf("„%s“ gelöscht", res.Name)
	if res.Nodes > 1 {
		what = fmt.Sprintf("„%s“ mit %d Unterknoten gelöscht", res.Name, res.Nodes-1)
	}
	if !res.Deleted {
		what = fmt.Sprintf("„%s“ ausgeblendet – alte Buchungen verweisen noch darauf", res.Name)
	}
	h.addHistory(ctx, "storage", id, "delete", "", "", "", what, getUser(r).ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span style="color:var(--green)"><i class="ti ti-check"></i> %s</span>`, esc(what))
}
