package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Die sechs Import-Vorschauen nutzen denselben Zuordnungs-Baustein
// (widgets/import_mapping.gohtml) – Dialog, Tabelle und Skript.
func TestImportPreviewsShareMappingWidget(t *testing.T) {
	tmpl := loadTestTemplates(t)
	const cid = "11111111-1111-1111-1111-111111111111"
	maps := []ImportMappingView{{ID: "m1", SourceRef: "ns=2;s=Temp", Name: "Temperatur Halle 1", InfrastructureName: "Presse 3", LastValue: "21.5", CreatedAt: "01.10.2026"}}
	cases := []struct {
		page       string
		data       any
		mark, head string
		extra      []string
	}{
		{"excel_preview", ExcelPreviewPageData{ConnectionID: cid, Applicable: true, CanWrite: true, Columns: []ExcelColumn{{Index: 0, Label: "Temp"}}, Rows: [][]string{{"1"}}, Mappings: maps}, "Spalte markieren", ">Spalte<", nil},
		{"import_response", ImportResponsePageData{ConnectionID: cid, Applicable: true, CanWrite: true, Fields: []JSONField{{Path: "a.b", Value: "1"}}, Mappings: maps}, "Wert markieren", ">Pfad<", nil},
		{"modbus_read", ModbusPageData{ConnectionID: cid, Applicable: true, CanWrite: true, TestResult: "42", Mappings: maps}, "Wert markieren", ">Quellwert<", []string{"testRegisterTypeChanged);", "importMarkOpen(type + ':'"}},
		{"mqtt_sniffer", MqttSnifferPageData{ConnectionID: cid, CanWrite: true, CanExecute: true, Mappings: maps}, "Wert markieren", ">Topic<", []string{">Angelegt<", ">Empfangen<", "01.10.2026", "im Hintergrund abonniert"}},
		{"opcua_browse", OPCUAPageData{ConnectionID: cid, Applicable: true, CanWrite: true, Fields: []OPCUANodeField{{NodeID: "ns=2;s=Temp"}}, Mappings: maps}, "Wert markieren", ">NodeID<", []string{"font-family:monospace\">ns=2;s=Temp"}},
		{"sql_browse", SQLPreviewPageData{ConnectionID: cid, Applicable: true, CanWrite: true, Tables: []string{"t"}, SelectedTable: "t", Columns: []string{"c"}, Rows: [][]string{{"1"}}, Mappings: maps}, "Spalte markieren", ">Tabelle.Spalte<", []string{"importMarkOpen(table + '.' + column)"}},
	}
	for _, c := range cases {
		out := renderPage(t, tmpl, c.page, c.data)
		want := append([]string{`id="mark-panel"`, `id="mark-source"`, c.mark, c.head, "Zuordnungen · 1", "Temperatur Halle 1", "Presse 3",
			"function importMarkOpen(", "/import/connections/" + cid + "/mappings", "mappingContextItems(this)"}, c.extra...)
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt", c.page, w)
			}
		}
		if n := strings.Count(out, "function mappingContextItems("); n != 1 {
			t.Errorf("%s: mappingContextItems %d× definiert", c.page, n)
		}
		checkScripts(t, c.page, out)
	}
}

// Import und Export teilen Verbindungstabelle und Skript (widgets/connection_list.gohtml).
func TestConnectionPagesShareListWidget(t *testing.T) {
	tmpl := loadTestTemplates(t)
	conns := []ConnectionView{{ID: "c1", Kind: "mqtt", KindLabel: "MQTT", Name: "Halle 2", Enabled: true, ConfigJSON: `{"use_tls":"true"}`, IsIntegratedBroker: true, CreatedBy: "Max", CreatedAt: "01.10.2026"}}
	for _, dir := range []string{"import", "export"} {
		out := renderPage(t, tmpl, dir, ConnectionsPageData{Direction: dir, Connections: conns, CanWrite: true, CanExecute: true})
		for _, w := range []string{"Bestehende Verbindungen · 1", `href="/` + dir + `/connections/c1"`, "connContextItems(this, '" + dir + "')",
			"function editConnFromRow(", "function connPageItems(", "const CONN_BOOL_FIELDS", "get('edit')"} {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt", dir, w)
			}
		}
		if n := strings.Count(out, "function connContextItems("); n != 1 {
			t.Errorf("%s: connContextItems %d× definiert", dir, n)
		}
		if broker := strings.Contains(out, "data-conn-broker-integrated"); broker != (dir == "import") {
			t.Errorf("%s: Broker-Merkmal falsch (%v)", dir, broker)
		}
		checkScripts(t, dir, out)
	}
}

// Der Kostenstellen-Baustein laedt seine Liste selbst (vorher nur auf drei
// Seiten – sonst blieb das Feld leer und Speichern loeschte die Kostenstelle).
func TestCostCenterPickerLoadsItself(t *testing.T) {
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, id := range []string{"a", "b"} { // zweimal auf derselben Seite (wie in der Anlagen-Detailseite)
		if err := c.ExecuteTemplate(&b, "cost-center-picker", map[string]any{"Label": "Kostenstelle", "TargetID": id, "Value": "cc1"}); err != nil {
			t.Fatal(err)
		}
	}
	out := b.String()
	for _, w := range []string{"window.__pdhCostCenters", "/api/v1/costcenters", "dataset.ccLoaded", `data-selected="cc1"`} {
		if !strings.Contains(out, w) {
			t.Errorf("Kostenstellen-Baustein: %q fehlt", w)
		}
	}
	checkScripts(t, "kostenstelle", out)
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "*.gohtml"))
	for _, f := range files {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), "populateCostCenterSelects") {
			t.Errorf("%s: eigene Kopie des Kostenstellen-Laders – der Baustein laedt selbst", filepath.Base(f))
		}
	}
}
