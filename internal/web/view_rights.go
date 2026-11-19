package web

import (
	"context"
	"errors"
	"net/http"
)

// Ansichtsrecht für Projekte und KVP-Vorschläge (Migration 183).
//
// Sehen darf einen Vorgang, wer das Ansichtsrecht für die Abteilung des
// Datensatzes hat (Bereichsadministrator eingeschlossen, das erledigt
// rbac.Scope) oder am Vorgang beteiligt ist. "Bearbeiten" (projects.edit) und
// "Steuern" (kvp.manage) schließen das Ansehen nicht ein.
//
// Fehler beim Laden der Abteilungen oder der Beteiligung führen immer zu
// "nicht sichtbar" (nie zu "ohne Abteilung = für alle").

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

var errViewNoDB = errors.New("keine datenbank")

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
	if !ok || h.db == nil || uid == "" || !bareUUIDRe.MatchString(id) {
		return false
	}
	var yes bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` r WHERE r.id = $2::uuid AND (`+cond+`))`, uid, id).Scan(&yes); err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("beteiligung prüfen")
		return false
	}
	return yes
}

// involvedIDs: alle Projekte bzw. Vorschläge, an denen die Person beteiligt ist
// (bei einem Fehler: leer).
func (h *Handler) involvedIDs(ctx context.Context, uid, refType string) map[string]bool {
	out, err := h.involvedIDsErr(ctx, uid, refType)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("beteiligte vorgänge laden")
		return map[string]bool{}
	}
	return out
}

