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

// pageAll liest eine Tabelle seitenweise mit dem Cursor des Dialekts und liefert die Schlüssel in Lesereihenfolge.
func pageAll(t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, d Dialect, ti *TableInfo, keys []string, changeCol, kind string, limit int) []string {
	t.Helper()
	var out []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 50 {
			t.Fatal("Schleife ohne Ende")
		}
		st, err := d.SelectRows(ti, nil, keys, changeCol, kind, cursor, "", 0, limit)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := q.QueryContext(context.Background(), st.SQL, st.Args...)
		if err != nil {
			t.Fatalf("%s: %v", st.SQL, err)
		}
		rows := scanRows(t, rs)
		if len(rows) == 0 {
			return out
		}
		for _, r := range rows {
			kv, err := d.KeyValues(ti, keys, r)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.Join(kv, "|"))
		}
		cursor, err = d.NextCursor(ti, keys, changeCol, kind, rows[len(rows)-1])
		if err != nil {
			t.Fatal(err)
		}
	}
}

// datetime-Spalte mit Gleichstand auf .003/.007/.010 ms und NULL-Zeilen: jede Zeile genau einmal.
func TestDatetimeTiesAndNullsLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	ti, err := d.Describe(ctx, db, "dbo", "MeldungenAlt")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := findColumn(ti, "Geaendert"); c == nil || c.Type != "datetime" || !c.Nullable {
		t.Fatalf("Geaendert: %+v", c)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	keys := []string{"Nr"}

	// Nur NULL: nichts wird gelesen, nichts blockiert.
	if got := pageAll(t, tx, d, ti, keys, "Geaendert", "timestamp", 2); len(got) != 0 {
		t.Fatalf("NULL-Zeilen dürfen nicht gelesen werden: %v", got)
	}

	base := time.Date(2026, 11, 13, 8, 15, 30, 0, time.UTC)
	set := func(nr string, ms int) {
		up, err := d.UpdateRow(ti, keys, []string{nr}, map[string]any{"Geaendert": base.Add(time.Duration(ms) * time.Millisecond)})
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
	}
	for _, nr := range []string{"1", "2", "3", "4", "5"} { // fünf Zeilen mit .007
		set(nr, 7)
	}
	set("6", 3)  // .003
	set("7", 3)  // .003 (Gleichstand)
	set("8", 10) // .010
	// 9 und 10 bleiben NULL.

	for _, limit := range []int{1, 2, 3, 4} {
		got := pageAll(t, tx, d, ti, keys, "Geaendert", "timestamp", limit)
		seen := map[string]bool{}
		for _, k := range got {
			if seen[k] {
				t.Fatalf("limit %d: Zeile %s doppelt in %v", limit, k, got)
			}
			seen[k] = true
		}
		if len(got) != 8 || seen["9"] || seen["10"] {
			t.Fatalf("limit %d: gelesen %v, erwartet die Zeilen 1 bis 8", limit, got)
		}
		// Reihenfolge: .003 (6,7), .007 (1..5), .010 (8)
		if strings.Join(got, ",") != "6,7,1,2,3,4,5,8" {
			t.Fatalf("limit %d: Reihenfolge %v", limit, got)
		}
	}
}

// Ohne Änderungsspalte: Schlüsselcursor liest die ganze Tabelle in Schlüsselfolge.
func TestNoneKeyPagingLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	for _, tc := range []struct {
		table string
		keys  []string
	}{
		{"Meldungen", []string{"MeldungNr"}},
		{"MeldungenGuid", []string{"Id", "Werk"}},
		{"MeldungenAlt", []string{"Nr"}},
	} {
		ti, err := d.Describe(ctx, db, "dbo", tc.table)
		if err != nil {
			t.Fatal(err)
		}
		var want int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo."+d.Quote(tc.table)).Scan(&want); err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 2, 3} {
			got := pageAll(t, db, d, ti, tc.keys, "", "none", limit)
			seen := map[string]bool{}
			for _, k := range got {
				if seen[k] {
					t.Fatalf("%s limit %d: %s doppelt", tc.table, limit, k)
				}
				seen[k] = true
			}
			if len(got) != want {
				t.Fatalf("%s limit %d: %d gelesen, erwartet %d", tc.table, limit, len(got), want)
			}
		}
	}
}

// varchar-Schlüssel unter der SQL-Kollation des Labors: Schlüsselfolge, Gleichstand, Aktualisieren.
func TestVarcharKeyLab(t *testing.T) {
	db := openLab(t)
	d := mustDialect(t)
	ctx := context.Background()
	ti, err := d.Describe(ctx, db, "dbo", "MeldungenText")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := findColumn(ti, "Kennung"); c == nil || c.Type != "varchar" || !c.IsKey {
		t.Fatalf("Kennung: %+v", c)
	}
	keys := []string{"Kennung"}
	order := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}, sqlText string) []string {
		rs, err := q.QueryContext(ctx, sqlText)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range scanRows(t, rs) {
			out = append(out, fmt.Sprint(r["Kennung"]))
		}
		return out
	}
	want := order(db, "SELECT Kennung FROM dbo.MeldungenText ORDER BY Kennung")
	if len(want) != 9 {
		t.Fatalf("Seed unvollständig: %v", want)
	}

	// Schlüsselcursor ohne Änderungsspalte.
	for _, limit := range []int{1, 2, 3} {
		got := pageAll(t, db, d, ti, keys, "", "none", limit)
		if strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
			t.Fatalf("none, limit %d: %v, erwartet %v", limit, got, want)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Alle Zeilen mit gleichem Zeitstempel: der Gleichstand läuft über den varchar-Schlüssel.
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	for _, k := range want {
		up, err := d.UpdateRow(ti, keys, []string{k}, map[string]any{"Geaendert": at})
		if err != nil {
			t.Fatal(err)
		}
		res, err := tx.ExecContext(ctx, up.SQL, up.Args...)
		if err != nil {
			t.Fatalf("%s: %v", up.SQL, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("Schlüssel %q: RowsAffected = %d", k, n)
		}
	}
	for _, limit := range []int{1, 2, 4} {
		got := pageAll(t, tx, d, ti, keys, "Geaendert", "timestamp", limit)
		if strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
			t.Fatalf("timestamp, limit %d: %v, erwartet %v", limit, got, want)
		}
	}

	// SelectRows -> KeyValues -> UpdateRow / SelectOne trifft genau die Zeile.
	sel, err := d.SelectRows(ti, []string{"Titel"}, keys, "", "none", "", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := tx.QueryContext(ctx, sel.SQL, sel.Args...)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range scanRows(t, rs) {
		kv, err := d.KeyValues(ti, keys, row)
		if err != nil {
			t.Fatal(err)
		}
		up, err := d.UpdateRow(ti, keys, kv, map[string]any{"Titel": "geprüft " + kv[0]})
		if err != nil {
			t.Fatal(err)
		}
		res, err := tx.ExecContext(ctx, up.SQL, up.Args...)
		if err != nil {
			t.Fatalf("%s: %v", up.SQL, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("%v: RowsAffected = %d", kv, n)
		}
		one, err := d.SelectOne(ti, []string{"Titel"}, keys, kv)
		if err != nil {
			t.Fatal(err)
		}
		r1, err := tx.QueryContext(ctx, one.SQL, one.Args...)
		if err != nil {
			t.Fatal(err)
		}
		got := scanRows(t, r1)
		if len(got) != 1 || got[0]["Titel"] != "geprüft "+kv[0] || got[0]["Kennung"] != kv[0] {
			t.Fatalf("%v: SelectOne = %v", kv, got)
		}
	}
}
