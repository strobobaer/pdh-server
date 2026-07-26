package tasks

import (
	"context"
	"fmt"
	"time"

	"pdh/internal/core/synclink"
	"pdh/internal/modules/inventory"
)

// ── Maßnahmen-Verlauf ────────────────────────────────────────

type TaskAction struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	Description   string    `json:"description"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (r *Repository) AddAction(ctx context.Context, taskID, description, userID string) (*TaskAction, error) {
	a := &TaskAction{TaskID: taskID, Description: description, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO task_actions (id, task_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, created_at`,
		taskID, description, userID,
	).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (r *Repository) GetActions(ctx context.Context, taskID string) ([]*TaskAction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ta.id, ta.task_id, ta.description, ta.created_by, u.first_name || ' ' || u.last_name, ta.created_at
		FROM task_actions ta
		JOIN users u ON ta.created_by = u.id
		WHERE ta.task_id = $1
		ORDER BY ta.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskAction
	for rows.Next() {
		a := &TaskAction{}
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Description, &a.CreatedBy, &a.CreatedByName, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) CountActions(ctx context.Context, taskID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM task_actions WHERE task_id=$1`, taskID).Scan(&n)
	return n, err
}

func (r *Repository) DeleteAction(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM task_actions WHERE id=$1`, id)
	return err
}

