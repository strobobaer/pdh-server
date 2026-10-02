package inventory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ersatzteil-Reservierung
//
// Ein fuer einen Vorgang (Stoerung, Ticket, Aufgabe, Wartung) vorgemerktes
// Teil wird sofort vom Lagerort abgebucht (Bewegung 'reserve'). Der Bestand
// zeigt damit nur noch die freie Menge, die reservierte steht in Klammern
// daneben. Beim Abschluss des Vorgangs wird die Reservierung zum Verbrauch
// ('out'); nicht benoetigte Mengen werden zurueckgebucht ('unreserve').
// Die Rueckbuchung selbst erledigt der Datenbank-Trigger
// pdh_reservation_return (Migration 090), damit sie auch beim Loeschen des
// Vorgangs greift.

type reservationKind struct {
	table  string // Vormerk-Tabelle
	refCol string // Spalte des Vorgangs in der Vormerk-Tabelle
	label  string
	url    string
	src    string // Tabelle des Vorgangs
}

var reservationKinds = map[string]reservationKind{
	"fault":            {"fault_pending_parts", "fault_id", "Störung", "/faults/", "faults"},
	"ticket":           {"ticket_pending_parts", "ticket_id", "Ticket", "/tickets/", "tickets"},
	"task":             {"task_pending_parts", "task_id", "Aufgabe", "/tasks/", "tasks"},
	"maintenance_task": {"maintenance_task_pending_parts", "task_id", "Wartung", "/maintenance/tasks/", "maintenance_tasks"},
}

func kindOf(kind string) (reservationKind, error) {
	k, ok := reservationKinds[kind]
	if !ok {
		return k, fmt.Errorf("unbekannte vorgangsart %q", kind)
	}
	return k, nil
}

// Reservation: eine aktive Reservierung (fuer die Lager-Detailseite).
type Reservation struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	KindLabel     string    `json:"kind_label"`
	RefID         string    `json:"ref_id"`
	RefTitle      string    `json:"ref_title"`
	RefURL        string    `json:"ref_url"`
	PartID        string    `json:"part_id"`
	StorageNodeID string    `json:"storage_node_id"`
	StorageName   string    `json:"storage_name"`
	Qty           float64   `json:"qty"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

// Reserve bucht qty vom Lagerort ab und legt die Reservierung beim Vorgang
// an. Liefert die ID des Vormerk-Eintrags.
func (s *Service) Reserve(ctx context.Context, kind, refID, partID, storageNodeID string, qty float64, userID string) (string, error) {
	k, err := kindOf(kind)
	if err != nil {
		return "", err
	}
	if partID == "" || storageNodeID == "" {
		return "", errors.New("ersatzteil und lagerort sind pflicht")
	}
	if qty <= 0 {
		return "", errors.New("menge muss größer als 0 sein")
	}
	locked, err := s.repo.IsLocationLocked(ctx, storageNodeID)
	if err != nil {
		return "", err
	}
	if locked {
		return "", errors.New("dieser lagerort ist aktuell durch eine laufende inventur gesperrt")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var free float64
	err = tx.QueryRow(ctx, `SELECT qty::float8 FROM spare_part_stock WHERE part_id=$1 AND storage_node_id=$2 FOR UPDATE`,
		partID, storageNodeID).Scan(&free)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if free < qty {
		return "", fmt.Errorf("nicht genug bestand am lagerort (frei: %s)", fmtQty(free))
	}

	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO `+k.table+` (id, `+k.refCol+`, part_id, storage_node_id, qty, created_by, reserved)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, true) RETURNING id`,
		refID, partID, storageNodeID, qty, userID).Scan(&id); err != nil {
		return "", err
	}
	m := &StockMovement{PartID: partID, Type: MovementReserve, Qty: qty, StorageNodeID: storageNodeID,
		Reference: "Reservierung " + k.label + " " + refID, CreatedBy: userID, ReservationID: id}
	switch kind {
	case "fault":
		m.FaultID = refID
	case "ticket":
		m.TicketID = refID
	case "task":
		m.TaskID = refID
	case "maintenance_task":
		m.MaintenanceTaskID = refID
	}
	if err := bookTx(ctx, tx, m); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if eventBus != nil {
		eventBus.Publish("inventory.booked", map[string]interface{}{
			"part_id": m.PartID, "type": string(m.Type), "qty": m.Qty,
			"qty_before": m.QtyBefore, "qty_after": m.QtyAfter,
			"storage_node_id": m.StorageNodeID, "created_by": m.CreatedBy,
		})
	}
	return id, nil
}

