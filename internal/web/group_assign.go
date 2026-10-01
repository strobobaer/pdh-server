package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Zuweisung an eine Gruppe (migrations/085): Mitglieder sehen den Vorgang
// unter "Mir zugewiesen" und bekommen die Aenderungshinweise.

// groupKeep: Platzhalter der Gruppenauswahl, solange die Optionen noch laden –
// der Server laesst die Zuweisung dann unveraendert.
const groupKeep = "__keep__"

// myGroupIDs: SQL-Teilabfrage der Gruppen von $1 (Benutzer-ID).
const myGroupIDs = `(SELECT gm.group_id FROM user_group_members gm WHERE gm.user_id = $1::uuid)`

func groupTable(refType string) (string, bool) {
	switch refType {
	case "ticket":
		return "tickets", true
	case "fault":
		return "faults", true
	case "maintenance_task":
		return "maintenance_tasks", true
	case "maintenance_plan":
		return "maintenance_plans", true
	case "task":
		return "tasks", true
	case "project":
		return "projects", true
	}
	return "", false
}

// groupOptionsHTML: <option>-Liste aller Gruppen, selected = aktuelle Gruppe.
func (h *Handler) groupOptionsHTML(ctx context.Context, selected string) string {
	var b strings.Builder
	b.WriteString(`<option value="">– keine Gruppe –</option>`)
	for _, g := range h.loadGroups(ctx) {
		sel := ""
		if g.ID == selected {
			sel = " selected"
		}
		label := g.Name
		if g.DepartmentName != "" {
			label += " (" + g.DepartmentName + ")"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(g.ID), sel, esc(label))
	}
	return b.String()
}

func (h *Handler) recordGroupID(ctx context.Context, table, id string) string {
	var gid *string
	_ = h.db.QueryRow(ctx, fmt.Sprintf(`SELECT assigned_group_id::text FROM %s WHERE id = $1::uuid`, table), id).Scan(&gid)
	if gid == nil {
		return ""
	}
	return *gid
}

// RecordGroupOptionsWeb: GET /records/{refType}/{id}/group-options
// ("new" als ID liefert die Liste ohne Vorauswahl).
func (h *Handler) RecordGroupOptionsWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	table, ok := groupTable(refType)
	if !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	selected := ""
	if id != "new" {
		selected = h.recordGroupID(r.Context(), table, id)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h.groupOptionsHTML(r.Context(), selected)))
}

// setRecordGroup setzt die Gruppe, sofern das Formular sie (geladen) mitschickt.
func (h *Handler) setRecordGroup(r *http.Request, refType, id string) error {
	vals, present := r.Form["assigned_group_id"]
	if !present || len(vals) == 0 || vals[0] == groupKeep {
		return nil
	}
	table, ok := groupTable(refType)
	if !ok {
		return nil
	}
	ctx := r.Context()
	old := h.recordGroupID(ctx, table, id)
	gid := strings.TrimSpace(vals[0])
	if old == gid {
		return nil
	}
	if _, err := h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET assigned_group_id = $1 WHERE id = $2::uuid`, table), nullID(gid), id); err != nil {
		return err
	}
	if refType != "maintenance_plan" {
		h.addHistory(ctx, refType, id, "people", "assigned_group_id", old, gid, "Zugewiesene Gruppe geändert", getUser(r).ID)
	}
	return nil
}

// RecordGroupWeb: PUT /records/{refType}/{id}/group – nur die Gruppe setzen
// (z. B. Aufgaben, deren Zuständige separat gepflegt werden).
func (h *Handler) RecordGroupWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	if _, ok := groupTable(refType); !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	r.ParseForm()
	if err := h.setRecordGroup(r, refType, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gruppe gespeichert</span>`)
}
