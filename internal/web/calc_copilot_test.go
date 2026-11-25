package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pdh/internal/modules/calc"
)

func calcToolNames(canWrite bool) map[string]bool {
	out := map[string]bool{}
	for _, t := range calcCopilotTools(canWrite) {
		out[t.Name] = true
	}
	return out
}

func TestCalcCopilotToolsWrite(t *testing.T) {
	read := calcToolNames(false)
	if read["propose_calc"] || !read["calc_values"] || !read["calc_inputs"] || !read["asset_tree"] {
		t.Errorf("ohne Schreibrecht: %v", read)
	}
	// die Werkzeuge der gewöhnlichen Verbindungsseite gibt es hier nicht
	for _, name := range []string{"connection_info", "mappings", "source_values", "propose_mappings"} {
		if read[name] {
			t.Errorf("%s gehört nicht auf die Seite „Berechnete Werte“", name)
		}
	}
	write := calcToolNames(true)
	if !write["propose_calc"] {
		t.Errorf("mit Schreibrecht: %v", write)
	}
	for name := range write {
		if copilotToolLabels[name] == "" {
			t.Errorf("Beschriftung für %s fehlt", name)
		}
	}
	for _, tool := range calcCopilotTools(true) {
		if tool.Name == "propose_calc" && (!strings.Contains(tool.Description, "WENN") || !strings.Contains(tool.Description, "„;“")) {
			t.Errorf("propose_calc nennt Funktionen und Trennzeichen nicht: %s", tool.Description)
		}
	}
}

func TestCalcCopilotPromptRules(t *testing.T) {
	p := calcCopilotPrompt(&copilotConn{Direction: "import", Kind: calc.Kind, Name: "Berechnete Werte", CanWrite: true})
	for _, want := range []string{"propose_calc", "Übernehmen", "Kennw", ";", "calc_values", "calc_inputs", "valid_from", "von Hand"} {
		if !strings.Contains(p, want) {
			t.Errorf("%q fehlt in den Regeln", want)
		}
	}
	if strings.Contains(p, "Im Internet") || strings.Contains(p, "propose_mappings") {
		t.Errorf("Regeln der gewöhnlichen Verbindungsseite stehen auf dieser Seite: %s", p)
	}
	ro := calcCopilotPrompt(&copilotConn{Direction: "import", Kind: calc.Kind, Name: "Berechnete Werte"})
	if strings.Contains(ro, "propose_calc") || !strings.Contains(ro, "nichts ändern") {
		t.Errorf("ohne Schreibrecht: %s", ro)
	}
}

func TestCopilotNoWebOnCalcPage(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := calcTestPage()
	d.CopilotConn, d.CopilotNoWeb = "import:"+calcTestConn, true
	page := withUIScripts(t, renderPage(t, tmpl, "calc_values", d))
	if !strings.Contains(page, "cop-privacy") || !strings.Contains(page, "an Anthropic") {
		t.Error("Seite „Berechnete Werte“: der Hinweis zum Datenschutz fehlt")
	}
	if strings.Contains(page, `id="cop-web"`) || bootValue(t, page, "copilotConn") != "import:"+calcTestConn {
		t.Error("Seite „Berechnete Werte“: kein Haken für die Internetsuche, aber die Verbindung für den Copilot")
	}
	cd := ConnectionDetailData{Direction: "import", ID: calcTestConn, Name: "Broker", Kind: "mqtt", KindLabel: "MQTT", CanWrite: true, Tab: "mappings"}
	cd.CopilotConn = "import:" + calcTestConn
	if page := renderPage(t, tmpl, "connection_detail", cd); !strings.Contains(page, `id="cop-web"`) {
		t.Error("gewöhnliche Verbindungsseite: der Haken für die Internetsuche fehlt")
	}
	if !strings.Contains(page, "r.formula") || !strings.Contains(page, "r.probe") {
		t.Error("die Änderungsliste zeigt Formel und Probe nicht")
	}
}

