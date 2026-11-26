package web

import (
	"errors"
	"strings"
	"testing"
)

// Datenbank-Vorschau für SQL Server (0.82.69): TOP statt LIMIT, Tabellen mit Schema, Sichten,
// Vorlagen zum Auslesen, Hinweis bei Windows-Anmeldung aus fremder Domäne.

func TestSQLPreviewQueryDialect(t *testing.T) {
	cases := []struct{ kind, table, want string }{
		{"mssql", "FaultAreas", "SELECT TOP 20 * FROM [FaultAreas]"},
		{"mssql", "erp.Artikel", "SELECT TOP 20 * FROM [erp].[Artikel]"},
		{"mysql", "sensoren", "SELECT * FROM `sensoren` LIMIT 20"},
		{"sqlite", "sensors", `SELECT * FROM "sensors" LIMIT 20`},
	}
	for _, c := range cases {
		if got := sqlPreviewQuery(c.kind, c.table, 20); got != c.want {
			t.Errorf("%s %s: %q, erwartet %q", c.kind, c.table, got, c.want)
		}
	}
}

func TestSQLLastRowQueryDialect(t *testing.T) {
	got := sqlLastRowQuery("mssql", "erp.Artikel", []string{"Nr", "Bestand"}, "[Nr]")
	if got != "SELECT TOP 1 [Nr], [Bestand] FROM [erp].[Artikel] ORDER BY [Nr] DESC" {
		t.Errorf("mssql: %q", got)
	}
	if got := sqlLastRowQuery("sqlite", "sensors", []string{"wert"}, "rowid"); got != `SELECT "wert" FROM "sensors" ORDER BY rowid DESC LIMIT 1` {
		t.Errorf("sqlite: %q", got)
	}
}

func TestQuoteSQLTable(t *testing.T) {
	for in, want := range map[string]string{"Artikel": "[Artikel]", "erp.Artikel": "[erp].[Artikel]", "a]b.c": "[a]]b].[c]"} {
		if got := quoteSQLTable("mssql", in); got != want {
			t.Errorf("mssql %q: %q, erwartet %q", in, got, want)
		}
	}
	if got := quoteSQLTable("sqlite", "a.b"); got != `"a.b"` {
		t.Errorf("sqlite: %q", got)
	}
}

// Quelle „Tabelle.Spalte“: Die Tabelle ist der längste Anfang, den es in der Datenbank gibt –
// so bleiben alte Zuordnungen („Artikel.Bestand.Ist“ = Spalte „Bestand.Ist“) richtig, und
// Tabellen mit Schema oder Punkt im Namen gehen auch.
func TestSplitSQLSourceRef(t *testing.T) {
	known := sqlKnownTables("mssql", []string{"dbo.Artikel", "erp.Artikel", "dbo.Teil.Nr", "dbo.FaultAreas"})
	cases := []struct{ kind, ref, table, col string }{
		{"mssql", "FaultAreas.Name", "FaultAreas", "Name"},
		{"mssql", "dbo.FaultAreas.Name", "dbo.FaultAreas", "Name"},
		{"mssql", "erp.Artikel.Bestand", "erp.Artikel", "Bestand"},
		{"mssql", "Artikel.Bestand.Ist", "Artikel", "Bestand.Ist"},
		{"mssql", "dbo.Teil.Nr.Wert", "dbo.Teil.Nr", "Wert"},
		{"mssql", "Unbekannt.a.b", "Unbekannt", "a.b"}, // nicht in der Liste: wie bisher erster Punkt
		{"sqlite", "sensors.wert", "sensors", "wert"},
		{"mysql", "a.b.c", "a", "b.c"},
	}
	for _, c := range cases {
		table, col, ok := splitSQLSourceRef(c.kind, c.ref, known)
		if !ok || table != c.table || col != c.col {
			t.Errorf("%s %q: %q %q %v", c.kind, c.ref, table, col, ok)
		}
	}
	if _, _, ok := splitSQLSourceRef("mssql", "ohnePunkt", known); ok {
		t.Error("Quelle ohne Spalte angenommen")
	}
	if table, col, ok := splitSQLSourceRef("mssql", "erp.Artikel.Bestand", nil); !ok || table != "erp" || col != "Artikel.Bestand" {
		t.Errorf("ohne Tabellenliste wie bisher: %q %q", table, col)
	}
}

