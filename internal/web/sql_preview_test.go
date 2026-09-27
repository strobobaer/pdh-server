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

func TestSQLBrowsableKind(t *testing.T) {
	cases := map[string]bool{"sqlite": true, "mysql": true, "mssql": true, "excel": false, "mqtt": false, "web": false}
	for kind, want := range cases {
		if got := sqlBrowsableKind(kind); got != want {
			t.Errorf("sqlBrowsableKind(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("mysql", "a`b"); got != "`a``b`" {
		t.Errorf("mysql quoteIdent = %q", got)
	}
	if got := quoteIdent("sqlite", `a"b`); got != `"a""b"` {
		t.Errorf("sqlite quoteIdent = %q", got)
	}
	if got := quoteIdent("mssql", "a]b"); got != "[a]]b]" {
		t.Errorf("mssql quoteIdent = %q", got)
	}
}

func TestListSQLTablesSqlite(t *testing.T) {
	path := writeTestSqliteDB(t)
	db, err := openSqliteDB(path)
	if err != nil {
		t.Fatalf("openSqliteDB: %v", err)
	}
	defer db.Close()
	tables, err := listSQLTables(db, "sqlite")
	if err != nil {
		t.Fatalf("listSQLTables: %v", err)
	}
	if len(tables) != 1 || tables[0] != "sensors" {
		t.Fatalf("unexpected tables: %+v", tables)
	}
}

func TestReadSQLPreviewSqlite(t *testing.T) {
	path := writeTestSqliteDB(t)
	db, err := openSqliteDB(path)
	if err != nil {
		t.Fatalf("openSqliteDB: %v", err)
	}
	defer db.Close()
	cols, rows, err := readSQLPreview(db, "sqlite", "sensors")
	if err != nil {
		t.Fatalf("readSQLPreview: %v", err)
	}
	if len(cols) != 2 || cols[0] != "name" || cols[1] != "wert" {
		t.Fatalf("unexpected columns: %+v", cols)
	}
	if len(rows) != 2 || rows[0][0] != "Halle 1" || rows[1][0] != "Halle 2" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestReadSQLLastRowSqlite(t *testing.T) {
	path := writeTestSqliteDB(t)
	db, err := openSqliteDB(path)
	if err != nil {
		t.Fatalf("openSqliteDB: %v", err)
	}
	defer db.Close()
	values, err := readSQLLastRow(db, "sqlite", "sensors", []string{"name", "wert"})
	if err != nil {
		t.Fatalf("readSQLLastRow: %v", err)
	}
	if values["name"] != "Halle 2" {
		t.Fatalf("expected last row to be Halle 2, got %+v", values)
	}
}

func TestOpenSqliteDBMissingFile(t *testing.T) {
	if _, err := openSqliteDB(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("expected error for missing/invalid file")
	}
}

func TestOpenSqliteDBNoPath(t *testing.T) {
	if _, err := openSqliteDB(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestOpenMysqlDBNoHost(t *testing.T) {
	if _, err := openMysqlDB(map[string]string{}); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestOpenMysqlDBUnreachable(t *testing.T) {
	// Port 1 is reserved/unroutable - connection must fail fast-ish rather than hang or succeed.
	_, err := openMysqlDB(map[string]string{"host": "127.0.0.1", "port": "1", "database": "x", "username": "x", "password": "x"})
	if err == nil {
		t.Fatal("expected error for unreachable mysql host")
	}
}

func TestOpenMssqlDBNoHost(t *testing.T) {
	if _, err := openMssqlDB(map[string]string{}); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestOpenMssqlDBUnreachable(t *testing.T) {
	_, err := openMssqlDB(map[string]string{"host": "127.0.0.1", "port": "1", "database": "x", "username": "x", "password": "x"})
	if err == nil {
		t.Fatal("expected error for unreachable mssql host")
	}
}