func TestProposalViewCalc(t *testing.T) {
	dec := 1
	p := &mappingProposal{ID: "p1", Direction: "import", Kind: calc.Kind, Summary: "Summe anlegen", Rows: []proposalRow{
		{Op: proposalOpAdd, Label: "Gesamtleistung", Name: "Gesamtleistung", AssetPath: "Halle 2", Unit: "kW", Decimals: &dec,
			Formula: "A + B", ValidFrom: "2026-12-01", Probe: "37,6 kW", Valid: true,
			Inputs: []calcProposalInput{{Short: "A", MappingID: calcTestInput, Label: "Halle 2 › Presse 1 › Leistung"}}},
		{Op: proposalOpDelete, MappingID: calcTestValue, Label: "Alt", Problem: errProposalDelete},
	}}
	view := proposalView(p)
	rows := view["rows"].([]map[string]any)
	if r := rows[0]; r["formula"] != "A + B" || r["probe"] != "37,6 kW" || r["valid_from"] != "01.12.2026" || r["unit"] != "kW" || r["label"] != "Gesamtleistung" || r["asset"] != "Halle 2" {
		t.Errorf("Zeile: %+v", r)
	}
	b, _ := json.Marshal(view)
	for _, bad := range []string{"calc:", calcTestInput, calcTestValue} {
		if strings.Contains(string(b), bad) {
			t.Errorf("Kennung %q in der Ansicht: %s", bad, b)
		}
	}
}

func TestCalcRowsFromInput(t *testing.T) {
	var in map[string]any
	_ = json.Unmarshal([]byte(`{"rows":[
		{"op":"ADD","name":" Summe ","infrastructure_id":"i1","unit":"kW","decimals":1,"max_age_minutes":0,"formula":"A + B","valid_from":"2026-12-01","reason":"Summe der Pressen",
		 "inputs":[{"short":"A","mapping_id":"m1"},{"short":"B","mapping_id":"m2"}]},
		{"op":"change","value_id":"v1","formula":"A * 2","inputs":[{"short":"A","mapping_id":"m1"}]},
		"kein Objekt"]}`), &in)
	rows, err := calcRowsFromInput(in)
	if err != nil || len(rows) != 2 {
		t.Fatalf("Zeilen: %+v %v", rows, err)
	}
	r := rows[0]
	if r.Op != proposalOpAdd || r.Name != "Summe" || r.InfrastructureID != "i1" || r.Unit != "kW" || r.Decimals == nil || *r.Decimals != 1 ||
		r.MaxAgeMinutes == nil || *r.MaxAgeMinutes != 0 || r.Formula != "A + B" || r.ValidFrom != "2026-12-01" || r.Reason != "Summe der Pressen" ||
		len(r.Inputs) != 2 || r.Inputs[1].Short != "B" || r.Inputs[1].MappingID != "m2" || r.Valid {
		t.Errorf("Zeile 1: %+v", r)
	}
	if r := rows[1]; r.Op != proposalOpChange || r.MappingID != "v1" || r.Decimals != nil || r.MaxAgeMinutes != nil {
		t.Errorf("Zeile 2: %+v", r)
	}
	many := map[string]any{"rows": make([]any, maxCalcProposalRows+1)}
	for i := range many["rows"].([]any) {
		many["rows"].([]any)[i] = map[string]any{"op": "add"}
	}
	if _, err := calcRowsFromInput(many); err == nil || !strings.Contains(err.Error(), "50") {
		t.Errorf("zu viele Zeilen: %v", err)
	}
	if _, err := calcRowsFromInput(map[string]any{}); err == nil {
		t.Error("ohne Zeilen muss es einen Fehler geben")
	}
}

// Auf der Seite „Berechnete Werte“ dürfen die Werkzeuge der gewöhnlichen
// Verbindungsseite nicht laufen (sie zeigten die interne Quelle der Werte).
func TestCalcConnBlocksMappingTools(t *testing.T) {
	c := &copilotConn{Direction: "import", Kind: calc.Kind, ID: calcTestConn, Name: "Berechnete Werte", CanWrite: true}
	d := &copilotData{h: &Handler{}, conn: c, can: func(string) bool { return true }}
	for _, name := range []string{"connection_info", "mappings", "source_values", "export_sources", "propose_mappings"} {
		if out, known, err := c.exec(context.Background(), d, name, map[string]any{}); !known || err == nil || out != "" {
			t.Errorf("%s: %q %v %v", name, out, known, err)
		}
	}
	if _, known, _ := c.exec(context.Background(), d, "search", map[string]any{}); known {
		t.Error("allgemeine Werkzeuge gehören nicht zur Verbindungsseite")
	}
}
