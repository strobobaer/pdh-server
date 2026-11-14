package ruesten

import (
	"context"
	"os"
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
