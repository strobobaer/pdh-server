package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

type MovementType string
type StockStatus  string

const (
	MovementIn       MovementType = "in"        // Zugang
	MovementOut      MovementType = "out"       // Abgang
	MovementTransfer MovementType = "transfer"  // Umbuchung
	MovementCorrect  MovementType = "correction"// Korrektur

	StatusOK       StockStatus = "ok"
	StatusLow      StockStatus = "low"       // Unter Mindestbestand
	StatusCritical StockStatus = "critical"  // Unter kritischem Bestand
	StatusEmpty    StockStatus = "empty"     // Leer
)

// ── Ersatzteil ───────────────────────────────────────────────

type SparePart struct {
	ID               string    `json:"id"`
	PartNumber       string    `json:"part_number"`
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	Category         string    `json:"category,omitempty"`
	Manufacturer     string    `json:"manufacturer,omitempty"`
	ManufacturerPart string    `json:"manufacturer_part,omitempty"`
	Unit             string    `json:"unit"`
	StockQty         float64   `json:"stock_qty"`
	MinQty           float64   `json:"min_qty"`
	CriticalQty      float64   `json:"critical_qty"`
	ReorderQty       float64   `json:"reorder_qty"`
	StorageLocation  string    `json:"storage_location,omitempty"`
	StoragePlace     string    `json:"storage_place,omitempty"`
	Price            float64   `json:"price,omitempty"`
	InfrastructureID *string   `json:"infrastructure_id,omitempty"`
	Active           bool      `json:"active"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// Berechnet
	Status     StockStatus `json:"status"`
	InfraName  string      `json:"infra_name,omitempty"`
}

// ── Lagerbewegung ────────────────────────────────────────────

type StockMovement struct {
	ID          string       `json:"id"`
	PartID      string       `json:"part_id"`
	Type        MovementType `json:"type"`
	Qty         float64      `json:"qty"`
	QtyBefore   float64      `json:"qty_before"`
	QtyAfter    float64      `json:"qty_after"`
	Reference   string       `json:"reference,omitempty"`
	Notes       string       `json:"notes,omitempty"`
	CreatedBy   string       `json:"created_by"`
	CreatedAt   time.Time    `json:"created_at"`

	PartName    string `json:"part_name,omitempty"`
	UserName    string `json:"user_name,omitempty"`
}

// ── Inputs ───────────────────────────────────────────────────

type CreatePartInput struct {
	PartNumber       string  `json:"part_number"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Category         string  `json:"category"`
	Manufacturer     string  `json:"manufacturer"`
	ManufacturerPart string  `json:"manufacturer_part"`
	Unit             string  `json:"unit"`
	MinQty           float64 `json:"min_qty"`
	CriticalQty      float64 `json:"critical_qty"`
	ReorderQty       float64 `json:"reorder_qty"`
	StorageLocation  string  `json:"storage_location"`
	StoragePlace     string  `json:"storage_place"`
	Price            float64 `json:"price"`
	InfrastructureID *string `json:"infrastructure_id,omitempty"`
	InitialStock     float64 `json:"initial_stock"`
}