func TestSQLSchemaAndName(t *testing.T) {
	if s, n := sqlSchemaAndName("erp.Artikel"); s != "erp" || n != "Artikel" {
		t.Errorf("%q %q", s, n)
	}
	if s, n := sqlSchemaAndName("Artikel"); s != "" || n != "Artikel" {
		t.Errorf("ohne Schema: %q %q", s, n)
	}
	if s, n := sqlSchemaAndName("dbo.Teil.Nr"); s != "dbo" || n != "Teil.Nr" {
		t.Errorf("Punkt im Tabellennamen: %q %q", s, n)
	}
}

func TestSqlitePatternEscaped(t *testing.T) {
	for _, tpl := range builtinQueryTemplates("sqlite") {
		if strings.Contains(tpl.Spec["sql"], "'sqlite_%'") {
			t.Errorf("%s: „_“ in LIKE ohne ESCAPE", tpl.Name)
		}
	}
}

// Vorlagen zum Auslesen stehen vorn und sind im Dialekt der Datenbank geschrieben.
func TestBuiltinReadTemplates(t *testing.T) {
	names := func(kind string) map[string]string {
		out := map[string]string{}
		for _, tpl := range builtinQueryTemplates(kind) {
			out[tpl.Name] = tpl.Spec["sql"]
		}
		return out
	}
	ms := names("mssql")
	for _, want := range []string{"Alle Tabellen und Sichten", "Spalten einer Tabelle", "Tabelle ansehen (20 Zeilen)", "Zeilen je Tabelle"} {
		if ms[want] == "" {
			t.Errorf("SQL Server ohne Vorlage %q", want)
		}
	}
	if first := builtinQueryTemplates("mssql")[0].Name; first != "Alle Tabellen und Sichten" {
		t.Errorf("erste Vorlage %q", first)
	}
	if !strings.Contains(ms["Tabelle ansehen (20 Zeilen)"], "TOP 20") || strings.Contains(ms["Tabelle ansehen (20 Zeilen)"], "LIMIT") {
		t.Errorf("SQL Server: %q", ms["Tabelle ansehen (20 Zeilen)"])
	}
	if !strings.Contains(ms["Alle Tabellen und Sichten"], "INFORMATION_SCHEMA.TABLES") {
		t.Errorf("SQL Server Tabellen: %q", ms["Alle Tabellen und Sichten"])
	}
	my := names("mysql")
	if !strings.Contains(my["Tabelle ansehen (20 Zeilen)"], "LIMIT 20") {
		t.Errorf("MySQL: %q", my["Tabelle ansehen (20 Zeilen)"])
	}
	sq := names("sqlite")
	if !strings.Contains(sq["Alle Tabellen und Sichten"], "sqlite_master") || !strings.Contains(sq["Spalten einer Tabelle"], "pragma_table_info") {
		t.Errorf("SQLite: %v", sq)
	}
	for _, tpl := range builtinQueryTemplates("mssql")[:4] {
		if tpl.Interval != 0 {
			t.Errorf("%s läuft automatisch (alle %d Min.) – Vorlagen zum Auslesen nur von Hand", tpl.Name, tpl.Interval)
		}
	}
}

