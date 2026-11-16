package web

import (
	"context"
	"errors"
	"os"
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
