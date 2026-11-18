package extsync

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

// Der Systembenutzer aus Migration 072 dient als Ersteller der Testdaten.
const systemUser = "00000000-0000-4000-8000-00000000d0d1"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixture legt Verbindung, Profil und ein Ticket an; die Verknüpfung nur auf Wunsch.
type fixture struct {
	pool     *pgxpool.Pool
	ticketID string
	linkID   string
}

func newFixture(t *testing.T, linked bool) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	f := &fixture{pool: pool}
	var connID, profID string
	must(t, pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mssql', 'extsync-test') RETURNING id`).Scan(&connID))
	must(t, pool.QueryRow(ctx, `INSERT INTO sync_profiles (name, connection_id, target_type, source_schema, source_table, key_columns)
		VALUES ('extsync-test', $1, 'ticket', 'dbo', 'Tickets', ARRAY['Id']) RETURNING id`, connID).Scan(&profID))
	must(t, pool.QueryRow(ctx, `INSERT INTO tickets (title, created_by) VALUES ('Ausgangstitel', $1) RETURNING id`,
		systemUser).Scan(&f.ticketID))
	if linked {
		must(t, pool.QueryRow(ctx, `INSERT INTO sync_links (profile_id, foreign_key, ref_type, ref_id)
			VALUES ($1, 'K1', 'ticket', $2) RETURNING id`, profID, f.ticketID).Scan(&f.linkID))
	}
	t.Cleanup(func() {
		c := context.Background()
		// Profil löschen entfernt Verknüpfungen und Warteliste (CASCADE).
		_, _ = pool.Exec(c, `DELETE FROM sync_profiles WHERE id = $1`, profID)
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1`, connID)
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE id = $1`, f.ticketID)
	})
	return f
}

func (f *fixture) outboxCount(t *testing.T, kind string) int {
	t.Helper()
	var n int
	must(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_outbox o
		JOIN sync_links l ON l.id = o.link_id
		WHERE l.ref_id = $1 AND ($2 = '' OR o.kind = $2)`, f.ticketID, kind).Scan(&n))
	return n
}

func TestOutboxOnlyForLinkedRecords(t *testing.T) {
	// Ohne Verknüpfung: keine Einträge.
	u := newFixture(t, false)
	_, err := u.pool.Exec(context.Background(), `UPDATE tickets SET title = 'Neu' WHERE id = $1`, u.ticketID)
	must(t, err)
	if n := u.outboxCount(t, ""); n != 0 {
		t.Fatalf("ohne Verknüpfung %d Einträge, erwartet 0", n)
	}

	// Mit Verknüpfung: ein Eintrag mit dem neuen Titel.
	f := newFixture(t, true)
	_, err = f.pool.Exec(context.Background(), `UPDATE tickets SET title = 'Neuer Titel' WHERE id = $1`, f.ticketID)
	must(t, err)
	if n := f.outboxCount(t, "update"); n != 1 {
		t.Fatalf("mit Verknüpfung %d Einträge, erwartet 1", n)
	}
	var title string
	must(t, f.pool.QueryRow(context.Background(), `SELECT payload->'fields'->>'title' FROM sync_outbox
		WHERE link_id = $1`, f.linkID).Scan(&title))
	if title != "Neuer Titel" {
		t.Fatalf("Titel in Nutzlast = %q", title)
	}
}

func TestOutboxSkipsSyncOrigin(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	must(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SET LOCAL pdh.sync_origin = '1'`)
	must(t, err)
	_, err = tx.Exec(ctx, `UPDATE tickets SET title = 'Von außen' WHERE id = $1`, f.ticketID)
	must(t, err)
	var n int
	must(t, tx.QueryRow(ctx, `SELECT count(*) FROM sync_outbox WHERE link_id = $1`, f.linkID).Scan(&n))
	if n != 0 {
		t.Fatalf("%d Einträge trotz Herkunft Abgleich, erwartet 0", n)
	}
	must(t, tx.Commit(ctx))
}

func TestOutboxIgnoresUnrelatedColumns(t *testing.T) {
	f := newFixture(t, true)
	_, err := f.pool.Exec(context.Background(), `UPDATE tickets SET updated_at = now() + interval '1 minute',
		no_parts_needed = true WHERE id = $1`, f.ticketID)
	must(t, err)
	if n := f.outboxCount(t, ""); n != 0 {
		t.Fatalf("%d Einträge bei fremden Spalten, erwartet 0", n)
	}
}

func TestOutboxComment(t *testing.T) {
	f := newFixture(t, true)
	_, err := f.pool.Exec(context.Background(), `INSERT INTO ticket_comments (ticket_id, user_id, text)
		VALUES ($1, $2, 'Ein Kommentar')`, f.ticketID, systemUser)
	must(t, err)
	if n := f.outboxCount(t, "comment"); n != 1 {
		t.Fatalf("%d Kommentar-Einträge, erwartet 1", n)
	}
	var text string
	must(t, f.pool.QueryRow(context.Background(), `SELECT payload->>'text' FROM sync_outbox
		WHERE link_id = $1 AND kind = 'comment'`, f.linkID).Scan(&text))
	if text != "Ein Kommentar" {
		t.Fatalf("Text in Nutzlast = %q", text)
	}
}

