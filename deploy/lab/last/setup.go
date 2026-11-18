package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Markierungen: Alles, was die Lastprüfung anlegt, ist daran zu erkennen und
// wird von -cleanup wieder entfernt.
//
//	Benutzer         last01…lastNN mit E-Mail …@example.invalid
//	Tickets/Störungen/Wartungsaufträge   Titel beginnt mit „Lastprüfung “
//	Anlagen          Name beginnt mit „LP-“ (Werk → Linie → Anlage)
//	Rüstzeiten       an LP-Anlagen bzw. Rüsttaste „LP-Rüsttaste“
//	Easy-Meldetext   „Lastprüfung Meldetext“ (damit keine Übersetzung nötig ist)
const (
	password   = "Last-Labor-2026!"
	titlePref  = "Lastprüfung "
	infraPref  = "LP-"
	buttonName = "LP-Rüsttaste"
	easyText   = "Lastprüfung Meldetext"
)

// setupOpts: Mengen für -setup.
type setupOpts struct {
	Users, Tickets, Faults, Plants, Maint, Setups int
}

// lpStep: eine benannte Auswahl; spätere Schritte dürfen frühere benutzen.
type lpStep struct{ name, sql string }

// lpSteps: die Mengen, auf denen Zählung und Aufräumen beruhen. Gleiche
// Auswahlen für beides, damit „übrig“ dasselbe misst, was gelöscht wird.
func lpSteps(hasSetup, hasMaint bool) []lpStep {
	none := `SELECT NULL::uuid AS id WHERE false`
	mt, setup := none, none
	faultSetup := ``
	if hasMaint {
		mt = `SELECT id FROM maintenance_tasks WHERE title LIKE 'Lastprüfung %' OR created_by IN (SELECT id FROM lp_users)
			OR infrastructure_id IN (SELECT id FROM lp_infra)`
	}
	if hasSetup {
		setup = `SELECT id FROM setup_times WHERE button_name = 'LP-Rüsttaste' OR created_by IN (SELECT id FROM lp_users)
			OR infrastructure_id IN (SELECT id FROM lp_infra)`
		faultSetup = ` OR id IN (SELECT fault_id FROM setup_times WHERE fault_id IS NOT NULL AND
			(infrastructure_id IN (SELECT id FROM lp_infra) OR button_name = 'LP-Rüsttaste'))`
	}
	return []lpStep{
		{"lp_users", `SELECT id FROM users WHERE username ~ '^last[0-9]{2,}$' AND email LIKE '%@example.invalid'`},
		{"lp_infra", `SELECT id FROM infrastructure WHERE name LIKE 'LP-%'`},
		{"lp_tickets", `SELECT id FROM tickets WHERE title LIKE 'Lastprüfung %' OR created_by IN (SELECT id FROM lp_users)
			OR infrastructure_id IN (SELECT id FROM lp_infra)`},
		{"lp_faults", `SELECT id FROM faults WHERE title LIKE 'Lastprüfung %' OR created_by IN (SELECT id FROM lp_users)
			OR infrastructure_id IN (SELECT id FROM lp_infra)` + faultSetup},
		{"lp_mt", mt},
		{"lp_setup", setup},
		{"lp_refs", `SELECT 'ticket'::text AS t, id AS r FROM lp_tickets UNION ALL SELECT 'fault', id FROM lp_faults
			UNION ALL SELECT 'maintenance_task', id FROM lp_mt UNION ALL SELECT 'infrastructure', id FROM lp_infra
			UNION ALL SELECT 'setup', id FROM lp_setup`},
	}
}

func withCTE(steps []lpStep) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = s.name + " AS (" + s.sql + ")"
	}
	return "WITH " + strings.Join(parts, ", ") + " "
}

func tableExists(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, name string) bool {
	var ok bool
	_ = q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok)
	return ok
}

