package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/namerange"
	"pdh/internal/core/storage"
)

// Lagerverwaltung → „Sammelanlegen“ (Ziel + Ebene 1 von–bis + optional Ebene 2
// von–bis) und Kopieren eines Knotens samt Unterknoten (storage/bulk.go).

// storageTarget: Auswahl „Ziel“ in den Dialogen (alles außer Plätzen).
type storageTarget struct {
	ID, Path, Type string
	Depth          int
}

var storageDepth = map[string]int{"lagerort": 0, "regal": 1, "fach": 2, "platz": 3}

func storageTargets(tree []StorageNodeView) []storageTarget {
	var out []storageTarget
	var walk func(ns []StorageNodeView, prefix string)
	walk = func(ns []StorageNodeView, prefix string) {
		for _, n := range ns {
			p := n.Name
			if prefix != "" {
				p = prefix + " › " + n.Name
			}
			if n.Type != "platz" {
				out = append(out, storageTarget{ID: n.ID, Path: p, Type: n.Type, Depth: storageDepth[n.Type]})
			}
			walk(n.Children, p)
		}
	}
	walk(tree, "")
	return out
}

func (h *Handler) canEditStorage(r *http.Request) bool { return h.hasPerm(r, "inventory.edit") }

func storageBack(w http.ResponseWriter, r *http.Request, msg string, err error) {
	q := url.Values{}
	if err != nil {
		q.Set("err", err.Error())
	} else {
		q.Set("msg", msg)
	}
	http.Redirect(w, r, "/storage?"+q.Encode(), http.StatusSeeOther)
}

func rangeFromForm(r *http.Request, key string) namerange.Spec {
	return namerange.Spec{Prefix: r.FormValue(key + "_prefix"), From: r.FormValue(key + "_from"), To: r.FormValue(key + "_to"), Suffix: r.FormValue(key + "_suffix")}
}

// namesFromForm: Reihe aus Präfix/von/bis; ohne „von“ ist das Präfix der Name.
func namesFromForm(r *http.Request, key string) ([]string, error) {
	s := rangeFromForm(r, key)
	if s.Empty() {
		if name := strings.TrimSpace(s.Prefix + s.Suffix); name != "" {
			return []string{name}, nil
		}
		return nil, nil
	}
	return s.Expand()
}

func bulkMsg(what string, res storage.BulkResult) string {
	msg := fmt.Sprintf("%s: %d angelegt", what, res.Created)
	if res.Skipped > 0 {
		msg += fmt.Sprintf(", %d gab es schon", res.Skipped)
	}
	return msg
}

// StorageBulkWeb: POST /storage/bulk
func (h *Handler) StorageBulkWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditStorage(r) {
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
		storageBack(w, r, "", err)
		return
	}
	res, err := h.storage.BulkCreate(r.Context(), r.FormValue("parent_id"),
		storage.BulkLevel{Type: storage.NodeType(r.FormValue("l1_type")), Names: n1},
		storage.BulkLevel{Type: storage.NodeType(r.FormValue("l2_type")), Names: n2},
		r.FormValue("l2_join"), r.FormValue("l2_sep"), getUser(r).ID)
	if err == nil {
		h.addHistory(r.Context(), "storage", r.FormValue("parent_id"), "update", "", "", "", bulkMsg("Sammelanlegen", res), getUser(r).ID)
	}
	storageBack(w, r, bulkMsg("Sammelanlegen", res), err)
}

// StorageCopyWeb: POST /storage/{id}/copy
func (h *Handler) StorageCopyWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditStorage(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	names, err := namesFromForm(r, "c")
	if err == nil && len(names) == 0 {
		err = errors.New("Name oder „von–bis“ für die Kopie angeben")
	}
	if err != nil {
		storageBack(w, r, "", err)
		return
	}
	var target *string
	if t := r.FormValue("target_id"); t != "" {
		target = &t
	}
	res, err := h.storage.Copy(r.Context(), chi.URLParam(r, "id"), target, names, r.FormValue("children") == "1", getUser(r).ID)
	storageBack(w, r, bulkMsg("Kopiert", res), err)
}