func TestConnErrorHintUntrustedDomain(t *testing.T) {
	de := errors.New("mssql: login error: Anmeldefehler. Die Anmeldung stammt aus einer nicht vertrauenswürdigen Domäne und kann mit der integrierten Authentifizierung nicht verwendet werden.")
	en := errors.New("mssql: login error: Login failed. The login is from an untrusted domain and cannot be used with Integrated authentication.")
	for _, err := range []error{de, en} {
		h := connErrorHint("mssql", map[string]string{"username": `FIRMA\m.muster`}, err)
		for _, want := range []string{"kurzen Domänennamen", "Kennwort", "eigenen SQL-Benutzer"} {
			if !strings.Contains(h, want) {
				t.Errorf("Hinweis ohne %q: %s", want, h)
			}
		}
	}
	if h := connErrorHint("mssql", map[string]string{"username": "pdh_leser"}, de); !strings.Contains(h, "SQL Server- und Windows-Authentifizierungsmodus") || !strings.Contains(h, "Möglicherweise") {
		t.Errorf("ohne Domäne: %s", h)
	}
	if h := connErrorHint("mssql", nil, errors.New("connection refused")); h != "" {
		t.Errorf("Hinweis bei anderem Fehler: %s", h)
	}
	if h := connErrorHint("mysql", nil, de); h != "" {
		t.Errorf("Hinweis bei MySQL: %s", h)
	}
}

// Gegen den Labor-SQL-Server (PDH_TEST_MSSQL_DSN): Tabellen mit Schema und Sichten, Vorschau,
// letzte Zeile und alle Vorlagen zum Auslesen laufen ohne Syntaxfehler.
func TestMssqlBrowseLab(t *testing.T) {
	cfg := labMssqlConfig(t)
	cfg["use_tls"] = "false"
	db, _, err := openSQLConnection("mssql", cfg)
	if err != nil {
		t.Fatalf("öffnen: %v", err)
	}
	defer db.Close()
	tables, err := listSQLTables(db, "mssql")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, x := range tables {
		have[x] = true
	}
	// immer mit Schema – sonst entscheidet das Standardschema der Anmeldung, welche Tabelle gemeint ist
	for _, want := range []string{"dbo.Meldungen", "erp.Artikel", "dbo.MeldungenOffen"} {
		if !have[want] {
			t.Errorf("Tabelle %q fehlt in %v", want, tables)
		}
	}
	for _, ref := range []string{"MeldungenAlt", "dbo.MeldungenAlt"} { // alte Zuordnungen ohne Schema gehen weiter
		if last, err := readSQLLastRow(db, "mssql", ref, []string{"Titel"}); err != nil || last["Titel"] != "Alt 10" {
			t.Errorf("letzte Zeile %s: %v %v", ref, err, last)
		}
	}
	cols, rows, err := readSQLPreview(db, "mssql", "erp.Artikel")
	if err != nil || len(cols) != 3 || len(rows) != 3 {
		t.Fatalf("Vorschau erp.Artikel: %v %v %d", err, cols, len(rows))
	}
	if _, rows, err := readSQLPreview(db, "mssql", "MeldungenOffen"); err != nil || len(rows) == 0 {
		t.Errorf("Vorschau der Sicht: %v %d", err, len(rows))
	}
	last, err := readSQLLastRow(db, "mssql", "erp.Artikel", []string{"Bezeichnung"})
	if err != nil || last["Bezeichnung"] != "Sicherung 16 A" {
		t.Errorf("letzte Zeile: %v %v", err, last)
	}
	// Platzhalter wie ein Anwender ersetzen (Reihenfolge: längster zuerst)
	repl := strings.NewReplacer("<tabellenname>", "Artikel", "<schema>", "erp", "<tabelle>", "erp.Artikel")
	for _, tpl := range builtinQueryTemplates("mssql")[:4] {
		q := repl.Replace(tpl.Spec["sql"])
		if err := validateSelectSQL(q); err != nil {
			t.Errorf("Vorlage %q abgelehnt: %v", tpl.Name, err)
		}
		r, err := db.Query(q)
		if err != nil {
			t.Errorf("Vorlage %q: %v\n%s", tpl.Name, err, q)
			continue
		}
		n := 0
		for r.Next() {
			n++
		}
		r.Close()
		if n == 0 {
			t.Errorf("Vorlage %q ohne Zeilen\n%s", tpl.Name, q)
		}
	}
}