// silent: keine Änderungswarteschlange, keine Benachrichtigungen, kein Abgleich
// mit Fremdsystemen für die Massendaten (gilt nur in dieser Transaktion).
func silent(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('pdh.sync_silent', '1', true), set_config('pdh.sync_origin', '1', true)`)
	return err
}

// countRow: eine Art mit ihrer Zahl.
type countRow struct {
	Label string
	N     int
}

// lpCounts zählt alle markierten Daten je Art.
func lpCounts(ctx context.Context, pool *pgxpool.Pool) ([]countRow, error) {
	hasSetup, hasMaint := tableExists(ctx, pool, "setup_times"), tableExists(ctx, pool, "maintenance_tasks")
	steps := lpSteps(hasSetup, hasMaint)
	pre := withCTE(steps)
	type q struct{ label, sql string }
	qs := []q{
		{"Benutzer", `SELECT count(*) FROM lp_users`},
		{"Tickets", `SELECT count(*) FROM lp_tickets`},
		{"Störmeldungen", `SELECT count(*) FROM lp_faults`},
		{"Anlagen (Werk/Linie/Anlage)", `SELECT count(*) FROM lp_infra`},
		{"Ticket-Kommentare", `SELECT count(*) FROM ticket_comments WHERE user_id IN (SELECT id FROM lp_users) OR ticket_id IN (SELECT id FROM lp_tickets)`},
		{"Störungs-Maßnahmen", `SELECT count(*) FROM fault_actions WHERE created_by IN (SELECT id FROM lp_users) OR fault_id IN (SELECT id FROM lp_faults)`},
		{"Anlagen-Kommentare", `SELECT count(*) FROM infrastructure_comments WHERE created_by IN (SELECT id FROM lp_users) OR infrastructure_id IN (SELECT id FROM lp_infra)`},
		{"Verlaufseinträge", `SELECT count(*) FROM record_history WHERE created_by IN (SELECT id FROM lp_users) OR (ref_type::text, ref_id) IN (SELECT t, r FROM lp_refs)`},
	}
	if hasMaint {
		qs = append(qs, q{"Wartungsaufträge", `SELECT count(*) FROM lp_mt`})
	}
	if hasSetup {
		qs = append(qs, q{"Rüstzeiten", `SELECT count(*) FROM lp_setup`},
			q{"Rüsttasten", `SELECT count(*) FROM setup_buttons WHERE name = 'LP-Rüsttaste'`})
	}
	if tableExists(ctx, pool, "easy_report_texts") {
		qs = append(qs, q{"Easy-Meldetexte", `SELECT count(*) FROM easy_report_texts WHERE text = 'Lastprüfung Meldetext'`})
	}
	out := make([]countRow, 0, len(qs))
	for _, x := range qs {
		var n int
		if err := pool.QueryRow(ctx, pre+x.sql).Scan(&n); err != nil {
			return nil, fmt.Errorf("zählen (%s): %w", x.label, err)
		}
		out = append(out, countRow{x.label, n})
	}
	return out, nil
}

func printCounts(w io.Writer, title string, rows []countRow) int {
	fmt.Fprintln(w, title)
	total := 0
	for _, r := range rows {
		fmt.Fprintf(w, "  %-30s %7d\n", r.Label, r.N)
		total += r.N
	}
	return total
}

// ── anlegen ─────────────────────────────────────────────────────────────

func doSetup(ctx context.Context, pool *pgxpool.Pool, o setupOpts, out io.Writer) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := silent(ctx, tx); err != nil {
		return err
	}
	hasSetup, hasMaint := tableExists(ctx, tx, "setup_times"), tableExists(ctx, tx, "maintenance_tasks")

	// Benutzer: Systembenutzer, damit der Nextcloud-Abgleich (aktiv UND kein
	// Systembenutzer) keine Nextcloud-Konten für sie anlegt. Anmelden und alle
	// Seiten sehen funktioniert trotzdem (siehe Bericht).
	for i := 1; i <= o.Users; i++ {
		name := fmt.Sprintf("last%02d", i)
		if _, err := tx.Exec(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, active, is_system_user)
			SELECT $1::text, $1::text || '@example.invalid', $2::text, 'Last', $1::text, 'admin', true, true
			WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = $1::text)`, name, string(hash)); err != nil {
			return fmt.Errorf("benutzer %s: %w", name, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET is_system_user = true, active = true
		WHERE username ~ '^last[0-9]{2,}$' AND email LIKE '%@example.invalid' AND (NOT is_system_user OR NOT active)`); err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE username = 'last01'`).Scan(&owner); err != nil {
		return fmt.Errorf("benutzer last01 fehlt (-setup-users mindestens 1): %w", err)
	}

	plants, err := setupInfra(ctx, tx, o.Plants)
	if err != nil {
		return fmt.Errorf("anlagen: %w", err)
	}
	if len(plants) == 0 {
		return errors.New("keine Anlagen – -plants muss größer als 0 sein")
	}

	// Tickets: nur so viele nachlegen, wie bis zur Zielmenge fehlen.
	have := 0
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE title LIKE 'Lastprüfung %' AND created_by IN (SELECT id FROM users WHERE username ~ '^last[0-9]{2,}$')`).Scan(&have)
	if n := o.Tickets - have; n > 0 {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE new_tickets ON COMMIT DROP AS SELECT NULL::uuid AS id, NULL::timestamptz AS at WHERE false`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `WITH ins AS (
				INSERT INTO tickets (title, description, priority, status, created_by, infrastructure_id, tags, created_at, resolved_at)
				SELECT 'Lastprüfung ' || to_char(g, 'FM00000'),
				       'Beispielticket der Lastprüfung. ' || repeat('Text zur Störung an der Anlage. ', 1 + g % 6),
				       (ARRAY['low','medium','high','critical'])[1 + g % 4]::ticket_priority,
				       (ARRAY['open','open','in_progress','pending','open','in_progress','open','pending','closed','resolved'])[1 + g % 10]::ticket_status,
				       $1::uuid, ($2::text[])[1 + g % cardinality($2::text[])]::uuid, '["lastprüfung"]'::jsonb,
				       now() - (g % 90) * interval '1 day' - (g % 1440) * interval '1 minute',
				       CASE WHEN g % 10 >= 8 THEN now() - (g % 30) * interval '1 day' END
				  FROM generate_series($3::int + 1, $4::int) g
				RETURNING id, created_at)
			INSERT INTO new_tickets SELECT id, created_at FROM ins`, owner, plants, have, o.Tickets); err != nil {
			return fmt.Errorf("tickets: %w", err)
		}
		// Kommentare an etwa 30 % der neuen Tickets, je 1 bis 3
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_comments (ticket_id, user_id, text, created_at)
			SELECT t.id, $1::uuid, 'Lastprüfung Kommentar ' || n || ' – bitte prüfen.', t.at + n * interval '1 hour'
			  FROM new_tickets t CROSS JOIN LATERAL generate_series(1, 1 + abs(hashtext(t.id::text)) % 3) n
			 WHERE abs(hashtext(t.id::text || 'k')) % 10 < 3`, owner); err != nil {
			return fmt.Errorf("ticket-kommentare: %w", err)
		}
	}

	haveF := 0
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM faults WHERE title LIKE 'Lastprüfung %' AND created_by IN (SELECT id FROM users WHERE username ~ '^last[0-9]{2,}$')`).Scan(&haveF)
	if n := o.Faults - haveF; n > 0 {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE new_faults ON COMMIT DROP AS SELECT NULL::uuid AS id, NULL::timestamptz AS at WHERE false`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `WITH ins AS (
				INSERT INTO faults (title, description, severity, status, created_by, infrastructure_id, detected_at, created_at, due_date, resolved_at)
				SELECT 'Lastprüfung Störung ' || to_char(g, 'FM00000'),
				       'Beispielstörung der Lastprüfung. ' || repeat('Anlage steht, Fehlercode wird angezeigt. ', 1 + g % 4),
				       (ARRAY['low','medium','high','critical'])[1 + g % 4]::fault_severity,
				       (ARRAY['detected','in_progress','detected','pending','in_progress','detected','resolved','detected'])[1 + g % 8]::fault_status,
				       $1::uuid, ($2::text[])[1 + g % cardinality($2::text[])]::uuid,
				       now() - (g % 60) * interval '1 day', now() - (g % 60) * interval '1 day',
				       now() + ((g % 30) - 10) * interval '1 day',
				       CASE WHEN g % 8 = 6 THEN now() - (g % 20) * interval '1 day' END
				  FROM generate_series($3::int + 1, $4::int) g
				RETURNING id, created_at)
			INSERT INTO new_faults SELECT id, created_at FROM ins`, owner, plants, haveF, o.Faults); err != nil {
			return fmt.Errorf("störmeldungen: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO fault_actions (fault_id, description, created_by, created_at)
			SELECT f.id, 'Lastprüfung Maßnahme ' || n || ': Anlage geprüft.', $1::uuid, f.at + n * interval '2 hours'
			  FROM new_faults f CROSS JOIN LATERAL generate_series(1, 1 + abs(hashtext(f.id::text)) % 3) n
			 WHERE abs(hashtext(f.id::text || 'k')) % 10 < 3`, owner); err != nil {
			return fmt.Errorf("störungs-maßnahmen: %w", err)
		}
	}

	// Anlagen-Kommentare: etwa jede fünfte Anlage, 1 bis 2
	var haveIC int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM infrastructure_comments WHERE text LIKE 'Lastprüfung %'`).Scan(&haveIC)
	if haveIC == 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO infrastructure_comments (infrastructure_id, text, created_by)
			SELECT p::uuid, 'Lastprüfung Anlagenhinweis ' || n, $1::uuid
			  FROM unnest($2::text[]) AS p CROSS JOIN LATERAL generate_series(1, 1 + abs(hashtext(p)) % 2) n
			 WHERE abs(hashtext(p || 'k')) % 5 = 0`, owner, plants); err != nil {
			return fmt.Errorf("anlagen-kommentare: %w", err)
		}
	}

	if hasMaint {
		var haveM int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM maintenance_tasks WHERE title LIKE 'Lastprüfung %' AND created_by IN (SELECT id FROM users WHERE username ~ '^last[0-9]{2,}$')`).Scan(&haveM)
		if o.Maint > haveM {
			if _, err := tx.Exec(ctx, `INSERT INTO maintenance_tasks (title, description, type, infrastructure_id, priority, status, due_date, created_by, completed_at)
				SELECT 'Lastprüfung Wartung ' || to_char(g, 'FM00000'), 'Wartungsauftrag der Lastprüfung',
				       (ARRAY['preventive','inspection','calibration','cleaning'])[1 + g % 4]::maintenance_type,
				       ($2::text[])[1 + g % cardinality($2::text[])]::uuid,
				       (ARRAY['low','medium','high','critical'])[1 + g % 4]::maintenance_priority,
				       (ARRAY['open','open','in_progress','done','pending','open'])[1 + g % 6]::maintenance_status,
				       now() + ((g % 60) - 20) * interval '1 day', $1::uuid,
				       CASE WHEN g % 6 = 3 THEN now() - (g % 20) * interval '1 day' END
				  FROM generate_series($3::int + 1, $4::int) g`, owner, plants, haveM, o.Maint); err != nil {
				return fmt.Errorf("wartungsaufträge: %w", err)
			}
		}
	}

	if hasSetup {
		if err := setupSetupTimes(ctx, tx, owner, plants, o.Setups); err != nil {
			return fmt.Errorf("rüstzeiten: %w", err)
		}
	}
	if tableExists(ctx, tx, "easy_report_texts") {
		var tid string
		err := tx.QueryRow(ctx, `SELECT id::text FROM easy_report_texts WHERE text = $1`, easyText).Scan(&tid)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO easy_report_texts (text, report_type, kind, created_by) VALUES ($1, 'both', '', $2::uuid) RETURNING id::text`, easyText, owner).Scan(&tid)
		}
		if err != nil {
			return fmt.Errorf("easy-meldetext: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO infra_easy_texts (infrastructure_id, text_id)
			SELECT p::uuid, $1::uuid FROM unnest($2::text[]) p ON CONFLICT DO NOTHING`, tid, plants); err != nil {
			return fmt.Errorf("easy-meldetext zuordnen: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	rows, err := lpCounts(ctx, pool)
	if err != nil {
		return err
	}
	printCounts(out, "Testdaten bereit:", rows)
	return nil
}

// setupInfra legt Werk → Linie → Anlage an (nur, was an der Zielmenge fehlt) und
// liefert die Kennungen aller LP-Anlagen (Typ plant).
func setupInfra(ctx context.Context, tx pgx.Tx, plants int) ([]string, error) {
	werke := max(1, (plants+49)/50)
	linien := werke * 4
	var nW, nL, nP int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type = 'building'), count(*) FILTER (WHERE type = 'line'), count(*) FILTER (WHERE type = 'plant')
		FROM infrastructure WHERE name LIKE 'LP-%'`).Scan(&nW, &nL, &nP); err != nil {
		return nil, err
	}
	ins := func(parent *string, name, typ string, i int) error {
		_, err := tx.Exec(ctx, `INSERT INTO infrastructure (parent_id, name, type, description, location, serial_no, manufacturer, model, installed_at)
			VALUES ($1::uuid, $2, $3::infra_type, 'Anlage der Lastprüfung', $4, $5, 'Lastprüfung GmbH', $6, current_date - ($7::int * 11))`,
			parent, name, typ, fmt.Sprintf("LP-Halle %d", 1+i%9), fmt.Sprintf("LP-SN-%05d", i), fmt.Sprintf("Modell %c", 'A'+rune(i%6)), i)
		return err
	}
	for i := nW + 1; i <= werke; i++ {
		if err := ins(nil, fmt.Sprintf("LP-Werk %d", i), "building", i); err != nil {
			return nil, err
		}
	}
	buildings, err := idsByType(ctx, tx, "building")
	if err != nil {
		return nil, err
	}
	for i := nL + 1; i <= linien; i++ {
		parent := buildings[(i-1)%len(buildings)]
		if err := ins(&parent, fmt.Sprintf("LP-Linie %02d", i), "line", i); err != nil {
			return nil, err
		}
	}
	lines, err := idsByType(ctx, tx, "line")
	if err != nil {
		return nil, err
	}
	for i := nP + 1; i <= plants; i++ {
		parent := lines[(i-1)%len(lines)]
		if err := ins(&parent, fmt.Sprintf("LP-Anlage %04d", i), "plant", i); err != nil {
			return nil, err
		}
	}
	return idsByType(ctx, tx, "plant")
}

