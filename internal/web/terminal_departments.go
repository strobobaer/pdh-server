package web

// Abteilungen eines Terminals: Aus diesen Abteilungen stammen die Mitarbeitenden, die man am Terminal
// auswählen kann (Vorgänge, weitere Terminalfunktionen). Einstellung je Terminal-Benutzer in
// terminal_settings.abteilungen (Migration 206); NULL = Vorgabe: die Abteilung(en) der Anlage des Terminals.

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// terminalDepartments liefert die Abteilungs-IDs des Terminals: die eingestellten, sonst die der Anlage
// des Terminals (infrastructure_department). Keine Anlage und keine Einstellung: leere Liste.
func (h *Handler) terminalDepartments(ctx context.Context, termUserID string) ([]string, error) {
	if h.db == nil || termUserID == "" {
		return nil, nil
	}
	var set []string
	err := h.db.QueryRow(ctx, `SELECT abteilungen::text[] FROM terminal_settings WHERE user_id = $1::uuid`, termUserID).Scan(&set)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(set) > 0 {
		// nur noch vorhandene, aktive Abteilungen (eine gelöschte Abteilung fällt still weg)
		return h.queryStrings(ctx, `SELECT id::text FROM departments WHERE id = ANY($1::uuid[]) AND active ORDER BY id`, set)
	}
	return h.queryStrings(ctx, `SELECT DISTINCT idp.department_id::text FROM infrastructure_department idp
		JOIN users u ON u.terminal_infrastructure_id = idp.infrastructure_id
		WHERE u.id = $1::uuid ORDER BY 1`, termUserID)
}

func (h *Handler) queryStrings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// terminalPerson: eine am Terminal wählbare Person.
type terminalPerson struct {
	ID, Name, Department string
}

// terminalDepartmentPeople: aktive Mitarbeitende (keine Systembenutzer, keine Bots, keine Betrachter) der
// Abteilungen des Terminals, nach Name.
func (h *Handler) terminalDepartmentPeople(ctx context.Context, termUserID string) ([]terminalPerson, error) {
	depts, err := h.terminalDepartments(ctx, termUserID)
	if err != nil || len(depts) == 0 {
		return nil, err
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, TRIM(first_name || ' ' || last_name), COALESCE(department, '')
		FROM users WHERE active AND NOT is_bot AND NOT is_system_user AND role::text <> 'viewer'
		AND department_id = ANY($1::uuid[]) ORDER BY lower(last_name), lower(first_name), id`, depts)
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

// ---- Einstellung auf der Benutzerseite des Terminals (Stammdaten) ----

type terminalDeptOption struct {
	ID, Name string
	On       bool
}

// terminalDeptsView: Daten des Abschnitts „Abteilungen des Terminals“.
type terminalDeptsView struct {
	Options []terminalDeptOption
	Custom  bool   // eigene Auswahl gespeichert
	Default string // Vorgabe als Text (Abteilungen der Anlage)
}

func (h *Handler) loadTerminalDeptsView(ctx context.Context, uid string) terminalDeptsView {
	var v terminalDeptsView
	if h.db == nil {
		return v
	}
	var set []string
	_ = h.db.QueryRow(ctx, `SELECT abteilungen::text[] FROM terminal_settings WHERE user_id = $1::uuid`, uid).Scan(&set)
	v.Custom = len(set) > 0
	on := map[string]bool{}
	for _, s := range set {
		on[s] = true
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, name FROM departments WHERE active ORDER BY lower(name)`)
	if err == nil {
		for rows.Next() {
			var o terminalDeptOption
			if rows.Scan(&o.ID, &o.Name) == nil {
				o.On = on[o.ID]
				v.Options = append(v.Options, o)
			}
		}
		rows.Close()
	}
	names, _ := h.queryStrings(ctx, `SELECT DISTINCT d.name FROM infrastructure_department idp JOIN departments d ON d.id = idp.department_id
		JOIN users u ON u.terminal_infrastructure_id = idp.infrastructure_id WHERE u.id = $1::uuid ORDER BY 1`, uid)
	v.Default = strings.Join(names, ", ")
	return v
}

// saveTerminalDepartmentsForm speichert die Auswahl (Werte des Felds terminal_depts); nichts angehakt = Vorgabe.
// Der Aufrufer prüft Recht, Systembenutzer und den Marker terminal_depts_sent.
func (h *Handler) saveTerminalDepartmentsForm(ctx context.Context, vals []string, uid, by string) error {
	var ids []string
	seen := map[string]bool{}
	for _, s := range vals {
		if s = strings.TrimSpace(s); len(s) == 36 && !seen[s] && uuidInPathRe.MatchString(s) {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	if len(ids) > 0 { // nur vorhandene Abteilungen
		var err error
		if ids, err = h.queryStrings(ctx, `SELECT id::text FROM departments WHERE id = ANY($1::uuid[]) AND active ORDER BY lower(name)`, ids); err != nil {
			return err
		}
	}
	before := h.loadTerminalDeptsView(ctx, uid)
	if _, err := h.db.Exec(ctx, `INSERT INTO terminal_settings (user_id) SELECT id FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO NOTHING`, uid); err != nil {
		return err
	}
	var arg any
	if len(ids) > 0 {
		arg = ids
	}
	if _, err := h.db.Exec(ctx, `UPDATE terminal_settings SET abteilungen = $2::uuid[], updated_at = NOW() WHERE user_id = $1::uuid`, uid, arg); err != nil {
		return err
	}
	after := h.loadTerminalDeptsView(ctx, uid)
	if o, n := terminalDeptsText(before), terminalDeptsText(after); o != n {
		h.addHistory(ctx, "user", uid, "update", "Abteilungen des Terminals", o, n, "Abteilungen des Terminals: "+n, by)
	}
	return nil
}

func terminalDeptsText(v terminalDeptsView) string {
	var n []string
	for _, o := range v.Options {
		if o.On {
			n = append(n, o.Name)
		}
	}
	if len(n) == 0 {
		return "Vorgabe (Abteilung der Anlage)"
	}
	return strings.Join(n, ", ")
}
