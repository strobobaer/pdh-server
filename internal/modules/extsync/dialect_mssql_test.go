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
	want := "UPDATE TOP (1) [dbo].[MeldungenGuid] SET [Zustand]=@p1 WHERE [Id]=@p2 AND [Werk]=@p3"
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

func TestInsertChildShape(t *testing.T) {
	d := mustDialect(t)
	now := time.Now()
	st, err := d.InsertChild(meldungenInfo(), kommentareInfo(), map[string]any{
		"Text": "hallo", "MeldungNr": int64(4), "Zeit": now, "Verfasser": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO [dbo].[MeldungKommentare] ([MeldungNr], [Text], [Verfasser], [Zeit]) VALUES (@p1, @p2, @p3, @p4)"
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
	want := "SELECT TOP (100) [MeldungNr], [Titel], [Zustand], [RV] FROM [dbo].[Meldungen] WHERE [RV] > @p1 AND ([Zustand] <> 'erledigt') ORDER BY [RV]"
	if st.SQL != want {
		t.Fatalf("SQL = %q\nerwartet %q", st.SQL, want)
	}
	if len(st.Args) != 1 || !reflect.DeepEqual(st.Args[0], []byte{0, 0, 0, 0, 0, 0, 7, 0xd1}) {
		t.Fatalf("Args = %#v", st.Args)
	}

	// rowversion ohne Cursor
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "RV", "rowversion", "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (10) [MeldungNr], [Titel], [RV] FROM [dbo].[Meldungen] ORDER BY [RV]" || len(st.Args) != 0 {
		t.Fatalf("SQL = %q, Args = %#v", st.SQL, st.Args)
	}

	// timestamp mit Fenster
	ts := "2026-11-13T10:00:00Z"
	st, err = d.SelectRows(ti, []string{"Titel"}, []string{"MeldungNr"}, "Geaendert", "timestamp", ts, "", 30, 50)
	if err != nil {
		t.Fatal(err)
	}
	want = "SELECT TOP (50) [MeldungNr], [Titel], [Geaendert] FROM [dbo].[Meldungen] WHERE [Geaendert] >= @p1 AND [Geaendert] >= DATEADD(day, -30, SYSUTCDATETIME()) ORDER BY [Geaendert], [MeldungNr]"
	if st.SQL != want {
		t.Fatalf("SQL = %q", st.SQL)
	}
	if tm, ok := st.Args[0].(time.Time); !ok || !tm.Equal(time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("Args = %#v", st.Args)
	}

	// none
	st, err = d.SelectRows(ti, []string{"Zustand", "Titel"}, []string{"MeldungNr"}, "", "none", "", "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != "SELECT TOP (5) [MeldungNr], [Titel], [Zustand] FROM [dbo].[Meldungen] ORDER BY [MeldungNr]" {
		t.Fatalf("SQL = %q", st.SQL)
	}
}

func TestSelectRowsRejects(t *testing.T) {
	d := mustDialect(t)
	ti := meldungenInfo()
	k := []string{"MeldungNr"}
	c := []string{"Titel"}
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
		{"unbekannte Änderungsspalte", c, k, "Nope", "timestamp", "", "", 0, 10},
		{"rowversion auf normaler Spalte", c, k, "Titel", "rowversion", "", "", 0, 10},
		{"unbekannte Art", c, k, "Geaendert", "magic", "", "", 0, 10},
		{"Cursor bei none", c, k, "", "none", "irgendwas", "", 0, 10},
		{"Cursor kein Hex", c, k, "RV", "rowversion", "zz'; DROP", "", 0, 10},
		{"Cursor keine Zeit", c, k, "Geaendert", "timestamp", "gestern", "", 0, 10},
		{"Filter böse", c, k, "", "none", "", "1=1; DELETE FROM x", 0, 10},
	}
	for _, x := range cases {
		if _, err := d.SelectRows(ti, x.cols, x.keys, x.chCol, x.kind, x.cur, x.filt, x.win, x.lim); err == nil {
			t.Errorf("%s: Fehler erwartet", x.name)
		}
	}
}

func TestSelectOneShape(t *testing.T) {
	d := mustDialect(t)
	st, err := d.SelectOne(guidInfo(), []string{"Titel"}, []string{"Id", "Werk"}, []string{"abc", "W1"})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT TOP (1) [Id], [Werk], [Titel] FROM [dbo].[MeldungenGuid] WHERE [Id]=@p1 AND [Werk]=@p2"
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

var (
	reBracket = regexp.MustCompile(`\[(?:[^\]]|\]\])*\]`)
	reLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
	reBad     = regexp.MustCompile(`(?i)\b(DELETE|TRUNCATE|DROP|MERGE|EXEC|EXECUTE)\b`)
)

// stripQuoted entfernt Bezeichner in [...] und Zeichenketten in '...'.
func stripQuoted(s string) string {
	// Reihenfolge je nach erstem Auftreten, damit ein ' in [..] nicht stört.
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			j := i + 1
			for j < len(s) {
				if s[j] == ']' {
					if j+1 < len(s) && s[j+1] == ']' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j + 1
			b.WriteByte(' ')
		case '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
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

// legalNames sind Werte, die auch als Spaltenname vorkommen dürfen.
var legalNames = map[string]bool{"Delete": true, "Drop": true, "Merge": true, "Drop]Table": true}

func checkStmt(t *testing.T, st Stmt, source *TableInfo, hostile []string) {
	t.Helper()
	sql := st.SQL
	if !(strings.HasPrefix(sql, "SELECT") || strings.HasPrefix(sql, "UPDATE TOP (1) ") || strings.HasPrefix(sql, "INSERT INTO ")) {
		t.Fatalf("unzulässiger Anfang: %q", sql)
	}
	rest := stripQuoted(sql)
	if m := reBad.FindString(rest); m != "" {
		t.Fatalf("verbotenes Wort %q außerhalb von Anführung: %q", m, sql)
	}
	for _, bad := range []string{";", "--", "/*", "*/", "\x00"} {
		if strings.Contains(rest, bad) {
			t.Fatalf("verbotenes Zeichen %q außerhalb von Anführung: %q", bad, sql)
		}
	}
	if strings.HasPrefix(sql, "INSERT INTO ") {
		target := "INSERT INTO " + (&mssqlDialect{}).Quote(source.Schema) + "." + (&mssqlDialect{}).Quote(source.Name) + " "
		if strings.HasPrefix(sql, target) {
			t.Fatalf("INSERT in die Quelltabelle: %q", sql)
		}
	}
	for _, h := range hostile {
		if _, legal := legalNames[h]; legal {
			continue // ein Wert, der zufällig auch ein Spaltenname ist
		}
		if len(h) > 3 && strings.Contains(sql, h) {
			t.Fatalf("Wert %q steht im SQL statt in Args: %q", h, sql)
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

	// Fremder Aufbau mit gefährlichen, aber legalen Namen.
	crafted := &TableInfo{
		Schema: "sch]ema; DROP", Name: "Delete Table]--",
		Columns: []Column{
			{Name: "Delete", Type: "int", IsKey: true},
			{Name: "Drop]Table", Type: "nvarchar", Nullable: true},
			{Name: "x] = 1; TRUNCATE TABLE z; --", Type: "nvarchar", Nullable: true},
			{Name: "Merge", Type: "nvarchar", Nullable: true},
			{Name: "RV", Type: "timestamp", IsRowVersion: true},
			{Name: "Zeit", Type: "datetime2", Nullable: true},
		},
	}
	child := &TableInfo{
		Schema: "dbo", Name: "Kind]; DROP",
		Columns: []Column{
			{Name: "Id", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "Text", Type: "nvarchar", Nullable: true},
			{Name: "Drop", Type: "nvarchar", Nullable: true},
		},
	}
	tables := []*TableInfo{crafted, meldungenInfo()}
	// Spaltennamen, die der Erzeuger probiert (echte und böse).
	colPool := []string{"Delete", "Drop]Table", "x] = 1; TRUNCATE TABLE z; --", "Merge", "RV", "Zeit",
		"Titel", "Zustand", "MeldungNr", "Geaendert"}
	colPool = append(colPool, hostile...)
	pickCols := func() []string {
		n := rng.Intn(3) + 1
		out := make([]string, n)
		for i := range out {
			out[i] = colPool[rng.Intn(len(colPool))]
		}
		return out
	}
	kinds := []string{"none", "timestamp", "rowversion", pick()}
	valuePool := []any{nil, "s", int64(1), 2.5, true, []byte{1, 2}, time.Unix(0, 0), struct{}{}, 7, pick(), pick()}

	produced, total := 0, 0
	run := func(st Stmt, err error, src *TableInfo) {
		total++
		if err != nil {
			return
		}
		produced++
		checkStmt(t, st, src, hostile)
	}
	for i := 0; i < 600; i++ {
		ti := tables[rng.Intn(len(tables))]
		keyCols := pickCols()[:1]
		if rng.Intn(3) == 0 {
			keyCols = []string{ti.Columns[0].Name}
		}
		keyVals := make([]string, len(keyCols))
		for j := range keyVals {
			keyVals[j] = pick()
		}
		// SelectRows
		st, err := d.SelectRows(ti, pickCols(), keyCols, colPool[rng.Intn(len(colPool))], kinds[rng.Intn(len(kinds))],
			pick(), pick(), rng.Intn(5)-1, rng.Intn(20)-2)
		run(st, err, ti)
		// SelectRows mit gültigeren Eingaben, damit auch Erfolge entstehen.
		st, err = d.SelectRows(ti, pickCols(), []string{ti.Columns[0].Name}, "", "none", "", []string{"", "[Merge] = 'DROP'"}[rng.Intn(2)], 0, 1+rng.Intn(50))
		run(st, err, ti)
		// SelectOne
		st, err = d.SelectOne(ti, pickCols(), keyCols, keyVals)
		run(st, err, ti)
		st, err = d.SelectOne(ti, pickCols(), []string{ti.Columns[0].Name}, []string{pick()})
		run(st, err, ti)
		// UpdateRow
		set := map[string]any{}
		for j := 0; j < 1+rng.Intn(3); j++ {
			set[colPool[rng.Intn(len(colPool))]] = valuePool[rng.Intn(len(valuePool))]
		}
		st, err = d.UpdateRow(ti, keyCols, keyVals, set)
		run(st, err, ti)
		set2 := map[string]any{[]string{"Drop]Table", "Merge", "Titel", "Zustand"}[rng.Intn(4)]: pick()}
		st, err = d.UpdateRow(ti, []string{ti.Columns[0].Name}, []string{pick()}, set2)
		run(st, err, ti)
		// InsertChild
		vals := map[string]any{}
		for j := 0; j < 1+rng.Intn(3); j++ {
			name := []string{"Text", "Drop", "Id", pick()}[rng.Intn(4)]
			vals[name] = valuePool[rng.Intn(len(valuePool))]
		}
		st, err = d.InsertChild(ti, child, vals)
		run(st, err, ti)
		st, err = d.InsertChild(child, ti, vals)
		run(st, err, child)
		st, err = d.InsertChild(ti, ti, vals)
		if err == nil {
			t.Fatal("InsertChild(source, source) darf nie gelingen")
		}
		run(st, err, ti)
	}
	if total < 200 {
		t.Fatalf("nur %d Kombinationen", total)
	}
	if produced < 50 {
		t.Fatalf("nur %d erzeugte Befehle, Test ist zu schwach", produced)
	}
	t.Logf("%d Kombinationen, %d erzeugte Befehle geprüft", total, produced)

	// Spaltenname mit ] wird verdoppelt.
	st, err := d.SelectOne(crafted, []string{"Drop]Table"}, []string{"Delete"}, []string{"1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "[Drop]]Table]") {
		t.Fatalf("Quoting fehlt: %q", st.SQL)
	}
	if err := ValidateFilter("1=1; DELETE FROM x"); err == nil {
		t.Fatal("Filter mit DELETE muss scheitern")
	}
	_ = fmt.Sprint
}
