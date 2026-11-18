package web

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Eine ältere Sicherung spielt Migration 165 erneut ein, nachdem alle
// Fremdschlüssel entfernt wurden: die Migration legt it_assets_type_fkey neu an,
// das Wiederanlegen der gemerkten Fremdschlüssel darf dann nicht scheitern.
func TestRestoreForeignKeysSkipsExisting(t *testing.T) {
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
	sqlText, err := os.ReadFile("../../migrations/165_it_categories.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	skipLock := func(err error) {
		t.Helper()
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "55P03" {
			t.Skip("Sperre auf it_assets nicht erhältlich (geteilte Test-DB)")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`)
	skipLock(err)

	var f restoredFK
	skipLock(tx.QueryRow(ctx, `SELECT conrelid::regclass::text, conname, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conname = 'it_assets_type_fkey'`).Scan(&f.table, &f.name, &f.def))
	_, err = tx.Exec(ctx, `ALTER TABLE it_assets DROP CONSTRAINT it_assets_type_fkey`)
	skipLock(err)
	_, err = tx.Exec(ctx, string(sqlText)) // legt den FK neu an
	skipLock(err)

	if _, err := restoreForeignKeys(ctx, tx, []restoredFK{f}); err != nil {
		t.Fatalf("restoreForeignKeys: %v", err)
	}
	var n int
	skipLock(tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'it_assets_type_fkey'`).Scan(&n))
	if n != 1 {
		t.Errorf("it_assets_type_fkey %d-mal vorhanden, erwartet 1", n)
	}
}

// Dasselbe für Migration 167 (SNMP): Die Sicherung nimmt alle Tabellen des Schemas mit,
// also auch it_snmp_profiles und it_snmp_targets. Spielt eine ältere Sicherung die
// Migration erneut ein, gibt es den Fremdschlüssel der Geräteliste danach genau einmal –
// und er schützt weiter das benutzte Profil (ON DELETE RESTRICT).
func TestRestoreForeignKeysSNMP(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	tables, err := publicTables(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]bool{}
	for _, tb := range tables {
		in[tb] = true
	}
	if !in["it_snmp_profiles"] || !in["it_snmp_targets"] {
		t.Fatalf("SNMP-Tabellen fehlen in der Sicherung: %v %v", in["it_snmp_profiles"], in["it_snmp_targets"])
	}
	sqlText, err := os.ReadFile("../../migrations/167_it_snmp.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	skipLock := func(err error) {
		t.Helper()
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "55P03" {
			t.Skip("Sperre auf it_snmp_targets nicht erhältlich (geteilte Test-DB)")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`)
	skipLock(err)
	var fks []restoredFK
	rows, err := tx.Query(ctx, `SELECT conrelid::regclass::text, conname, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE contype = 'f' AND conrelid = 'it_snmp_targets'::regclass`)
	skipLock(err)
	for rows.Next() {
		var f restoredFK
		if err := rows.Scan(&f.table, &f.name, &f.def); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, f)
	}
	rows.Close()
	if len(fks) != 1 || !strings.Contains(fks[0].def, "ON DELETE RESTRICT") {
		t.Fatalf("Fremdschlüssel der Geräteliste: %+v", fks)
	}
	_, err = tx.Exec(ctx, `ALTER TABLE it_snmp_targets DROP CONSTRAINT `+fks[0].name)
	skipLock(err)
	_, err = tx.Exec(ctx, string(sqlText)) // ältere Sicherung: Migration erneut
	skipLock(err)
	if _, err := restoreForeignKeys(ctx, tx, fks); err != nil {
		t.Fatalf("restoreForeignKeys: %v", err)
	}
	var n int
	skipLock(tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conrelid = 'it_snmp_targets'::regclass AND confdeltype = 'r'`).Scan(&n))
	if n != 1 {
		t.Errorf("Fremdschlüssel %d-mal vorhanden, erwartet 1", n)
	}
}
