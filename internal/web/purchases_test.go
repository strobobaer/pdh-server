package web

import (
	"strings"
	"testing"
)

// Abgleich AB ↔ Bestellung und Lieferung ↔ AB (purchases.go).
func TestPurchaseCheck(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	p := &purchaseView{Status: "partial", WantedDate: "2026-11-05", ConfirmedDelivery: "2026-11-12", Items: []purchaseItemView{
		{QtyOrdered: f(10), PriceOrdered: f(5), QtyConfirmed: f(8), PriceConfirmed: f(5), Delivered: 9},
		{QtyOrdered: f(2), PriceOrdered: f(85), QtyConfirmed: f(2), PriceConfirmed: f(90), Delivered: 1},
		{QtyOrdered: f(3), Delivered: 0},
	}}
	p.check()
	if !strings.Contains(p.Items[0].ABWarn, "Menge 8 statt 10") || !strings.Contains(p.Items[0].DeliveryWarn, "Überlieferung") {
		t.Errorf("Position 1: %q / %q", p.Items[0].ABWarn, p.Items[0].DeliveryWarn)
	}
	if !strings.Contains(p.Items[1].ABWarn, "Preis") || p.Items[1].DeliveryWarn != "offen: 1" {
		t.Errorf("Position 2: %q / %q", p.Items[1].ABWarn, p.Items[1].DeliveryWarn)
	}
	if p.Items[2].ABWarn != "" || p.Items[2].Open() != 3 {
		t.Errorf("Position 3 ohne AB: %q, offen %v", p.Items[2].ABWarn, p.Items[2].Open())
	}
	if len(p.Warnings) < 3 {
		t.Errorf("Hinweise: %v", p.Warnings)
	}
	var cur string
	for _, s := range p.Steps {
		if s.Current {
			cur = s.Label
		}
	}
	if cur != "Teillieferung" {
		t.Errorf("aktueller Schritt %q", cur)
	}
}
