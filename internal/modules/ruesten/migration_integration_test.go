package ruesten

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

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

func TestSetupMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM setup_reasons WHERE key IN ('changeover','aids','cleaning')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("Gründe: %d, %v", n, err)
	}

	var infra, reason string
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('RZ-Migrationstest', 'device') RETURNING id`).Scan(&infra); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM setup_times WHERE infrastructure_id = $1`, infra)
		pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1`, infra)
	})
	if err := pool.QueryRow(ctx, `SELECT id FROM setup_reasons WHERE key = 'changeover'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}

	ins := `INSERT INTO setup_times (infrastructure_id, started_at, ended_at, reason_id, origin, status, max_minutes, grace_minutes)
		VALUES ($1, now() - interval '1 hour', $2, $3, 'manual', $4, 120, 15)`
	if _, err := pool.Exec(ctx, ins, infra, nil, reason, "running"); err != nil {
		t.Fatalf("erstes running: %v", err)
	}
	if _, err := pool.Exec(ctx, ins, infra, nil, reason, "running"); err == nil {
		t.Fatal("zweites running an derselben Anlage muss scheitern")
	}
	if _, err := pool.Exec(ctx, ins, infra, nil, reason, "done"); err == nil {
		t.Fatal("done ohne ended_at muss scheitern")
	}
	if _, err := pool.Exec(ctx, `UPDATE infrastructure SET setup_max_minutes = 0 WHERE id = $1`, infra); err == nil {
		t.Fatal("setup_max_minutes = 0 muss scheitern")
	}
	if _, err := pool.Exec(ctx, `UPDATE infrastructure SET setup_max_minutes = 60 WHERE id = $1`, infra); err != nil {
		t.Fatalf("setup_max_minutes = 60: %v", err)
	}

	// Invarianten auf dem migrierten Bestand: niemand, der Anlagen sehen bzw.
	// bearbeiten durfte, wird ausgesperrt (Rechte kommen aus der Migration).
	for _, c := range []struct{ from, to string }{
		{"infrastructure.view", "setup.view"},
		{"infrastructure.edit", "setup.view"},
		{"infrastructure.edit", "setup.edit"},
	} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp
			JOIN permissions o ON o.id = rp.permission_id AND o.key = $1
			WHERE NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions p ON p.id = x.permission_id
				WHERE x.role_id = rp.role_id AND p.key = $2)`, c.from, c.to).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d Rollen mit %s ohne %s: %v", n, c.from, c.to, err)
		}
	}
	// Die Administratorrolle hat setup.admin.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		JOIN roles r ON r.id = rp.role_id WHERE r.key = 'admin' AND p.key = 'setup.admin'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("setup.admin für admin: %d, %v", n, err)
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t WHERE t.tgrelid = 'setup_times'::regclass AND NOT t.tgisinternal`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Auslöser auf setup_times: %d, %v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE name = 'Störung durch Rüsten'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Kategorie: %d, %v", n, err)
	}
}

// Hat eine Rolle ein Quellrecht überall und ein anderes nur in einer
// Abteilung, gilt das abgeleitete Rüstzeiten-Recht überall (je Rolle und
// Recht gibt es nur eine Zeile).
func TestSetupMigrationPermissionScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../../migrations/159_setup_times.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	from := strings.Index(sql, "INSERT INTO role_permissions (role_id, permission_id, department_id)")
	to := strings.Index(sql, "-- Bereichsadministrator:")
	if from < 0 || to < from {
		t.Fatal("Rechte-Abschnitt der Migration nicht gefunden")
	}
	section := sql[from:to]

	var dept, roleA, roleB string
	must(t, pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('RZ-Abteilung') RETURNING id`).Scan(&dept))
	must(t, pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('rz_scope_a', 'RZ A') RETURNING id`).Scan(&roleA))
	must(t, pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('rz_scope_b', 'RZ B') RETURNING id`).Scan(&roleB))
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM roles WHERE id IN ($1, $2)`, roleA, roleB)
		pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1`, dept)
	})
	grant := func(role, key string, d any) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
			SELECT $1, id, $3 FROM permissions WHERE key = $2`, role, key, d)
		must(t, err)
	}
	grant(roleA, "infrastructure.view", dept) // ansehen nur in der Abteilung
	grant(roleA, "infrastructure.edit", nil)  // bearbeiten überall
	grant(roleB, "infrastructure.view", nil)  // ansehen überall
	grant(roleB, "infrastructure.edit", dept) // bearbeiten nur in der Abteilung

	if _, err := pool.Exec(ctx, section); err != nil {
		t.Fatal(err)
	}
	scope := func(role, key string) (found bool, global bool) {
		t.Helper()
		var d *string
		err := pool.QueryRow(ctx, `SELECT rp.department_id::text FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = $1 AND p.key = $2`, role, key).Scan(&d)
		if err != nil {
			return false, false
		}
		return true, d == nil
	}
	for _, c := range []struct{ role, key string }{{roleA, "setup.view"}, {roleA, "setup.edit"}, {roleB, "setup.view"}} {
		if found, global := scope(c.role, c.key); !found || !global {
			t.Errorf("%s soll überall gelten (gefunden %v, überall %v)", c.key, found, global)
		}
	}
	if found, global := scope(roleB, "setup.edit"); !found || global {
		t.Errorf("setup.edit von Rolle B soll auf die Abteilung begrenzt bleiben (gefunden %v, überall %v)", found, global)
	}
}
