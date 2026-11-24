package web

// Personenauswahl am Terminal (0.82.50): Wer am Terminal gewählt werden kann (Vorgänge, Vorgangsleiste), ist die
// Vereinigung mehrerer Quellen. Dieselbe Menge ist die Menge der erlaubten Zuweisungen – der Server prüft sie bei
// jedem Schreibweg selbst.
//
//   - die Abteilungen des Terminals (terminal_departments.go),
//   - die eingestellten Quellen des Terminal-Benutzers (Stammdaten, Abschnitt „Freier Terminal-Benutzer“):
//     Gruppen, Abteilungen und einzelne Personen (Tabelle terminal_pick_sources, Migration 214),
//   - die Inhaber des Rechts „Am Terminal auswählbar“ (terminal.selectable, abteilungsbezogen: gilt an Terminals,
//     deren Anlage zur Abteilung des Rechts gehört),
//   - immer die Personen der Abteilung „Instandhaltung“.
//
// Es zählen nur aktive Mitarbeitende – keine Systembenutzer, keine Bots, keine Betrachter. „Auswählbar“ ändert
// nichts an den Rechten der Person: terminal.selectable wird sonst nirgends gelesen.

import (
	"context"
	"strings"
)

// terminalPermSelectable: Recht „Am Terminal auswählbar“.
const terminalPermSelectable = "terminal.selectable"

// terminalDepartmentPeople: die am Terminal wählbaren Personen (zugleich die erlaubten Zuweisungen), nach Name.
func (h *Handler) terminalDepartmentPeople(ctx context.Context, termUserID string) ([]terminalPerson, error) {
	return h.terminalPickPeople(ctx, termUserID)
}