func idsByType(ctx context.Context, tx pgx.Tx, typ string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM infrastructure WHERE name LIKE 'LP-%' AND type = $1::infra_type ORDER BY name`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// setupSetupTimes: Rüsttaste (an allen LP-Anlagen) und beendete Rüstzeiten.
func setupSetupTimes(ctx context.Context, tx pgx.Tx, owner string, plants []string, want int) error {
	var reason string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM setup_reasons WHERE active ORDER BY (key = 'changeover') DESC, sort LIMIT 1`).Scan(&reason); err != nil {
		return fmt.Errorf("kein Rüstgrund im Katalog: %w", err)
	}
	var btn string
	err := tx.QueryRow(ctx, `SELECT id::text FROM setup_buttons WHERE name = $1`, buttonName).Scan(&btn)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO setup_buttons (name, reason_id, sort) VALUES ($1, $2::uuid, 900) RETURNING id::text`, buttonName, reason).Scan(&btn)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO infra_setup_buttons (infrastructure_id, button_id)
		SELECT p::uuid, $1::uuid FROM unnest($2::text[]) p ON CONFLICT DO NOTHING`, btn, plants); err != nil {
		return err
	}
	var have int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM setup_times WHERE button_name = $1 AND status = 'done'`, buttonName).Scan(&have)
	if want <= have {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO setup_times (infrastructure_id, started_at, ended_at, reason_id, button_id, button_name, origin, status, max_minutes, grace_minutes, note, created_by)
		SELECT ($1::text[])[1 + g % cardinality($1::text[])]::uuid,
		       now() - interval '2 days' - g * interval '90 minutes',
		       now() - interval '2 days' - g * interval '90 minutes' + (10 + g % 50) * interval '1 minute',
		       $2::uuid, $3::uuid, $4::text, 'manual', 'done', 120, 15, 'Rüstzeit der Lastprüfung', $5::uuid
		  FROM generate_series($6::int + 1, $7::int) g`, plants, reason, btn, buttonName, owner, have, want)
	return err
}

