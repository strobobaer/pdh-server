package tickets

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

type TicketAction struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticket_id"`
	Description   string    `json:"description"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (r *Repository) AddAction(ctx context.Context, ticketID, description, userID string) (*TicketAction, error) {
	a := &TicketAction{TicketID: ticketID, Description: description, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO ticket_actions (id, ticket_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, created_at`,
		ticketID, description, userID,
	).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (r *Repository) GetActions(ctx context.Context, ticketID string) ([]*TicketAction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ta.id, ta.ticket_id, ta.description, ta.created_by, u.first_name || ' ' || u.last_name, ta.created_at
		FROM ticket_actions ta
		JOIN users u ON ta.created_by = u.id
		WHERE ta.ticket_id = $1
		ORDER BY ta.created_at`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TicketAction
	for rows.Next() {
		a := &TicketAction{}
		if err := rows.Scan(&a.ID, &a.TicketID, &a.Description, &a.CreatedBy, &a.CreatedByName, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) CountActions(ctx context.Context, ticketID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_actions WHERE ticket_id=$1`, ticketID).Scan(&n)
	return n, err
}

func (r *Repository) DeleteAction(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM ticket_actions WHERE id=$1`, id)
	return err
}

// ── Ersatzteil-Merkliste (Buchung erst beim Schließen) ────────

type PendingPart struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticket_id"`
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

func (r *Repository) AddPendingPart(ctx context.Context, ticketID, partID, storageNodeID string, qty float64, userID string) (*PendingPart, error) {
	p := &PendingPart{TicketID: ticketID, PartID: partID, StorageNodeID: storageNodeID, Qty: qty, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO ticket_pending_parts (id, ticket_id, part_id, storage_node_id, qty, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		ticketID, partID, storageNodeID, qty, userID,
	).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetPendingParts(ctx context.Context, ticketID string) ([]*PendingPart, error) {
	rows, err := r.db.Query(ctx, `
		SELECT tp.id, tp.ticket_id, tp.part_id, sp.name, sp.part_number,
			tp.storage_node_id, sn.name, tp.qty, tp.created_by, u.first_name || ' ' || u.last_name, tp.created_at
		FROM ticket_pending_parts tp
		JOIN spare_parts sp ON tp.part_id = sp.id
		JOIN storage_nodes sn ON tp.storage_node_id = sn.id
		JOIN users u ON tp.created_by = u.id
		WHERE tp.ticket_id = $1
		ORDER BY tp.created_at`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PendingPart
	for rows.Next() {
		p := &PendingPart{}
		if err := rows.Scan(&p.ID, &p.TicketID, &p.PartID, &p.PartName, &p.PartNumber,
			&p.StorageNodeID, &p.StorageName, &p.Qty, &p.CreatedBy, &p.CreatedByName, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) DeletePendingPart(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM ticket_pending_parts WHERE id=$1`, id)
	return err
}

// PartUsage: ein tatsächlich für dieses Ticket gebuchtes Ersatzteil
// (stock_movements.ticket_id).
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

func (r *Repository) GetPartsUsage(ctx context.Context, ticketID string) ([]*PartUsage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sm.id, sm.part_id, sp.name, sp.part_number, sm.qty, sm.created_by,
			u.first_name || ' ' || u.last_name, sm.created_at
		FROM stock_movements sm
		JOIN spare_parts sp ON sm.part_id = sp.id
		JOIN users u ON sm.created_by = u.id
		WHERE sm.ticket_id = $1
		ORDER BY sm.created_at DESC`, ticketID)
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

// ── Resolve (Ticket lösen) ───────────────────────────────────

func (r *Repository) Resolve(ctx context.Context, id, resolution, rootCause, userID string, noPartsNeeded bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE tickets SET status='resolved', resolution=$1, root_cause=$2, no_parts_needed=$3,
		resolved_at=NOW(), archived_at=COALESCE(archived_at,NOW()), archived_by=$5, updated_at=NOW() WHERE id=$4`,
		resolution, rootCause, noPartsNeeded, id, userID)
	if err == nil {
		_, _ = r.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('ticket', $1, 'resolved', 'status', 'resolved', $2, 'Ticket gelöst und archiviert')`, id, userID)
	}
	return err
}

var invService *inventory.Service

// SetInventoryService verbindet das tickets-Modul mit dem Lager-Service
// (wird in main.go gesetzt) - noetig, um vorgemerkte Ersatzteile beim
// Loesen tatsaechlich zu buchen.
func SetInventoryService(inv *inventory.Service) { invService = inv }

// Resolve schließt ein Ticket ab: mindestens eine Maßnahme ist Pflicht,
// Ersatzteile ODER die "keine Teile benötigt"-Bestätigung ebenso. Beim
// Abschluss werden alle vorgemerkten Ersatzteile tatsächlich gebucht
// (Warenausgang, verknüpft mit dem Ticket).
func (s *Service) Resolve(ctx context.Context, ticketID, resolution, rootCause, userID string, noPartsNeeded bool) error {
	actionCount, err := s.repo.CountActions(ctx, ticketID)
	if err != nil {
		return err
	}
	if actionCount == 0 {
		return fmt.Errorf("bitte mindestens eine durchgeführte maßnahme erfassen, bevor das ticket geschlossen werden kann")
	}

	pendingParts, err := s.repo.GetPendingParts(ctx, ticketID)
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
				StorageNodeID: pp.StorageNodeID, Reference: "Ticket " + ticketID, TicketID: ticketID,
			}, pp.CreatedBy)
			if bookErr != nil {
				return fmt.Errorf("buchung für ersatzteil \"%s\" fehlgeschlagen: %w", pp.PartName, bookErr)
			}
			_ = s.repo.DeletePendingPart(ctx, pp.ID)
		}
	}

	if err := s.repo.Resolve(ctx, ticketID, resolution, rootCause, userID, noPartsNeeded); err != nil {
		return err
	}
	if eventBus != nil {
		eventBus.Publish("ticket.resolved", map[string]interface{}{
			"id": ticketID, "resolved_by": userID,
		})
	}
	if ticketLinker != nil {
		ticketLinker.OnTicketStatusChanged(ctx, ticketID, "resolved", userID)
	}
	return nil
}