func (h *Handler) involvedIDsErr(ctx context.Context, uid, refType string) (map[string]bool, error) {
	out := map[string]bool{}
	table, cond, ok := involvedSource(refType)
	if !ok || uid == "" {
		return out, nil
	}
	if h.db == nil {
		return out, errViewNoDB
	}
	rows, err := h.db.Query(ctx, `SELECT r.id::text FROM `+table+` r WHERE `+cond, uid)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// viewDepartments: Abteilungen eines Datensatzes – anders als
// recordDepartments mit Fehler: ein Fehler darf nie als "keine Abteilung"
// (= für alle sichtbar) gelten.
func (h *Handler) viewDepartments(ctx context.Context, refType, id string) ([]string, error) {
	if h.viewDeptErr != nil {
		return nil, h.viewDeptErr
	}
	src, ok := recordDeptSource[refAlias(refType)]
	if !ok {
		return nil, nil
	}
	if h.db == nil {
		return nil, errViewNoDB
	}
	q := `SELECT idp.department_id::text FROM ` + src.table + ` r JOIN infrastructure_department idp ON idp.infrastructure_id = r.infrastructure_id WHERE r.id = $1::uuid`
	if src.group {
		q += ` UNION SELECT g.department_id::text FROM ` + src.table + ` r JOIN user_groups g ON g.id = r.assigned_group_id WHERE r.id = $1::uuid AND g.department_id IS NOT NULL`
	}
	rows, err := h.db.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		if d != "" {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// allRecordDepartments: Abteilungen aller Datensätze eines Typs (wie
// viewDepartments, aber in einer Abfrage); Datensätze ohne Abteilung fehlen.
func (h *Handler) allRecordDepartments(ctx context.Context, refType string) (map[string][]string, error) {
	if h.viewDeptErr != nil {
		return nil, h.viewDeptErr
	}
	out := map[string][]string{}
	src, ok := recordDeptSource[refAlias(refType)]
	if !ok {
		return out, nil
	}
	if h.db == nil {
		return nil, errViewNoDB
	}
	q := `SELECT r.id::text, idp.department_id::text FROM ` + src.table + ` r JOIN infrastructure_department idp ON idp.infrastructure_id = r.infrastructure_id`
	if src.group {
		q += ` UNION SELECT r.id::text, g.department_id::text FROM ` + src.table + ` r JOIN user_groups g ON g.id = r.assigned_group_id WHERE g.department_id IS NOT NULL`
	}
	rows, err := h.db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, d string
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		if d != "" {
			out[id] = append(out[id], d)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// viewMode: wie das Recht ausgewertet wird. Ohne rbac gilt nur in Tests mit
// hasPermHook: dann "überall", wenn der Hook das Recht gibt.
func (h *Handler) viewMode(r *http.Request, perm string) (all bool, depts []string, useRBAC bool) {
	if h.rbac == nil {
		return h.hasPermHook != nil && h.hasPerm(r, perm), nil, false
	}
	u := getUser(r)
	if u == nil || u.ID == "" || (h.hasPermHook != nil && !h.hasPerm(r, perm)) {
		return false, nil, true
	}
	all, depts = h.rbac.Scope(u.ID, string(u.Role), perm)
	return all, depts, true
}

// canViewRec: darf die angemeldete Person diesen Datensatz sehen? Typen ohne
// Ansichtsrecht: immer ja. Ohne angemeldeten Benutzer: nein.
func (h *Handler) canViewRec(r *http.Request, refType, id string) bool {
	refType = refAlias(refType)
	perm, ok := viewRights[refType]
	if !ok {
		return true
	}
	if !bareUUIDRe.MatchString(id) {
		return false
	}
	u := getUser(r)
	if u == nil || u.ID == "" {
		return false
	}
	if all, depts, _ := h.viewMode(r, perm); all {
		return true
	} else if len(depts) > 0 {
		if rd, err := h.viewDepartments(r.Context(), refType, id); err != nil {
			componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("abteilung des datensatzes laden")
		} else if h.hasPermIn(r, perm, rd) {
			return true
		}
	}
	return h.involvedIn(r.Context(), u.ID, refType, id)
}

// mayViewAny: hat die Person das Ansichtsrecht irgendwo (überall oder in
// mindestens einer Abteilung)? Für Menüeinträge und Bausteine.
func (h *Handler) mayViewAny(r *http.Request, refType string) bool {
	perm, ok := viewRights[refAlias(refType)]
	if !ok {
		return true
	}
	all, depts, _ := h.viewMode(r, perm)
	return all || len(depts) > 0
}

// viewFilter: Listenfilter "darf diese ID gesehen werden" – gleiches Ergebnis
// wie canViewRec, aber mit einer Abfrage je Liste statt je Eintrag. Bei Recht
// "überall" ohne Abfrage immer wahr. Bei Fehlern zählt nur die Beteiligung.
func (h *Handler) viewFilter(r *http.Request, refType string) func(id string) bool {
	refType = refAlias(refType)
	perm, ok := viewRights[refType]
	if !ok {
		return func(string) bool { return true }
	}
	u := getUser(r)
	if u == nil || u.ID == "" {
		return func(string) bool { return false }
	}
	all, depts, _ := h.viewMode(r, perm)
	if all {
		return func(string) bool { return true }
	}
	ctx := r.Context()
	involved := h.involvedIDs(ctx, u.ID, refType)
	if len(depts) == 0 {
		return func(id string) bool { return involved[id] }
	}
	recDepts, err := h.allRecordDepartments(ctx, refType)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", refType).Msg("abteilungen der datensätze laden")
		return func(id string) bool { return involved[id] }
	}
	return func(id string) bool {
		return involved[id] || h.hasPermIn(r, perm, recDepts[id])
	}
}

// viewsAll: hat die Person das Ansichtsrecht überall (dann gibt es nichts zu
// filtern)? Typen ohne Ansichtsrecht: immer ja.
func (h *Handler) viewsAll(r *http.Request, refType string) bool {
	perm, ok := viewRights[refAlias(refType)]
	if !ok {
		return true
	}
	all, _, _ := h.viewMode(r, perm)
	return all
}

// hiddenCategoryCounts: je Kategorie die Zahl der Datensätze des Typs, die die
// Person nicht sehen darf. nil, wenn nichts zu verstecken ist (Typ ohne
// Ansichtsrecht oder Recht überall). Damit zeigen Zähler nie mehr als die Liste.
func (h *Handler) hiddenCategoryCounts(r *http.Request, module string) map[string]int {
	if h.viewsAll(r, module) || h.db == nil {
		return nil
	}
	canView := h.viewFilter(r, module)
	rows, err := h.db.Query(r.Context(), `SELECT category_id::text, record_id::text FROM record_categories WHERE module = $1`, module)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", module).Msg("kategorie-zähler laden")
		return nil
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var cat, rec string
		if rows.Scan(&cat, &rec) == nil && !canView(rec) {
			out[cat]++
		}
	}
	return out
}
