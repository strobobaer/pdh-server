package netsync

import (
	"context"
	"os"
	"testing"
)

func TestITCategoriesMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	t.Run("Vorbelegungen", func(t *testing.T) {
		want := []struct {
			key, name string
			monitor   bool
			builtin   bool
		}{
			{"server", "Server", true, false},
			{"network", "Netzwerk", true, true},
			{"workstation", "Workstation", false, false},
			{"printer", "Drucker", true, false},
			{"phone", "Telefon", false, false},
			{"tablet", "Tablet", false, false},
			{"other", "Sonstiges", true, true},
		}
		for _, w := range want {
			var name string
			var monitor, builtin, active bool
			var grace *int
			err := pool.QueryRow(ctx, `SELECT name, monitor_default, builtin, active, grace_minutes
				FROM it_asset_categories WHERE key = $1`, w.key).Scan(&name, &monitor, &builtin, &active, &grace)
			if err != nil {
				t.Fatalf("%s: %v", w.key, err)
			}
			if name != w.name || monitor != w.monitor || builtin != w.builtin || !active || grace != nil {
				t.Errorf("%s: name=%q monitor=%v builtin=%v active=%v grace=%v", w.key, name, monitor, builtin, active, grace)
			}
		}
		var order string
		must(t, pool.QueryRow(ctx, `SELECT string_agg(key, ',' ORDER BY sort_order)
			FROM it_asset_categories WHERE key IN ('server','network','workstation','printer','phone','tablet','other')`).Scan(&order))
		if order != "server,network,workstation,printer,phone,tablet,other" {
			t.Errorf("Reihenfolge: %s", order)
		}
	})

	t.Run("type ist Text mit Fremdschlüssel und Index", func(t *testing.T) {
		var dt, def string
		must(t, pool.QueryRow(ctx, `SELECT data_type, column_default FROM information_schema.columns
			WHERE table_name = 'it_assets' AND column_name = 'type'`).Scan(&dt, &def))
		if dt != "text" {
			t.Errorf("data_type = %s", dt)
		}
		if def != "'other'::text" {
			t.Errorf("default = %s", def)
		}
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_it_type'`).Scan(&n))
		if n != 1 {
			t.Error("idx_it_type fehlt")
		}
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_type WHERE typname = 'it_asset_type'`).Scan(&n))
		if n != 0 {
			t.Error("Enum it_asset_type nicht gelöscht")
		}
		var onUpdate string
		must(t, pool.QueryRow(ctx, `SELECT confupdtype::text FROM pg_constraint WHERE conname = 'it_assets_type_fkey'`).Scan(&onUpdate))
		if onUpdate != "c" {
			t.Errorf("ON UPDATE = %s, erwartet c (CASCADE)", onUpdate)
		}
	})

	t.Run("unbekannter Typ scheitert, bekannter geht", func(t *testing.T) {
		var uid string
		must(t, pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&uid))
		tx, err := pool.Begin(ctx)
		must(t, err)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO it_assets (name, type, created_by) VALUES ('kat-test-unbekannt', 'gibts-nicht', $1::uuid)`, uid)
		if err == nil {
			t.Error("Insert mit unbekanntem Typ hätte scheitern müssen")
		}
		must(t, func() error { _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`); return e }())
		if _, err := tx.Exec(ctx, `INSERT INTO it_assets (name, type, created_by) VALUES ('kat-test-ok', 'printer', $1::uuid)`, uid); err != nil {
			t.Errorf("Insert mit bekanntem Typ: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO it_assets (name, net_grace_minutes, created_by) VALUES ('kat-test-grace', 0, $1::uuid)`, uid); err == nil {
			t.Error("net_grace_minutes = 0 hätte scheitern müssen")
		}
	})

	t.Run("Migration zweimal ausführbar", func(t *testing.T) {
		sql, err := os.ReadFile("../../../../migrations/165_it_categories.up.sql")
		must(t, err)
		tx, err := pool.Begin(ctx)
		must(t, err)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("zweiter Lauf: %v", err)
		}
		var n int
		must(t, tx.QueryRow(ctx, `SELECT count(*) FROM it_asset_categories`).Scan(&n))
		if n < 7 {
			t.Errorf("Kategorien nach zweitem Lauf: %d", n)
		}
	})

	t.Run("Umstellung vorhandener Assets bewahrt den Typ", func(t *testing.T) {
		sql, err := os.ReadFile("../../../../migrations/165_it_categories.up.sql")
		must(t, err)
		var uid string
		must(t, pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&uid))
		// Alles in einer Transaktion, die immer zurückgerollt wird.
		tx, err := pool.Begin(ctx)
		must(t, err)
		defer tx.Rollback(ctx)
		for _, q := range []string{
			`ALTER TABLE it_assets DROP CONSTRAINT it_assets_type_fkey`,
			`CREATE TYPE it_asset_type AS ENUM ('server','network','workstation','printer','phone','tablet','other')`,
			`ALTER TABLE it_assets ALTER COLUMN type DROP DEFAULT`,
			`ALTER TABLE it_assets ALTER COLUMN type TYPE it_asset_type USING type::it_asset_type`,
			`ALTER TABLE it_assets ALTER COLUMN type SET DEFAULT 'other'`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatalf("Altzustand herstellen (%s): %v", q, err)
			}
		}
		types := map[string]string{"kat-alt-printer": "printer", "kat-alt-phone": "phone", "kat-alt-network": "network"}
		for name, typ := range types {
			if _, err := tx.Exec(ctx, `INSERT INTO it_assets (name, type, created_by) VALUES ($1, $2::it_asset_type, $3::uuid)`, name, typ, uid); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("Migration auf Altzustand: %v", err)
		}
		for name, typ := range types {
			var got string
			must(t, tx.QueryRow(ctx, `SELECT type FROM it_assets WHERE name = $1`, name).Scan(&got))
			if got != typ {
				t.Errorf("%s: Typ %q statt %q", name, got, typ)
			}
		}
		var dt string
		must(t, tx.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
			WHERE table_name = 'it_assets' AND column_name = 'type'`).Scan(&dt))
		if dt != "text" {
			t.Errorf("data_type = %s", dt)
		}
		var n int
		must(t, tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'it_assets_type_fkey'`).Scan(&n))
		if n != 1 {
			t.Error("FK it_assets_type_fkey fehlt")
		}
		var valid bool
		must(t, tx.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = 'idx_it_type'`).Scan(&valid))
		if !valid {
			t.Error("idx_it_type ungültig")
		}
		must(t, tx.QueryRow(ctx, `SELECT count(*) FROM pg_type WHERE typname = 'it_asset_type'`).Scan(&n))
		if n != 0 {
			t.Error("Enum it_asset_type nicht gelöscht")
		}
	})

	t.Run("Recht it.categories", func(t *testing.T) {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key = 'it.categories' AND category = 'IT'`).Scan(&n))
		if n != 1 {
			t.Fatal("Recht it.categories fehlt")
		}
		// Jede Rolle mit it.admin hat it.categories.
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions a
			JOIN permissions pa ON pa.id = a.permission_id AND pa.key = 'it.admin'
			WHERE NOT EXISTS (SELECT 1 FROM role_permissions c
				JOIN permissions pc ON pc.id = c.permission_id AND pc.key = 'it.categories'
				WHERE c.role_id = a.role_id)`).Scan(&n))
		if n != 0 {
			t.Errorf("%d Rollen mit it.admin ohne it.categories", n)
		}
	})
}
