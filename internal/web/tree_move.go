package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/core/storage"
)

// Bäume per Ziehen ordnen (Entwurf docs/superpowers/specs/2026-11-26-baum-ziehen-design.md,
// Teil A): eine Anlage oder einen Lagerort unter einen anderen Knoten hängen. Das Skript
// pdh-tree-drag.js fragt vorher nach; der Server prüft Recht, Schleifen und den Stand neu.
//
//	parent = neuer Elternknoten ("" = oberste Ebene)
//	from   = Elternknoten, den das Skript gesehen hat ("" = oberste Ebene) – weicht er ab,
//	         hat jemand anderes den Knoten inzwischen verschoben (409).
//
// Antwort 200 als JSON {"message": …, "from": …} – „from“ ist der alte Elternknoten (für
// „Rückgängig“); Fehler als Text mit 400/403/404/409.

type treeMoveError struct {
	status int
	msg    string
}

func (e *treeMoveError) Error() string { return e.msg }

func treeMoveFail(status int, msg string) error { return &treeMoveError{status, msg} }

func writeTreeMove(w http.ResponseWriter, msg, from string, err error) {
	var te *treeMoveError
	if errors.As(err, &te) {
		http.Error(w, te.msg, te.status)
		return
	}
	if err != nil {
		componentLog("baum").Error().Err(err).Msg("verschieben")
		http.Error(w, "Nicht verschoben – bitte erneut versuchen", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg, "from": from})
}

// treePlace: Ort im Baum für Meldungen („„Werk › Halle 2““ bzw. „ganz oben“).
func treePlace(path string) string {
	if path == "" {
		return "ganz oben"
	}
	return "„" + path + "“"
}

// InfraMoveWeb: POST /infrastructure/{id}/move – Recht infrastructure.edit am Knoten und am
// Ziel (je mit Abteilung), auf die oberste Ebene nur mit dem Recht überall.
func (h *Handler) InfraMoveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	parent, from := strings.TrimSpace(r.FormValue("parent")), strings.TrimSpace(r.FormValue("from"))
	msg, err := h.infraMove(r, id, parent, from)
	writeTreeMove(w, msg, from, err)
}

func (h *Handler) infraMove(r *http.Request, id, parent, from string) (string, error) {
	const perm = "infrastructure.edit"
	ctx := r.Context()
	if !h.hasPermRec(r, perm, "infrastructure", id) {
		return "", treeMoveFail(http.StatusForbidden, "Keine Berechtigung, diese Anlage zu verschieben")
	}
	if parent == "" {
		u := getUser(r)
		if all, _ := h.rbac.Scope(u.ID, string(u.Role), perm); !all {
			return "", treeMoveFail(http.StatusForbidden, "Auf die oberste Ebene darf nur, wer die Infrastruktur überall bearbeiten darf")
		}
	} else if !h.hasPermRec(r, perm, "infrastructure", parent) {
		return "", treeMoveFail(http.StatusForbidden, "Keine Berechtigung für das Ziel")
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var cur, name string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(parent_id::text, ''), name FROM infrastructure WHERE id::text = $1 FOR UPDATE`, id).Scan(&cur, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", treeMoveFail(http.StatusNotFound, "Anlage nicht gefunden")
		}
		return "", err
	}
	if cur != from {
		return "", treeMoveFail(http.StatusConflict, "Inzwischen geändert – jemand hat „"+name+"“ schon verschoben. Bitte die Seite neu laden.")
	}
	if cur == parent {
		return "„" + name + "“ liegt schon dort.", nil
	}
	if parent != "" {
		var exists, loop bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id::text = $1)`, parent).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", treeMoveFail(http.StatusNotFound, "Ziel nicht gefunden")
		}
		if err := tx.QueryRow(ctx, `WITH RECURSIVE sub AS (
			SELECT id FROM infrastructure WHERE id::text = $1
			UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id)
			SELECT EXISTS (SELECT 1 FROM sub WHERE id::text = $2)`, id, parent).Scan(&loop); err != nil {
			return "", err
		}
		if loop {
			return "", treeMoveFail(http.StatusBadRequest, "„"+name+"“ kann nicht unter sich selbst oder eine eigene Unteranlage")
		}
	}
	if cur == "" {
		// der einzige oberste Knoten (Firmenstammdaten) bleibt oben
		var roots int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM infrastructure WHERE parent_id IS NULL`).Scan(&roots); err != nil {
			return "", err
		}
		if roots <= 1 {
			return "", treeMoveFail(http.StatusBadRequest, "Der oberste Knoten bleibt oben")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE infrastructure SET parent_id = NULLIF($2, '')::uuid, updated_at = NOW() WHERE id::text = $1`, id, parent); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	oldPath, newPath := h.infraPath(ctx, cur), h.infraPath(ctx, parent)
	msg := fmt.Sprintf("„%s“ verschoben von %s nach %s", name, treePlace(oldPath), treePlace(newPath))
	h.addHistory(ctx, "infrastructure", id, "update", "parent_id", oldPath, newPath, msg, getUser(r).ID)
	return msg, nil
}

// StorageMoveWeb: POST /storage/{id}/move – Recht inventory.edit (das Lager hat keine
// Abteilungen); Ebenen und Schleifen prüft storage.ChangeStructure.
func (h *Handler) StorageMoveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	parent, from := strings.TrimSpace(r.FormValue("parent")), strings.TrimSpace(r.FormValue("from"))
	msg, err := h.storageMove(r, id, parent, from)
	writeTreeMove(w, msg, from, err)
}

func (h *Handler) storageMove(r *http.Request, id, parent, from string) (string, error) {
	ctx := r.Context()
	if !h.canEditStorage(r) {
		return "", treeMoveFail(http.StatusForbidden, "Keine Berechtigung (Lager bearbeiten)")
	}
	old, err := h.storage.GetSubtree(ctx, id)
	if err != nil || old == nil {
		return "", treeMoveFail(http.StatusNotFound, "Lagerort nicht gefunden")
	}
	cur := ""
	if old.ParentID != nil {
		cur = *old.ParentID
	}
	if cur != from {
		return "", treeMoveFail(http.StatusConflict, "Inzwischen geändert – jemand hat „"+old.Name+"“ schon verschoben. Bitte die Seite neu laden.")
	}
	if cur == parent {
		return "„" + old.Name + "“ liegt schon dort.", nil
	}
	in := storage.StructureInput{Name: old.Name, Description: old.Description, Location: old.Location, Capacity: old.Capacity, Type: old.Type}
	if parent != "" {
		in.ParentID = &parent
	}
	if err := h.storage.ChangeStructure(ctx, id, in); err != nil {
		if strings.Contains(err.Error(), "nicht gefunden") {
			return "", treeMoveFail(http.StatusNotFound, err.Error())
		}
		return "", treeMoveFail(http.StatusBadRequest, err.Error())
	}
	name := func(nid string) string {
		if nid == "" {
			return ""
		}
		if n, err := h.storage.GetSubtree(ctx, nid); err == nil && n != nil {
			return n.Name
		}
		return ""
	}
	oldName, newName := name(cur), name(parent)
	msg := fmt.Sprintf("„%s“ verschoben von %s nach %s", old.Name, treePlace(oldName), treePlace(newName))
	h.addHistory(ctx, "storage", id, "update", "parent_id", oldName, newName, msg, getUser(r).ID)
	return msg, nil
}
