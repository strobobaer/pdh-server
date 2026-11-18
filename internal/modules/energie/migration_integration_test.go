package energie

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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnergyMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var infra, connID, mapID string
	must(t, pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Energie-Test', 'plant') RETURNING id::text`).Scan(&infra))
	must(t, pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mqtt', 'energie-test') RETURNING id::text`).Scan(&connID))
	must(t, pool.QueryRow(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name)
		VALUES ($1, 'energie/test', $2, 'Energie-Test') RETURNING id::text`, connID, infra).Scan(&mapID))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE infrastructure_id = $1`, infra)
		_, _ = pool.Exec(c, `DELETE FROM import_mappings WHERE id = $1`, mapID)
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1`, connID)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, infra)
	})

	var first string
	must(t, pool.QueryRow(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id)
		VALUES ('EM-1', 'Strom Halle', $1) RETURNING id::text`, infra).Scan(&first))

	t.Run("gleiche Messstellen-ID scheitert", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id) VALUES ('EM-1', 'Doppelt', $1)`, infra)
		if err == nil {
			t.Fatal("zweite Messstelle mit gleicher ID wurde angelegt")
		}
	})
	t.Run("Faktor 0 scheitert", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, factor) VALUES ('EM-F0', 'Faktor', $1, 0)`, infra)
		if err == nil {
			t.Fatal("Faktor 0 wurde angenommen")
		}
	})
	t.Run("unbekannte Art scheitert", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind) VALUES ('EM-X', 'Art', $1, 'x')`, infra)
		if err == nil {
			t.Fatal("Art x wurde angenommen")
		}
	})
	t.Run("Formel und Quelle zugleich scheitert", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, formula, import_mapping_id)
			VALUES ('EM-FQ', 'Beides', $1, '[EM-1] * 2', $2)`, infra, mapID)
		if err == nil {
			t.Fatal("Formel zusammen mit Quelle wurde angenommen")
		}
	})
	t.Run("Löschen der Zuordnung setzt Quelle auf NULL", func(t *testing.T) {
		var id string
		must(t, pool.QueryRow(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, import_mapping_id)
			VALUES ('EM-Q', 'Quelle', $1, $2) RETURNING id::text`, infra, mapID).Scan(&id))
		_, err := pool.Exec(ctx, `DELETE FROM import_mappings WHERE id = $1`, mapID)
		must(t, err)
		var isNull bool
		must(t, pool.QueryRow(ctx, `SELECT import_mapping_id IS NULL FROM energy_meters WHERE id = $1`, id).Scan(&isNull))
		if !isNull {
			t.Fatal("import_mapping_id nicht auf NULL gesetzt")
		}
	})
}

func TestEnergyPermissionsGranted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT key, category FROM permissions WHERE key LIKE 'energy.%'`)
	must(t, err)
	cats := map[string]string{}
	for rows.Next() {
		var k, c string
		must(t, rows.Scan(&k, &c))
		cats[k] = c
	}
	must(t, rows.Err())
	for _, k := range []string{"energy.view", "energy.edit", "energy.admin"} {
		if cats[k] != "Daten & Schnittstellen" {
			t.Errorf("Recht %s: Kategorie %q", k, cats[k])
		}
	}

	// Jede Rolle mit globalem export.write hat energy.view und energy.edit.
	var missing int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM roles r
		JOIN role_permissions rp ON rp.role_id = r.id AND rp.department_id IS NULL
		JOIN permissions w ON w.id = rp.permission_id AND w.key = 'export.write'
		CROSS JOIN (VALUES ('energy.view'), ('energy.edit')) AS need(k)
		WHERE NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions px ON px.id = x.permission_id
		                   WHERE x.role_id = r.id AND x.department_id IS NULL AND px.key = need.k)`).Scan(&missing))
	if missing != 0 {
		t.Errorf("%d fehlende energy.view/energy.edit bei Rollen mit export.write", missing)
	}

	// Rolle admin hat energy.admin.
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM roles r
		JOIN role_permissions rp ON rp.role_id = r.id
		JOIN permissions p ON p.id = rp.permission_id AND p.key = 'energy.admin'
		WHERE r.key = 'admin'`).Scan(&n))
	if n != 1 {
		t.Errorf("Rolle admin hat energy.admin %d-mal, erwartet 1", n)
	}
}
