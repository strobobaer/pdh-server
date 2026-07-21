package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/response"
)

type StocktakeStatus string

const (
	StocktakeOpen      StocktakeStatus = "open"
	StocktakeBooked    StocktakeStatus = "booked"
	StocktakeCancelled StocktakeStatus = "cancelled"
)

// StocktakeSession: eine Inventur-Sitzung über einen Lagerort-Bereich
// ("von"-"bis", Geschwister unter demselben Elternknoten).
type StocktakeSession struct {
	ID         string          `json:"id"`
	FromNodeID string          `json:"from_node_id"`
	FromName   string          `json:"from_name,omitempty"`
	ToNodeID   string          `json:"to_node_id"`
	ToName     string          `json:"to_name,omitempty"`
	Status     StocktakeStatus `json:"status"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  time.Time       `json:"created_at"`
	BookedBy   *string         `json:"booked_by,omitempty"`
	BookedAt   *time.Time      `json:"booked_at,omitempty"`
}

// StocktakeItem: eine Zeile der Zählliste (ein Lagerort + ein Ersatzteil).
// PartID ist NULL bei Platzhalterzeilen für leere Plätze.
type StocktakeItem struct {
	ID            string     `json:"id"`
	SessionID     string     `json:"session_id"`
	StorageNodeID string     `json:"storage_node_id"`
	StorageName   string     `json:"storage_name,omitempty"`
	PartID        *string    `json:"part_id,omitempty"`
	PartName      string     `json:"part_name,omitempty"`
	PartNumber    string     `json:"part_number,omitempty"`
	ExpectedQty   float64    `json:"expected_qty"`
	CountedQty    *float64   `json:"counted_qty,omitempty"`
	CountedBy     *string    `json:"counted_by,omitempty"`
	CountedAt     *time.Time `json:"counted_at,omitempty"`
}

type CreateStocktakeInput struct {
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`
}

type CountItemInput struct {
	ItemID     string   `json:"item_id"`
	PartID     *string  `json:"part_id,omitempty"`
	CountedQty *float64 `json:"counted_qty"`
}

type SubmitCountsInput struct {
	Items []CountItemInput `json:"items"`
}

// ── Repository ───────────────────────────────────────────────

// resolveLeafNodes: alle Blatt-Lagerorte (ohne Kinder), erreichbar von
// nodeID (inklusive nodeID selbst, falls es bereits ein Blatt ist).
func (r *Repository) resolveLeafNodes(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE sub AS (
			SELECT id, parent_id FROM storage_nodes WHERE id = $1 AND active = true
			UNION ALL
			SELECT sn.id, sn.parent_id FROM storage_nodes sn
			JOIN sub ON sn.parent_id = sub.id
			WHERE sn.active = true
		)
		SELECT s.id::text FROM sub s
		WHERE NOT EXISTS (SELECT 1 FROM storage_nodes c WHERE c.parent_id = s.id AND c.active = true)`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// resolveRange: findet alle Blatt-Lagerorte zwischen fromNodeID und toNodeID.
// Beide muessen Geschwister mit demselben Elternknoten sein (oder beide
// oberste Ebene ohne Elternknoten).
func (r *Repository) resolveRange(ctx context.Context, fromNodeID, toNodeID string) ([]string, error) {
	var fromParent, toParent *string
	if err := r.db.QueryRow(ctx, `SELECT parent_id::text FROM storage_nodes WHERE id=$1 AND active=true`, fromNodeID).Scan(&fromParent); err != nil {
		return nil, fmt.Errorf("lagerort 'von' nicht gefunden")
	}
	if err := r.db.QueryRow(ctx, `SELECT parent_id::text FROM storage_nodes WHERE id=$1 AND active=true`, toNodeID).Scan(&toParent); err != nil {
		return nil, fmt.Errorf("lagerort 'bis' nicht gefunden")
	}
	sameParent := (fromParent == nil && toParent == nil) || (fromParent != nil && toParent != nil && *fromParent == *toParent)
	if !sameParent {
		return nil, fmt.Errorf("'von' und 'bis' müssen unter demselben übergeordneten Lagerort liegen")
	}

	var siblingsQuery string
	var args []interface{}
	if fromParent == nil {
		siblingsQuery = `SELECT id::text FROM storage_nodes WHERE parent_id IS NULL AND active=true ORDER BY name`
	} else {
		siblingsQuery = `SELECT id::text FROM storage_nodes WHERE parent_id=$1 AND active=true ORDER BY name`
		args = append(args, *fromParent)
	}
	rows, err := r.db.Query(ctx, siblingsQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var siblings []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		siblings = append(siblings, id)
	}

	fromIdx, toIdx := -1, -1
	for i, id := range siblings {
		if id == fromNodeID {
			fromIdx = i
		}
		if id == toNodeID {
			toIdx = i
		}
	}
	if fromIdx == -1 || toIdx == -1 {
		return nil, fmt.Errorf("lagerorte konnten in der geschwisterliste nicht gefunden werden")
	}
	if fromIdx > toIdx {
		fromIdx, toIdx = toIdx, fromIdx
	}
	rangeNodes := siblings[fromIdx : toIdx+1]

	leafSet := map[string]bool{}
	for _, nodeID := range rangeNodes {
		leaves, err := r.resolveLeafNodes(ctx, nodeID)
		if err != nil {
			return nil, err
		}
		for _, l := range leaves {
			leafSet[l] = true
		}
	}
	var result []string
	for id := range leafSet {
		result = append(result, id)
	}
	return result, nil
}

// IsLocationLocked: liegt storageNodeID in einer aktuell offenen Inventur?
func (r *Repository) IsLocationLocked(ctx context.Context, storageNodeID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM inventory_session_items isi
			JOIN inventory_sessions s ON isi.session_id = s.id
			WHERE isi.storage_node_id = $1 AND s.status = 'open'
		)`, storageNodeID).Scan(&exists)
	return exists, err
}

