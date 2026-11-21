package web

// Widget „Lagerbestand“ (list_stock): Seit Migration 027 steht der Lagerort in
// spare_part_stock/storage_nodes, die alte Spalte spare_parts.storage_location
// heißt storage_location_legacy. Das Widget fragte noch die alte Spalte ab und
// scheiterte auf jedem System (gesehen auf 10.10.0.202, 21.11.2026).

import (
	"context"
	"testing"
)

func TestListStockWidgetShowsLocation(t *testing.T) {
	e := newHistEnv(t)
	maker := e.user("Lager", "Prüfer")
	lager := e.row("storage_nodes", `INSERT INTO storage_nodes (name, type, created_by) VALUES ($2, 'lagerort', $1::uuid) RETURNING id::text`, maker.ID, "LW-Lager"+e.sfx)
	fach := e.row("storage_nodes", `INSERT INTO storage_nodes (parent_id, name, type, created_by) VALUES ($2::uuid, 'Fach 7', 'fach', $1::uuid) RETURNING id::text`, maker.ID, lager)
	// Weit unter dem Mindestbestand und kritisch: steht vorn in der Liste (höchstens 8 Einträge).
	part := e.row("spare_parts", `INSERT INTO spare_parts (part_number, name, stock_qty, min_qty, critical_qty, unit, created_by)
		VALUES ($2, 'LW-Teil', 2, 100000, 50000, 'Stk', $1::uuid) RETURNING id::text`, maker.ID, "LW"+e.sfx)
	e.exec(`INSERT INTO spare_part_stock (part_id, storage_node_id, qty) VALUES ($1::uuid, $2::uuid, 2)`, part, fach)
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM spare_part_stock WHERE part_id = $1::uuid`, part)
	})

	got, err := loadListStock(e.h, e.ctx, maker.ID, WidgetInstance{}, func(string) bool { return true })
	if err != nil {
		t.Fatalf("Widget Lagerbestand: %v", err)
	}
	items, _ := got.([]listItem)
	for _, it := range items {
		if it.URL != "/inventory/"+part {
			continue
		}
		if want := "LW" + e.sfx + " · LW-Lager" + e.sfx + " › Fach 7"; it.Sub != want {
			t.Errorf("Unterzeile %q, erwartet %q", it.Sub, want)
		}
		if it.BadgeClass != "b-red" {
			t.Errorf("kritischer Bestand: Farbe %q, erwartet b-red", it.BadgeClass)
		}
		return
	}
	t.Fatalf("Teil fehlt in der Liste (%d Einträge)", len(items))
}
