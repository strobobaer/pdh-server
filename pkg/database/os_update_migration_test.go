//go:build integration

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration 222: Das Recht system.os_update bekommen nur Rollen, deren Rollenverwaltung
// überall gilt (department_id IS NULL); die Migration lässt sich wiederholen.
func TestMigrationOSUpdatePermission(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := RunMigrations(ctx, pool, migrationsDir); err != nil {
		t.Fatal(err)
	}
	sqlText, err := os.ReadFile(filepath.Join(migrationsDir, "222_os_update_permission.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // Testdaten nie festschreiben

	must := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Ausgangslage: das Recht ist an keine Rolle vergeben, drei Testrollen
	must(`DELETE FROM role_permissions WHERE permission_id = (SELECT id FROM permissions WHERE key = 'system.os_update')`)
	must(`INSERT INTO roles (key, label) VALUES ('t_os_a', 'A überall'), ('t_os_b', 'B nur Abteilung'), ('t_os_c', 'C ohne')`)
	var dept string
	if err := tx.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('t_os_abt') RETURNING id`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO role_permissions (role_id, permission_id) SELECT r.id, p.id FROM roles r, permissions p WHERE r.key='t_os_a' AND p.key='system.manage_roles'`)
	must(`INSERT INTO role_permissions (role_id, permission_id, department_id) SELECT r.id, p.id, $1 FROM roles r, permissions p WHERE r.key='t_os_b' AND p.key='system.manage_roles'`, dept)

	for run := 1; run <= 2; run++ { // zweimal: ON CONFLICT
		must(string(sqlText))
		var label, cat string
		if err := tx.QueryRow(ctx, `SELECT label, category FROM permissions WHERE key='system.os_update'`).Scan(&label, &cat); err != nil {
			t.Fatalf("Lauf %d: Recht fehlt: %v", run, err)
		}
		if label != "Betriebssystem aktualisieren" || cat != "System & Sicherheit" {
			t.Fatalf("Lauf %d: Bezeichnung %q, Kategorie %q", run, label, cat)
		}
		got := map[string]int{}
		rows, err := tx.Query(ctx, `SELECT r.key, COUNT(*) FROM role_permissions rp JOIN roles r ON r.id=rp.role_id
			JOIN permissions p ON p.id=rp.permission_id WHERE p.key='system.os_update' AND r.key LIKE 't\_os\_%' GROUP BY r.key`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				t.Fatal(err)
			}
			got[k] = n
		}
		rows.Close()
		if got["t_os_a"] != 1 || got["t_os_b"] != 0 || got["t_os_c"] != 0 || len(got) != 1 {
			t.Fatalf("Lauf %d: Vergabe %v, erwartet nur t_os_a", run, got)
		}
	}
}
