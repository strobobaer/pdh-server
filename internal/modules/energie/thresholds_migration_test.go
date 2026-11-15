package energie

import (
	"context"
	"os"
	"testing"
)

func TestThresholdMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var infra, meter string
	must(t, pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Schwellwert-Test', 'plant') RETURNING id::text`).Scan(&infra))
	must(t, pool.QueryRow(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id)
		VALUES ('SW-1', 'Strom Schwellwert', $1) RETURNING id::text`, infra).Scan(&meter))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE infrastructure_id = $1`, infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, infra)
	})

	const ins = `INSERT INTO energy_thresholds (meter_id, name, condition, limit_value, window_minutes)
		VALUES ($1, 'Grenze', $2, 10, $3) RETURNING id::text`

	var th string
	t.Run("Schwellwert anlegen mit Vorgaben", func(t *testing.T) {
		must(t, pool.QueryRow(ctx, ins, meter, "above", 5).Scan(&th))
		var reset int
		var prio, tag string
		var active bool
		must(t, pool.QueryRow(ctx, `SELECT reset_minutes, priority, tag, active FROM energy_thresholds WHERE id = $1`, th).
			Scan(&reset, &prio, &tag, &active))
		if reset != 15 || prio != "medium" || tag != "" || !active {
			t.Errorf("Vorgaben: reset=%d priority=%q tag=%q active=%v", reset, prio, tag, active)
		}
	})
	t.Run("unbekannte Bedingung scheitert", func(t *testing.T) {
		var id string
		if err := pool.QueryRow(ctx, ins, meter, "x", 5).Scan(&id); err == nil {
			t.Fatal("Bedingung x wurde angenommen")
		}
	})
	t.Run("Zeitraum 0 und 1441 scheitern", func(t *testing.T) {
		for _, w := range []int{0, 1441} {
			var id string
			if err := pool.QueryRow(ctx, ins, meter, "below", w).Scan(&id); err == nil {
				t.Fatalf("Zeitraum %d wurde angenommen", w)
			}
		}
	})
	t.Run("Rückschaltzeit 0 scheitert", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE energy_thresholds SET reset_minutes = 0 WHERE id = $1`, th); err == nil {
			t.Fatal("Rückschaltzeit 0 wurde angenommen")
		}
	})
	t.Run("unbekannte Priorität scheitert", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE energy_thresholds SET priority = 'x' WHERE id = $1`, th); err == nil {
			t.Fatal("Priorität x wurde angenommen")
		}
	})
	t.Run("Benutzer und Gruppe zugleich scheitert", func(t *testing.T) {
		var user, group string
		must(t, pool.QueryRow(ctx, `SELECT id::text FROM users LIMIT 1`).Scan(&user))
		must(t, pool.QueryRow(ctx, `INSERT INTO user_groups (name) VALUES ('Schwellwert-Testgruppe') RETURNING id::text`).Scan(&group))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_groups WHERE id = $1`, group) })
		if _, err := pool.Exec(ctx, `UPDATE energy_thresholds SET responsible_user_id = $2, responsible_group_id = $3 WHERE id = $1`, th, user, group); err == nil {
			t.Fatal("Benutzer und Gruppe zugleich wurden angenommen")
		}
		_, err := pool.Exec(ctx, `UPDATE energy_thresholds SET responsible_group_id = $2 WHERE id = $1`, th, group)
		must(t, err)
	})
	t.Run("Zustand: Vorgabe normal, Löschen der Messstelle räumt auf", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO energy_threshold_state (threshold_id) VALUES ($1)`, th)
		must(t, err)
		var st string
		must(t, pool.QueryRow(ctx, `SELECT state FROM energy_threshold_state WHERE threshold_id = $1`, th).Scan(&st))
		if st != "normal" {
			t.Errorf("Zustand = %q", st)
		}
		if _, err := pool.Exec(ctx, `UPDATE energy_threshold_state SET state = 'x' WHERE threshold_id = $1`, th); err == nil {
			t.Error("Zustand x wurde angenommen")
		}
		_, err = pool.Exec(ctx, `DELETE FROM energy_meters WHERE id = $1`, meter)
		must(t, err)
		var n int
		must(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM energy_thresholds WHERE id = $1)
			+ (SELECT count(*) FROM energy_threshold_state WHERE threshold_id = $1)`, th).Scan(&n))
		if n != 0 {
			t.Errorf("%d Zeilen nach Löschen der Messstelle übrig", n)
		}
	})
	t.Run("tickets.requires_root_cause Vorgabe false", func(t *testing.T) {
		var def string
		must(t, pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
			WHERE table_name = 'tickets' AND column_name = 'requires_root_cause'`).Scan(&def))
		if def != "false" {
			t.Errorf("Vorgabe = %q", def)
		}
	})
}

func TestThresholdPermissionKeepsDepartment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var cat string
	must(t, pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'energy.thresholds'`).Scan(&cat))
	if cat != "Daten & Schnittstellen" {
		t.Errorf("Kategorie = %q", cat)
	}

	var dept, role string
	must(t, pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Schwellwert-Abteilung') RETURNING id::text`).Scan(&dept))
	must(t, pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('sw_test_rolle', 'Schwellwert-Testrolle') RETURNING id::text`).Scan(&role))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM roles WHERE id = $1`, role)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id = $1`, dept)
	})
	_, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT $1, id, $2 FROM permissions WHERE key = 'energy.edit'`, role, dept)
	must(t, err)

	// Die Migration noch einmal ausführen (idempotent): die neu angelegte Rolle
	// muss das neue Recht mit derselben Abteilung bekommen.
	sqlText, err := os.ReadFile("../../../migrations/163_energy_thresholds.up.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(sqlText))
	must(t, err)

	var got *string
	n := 0
	rows, err := pool.Query(ctx, `SELECT rp.department_id::text FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id AND p.key = 'energy.thresholds'
		WHERE rp.role_id = $1`, role)
	must(t, err)
	for rows.Next() {
		must(t, rows.Scan(&got))
		n++
	}
	rows.Close()
	if n != 1 || got == nil || *got != dept {
		t.Fatalf("energy.thresholds: %d Zeilen, Abteilung %v, erwartet genau eine in %s", n, got, dept)
	}
}