// terminalPickPeople: siehe Kopf der Datei.
func (h *Handler) terminalPickPeople(ctx context.Context, termUserID string) ([]terminalPerson, error) {
	if h.db == nil || termUserID == "" {
		return nil, nil
	}
	depts, err := h.terminalDepartments(ctx, termUserID)
	if err != nil {
		return nil, err
	}
	selectable, err := h.terminalSelectableHolders(ctx, termUserID)
	if err != nil {
		return nil, err
	}
	if depts == nil {
		depts = []string{}
	}
	if selectable == nil {
		selectable = []string{}
	}
	rows, err := h.db.Query(ctx, `SELECT u.id::text, TRIM(u.first_name || ' ' || u.last_name), COALESCE(u.department, '')
		FROM users u
		WHERE u.active AND NOT u.is_bot AND NOT u.is_system_user AND u.role::text <> 'viewer'
		AND (
			u.department_id = ANY($1::uuid[])
			OR u.id = ANY($3::uuid[])
			OR u.department_id IN (SELECT id FROM departments WHERE active AND lower(btrim(name)) = $2)
			OR u.department_id IN (SELECT s.ref_id FROM terminal_pick_sources s JOIN departments d ON d.id = s.ref_id AND d.active
				WHERE s.terminal_user_id = $4::uuid AND s.kind = 'department')
			OR u.id IN (SELECT m.user_id FROM terminal_pick_sources s JOIN user_groups g ON g.id = s.ref_id AND g.active
				JOIN user_group_members m ON m.group_id = g.id WHERE s.terminal_user_id = $4::uuid AND s.kind = 'group')
			OR u.id IN (SELECT s.ref_id FROM terminal_pick_sources s WHERE s.terminal_user_id = $4::uuid AND s.kind = 'user')
		) ORDER BY lower(u.last_name), lower(u.first_name), u.id`, depts, terminalMaintenanceDeptName, selectable, termUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []terminalPerson
	for rows.Next() {
		var p terminalPerson
		if err := rows.Scan(&p.ID, &p.Name, &p.Department); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// terminalSelectableHolders: Personen mit dem Recht terminal.selectable (Rolle oder Einzelberechtigung), deren
// Abteilungsbereich die Anlage des Terminals einschließt. Nur dieses eine Recht wird gelesen.
func (h *Handler) terminalSelectableHolders(ctx context.Context, termUserID string) ([]string, error) {
	if h.rbac == nil {
		return nil, nil
	}
	infra, _, err := h.terminalInfraLookup(ctx, termUserID)
	if err != nil {
		return nil, err
	}
	plantDepts := h.recordDepartments(ctx, "infrastructure", infra)
	// Vorauswahl in SQL: Rolle oder Einzelberechtigung bringt das Recht irgendwo mit; die Abteilung prüft rbac
	rows, err := h.db.Query(ctx, `SELECT u.id::text, u.role::text FROM users u
		WHERE u.active AND NOT u.is_bot AND NOT u.is_system_user AND u.role::text <> 'viewer'
		AND (EXISTS (SELECT 1 FROM role_permissions rp JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.id = rp.permission_id
				WHERE p.key = $1 AND r.key = u.role::text)
			OR EXISTS (SELECT 1 FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
				WHERE p.key = $1 AND up.user_id = u.id AND up.granted))`, terminalPermSelectable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		if h.rbac.HasPermissionInDepartment(id, role, terminalPermSelectable, plantDepts) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// ---- Einstellung auf der Benutzerseite des Terminals (Abschnitt „Freier Terminal-Benutzer“) ----

const (
	pickKindGroup = "group"
	pickKindDept  = "department"
	pickKindUser  = "user"
)

type terminalPickOption struct {
	ID, Name, Sub string
	On            bool
}

// terminalPickView: Daten des Abschnitts „Wer am Terminal wählbar ist“.
type terminalPickView struct {
	Groups, Depts, People []terminalPickOption
}

func (h *Handler) loadTerminalPickView(ctx context.Context, uid string) terminalPickView {
	var v terminalPickView
	if h.db == nil {
		return v
	}
	on := map[string]bool{}
	if rows, err := h.db.Query(ctx, `SELECT kind || ':' || ref_id::text FROM terminal_pick_sources WHERE terminal_user_id = $1::uuid`, uid); err == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				on[k] = true
			}
		}
		rows.Close()
	}
	read := func(q, kind string, dst *[]terminalPickOption) {
		rows, err := h.db.Query(ctx, q)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var o terminalPickOption
			if rows.Scan(&o.ID, &o.Name, &o.Sub) == nil {
				o.On = on[kind+":"+o.ID]
				*dst = append(*dst, o)
			}
		}
	}
	read(`SELECT id::text, name, '' FROM user_groups WHERE active ORDER BY lower(name)`, pickKindGroup, &v.Groups)
	read(`SELECT id::text, name, '' FROM departments WHERE active ORDER BY lower(name)`, pickKindDept, &v.Depts)
	read(`SELECT id::text, TRIM(first_name || ' ' || last_name), COALESCE(department, '') FROM users
		WHERE active AND NOT is_bot AND NOT is_system_user AND role::text <> 'viewer' ORDER BY lower(last_name), lower(first_name), id`, pickKindUser, &v.People)
	return v
}

func terminalPickText(v terminalPickView) string {
	var n []string
	add := func(prefix string, os []terminalPickOption) {
		for _, o := range os {
			if o.On {
				n = append(n, prefix+o.Name)
			}
		}
	}
	add("Gruppe ", v.Groups)
	add("Abteilung ", v.Depts)
	add("", v.People)
	if len(n) == 0 {
		return "keine"
	}
	return strings.Join(n, ", ")
}

// saveTerminalPickSources speichert die Quellen (Felder terminal_pick_groups, terminal_pick_depts,
// terminal_pick_people). Der Aufrufer prüft Recht, Terminal-Systembenutzer und den Marker terminal_pick_sent.
// Es werden nur vorhandene, aktive Einträge übernommen.
func (h *Handler) saveTerminalPickSources(ctx context.Context, uid string, groups, depts, people []string, by string) error {
	clean := func(vals []string) []string {
		var ids []string
		seen := map[string]bool{}
		for _, s := range vals {
			if s = strings.TrimSpace(s); len(s) == 36 && !seen[s] && uuidInPathRe.MatchString(s) {
				seen[s] = true
				ids = append(ids, s)
			}
		}
		return ids
	}
	before := h.loadTerminalPickView(ctx, uid)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM terminal_pick_sources WHERE terminal_user_id = $1::uuid`, uid); err != nil {
		return err
	}
	ins := func(kind, q string, vals []string) error {
		ids := clean(vals)
		if len(ids) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO terminal_pick_sources (terminal_user_id, kind, ref_id)
			SELECT $1::uuid, $2::text, x.id FROM (`+q+`) x ON CONFLICT DO NOTHING`, uid, kind, ids)
		return err
	}
	if err := ins(pickKindGroup, `SELECT id FROM user_groups WHERE active AND id = ANY($3::uuid[])`, groups); err != nil {
		return err
	}
	if err := ins(pickKindDept, `SELECT id FROM departments WHERE active AND id = ANY($3::uuid[])`, depts); err != nil {
		return err
	}
	if err := ins(pickKindUser, `SELECT id FROM users WHERE active AND NOT is_bot AND NOT is_system_user AND role::text <> 'viewer' AND id = ANY($3::uuid[])`, people); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after := h.loadTerminalPickView(ctx, uid)
	if o, n := terminalPickText(before), terminalPickText(after); o != n {
		h.addHistory(ctx, "user", uid, "update", "Auswahl am Terminal", o, n, "Auswahl am Terminal (zusätzliche Quellen): "+n, by)
	}
	return nil
}