type BookMovementInput struct {
	PartID    string       `json:"part_id"`
	Type      MovementType `json:"type"`
	Qty       float64      `json:"qty"`
	Reference string       `json:"reference"`
	Notes     string       `json:"notes"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }
func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func calcStatus(stock, min, critical float64) StockStatus {
	if stock <= 0          { return StatusEmpty }
	if stock <= critical   { return StatusCritical }
	if stock <= min        { return StatusLow }
	return StatusOK
}

func (r *Repository) Create(ctx context.Context, p *SparePart) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO spare_parts
		  (id, part_number, name, description, category, manufacturer,
		   manufacturer_part, unit, stock_qty, min_qty, critical_qty, reorder_qty,
		   storage_location, storage_place, price, infrastructure_id, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id, active, created_at, updated_at`,
		p.PartNumber, p.Name, p.Description, p.Category,
		p.Manufacturer, p.ManufacturerPart, p.Unit,
		p.StockQty, p.MinQty, p.CriticalQty, p.ReorderQty,
		p.StorageLocation, p.StoragePlace, p.Price,
		p.InfrastructureID, p.CreatedBy,
	).Scan(&p.ID, &p.Active, &p.CreatedAt, &p.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*SparePart, error) {
	p := &SparePart{}
	err := r.db.QueryRow(ctx, `
		SELECT sp.id, sp.part_number, sp.name, COALESCE(sp.description,''),
		       COALESCE(sp.category,''), COALESCE(sp.manufacturer,''),
		       COALESCE(sp.manufacturer_part,''), sp.unit,
		       sp.stock_qty, sp.min_qty, sp.critical_qty, sp.reorder_qty,
		       COALESCE(sp.storage_location,''), COALESCE(sp.storage_place,''),
		       sp.price, sp.infrastructure_id, sp.active,
		       sp.created_by, sp.created_at, sp.updated_at,
		       COALESCE(i.name,'')
		FROM spare_parts sp
		LEFT JOIN infrastructure i ON sp.infrastructure_id = i.id
		WHERE sp.id=$1 AND sp.active=true`, id).Scan(
		&p.ID, &p.PartNumber, &p.Name, &p.Description,
		&p.Category, &p.Manufacturer, &p.ManufacturerPart, &p.Unit,
		&p.StockQty, &p.MinQty, &p.CriticalQty, &p.ReorderQty,
		&p.StorageLocation, &p.StoragePlace,
		&p.Price, &p.InfrastructureID, &p.Active,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.InfraName,
	)
	if err != nil { return nil, err }
	p.Status = calcStatus(p.StockQty, p.MinQty, p.CriticalQty)
	return p, nil
}

func (r *Repository) List(ctx context.Context, category, status, q string) ([]*SparePart, error) {
	query := `
		SELECT sp.id, sp.part_number, sp.name, COALESCE(sp.description,''),
		       COALESCE(sp.category,''), COALESCE(sp.manufacturer,''),
		       COALESCE(sp.manufacturer_part,''), sp.unit,
		       sp.stock_qty, sp.min_qty, sp.critical_qty, sp.reorder_qty,
		       COALESCE(sp.storage_location,''), COALESCE(sp.storage_place,''),
		       sp.price, sp.infrastructure_id, sp.active,
		       sp.created_by, sp.created_at, sp.updated_at,
		       COALESCE(i.name,'')
		FROM spare_parts sp
		LEFT JOIN infrastructure i ON sp.infrastructure_id = i.id
		WHERE sp.active=true`
	args := []interface{}{}
	n := 1

	if category != "" {
		query += fmt.Sprintf(" AND sp.category=$%d", n)
		args = append(args, category); n++
	}
	if q != "" {
		query += fmt.Sprintf(" AND (sp.name ILIKE $%d OR sp.part_number ILIKE $%d OR sp.manufacturer ILIKE $%d)", n, n, n)
		args = append(args, "%"+q+"%"); n++
	}

	query += " ORDER BY sp.category, sp.name"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil { return nil, err }
	defer rows.Close()

	var parts []*SparePart
	for rows.Next() {
		p := &SparePart{}
		rows.Scan(
			&p.ID, &p.PartNumber, &p.Name, &p.Description,
			&p.Category, &p.Manufacturer, &p.ManufacturerPart, &p.Unit,
			&p.StockQty, &p.MinQty, &p.CriticalQty, &p.ReorderQty,
			&p.StorageLocation, &p.StoragePlace,
			&p.Price, &p.InfrastructureID, &p.Active,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.InfraName,
		)
		p.Status = calcStatus(p.StockQty, p.MinQty, p.CriticalQty)

		// Status-Filter
		if status != "" && string(p.Status) != status { continue }
		parts = append(parts, p)
	}
	return parts, nil
}

