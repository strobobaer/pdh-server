#!/usr/bin/env python3
"""
Patcht internal/modules/inventory/inventory.go: verknüpft Lagerbuchungen
zusätzlich optional mit einem Wartungsauftrag (maintenance_task_id), analog
zu fault_id und ticket_id.

Aufruf (NACH apply_inventory_ticket_link_patch.py ausfuehren):
    python3 apply_inventory_maintenance_link_patch.py internal/modules/inventory/inventory.go
"""
import sys

REPLACEMENTS = [
    (
        "StockMovement: MaintenanceTaskID ergaenzen",
        '''	FaultID       string       `json:"fault_id,omitempty"`
	TicketID      string       `json:"ticket_id,omitempty"`
	CreatedBy     string       `json:"created_by"`''',
        '''	FaultID            string  `json:"fault_id,omitempty"`
	TicketID           string  `json:"ticket_id,omitempty"`
	MaintenanceTaskID  string  `json:"maintenance_task_id,omitempty"`
	CreatedBy          string  `json:"created_by"`''',
    ),
    (
        "BookMovementInput: MaintenanceTaskID ergaenzen",
        '''	FaultID       string       `json:"fault_id,omitempty"`
	TicketID      string       `json:"ticket_id,omitempty"`
}''',
        '''	FaultID           string `json:"fault_id,omitempty"`
	TicketID          string `json:"ticket_id,omitempty"`
	MaintenanceTaskID string `json:"maintenance_task_id,omitempty"`
}''',
    ),
    (
        "BookMovement(): maintenance_task_id mitschreiben (NULL wenn leer)",
        '''	var faultIDArg, ticketIDArg interface{}
	if m.FaultID != "" {
		faultIDArg = m.FaultID
	}
	if m.TicketID != "" {
		ticketIDArg = m.TicketID
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO stock_movements (id, part_id, type, qty, qty_before, qty_after, storage_node_id, reference, notes, created_by, fault_id, ticket_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at`,
		m.PartID, m.Type, m.Qty, m.QtyBefore, m.QtyAfter, m.StorageNodeID, m.Reference, m.Notes, m.CreatedBy, faultIDArg, ticketIDArg,
	).Scan(&m.ID, &m.CreatedAt); err != nil {
		return err
	}''',
        '''	var faultIDArg, ticketIDArg, maintTaskIDArg interface{}
	if m.FaultID != "" {
		faultIDArg = m.FaultID
	}
	if m.TicketID != "" {
		ticketIDArg = m.TicketID
	}
	if m.MaintenanceTaskID != "" {
		maintTaskIDArg = m.MaintenanceTaskID
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO stock_movements (id, part_id, type, qty, qty_before, qty_after, storage_node_id, reference, notes, created_by, fault_id, ticket_id, maintenance_task_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at`,
		m.PartID, m.Type, m.Qty, m.QtyBefore, m.QtyAfter, m.StorageNodeID, m.Reference, m.Notes, m.CreatedBy, faultIDArg, ticketIDArg, maintTaskIDArg,
	).Scan(&m.ID, &m.CreatedAt); err != nil {
		return err
	}''',
    ),
    (
        "Service.Book(): MaintenanceTaskID weitergeben",
        '''	m := &StockMovement{
		PartID: in.PartID, Type: in.Type, Qty: in.Qty, StorageNodeID: in.StorageNodeID,
		Reference: in.Reference, Notes: in.Notes, FaultID: in.FaultID, TicketID: in.TicketID, CreatedBy: userID,
	}''',
        '''	m := &StockMovement{
		PartID: in.PartID, Type: in.Type, Qty: in.Qty, StorageNodeID: in.StorageNodeID,
		Reference: in.Reference, Notes: in.Notes, FaultID: in.FaultID, TicketID: in.TicketID,
		MaintenanceTaskID: in.MaintenanceTaskID, CreatedBy: userID,
	}''',
    ),
]


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_inventory_maintenance_link_patch.py internal/modules/inventory/inventory.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    changed = content
    for label, old, new in REPLACEMENTS:
        count = changed.count(old)
        if count == 0:
            print(f"FEHLER: Block '{label}' wurde nicht gefunden. Nichts geändert.")
            sys.exit(1)
        if count > 1:
            print(f"FEHLER: Block '{label}' wurde {count}x gefunden (erwartet 1x). Nichts geändert.")
            sys.exit(1)
        changed = changed.replace(old, new, 1)

    with open(path, "w", encoding="utf-8") as f:
        f.write(changed)
    print(f"OK: {path} gepatcht ({len(REPLACEMENTS)} Stellen ersetzt).")


if __name__ == "__main__":
    main()