// ReturnReservation bucht qty einer Reservierung an den Lagerort zurueck
// (qty <= 0 oder >= reservierte Menge: alles, der Eintrag entfaellt). Bei
// einer alten, noch nicht abgebuchten Vormerkung wird nur die Menge
// verringert bzw. der Eintrag entfernt. refID muss zum Eintrag passen.
func (s *Service) ReturnReservation(ctx context.Context, kind, refID, id string, qty float64, userID string) (float64, error) {
	k, err := kindOf(kind)
	if err != nil {
		return 0, err
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if userID != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('pdh.user_id', $1, true)`, userID); err != nil {
			return 0, err
		}
	}
	var cur float64
	if err := tx.QueryRow(ctx, `SELECT qty::float8 FROM `+k.table+` WHERE id=$1::uuid AND `+k.refCol+`=$2::uuid FOR UPDATE`, id, refID).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errors.New("reservierung nicht gefunden")
		}
		return 0, err
	}
	back := qty
	if qty <= 0 || qty >= cur {
		back = cur
		_, err = tx.Exec(ctx, `DELETE FROM `+k.table+` WHERE id=$1`, id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE `+k.table+` SET qty = qty - $2 WHERE id=$1`, id, qty)
	}
	if err != nil {
		return 0, err
	}
	return back, tx.Commit(ctx)
}

// ConsumeReservations: beim Abschluss des Vorgangs werden alle abgebuchten
// Reservierungen zum Verbrauch (Bewegung 'reserve' -> 'out', Rueckbuchungen
// 'unreserve' -> 'in'). Der Bestand aendert sich dabei nicht mehr.
// Alte, noch nicht abgebuchte Vormerkungen bleiben stehen und werden vom
// Modul wie bisher gebucht.
func (s *Service) ConsumeReservations(ctx context.Context, kind, refID string) error {
	k, err := kindOf(kind)
	if err != nil {
		return err
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	sub := `SELECT id FROM ` + k.table + ` WHERE ` + k.refCol + `=$1::uuid AND reserved`
	if _, err := tx.Exec(ctx, `UPDATE stock_movements SET type = CASE type WHEN 'reserve' THEN 'out'::stock_movement_type ELSE 'in'::stock_movement_type END,
			notes = CASE WHEN COALESCE(notes,'') = '' THEN 'aus Reservierung' ELSE notes || ' · aus Reservierung' END
		WHERE reservation_id IN (`+sub+`) AND type IN ('reserve', 'unreserve')`, refID); err != nil {
		return err
	}
	// reserved=false vor dem Loeschen: der Trigger bucht dann nichts zurueck
	if _, err := tx.Exec(ctx, `UPDATE `+k.table+` SET reserved = false WHERE `+k.refCol+`=$1::uuid AND reserved`, refID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+k.table+` t WHERE t.`+k.refCol+`=$1::uuid AND NOT t.reserved
			AND EXISTS (SELECT 1 FROM stock_movements sm WHERE sm.reservation_id = t.id)`, refID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Reservations: aktive Reservierungen eines Teils (partID leer: alle).
func (s *Service) Reservations(ctx context.Context, partID string) ([]*Reservation, error) {
	var parts []string
	for kind, k := range reservationKinds {
		parts = append(parts, `SELECT rv.id::text, '`+kind+`', rv.ref_id::text, COALESCE(x.title,''), rv.part_id::text,
			rv.storage_node_id::text, COALESCE(sn.name,''), rv.qty::float8, COALESCE(u.first_name || ' ' || u.last_name, ''), rv.created_at
			FROM spare_part_reservations rv
			JOIN `+k.src+` x ON x.id = rv.ref_id
			LEFT JOIN storage_nodes sn ON sn.id = rv.storage_node_id
			LEFT JOIN users u ON u.id = rv.created_by
			WHERE rv.kind = '`+kind+`' AND ($1 = '' OR rv.part_id::text = $1)`)
	}
	rows, err := s.repo.db.Query(ctx, `SELECT * FROM (`+strings.Join(parts, " UNION ALL ")+`) q ORDER BY 10`, partID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Reservation
	for rows.Next() {
		r := &Reservation{}
		if err := rows.Scan(&r.ID, &r.Kind, &r.RefID, &r.RefTitle, &r.PartID, &r.StorageNodeID, &r.StorageName,
			&r.Qty, &r.CreatedByName, &r.CreatedAt); err != nil {
			return nil, err
		}
		k := reservationKinds[r.Kind]
		r.KindLabel, r.RefURL = k.label, k.url+r.RefID
		out = append(out, r)
	}
	return out, rows.Err()
}
