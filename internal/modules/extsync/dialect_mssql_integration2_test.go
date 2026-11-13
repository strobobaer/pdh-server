package extsync

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// scanRows liest alle Zeilen als Maps (Spaltenname -> Wert).
func scanRows(t *testing.T, rows *sql.Rows) []map[string]any {
	t.Helper()
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDescribeUniqueKeysLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ti, err := d.Describe(context.Background(), db, "dbo", "MeldungenGuid")
	if err != nil {
		t.Fatal(err)
	}
	if len(ti.UniqueKeys) != 1 || strings.Join(ti.UniqueKeys[0], ",") != "Id,Werk" {
		t.Fatalf("UniqueKeys = %v", ti.UniqueKeys)
	}
	if !KeyIsUnique(ti, []string{"Werk", "Id"}) || KeyIsUnique(ti, []string{"Id"}) {
		t.Fatal("KeyIsUnique stimmt nicht")
	}
	mi, err := d.Describe(context.Background(), db, "dbo", "Meldungen")
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.UniqueKeys) != 1 || mi.UniqueKeys[0][0] != "MeldungNr" {
		t.Fatalf("UniqueKeys = %v", mi.UniqueKeys)
	}
}

// SelectRows -> KeyValues -> UpdateRow / SelectOne, durchgehend, auf beiden Tabellen.
func TestKeyRoundTripLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tc := range []struct {
		table string
		keys  []string
		col   string
	}{
		{"Meldungen", []string{"MeldungNr"}, "Zustand"},
		{"MeldungenGuid", []string{"Id", "Werk"}, "Zustand"},
	} {
		ti, err := d.Describe(ctx, db, "dbo", tc.table)
		if err != nil {
			t.Fatal(err)
		}
		sel, err := d.SelectRows(ti, []string{tc.col}, tc.keys, "", "none", "", "", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := tx.QueryContext(ctx, sel.SQL, sel.Args...)
		if err != nil {
			t.Fatalf("%s: %v", sel.SQL, err)
		}
		rows := scanRows(t, rs)
		if len(rows) < 2 {
			t.Fatalf("%s: zu wenige Zeilen", tc.table)
		}
		for _, row := range rows {
			kv, err := d.KeyValues(ti, tc.keys, row)
			if err != nil {
				t.Fatal(err)
			}
			up, err := d.UpdateRow(ti, tc.keys, kv, map[string]any{tc.col: "geprüft"})
			if err != nil {
				t.Fatal(err)
			}
			res, err := tx.ExecContext(ctx, up.SQL, up.Args...)
			if err != nil {
				t.Fatalf("%s: %v", up.SQL, err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatalf("%s %v: RowsAffected = %d", tc.table, kv, n)
			}
			one, err := d.SelectOne(ti, []string{tc.col}, tc.keys, kv)
			if err != nil {
				t.Fatal(err)
			}
			r1, err := tx.QueryContext(ctx, one.SQL, one.Args...)
			if err != nil {
				t.Fatal(err)
			}
			got := scanRows(t, r1)
			if len(got) != 1 || got[0][tc.col] != "geprüft" {
				t.Fatalf("%s %v: SelectOne = %v", tc.table, kv, got)
			}
			kv2, err := d.KeyValues(ti, tc.keys, got[0])
			if err != nil || strings.Join(kv2, "|") != strings.Join(kv, "|") {
				t.Fatalf("Schlüssel aus SelectOne %v weicht ab von %v (%v)", kv2, kv, err)
			}
		}
	}
}

// Fünf Zeilen mit gleichem Zeitstempel, Seitengröße 2: jede Zeile genau einmal.
func TestTimestampKeysetPagingLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	src, err := d.Describe(ctx, db, "dbo", "Meldungen")
	if err != nil {
		t.Fatal(err)
	}
	kt, err := d.Describe(ctx, db, "dbo", "MeldungKommentare")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	same := time.Date(2026, 11, 13, 8, 0, 0, 123456700, time.FixedZone("p2", 2*3600))
	for i := 0; i < 5; i++ {
		ins, err := d.InsertChild(src, kt, map[string]any{"MeldungNr": int64(1), "Text": fmt.Sprint("gleich ", i), "Zeit": same})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, ins.SQL, ins.Args...); err != nil {
			t.Fatal(err)
		}
	}
	var want int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.MeldungKommentare WHERE Zeit IS NOT NULL").Scan(&want); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		st, err := d.SelectRows(kt, []string{"Text"}, []string{"Id"}, "Zeit", "timestamp", cursor, "", 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := tx.QueryContext(ctx, st.SQL, st.Args...)
		if err != nil {
			t.Fatalf("%s: %v", st.SQL, err)
		}
		rows := scanRows(t, rs)
		if len(rows) == 0 {
			break
		}
		pages++
		for _, r := range rows {
			k := fmt.Sprint(r["Id"])
			if seen[k] {
				t.Fatalf("Zeile %s doppelt gelesen", k)
			}
			seen[k] = true
		}
		cursor, err = d.NextCursor(kt, []string{"Id"}, "Zeit", "timestamp", rows[len(rows)-1])
		if err != nil {
			t.Fatal(err)
		}
		if pages > 20 {
			t.Fatal("Schleife ohne Ende")
		}
	}
	if len(seen) != want {
		t.Fatalf("%d Zeilen gelesen, erwartet %d", len(seen), want)
	}
	if pages < 3 {
		t.Fatalf("nur %d Seiten", pages)
	}
}

// Eine mit fremder Zeitzone geschriebene Zeit kommt als derselbe Zeitpunkt zurück.
func TestTimeRoundTripLab(t *testing.T) {
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
	for _, loc := range []*time.Location{time.FixedZone("plus5", 5*3600), time.FixedZone("minus8", -8*3600), time.UTC} {
		at := time.Date(2026, 11, 13, 15, 30, 45, 123456700, loc)
		up, err := d.UpdateRow(ti, []string{"MeldungNr"}, []string{"2"}, map[string]any{"Faellig": at})
		if err != nil {
			t.Fatal(err)
		}
		res, err := tx.ExecContext(ctx, up.SQL, up.Args...)
		if err != nil {
			t.Fatalf("%s: %v", up.SQL, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("RowsAffected = %d", n)
		}
		one, _ := d.SelectOne(ti, []string{"Faellig"}, []string{"MeldungNr"}, []string{"2"})
		rs, err := tx.QueryContext(ctx, one.SQL, one.Args...)
		if err != nil {
			t.Fatal(err)
		}
		rows := scanRows(t, rs)
		got, ok := rows[0]["Faellig"].(time.Time)
		if !ok || !got.Equal(at) {
			t.Fatalf("%v: zurückgelesen %v", loc, rows[0]["Faellig"])
		}
	}
}
