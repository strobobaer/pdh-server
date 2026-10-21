package web

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func testKanbanLoop() *kanbanLoopView {
	k := &kanbanLoopView{PartID: "11111111-1111-7111-8111-111111111111", PartNumber: "M8-50", PartName: "Schraube M8×50", Unit: "Stück",
		Active: true, AutoEmpty: true, CardCount: 3, CardQty: 50, StorageID: "s1", StoragePath: "Halle 1 › Regal A",
		SupplierID: "22222222-2222-7222-8222-222222222222", SupplierName: "Würth", SupplierEmail: "bestellung@example.com",
		ContractID: "c1", ContractLabel: "RV-7 · Normteile", UnitPrice: 0.12, Stock: 60}
	k.setCards([]kanbanCardView{
		{ID: "a1", No: 1, Status: "full", StatusLabel: "voll", StatusClass: "b-green"},
		{ID: "a2", No: 2, Status: "empty", StatusLabel: "leer – zu bestellen", StatusClass: "b-red", Since: "21.10.2026 08:00"},
		{ID: "a3", No: 3, Status: "ordered", StatusLabel: "bestellt", StatusClass: "b-amber", OrderRef: "B-1", Expected: "20.10.2026", Late: true},
	})
	return k
}

func TestKanbanLoopCounts(t *testing.T) {
	k := testKanbanLoop()
	if k.Full != 1 || k.Empty != 1 || k.Ordered != 1 {
		t.Fatalf("Zählung falsch: %d/%d/%d", k.Full, k.Empty, k.Ordered)
	}
	if k.OrderQty() != "50" || k.Total() != "150" {
		t.Errorf("Bestellmenge %s, Gesamt %s", k.OrderQty(), k.Total())
	}
	g := &procurementGroup{SupplierEmail: k.SupplierEmail, Loops: []*kanbanLoopView{k}}
	mail := kanbanOrderMail(g)
	if !strings.HasPrefix(mail, "mailto:bestellung@example.com?subject=Bestellung&body=") || !strings.Contains(mail, "M8-50") {
		t.Errorf("Bestell-Mail: %s", mail)
	}
	if kanbanOrderMail(&procurementGroup{}) != "" {
		t.Error("ohne E-Mail-Adresse kein mailto")
	}
}

func TestProcurementPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	k := testKanbanLoop()
	g := &procurementGroup{SupplierID: k.SupplierID, SupplierName: k.SupplierName, Loops: []*kanbanLoopView{k}, Total: "6,00 €"}
	d := ProcurementData{Tab: "proposal", Loops: []*kanbanLoopView{k}, Groups: []*procurementGroup{g}, Ordered: []*kanbanLoopView{k},
		Contracts: []contractView{{ID: "c1", No: "RV-7", SupplierName: "Würth", StatusLabel: "aktiv", StatusClass: "b-green"}},
		CountFull: 1, CountEmpty: 1, CountOrdered: 1, CountLate: 1, CanOrder: true, CanContracts: true}
	out := renderPage(t, tmpl, "procurement", d)
	checkTabs(t, "procurement", out, "board", "proposal", "orders", "contracts")
	for _, want := range []string{`name="card" value="a2"`, `action="/procurement/cards/a3/receive"`, "überfällig", "RV-7", `action="/procurement/order"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Beschaffung enthält %q nicht", want)
		}
	}
	// ohne Bestellrecht kein Bestellformular
	d.CanOrder = false
	if out := renderPage(t, tmpl, "procurement", d); strings.Contains(out, "Als bestellt melden</button>") {
		t.Error("Bestellen ohne Recht sichtbar")
	}
}

func TestPartDetailKanbanTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := PartMaster{ID: "11111111-1111-7111-8111-111111111111", Name: "Schraube", PartNumber: "M8-50", Unit: "Stück"}
	// ohne Recht kein Reiter
	out := renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, Tab: "overview"})
	if strings.Contains(out, `data-tab="kanban"`) {
		t.Error("Kanban-Reiter ohne Recht sichtbar")
	}
	// einrichten, noch nicht aktiv
	out = renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, Tab: "kanban", CanKanbanView: true, CanKanban: true})
	checkTabs(t, "inventory_detail", out, "kanban")
	if !strings.Contains(out, "Kanban aktivieren") {
		t.Error("Aktivieren fehlt")
	}
	// aktiv mit Karten
	out = renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, Tab: "kanban", CanKanbanView: true, CanCardActions: true, Kanban: testKanbanLoop(), CanBook: true})
	for _, want := range []string{"/procurement/cards/a1/empty", "/procurement/cards/a2/receive", "/procurement/kanban/" + p.ID + "/print", "/inventory/" + p.ID + "/booking"} {
		if !strings.Contains(out, want) {
			t.Errorf("Kanban-Reiter enthält %q nicht", want)
		}
	}
}

func TestPartBookingPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := PartMaster{ID: "11111111-1111-7111-8111-111111111111", Name: "Lager 6204", PartNumber: "6204-2RS", Unit: "Stück", Description: "Rillenkugellager", ImageURL: "/uploads/x.jpg"}
	d := PartBookingData{Part: p, Actions: bookingActions, Action: "out", CanBook: true, CanCorrect: true,
		OtherReasons: bookingOtherReasons, CorrectReasons: bookingCorrectReasons,
		StorageOptions: []partnerOption{{Value: "s1", Label: "Halle 1"}}, DefaultStore: "s1",
		Stock:        []PartStockRow{{NodeID: "s1", Path: "Halle 1", Qty: "4"}},
		Reservations: []PartReservationRow{{ID: "r1", Kind: "infrastructure", RefID: "i1", RefLabel: "Anlage: Presse 3", QtyRaw: 1, Qty: "1"}},
		RefValue:     "ticket:33333333-3333-7333-8333-333333333333", RefLabel: "Ticket: Lager tauschen"}
	out := renderPage(t, tmpl, "inventory_booking", d)
	for _, want := range []string{`value="out" checked`, `value="reserve"`, `value="other"`, `value="correct"`, `value="in"`,
		`name="target" value="infra"`, `id="bk-infra"`, "Ticket: Lager tauschen", "Rillenkugellager", `value="s1" selected`,
		"pdhConsumeReservation(", `action="/inventory/` + p.ID + `/booking"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Buchungserfassung enthält %q nicht", want)
		}
	}
	// nur buchen, keine Korrekturen
	d.CanCorrect = false
	out = renderPage(t, tmpl, "inventory_booking", d)
	if strings.Contains(out, `value="correct"`) || strings.Contains(out, `name="action" value="other"`) {
		t.Error("Korrektur/Sonstiges ohne Recht wählbar")
	}
}

func TestContractAndKanbanCardRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c := contractView{ID: "c1", No: "RV-7", SupplierID: "s", SupplierName: "Würth", Active: true, StatusLabel: "aktiv", StatusClass: "b-green", TotalValue: "1.000,00 €", UsedPct: 40}
	out := renderPage(t, tmpl, "procurement_contract", ContractPageData{Contract: c, CanEdit: true,
		Items: []contractItemView{{ID: "i1", PartID: "p1", PartNumber: "M8", PartName: "Schraube", Unit: "Stück", Price: 0.12, ContractQty: 1000, CalledQty: 400, UsedPct: 40, Kanban: true}}})
	for _, want := range []string{`action="/procurement/contracts/c1/items"`, "/procurement/contracts/c1/items/i1/delete", "40 %"} {
		if !strings.Contains(out, want) {
			t.Errorf("Rahmenvertrag enthält %q nicht", want)
		}
	}
	out = renderPage(t, tmpl, "procurement_contract", ContractPageData{IsNew: true, CanEdit: true, Contract: contractView{Active: true}})
	if !strings.Contains(out, `action="/procurement/contracts/new"`) {
		t.Error("Neuanlage-Formular fehlt")
	}
	k := testKanbanLoop()
	out = renderPage(t, tmpl, "kanban_card", KanbanCardPageData{Loop: k, Card: &k.Cards[0], CanEmpty: true, CanReceive: true})
	if !strings.Contains(out, "/procurement/cards/a1/empty") || strings.Contains(out, "/procurement/cards/a1/receive") {
		t.Error("volle Karte: nur „leer melden“ erwartet")
	}
	out = renderPage(t, tmpl, "kanban_card", KanbanCardPageData{Loop: k, Card: &k.Cards[2], CanEmpty: true, CanReceive: true, CanCancel: true})
	if !strings.Contains(out, "/procurement/cards/a3/receive") || !strings.Contains(out, "/procurement/cards/a3/cancel") {
		t.Error("bestellte Karte: Wareneingang und Zurücknehmen erwartet")
	}
}

func TestKanbanPrintRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "kanban_print.gohtml")); err != nil {
		t.Fatal(err)
	}
	qr, err := qrSVG("https://pdh.example/k/a1")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cards := []struct {
		No  int
		QR  any
		URL string
	}{{1, qr, "https://pdh.example/k/a1"}}
	if err := c.ExecuteTemplate(&buf, "kanban-print-page", map[string]any{"Loop": testKanbanLoop(), "Cards": cards}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "Schraube M8×50") || !strings.Contains(out, "RV-7") {
		t.Error("Kanban-Karte unvollständig")
	}
}

func TestOwnQRRecognised(t *testing.T) {
	for in, want := range map[string]string{
		"https://pdh.example/inventory/11111111-1111-7111-8111-111111111111":               "inventory",
		"https://pdh.example/inventory/11111111-1111-7111-8111-111111111111/booking?loc=x": "inventory",
		"https://pdh.example/k/11111111-1111-7111-8111-111111111111":                       "k",
		"4006381333931": "",
	} {
		m := ownQRRe.FindStringSubmatch(in)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%s: %q, erwartet %q", in, got, want)
		}
	}
	if u := partLabelURL("https://pdh.example", labelItem{PartID: "p1", NodeID: "n1"}); u != "https://pdh.example/inventory/p1/booking?loc=n1" {
		t.Errorf("Etikett-URL %s", u)
	}
}

func TestScanNoticeKeepsMsgParams(t *testing.T) {
	if got := scanNotice("/inventory/scan?tab=part", "err", "Menge fehlt"); got != "/inventory/scan?tab=part&err=Menge+fehlt" {
		t.Errorf("scanNotice: %s", got)
	}
}
