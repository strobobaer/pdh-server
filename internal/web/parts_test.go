package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestPartImageRoundTrip(t *testing.T) {
	desc := "Rillenkugellager\nPDH_IMAGE_URL: https://x/y.png\n"
	clean, img := splitPartImage(desc)
	if clean != "Rillenkugellager" || img != "https://x/y.png" {
		t.Fatalf("split: %q %q", clean, img)
	}
	if joined := joinPartImage(clean, img); joined != "Rillenkugellager\nPDH_IMAGE_URL: https://x/y.png" {
		t.Errorf("join: %q", joined)
	}
	if c, i := splitPartImage(joinPartImage("", "https://a")); c != "" || i != "https://a" {
		t.Errorf("nur Bild: %q %q", c, i)
	}
	if joinPartImage("Text", "") != "Text" {
		t.Error("ohne Bild")
	}
}

func TestPartStatus(t *testing.T) {
	for _, c := range []struct {
		stock, min, crit float64
		want             string
	}{{0, 5, 1, "empty"}, {1, 5, 1, "critical"}, {3, 5, 1, "low"}, {5, 5, 1, "low"}, {6, 5, 1, "ok"}} {
		if k, _, _ := partStatus(c.stock, c.min, c.crit); k != c.want {
			t.Errorf("partStatus(%v,%v,%v) = %s, want %s", c.stock, c.min, c.crit, k, c.want)
		}
	}
}

func TestNormalizeFieldValue(t *testing.T) {
	num := &FieldView{Name: "Leistung", Type: "number", Unit: "kW"}
	if v, err := normalizeFieldValue(num, "1.234,5"); err != nil || v != "1234.5" {
		t.Errorf("number: %q %v", v, err)
	}
	num.Value = "1234.5"
	if d := formatFieldDisplay(num); d != "1234,5 kW" {
		t.Errorf("display: %q", d)
	}
	if _, err := normalizeFieldValue(num, "abc"); err == nil {
		t.Error("ungültige Zahl akzeptiert")
	}
	sel := &FieldView{Name: "Schutzart", Type: "select", Options: []FieldOptionView{{Value: "IP54"}, {Value: "IP65"}}}
	if _, err := normalizeFieldValue(sel, "IP99"); err == nil {
		t.Error("unbekannter Auswahlwert akzeptiert")
	}
	if v, _ := normalizeFieldValue(sel, "IP65"); v != "IP65" {
		t.Error("Auswahl")
	}
	req := &FieldView{Name: "Pflicht", Type: "text", Required: true}
	if _, err := normalizeFieldValue(req, "  "); err == nil {
		t.Error("Pflichtfeld leer akzeptiert")
	}
	cb := &FieldView{Name: "Ex-geschützt", Type: "checkbox", Required: true}
	if v, err := normalizeFieldValue(cb, ""); err != nil || v != "" {
		t.Error("Checkbox leer = nein, auch bei Pflicht")
	}
	date := &FieldView{Name: "Prüfung", Type: "date", Value: "2026-09-29"}
	if formatFieldDisplay(date) != "29.09.2026" {
		t.Error("Datumsanzeige")
	}
	if _, err := normalizeFieldValue(&FieldView{Name: "Doku", Type: "url"}, "javascript:alert(1)"); err == nil {
		t.Error("javascript-URL akzeptiert")
	}
}

func TestFieldModulesWhitelist(t *testing.T) {
	for _, key := range []string{"part", "ticket", "fault", "task", "project", "maintenance_task", "infrastructure", "storage", "user"} {
		if _, ok := fieldModuleByKey(key); !ok {
			t.Errorf("Modul %s fehlt", key)
		}
	}
	if _, ok := fieldModuleByKey("users; DROP TABLE x"); ok {
		t.Error("unbekanntes Modul akzeptiert")
	}
	if withTab("/inventory/1", "stock") != "/inventory/1?tab=stock" || withTab("/inventory/1?err=x", "stock") != "/inventory/1?err=x&tab=stock" {
		t.Error("withTab")
	}
}

func TestPartPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	list := renderPage(t, tmpl, "inventory", InventoryListData{
		Total: 2, CanEdit: true, Category: "Lager",
		Tabs: []partCategoryTab{{Key: "Lager", Label: "Lager", Count: 1}, {Key: partNoCategory, Label: "Ohne Kategorie", Count: 1}},
		Rows: []PartListRow{{ID: "s1", Name: "Kugellager", PartNumber: "4711", SupplierID: "p1", Supplier: "ACME", StatusLabel: "OK"}},
	})
	for _, want := range []string{"/inventory/s1", "Ohne Kategorie", `class="active">Lager`, "openBookingModal()", "openStocktakeModal"} {
		if !strings.Contains(list, want) {
			t.Errorf("Liste enthält %q nicht", want)
		}
	}
	// Reservierte Menge in Klammern neben dem Bestand
	rl := renderPage(t, tmpl, "inventory", InventoryListData{Rows: []PartListRow{{ID: "r1", Name: "Lager 6204", Stock: "6", MinQty: "2", Unit: "Stk", Reserved: "4"}}})
	if !strings.Contains(rl, `<b>6</b> <span class="res-q"`) || !strings.Contains(rl, ">(4)</span>") {
		t.Error("Liste zeigt die reservierte Menge nicht in Klammern")
	}
	rp := PartMaster{ID: "s2", Name: "Lager", Unit: "Stk", StatusKey: "ok", StatusClass: "b-green", StatusLabel: "OK", StockQty: 6, ReservedQty: 4}
	rd := renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: rp, Tab: "stock",
		Stock:        []PartStockRow{{NodeID: "n1", Path: "Regal R", Qty: "6", Reserved: "4"}},
		Reservations: []PartReservationRow{{ID: "pp1", Kind: "fault", RefID: "f1", RefLabel: "Störung: Lagerschaden", RefURL: "/faults/f1", Path: "Regal R", Qty: "4", QtyRaw: 4}},
	})
	for _, want := range []string{"6 Stk (4 reserviert)", ">(4)</span>", "Störung: Lagerschaden", "pdhReturnReservation('fault','f1','pp1', 4 ,pdhPartReload)", "function pdhReservedTag"} {
		if !strings.Contains(rd, want) {
			t.Errorf("Detail (Reservierung) enthält %q nicht", want)
		}
	}
	// ohne Hauptlieferant / Hersteller darf nichts an nil scheitern
	p := PartMaster{ID: "s1", Name: "Kugellager", PartNumber: "4711", Unit: "Stück", StatusKey: "ok", StatusClass: "b-green", StatusLabel: "OK"}
	renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, CanEdit: true, Tab: "overview"})
	main := PartSupplierView{PartnerID: "p1", Name: "ACME", SupplierPartNo: "A-1", Price: "12,00 €", PriceRaw: "12", LeadDays: "5", Preferred: true}
	detail := renderPage(t, tmpl, "inventory_detail", PartDetailData{
		Part: p, CanEdit: true, CanPartners: true, CanPurchasing: true,
		MainSupplier: &main, MainSupplierID: "p1", Suppliers: []PartSupplierView{main},
		MainSupplierContact: &partnerContact{ID: "p1", Name: "ACME", Phone: "0800"},
		Manufacturer:        &partnerContact{ID: "m1", Name: "SKF"},
		Stock:               []PartStockRow{{NodeID: "n1", Path: "Halle 1 › Regal A", Qty: "4"}},
	})
	for _, want := range []string{"Hauptlieferant", "SKF", "A-1", `name="main_supplier_id"`, `data-selected="p1"`, "Halle 1 › Regal A", `hx-get="/records/fields/part/s1"`, `data-tab="fields"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("Detail enthält %q nicht", want)
		}
	}

	renderPage(t, tmpl, "fieldsets_admin", FieldSetsAdminData{
		Modules: fieldModules, Module: fieldModules[0], TypeOrder: fieldTypeOrder, TypeLabels: fieldTypeLabels,
		Sets: []*FieldSetView{{ID: "fs1", Name: "Motoren", Active: true, AutoMatch: "Motoren",
			Fields: []*FieldView{{ID: "f1", Name: "Leistung", Type: "number", Unit: "kW", Active: true},
				{ID: "f2", Name: "Schutzart", Type: "select", Active: true, Options: []FieldOptionView{{ID: "o1", Value: "IP54"}}}}}},
	})

	sets := []*FieldSetView{{ID: "fs1", Name: "Motoren", Fields: []*FieldView{
		{ID: "f1", Name: "Leistung", Type: "number", Unit: "kW", Value: "5.5", Display: "5,5 kW"},
		{ID: "f2", Name: "Ex", Type: "checkbox", Value: "1", Display: "Ja"},
		{ID: "f3", Name: "Schutzart", Type: "select", Value: "IP54", Options: []FieldOptionView{{Value: "IP54"}, {Value: "IP65"}}}}}}
	for _, edit := range []bool{false, true} {
		var buf bytes.Buffer
		c, _ := tmpl.Clone()
		if err := c.ExecuteTemplate(&buf, "record-fields", RecordFieldsData{Module: "part", ID: "s1", Sets: sets, AllSets: sets, Edit: edit, CanEdit: true}); err != nil {
			t.Fatalf("record-fields edit=%v: %v", edit, err)
		}
		out := buf.String()
		if !edit && !strings.Contains(out, "5,5 kW") {
			t.Error("Ansicht ohne formatierten Wert")
		}
		if edit && (!strings.Contains(out, `name="f_f1"`) || !strings.Contains(out, `value="IP54" selected`) || !strings.Contains(out, `name="present_f2"`)) {
			t.Error("Bearbeitungsformular unvollständig")
		}
	}
}