func (r *Repository) CreateStocktakeSession(ctx context.Context, fromNodeID, toNodeID, userID string) (*StocktakeSession, []*StocktakeItem, error) {
	leafNodes, err := r.resolveRange(ctx, fromNodeID, toNodeID)
	if err != nil {
		return nil, nil, err
	}
	if len(leafNodes) == 0 {
		return nil, nil, fmt.Errorf("im gewählten bereich wurden keine lagerorte gefunden")
	}

	for _, nodeID := range leafNodes {
		locked, err := r.IsLocationLocked(ctx, nodeID)
		if err != nil {
			return nil, nil, err
		}
		if locked {
			return nil, nil, fmt.Errorf("mindestens ein lagerort im gewählten bereich ist bereits durch eine laufende inventur gesperrt")
		}
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	session := &StocktakeSession{FromNodeID: fromNodeID, ToNodeID: toNodeID, Status: StocktakeOpen, CreatedBy: userID}
	if err := tx.QueryRow(ctx, `
		INSERT INTO inventory_sessions (id, from_node_id, to_node_id, status, created_by)
		VALUES (gen_random_uuid(), $1, $2, 'open', $3)
		RETURNING id, created_at`, fromNodeID, toNodeID, userID,
	).Scan(&session.ID, &session.CreatedAt); err != nil {
		return nil, nil, err
	}

	var items []*StocktakeItem
	for _, nodeID := range leafNodes {
		stockRows, err := tx.Query(ctx, `SELECT part_id::text, qty FROM spare_part_stock WHERE storage_node_id=$1 AND qty <> 0`, nodeID)
		if err != nil {
			return nil, nil, err
		}
		type stockRow struct {
			PartID string
			Qty    float64
		}
		var stocks []stockRow
		for stockRows.Next() {
			var s stockRow
			if err := stockRows.Scan(&s.PartID, &s.Qty); err != nil {
				stockRows.Close()
				return nil, nil, err
			}
			stocks = append(stocks, s)
		}
		stockRows.Close()

		if len(stocks) == 0 {
			item := &StocktakeItem{SessionID: session.ID, StorageNodeID: nodeID, ExpectedQty: 0}
			if err := tx.QueryRow(ctx, `
				INSERT INTO inventory_session_items (id, session_id, storage_node_id, part_id, expected_qty)
				VALUES (gen_random_uuid(), $1, $2, NULL, 0)
				RETURNING id`, session.ID, nodeID).Scan(&item.ID); err != nil {
				return nil, nil, err
			}
			items = append(items, item)
			continue
		}
		for _, s := range stocks {
			partID := s.PartID
			item := &StocktakeItem{SessionID: session.ID, StorageNodeID: nodeID, PartID: &partID, ExpectedQty: s.Qty}
			if err := tx.QueryRow(ctx, `
				INSERT INTO inventory_session_items (id, session_id, storage_node_id, part_id, expected_qty)
				VALUES (gen_random_uuid(), $1, $2, $3, $4)
				RETURNING id`, session.ID, nodeID, s.PartID, s.Qty).Scan(&item.ID); err != nil {
				return nil, nil, err
			}
			items = append(items, item)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return session, items, nil
}

func (r *Repository) GetStocktakeSession(ctx context.Context, sessionID string) (*StocktakeSession, error) {
	s := &StocktakeSession{}
	err := r.db.QueryRow(ctx, `
		SELECT ses.id, ses.from_node_id, COALESCE(fn.name,''), ses.to_node_id, COALESCE(tn.name,''),
			ses.status, ses.created_by, ses.created_at, ses.booked_by, ses.booked_at
		FROM inventory_sessions ses
		LEFT JOIN storage_nodes fn ON ses.from_node_id = fn.id
		LEFT JOIN storage_nodes tn ON ses.to_node_id = tn.id
		WHERE ses.id=$1`, sessionID,
	).Scan(&s.ID, &s.FromNodeID, &s.FromName, &s.ToNodeID, &s.ToName, &s.Status, &s.CreatedBy, &s.CreatedAt, &s.BookedBy, &s.BookedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *Repository) GetStocktakeItems(ctx context.Context, sessionID string) ([]*StocktakeItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT isi.id, isi.session_id, isi.storage_node_id, COALESCE(sn.name,''),
			isi.part_id, COALESCE(sp.name,''), COALESCE(sp.part_number,''),
			isi.expected_qty, isi.counted_qty, isi.counted_by, isi.counted_at
		FROM inventory_session_items isi
		JOIN storage_nodes sn ON isi.storage_node_id = sn.id
		LEFT JOIN spare_parts sp ON isi.part_id = sp.id
		WHERE isi.session_id=$1
		ORDER BY sn.name, sp.name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*StocktakeItem
	for rows.Next() {
		it := &StocktakeItem{}
		if err := rows.Scan(&it.ID, &it.SessionID, &it.StorageNodeID, &it.StorageName,
			&it.PartID, &it.PartName, &it.PartNumber,
			&it.ExpectedQty, &it.CountedQty, &it.CountedBy, &it.CountedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (r *Repository) ListOpenStocktakeSessions(ctx context.Context) ([]*StocktakeSession, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ses.id, ses.from_node_id, COALESCE(fn.name,''), ses.to_node_id, COALESCE(tn.name,''),
			ses.status, ses.created_by, ses.created_at, ses.booked_by, ses.booked_at
		FROM inventory_sessions ses
		LEFT JOIN storage_nodes fn ON ses.from_node_id = fn.id
		LEFT JOIN storage_nodes tn ON ses.to_node_id = tn.id
		WHERE ses.status='open'
		ORDER BY ses.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []*StocktakeSession
	for rows.Next() {
		s := &StocktakeSession{}
		if err := rows.Scan(&s.ID, &s.FromNodeID, &s.FromName, &s.ToNodeID, &s.ToName, &s.Status, &s.CreatedBy, &s.CreatedAt, &s.BookedBy, &s.BookedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (r *Repository) SubmitCounts(ctx context.Context, sessionID string, items []CountItemInput, userID string) error {
	for _, it := range items {
		if it.CountedQty == nil {
			continue
		}
		if it.PartID != nil {
			if _, err := r.db.Exec(ctx, `
				UPDATE inventory_session_items
				SET part_id=$1, counted_qty=$2, counted_by=$3, counted_at=NOW()
				WHERE id=$4 AND session_id=$5`,
				*it.PartID, *it.CountedQty, userID, it.ItemID, sessionID); err != nil {
				return err
			}
		} else {
			if _, err := r.db.Exec(ctx, `
				UPDATE inventory_session_items
				SET counted_qty=$1, counted_by=$2, counted_at=NOW()
				WHERE id=$3 AND session_id=$4`,
				*it.CountedQty, userID, it.ItemID, sessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

// BookStocktakeSession: bucht alle Abweichungen (Ist != Soll) als
// Inventurbuchungen und schliesst die Sitzung ab (loest damit auch die
// Buchungssperre fuer den Bereich, da IsLocationLocked nur 'open'-Sitzungen
// zaehlt). Verlangt, dass ALLE Positionen gezaehlt wurden.
func (r *Repository) BookStocktakeSession(ctx context.Context, sessionID, userID string) error {
	session, err := r.GetStocktakeSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Status != StocktakeOpen {
		return fmt.Errorf("inventur ist nicht mehr offen")
	}

	items, err := r.GetStocktakeItems(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.CountedQty == nil {
			return fmt.Errorf("es sind noch nicht alle positionen gezählt worden")
		}
	}

	for _, it := range items {
		if it.PartID == nil {
			continue // Platzhalter ohne zugewiesenes Teil - nichts zu buchen
		}
		if *it.CountedQty != it.ExpectedQty {
			m := &StockMovement{
				PartID: *it.PartID, Type: MovementInventory, Qty: *it.CountedQty,
				StorageNodeID: it.StorageNodeID, Reference: "Inventur " + sessionID[:8], CreatedBy: userID,
			}
			if err := r.BookMovement(ctx, m); err != nil {
				return err
			}
		}
	}

	_, err = r.db.Exec(ctx, `UPDATE inventory_sessions SET status='booked', booked_by=$1, booked_at=NOW() WHERE id=$2`, userID, sessionID)
	return err
}

func (r *Repository) CancelStocktakeSession(ctx context.Context, sessionID string) error {
	_, err := r.db.Exec(ctx, `UPDATE inventory_sessions SET status='cancelled' WHERE id=$1 AND status='open'`, sessionID)
	return err
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) CreateStocktake(ctx context.Context, in *CreateStocktakeInput, userID string) (*StocktakeSession, []*StocktakeItem, error) {
	return s.repo.CreateStocktakeSession(ctx, in.FromNodeID, in.ToNodeID, userID)
}
func (s *Service) GetStocktakeSession(ctx context.Context, id string) (*StocktakeSession, error) {
	return s.repo.GetStocktakeSession(ctx, id)
}
func (s *Service) GetStocktakeItems(ctx context.Context, id string) ([]*StocktakeItem, error) {
	return s.repo.GetStocktakeItems(ctx, id)
}
func (s *Service) ListOpenStocktakes(ctx context.Context) ([]*StocktakeSession, error) {
	return s.repo.ListOpenStocktakeSessions(ctx)
}
func (s *Service) SubmitCounts(ctx context.Context, sessionID string, in *SubmitCountsInput, userID string) error {
	return s.repo.SubmitCounts(ctx, sessionID, in.Items, userID)
}
func (s *Service) BookStocktake(ctx context.Context, sessionID, userID string) error {
	return s.repo.BookStocktakeSession(ctx, sessionID, userID)
}
func (s *Service) CancelStocktake(ctx context.Context, sessionID string) error {
	return s.repo.CancelStocktakeSession(ctx, sessionID)
}

// ── Handler ──────────────────────────────────────────────────

func (h *Handler) CreateStocktake(w http.ResponseWriter, r *http.Request) {
	var in CreateStocktakeInput
	if err := decodeJSONOrForm(r, &in, func() {
		in.FromNodeID = r.FormValue("from_node_id")
		in.ToNodeID = r.FormValue("to_node_id")
	}); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	session, items, err := h.svc.CreateStocktake(r.Context(), &in, uid(r))
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, map[string]interface{}{"session": session, "items": items})
}

func (h *Handler) GetStocktake(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionID")
	session, err := h.svc.GetStocktakeSession(r.Context(), id)
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	items, err := h.svc.GetStocktakeItems(r.Context(), id)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]interface{}{"session": session, "items": items})
}

func (h *Handler) ListOpenStocktakes(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.svc.ListOpenStocktakes(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, sessions)
}

func (h *Handler) SubmitStocktakeCounts(w http.ResponseWriter, r *http.Request) {
	var in SubmitCountsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.SubmitCounts(r.Context(), chi.URLParam(r, "sessionID"), &in, uid(r)); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) BookStocktake(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.BookStocktake(r.Context(), chi.URLParam(r, "sessionID"), uid(r)); err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gebucht"})
}

func (h *Handler) CancelStocktake(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.CancelStocktake(r.Context(), chi.URLParam(r, "sessionID")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "abgebrochen"})
}