// ── aufräumen ───────────────────────────────────────────────────────────

// cleanSteps: Löschfolge. table "" = immer; sonst nur, wenn die Tabelle existiert.
type cleanStep struct{ table, sql string }

func cleanSteps() []cleanStep {
	refs := `(ref_type::text, ref_id) IN (SELECT t, r FROM lp_refs)`
	mod := `(module::text, record_id) IN (SELECT t, r FROM lp_refs)`
	return []cleanStep{
		// abhängige Zeilen zuerst
		{"record_history", `DELETE FROM record_history WHERE created_by IN (SELECT id FROM lp_users) OR ` + refs},
		{"attachments", `DELETE FROM attachments WHERE created_by IN (SELECT id FROM lp_users) OR ` + refs},
		{"chat_message_links", `DELETE FROM chat_message_links WHERE ` + refs},
		{"deck_sync_markers", `DELETE FROM deck_sync_markers WHERE ` + refs},
		{"global_dashboard_actions", `DELETE FROM global_dashboard_actions WHERE assigned_to IN (SELECT id FROM lp_users) OR action_user_id IN (SELECT id FROM lp_users) OR ` + refs},
		{"push_alerts", `DELETE FROM push_alerts WHERE ` + refs},
		{"record_external_parties", `DELETE FROM record_external_parties WHERE created_by IN (SELECT id FROM lp_users) OR ` + refs},
		{"deletion_requests", `DELETE FROM deletion_requests WHERE ` + mod},
		{"record_categories", `DELETE FROM record_categories WHERE ` + mod},
		{"record_change_queue", `DELETE FROM record_change_queue WHERE ` + mod},
		{"record_field_sets", `DELETE FROM record_field_sets WHERE ` + mod},
		{"record_field_values", `DELETE FROM record_field_values WHERE ` + mod},
		{"time_entries", `DELETE FROM time_entries WHERE user_id IN (SELECT id FROM lp_users) OR infrastructure_id IN (SELECT id FROM lp_infra) OR ` + refs},
		{"infrastructure_comments", `DELETE FROM infrastructure_comments WHERE created_by IN (SELECT id FROM lp_users) OR infrastructure_id IN (SELECT id FROM lp_infra) OR ` + refs},
		{"ticket_comments", `DELETE FROM ticket_comments WHERE user_id IN (SELECT id FROM lp_users) OR ticket_id IN (SELECT id FROM lp_tickets)`},
		{"ticket_actions", `DELETE FROM ticket_actions WHERE created_by IN (SELECT id FROM lp_users) OR ticket_id IN (SELECT id FROM lp_tickets)`},
		{"fault_actions", `DELETE FROM fault_actions WHERE created_by IN (SELECT id FROM lp_users) OR fault_id IN (SELECT id FROM lp_faults)`},
		{"ticket_pending_parts", `DELETE FROM ticket_pending_parts WHERE created_by IN (SELECT id FROM lp_users) OR ticket_id IN (SELECT id FROM lp_tickets)`},
		{"fault_pending_parts", `DELETE FROM fault_pending_parts WHERE created_by IN (SELECT id FROM lp_users) OR fault_id IN (SELECT id FROM lp_faults)`},
		{"maintenance_task_actions", `DELETE FROM maintenance_task_actions WHERE created_by IN (SELECT id FROM lp_users) OR task_id IN (SELECT id FROM lp_mt)`},
		{"maintenance_task_pending_parts", `DELETE FROM maintenance_task_pending_parts WHERE created_by IN (SELECT id FROM lp_users) OR task_id IN (SELECT id FROM lp_mt)`},
		// die Vorgänge selbst
		{"setup_times", `DELETE FROM setup_times WHERE id IN (SELECT id FROM lp_setup)`},
		{"", `DELETE FROM tickets WHERE id IN (SELECT id FROM lp_tickets)`},
		{"", `DELETE FROM faults WHERE id IN (SELECT id FROM lp_faults)`},
		{"maintenance_tasks", `DELETE FROM maintenance_tasks WHERE id IN (SELECT id FROM lp_mt)`},
		{"maintenance_plans", `DELETE FROM maintenance_plans WHERE name LIKE 'LP-%' OR infrastructure_id IN (SELECT id FROM lp_infra) OR created_by IN (SELECT id FROM lp_users)`},
		{"setup_buttons", `DELETE FROM setup_buttons WHERE name = 'LP-Rüsttaste'`},
		{"easy_report_texts", `DELETE FROM easy_report_texts WHERE text = 'Lastprüfung Meldetext'`},
		{"", `DELETE FROM infrastructure WHERE id IN (SELECT id FROM lp_infra)`},
		{"chat_conversations", `DELETE FROM chat_conversations WHERE created_by IN (SELECT id FROM lp_users)`},
		{"", `DELETE FROM users WHERE id IN (SELECT id FROM lp_users)`},
	}
}