// QuickResolve schließt ein Ticket OHNE die Pflichtprüfung (Maßnahmen/
// Ersatzteile) ab - für den generischen Archivieren-Weg (RecordArchiveWeb),
// z.B. bei versehentlich angelegten Datensätzen.
func (s *Service) QuickResolve(ctx context.Context, ticketID, resolution, rootCause, userID string) error {
	err := s.repo.Resolve(ctx, ticketID, resolution, rootCause, userID, true)
	if err == nil && eventBus != nil {
		eventBus.Publish("ticket.resolved", map[string]interface{}{
			"id": ticketID, "resolved_by": userID,
		})
	}
	return err
}

// ── Service-Wrapper ──────────────────────────────────────────

func (s *Service) AddAction(ctx context.Context, ticketID, description, userID string) (*TicketAction, error) {
	if strings.TrimSpace(description) == "" {
		return nil, fmt.Errorf("beschreibung ist pflicht")
	}
	a, err := s.repo.AddAction(ctx, ticketID, description, userID)
	if err == nil && ticketLinker != nil {
		ticketLinker.OnTicketActionAdded(ctx, ticketID, description, userID)
	}
	return a, err
}
func (s *Service) GetActions(ctx context.Context, ticketID string) ([]*TicketAction, error) {
	return s.repo.GetActions(ctx, ticketID)
}
func (s *Service) DeleteAction(ctx context.Context, id string) error {
	return s.repo.DeleteAction(ctx, id)
}
func (s *Service) AddPendingPart(ctx context.Context, ticketID string, in *AddPendingPartInput, userID string) (*PendingPart, error) {
	if in.PartID == "" || in.StorageNodeID == "" {
		return nil, fmt.Errorf("ersatzteil und lagerort sind pflicht")
	}
	if in.Qty <= 0 {
		return nil, fmt.Errorf("menge muss größer als 0 sein")
	}
	return s.repo.AddPendingPart(ctx, ticketID, in.PartID, in.StorageNodeID, in.Qty, userID)
}
func (s *Service) GetPendingParts(ctx context.Context, ticketID string) ([]*PendingPart, error) {
	return s.repo.GetPendingParts(ctx, ticketID)
}
func (s *Service) DeletePendingPart(ctx context.Context, id string) error {
	return s.repo.DeletePendingPart(ctx, id)
}
func (s *Service) GetPartsUsage(ctx context.Context, ticketID string) ([]*PartUsage, error) {
	return s.repo.GetPartsUsage(ctx, ticketID)
}

// ── Handler ──────────────────────────────────────────────────

func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Resolution    string `json:"resolution"`
		RootCause     string `json:"root_cause"`
		NoPartsNeeded bool   `json:"no_parts_needed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.Resolve(r.Context(), id, in.Resolution, in.RootCause, userID, in.NoPartsNeeded); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

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
