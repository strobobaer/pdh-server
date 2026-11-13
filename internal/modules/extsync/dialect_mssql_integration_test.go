package extsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

// openLab öffnet das Labor-SQL-Server; ohne PDH_TEST_MSSQL_DSN wird übersprungen.
func openLab(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_MSSQL_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_MSSQL_DSN nicht gesetzt")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDescribeLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()

	tabs, err := d.Tables(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, x := range tabs {
		names[x.Schema+"."+x.Name] = true
		if len(x.Columns) != 0 {
			t.Fatalf("Tables soll nur Namen liefern: %+v", x)
		}
	}
	for _, n := range []string{"dbo.Meldungen", "dbo.MeldungKommentare", "dbo.MeldungenGuid"} {
		if !names[n] {
			t.Errorf("Tabelle %s fehlt in %v", n, names)
		}
	}

	ti, err := d.Describe(ctx, db, "dbo", "Meldungen")
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]Column{}
	for _, c := range ti.Columns {
		cols[c.Name] = c
	}
	if c := cols["MeldungNr"]; !c.IsKey || !c.IsIdentity || c.Nullable {
		t.Errorf("MeldungNr: %+v", c)
	}
	if c := cols["RV"]; !c.IsRowVersion {
		t.Errorf("RV: %+v", c)
	}
	if c := cols["Titel"]; c.MaxLen != 200 || c.Nullable || c.IsKey {
		t.Errorf("Titel: %+v", c)
	}
	if c := cols["Beschreibung"]; c.MaxLen != 0 || !c.Nullable {
		t.Errorf("Beschreibung (MAX): %+v", c)
	}
	if len(ti.Triggers) != 1 || ti.Triggers[0] != "trg_Meldungen_Geaendert" {
		t.Errorf("Auslöser: %v", ti.Triggers)
	}

	if _, err := d.Describe(ctx, db, "dbo", "GibtEsNicht"); err == nil || !strings.Contains(err.Error(), ErrNotFound.Error()) {
		t.Errorf("unbekannte Tabelle: %v", err)
	}
}

func TestSelectRowsCursor(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	ti, err := d.Describe(ctx, db, "dbo", "Meldungen")
	if err != nil {
		t.Fatal(err)
	}
	read := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}, cursor string) []map[string]any {
		st, err := d.SelectRows(ti, []string{"Titel", "Zustand"}, []string{"MeldungNr"}, "RV", "rowversion", cursor, "", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := q.QueryContext(ctx, st.SQL, st.Args...)
		if err != nil {
			t.Fatalf("%s: %v", st.SQL, err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var nr int64
			var titel, zustand string
			var rv []byte
			if err := rows.Scan(&nr, &titel, &zustand, &rv); err != nil {
				t.Fatal(err)
			}
			out = append(out, map[string]any{"nr": nr, "rv": hex.EncodeToString(rv)})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	all := read(db, "")
	if len(all) < 5 {
		t.Fatalf("erwartet mindestens 5 Zeilen, %d", len(all))
	}
	maxRV := all[len(all)-1]["rv"].(string)
	if got := read(db, maxRV); len(got) != 0 {
		t.Fatalf("nach dem höchsten Stand dürfen keine Zeilen kommen: %v", got)
	}

	// In einer Transaktion eine Zeile ändern: nur sie kommt nach dem Cursor.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	up, err := d.UpdateRow(ti, []string{"MeldungNr"}, []string{"3"}, map[string]any{"Prio": "mittel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, up.SQL, up.Args...); err != nil {
		t.Fatal(err)
	}
	got := read(tx, maxRV)
	if len(got) != 1 || got[0]["nr"].(int64) != 3 {
		t.Fatalf("erwartet nur Zeile 3, bekam %v", got)
	}
}

func TestUpdateRowHitsOneRow(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	ti, err := d.Describe(ctx, db, "dbo", "Meldungen")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	st, err := d.UpdateRow(ti, []string{"MeldungNr"}, []string{"1"}, map[string]any{
		"Titel": "Pumpe 3 tropft (geändert)", "Faellig": time.Now().UTC(), "Prio": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tx.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		t.Fatalf("%s: %v", st.SQL, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d, erwartet 1", n)
	}
	// Schlüssel, den es nicht gibt: null Zeilen.
	st, _ = d.UpdateRow(ti, []string{"MeldungNr"}, []string{"999999"}, map[string]any{"Titel": "x"})
	res, err = tx.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("RowsAffected = %d, erwartet 0", n)
	}
	// SelectOne sieht die Änderung in der Transaktion.
	one, err := d.SelectOne(ti, []string{"Titel"}, []string{"MeldungNr"}, []string{"1"})
	if err != nil {
		t.Fatal(err)
	}
	var nr int64
	var titel string
	if err := tx.QueryRowContext(ctx, one.SQL, one.Args...).Scan(&nr, &titel); err != nil {
		t.Fatal(err)
	}
	if titel != "Pumpe 3 tropft (geändert)" {
		t.Fatalf("Titel = %q", titel)
	}

	// Kommentar anhängen (in derselben Transaktion, wird zurückgerollt).
	kt, err := d.Describe(ctx, db, "dbo", "MeldungKommentare")
	if err != nil {
		t.Fatal(err)
	}
	ins, err := d.InsertChild(ti, kt, map[string]any{
		"MeldungNr": int64(1), "Text": "Test'; DROP TABLE x;--", "Verfasser": "PDH", "Zeit": time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err = tx.ExecContext(ctx, ins.SQL, ins.Args...)
	if err != nil {
		t.Fatalf("%s: %v", ins.SQL, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("INSERT RowsAffected = %d", n)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var cnt int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dbo.Meldungen WHERE Titel = @p1`, "Pumpe 3 tropft (geändert)").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatal("Rollback hat die Änderung nicht zurückgenommen")
	}
}

func TestGuidCompositeKey(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	ti, err := d.Describe(ctx, db, "dbo", "MeldungenGuid")
	if err != nil {
		t.Fatal(err)
	}
	keys := 0
	for _, c := range ti.Columns {
		if c.IsKey {
			keys++
		}
	}
	if keys != 2 {
		t.Fatalf("erwartet 2 Schlüsselspalten, %d", keys)
	}
	var id, werk string
	if err := db.QueryRowContext(ctx, `SELECT TOP (1) CAST(Id AS NVARCHAR(36)), Werk FROM dbo.MeldungenGuid ORDER BY Werk`).Scan(&id, &werk); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	st, err := d.UpdateRow(ti, []string{"Id", "Werk"}, []string{id, werk}, map[string]any{"Zustand": "in Arbeit"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tx.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		t.Fatalf("%s: %v", st.SQL, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d, erwartet 1", n)
	}
	one, err := d.SelectOne(ti, []string{"Zustand"}, []string{"Id", "Werk"}, []string{id, werk})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(ctx, one.SQL, one.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if n != 1 {
		t.Fatalf("SelectOne lieferte %d Zeilen", n)
	}
}

// Ein Spaltenname mit ] kommt über Quote unverändert beim Server an.
func TestQuoteRoundTripLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	rows, err := db.QueryContext(context.Background(), "SELECT 1 AS "+d.Quote("Drop]Table"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "Drop]Table" {
		t.Fatalf("Spalten = %v", cols)
	}
}