func (r *Repository) BookMovement(ctx context.Context, m *StockMovement) error {
	// Aktuellen Bestand holen
	var current float64
	r.db.QueryRow(ctx, `SELECT stock_qty FROM spare_parts WHERE id=$1`, m.PartID).Scan(&current)
	m.QtyBefore = current

	var newQty float64
	switch m.Type {
	case MovementIn:
		newQty = current + m.Qty
	case MovementOut, MovementTransfer:
		newQty = current - m.Qty
	case MovementCorrect:
		newQty = m.Qty
		m.Qty = newQty - current
	}
	m.QtyAfter = newQty

	// Bestand aktualisieren
	_, err := r.db.Exec(ctx,
		`UPDATE spare_parts SET stock_qty=$1, updated_at=NOW() WHERE id=$2`,
		newQty, m.PartID)
	if err != nil { return err }

	// Bewegung buchen
	return r.db.QueryRow(ctx, `
		INSERT INTO stock_movements
		  (id, part_id, type, qty, qty_before, qty_after, reference, notes, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		m.PartID, m.Type, m.Qty, m.QtyBefore, m.QtyAfter,
		m.Reference, m.Notes, m.CreatedBy,
	).Scan(&m.ID, &m.CreatedAt)
}

func (r *Repository) GetMovements(ctx context.Context, partID string) ([]*StockMovement, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sm.id, sm.part_id, sm.type, sm.qty, sm.qty_before, sm.qty_after,
		       COALESCE(sm.reference,''), COALESCE(sm.notes,''),
		       sm.created_by, sm.created_at,
		       sp.name, u.first_name||' '||u.last_name
		FROM stock_movements sm
		JOIN spare_parts sp ON sm.part_id = sp.id
		JOIN users u ON sm.created_by = u.id
		WHERE sm.part_id=$1
		ORDER BY sm.created_at DESC LIMIT 50`, partID)
	if err != nil { return nil, err }
	defer rows.Close()

	var movements []*StockMovement
	for rows.Next() {
		m := &StockMovement{}
		rows.Scan(&m.ID, &m.PartID, &m.Type, &m.Qty,
			&m.QtyBefore, &m.QtyAfter, &m.Reference, &m.Notes,
			&m.CreatedBy, &m.CreatedAt, &m.PartName, &m.UserName)
		movements = append(movements, m)
	}
	return movements, nil
}

