//go:build integration

// Sammel-Anlage von Zuordnungen gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestBulk ./internal/web/
package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/pkg/database"
)

// mappingEnv: Benutzer, Import- und Export-Verbindung (CSV), zwei Anlagen.
type mappingEnv struct {
	pool                               *pgxpool.Pool
	h                                  *Handler
	uid, imp, exp, assetA, assetB, sfx string
}

func newMappingEnv(t *testing.T) *mappingEnv {
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
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	e := &mappingEnv{pool: pool, h: &Handler{db: pool}, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.uid = e.id(t, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Z', 'O', 'admin', '', '') RETURNING id::text`, "zu"+e.sfx)
	e.imp = e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'csv', $1, '{}'::jsonb) RETURNING id::text`, "Imp "+e.sfx)
	e.exp = e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('export', 'csv', $1, '{}'::jsonb) RETURNING id::text`, "Exp "+e.sfx)
	e.assetA = e.id(t, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse A "+e.sfx)
	e.assetB = e.id(t, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse B "+e.sfx)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = ANY($1::uuid[])`, []string{e.imp, e.exp})
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = ANY($1::uuid[])`, []string{e.assetA, e.assetB})
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, e.uid)
	})
	return e
}

func (e *mappingEnv) id(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *mappingEnv) count(t *testing.T, table, conn string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE connection_id = $1::uuid`, conn).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBulkImportMappings(t *testing.T) {
	e := newMappingEnv(t)
	onlyA := func(id string) bool { return id == e.assetA }
	res := e.h.bulkImportMappings(context.Background(), e.imp, e.uid, []importMappingCreateInput{
		{SourceRef: "Spalte 1", InfrastructureID: e.assetA, Name: "Strom"},
		{SourceRef: "Spalte 2", InfrastructureID: e.assetA, Name: "Spannung"},
		{SourceRef: "Spalte 1", InfrastructureID: e.assetA, Name: "Strom doppelt"},
		{SourceRef: "Spalte 3", InfrastructureID: e.assetB, Name: "Druck"},
		{SourceRef: "Spalte 4", InfrastructureID: e.assetA, Name: "x"},
	}, onlyA)
	if res.Created != 2 || res.Skipped != 1 || len(res.Errors) != 2 {
		t.Fatalf("Ergebnis: %+v", res)
	}
	if res.Errors[0].Index != 3 || res.Errors[0].Error != "Keine Berechtigung für diese Anlage" {
		t.Errorf("Anlage verboten: %+v", res.Errors[0])
	}
	if res.Errors[1].Index != 4 || !strings.Contains(res.Errors[1].Error, "Bezeichnung") {
		t.Errorf("Name zu kurz: %+v", res.Errors[1])
	}
	if n := e.count(t, "import_mappings", e.imp); n != 2 {
		t.Errorf("Zeilen: %d", n)
	}
	// unbekannte Anlage: Fehler statt Abbruch
	res = e.h.bulkImportMappings(context.Background(), e.imp, e.uid, []importMappingCreateInput{
		{SourceRef: "Spalte 9", InfrastructureID: "00000000-0000-0000-0000-000000000000", Name: "Fehlt"},
	}, func(string) bool { return true })
	if res.Created != 0 || len(res.Errors) != 1 {
		t.Errorf("unbekannte Anlage: %+v", res)
	}
}

func TestBulkExportMappings(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	w1, dup, err := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "S1", InfrastructureID: e.assetA, Name: "Strom"})
	if err != nil || dup {
		t.Fatal(err, dup)
	}
	w2, _, err := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "S2", InfrastructureID: e.assetB, Name: "Druck"})
	if err != nil {
		t.Fatal(err)
	}
	if a, err := e.h.exportSourceAsset(ctx, w2); err != nil || a != e.assetB {
		t.Errorf("Anlage des Werts: %q %v", a, err)
	}
	res := e.h.bulkExportMappings(ctx, e.exp, e.uid, []exportMappingCreateInput{
		{ImportMappingID: w1, FieldName: "Presse_A_Strom"},
		{ImportMappingID: w1, FieldName: "nochmal"},
		{ImportMappingID: w2, FieldName: "Presse_B_Druck"},
	}, func(id string) bool { return id == e.assetA })
	if res.Created != 1 || res.Skipped != 1 || len(res.Errors) != 1 || res.Errors[0].Index != 2 {
		t.Fatalf("Ergebnis: %+v", res)
	}
	if n := e.count(t, "export_mappings", e.exp); n != 1 {
		t.Errorf("Zeilen: %d", n)
	}
}

func TestMappingSourcesCSV(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	path := t.TempDir() + "/werte.csv"
	if err := os.WriteFile(path, []byte("Strom;Spannung\n1;230\n2;231\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "Strom", InfrastructureID: e.assetA, Name: "Strom"}); err != nil {
		t.Fatal(err)
	}
	set := e.h.mappingSources(ctx, e.imp, "csv", map[string]string{"source_path": path, "delimiter": ";", "has_header": "true"})
	if len(set.Sources) != 1 || set.Sources[0].Ref != "Spannung" || set.Sources[0].Sample != "231" {
		t.Errorf("nur die offene Spalte mit letztem Wert: %+v (Hinweise %v)", set.Sources, set.Hints)
	}
	set = e.h.mappingSources(ctx, e.imp, "csv", map[string]string{"source_path": path + ".fehlt", "delimiter": ";"})
	if len(set.Sources) != 0 || len(set.Hints) == 0 {
		t.Errorf("fehlende Datei wird Hinweis: %+v", set)
	}
}
