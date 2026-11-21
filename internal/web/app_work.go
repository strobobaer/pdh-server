package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// „Meine Vorgänge“: offene Arbeit eines Benutzers über Störungen, Tickets,
// Aufgaben, Wartungen (und fürs Startseiten-Widget Rüstzeiten). Gemeinsame
// Grundlage des Widgets „Meine Aufgaben“ (loadListMine) und der PDH-App
// (GET /api/app/v1/work).

// myWorkRow: eine Zeile aus myOpenWork. Number ist leer, wo die Art keine
// eigene Nummer hat (nur Rüstzeiten haben „RZ-…“).
type myWorkRow struct {
	Type, ID, Number, Title, Status, Priority, AssetID, AssetName string
	Due                                                           *time.Time // nur Datum (UTC-Mitternacht)
	UpdatedAt                                                     time.Time
}

// appWorkLimit: so viele Vorgänge liefert die App höchstens.
const appWorkLimit = 100

// myWorkKind: Alias der Tabelle und Bereichsart der Abteilungsbegrenzung je Vorgangsart.
var myWorkKind = map[string]struct{ alias, scopeRef string }{
	"ticket":      {"r", "ticket"},
	"fault":       {"r", "fault"},
	"task":        {"t", "task"}, // myTasksWhere nennt die Tabelle t
	"maintenance": {"r", "maintenance_task"},
	setupRef:      {"s", setupRef},
}

// myOpenWorkSQL: je Art der SELECT (Spalten: k, id, number, title, status,
// priority, asset_id, asset_name, due, updated_at). cond ist die
// Abteilungsbedingung (" AND …", Platzhalter $2/$3) oder leer.
func myOpenWorkSQL(kind, cond string) string {
	const cols = `, COALESCE(i.id::text, ''), COALESCE(i.name, '')`
	switch kind {
	case "ticket":
		return `SELECT 'ticket', r.id::text, ''::text, r.title::text, r.status::text, r.priority::text` + cols + `, r.due_date::date, r.updated_at
			FROM tickets r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status IN ('open','in_progress','pending') AND r.archived_at IS NULL` + cond
	case "fault":
		return `SELECT 'fault', r.id::text, ''::text, r.title::text, r.status::text, r.severity::text` + cols + `, r.due_date::date, r.updated_at
			FROM faults r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status IN ('detected','analyzing','in_progress','pending') AND r.archived_at IS NULL` + cond
	case "task":
		return `SELECT 'task', t.id::text, ''::text, t.title::text, t.status::text, t.priority::text` + cols + `, t.due_date::date, t.updated_at
			FROM tasks t LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
			WHERE ` + myTasksWhere + cond
	case "maintenance":
		return `SELECT 'maintenance', r.id::text, ''::text, r.title::text, r.status::text, r.priority::text` + cols + `, r.due_date::date, r.updated_at
			FROM maintenance_tasks r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status::text IN ('open','in_progress','pending') AND r.archived_at IS NULL` + cond
	case setupRef:
		// laufende Rüstzeiten beim Anlagenverantwortlichen und beim Schichtführer (beendete nicht)
		return `SELECT 'setup', s.id::text, 'RZ-' || lpad(s.number::text, greatest(4, length(s.number::text)), '0'), (` + setupTitleSQL + `)::text, s.status::text, ''::text` + cols + `, NULL::date, s.updated_at
			FROM setup_times s LEFT JOIN infrastructure i ON i.id = s.infrastructure_id
			WHERE (s.responsible_to = $1::uuid OR s.assigned_to = $1::uuid) AND s.status = 'running' AND s.archived_at IS NULL` + cond
	}
	return ""
}

// myOpenWork liefert die offenen Vorgänge von uid der angegebenen Arten
// ("ticket", "fault", "task", "maintenance", setupRef), höchstens limit,
// sortiert nach Fälligkeit (leer zuletzt), Priorität, zuletzt geändert zuerst.
// Es gilt die Abteilungsbegrenzung des Benutzers; Rechte je Art prüft der Aufrufer.
func (h *Handler) myOpenWork(ctx context.Context, uid string, kinds []string, limit int) ([]myWorkRow, error) {
	scope := h.scopeForUserID(ctx, uid)
	args := []any{uid}
	var parts []string
	for _, k := range kinds {
		mk := myWorkKind[k]
		cond, a := scopeSQL(scope, mk.scopeRef, mk.alias, 1)
		if a != nil {
			args = []any{uid, a[0], a[1]} // $2/$3 sind für jede Art gleich
		}
		if q := myOpenWorkSQL(k, cond); q != "" {
			parts = append(parts, q)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT * FROM (`+strings.Join(parts, "\nUNION ALL\n")+`) x(k, id, number, title, status, priority, asset_id, asset_name, due, updated_at)
		ORDER BY (due IS NULL), due,
		  CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
		  updated_at DESC LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []myWorkRow
	for rows.Next() {
		var w myWorkRow
		if err := rows.Scan(&w.Type, &w.ID, &w.Number, &w.Title, &w.Status, &w.Priority, &w.AssetID, &w.AssetName, &w.Due, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// AppWorkAPI: GET /api/app/v1/work – „Meine Vorgänge“ der App. Arten =
// allowed_types des Geräts ∩ Ansichtsrecht des Benutzers (wie das Widget).
func (h *Handler) AppWorkAPI(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	dev := appDevice(r)
	if dev == nil || !h.hasPerm(r, permAppUse) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	view := map[string]string{"fault": "faults.view", "ticket": "tickets.view", "task": "", "maintenance": "maintenance.view"}
	var kinds []string
	for _, k := range []string{"fault", "ticket", "task", "maintenance"} {
		allowed := false
		for _, t := range dev.AllowedTypes {
			allowed = allowed || t == k
		}
		if allowed && h.hasPerm(r, view[k]) {
			kinds = append(kinds, k)
		}
	}
	rows, err := h.myOpenWork(r.Context(), u.ID, kinds, appWorkLimit)
	if err != nil {
		componentLog("app").Error().Err(err).Msg("meine vorgänge")
		appJSONErr(w, http.StatusInternalServerError, "app_unavailable", "app_unavailable")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		it := map[string]any{
			"type": x.Type, "id": x.ID, "number": x.Number, "title": x.Title, "status": x.Status, "priority": x.Priority,
			"asset":      map[string]string{"id": x.AssetID, "name": x.AssetName},
			"due":        nil,
			"updated_at": x.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if x.Due != nil {
			it["due"] = x.Due.Format("2006-01-02")
		}
		items = append(items, it)
	}
	terminalJSON(w, http.StatusOK, map[string]any{"items": items})
}