// Störmeldung mit zwei Verknüpfungen: je Verknüpfung ein Eintrag.
func TestOutboxFaultMultipleLinks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var connID, profID, faultID string
	must(t, pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mssql', 'extsync-test-f') RETURNING id`).Scan(&connID))
	must(t, pool.QueryRow(ctx, `INSERT INTO sync_profiles (name, connection_id, target_type, source_schema, source_table, key_columns)
		VALUES ('extsync-test-f', $1, 'fault', 'dbo', 'Stoerungen', ARRAY['Id']) RETURNING id`, connID).Scan(&profID))
	must(t, pool.QueryRow(ctx, `INSERT INTO faults (title, created_by) VALUES ('Störung', $1) RETURNING id`, systemUser).Scan(&faultID))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM sync_profiles WHERE id = $1`, profID)
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1`, connID)
		_, _ = pool.Exec(c, `DELETE FROM faults WHERE id = $1`, faultID)
	})
	_, err := pool.Exec(ctx, `INSERT INTO sync_links (profile_id, foreign_key, ref_type, ref_id) VALUES
		($1, 'F1', 'fault', $2), ($1, 'F2', 'fault', $2)`, profID, faultID)
	must(t, err)
	_, err = pool.Exec(ctx, `UPDATE faults SET severity = 'high' WHERE id = $1`, faultID)
	must(t, err)
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM sync_outbox o JOIN sync_links l ON l.id = o.link_id
		WHERE l.ref_id = $1 AND o.kind = 'update' AND o.payload->'fields'->>'severity' = 'high'`, faultID).Scan(&n))
	if n != 2 {
		t.Fatalf("%d Einträge für zwei Verknüpfungen, erwartet 2", n)
	}
}

func TestSyncPermissionsGranted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT key, category FROM permissions WHERE key LIKE 'sync.%'`)
	must(t, err)
	cats := map[string]string{}
	for rows.Next() {
		var k, c string
		must(t, rows.Scan(&k, &c))
		cats[k] = c
	}
	must(t, rows.Err())
	for _, k := range []string{"sync.view", "sync.edit", "sync.run", "sync.admin"} {
		if cats[k] != "Daten & Schnittstellen" {
			t.Errorf("Recht %s: Kategorie %q", k, cats[k])
		}
	}
	// Jede Rolle mit import.write (ohne Abteilung) hat view/edit/run.
	var missing int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM roles r
		JOIN role_permissions rp ON rp.role_id = r.id AND rp.department_id IS NULL
		JOIN permissions w ON w.id = rp.permission_id AND w.key = 'import.write'
		CROSS JOIN permissions p
		WHERE p.key IN ('sync.view','sync.edit','sync.run')
		  AND NOT EXISTS (SELECT 1 FROM role_permissions x WHERE x.role_id = r.id
		                  AND x.permission_id = p.id AND x.department_id IS NULL)`).Scan(&missing))
	if missing != 0 {
		t.Errorf("%d fehlende Vergaben an Rollen mit import.write", missing)
	}
	// Rolle admin hat sync.admin.
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM roles r JOIN role_permissions rp ON rp.role_id = r.id
		JOIN permissions p ON p.id = rp.permission_id WHERE r.key = 'admin' AND p.key = 'sync.admin'`).Scan(&n))
	if n == 0 {
		t.Error("Rolle admin hat sync.admin nicht")
	}
}

// Die Benachrichtigungs-Auslöser (record_change_queue) schweigen nur mit
// pdh.sync_silent = '1' in der Transaktion; sonst arbeiten sie wie bisher.
func TestNotifyTriggersHonourSilentGuc(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	queued := func(id string) int {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM record_change_queue WHERE record_id = $1`, id).Scan(&n))
		return n
	}
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = pool.Exec(ctx, `DELETE FROM record_change_queue WHERE record_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM tickets WHERE id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM faults WHERE id = $1`, id)
		}
	})
	for _, tbl := range []string{"tickets", "faults"} {
		// Still: Anlegen und Ändern in derselben Transaktion ohne Eintrag.
		tx, err := pool.Begin(ctx)
		must(t, err)
		_, err = tx.Exec(ctx, `SET LOCAL pdh.sync_silent = '1'`)
		must(t, err)
		var id string
		must(t, tx.QueryRow(ctx, `INSERT INTO `+tbl+` (title, created_by) VALUES ('Still', $1) RETURNING id`, systemUser).Scan(&id))
		ids = append(ids, id)
		_, err = tx.Exec(ctx, `UPDATE `+tbl+` SET title = 'Still 2' WHERE id = $1`, id)
		must(t, err)
		must(t, tx.Commit(ctx))
		if n := queued(id); n != 0 {
			t.Fatalf("%s still: %d Einträge", tbl, n)
		}
		// Ohne Kennzeichen: Änderung wird wie bisher eingereiht.
		_, err = pool.Exec(ctx, `UPDATE `+tbl+` SET title = 'Laut' WHERE id = $1`, id)
		must(t, err)
		if n := queued(id); n != 1 {
			t.Fatalf("%s Änderung: %d Einträge, erwartet 1", tbl, n)
		}
		// Anlegen ohne Kennzeichen: 'created' wie bisher.
		var id2 string
		must(t, pool.QueryRow(ctx, `INSERT INTO `+tbl+` (title, created_by) VALUES ('Laut', $1) RETURNING id`, systemUser).Scan(&id2))
		ids = append(ids, id2)
		var kind string
		must(t, pool.QueryRow(ctx, `SELECT kind FROM record_change_queue WHERE record_id = $1`, id2).Scan(&kind))
		if kind != "created" {
			t.Fatalf("%s Anlegen: %q", tbl, kind)
		}
	}
	// Die neuen Spalten der Migration.
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'sync_profiles' AND column_name = 'created_column')
		   OR (table_name = 'sync_runs' AND column_name = 'n_backfill')`).Scan(&n))
	if n != 2 {
		t.Fatalf("%d von 2 neuen Spalten vorhanden", n)
	}
}
