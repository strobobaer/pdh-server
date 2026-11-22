package web

import (
	"net/http"
)

// appAssetsMax: so viele Anlagen liefert GET /api/app/v1/assets höchstens (Variable,
// damit der Test die Grenze klein setzen kann).
var appAssetsMax = 5000

// AppAssetsAPI: GET /api/app/v1/assets – Anlagen für den Offline-Speicher der App
// (Auswahl beim Melden einer Störung ohne Verbindung). Es kommen nur sichtbare
// Anlagen im Abteilungsbereich des Benutzers (Anlagen ohne Abteilung für alle,
// wie appInfraInScope) und nur, wenn das Gerät samt Benutzer melden darf; sonst bleibt
// die Liste leer. Der Pfad ist derselbe wie bei /scan (assetPathExpr: Wurzel › … › Name).
// Eine einzige Abfrage: der Pfad wird von der Wurzel her aufgebaut, der Bereich über die
// wirksame Abteilung (infrastructure_department) einmal für alle Zeilen geprüft.
func (h *Handler) AppAssetsAPI(w http.ResponseWriter, r *http.Request) {
	items := []map[string]string{}
	truncated := false
	if h.appMayReport(r) {
		scopeAll, deps := true, []string{}
		if s := h.requestScope(r); s != nil {
			scopeAll, deps = false, s.Departments
		}
		rows, err := h.db.Query(r.Context(), `
			WITH RECURSIVE tree AS (
				SELECT id, name::text AS name, name::text AS path, hidden_at FROM infrastructure WHERE parent_id IS NULL
				UNION ALL
				SELECT c.id, c.name::text, tree.path || ' › ' || c.name, c.hidden_at
				  FROM infrastructure c JOIN tree ON c.parent_id = tree.id
			)
			SELECT t.id::text, t.name, t.path FROM tree t
			 WHERE t.hidden_at IS NULL
			   AND ($1::bool
			        OR NOT EXISTS (SELECT 1 FROM infrastructure_department d WHERE d.infrastructure_id = t.id)
			        OR t.id IN (SELECT d.infrastructure_id FROM infrastructure_department d WHERE d.department_id::text = ANY($2)))
			 ORDER BY t.path, t.name, t.id
			 LIMIT $3`, scopeAll, deps, appAssetsMax+1)
		if err != nil {
			appUnavailable(w, err, "assets", "", "anlagenliste laden")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, path string
			if err := rows.Scan(&id, &name, &path); err != nil {
				appUnavailable(w, err, "assets", "", "anlagenliste lesen")
				return
			}
			items = append(items, map[string]string{"id": id, "name": name, "path": path})
		}
		if err := rows.Err(); err != nil {
			appUnavailable(w, err, "assets", "", "anlagenliste lesen")
			return
		}
		if len(items) > appAssetsMax {
			items, truncated = items[:appAssetsMax], true
		}
	}
	terminalJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}
