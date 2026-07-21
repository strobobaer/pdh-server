package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/inventory"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
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
		INSERT INTO maintenance_task_actions (id, task_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, created_at`,
		taskID, description, userID,
	).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (r *Repository) GetActions(ctx context.Context, taskID string) ([]*TaskAction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ta.id, ta.task_id, ta.description, ta.created_by, u.first_name || ' ' || u.last_name, ta.created_at
		FROM maintenance_task_actions ta
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
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_actions WHERE task_id=$1`, taskID).Scan(&n)
	return n, err
}

func (r *Repository) DeleteAction(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_task_actions WHERE id=$1`, id)
	return err
}

// ── Ersatzteil-Merkliste (Buchung erst beim Abschließen) ──────

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
		INSERT INTO maintenance_task_pending_parts (id, task_id, part_id, storage_node_id, qty, created_by)
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
		FROM maintenance_task_pending_parts tp
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
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_task_pending_parts WHERE id=$1`, id)
	return err
}

// PartUsage: ein tatsächlich für diesen Wartungsauftrag gebuchtes Ersatzteil.
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
		WHERE sm.maintenance_task_id = $1
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

// ── CompleteTask: no_parts_needed mitschreiben ────────────────

func (r *Repository) CompleteTaskWithFlag(ctx context.Context, id, userID, notes string, durationMin int, noPartsNeeded bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE maintenance_tasks SET status='done', completed_at=NOW(),
		 notes=$1, duration_min=$2, no_parts_needed=$3 WHERE id=$4`,
		notes, durationMin, noPartsNeeded, id)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE maintenance_plans mp
		SET last_executed_at=NOW(),
		    next_due_at=NOW() + (interval_days || ' days')::interval
		FROM maintenance_tasks mt
		WHERE mt.id=$1 AND mt.plan_id=mp.id`, id)
	return err
}

var invService *inventory.Service

// SetInventoryService verbindet das maintenance-Modul mit dem Lager-Service
// (wird in main.go gesetzt) - noetig, um vorgemerkte Ersatzteile beim
// Abschliessen tatsaechlich zu buchen.
func SetInventoryService(inv *inventory.Service) { invService = inv }

// CompleteTaskValidated schließt einen Wartungsauftrag ab: mindestens eine
// Maßnahme ist Pflicht, Ersatzteile ODER die "keine Teile benötigt"-
// Bestätigung ebenso. Beim Abschluss werden alle vorgemerkten Ersatzteile
// tatsächlich gebucht (Warenausgang, verknüpft mit dem Auftrag).
func (s *Service) CompleteTaskValidated(ctx context.Context, taskID, userID string, in *CompleteTaskInput, noPartsNeeded bool) error {
	actionCount, err := s.repo.CountActions(ctx, taskID)
	if err != nil {
		return err
	}
	if actionCount == 0 {
		return fmt.Errorf("bitte mindestens eine durchgeführte maßnahme erfassen, bevor der auftrag abgeschlossen werden kann")
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
				StorageNodeID: pp.StorageNodeID, Reference: "Wartungsauftrag " + taskID, MaintenanceTaskID: taskID,
			}, pp.CreatedBy)
			if bookErr != nil {
				return fmt.Errorf("buchung für ersatzteil \"%s\" fehlgeschlagen: %w", pp.PartName, bookErr)
			}
			_ = s.repo.DeletePendingPart(ctx, pp.ID)
		}
	}

	if err := s.repo.CompleteTaskWithFlag(ctx, taskID, userID, in.Notes, in.DurationMin, noPartsNeeded); err != nil {
		return err
	}
	if eventBus != nil {
		eventBus.Publish("maintenance.task_completed", map[string]interface{}{
			"id": taskID, "completed_by": userID, "notes": in.Notes, "duration_min": in.DurationMin,
		})
	}
	return nil
}

// QuickComplete schließt einen Auftrag OHNE die Pflichtprüfung ab - für den
// generischen Archivieren-Weg (falls vorhanden) bzw. Altbestand.
func (s *Service) QuickComplete(ctx context.Context, taskID, userID string, in *CompleteTaskInput) error {
	err := s.repo.CompleteTaskWithFlag(ctx, taskID, userID, in.Notes, in.DurationMin, true)
	if err == nil && eventBus != nil {
		eventBus.Publish("maintenance.task_completed", map[string]interface{}{
			"id": taskID, "completed_by": userID, "notes": in.Notes, "duration_min": in.DurationMin,
		})
	}
	return err
}

// ── Service-Wrapper ──────────────────────────────────────────

func (s *Service) AddAction(ctx context.Context, taskID, description, userID string) (*TaskAction, error) {
	if strings.TrimSpace(description) == "" {
		return nil, fmt.Errorf("beschreibung ist pflicht")
	}
	return s.repo.AddAction(ctx, taskID, description, userID)
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

// ── Handler ──────────────────────────────────────────────────

func (h *Handler) AddAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	a, err := h.svc.AddAction(r.Context(), chi.URLParam(r, "id"), in.Description, userID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, a)
}

func (h *Handler) GetActions(w http.ResponseWriter, r *http.Request) {
	actions, err := h.svc.GetActions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, actions)
}

func (h *Handler) DeleteAction(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteAction(r.Context(), chi.URLParam(r, "actionID")); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "gelöscht"})
}

func (h *Handler) AddPendingPart(w http.ResponseWriter, r *http.Request) {
	var in AddPendingPartInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	p, err := h.svc.AddPendingPart(r.Context(), chi.URLParam(r, "id"), &in, userID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, p)
}

func (h *Handler) GetPendingParts(w http.ResponseWriter, r *http.Request) {
	parts, err := h.svc.GetPendingParts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, parts)
}

func (h *Handler) DeletePendingPart(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePendingPart(r.Context(), chi.URLParam(r, "partItemID")); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "entfernt"})
}

func (h *Handler) GetPartsUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := h.svc.GetPartsUsage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, usage)
}
