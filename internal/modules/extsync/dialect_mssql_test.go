package extsync

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// meldungenInfo bildet dbo.Meldungen des Labors nach.
func meldungenInfo() *TableInfo {
	return &TableInfo{
		Schema: "dbo", Name: "Meldungen",
		Columns: []Column{
			{Name: "MeldungNr", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "Titel", Type: "nvarchar", MaxLen: 200},
			{Name: "Beschreibung", Type: "nvarchar", Nullable: true},
			{Name: "Prio", Type: "nvarchar", Nullable: true, MaxLen: 10},
			{Name: "Zustand", Type: "nvarchar", MaxLen: 20},
			{Name: "Faellig", Type: "datetime2", Nullable: true},
			{Name: "Geaendert", Type: "datetime2"},
			{Name: "RV", Type: "timestamp", IsRowVersion: true},
		},
		Triggers: []string{"trg_Meldungen_Geaendert"},
	}
}

func kommentareInfo() *TableInfo {
	return &TableInfo{
		Schema: "dbo", Name: "MeldungKommentare",
		Columns: []Column{
			{Name: "Id", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "MeldungNr", Type: "int"},
			{Name: "Text", Type: "nvarchar", Nullable: true},
			{Name: "Verfasser", Type: "nvarchar", Nullable: true, MaxLen: 100},
			{Name: "Zeit", Type: "datetime2", Nullable: true},
		},
	}
}

func guidInfo() *TableInfo {
	return &TableInfo{
		Schema: "dbo", Name: "MeldungenGuid",
		Columns: []Column{
			{Name: "Id", Type: "uniqueidentifier", IsKey: true},
			{Name: "Werk", Type: "nvarchar", IsKey: true, MaxLen: 10},
			{Name: "Titel", Type: "nvarchar", MaxLen: 200},
			{Name: "Zustand", Type: "nvarchar", MaxLen: 20},
		},
	}
}