// doCleanup entfernt alle markierten Daten und gibt „übrig: N“ je Art aus.
// Fehler, wenn danach etwas übrig ist.
func doCleanup(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	before, err := lpCounts(ctx, pool)
	if err != nil {
		return err
	}
	printCounts(out, "Vorhanden vor dem Aufräumen:", before)

	hasSetup, hasMaint := tableExists(ctx, pool, "setup_times"), tableExists(ctx, pool, "maintenance_tasks")
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := silent(ctx, tx); err != nil {
		return err
	}
	for _, s := range lpSteps(hasSetup, hasMaint) {
		if _, err := tx.Exec(ctx, "CREATE TEMP TABLE "+s.name+" ON COMMIT DROP AS "+s.sql); err != nil {
			return fmt.Errorf("auswahl %s: %w", s.name, err)
		}
	}
	for _, s := range cleanSteps() {
		if s.table != "" && !tableExists(ctx, tx, s.table) {
			continue
		}
		if _, err := tx.Exec(ctx, s.sql); err != nil {
			first, _, _ := strings.Cut(s.sql, " WHERE")
			return fmt.Errorf("löschen (%s): %w", first, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after, err := lpCounts(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Aufgeräumt. Übrig:")
	left := 0
	for _, r := range after {
		fmt.Fprintf(out, "  %-30s übrig: %d\n", r.Label, r.N)
		left += r.N
	}
	if left > 0 {
		return fmt.Errorf("es sind noch %d markierte Zeilen übrig", left)
	}
	fmt.Fprintln(out, "übrig: 0")
	return nil
}
