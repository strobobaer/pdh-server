package web

import (
	"context"
	"net/http"
)

// Ansichtsrecht für Projekte und KVP-Vorschläge (Migration 183).
//
// Sehen darf einen Vorgang, wer das Ansichtsrecht für die Abteilung des
// Datensatzes hat (Bereichsadministrator eingeschlossen, das erledigt
// rbac.Scope) oder am Vorgang beteiligt ist. "Bearbeiten" (projects.edit) und
// "Steuern" (kvp.manage) schließen das Ansehen nicht ein.

const (
	permProjectsView = "projects.view"
	permKVPView      = "kvp.view"
)

// viewRights: Datensatz-Typen mit Ansichtsrecht. Alle anderen Typen sind von
// dieser Prüfung nicht betroffen.
var viewRights = map[string]string{"project": permProjectsView, "kvp": permKVPView}

// kvpPeopleCond: Beteiligung von $1 (Benutzer-ID) an einem Vorschlag r –
// Einreicher, Ersteller, Prüfer, Verantwortlicher, Mitwirkende und
// Verantwortliche einer Maßnahme. Bewusst nicht in scopeTables: das würde die
// Abteilungssperre der Rolle auch für KVP einschalten.
const kvpPeopleCond = `r.submitter_id = $1::uuid OR r.created_by = $1::uuid OR r.reviewer_id = $1::uuid OR r.responsible_to = $1::uuid` +
	` OR EXISTS (SELECT 1 FROM kvp_members m WHERE m.idea_id = r.id AND m.user_id = $1::uuid)` +
	` OR EXISTS (SELECT 1 FROM kvp_actions a WHERE a.idea_id = r.id AND a.responsible_id = $1::uuid)`

// involvedSource: Tabelle und Beteiligungs-Bedingung eines Typs mit Ansichtsrecht.
func involvedSource(refType string) (table, cond string, ok bool) {
	switch refAlias(refType) {
	case "project":
		return scopeTables["project"].table, scopeTables["project"].people, true
	case "kvp":
		return "kvp_ideas", kvpPeopleCond, true
	}
	return "", "", false
}

// involvedIn: ist die Person (aktueller Stand) am Projekt bzw. Vorschlag beteiligt?
func (h *Handler) involvedIn(ctx context.Context, uid, refType, id string) bool {
	table, cond, ok := involvedSource(refType)
	if !ok || h.db == nil || uid == "" || id == "" {
		return false
	}
	var yes bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` r WHERE r.id = $2::uuid AND (`+cond+`))`, uid, id).Scan(&yes); err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("beteiligung prüfen")
		return false
	}
	return yes
}

// involvedIDs: alle Projekte bzw. Vorschläge, an denen die Person beteiligt ist.
func (h *Handler) involvedIDs(ctx context.Context, uid, refType string) map[string]bool {
	out := map[string]bool{}
	table, cond, ok := involvedSource(refType)
	if !ok || h.db == nil || uid == "" {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT r.id::text FROM `+table+` r WHERE `+cond, uid)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("beteiligte vorgänge laden")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// canViewRec: darf die angemeldete Person diesen Datensatz sehen? Typen ohne
// Ansichtsrecht: immer ja. Ohne angemeldeten Benutzer: nein.
func (h *Handler) canViewRec(r *http.Request, refType, id string) bool {
	refType = refAlias(refType)
	perm, ok := viewRights[refType]
	if !ok {
		return true
	}
	u := getUser(r)
	if u == nil || u.ID == "" || h.rbac == nil {
		return false
	}
	return h.hasPermRec(r, perm, refType, id) || h.involvedIn(r.Context(), u.ID, refType, id)
}

// mayViewAny: hat die Person das Ansichtsrecht irgendwo (überall oder in
// mindestens einer Abteilung)? Für Menüeinträge und Bausteine.
func (h *Handler) mayViewAny(r *http.Request, refType string) bool {
	perm, ok := viewRights[refAlias(refType)]
	if !ok {
		return true
	}
	u := getUser(r)
	if u == nil || u.ID == "" || h.rbac == nil {
		return false
	}
	all, depts := h.rbac.Scope(u.ID, string(u.Role), perm)
	return all || len(depts) > 0
}

// viewFilter: Listenfilter "darf diese ID gesehen werden" – gleiches Ergebnis
// wie canViewRec, aber mit einer Abfrage je Liste statt je Eintrag. Bei Recht
// "überall" ohne Abfrage immer wahr.
func (h *Handler) viewFilter(r *http.Request, refType string) func(id string) bool {
	refType = refAlias(refType)
	perm, ok := viewRights[refType]
	if !ok {
		return func(string) bool { return true }
	}
	u := getUser(r)
	if u == nil || u.ID == "" || h.rbac == nil {
		return func(string) bool { return false }
	}
	all, depts := h.rbac.Scope(u.ID, string(u.Role), perm)
	if all {
		return func(string) bool { return true }
	}
	ctx := r.Context()
	involved := h.involvedIDs(ctx, u.ID, refType)
	if len(depts) == 0 {
		return func(id string) bool { return involved[id] }
	}
	recDepts := h.allRecordDepartments(ctx, refType)
	return func(id string) bool {
		return involved[id] || h.hasPermIn(r, perm, recDepts[id])
	}
}

// allRecordDepartments: Abteilungen aller Datensätze eines Typs (wie
// recordDepartments, aber in einer Abfrage); Datensätze ohne Abteilung fehlen.
func (h *Handler) allRecordDepartments(ctx context.Context, refType string) map[string][]string {
	out := map[string][]string{}
	src, ok := recordDeptSource[refAlias(refType)]
	if !ok || h.db == nil {
		return out
	}
	q := `SELECT r.id::text, idp.department_id::text FROM ` + src.table + ` r JOIN infrastructure_department idp ON idp.infrastructure_id = r.infrastructure_id`
	if src.group {
		q += ` UNION SELECT r.id::text, g.department_id::text FROM ` + src.table + ` r JOIN user_groups g ON g.id = r.assigned_group_id WHERE g.department_id IS NOT NULL`
	}
	rows, err := h.db.Query(ctx, q)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("abteilungen der datensätze laden")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, d string
		if rows.Scan(&id, &d) == nil && d != "" {
			out[id] = append(out[id], d)
		}
	}
	return out
}