func (r *Repository) GetLowStock(ctx context.Context) ([]*SparePart, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sp.id, sp.part_number, sp.name, COALESCE(sp.description,''),
		       COALESCE(sp.category,''), COALESCE(sp.manufacturer,''),
		       COALESCE(sp.manufacturer_part,''), sp.unit,
		       sp.stock_qty, sp.min_qty, sp.critical_qty, sp.reorder_qty,
		       COALESCE(sp.storage_location,''), COALESCE(sp.storage_place,''),
		       sp.price, sp.infrastructure_id, sp.active,
		       sp.created_by, sp.created_at, sp.updated_at,
		       COALESCE(i.name,'')
		FROM spare_parts sp
		LEFT JOIN infrastructure i ON sp.infrastructure_id = i.id
		WHERE sp.active=true AND sp.stock_qty <= sp.min_qty
		ORDER BY sp.stock_qty / NULLIF(sp.min_qty, 0) ASC`)
	if err != nil { return nil, err }
	defer rows.Close()

	var parts []*SparePart
	for rows.Next() {
		p := &SparePart{}
		rows.Scan(
			&p.ID, &p.PartNumber, &p.Name, &p.Description,
			&p.Category, &p.Manufacturer, &p.ManufacturerPart, &p.Unit,
			&p.StockQty, &p.MinQty, &p.CriticalQty, &p.ReorderQty,
			&p.StorageLocation, &p.StoragePlace,
			&p.Price, &p.InfrastructureID, &p.Active,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.InfraName,
		)
		p.Status = calcStatus(p.StockQty, p.MinQty, p.CriticalQty)
		parts = append(parts, p)
	}
	return parts, nil
}

func (r *Repository) GetStats(ctx context.Context) (map[string]interface{}, error) {
	var total, lowStock, critical, empty int
	var totalValue float64
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true`).Scan(&total)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= min_qty`).Scan(&lowStock)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= critical_qty`).Scan(&critical)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= 0`).Scan(&empty)
	r.db.QueryRow(ctx, `SELECT COALESCE(SUM(stock_qty * price), 0) FROM spare_parts WHERE active=true`).Scan(&totalValue)
	return map[string]interface{}{
		"total":       total,
		"low_stock":   lowStock,
		"critical":    critical,
		"empty":       empty,
		"total_value": totalValue,
	}, nil
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }
func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreatePartInput, userID string) (*SparePart, error) {
	p := &SparePart{
		PartNumber: in.PartNumber, Name: in.Name, Description: in.Description,
		Category: in.Category, Manufacturer: in.Manufacturer,
		ManufacturerPart: in.ManufacturerPart, Unit: in.Unit,
		StockQty: in.InitialStock, MinQty: in.MinQty,
		CriticalQty: in.CriticalQty, ReorderQty: in.ReorderQty,
		StorageLocation: in.StorageLocation, StoragePlace: in.StoragePlace,
		Price: in.Price, InfrastructureID: in.InfrastructureID,
		CreatedBy: userID,
	}
	if p.Unit == "" { p.Unit = "Stück" }
	return p, s.repo.Create(ctx, p)
}

func (s *Service) GetByID(ctx context.Context, id string) (*SparePart, error)                { return s.repo.GetByID(ctx, id) }
func (s *Service) List(ctx context.Context, cat, status, q string) ([]*SparePart, error)     { return s.repo.List(ctx, cat, status, q) }
func (s *Service) GetLowStock(ctx context.Context) ([]*SparePart, error)                      { return s.repo.GetLowStock(ctx) }
func (s *Service) GetMovements(ctx context.Context, partID string) ([]*StockMovement, error)  { return s.repo.GetMovements(ctx, partID) }
func (s *Service) GetStats(ctx context.Context) (map[string]interface{}, error)               { return s.repo.GetStats(ctx) }

func (s *Service) Book(ctx context.Context, in *BookMovementInput, userID string) (*StockMovement, error) {
	if in.Qty <= 0 && in.Type != MovementCorrect {
		return nil, fmt.Errorf("menge muss größer als 0 sein")
	}
	m := &StockMovement{
		PartID: in.PartID, Type: in.Type, Qty: in.Qty,
		Reference: in.Reference, Notes: in.Notes, CreatedBy: userID,
	}
	return m, s.repo.BookMovement(ctx, m)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/",          h.List)
	r.Post("/",         h.Create)
	r.Get("/low",       h.LowStock)
	r.Get("/stats",     h.Stats)
	r.Post("/book",     h.Book)
	r.Get("/{id}",      h.GetByID)
	r.Get("/{id}/movements", h.GetMovements)

	return r
}

func decode(r *http.Request, v interface{}) error { return json.NewDecoder(r.Body).Decode(v) }
func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreatePartInput
	if err := decode(r, &in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	p, err := h.svc.Create(r.Context(), &in, uid(r))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 201, p)
}
func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil { response.Error(w, 404, "nicht gefunden"); return }
	response.JSON(w, 200, p)
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	parts, err := h.svc.List(r.Context(),
		r.URL.Query().Get("category"),
		r.URL.Query().Get("status"),
		r.URL.Query().Get("q"))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, parts)
}
func (h *Handler) LowStock(w http.ResponseWriter, r *http.Request) {
	parts, err := h.svc.GetLowStock(r.Context())
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, parts)
}
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.GetStats(r.Context())
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, stats)
}
func (h *Handler) Book(w http.ResponseWriter, r *http.Request) {
	var in BookMovementInput
	if err := decode(r, &in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
	m, err := h.svc.Book(r.Context(), &in, uid(r))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 201, m)
}
func (h *Handler) GetMovements(w http.ResponseWriter, r *http.Request) {
	movements, err := h.svc.GetMovements(r.Context(), chi.URLParam(r, "id"))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, movements)
}
