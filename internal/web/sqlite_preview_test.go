package web

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func writeTestSqliteDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sensors (name TEXT, wert REAL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sensors (name, wert) VALUES ('Halle 1', 21.4), ('Halle 2', 19.8)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return path
}

func TestListSqliteTables(t *testing.T) {
	path := writeTestSqliteDB(t)
	db, err := openSqlite(path)
	if err != nil {
		t.Fatalf("openSqlite: %v", err)
	}
	defer db.Close()
	tables, err := listSqliteTables(db)
	if err != nil {
		t.Fatalf("listSqliteTables: %v", err)
	}
	if len(tables) != 1 || tables[0] != "sensors" {
		t.Fatalf("unexpected tables: %+v", tables)
	}
}

func TestReadSqlitePreview(t *testing.T) {
	path := writeTestSqliteDB(t)
	cols, rows, err := readSqlitePreview(path, "sensors")
	if err != nil {
		t.Fatalf("readSqlitePreview: %v", err)
	}
	if len(cols) != 2 || cols[0] != "name" || cols[1] != "wert" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 2 || rows[0][0] != "Halle 1" || rows[1][0] != "Halle 2" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestReadSqliteLastRow(t *testing.T) {
	path := writeTestSqliteDB(t)
	db, err := openSqlite(path)
	if err != nil {
		t.Fatalf("openSqlite: %v", err)
	}
	defer db.Close()
	values, err := readSqliteLastRow(db, "sensors", []string{"name", "wert"})
	if err != nil {
		t.Fatalf("readSqliteLastRow: %v", err)
	}
	if values["name"] != "Halle 2" {
		t.Fatalf("expected last row to be Halle 2, got %+v", values)
	}
}

func TestOpenSqliteMissingFile(t *testing.T) {
	if _, err := openSqlite(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("expected error for missing/invalid file")
	}
}

func TestOpenSqliteNoPath(t *testing.T) {
	if _, err := openSqlite(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}