func mustDialect(t *testing.T) Dialect {
	t.Helper()
	d, err := DialectFor("mssql")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDialectFor(t *testing.T) {
	if _, err := DialectFor("mssql"); err != nil {
		t.Fatal(err)
	}
	_, err := DialectFor("oracle")
	if err == nil || !strings.Contains(err.Error(), "Datenbankart wird noch nicht unterstützt") {
		t.Fatalf("erwartet deutschen Fehler, bekam %v", err)
	}
}

func TestQuote(t *testing.T) {
	d := mustDialect(t)
	cases := map[string]string{
		"Titel":                        "[Titel]",
		"a]b":                          "[a]]b]",
		"Drop]Table":                   "[Drop]]Table]",
		"x] = 1; TRUNCATE TABLE z; --": "[x]] = 1; TRUNCATE TABLE z; --]",
		"mit Leerzeichen":              "[mit Leerzeichen]",
	}
	for in, want := range cases {
		if got := d.Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

func TestUpdateRowShape(t *testing.T) {
	d := mustDialect(t)
	st, err := d.UpdateRow(meldungenInfo(), []string{"MeldungNr"}, []string{"7"},
		map[string]any{"Zustand": "erledigt", "Titel": "Neu"})
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE TOP (1) [dbo].[Meldungen] SET [Titel]=@p1, [Zustand]=@p2 WHERE [MeldungNr]=@p3"
	if st.SQL != want {
		t.Fatalf("SQL = %q\nerwartet %q", st.SQL, want)
	}
	if !reflect.DeepEqual(st.Args, []any{"Neu", "erledigt", "7"}) {
		t.Fatalf("Args = %#v", st.Args)
	}
}

func TestUpdateRowCompositeKey(t *testing.T) {
	d := mustDialect(t)
	st, err := d.UpdateRow(guidInfo(), []string{"Id", "Werk"}, []string{"g", "W1"}, map[string]any{"Zustand": "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE TOP (1) [dbo].[MeldungenGuid] SET [Zustand]=@p1 WHERE [Id]=@p2 AND [Werk]=CAST(@p3 AS nvarchar(4000))"
	if st.SQL != want {
		t.Fatalf("SQL = %q", st.SQL)
	}
	if _, err := d.UpdateRow(guidInfo(), []string{"Id"}, []string{"g"}, map[string]any{"Zustand": "x"}); err == nil {
		t.Fatal("fehlende Schlüsselspalte muss scheitern")
	}
	if _, err := d.UpdateRow(guidInfo(), []string{"Id", "Werk"}, []string{"g"}, map[string]any{"Zustand": "x"}); err == nil {
		t.Fatal("zu wenige Schlüsselwerte müssen scheitern")
	}
}

func TestUpdateRowRejectsUnknownAndKeyColumns(t *testing.T) {
	d := mustDialect(t)
	bad := []string{"x]; DROP TABLE y--", "MeldungNr", "RV", "meldungnr2", ""}
	for _, c := range bad {
		if _, err := d.UpdateRow(meldungenInfo(), []string{"MeldungNr"}, []string{"1"}, map[string]any{c: "v"}); err == nil {
			t.Errorf("Spalte %q hätte abgelehnt werden müssen", c)
		}
	}
	// Leeres SET.
	if _, err := d.UpdateRow(meldungenInfo(), []string{"MeldungNr"}, []string{"1"}, map[string]any{}); err == nil {
		t.Error("leeres SET muss scheitern")
	}
	// Unzulässiger Go-Typ.
	if _, err := d.UpdateRow(meldungenInfo(), []string{"MeldungNr"}, []string{"1"}, map[string]any{"Titel": struct{}{}}); err == nil {
		t.Error("unzulässiger Typ muss scheitern")
	}
	// Berechnete / Identitätsspalte ohne Schlüssel.
	ti := meldungenInfo()
	ti.Columns = append(ti.Columns, Column{Name: "Calc", Type: "int", IsComputed: true})
	if _, err := d.UpdateRow(ti, []string{"MeldungNr"}, []string{"1"}, map[string]any{"Calc": 1}); err == nil {
		t.Error("berechnete Spalte muss scheitern")
	}
	// Schlüssel, der nicht der Primärschlüssel ist, obwohl einer deklariert ist.
	if _, err := d.UpdateRow(meldungenInfo(), []string{"Titel"}, []string{"x"}, map[string]any{"Zustand": "a"}); err == nil {
		t.Error("fremder Schlüssel bei deklariertem Primärschlüssel muss scheitern")
	}
}

func TestUpdateRowTableWithoutPrimaryKey(t *testing.T) {
	d := mustDialect(t)
	ti := &TableInfo{Schema: "dbo", Name: "Lose", Columns: []Column{
		{Name: "Nr", Type: "int"}, {Name: "Text", Type: "nvarchar"},
	}}
	// Ohne Primärschlüssel und ohne eindeutigen Index ist kein Schlüssel eindeutig.
	_, err := d.UpdateRow(ti, []string{"Nr"}, []string{"3"}, map[string]any{"Text": "a"})
	if err == nil || !strings.Contains(err.Error(), "Schlüssel ist nicht eindeutig") {
		t.Fatalf("erwartet Fehler zur Eindeutigkeit, bekam %v", err)
	}
	if _, err := d.SelectOne(ti, []string{"Text"}, []string{"Nr"}, []string{"3"}); err == nil {
		t.Fatal("SelectOne mit nicht eindeutigem Schlüssel muss scheitern")
	}
	// Mit eindeutigem Index (UniqueKeys) geht es.
	ti.UniqueKeys = [][]string{{"Nr"}}
	st, err := d.UpdateRow(ti, []string{"Nr"}, []string{"3"}, map[string]any{"Text": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "UPDATE TOP (1) [dbo].[Lose] SET [Text]=@p1 WHERE [Nr]=@p2" {
		t.Fatalf("SQL = %q", st.SQL)
	}
	if _, err := d.UpdateRow(ti, []string{"Nr"}, []string{"3"}, map[string]any{"Nr": 1}); err == nil {
		t.Fatal("Schlüsselspalte im SET muss scheitern")
	}
	if _, err := d.UpdateRow(ti, []string{"Nope"}, []string{"3"}, map[string]any{"Text": "a"}); err == nil {
		t.Fatal("unbekannte Schlüsselspalte muss scheitern")
	}
	if _, err := d.UpdateRow(ti, nil, nil, map[string]any{"Text": "a"}); err == nil {
		t.Fatal("ohne Schlüssel darf kein UPDATE entstehen")
	}
}

func TestKeyIsUnique(t *testing.T) {
	ti := guidInfo() // Primärschlüssel (Id, Werk)
	ti.UniqueKeys = [][]string{{"Titel"}}
	cases := []struct {
		cols []string
		want bool
	}{
		{[]string{"Id", "Werk"}, true},
		{[]string{"Werk", "Id"}, true}, // als Menge
		{[]string{"Id"}, false},
		{[]string{"Titel"}, true},
		{[]string{"Titel", "Id"}, false},
		{[]string{"Id", "Id"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := KeyIsUnique(ti, c.cols); got != c.want {
			t.Errorf("KeyIsUnique(%v) = %v", c.cols, got)
		}
	}
	if KeyIsUnique(nil, []string{"Id"}) {
		t.Error("nil-Tabelle darf nicht eindeutig sein")
	}
}

func TestKeyTypesRestricted(t *testing.T) {
	d := mustDialect(t)
	ti := &TableInfo{Schema: "dbo", Name: "T", Columns: []Column{
		{Name: "F", Type: "float", IsKey: true}, {Name: "X", Type: "nvarchar"},
	}}
	_, err := d.UpdateRow(ti, []string{"F"}, []string{"1"}, map[string]any{"X": "a"})
	if err == nil || !strings.Contains(err.Error(), "Schlüsselspalte vom Typ float wird nicht unterstützt") {
		t.Fatalf("Fehler = %v", err)
	}
	if _, err := d.SelectRows(ti, []string{"X"}, []string{"F"}, "", "none", "", "", 0, 5); err == nil {
		t.Fatal("SelectRows mit float-Schlüssel muss scheitern")
	}
	if _, err := d.SelectOne(ti, []string{"X"}, []string{"F"}, []string{"1"}); err == nil {
		t.Fatal("SelectOne mit float-Schlüssel muss scheitern")
	}
	if _, err := d.KeyValues(ti, []string{"F"}, map[string]any{"F": 1.5}); err == nil {
		t.Fatal("KeyValues mit float-Schlüssel muss scheitern")
	}
}

func TestKeyValues(t *testing.T) {
	d := mustDialect(t)
	kv, err := d.KeyValues(guidInfo(), []string{"Id", "Werk"}, map[string]any{"Id": "ABCDEF01-0000-0000-0000-000000000001", "Werk": "W1"})
	if err != nil || !reflect.DeepEqual(kv, []string{"abcdef01-0000-0000-0000-000000000001", "W1"}) {
		t.Fatalf("kv = %v, err = %v", kv, err)
	}
	kv, err = d.KeyValues(meldungenInfo(), []string{"MeldungNr"}, map[string]any{"MeldungNr": int64(42)})
	if err != nil || !reflect.DeepEqual(kv, []string{"42"}) {
		t.Fatalf("kv = %v, err = %v", kv, err)
	}
	if _, err := d.KeyValues(meldungenInfo(), []string{"MeldungNr"}, map[string]any{}); err == nil {
		t.Fatal("fehlender Wert muss scheitern")
	}
	if _, err := d.KeyValues(meldungenInfo(), []string{"MeldungNr"}, map[string]any{"MeldungNr": nil}); err == nil {
		t.Fatal("NULL-Schlüssel muss scheitern")
	}
	if _, err := d.KeyValues(meldungenInfo(), []string{"Titel2"}, map[string]any{}); err == nil {
		t.Fatal("unbekannte Spalte muss scheitern")
	}
}

func TestNextCursor(t *testing.T) {
	d := mustDialect(t)
	rv := []byte{0, 0, 0, 0, 0, 0, 7, 0xd1}
	c, err := d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "RV", "rowversion", map[string]any{"RV": rv})
	if err != nil || c != "00000000000007d1" {
		t.Fatalf("c = %q err = %v", c, err)
	}
	tm := time.Date(2026, 11, 13, 12, 0, 0, 123456700, time.FixedZone("x", 3600))
	c, err = d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "Geaendert", "timestamp",
		map[string]any{"Geaendert": tm, "MeldungNr": int64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if c != `{"t":"2026-11-13T11:00:00.1234567Z","k":["5"]}` {
		t.Fatalf("c = %q", c)
	}
	if c, err := d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "", "none", map[string]any{"MeldungNr": int64(9)}); err != nil || c != `{"k":["9"]}` {
		t.Fatalf("none: %q %v", c, err)
	}
	if _, err := d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "", "none", nil); err == nil {
		t.Error("none ohne Schlüsselwert muss scheitern")
	}
	// Nicht unterstützte Typen als Zeitstempel (auch hier, nicht erst beim Lesen).
	ti := meldungenInfo()
	ti.Columns = append(ti.Columns, Column{Name: "Off", Type: "datetimeoffset"}, Column{Name: "Txt", Type: "nvarchar"})
	for _, col := range []string{"Off", "Txt", "RV"} {
		_, err := d.NextCursor(ti, []string{"MeldungNr"}, col, "timestamp", map[string]any{col: tm, "MeldungNr": int64(1)})
		if err == nil {
			t.Errorf("Spalte %s darf kein Zeitstempel sein", col)
		}
	}
	for _, row := range []map[string]any{{}, {"RV": []byte{1}}, {"RV": "x"}} {
		if _, err := d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "RV", "rowversion", row); err == nil {
			t.Errorf("Zeile %v muss scheitern", row)
		}
	}
	if _, err := d.NextCursor(meldungenInfo(), []string{"MeldungNr"}, "Geaendert", "timestamp", map[string]any{"Geaendert": tm}); err == nil {
		t.Error("ohne Schlüsselwert muss scheitern")
	}
}

func TestTimeParametersAreUTCDatetime2(t *testing.T) {
	d := mustDialect(t)
	loc := time.FixedZone("plus5", 5*3600)
	tm := time.Date(2026, 11, 13, 15, 0, 0, 0, loc)
	st, err := d.UpdateRow(meldungenInfo(), []string{"MeldungNr"}, []string{"1"}, map[string]any{"Faellig": tm, "Titel": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "UPDATE TOP (1) [dbo].[Meldungen] SET [Faellig]=CAST(@p1 AS datetime2(7)), [Titel]=@p2 WHERE [MeldungNr]=@p3" {
		t.Fatalf("SQL = %q", st.SQL)
	}
	got := st.Args[0].(time.Time)
	if got.Location() != time.UTC || !got.Equal(tm) || got.Hour() != 10 {
		t.Fatalf("Zeit nicht auf UTC normalisiert: %v", got)
	}
	st, err = d.InsertChild(meldungenInfo(), kommentareInfo(), map[string]any{"Zeit": tm})
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "INSERT INTO [dbo].[MeldungKommentare] ([Zeit]) VALUES (CAST(@p1 AS datetime2(7)))" || st.Args[0].(time.Time).Hour() != 10 {
		t.Fatalf("SQL = %q Args = %v", st.SQL, st.Args)
	}
}

func TestInsertChildShape(t *testing.T) {
	d := mustDialect(t)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	st, err := d.InsertChild(meldungenInfo(), kommentareInfo(), map[string]any{
		"Text": "hallo", "MeldungNr": int64(4), "Zeit": now, "Verfasser": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO [dbo].[MeldungKommentare] ([MeldungNr], [Text], [Verfasser], [Zeit]) VALUES (@p1, @p2, @p3, CAST(@p4 AS datetime2(7)))"
	if st.SQL != want {
		t.Fatalf("SQL = %q", st.SQL)
	}
	if !reflect.DeepEqual(st.Args, []any{int64(4), "hallo", nil, now}) {
		t.Fatalf("Args = %#v", st.Args)
	}
	for _, bad := range []string{"Id", "x]; DROP TABLE y--"} {
		if _, err := d.InsertChild(meldungenInfo(), kommentareInfo(), map[string]any{bad: 1}); err == nil {
			t.Errorf("Spalte %q hätte abgelehnt werden müssen", bad)
		}
	}
	if _, err := d.InsertChild(meldungenInfo(), kommentareInfo(), map[string]any{}); err == nil {
		t.Error("leere Werte müssen scheitern")
	}
}

func TestInsertChildRejectsSource(t *testing.T) {
	d := mustDialect(t)
	src := meldungenInfo()
	same := meldungenInfo()
	same.Schema, same.Name = "DBO", "MELDUNGEN"
	for _, c := range []*TableInfo{src, same} {
		_, err := d.InsertChild(src, c, map[string]any{"Titel": "x"})
		if !errors.Is(err, ErrInsertIntoSource) {
			t.Fatalf("erwartet ErrInsertIntoSource, bekam %v", err)
		}
	}
	// Ohne bekannte Quelle (nil) oder Kind wird nie eingefügt (kein Einfügen in die Meldungstabelle).
	if st, err := d.InsertChild(nil, src, map[string]any{"Titel": "x"}); err == nil {
		t.Fatalf("InsertChild(nil, Meldungen) darf nicht gelingen: %q", st.SQL)
	}
	if _, err := d.InsertChild(src, nil, map[string]any{"Titel": "x"}); err == nil {
		t.Fatal("InsertChild(…, nil) darf nicht gelingen")
	}
	// Auch ein RowVersion-Wert im Kind wird abgelehnt.
	k := kommentareInfo()
	k.Columns = append(k.Columns, Column{Name: "RV", Type: "timestamp", IsRowVersion: true})
	if _, err := d.InsertChild(src, k, map[string]any{"RV": []byte{1}}); err == nil {
		t.Fatal("RowVersion-Spalte muss scheitern")
	}
}

func TestValidateFilter(t *testing.T) {
	ok := []string{
		"", "[Zustand] <> 'erledigt'", "[Status] = 'DROP'", "[Update] = 1",
		"Prio IN ('hoch','mittel') AND Faellig IS NOT NULL",
		"[a]]b] = 1", "(Prio = 'x' OR Prio = 'y')", "Titel = 'it''s fine'",
	}
	for _, f := range ok {
		if err := ValidateFilter(f); err != nil {
			t.Errorf("Filter %q sollte gültig sein: %v", f, err)
		}
	}
	bad := map[string]string{
		"1=1; DELETE FROM x":      ";",
		"1=1 -- x":                "--",
		"1=1 /* x */":             "/*",
		"a = 1 DELETE":            "DELETE",
		"a = 1 or update = 2":     "UPDATE",
		"x = 1 insert":            "INSERT",
		"MERGE":                   "MERGE",
		"DrOp":                    "DROP",
		"truncate":                "TRUNCATE",
		"exec":                    "EXEC",
		"EXECUTE":                 "EXECUTE",
		"alter":                   "ALTER",
		"create":                  "CREATE",
		"grant":                   "GRANT",
		"into":                    "INTO",
		"a = 'offen":              "Anführungszeichen",
		"[a = 1":                  "Klammer",
		"xp_cmdshell":             "xp_",
		"sp_who":                  "sp_",
		"OPENROWSET":              "OPENROWSET",
		"openquery":               "OPENQUERY",
		"OPENDATASOURCE":          "OPENDATASOURCE",
		"waitfor":                 "WAITFOR",
		"shutdown":                "SHUTDOWN",
		"backup":                  "BACKUP",
		"restore":                 "RESTORE",
		"dbcc":                    "DBCC",
		"kill":                    "KILL",
		"deny":                    "DENY",
		"revoke":                  "REVOKE",
		"use":                     "USE",
		"a IN (SELECT 1)":         "SELECT",
		"a=1 UNION":               "UNION",
		"a = 1)) OR ((1=1":        "Klammern",
		"a = 1\x00":               "Steuerzeichen",
		strings.Repeat("a", 2001): "2000",
	}
	for f, word := range bad {
		err := ValidateFilter(f)
		if err == nil {
			t.Errorf("Filter %q hätte abgelehnt werden müssen", f)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(word)) {
			t.Errorf("Filter %q: Fehler %q nennt %q nicht", f, err, word)
		}
	}
}

func TestSelectRowsShapes(t *testing.T) {
	d := mustDialect(t)
	ti := meldungenInfo()

	// rowversion mit Cursor
	st, err := d.SelectRows(ti, []string{"Titel", "Zustand"}, []string{"MeldungNr"}, "RV", "rowversion",
		"00000000000007d1", "[Zustand] <> 'erledigt'", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT TOP (100) [MeldungNr], [Titel], [Zustand], [RV] FROM [dbo].[Meldungen] AS [t] WHERE [RV] > @p1 AND [RV] < MIN_ACTIVE_ROWVERSION() AND ([Zustand] <> 'erledigt') ORDER BY [t].[RV]"
	if st.SQL != want {
		t.Fatalf("SQL = %q\nerwartet %q", st.SQL, want)
	}
	if len(st.Args) != 1 || !reflect.DeepEqual(st.Args[0], []byte{0, 0, 0, 0, 0, 0, 7, 0xd1}) {
		t.Fatalf("Args = %#v", st.Args)
	}

	// rowversion ohne Cursor: die Obergrenze gilt trotzdem
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "RV", "rowversion", "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (10) [MeldungNr], [Titel], [RV] FROM [dbo].[Meldungen] AS [t] WHERE [RV] < MIN_ACTIVE_ROWVERSION() ORDER BY [t].[RV]" || len(st.Args) != 0 {
		t.Fatalf("SQL = %q, Args = %#v", st.SQL, st.Args)
	}

	// timestamp mit Schlüsselfolge-Cursor und Fenster
	cur := `{"t":"2026-11-13T10:00:00.1234567Z","k":["7"]}`
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "Geaendert", "timestamp", cur, "", 30, 50)
	if err != nil {
		t.Fatal(err)
	}
	want = "SELECT TOP (50) [MeldungNr], [Titel], CONVERT(datetime2(7), [Geaendert]) AS [Geaendert] FROM [dbo].[Meldungen] AS [t] WHERE [Geaendert] IS NOT NULL AND ([Geaendert] > CAST(@p1 AS datetime2(7)) OR ([Geaendert] = CAST(@p1 AS datetime2(7)) AND [MeldungNr] > @p2)) AND [Geaendert] >= DATEADD(day, -30, SYSUTCDATETIME()) ORDER BY [t].[Geaendert], [t].[MeldungNr]"
	if st.SQL != want {
		t.Fatalf("SQL = %q\nerwartet %q", st.SQL, want)
	}
	if tm, ok := st.Args[0].(time.Time); !ok || !tm.Equal(time.Date(2026, 11, 13, 10, 0, 0, 123456700, time.UTC)) || tm.Location() != time.UTC {
		t.Fatalf("Args = %#v", st.Args)
	}
	if st.Args[1] != "7" {
		t.Fatalf("Args = %#v", st.Args)
	}

	// timestamp mit zusammengesetztem Schlüssel: lexikographischer Vergleich
	gi := guidInfo()
	gi.Columns = append(gi.Columns, Column{Name: "Geaendert", Type: "datetime2"})
	st, err = d.SelectRows(gi, []string{"Titel"}, []string{"Id", "Werk"}, "Geaendert", "timestamp",
		`{"t":"2026-11-13T10:00:00Z","k":["abc","W1"]}`, "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	want = "SELECT TOP (5) LOWER(CONVERT(nvarchar(36), [Id])) AS [Id], [Werk], [Titel], CONVERT(datetime2(7), [Geaendert]) AS [Geaendert] FROM [dbo].[MeldungenGuid] AS [t] WHERE [Geaendert] IS NOT NULL AND ([Geaendert] > CAST(@p1 AS datetime2(7)) OR ([Geaendert] = CAST(@p1 AS datetime2(7)) AND ([Id] > @p2 OR ([Id] = @p2 AND [Werk] > CAST(@p3 AS nvarchar(4000)))))) ORDER BY [t].[Geaendert], [t].[Id], [t].[Werk]"
	if st.SQL != want {
		t.Fatalf("SQL = %q\nerwartet %q", st.SQL, want)
	}

	// timestamp ohne Cursor
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "Geaendert", "timestamp", "", "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (5) [MeldungNr], [Titel], CONVERT(datetime2(7), [Geaendert]) AS [Geaendert] FROM [dbo].[Meldungen] AS [t] WHERE [Geaendert] IS NOT NULL ORDER BY [t].[Geaendert], [t].[MeldungNr]" {
		t.Fatalf("SQL = %q", st.SQL)
	}

	// timestamp auf datetime / smalldatetime / date: Parameter im EIGENEN Typ der Spalte
	for typ, cast := range map[string]string{"datetime": "datetime", "smalldatetime": "smalldatetime", "date": "date", "datetime2": "datetime2(7)"} {
		ti2 := meldungenInfo()
		ti2.Columns[6].Type = typ
		st, err = d.SelectRows(ti2, []string{"Titel"}, []string{"MeldungNr"}, "Geaendert", "timestamp", cur, "", 0, 5)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !strings.Contains(st.SQL, "[Geaendert] > CAST(@p1 AS "+cast+") OR ([Geaendert] = CAST(@p1 AS "+cast+")") ||
			!strings.Contains(st.SQL, "CONVERT(datetime2(7), [Geaendert]) AS [Geaendert]") {
			t.Fatalf("%s: SQL = %q", typ, st.SQL)
		}
	}

	// none
	st, err = d.SelectRows(ti, []string{"Zustand", "Titel"}, []string{"MeldungNr"}, "", "none", "", "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (5) [MeldungNr], [Titel], [Zustand] FROM [dbo].[Meldungen] AS [t] ORDER BY [t].[MeldungNr]" {
		t.Fatalf("SQL = %q", st.SQL)
	}

	// none mit Schlüsselcursor
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "", "none", `{"k":["4"]}`, "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (5) [MeldungNr], [Titel] FROM [dbo].[Meldungen] AS [t] WHERE [MeldungNr] > @p1 ORDER BY [t].[MeldungNr]" || !reflect.DeepEqual(st.Args, []any{"4"}) {
		t.Fatalf("SQL = %q Args = %#v", st.SQL, st.Args)
	}
	st, err = d.SelectRows(guidInfo(), []string{"Titel"}, []string{"Id", "Werk"}, "", "none", `{"k":["abc","W1"]}`, "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (5) LOWER(CONVERT(nvarchar(36), [Id])) AS [Id], [Werk], [Titel] FROM [dbo].[MeldungenGuid] AS [t] WHERE ([Id] > @p1 OR ([Id] = @p1 AND [Werk] > CAST(@p2 AS nvarchar(4000)))) ORDER BY [t].[Id], [t].[Werk]" {
		t.Fatalf("SQL = %q", st.SQL)
	}
}

func TestSelectRowsRejects(t *testing.T) {
	d := mustDialect(t)
	ti := meldungenInfo()
	k := []string{"MeldungNr"}
	c := []string{"Titel"}
	ti.Columns = append(ti.Columns, Column{Name: "Off", Type: "datetimeoffset"})
	goodTS := `{"t":"2026-11-13T10:00:00Z","k":["1"]}`
	type tc struct {
		name                   string
		cols, keys             []string
		chCol, kind, cur, filt string
		win, lim               int
	}
	cases := []tc{
		{"Fenster ohne timestamp", c, k, "RV", "rowversion", "", "", 5, 10},
		{"Fenster bei none", c, k, "", "none", "", "", 5, 10},
		{"Fenster zu groß", c, k, "Geaendert", "timestamp", "", "", 3651, 10},
		{"Limit 0", c, k, "", "none", "", "", 0, 0},
		{"Limit zu groß", c, k, "", "none", "", "", 0, 10001},
		{"unbekannte Spalte", []string{"x]; DROP TABLE y--"}, k, "", "none", "", "", 0, 10},
		{"unbekannter Schlüssel", c, []string{"Nope"}, "", "none", "", "", 0, 10},
		{"kein Schlüssel", c, nil, "", "none", "", "", 0, 10},
		{"nicht eindeutiger Schlüssel", c, []string{"Titel"}, "", "none", "", "", 0, 10},
		{"unbekannte Änderungsspalte", c, k, "Nope", "timestamp", "", "", 0, 10},
		{"rowversion auf normaler Spalte", c, k, "Titel", "rowversion", "", "", 0, 10},
		{"unbekannte Art", c, k, "Geaendert", "magic", "", "", 0, 10},
		{"Cursor bei none", c, k, "", "none", "irgendwas", "", 0, 10},
		{"Cursor bei none, falsche Schlüsselzahl", c, k, "", "none", `{"k":["1","2"]}`, "", 0, 10},
		{"Cursor bei none, fremdes Feld", c, k, "", "none", `{"k":["1"],"t":"x"}`, "", 0, 10},
		{"Zeitstempel auf Text", c, k, "Titel", "timestamp", "", "", 0, 10},
		{"Zeitstempel auf datetimeoffset", c, k, "Off", "timestamp", "", "", 0, 10},
		{"Cursor kein Hex", c, k, "RV", "rowversion", "zz'; DROP", "", 0, 10},
		{"Cursor keine Zeit", c, k, "Geaendert", "timestamp", "gestern", "", 0, 10},
		{"Cursor alter Aufbau (nur Zeit)", c, k, "Geaendert", "timestamp", "2026-11-13T10:00:00Z", "", 0, 10},
		{"Cursor falsche Schlüsselzahl", c, k, "Geaendert", "timestamp", `{"t":"2026-11-13T10:00:00Z","k":["1","2"]}`, "", 0, 10},
		{"Cursor fremdes Feld", c, k, "Geaendert", "timestamp", `{"t":"2026-11-13T10:00:00Z","k":["1"],"x":1}`, "", 0, 10},
		{"Cursor schlechte Zeit", c, k, "Geaendert", "timestamp", `{"t":"x","k":["1"]}`, "", 0, 10},
		{"Filter böse", c, k, "", "none", "", "1=1; DELETE FROM x", 0, 10},
	}
	for _, x := range cases {
		if _, err := d.SelectRows(ti, x.cols, x.keys, x.chCol, x.kind, x.cur, x.filt, x.win, x.lim); err == nil {
			t.Errorf("%s: Fehler erwartet", x.name)
		}
	}
	if _, err := d.SelectRows(ti, c, k, "Geaendert", "timestamp", goodTS, "", 0, 10); err != nil {
		t.Errorf("gültiger Cursor: %v", err)
	}
}

func TestSelectOneShape(t *testing.T) {
	d := mustDialect(t)
	st, err := d.SelectOne(guidInfo(), []string{"Titel"}, []string{"Id", "Werk"}, []string{"abc", "W1"})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT TOP (1) LOWER(CONVERT(nvarchar(36), [Id])) AS [Id], [Werk], [Titel] FROM [dbo].[MeldungenGuid] WHERE [Id]=@p1 AND [Werk]=CAST(@p2 AS nvarchar(4000))"
	if st.SQL != want || !reflect.DeepEqual(st.Args, []any{"abc", "W1"}) {
		t.Fatalf("SQL = %q Args = %#v", st.SQL, st.Args)
	}
	if _, err := d.SelectOne(guidInfo(), []string{"Titel"}, []string{"Id"}, []string{"abc"}); err == nil {
		t.Fatal("unvollständiger Schlüssel muss scheitern")
	}
	if _, err := d.SelectOne(guidInfo(), []string{"Nope"}, []string{"Id", "Werk"}, []string{"a", "b"}); err == nil {
		t.Fatal("unbekannte Spalte muss scheitern")
	}
	if _, err := d.SelectOne(guidInfo(), []string{"Titel"}, []string{"Id", "Werk"}, []string{"a"}); err == nil {
		t.Fatal("falsche Anzahl Schlüsselwerte muss scheitern")
	}
}

// ---------- Eigenschaftstest: nichts Zerstörerisches erzeugbar ----------

var reBad = regexp.MustCompile(`(?i)\b(DELETE|TRUNCATE|DROP|MERGE|EXEC|EXECUTE)\b`)

// stripQuoted entfernt Bezeichner in [...] und Zeichenketten in '...'.
func stripQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '[', '\'':
			closer := byte(']')
			if s[i] == '\'' {
				closer = '\''
			}
			j := i + 1
			for j < len(s) {
				if s[j] == closer {
					if j+1 < len(s) && s[j+1] == closer {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j + 1
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// valuesInArgs: jeder Wert muss unverändert (Zeiten in UTC) in den Args stehen.
func argsContain(args []any, v any) bool {
	if tm, ok := v.(time.Time); ok {
		v = tm.UTC()
	}
	for _, a := range args {
		if reflect.DeepEqual(a, v) {
			return true
		}
	}
	return false
}

// checkStmt prüft die Unveränderlichen eines erzeugten Befehls. values sind die
// Werte (Parameter), die der Aufrufer übergeben hat; names alle Bezeichner, die
// legal im SQL stehen dürfen.
func checkStmt(t *testing.T, st Stmt, source *TableInfo, values []any, names map[string]bool) {
	t.Helper()
	sqlText := st.SQL
	if !(strings.HasPrefix(sqlText, "SELECT") || strings.HasPrefix(sqlText, "UPDATE TOP (1) ") || strings.HasPrefix(sqlText, "INSERT INTO ")) {
		t.Fatalf("unzulässiger Anfang: %q", sqlText)
	}
	rest := stripQuoted(sqlText)
	if m := reBad.FindString(rest); m != "" {
		t.Fatalf("verbotenes Wort %q außerhalb von Anführung: %q", m, sqlText)
	}
	for _, bad := range []string{";", "--", "/*", "*/", "\x00", "]", "'"} {
		if strings.Contains(rest, bad) {
			t.Fatalf("verbotenes Zeichen %q außerhalb von Anführung: %q", bad, sqlText)
		}
	}
	if strings.HasPrefix(sqlText, "INSERT INTO ") {
		d := &mssqlDialect{}
		if source == nil || strings.HasPrefix(sqlText, "INSERT INTO "+d.Quote(source.Schema)+"."+d.Quote(source.Name)+" ") {
			t.Fatalf("INSERT in die Quelltabelle: %q", sqlText)
		}
	}
	for _, v := range values {
		if !argsContain(st.Args, v) {
			t.Fatalf("Wert %#v fehlt in Args %#v (SQL %q)", v, st.Args, sqlText)
		}
		if s, ok := v.(string); ok && len(s) > 3 && !names[s] && strings.Contains(sqlText, s) {
			t.Fatalf("Wert %q steht im SQL statt nur in Args: %q", s, sqlText)
		}
	}
}

func TestNoDestructiveStatementPossible(t *testing.T) {
	d := mustDialect(t)
	rng := rand.New(rand.NewSource(20261113))
	hostile := []string{
		"'; DROP TABLE x--", "]; DELETE FROM y; --", "x] = 1; TRUNCATE TABLE z; --",
		"Ｄｒｏｐ ＴＡＢＬＥ", "а'; ЕХЕС xp_cmdshell 'dir'--", "line1\nDELETE FROM q\n", "nul\x00byte",
		strings.Repeat("A", 5000), "Delete", "Drop", "1=1; DELETE FROM x", "' OR 1=1 --",
		"/* x */ MERGE", "Drop]Table", "[x]", "]]", "'", "", "EXEC('DROP TABLE a')", "0xDEADBEEF",
	}
	pick := func() string { return hostile[rng.Intn(len(hostile))] }

	// Fremder Aufbau mit gefährlichen, aber legalen Namen – an jeder Stelle, an der
	// ein Bezeichner im SQL landet (Schema, Tabelle, Schlüssel, Spalten,
	// Änderungsspalten).
	crafted := &TableInfo{
		Schema: "sch]ema; DROP", Name: "Delete Table]--",
		Columns: []Column{
			{Name: "Delete", Type: "int", IsKey: true},
			{Name: "Key]; DELETE", Type: "nvarchar", IsKey: true},
			{Name: "Drop]Table", Type: "nvarchar", Nullable: true},
			{Name: "x] = 1; TRUNCATE TABLE z; --", Type: "nvarchar", Nullable: true},
			{Name: "Merge", Type: "nvarchar", Nullable: true},
			{Name: "RV]; DROP", Type: "timestamp", IsRowVersion: true},
			{Name: "Zeit]; DELETE FROM q; --", Type: "datetime2", Nullable: true},
			{Name: "Guid]; EXEC", Type: "uniqueidentifier", Nullable: true},
			{Name: "DT]; DROP", Type: "datetime", Nullable: true},
			{Name: "SD]; DELETE", Type: "smalldatetime", Nullable: true},
			{Name: "D]; EXEC", Type: "date", Nullable: true},
		},
	}
	child := &TableInfo{
		Schema: "dbo", Name: "Kind]; DROP",
		Columns: []Column{
			{Name: "Id", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "Text", Type: "nvarchar", Nullable: true},
			{Name: "Drop", Type: "nvarchar", Nullable: true},
			{Name: "Zeit]; DELETE", Type: "datetime2", Nullable: true},
		},
	}
	// Zweite Quelle mit eindeutigem Einzelschlüssel, hostil benannt.
	single := &TableInfo{
		Schema: "s]; --", Name: "T]; TRUNCATE",
		Columns: []Column{
			{Name: "k]; DROP", Type: "bigint", IsKey: true},
			{Name: "Geändert]; --", Type: "datetime2"},
			{Name: "RV2]; MERGE", Type: "timestamp", IsRowVersion: true},
			{Name: "Wert]; EXEC", Type: "nvarchar", Nullable: true},
		},
	}
	tables := []*TableInfo{crafted, single, meldungenInfo(), guidInfo()}
	keysOf := func(ti *TableInfo) []string {
		var k []string
		for _, c := range ti.Columns {
			if c.IsKey {
				k = append(k, c.Name)
			}
		}
		return k
	}
	namesOf := func(tis ...*TableInfo) map[string]bool {
		m := map[string]bool{}
		for _, ti := range tis {
			m[ti.Schema], m[ti.Name] = true, true
			for _, c := range ti.Columns {
				m[c.Name] = true
			}
		}
		return m
	}
	valuePool := []any{nil, "s", int64(1), 2.5, true, []byte{1, 2}, time.Unix(0, 0), time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("z", 7200)), "'; DROP TABLE x--", "]; DELETE FROM y; --", "Ｄｒｏｐ", "line1\nDELETE FROM q\n", "nul\x00byte", strings.Repeat("A", 5000), "Delete", "Drop", "Merge", "EXEC('x')"}
	badValuePool := []any{struct{}{}, 7, int32(3), []string{"x"}}

	produced, total := 0, 0
	byKind := map[string]int{}
	run := func(kind string, st Stmt, err error, src *TableInfo, values []any, names map[string]bool) {
		total++
		if err != nil {
			return
		}
		produced++
		byKind[kind]++
		checkStmt(t, st, src, values, names)
	}
	for i := 0; i < 600; i++ {
		ti := tables[rng.Intn(len(tables))]
		names := namesOf(ti, child)
		keys := keysOf(ti)
		allCols := make([]string, 0, len(ti.Columns))
		for _, c := range ti.Columns {
			allCols = append(allCols, c.Name)
		}
		pickCol := func() string {
			if rng.Intn(4) == 0 {
				return pick()
			}
			return allCols[rng.Intn(len(allCols))]
		}
		pickCols := func() []string {
			out := make([]string, rng.Intn(3)+1)
			for i := range out {
				out[i] = pickCol()
			}
			return out
		}
		realCols := func() []string {
			out := make([]string, rng.Intn(3)+1)
			for i := range out {
				out[i] = allCols[rng.Intn(len(allCols))]
			}
			return out
		}
		// Zeile mit gültigen Werten für NextCursor.
		row := map[string]any{}
		for _, c := range ti.Columns {
			switch {
			case c.IsRowVersion:
				row[c.Name] = []byte{0, 0, 0, 0, 0, 0, byte(rng.Intn(256)), byte(rng.Intn(256))}
			case isTimeType(c.Type):
				row[c.Name] = time.Unix(int64(rng.Intn(1e9)), int64(rng.Intn(1e9))).In(time.FixedZone("q", 3600))
			case c.Type == "uniqueidentifier":
				row[c.Name] = "ABCDEF01-0000-0000-0000-000000000001"
			case c.Type == "bigint" || c.Type == "int":
				row[c.Name] = int64(rng.Intn(1000))
			default:
				row[c.Name] = pick()
			}
		}

		// SelectRows mit zufälligen Eingaben (viele Ablehnungen sind erwartet).
		kinds := []string{"none", "timestamp", "rowversion", pick()}
		kind := kinds[rng.Intn(len(kinds))]
		st, err := d.SelectRows(ti, pickCols(), pickCols(), pickCol(), kind, pick(), pick(), rng.Intn(5)-1, rng.Intn(20)-2)
		run("select-random", st, err, ti, nil, names)

		// SelectRows mit gültigen Cursorn aus NextCursor – Änderungsspalte hostil benannt.
		var chCol, chKind string
		for _, c := range ti.Columns {
			if c.IsRowVersion && rng.Intn(2) == 0 {
				chCol, chKind = c.Name, "rowversion"
			}
			if isTimeType(c.Type) && rng.Intn(2) == 0 {
				chCol, chKind = c.Name, "timestamp"
			}
		}
		if chCol != "" {
			cur, cerr := d.NextCursor(ti, keys, chCol, chKind, row)
			if cerr != nil {
				t.Fatalf("NextCursor für gültige Zeile: %v", cerr)
			}
			if rng.Intn(3) == 0 {
				cur = ""
			}
			win := 0
			if chKind == "timestamp" {
				win = rng.Intn(3)
			}
			st, err = d.SelectRows(ti, realCols(), keys, chCol, chKind, cur, []string{"", "[Merge] = 'DROP'"}[rng.Intn(2)], win, 1+rng.Intn(50))
			if err != nil {
				// Filter [Merge] gibt es nicht in jeder Tabelle; sonst muss es gelingen.
				t.Fatalf("gültige Abfrage scheitert: %v (%s, %s)", err, chCol, chKind)
			}
			run("select-cursor-"+chKind, st, err, ti, nil, names)
			if err == nil && chKind == "timestamp" {
				col, _ := findColumn(ti, chCol)
				cast, _ := tsCast(col.Type)
				if !strings.Contains(st.SQL, "] IS NOT NULL") || !strings.Contains(st.SQL, "CONVERT(datetime2(7), ") {
					t.Fatalf("IS NOT NULL oder genaues Lesen fehlt: %q", st.SQL)
				}
				if cur != "" {
					if len(st.Args) < 2 || !strings.Contains(st.SQL, " AS "+cast+")") {
						t.Fatalf("Cursor oder Parametertyp %s fehlt: %q %#v", cast, st.SQL, st.Args)
					}
				}
			}
		}

		// Schlüsselcursor ohne Änderungsspalte (none).
		if curN, cerr := d.NextCursor(ti, keys, "", "none", row); cerr != nil {
			t.Fatalf("NextCursor none: %v", cerr)
		} else {
			if rng.Intn(4) == 0 {
				curN = ""
			}
			st, err = d.SelectRows(ti, realCols(), keys, "", "none", curN, "", 0, 1+rng.Intn(50))
			if err != nil {
				t.Fatalf("gültige none-Abfrage scheitert: %v", err)
			}
			run("select-cursor-none", st, err, ti, nil, names)
			if curN != "" && len(st.Args) != len(keys) {
				t.Fatalf("Schlüsselcursor fehlt in Args: %#v", st.Args)
			}
		}

		// SelectOne mit gültigem Schlüssel.
		kv, kerr := d.KeyValues(ti, keys, row)
		if kerr != nil {
			t.Fatalf("KeyValues: %v", kerr)
		}
		kvAny := make([]any, len(kv))
		for j := range kv {
			kvAny[j] = kv[j]
		}
		st, err = d.SelectOne(ti, realCols(), keys, kv)
		if err != nil {
			t.Fatalf("gültiges SelectOne scheitert: %v", err)
		}
		run("selectone", st, err, ti, kvAny, names)
		hv := make([]string, len(keys))
		for j := range hv {
			hv[j] = pick()
		}
		st, err = d.SelectOne(ti, realCols(), keys, hv)
		hvAny := make([]any, len(hv))
		for j := range hv {
			hvAny[j] = hv[j]
		}
		run("selectone-hostile", st, err, ti, hvAny, names)

		// UpdateRow: gültige Spalten mit bösartigen Werten.
		set := map[string]any{}
		var setVals []any
		for _, c := range ti.Columns {
			if c.IsKey || c.IsRowVersion || c.IsComputed || c.IsIdentity || rng.Intn(2) == 0 {
				continue
			}
			v := valuePool[rng.Intn(len(valuePool))]
			set[c.Name] = v
			setVals = append(setVals, v)
		}
		if len(set) > 0 {
			st, err = d.UpdateRow(ti, keys, hv, set)
			if err != nil {
				t.Fatalf("gültiges UpdateRow scheitert: %v", err)
			}
			run("update", st, err, ti, append(setVals, hvAny...), names)
		}
		// UpdateRow mit zufälligen (oft ungültigen) Eingaben.
		rset := map[string]any{pickCol(): valuePool[rng.Intn(len(valuePool))], pick(): badValuePool[rng.Intn(len(badValuePool))]}
		st, err = d.UpdateRow(ti, pickCols(), hv, rset)
		run("update-random", st, err, ti, nil, names)

		// InsertChild: gültige und zufällige Eingaben, auch nil-Quelle.
		vals := map[string]any{}
		var insVals []any
		for _, n := range []string{"Text", "Drop", "Zeit]; DELETE"} {
			if rng.Intn(3) > 0 {
				v := valuePool[rng.Intn(len(valuePool))]
				vals[n] = v
				insVals = append(insVals, v)
			}
		}
		if len(vals) > 0 {
			st, err = d.InsertChild(ti, child, vals)
			if err != nil {
				t.Fatalf("gültiges InsertChild scheitert: %v", err)
			}
			run("insert", st, err, ti, insVals, namesOf(ti, child))
		}
		rvals := map[string]any{[]string{"Text", "Drop", "Id", pick()}[rng.Intn(4)]: valuePool[rng.Intn(len(valuePool))]}
		st, err = d.InsertChild(ti, child, rvals)
		run("insert-random", st, err, ti, nil, namesOf(ti, child))
		// Kind und Quelle vertauscht: die Quelle ist dann `child`.
		st, err = d.InsertChild(child, ti, vals)
		run("insert-swapped", st, err, child, nil, namesOf(ti, child))
		// Quelle gleich Kind und nil-Quelle dürfen nie gelingen.
		if _, err = d.InsertChild(ti, ti, vals); err == nil {
			t.Fatal("InsertChild(source, source) darf nie gelingen")
		}
		if _, err = d.InsertChild(nil, ti, vals); err == nil {
			t.Fatal("InsertChild(nil, …) darf nie gelingen")
		}
		if _, err = d.InsertChild(ti, nil, vals); err == nil {
			t.Fatal("InsertChild(…, nil) darf nie gelingen")
		}
	}
	if total < 200 {
		t.Fatalf("nur %d Kombinationen", total)
	}
	for _, k := range []string{"select-cursor-rowversion", "select-cursor-timestamp", "select-cursor-none", "selectone", "update", "insert", "selectone-hostile"} {
		if byKind[k] < 20 {
			t.Fatalf("Pfad %s nur %d-mal erzeugt, Test ist zu schwach (%v)", k, byKind[k], byKind)
		}
	}
	t.Logf("%d Kombinationen, %d erzeugte Befehle geprüft: %v", total, produced, byKind)

	// Spaltenname mit ] wird verdoppelt; die hostile Änderungsspalte ebenfalls.
	st, err := d.SelectOne(crafted, []string{"Drop]Table"}, []string{"Delete", "Key]; DELETE"}, []string{"1", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "[Drop]]Table]") {
		t.Fatalf("Quoting fehlt: %q", st.SQL)
	}
	cur, _ := d.NextCursor(crafted, []string{"Delete", "Key]; DELETE"}, "Zeit]; DELETE FROM q; --", "timestamp",
		map[string]any{"Zeit]; DELETE FROM q; --": time.Unix(1, 0), "Delete": int64(1), "Key]; DELETE": "a"})
	st, err = d.SelectRows(crafted, []string{"Merge"}, []string{"Delete", "Key]; DELETE"}, "Zeit]; DELETE FROM q; --", "timestamp", cur, "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "[Zeit]]; DELETE FROM q; --] >") || !strings.Contains(st.SQL, "ORDER BY [t].[Zeit]]; DELETE FROM q; --], ") {
		t.Fatalf("Änderungsspalte nicht gequotet: %q", st.SQL)
	}
	st, err = d.SelectRows(crafted, []string{"Merge"}, []string{"Delete", "Key]; DELETE"}, "RV]; DROP", "rowversion", "00000000000007d1", "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "[RV]]; DROP] > @p1 AND [RV]]; DROP] < MIN_ACTIVE_ROWVERSION()") {
		t.Fatalf("rowversion-Spalte nicht gequotet: %q", st.SQL)
	}
	if err := ValidateFilter("1=1; DELETE FROM x"); err == nil {
		t.Fatal("Filter mit DELETE muss scheitern")
	}
}

// isTimeType: Typen, die als Zeitstempel-Änderungsspalte erlaubt sind.
func isTimeType(typ string) bool {
	_, ok := tsCast(typ)
	return ok
}

// Schlüsselwerte werden im Typbereich der Spalte verglichen (Reihenfolge wie ORDER BY, Index nutzbar).
func TestKeyPlaceholderByType(t *testing.T) {
	d := mustDialect(t)
	mk := func(typ string) *TableInfo {
		return &TableInfo{Schema: "dbo", Name: "T", Columns: []Column{
			{Name: "K", Type: typ, IsKey: true}, {Name: "X", Type: "nvarchar"},
		}}
	}
	cases := map[string]string{
		"varchar":          "CAST(@p%d AS varchar(8000))",
		"char":             "CAST(@p%d AS varchar(8000))",
		"nvarchar":         "CAST(@p%d AS nvarchar(4000))",
		"nchar":            "CAST(@p%d AS nvarchar(4000))",
		"int":              "@p%d",
		"bigint":           "@p%d",
		"uniqueidentifier": "@p%d",
	}
	for typ, tmpl := range cases {
		ti := mk(typ)
		st, err := d.SelectRows(ti, []string{"X"}, []string{"K"}, "", "none", `{"k":["a-b"]}`, "", 0, 1)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if want := "[K] > " + fmt.Sprintf(tmpl, 1); !strings.Contains(st.SQL, want) {
			t.Errorf("%s: SQL = %q, erwartet %q", typ, st.SQL, want)
		}
		st, err = d.SelectOne(ti, []string{"X"}, []string{"K"}, []string{"a-b"})
		if err != nil {
			t.Fatal(err)
		}
		if want := "WHERE [K]=" + fmt.Sprintf(tmpl, 1); !strings.HasSuffix(st.SQL, want) {
			t.Errorf("%s: SQL = %q, erwartet Ende %q", typ, st.SQL, want)
		}
		st, err = d.UpdateRow(ti, []string{"K"}, []string{"a-b"}, map[string]any{"X": "v"})
		if err != nil {
			t.Fatal(err)
		}
		if want := "WHERE [K]=" + fmt.Sprintf(tmpl, 2); !strings.HasSuffix(st.SQL, want) {
			t.Errorf("%s: SQL = %q, erwartet Ende %q", typ, st.SQL, want)
		}
	}
}
