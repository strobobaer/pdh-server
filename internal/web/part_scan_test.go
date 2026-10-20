package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScanNormalizing(t *testing.T) {
	if got := normCodes("6205-2RS", "6205 2rs", "ab", "", "SKF.6205/2RS"); strings.Join(got, "|") != "62052rs|skf62052rs" {
		t.Errorf("normCodes = %v", got)
	}
	if got := searchWords("Rillenkugellager 6205-2RS, 10 Stk. und Dichtung"); strings.Join(got, "|") != "rillenkugellager|6205-2rs|dichtung" {
		t.Errorf("searchWords = %v", got)
	}
	if got := normCompany("Würth Industrie Service GmbH & Co. KG"); got != "würth industrie service" {
		t.Errorf("normCompany = %q", got)
	}
	sup := []partnerOption{{Value: "a", Label: "Bosch Rexroth AG"}, {Value: "b", Label: "Würth Industrie Service GmbH & Co. KG"}, {Value: "c", Label: "Würth Elektronik"}}
	if id, _ := matchSupplier("WÜRTH Industrie Service GmbH & Co. KG, Bad Mergentheim", sup); id != "b" {
		t.Errorf("Lieferant = %q, erwartet b", id)
	}
	if id, _ := matchSupplier("Rexroth", sup); id != "a" {
		t.Errorf("Teilname = %q, erwartet a", id)
	}
	if id, _ := matchSupplier("Unbekannt GmbH", sup); id != "" {
		t.Errorf("unbekannter Lieferant zugeordnet: %q", id)
	}
}

// Schemas für structured outputs: jedes Objekt mit additionalProperties:false
// und allen Feldern als required.
func TestScanSchemasStrict(t *testing.T) {
	var check func(path string, s map[string]any)
	check = func(path string, s map[string]any) {
		if s["type"] == "object" {
			props, _ := s["properties"].(map[string]any)
			req, _ := s["required"].([]string)
			if s["additionalProperties"] != false || len(req) != len(props) {
				t.Errorf("%s: additionalProperties/required unvollständig", path)
			}
			for k, v := range props {
				check(path+"."+k, v.(map[string]any))
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			check(path+"[]", items)
		}
	}
	check("delivery", deliverySchema)
	check("part", partSchema)
	// Felder der Go-Typen müssen zum Schema passen
	for name, pair := range map[string]struct {
		schema map[string]any
		v      any
	}{"delivery": {deliverySchema, deliveryResult{Items: []deliveryItem{{}}}}, "part": {partSchema, partResult{}}} {
		raw, _ := json.Marshal(pair.v)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		for k := range pair.schema["properties"].(map[string]any) {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: Schemafeld %q fehlt im Go-Typ", name, k)
			}
		}
	}
}

func TestNewPartFromRecognition(t *testing.T) {
	np := newPartFromRecognition(partResult{Name: "Rillenkugellager", Manufacturer: "SKF", TypeDesignation: "6205-2RSH", EAN: "7316577",
		Specs: []partSpec{{"Innendurchmesser", "25 mm"}, {"", "x"}}})
	if np.Number != "6205-2RSH" || np.ManufacturerPart != "6205-2RSH" || np.Unit != "Stück" ||
		np.Description != "Hersteller: SKF\nInnendurchmesser: 25 mm\nEAN: 7316577" {
		t.Errorf("Vorbelegung: %+v", np)
	}
}

func TestScanPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "inventory_scan", InventoryScanPageData{VisionReady: true, Recent: []scanListRow{{ID: "s1", Kind: "delivery", Status: "done", NoteNo: "LS-4711"}}})
	checkScripts(t, "inventory_scan", out)
	for _, want := range []string{`capture="environment"`, `accept="image/*,application/pdf"`, "scSend('delivery')", "scSend('part')", `href="/inventory/scan/s1"`, "LS-4711"} {
		if !strings.Contains(out, want) {
			t.Errorf("Scan-Seite enthält %q nicht", want)
		}
	}

	d := InventoryScanDetailData{Scan: scanListRow{ID: "s1", Kind: "delivery", Status: "done"},
		Delivery:  &deliveryResult{SupplierName: "Würth", DeliveryNoteNo: "LS-4711"},
		Suppliers: []partnerOption{{Value: "sup", Label: "Würth"}}, SupplierID: "sup",
		Storages: []partnerOption{{Value: "st1", Label: "Halle 1 › Regal A"}},
		Lines: []deliveryLine{
			{Index: 0, Item: deliveryItem{Position: "1", SupplierArticleNo: "0890 100", Description: "Kabelbinder"}, Qty: "100", Action: "book", PartID: "p1", StorageID: "st1",
				Candidates: []partCandidate{{ID: "p1", PartNumber: "KB-100", Name: "Kabelbinder", Score: 100, Reason: "Artikelnr. dieses Lieferanten"}}},
			{Index: 1, Item: deliveryItem{Position: "2", Description: "Sensor M12"}, Qty: "2", Action: "new", NewNumber: "IFM-123", NewName: "Sensor M12"},
		}}
	out = renderPage(t, tmpl, "inventory_scan_detail", d)
	checkScripts(t, "inventory_scan_detail", out)
	for _, want := range []string{`action="/inventory/scan/s1/apply"`, `name="lines" value="2"`, `<option value="sup" selected>`, `name="act_0"`,
		`<option value="p1" selected>`, `value="IFM-123"`, "gefunden: Artikelnr. dieses Lieferanten", "Zugänge buchen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Lieferschein-Ergebnis enthält %q nicht", want)
		}
	}

	p := InventoryScanDetailData{Scan: scanListRow{ID: "s2", Kind: "part", Status: "done"},
		Part:           &partResult{Recognized: true, Name: "Rillenkugellager", Manufacturer: "SKF", Confidence: "high"},
		PartCandidates: []partCandidate{{ID: "p9", PartNumber: "6205", Name: "Kugellager", Reason: "Teilenummer"}},
		NewPart:        scanNewPart{Number: "6205-2RS", Name: "Rillenkugellager", Unit: "Stück"}}
	out = renderPage(t, tmpl, "inventory_scan_detail", p)
	for _, want := range []string{"Rillenkugellager", "sicher", `href="/inventory/p9"`, `action="/inventory/scan/s2/create"`, `value="6205-2RS"`, `name="use_photo"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Bauteil-Ergebnis enthält %q nicht", want)
		}
	}

	pending := renderPage(t, tmpl, "inventory_scan_detail", InventoryScanDetailData{Scan: scanListRow{ID: "s3", Kind: "part", Status: "pending"}})
	if !strings.Contains(pending, `id="sc-pending"`) {
		t.Error("Wartehinweis fehlt")
	}
}

func TestInfraResponsibleRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Presse 3"}, Types: infraTypes,
		EffectiveRespID: "u1", EffectiveResp: "Eva Maier", RespFromID: "h1", RespFromName: "Halle 1",
		Users: []UserOption{{ID: "u1", Name: "Eva Maier"}}}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	for _, want := range []string{"Verantwortlich", `href="/users/u1"`, `geerbt von <a href="/infrastructure/h1">Halle 1</a>`, `name="responsible_to"`,
		"von übergeordneter Anlage erben (Eva Maier)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Stammkarte enthält %q nicht", want)
		}
	}
	if !strings.Contains(out, `id="infra-edit-service-id"`) || !strings.Contains(out, `data-kind="supplier"`) {
		t.Error("Servicepartner nicht auf Lieferanten beschränkt")
	}
}

func TestCoreSettingsInfraResponsible(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "core_settings", CoreSettingsPageData{InfraRespDefault: true})
	if !strings.Contains(out, `action="/core/settings/infra-responsible"`) || !strings.Contains(out, `name="on" value="1" checked`) {
		t.Error("Schalter „Verantwortliche aus der Infrastruktur“ fehlt oder nicht angehakt")
	}
}