// AddActionDirect wird nur vom synclink-Paket aufgerufen (keine erneute
// Sync-Ausloesung, um Endlosschleifen zu vermeiden).
func (r *Repository) AddActionDirect(ctx context.Context, taskID, description, userID string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO task_actions (id, task_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)`,
		taskID, description, userID)
	return err
}

// ── Ersatzteil-Merkliste ───────────────────────────────────────

type PendingPart struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	PartID        string    `json:"part_id"`
	PartName      string    `json:"part_name"`
	PartNumber    string    `json:"part_number"`
	StorageNodeID string    `json:"storage_node_id"`
	StorageName   string    `json:"storage_name"`
	Qty           float64   `json:"qty"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

type AddPendingPartInput struct {
	PartID        string  `json:"part_id"`
	StorageNodeID string  `json:"storage_node_id"`
	Qty           float64 `json:"qty"`
}

func (r *Repository) AddPendingPart(ctx context.Context, taskID, partID, storageNodeID string, qty float64, userID string) (*PendingPart, error) {
	p := &PendingPart{TaskID: taskID, PartID: partID, StorageNodeID: storageNodeID, Qty: qty, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO task_pending_parts (id, task_id, part_id, storage_node_id, qty, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		taskID, partID, storageNodeID, qty, userID,
	).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetPendingParts(ctx context.Context, taskID string) ([]*PendingPart, error) {
	rows, err := r.db.Query(ctx, `
		SELECT tp.id, tp.task_id, tp.part_id, sp.name, sp.part_number,
			tp.storage_node_id, sn.name, tp.qty, tp.created_by, u.first_name || ' ' || u.last_name, tp.created_at
		FROM task_pending_parts tp
		JOIN spare_parts sp ON tp.part_id = sp.id
		JOIN storage_nodes sn ON tp.storage_node_id = sn.id
		JOIN users u ON tp.created_by = u.id
		WHERE tp.task_id = $1
		ORDER BY tp.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PendingPart
	for rows.Next() {
		p := &PendingPart{}
		if err := rows.Scan(&p.ID, &p.TaskID, &p.PartID, &p.PartName, &p.PartNumber,
			&p.StorageNodeID, &p.StorageName, &p.Qty, &p.CreatedBy, &p.CreatedByName, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) DeletePendingPart(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM task_pending_parts WHERE id=$1`, id)
	return err
}

type PartUsage struct {
	ID            string    `json:"id"`
	PartID        string    `json:"part_id"`
	PartName      string    `json:"part_name"`
	PartNumber    string    `json:"part_number"`
	Qty           float64   `json:"qty"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

func (r *Repository) GetPartsUsage(ctx context.Context, taskID string) ([]*PartUsage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sm.id, sm.part_id, sp.name, sp.part_number, sm.qty, sm.created_by,
			u.first_name || ' ' || u.last_name, sm.created_at
		FROM stock_movements sm
		JOIN spare_parts sp ON sm.part_id = sp.id
		JOIN users u ON sm.created_by = u.id
		WHERE sm.task_id = $1
		ORDER BY sm.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PartUsage
	for rows.Next() {
		u := &PartUsage{}
		if err := rows.Scan(&u.ID, &u.PartID, &u.PartName, &u.PartNumber, &u.Qty, &u.CreatedBy, &u.CreatedByName, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ── Status setzen / Resolve ───────────────────────────────────

// SetStatusDirect wird nur vom synclink-Paket aufgerufen.
func (r *Repository) SetStatusDirect(ctx context.Context, taskID, status, userID string) error {
	query := `UPDATE tasks SET status=$1, updated_at=NOW()`
	if status == "resolved" || status == "closed" {
		query += `, resolved_at=COALESCE(resolved_at,NOW()), archived_at=COALESCE(archived_at,NOW())`
	}
	query += ` WHERE id=$2`
	_, err := r.db.Exec(ctx, query, status, taskID)
	return err
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	err := r.SetStatusDirect(ctx, id, string(status), userID)
	if err == nil && taskLinker != nil && userID != "" {
		taskLinker.OnTaskStatusChanged(ctx, id, string(status), userID)
	}
	return err
}

func (r *Repository) Resolve(ctx context.Context, id, resolution, rootCause, userID string, noPartsNeeded bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE tasks SET status='resolved', resolution=$1, root_cause=$2, no_parts_needed=$3,
		resolved_at=NOW(), archived_at=COALESCE(archived_at,NOW()), updated_at=NOW() WHERE id=$4`,
		resolution, rootCause, noPartsNeeded, id)
	return err
}

func (r *Repository) GetLinkedFaultID(ctx context.Context, taskID string) (string, error) {
	var id *string
	err := r.db.QueryRow(ctx, `SELECT linked_fault_id::text FROM tasks WHERE id=$1`, taskID).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

func (r *Repository) GetLinkedTicketID(ctx context.Context, taskID string) (string, error) {
	var id *string
	err := r.db.QueryRow(ctx, `SELECT linked_ticket_id::text FROM tasks WHERE id=$1`, taskID).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

var invService *inventory.Service

func SetInventoryService(inv *inventory.Service) { invService = inv }

var taskLinker *synclink.Linker
var globalTaskRepo *Repository

// SetLinker verbindet das tasks-Modul mit dem synclink-Paket.
func SetLinker(l *synclink.Linker) {
	taskLinker = l
	l.RegisterTask(
		func(ctx context.Context, taskID, status, userID string) error {
			return globalTaskRepo.SetStatusDirect(ctx, taskID, status, userID)
		},
		func(ctx context.Context, taskID, description, userID string) error {
			return globalTaskRepo.AddActionDirect(ctx, taskID, description, userID)
		},
		func(ctx context.Context, taskID string) (string, error) {
			return globalTaskRepo.GetLinkedFaultID(ctx, taskID)
		},
		func(ctx context.Context, taskID string) (string, error) {
			return globalTaskRepo.GetLinkedTicketID(ctx, taskID)
		},
	)
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) AddAction(ctx context.Context, taskID, description, userID string) (*TaskAction, error) {
	if description == "" {
		return nil, fmt.Errorf("beschreibung ist pflicht")
	}
	a, err := s.repo.AddAction(ctx, taskID, description, userID)
	if err == nil && taskLinker != nil {
		taskLinker.OnTaskActionAdded(ctx, taskID, description, userID)
	}
	return a, err
}
func (s *Service) GetActions(ctx context.Context, taskID string) ([]*TaskAction, error) {
	return s.repo.GetActions(ctx, taskID)
}
func (s *Service) DeleteAction(ctx context.Context, id string) error {
	return s.repo.DeleteAction(ctx, id)
}
func (s *Service) AddPendingPart(ctx context.Context, taskID string, in *AddPendingPartInput, userID string) (*PendingPart, error) {
	if in.PartID == "" || in.StorageNodeID == "" {
		return nil, fmt.Errorf("ersatzteil und lagerort sind pflicht")
	}
	if in.Qty <= 0 {
		return nil, fmt.Errorf("menge muss größer als 0 sein")
	}
	return s.repo.AddPendingPart(ctx, taskID, in.PartID, in.StorageNodeID, in.Qty, userID)
}
func (s *Service) GetPendingParts(ctx context.Context, taskID string) ([]*PendingPart, error) {
	return s.repo.GetPendingParts(ctx, taskID)
}
func (s *Service) DeletePendingPart(ctx context.Context, id string) error {
	return s.repo.DeletePendingPart(ctx, id)
}
func (s *Service) GetPartsUsage(ctx context.Context, taskID string) ([]*PartUsage, error) {
	return s.repo.GetPartsUsage(ctx, taskID)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	if status == StatusResolved || status == StatusClosed {
		return fmt.Errorf(`bitte die Aufgabe über "Aufgabe lösen" abschließen, nicht über die Status-Auswahl`)
	}
	return s.repo.UpdateStatus(ctx, id, status, userID)
}

func (s *Service) Resolve(ctx context.Context, taskID, resolution, rootCause, userID string, noPartsNeeded bool) error {
	actionCount, err := s.repo.CountActions(ctx, taskID)
	if err != nil {
		return err
	}
	if actionCount == 0 {
		return fmt.Errorf("bitte mindestens eine durchgeführte maßnahme erfassen, bevor die aufgabe gelöst werden kann")
	}
	pendingParts, err := s.repo.GetPendingParts(ctx, taskID)
	if err != nil {
		return err
	}
	if !noPartsNeeded && len(pendingParts) == 0 {
		return fmt.Errorf(`bitte verwendete ersatzteile erfassen oder "keine teile benötigt" bestätigen`)
	}
	if invService != nil {
		for _, pp := range pendingParts {
			_, bookErr := invService.Book(ctx, &inventory.BookMovementInput{
				PartID: pp.PartID, Type: inventory.MovementOut, Qty: pp.Qty,
				StorageNodeID: pp.StorageNodeID, Reference: "Aufgabe " + taskID, TaskID: taskID,
			}, pp.CreatedBy)
			if bookErr != nil {
				return fmt.Errorf("buchung für ersatzteil \"%s\" fehlgeschlagen: %w", pp.PartName, bookErr)
			}
			_ = s.repo.DeletePendingPart(ctx, pp.ID)
		}
	}
	if err := s.repo.Resolve(ctx, taskID, resolution, rootCause, userID, noPartsNeeded); err != nil {
		return err
	}
	if taskLinker != nil {
		taskLinker.OnTaskStatusChanged(ctx, taskID, "resolved", userID)
	}
	return nil
}
