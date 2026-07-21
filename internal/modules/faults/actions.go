package faults

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// FaultAction: ein einzelner Eintrag im Maßnahmen-Verlauf einer Störung.
type FaultAction struct {
	ID            string    `json:"id"`
	FaultID       string    `json:"fault_id"`
	Description   string    `json:"description"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ── Repository ───────────────────────────────────────────────

func (r *Repository) AddAction(ctx context.Context, faultID, description, userID string) (*FaultAction, error) {
	a := &FaultAction{FaultID: faultID, Description: description, CreatedBy: userID}
	err := r.db.QueryRow(ctx, `
		INSERT INTO fault_actions (id, fault_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, created_at`,
		faultID, description, userID,
	).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (r *Repository) GetActions(ctx context.Context, faultID string) ([]*FaultAction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT fa.id, fa.fault_id, fa.description, fa.created_by, u.first_name || ' ' || u.last_name, fa.created_at
		FROM fault_actions fa
		JOIN users u ON fa.created_by = u.id
		WHERE fa.fault_id = $1
		ORDER BY fa.created_at`, faultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []*FaultAction
	for rows.Next() {
		a := &FaultAction{}
		if err := rows.Scan(&a.ID, &a.FaultID, &a.Description, &a.CreatedBy, &a.CreatedByName, &a.CreatedAt); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

// Hinweis: CountActions() und CountPartsUsage() werden von
// apply_fault_actions_repository_patch_v2.py direkt in repository.go
// ergaenzt (fuer Service.Resolve()) - hier nicht nochmal definieren.

func (r *Repository) DeleteAction(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM fault_actions WHERE id=$1`, id)
	return err
}

// FaultPartUsage: ein Ersatzteil, das für diese Störung gebucht wurde
// (echte Lagerbuchung, stock_movements.fault_id).
type FaultPartUsage struct {
	ID            string    `json:"id"`
	PartID        string    `json:"part_id"`
	PartName      string    `json:"part_name"`
	PartNumber    string    `json:"part_number"`
	Qty           float64   `json:"qty"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

func (r *Repository) GetPartsUsage(ctx context.Context, faultID string) ([]*FaultPartUsage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sm.id, sm.part_id, sp.name, sp.part_number, sm.qty, sm.created_by,
			u.first_name || ' ' || u.last_name, sm.created_at
		FROM stock_movements sm
		JOIN spare_parts sp ON sm.part_id = sp.id
		JOIN users u ON sm.created_by = u.id
		WHERE sm.fault_id = $1
		ORDER BY sm.created_at DESC`, faultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FaultPartUsage
	for rows.Next() {
		u := &FaultPartUsage{}
		if err := rows.Scan(&u.ID, &u.PartID, &u.PartName, &u.PartNumber, &u.Qty, &u.CreatedBy, &u.CreatedByName, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) AddAction(ctx context.Context, faultID, description, userID string) (*FaultAction, error) {
	if strings.TrimSpace(description) == "" {
		return nil, fmt.Errorf("beschreibung ist pflicht")
	}
	return s.repo.AddAction(ctx, faultID, description, userID)
}
func (s *Service) GetActions(ctx context.Context, faultID string) ([]*FaultAction, error) {
	return s.repo.GetActions(ctx, faultID)
}
func (s *Service) DeleteAction(ctx context.Context, id string) error {
	return s.repo.DeleteAction(ctx, id)
}
func (s *Service) GetPartsUsage(ctx context.Context, faultID string) ([]*FaultPartUsage, error) {
	return s.repo.GetPartsUsage(ctx, faultID)
}

// ── Handler ──────────────────────────────────────────────────

// AddAction akzeptiert sowohl Formulardaten (fuer htmx-lose JS-Fetch-Aufrufe
// mit FormData) als auch JSON, analog zu den bereits bestehenden dual-parse
// Handlern in diesem Modul (z.B. UpdateCostCenter).
func (h *Handler) AddAction(w http.ResponseWriter, r *http.Request) {
	description := ""
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var in struct {
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err == nil {
			description = in.Description
		}
	} else {
		r.ParseForm()
		description = r.FormValue("description")
	}

	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	a, err := h.svc.AddAction(r.Context(), chi.URLParam(r, "id"), description, userID)
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

func (h *Handler) GetPartsUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := h.svc.GetPartsUsage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, usage)
}
