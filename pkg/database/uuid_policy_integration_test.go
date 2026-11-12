//go:build integration

package database

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Jede ID-Spalte (uuid, Name id) einer Tabelle erzeugt neue Werte mit pdh_uuid_v7()
// (Migration 122): eindeutig und zeitlich sortiert. Neue Tabellen dürfen weder
// gen_random_uuid() noch eine Zahlenfolge als ID-Standard verwenden.
func TestUUIDv7Policy(t *testing.T) {
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
	rows, err := pool.Query(ctx, `
		SELECT c.table_name, COALESCE(c.column_default, '')
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND c.column_name = 'id'
		  AND (c.data_type <> 'uuid' OR COALESCE(c.column_default, '') <> 'pdh_uuid_v7()')
		  -- Altbestand vor 122 mit Zahlenfolge (nur intern, nie nach außen gegeben)
		  AND c.table_name NOT IN ('password_history', 'record_change_queue')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, def string
		if err := rows.Scan(&table, &def); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s.id: Standard %q statt pdh_uuid_v7() – in einer neuen Migration umstellen", table, def)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
