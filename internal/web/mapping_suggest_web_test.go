package web

import (
	"bytes"
	"strings"
	"testing"

	"pdh/internal/core/mapsuggest"
)

func TestBuildImportSuggestions(t *testing.T) {
	src := sourceSet{
		Sources: []mapsuggest.Source{{Ref: "lager/x", Label: "lager/x"}, {Ref: "halle/presse3/temp", Label: "halle/presse3/temp", Sample: "21"}},
		Hints:   []string{"Hinweis"},
	}
	v := buildImportSuggestions("c1", src, []mapsuggest.Asset{{ID: "a3", Name: "Presse 3", Path: "Werk › Presse 3"}})
	if v.Direction != "import" || v.ConnectionID != "c1" || len(v.Rows) != 2 || len(v.Hints) != 1 {
		t.Fatalf("%+v", v)
	}
	if r := v.Rows[0]; r.Ref != "halle/presse3/temp" || !r.Checked || r.AssetID != "a3" || r.AssetPath != "Werk › Presse 3" || r.Note != "Einheit °C" || r.Sample != "21" || r.Name == "" {
		t.Errorf("mit Anlage zuerst und angehakt: %+v", r)
	}
	if r := v.Rows[1]; r.Checked || r.AssetID != "" {
		t.Errorf("ohne Anlage nicht angehakt: %+v", r)
	}
}

func TestBuildExportSuggestions(t *testing.T) {
	v := buildExportSuggestions("e1", []ImportMappingView{
		{ID: "m1", InfrastructureName: "Werk › Presse 3", Name: "Strom", LastValue: "3", ConnectionName: "MQTT"},
		{ID: "m2", InfrastructureName: "Werk › Presse 3", Name: "Druck"},
	}, map[string]bool{"m2": true})
	if v.Direction != "export" || len(v.Rows) != 1 {
		t.Fatalf("%+v", v)
	}
	if r := v.Rows[0]; r.ImportMappingID != "m1" || r.FieldName != "Presse_3_Strom" || r.Checked || r.Label != "Strom" || r.AssetPath != "Werk › Presse 3" || r.Sample != "3" {
		t.Errorf("%+v", r)
	}
}

func TestSuggestionsRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(v SuggestionsView) string {
		var b bytes.Buffer
		if err := tmpl.ExecuteTemplate(&b, "mapping-suggestions", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	imp := render(SuggestionsView{Direction: "import", ConnectionID: "c1",
		Rows:  []suggestRow{{Ref: "halle/1/temp", Label: "halle/1/temp", AssetID: "a", AssetPath: "Werk › Presse", Name: "Temp", Checked: true}},
		Hints: []string{"Broker nicht erreichbar: x"}})
	for _, want := range []string{`data-ref="halle/1/temp"`, "Ausgewählte übernehmen", "Broker nicht erreichbar: x", `data-asset="a"`, "Werk › Presse", "checked"} {
		if !strings.Contains(imp, want) {
			t.Errorf("Import enthält %q nicht", want)
		}
	}
	exp := render(SuggestionsView{Direction: "export", ConnectionID: "e1", Rows: []suggestRow{{ImportMappingID: "m1", Label: "Strom", FieldName: "Presse_Strom"}}})
	for _, want := range []string{`data-mapping="m1"`, "Feldname", "Presse_Strom"} {
		if !strings.Contains(exp, want) {
			t.Errorf("Export enthält %q nicht", want)
		}
	}
	if empty := render(SuggestionsView{Direction: "import", ConnectionID: "c1"}); !strings.Contains(empty, "Keine offenen Quellwerte") {
		t.Error("Leerhinweis fehlt")
	}
}

func TestConnectionDetailHasSuggestButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Broker", Kind: "mqtt", KindLabel: "MQTT", CanWrite: true, Tab: "mappings"}
	page := renderPage(t, tmpl, "connection_detail", d)
	if !strings.Contains(page, "/import/connections/11111111-1111-1111-1111-111111111111/suggestions") || !strings.Contains(page, "Zuordnungen vorschlagen") {
		t.Error("Knopf fehlt")
	}
	checkScripts(t, "mapping-suggest", page)
	d.Direction = "export"
	if page := renderPage(t, tmpl, "connection_detail", d); !strings.Contains(page, "Felder vorschlagen") {
		t.Error("Export-Knopf fehlt")
	}
	d.CanWrite = false
	if strings.Contains(renderPage(t, tmpl, "connection_detail", d), "/suggestions") {
		t.Error("ohne Schreibrecht kein Knopf")
	}
	d.Kind = efficioKind
	d.CanWrite = true
	if strings.Contains(renderPage(t, tmpl, "connection_detail", d), "/suggestions") {
		t.Error("Efficio hat keine Felder")
	}
}

// Schlussprüfung M3: Übernommen und gezählt werden nur sichtbare Zeilen –
// was der Filter ausblendet, bleibt liegen, auch wenn es angehakt ist.
func TestSuggestApplyOnlyVisibleRows(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Broker", Kind: "mqtt", KindLabel: "MQTT", CanWrite: true, Tab: "mappings"}
	page := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{
		"const chosenRows = () => rows().filter(r => !r.hidden && pick(r).checked);",
		"const chosen = chosenRows();",
		"ausgeblendet – werden nicht übernommen",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	if strings.Contains(page, "const chosen = rows().filter(r => pick(r).checked);") {
		t.Error("Übernehmen nimmt noch ausgeblendete Zeilen mit")
	}
	checkScripts(t, "mapping-suggest-visible", page)
}

// Am Handy stehen die Angaben einer Zeile untereinander (keine breite Tabelle
// zum Seitwärtsrollen), lange Quellnamen brechen um.
func TestSuggestListPhoneLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Broker", Kind: "mqtt", KindLabel: "MQTT", CanWrite: true, Tab: "mappings"}
	page := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{
		".cd-sugg-src{max-width:340px;overflow-wrap:anywhere}",
		"@media (max-width:900px){.cd-sugg-tbl thead{display:none}",
		".cd-sugg-tbl td[data-label]::before{content:attr(data-label)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	var buf strings.Builder
	v := SuggestionsView{Direction: "import", ConnectionID: d.ID, Rows: []suggestRow{{Ref: "a/b", Label: "a/b", Name: "Strom"}}}
	if err := tmpl.ExecuteTemplate(&buf, "mapping-suggestions", v); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-label="Anlage"`, `data-label="Bezeichnung"`, `data-label="Notiz"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%s fehlt in der Zeile", want)
		}
	}
}
