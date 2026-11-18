package netsync

import (
	"context"
	"os"
	"path/filepath"
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
	if err := database.RunMigrations(ctx, pool, "../../../../migrations"); err != nil {
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

func TestITNetsyncMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	t.Run("Spalten und Tabellen vorhanden", func(t *testing.T) {
		cols := map[string][]string{
			"it_assets":          {"net_state", "net_monitored", "net_gone", "firmware_update", "firmware", "net_uplink", "net_source", "net_external_id", "net_last_seen", "net_state_since", "net_role", "net_detached"},
			"it_asset_ports":     {"asset_id", "idx", "connector", "state", "speed_mbps", "max_speed_mbps", "poe_state"},
			"it_net_candidates":  {"source", "mac", "external_id", "name", "ip", "kind", "uplink", "last_seen", "ignored"},
			"it_net_alarm_state": {"asset_id", "state", "offline_since", "ticket_id"},
		}
		for table, list := range cols {
			for _, c := range list {
				var n int
				must(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
					WHERE table_name = $1 AND column_name = $2`, table, c).Scan(&n))
				if n != 1 {
					t.Errorf("%s.%s fehlt", table, c)
				}
			}
		}
	})

	t.Run("gleiche Quelle und ID scheitert", func(t *testing.T) {
		clean := func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM it_assets WHERE net_source = 'migtest' OR name = 'mig-hand-1'`)
		}
		clean()
		t.Cleanup(clean)
		_, err := pool.Exec(ctx, `INSERT INTO it_assets (name, net_source, net_external_id, created_by) VALUES ('A', 'migtest', 'x1', (SELECT id FROM users LIMIT 1))`)
		must(t, err)
		if _, err := pool.Exec(ctx, `INSERT INTO it_assets (name, net_source, net_external_id, created_by) VALUES ('B', 'migtest', 'x1', (SELECT id FROM users LIMIT 1))`); err == nil {
			t.Fatal("zweites Asset mit gleicher Quelle und ID wurde angenommen")
		}
		// Ohne Quelle (von Hand angelegt) darf es mehrfach geben.
		_, err = pool.Exec(ctx, `INSERT INTO it_assets (name, created_by) VALUES ('mig-hand-1', (SELECT id FROM users LIMIT 1)), ('mig-hand-1', (SELECT id FROM users LIMIT 1))`)
		must(t, err)
	})

	t.Run("Migration zweimal ausführbar", func(t *testing.T) {
		sql, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "164_it_netsync.up.sql"))
		must(t, err)
		tx, err := pool.Begin(ctx)
		must(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("zweiter Lauf: %v", err)
		}
		var def string
		must(t, tx.QueryRow(ctx, `SELECT column_default || '/' || is_nullable FROM information_schema.columns
			WHERE table_name = 'it_assets' AND column_name = 'net_detached'`).Scan(&def))
		if def != "false/NO" {
			t.Errorf("net_detached: %q, erwartet false/NO", def)
		}
	})

	t.Run("Recht it.netsync wie it.edit vergeben", func(t *testing.T) {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key = 'it.netsync' AND category = 'IT'`).Scan(&n))
		if n != 1 {
			t.Fatalf("Recht it.netsync fehlt, Anzahl %d", n)
		}
		var missing int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp
			JOIN permissions e ON e.id = rp.permission_id AND e.key = 'it.edit'
			WHERE NOT EXISTS (SELECT 1 FROM role_permissions r2
				JOIN permissions p ON p.id = r2.permission_id AND p.key = 'it.netsync'
				WHERE r2.role_id = rp.role_id)`).Scan(&missing))
		if missing != 0 {
			t.Errorf("%d Rollen mit it.edit ohne it.netsync", missing)
		}
	})
}
