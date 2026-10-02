//go:build integration

// Integrationstest Ersatzteil-Reservierung gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Reservation ./internal/modules/faults/
package faults

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/modules/inventory"
)

func TestReservationIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(q string, args ...interface{}) string {
		t.Helper()
		var v string
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	num := func(q string, args ...interface{}) float64 {
		t.Helper()
		var v float64
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")

	uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Rita', 'Res', '', '') RETURNING id::text`, "res"+sfx)
	node := id(`INSERT INTO storage_nodes (name, type, created_by) VALUES ('Regal R'||$1::text, 'regal', $2::uuid) RETURNING id::text`, sfx, uid)
	part := id(`INSERT INTO spare_parts (part_number, name, unit, created_by) VALUES ('RES-'||$1::text, 'Lager 6204', 'Stk', $2::uuid) RETURNING id::text`, sfx, uid)
	newFault := func(title string) string {
		return id(`INSERT INTO faults (title, description, severity, status, created_by) VALUES ($1, '', 'medium', 'detected', $2::uuid) RETURNING id::text`, title+sfx, uid)
	}

	inv := inventory.NewService(inventory.NewRepository(pool))
	SetInventoryService(inv)
	defer SetInventoryService(nil)
	svc := NewService(NewRepository(pool), nil)

	_, err = inv.Book(ctx, &inventory.BookMovementInput{PartID: part, Type: inventory.MovementIn, Qty: 10, StorageNodeID: node}, uid)
	must(err)
	stock := func() float64 {
		return num(`SELECT qty::float8 FROM spare_part_stock WHERE part_id=$1::uuid AND storage_node_id=$2::uuid`, part, node)
	}
	reserved := func() float64 {
		p, err := inv.GetByID(ctx, part)
		must(err)
		return p.ReservedQty
	}

	// 1) Reservieren: sofort abgebucht, reservierte Menge sichtbar
	f1 := newFault("Lagerschaden ")
	pp, err := svc.AddPendingPart(ctx, f1, &AddPendingPartInput{PartID: part, StorageNodeID: node, Qty: 4}, uid)
	must(err)
	if !pp.Reserved {
		t.Fatal("Vormerkung ist keine Reservierung")
	}
	if s, r := stock(), reserved(); s != 6 || r != 4 {
		t.Fatalf("nach Reservierung: Bestand %v (6), reserviert %v (4)", s, r)
	}
	if n := num(`SELECT COUNT(*)::float8 FROM stock_movements WHERE reservation_id=$1::uuid AND type='reserve' AND fault_id=$2::uuid`, pp.ID, f1); n != 1 {
		t.Fatalf("reserve-Bewegungen: %v", n)
	}
	locs, err := inv.GetStockLocations(ctx, part)
	must(err)
	if len(locs) != 1 || locs[0].ReservedQty != 4 {
		t.Fatalf("Lagerplatz-Reservierung falsch: %+v", locs)
	}

	// 2) Mehr als frei verfuegbar: abgelehnt, nichts gebucht
	if _, err := svc.AddPendingPart(ctx, f1, &AddPendingPartInput{PartID: part, StorageNodeID: node, Qty: 20}, uid); err == nil || !strings.Contains(err.Error(), "nicht genug") {
		t.Fatalf("Überreservierung nicht abgelehnt: %v", err)
	}
	if stock() != 6 {
		t.Fatal("Bestand nach abgelehnter Reservierung verändert")
	}

	// 3) Teilweise zurueckbuchen
	back, err := inv.ReturnReservation(ctx, "fault", f1, pp.ID, 1, uid)
	must(err)
	if back != 1 || stock() != 7 || reserved() != 3 {
		t.Fatalf("Teil-Rückbuchung: zurück %v, Bestand %v (7), reserviert %v (3)", back, stock(), reserved())
	}
	if _, err := inv.ReturnReservation(ctx, "fault", newFault("Fremd "), pp.ID, 1, uid); err == nil {
		t.Fatal("Rückbuchung über fremden Vorgang möglich")
	}

	// 4) Entfernen einer Reservierung bucht alles zurueck
	pp2, err := svc.AddPendingPart(ctx, f1, &AddPendingPartInput{PartID: part, StorageNodeID: node, Qty: 2}, uid)
	must(err)
	if stock() != 5 {
		t.Fatalf("zweite Reservierung: Bestand %v (5)", stock())
	}
	must(svc.DeletePendingPart(ctx, pp2.ID))
	if stock() != 7 || reserved() != 3 {
		t.Fatalf("nach Entfernen: Bestand %v (7), reserviert %v (3)", stock(), reserved())
	}

	// 5) Abschluss: Reservierung wird Verbrauch, kein doppeltes Abbuchen
	_, err = svc.AddAction(ctx, f1, "Lager getauscht", uid)
	must(err)
	must(svc.Resolve(ctx, f1, "getauscht", "Verschleiß", uid, false))
	if stock() != 7 || reserved() != 0 {
		t.Fatalf("nach Abschluss: Bestand %v (7), reserviert %v (0)", stock(), reserved())
	}
	if n := num(`SELECT COUNT(*)::float8 FROM fault_pending_parts WHERE fault_id=$1::uuid`, f1); n != 0 {
		t.Fatalf("Reservierungen nach Abschluss übrig: %v", n)
	}
	if net := num(`SELECT COALESCE(SUM(CASE WHEN type='out' THEN qty ELSE -qty END),0)::float8 FROM stock_movements
		WHERE fault_id=$1::uuid AND type IN ('out','in')`, f1); net != 3 {
		t.Fatalf("Verbrauch der Störung: %v (3)", net)
	}
	if n := num(`SELECT COUNT(*)::float8 FROM stock_movements WHERE reservation_id=$1::uuid AND type IN ('reserve','unreserve')`, pp.ID); n != 0 {
		t.Fatalf("offene Reservierungsbewegungen nach Abschluss: %v", n)
	}

	// 6) Vorgang geloescht: Reservierung geht automatisch zurueck
	f2 := newFault("Gelöscht ")
	_, err = svc.AddPendingPart(ctx, f2, &AddPendingPartInput{PartID: part, StorageNodeID: node, Qty: 2}, uid)
	must(err)
	if stock() != 5 {
		t.Fatalf("Reservierung f2: Bestand %v (5)", stock())
	}
	_, err = pool.Exec(ctx, `DELETE FROM faults WHERE id=$1::uuid`, f2)
	must(err)
	if stock() != 7 || reserved() != 0 {
		t.Fatalf("nach Löschen des Vorgangs: Bestand %v (7), reserviert %v (0)", stock(), reserved())
	}
	if n := num(`SELECT COUNT(*)::float8 FROM stock_movements WHERE part_id=$1::uuid AND type='unreserve' AND fault_id IS NULL AND notes='Vorgang gelöscht'`, part); n != 1 {
		t.Fatalf("Rückbuchung bei gelöschtem Vorgang: %v", n)
	}
	if v := num(`SELECT stock_qty::float8 FROM spare_parts WHERE id=$1::uuid`, part); v != 7 {
		t.Fatalf("Gesamtbestand-Cache: %v (7)", v)
	}

	// 7) Alte Vormerkung (nicht abgebucht) wird beim Abschluss wie bisher gebucht
	f3 := newFault("Alt ")
	_, err = pool.Exec(ctx, `INSERT INTO fault_pending_parts (fault_id, part_id, storage_node_id, qty, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, 1, $4::uuid)`, f3, part, node, uid)
	must(err)
	_, err = svc.AddAction(ctx, f3, "erledigt", uid)
	must(err)
	must(svc.Resolve(ctx, f3, "ok", "", uid, false))
	if stock() != 6 {
		t.Fatalf("alte Vormerkung: Bestand %v (6)", stock())
	}

	// 7b) Ohne Materialbuchung geschlossen (Verwerfen/Archivieren): Reservierung geht zurueck
	f4 := newFault("Verworfen ")
	_, err = svc.AddPendingPart(ctx, f4, &AddPendingPartInput{PartID: part, StorageNodeID: node, Qty: 3}, uid)
	must(err)
	if stock() != 3 {
		t.Fatalf("Reservierung f4: Bestand %v (3)", stock())
	}
	must(svc.QuickResolve(ctx, f4, "Verworfen: doppelt", "", uid))
	if stock() != 6 || reserved() != 0 {
		t.Fatalf("nach Verwerfen: Bestand %v (6), reserviert %v (0)", stock(), reserved())
	}

	// 8) Reservierungsarten nicht manuell buchbar
	if _, err := inv.Book(ctx, &inventory.BookMovementInput{PartID: part, Type: inventory.MovementReserve, Qty: 1, StorageNodeID: node}, uid); err == nil {
		t.Fatal("manuelle Reservierungsbuchung möglich")
	}
	res, err := inv.Reservations(ctx, part)
	must(err)
	if len(res) != 0 {
		t.Fatalf("aktive Reservierungen: %d", len(res))
	}
}
