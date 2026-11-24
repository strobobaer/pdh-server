package energie

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// seedVersions wiederholt den Satz aus Migration 218, der jeder berechneten
// Messstelle ihre erste Fassung gibt.
const seedVersions = `INSERT INTO energy_formula_versions (meter_id, valid_from, formula, created_by_name, created_at)
SELECT id, created_at::date, formula, 'Stand beim Update', created_at FROM energy_meters m
 WHERE formula <> '' AND NOT EXISTS (SELECT 1 FROM energy_formula_versions v WHERE v.meter_id = m.id)`

func TestMigration218Versions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, c := range []struct{ table, col string }{
		{"energy_formula_versions", "valid_from"},
		{"energy_recalc_runs", "done_until"},
		{"energy_export_files", "first_end"},
		{"energy_export_files", "last_end"},
		{"energy_export_files", "fetched_at"},
	} {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, c.table, c.col).Scan(&n))
		if n != 1 {
			t.Fatalf("Spalte %s.%s fehlt", c.table, c.col)
		}
	}

	s := NewStore(pool)
	infra := newInfra(t, pool, "")
	_, aKey := virtual(t, s, infra, KindCounter, "1")
	vID, _ := virtual(t, s, infra, KindCounter, "["+aKey+"] * 2")

	_, err := pool.Exec(ctx, seedVersions)
	must(t, err)
	_, err = pool.Exec(ctx, seedVersions)
	must(t, err)
	var n int
	var formula string
	must(t, pool.QueryRow(ctx, `SELECT count(*), max(formula) FROM energy_formula_versions WHERE meter_id = $1::uuid`, vID).Scan(&n, &formula))
	if n != 1 || formula != "["+aKey+"] * 2" {
		t.Errorf("Fassungen von V: %d, Formel %q", n, formula)
	}

	// Höchstens ein Lauf gleichzeitig.
	ins := `INSERT INTO energy_recalc_runs (meter_id, meter_ids, meter_keys, from_date, days_total, state)
		VALUES ($1::uuid, ARRAY[$1::uuid], ARRAY['V'], '2026-11-13', 3, $2) RETURNING id::text`
	var first string
	must(t, pool.QueryRow(ctx, ins, vID, "running").Scan(&first))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM energy_recalc_runs WHERE meter_id = $1::uuid`, vID)
	})
	var second string
	err = pool.QueryRow(ctx, ins, vID, "running").Scan(&second)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23505" {
		t.Errorf("zweiter laufender Lauf: %v, erwartet 23505", err)
	}
	// Ein beendeter Lauf daneben ist erlaubt.
	must(t, pool.QueryRow(ctx, ins, vID, "done").Scan(&second))
}
