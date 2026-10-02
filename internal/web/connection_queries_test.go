package web

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSelectSQL(t *testing.T) {
	ok := []string{
		"SELECT * FROM messwerte ORDER BY ts DESC LIMIT 1",
		"  -- Kommentar\nselect a, b from t;",
		"WITH x AS (SELECT 1 AS a) SELECT a FROM x",
		"SELECT 'update; drop' AS text FROM t", // Woerter in Zeichenketten sind egal
		"SELECT TOP 1 * FROM [dbo].[Zaehler] ORDER BY Zeit DESC",
	}
	for _, q := range ok {
		if err := validateSelectSQL(q); err != nil {
			t.Errorf("%q abgelehnt: %v", q, err)
		}
	}
	bad := []string{
		"", "DELETE FROM t", "UPDATE t SET a = 1", "SELECT 1; DROP TABLE t",
		"WITH x AS (SELECT 1) DELETE FROM t", "SELECT * INTO kopie FROM t", "EXEC sp_who",
		"/* tarnen */ INSERT INTO t VALUES (1)", "PRAGMA writable_schema = 1",
	}
	for _, q := range bad {
		if err := validateSelectSQL(q); err == nil {
			t.Errorf("%q wurde angenommen", q)
		}
	}
}

func TestRunSQLQuerySQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maschine.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE messwerte (id INTEGER PRIMARY KEY, temp REAL, stueck INTEGER)`,
		`INSERT INTO messwerte (temp, stueck) VALUES (21.5, 100), (22.25, 140), (23, 180)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := map[string]string{"file_path": path}
	res, err := runConnQuery(context.Background(), "sqlite", cfg, map[string]string{"sql": "SELECT temp, stueck FROM messwerte ORDER BY id DESC LIMIT 1", "row": "first"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Columns, ",") != "temp,stueck" || len(res.Rows) != 1 || res.Rows[0][1] != "180" {
		t.Errorf("Ergebnis falsch: %+v", res)
	}
	if v, ok := res.value(map[string]string{}, "temp"); !ok || v != "23" {
		t.Errorf("Wert temp = %q", v)
	}
	all, _ := runConnQuery(context.Background(), "sqlite", cfg, map[string]string{"sql": "SELECT id FROM messwerte ORDER BY id", "row": "last"})
	if v, _ := all.value(map[string]string{"row": "last"}, "id"); v != "3" {
		t.Errorf("letzte Zeile: %q", v)
	}
	if _, err := runConnQuery(context.Background(), "sqlite", cfg, map[string]string{"sql": "DELETE FROM messwerte"}); err == nil {
		t.Error("DELETE wurde ausgeführt")
	}
	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messwerte`).Scan(&n)
	if n != 3 {
		t.Errorf("Daten verändert: %d Zeilen", n)
	}
}

func TestJSONQueryResult(t *testing.T) {
	res, err := jsonToQueryResult([]byte(`{"data":{"items":[{"id":1,"wert":{"temp":21.5}},{"id":2,"wert":{"temp":22},"extra":"x"}]},"ok":true}`), "data.items")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Columns, ",") != "extra,id,wert.temp" || len(res.Rows) != 2 || res.Rows[1][2] != "22" || res.Rows[0][0] != "" {
		t.Errorf("Liste falsch: %+v", res)
	}
	one, _ := jsonToQueryResult([]byte(`{"status":"run","werte":[5,6]}`), "")
	if len(one.Rows) != 1 || strings.Join(one.Columns, ",") != "status,werte[0],werte[1]" {
		t.Errorf("Objekt falsch: %+v", one)
	}
	plain, _ := jsonToQueryResult([]byte(`[3,4]`), "")
	if strings.Join(plain.Columns, ",") != "wert" || len(plain.Rows) != 2 {
		t.Errorf("Liste aus Werten falsch: %+v", plain)
	}
	text, _ := jsonToQueryResult([]byte("OK 42"), "")
	if text.Columns[0] != "text" || text.Rows[0][0] != "OK 42" {
		t.Errorf("Text falsch: %+v", text)
	}
	if _, err := jsonToQueryResult([]byte(`{"a":1}`), "gibt.es.nicht"); err == nil {
		t.Error("fehlender Pfad nicht gemeldet")
	}
	if v, ok := jsonAtPath(map[string]interface{}{"r": []interface{}{map[string]interface{}{"x": "y"}}}, "r[0].x"); !ok || v != "y" {
		t.Errorf("jsonAtPath: %v %v", v, ok)
	}
}

func TestQueryTargetURL(t *testing.T) {
	for _, c := range []struct{ base, path, want string }{
		{"https://erp.local/api/v1", "", "https://erp.local/api/v1"},
		{"https://erp.local/api/v1", "/status", "https://erp.local/status"},
		{"https://erp.local/api/v1", "maschinen/7", "https://erp.local/api/v1/maschinen/7"},
		{"https://erp.local/api/v1/", "maschinen", "https://erp.local/api/v1/maschinen"},
		{"https://erp.local/api", "?limit=1", "https://erp.local/api?limit=1"},
		{"https://erp.local/api", "https://andere.local/x", "https://andere.local/x"},
	} {
		got, err := queryTargetURL(c.base, c.path)
		if err != nil || got != c.want {
			t.Errorf("queryTargetURL(%q, %q) = %q, %v – erwartet %q", c.base, c.path, got, err, c.want)
		}
	}
}

func TestTabularQueryLastRowBeyondPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.csv")
	var b strings.Builder
	b.WriteString("zeit;wert\n")
	for i := 1; i <= excelPreviewMaxRows+250; i++ {
		fmt.Fprintf(&b, "t%d;%d\n", i, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{"source_path": path, "delimiter": ";", "has_header": "true"}
	res, err := runConnQuery(context.Background(), "csv", cfg, map[string]string{"row": "last"})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint(excelPreviewMaxRows + 250)
	if v, _ := res.value(map[string]string{"row": "last"}, "wert"); v != want {
		t.Errorf("letzte Zeile: %q, erwartet %q", v, want)
	}
	if !res.Truncated || len(res.Rows) != queryMaxRows {
		t.Errorf("Kürzung: %v / %d Zeilen", res.Truncated, len(res.Rows))
	}
}

func TestQuerySpecAndRefs(t *testing.T) {
	if id, col, ok := parseQueryRef(queryRef("abc", "wert:a")); !ok || id != "abc" || col != "wert:a" {
		t.Errorf("parseQueryRef: %q %q %v", id, col, ok)
	}
	if _, _, ok := parseQueryRef("holding:100:float32"); ok || isQueryRef("Halle/Temp") {
		t.Error("normale Quellwerte als Abfrage erkannt")
	}
	spec, err := normalizeQuerySpec("modbus", map[string]string{"area": "coil", "start": "5", "count": "8", "type": "float32"})
	if err != nil || spec["type"] != "bool" {
		t.Errorf("Modbus-Coils: %v %v", spec, err)
	}
	if _, err := normalizeQuerySpec("modbus", map[string]string{"count": "500"}); err == nil {
		t.Error("zu viele Register angenommen")
	}
	if spec, err := normalizeQuerySpec("opcua", map[string]string{"nodes": "i=2258, ns=2;s=Linie1.Temp\n\n"}); err != nil || spec["nodes"] != "i=2258\nns=2;s=Linie1.Temp" {
		t.Errorf("OPC-UA-Knoten: %q %v", spec["nodes"], err)
	}
	if _, err := normalizeQuerySpec("opcua", map[string]string{"nodes": "Unsinn"}); err == nil {
		t.Error("ungültiger Knoten angenommen")
	}
	if _, err := normalizeQuerySpec("mqtt", map[string]string{}); err == nil {
		t.Error("MQTT-Abfrage angenommen")
	}
	// alle eingebauten Vorlagen muessen gueltig sein
	for _, kind := range []string{"sqlite", "mysql", "mssql", "web", "rest_api", "modbus", "opcua", "excel", "csv"} {
		tpls := builtinQueryTemplates(kind)
		if len(tpls) == 0 {
			t.Errorf("%s: keine Vorlagen", kind)
		}
		for _, tpl := range tpls {
			if _, err := normalizeQuerySpec(kind, tpl.Spec); err != nil {
				t.Errorf("%s / %s: %v", kind, tpl.Name, err)
			}
		}
	}
	if !strings.Contains(builtinQueryTemplates("mssql")[0].Spec["sql"], "TOP 1") || !strings.Contains(builtinQueryTemplates("mysql")[0].Spec["sql"], "LIMIT 1") {
		t.Error("SQL-Dialekt der Vorlagen falsch")
	}
}

func TestConnectionDetailRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	okRun := true
	imp := ConnectionDetailData{
		Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Presse 3", Kind: "sqlite", KindLabel: "SQLite", Enabled: true,
		CanWrite: true, CanExecute: true, Tab: "queries", QueriesSupported: true,
		Config: []connConfigRow{{"Datenbankdatei", "/daten/presse.db"}},
		Check:  &checkReport{At: "01.10.2026", Items: []checkResult{{Group: "Erreichbarkeit", Name: "Datei", Status: "fail", Detail: "fehlt"}}, Fail: 1},
		Queries: []connQuery{{ID: "q1", Name: "Zähler", Spec: map[string]string{"sql": "SELECT a FROM t", "row": "first"}, SpecJSON: `{"sql":"SELECT a FROM t"}`, Enabled: true, IntervalMinutes: 5,
			LastOK: &okRun, LastMessage: "1 Zeile(n)", LastResult: &queryResult{Columns: []string{"a"}, Rows: [][]string{{"42"}}}}},
		QueryFields: queryFormFields("sqlite"), Builtins: builtinQueryTemplates("sqlite"),
		Mappings: []connMappingRow{{ImportMappingView: ImportMappingView{ID: "m1", Name: "Zählerstand", SourceRef: "q:q1:a", LastValue: "42"}, QueryName: "Zähler", Column: "a"}},
		Tools:    connectionTools("import", "sqlite", "x", nil),
	}
	page := renderPage(t, tmpl, "connection_detail", imp)
	for _, want := range []string{"Presse 3", "Jetzt prüfen", `data-tab="queries"`, "Zähler", "cdMap(", "Speichern &amp; ausführen", "Letzter Datensatz", "<b>Zähler</b> › a", "Datenbank durchsuchen", `id="cd-map"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Importseite enthält %q nicht", want)
		}
	}
	checkScripts(t, "verbindung-import", page)
	exp := ConnectionDetailData{Direction: "export", ID: "x", Name: "Tagesbericht", Kind: "pdf", KindLabel: "PDF", ExportMappings: []ExportMappingView{{FieldName: "Temp", SourceName: "Öltemperatur"}}, Tools: connectionTools("export", "pdf", "x", nil)}
	ep := renderPage(t, tmpl, "connection_detail", exp)
	for _, want := range []string{"Tagesbericht", "Felder im Export", "Temp", "Exportvorlagen"} {
		if !strings.Contains(ep, want) {
			t.Errorf("Exportseite enthält %q nicht", want)
		}
	}
	if strings.Contains(ep, `data-tab="queries"`) || strings.Contains(ep, "/checks\"") {
		t.Error("Exportseite ohne Rechte zeigt Abfragen oder Prüfknopf")
	}
	checkScripts(t, "verbindung-export", ep)
}
