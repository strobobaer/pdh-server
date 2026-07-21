package faults

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/inventory"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// PendingPart: ein für eine Störung vorgesehenes Ersatzteil, das noch NICHT
// gebucht wurde (Bezeichnung + Lagerort + Menge). Wird erst beim Lösen der
// Störung tatsächlich gebucht (siehe Service.Resolve).
type PendingPart struct {
	ID            string    `json:"id"`
	FaultID       string    `json:"fault_id"`
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

var invService *inventory.Service

// SetInventoryService verbindet das faults-Modul mit dem Lager-Service
// (wird in main.go gesetzt) - noetig, um vorgemerkte Ersatzteile beim
// Loesen tatsaechlich zu buchen.
func SetInventoryService(inv *inventory.Service) { invService = inv }

// ── Repository ───────────────────────────────────────────────

func (r *Repository) AddPendingPart(ctx context.Context, faultID, partID, storageNodeID string, qty float64, userID string) (*PendingPart, error) {
	p := &PendingPart{FaultID: faultID, PartID: partID, StorageNodeID: storageNodeID, Qty: qty, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO fault_pending_parts (id, fault_id, part_id, storage_node_id, qty, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		faultID, partID, storageNodeID, qty, userID,
	).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetPendingParts(ctx context.Context, faultID string) ([]*PendingPart, error) {
	rows, err := r.db.Query(ctx, `
		SELECT fp.id, fp.fault_id, fp.part_id, sp.name, sp.part_number,
			fp.storage_node_id, sn.name, fp.qty, fp.created_by, u.first_name || ' ' || u.last_name, fp.created_at
		FROM fault_pending_parts fp
		JOIN spare_parts sp ON fp.part_id = sp.id
		JOIN storage_nodes sn ON fp.storage_node_id = sn.id
		JOIN users u ON fp.created_by = u.id
		WHERE fp.fault_id = $1
		ORDER BY fp.created_at`, faultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PendingPart
	for rows.Next() {
		p := &PendingPart{}
		if err := rows.Scan(&p.ID, &p.FaultID, &p.PartID, &p.PartName, &p.PartNumber,
			&p.StorageNodeID, &p.StorageName, &p.Qty, &p.CreatedBy, &p.CreatedByName, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) DeletePendingPart(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM fault_pending_parts WHERE id=$1`, id)
	return err
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) AddPendingPart(ctx context.Context, faultID string, in *AddPendingPartInput, userID string) (*PendingPart, error) {
	if in.PartID == "" || in.StorageNodeID == "" {
		return nil, fmt.Errorf("ersatzteil und lagerort sind pflicht")
	}
	if in.Qty <= 0 {
		return nil, fmt.Errorf("menge muss größer als 0 sein")
	}
	return s.repo.AddPendingPart(ctx, faultID, in.PartID, in.StorageNodeID, in.Qty, userID)
}
func (s *Service) GetPendingParts(ctx context.Context, faultID string) ([]*PendingPart, error) {
	return s.repo.GetPendingParts(ctx, faultID)
}
func (s *Service) DeletePendingPart(ctx context.Context, id string) error {
	return s.repo.DeletePendingPart(ctx, id)
}

// ── Handler ──────────────────────────────────────────────────

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
