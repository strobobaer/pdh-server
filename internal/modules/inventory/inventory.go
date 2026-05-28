package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type MovementType string
type StockStatus string

const (
	MovementIn       MovementType = "in"
	MovementOut      MovementType = "out"
	MovementTransfer MovementType = "transfer"
	MovementCorrect  MovementType = "correction"

	StatusOK       StockStatus = "ok"
	StatusLow      StockStatus = "low"
	StatusCritical StockStatus = "critical"
	StatusEmpty    StockStatus = "empty"
)

type SparePart struct {
	ID               string      `json:"id"`
	PartNumber       string      `json:"part_number"`
	Name             string      `json:"name"`
	Description      string      `json:"description,omitempty"`
	Category         string      `json:"category,omitempty"`
	Manufacturer     string      `json:"manufacturer,omitempty"`
	ManufacturerPart string      `json:"manufacturer_part,omitempty"`
	Unit             string      `json:"unit"`
	StockQty         float64     `json:"stock_qty"`
	MinQty           float64     `json:"min_qty"`
	CriticalQty      float64     `json:"critical_qty"`
	ReorderQty       float64     `json:"reorder_qty"`
	StorageLocation  string      `json:"storage_location,omitempty"`
	StoragePlace     string      `json:"storage_place,omitempty"`
	Price            float64     `json:"price,omitempty"`
	InfrastructureID *string     `json:"infrastructure_id,omitempty"`
	Active           bool        `json:"active"`
	CreatedBy        string      `json:"created_by"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	Status           StockStatus `json:"status"`
	InfraName        string      `json:"infra_name,omitempty"`
}

type StockMovement struct {
	ID        string       `json:"id"`
	PartID    string       `json:"part_id"`
	Type      MovementType `json:"type"`
	Qty       float64      `json:"qty"`
	QtyBefore float64      `json:"qty_before"`
	QtyAfter  float64      `json:"qty_after"`
	Reference string       `json:"reference,omitempty"`
	Notes     string       `json:"notes,omitempty"`
	CreatedBy string       `json:"created_by"`
	CreatedAt time.Time    `json:"created_at"`
	PartName  string       `json:"part_name,omitempty"`
	UserName  string       `json:"user_name,omitempty"`
}

type CustomFieldDef struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	FieldType string    `json:"field_type"`
	SortOrder int       `json:"sort_order"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type CustomFieldValue struct {
	FieldID   string `json:"field_id"`
	Name      string `json:"name,omitempty"`
	FieldType string `json:"field_type,omitempty"`
	Value     string `json:"value"`
}

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

type UpdatePartInput struct {
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
}

type BookMovementInput struct {
	PartID    string       `json:"part_id"`
	Type      MovementType `json:"type"`
	Qty       float64      `json:"qty"`
	Reference string       `json:"reference"`
	Notes     string       `json:"notes"`
}

type CreateFieldDefInput struct {
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
	SortOrder int    `json:"sort_order"`
}

type UpsertFieldValuesInput struct { Values map[string]string `json:"values"` }

type Repository struct{ db *pgxpool.Pool }
func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func calcStatus(stock, min, critical float64) StockStatus { if stock <= 0 { return StatusEmpty }; if stock <= critical { return StatusCritical }; if stock <= min { return StatusLow }; return StatusOK }

func (r *Repository) ensureCustomFieldTables(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS spare_part_field_defs (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), name VARCHAR(100) NOT NULL, field_type VARCHAR(30) NOT NULL DEFAULT 'text', sort_order INTEGER NOT NULL DEFAULT 100, active BOOLEAN NOT NULL DEFAULT true, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_defs_name_active ON spare_part_field_defs (lower(name)) WHERE active=true;
		CREATE TABLE IF NOT EXISTS spare_part_field_values (part_id UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE, field_id UUID NOT NULL REFERENCES spare_part_field_defs(id) ON DELETE CASCADE, value TEXT NOT NULL DEFAULT '', updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (part_id, field_id));`)
	return err
}

func (r *Repository) Create(ctx context.Context, p *SparePart) error { return r.db.QueryRow(ctx, `INSERT INTO spare_parts (id, part_number, name, description, category, manufacturer, manufacturer_part, unit, stock_qty, min_qty, critical_qty, reorder_qty, storage_location, storage_place, price, infrastructure_id, created_by) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id, active, created_at, updated_at`, p.PartNumber, p.Name, p.Description, p.Category, p.Manufacturer, p.ManufacturerPart, p.Unit, p.StockQty, p.MinQty, p.CriticalQty, p.ReorderQty, p.StorageLocation, p.StoragePlace, p.Price, p.InfrastructureID, p.CreatedBy).Scan(&p.ID, &p.Active, &p.CreatedAt, &p.UpdatedAt) }
func (r *Repository) Update(ctx context.Context, id string, p *SparePart) error { _, err := r.db.Exec(ctx, `UPDATE spare_parts SET part_number=$1, name=$2, description=$3, category=$4, manufacturer=$5, manufacturer_part=$6, unit=$7, min_qty=$8, critical_qty=$9, reorder_qty=$10, storage_location=$11, storage_place=$12, price=$13, infrastructure_id=$14, updated_at=NOW() WHERE id=$15 AND active=true`, p.PartNumber, p.Name, p.Description, p.Category, p.Manufacturer, p.ManufacturerPart, p.Unit, p.MinQty, p.CriticalQty, p.ReorderQty, p.StorageLocation, p.StoragePlace, p.Price, p.InfrastructureID, id); return err }

func scanPart(row interface{ Scan(...interface{}) error }) (*SparePart, error) { p := &SparePart{}; err := row.Scan(&p.ID, &p.PartNumber, &p.Name, &p.Description, &p.Category, &p.Manufacturer, &p.ManufacturerPart, &p.Unit, &p.StockQty, &p.MinQty, &p.CriticalQty, &p.ReorderQty, &p.StorageLocation, &p.StoragePlace, &p.Price, &p.InfrastructureID, &p.Active, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.InfraName); if err != nil { return nil, err }; p.Status = calcStatus(p.StockQty, p.MinQty, p.CriticalQty); return p, nil }
const partSelect = `SELECT sp.id, sp.part_number, sp.name, COALESCE(sp.description,''), COALESCE(sp.category,''), COALESCE(sp.manufacturer,''), COALESCE(sp.manufacturer_part,''), sp.unit, sp.stock_qty, sp.min_qty, sp.critical_qty, sp.reorder_qty, COALESCE(sp.storage_location,''), COALESCE(sp.storage_place,''), sp.price, sp.infrastructure_id, sp.active, sp.created_by, sp.created_at, sp.updated_at, COALESCE(i.name,'') FROM spare_parts sp LEFT JOIN infrastructure i ON sp.infrastructure_id = i.id`
func (r *Repository) GetByID(ctx context.Context, id string) (*SparePart, error) { return scanPart(r.db.QueryRow(ctx, partSelect+` WHERE sp.id=$1 AND sp.active=true`, id)) }
func (r *Repository) List(ctx context.Context, category, status, q string) ([]*SparePart, error) { query := partSelect + ` WHERE sp.active=true`; args := []interface{}{}; n := 1; if category != "" { query += fmt.Sprintf(" AND sp.category=$%d", n); args = append(args, category); n++ }; if q != "" { query += fmt.Sprintf(" AND (sp.name ILIKE $%d OR sp.part_number ILIKE $%d OR sp.manufacturer ILIKE $%d)", n, n, n); args = append(args, "%"+q+"%"); n++ }; query += " ORDER BY sp.category, sp.name"; rows, err := r.db.Query(ctx, query, args...); if err != nil { return nil, err }; defer rows.Close(); var parts []*SparePart; for rows.Next() { p, err := scanPart(rows); if err != nil { return nil, err }; if status != "" && string(p.Status) != status { continue }; parts = append(parts, p) }; return parts, rows.Err() }

func (r *Repository) BookMovement(ctx context.Context, m *StockMovement) error { var current float64; r.db.QueryRow(ctx, `SELECT stock_qty FROM spare_parts WHERE id=$1`, m.PartID).Scan(&current); m.QtyBefore = current; newQty := current; switch m.Type { case MovementIn: newQty = current + m.Qty; case MovementOut, MovementTransfer: newQty = current - m.Qty; case MovementCorrect: newQty = m.Qty; m.Qty = newQty - current }; m.QtyAfter = newQty; if _, err := r.db.Exec(ctx, `UPDATE spare_parts SET stock_qty=$1, updated_at=NOW() WHERE id=$2`, newQty, m.PartID); err != nil { return err }; return r.db.QueryRow(ctx, `INSERT INTO stock_movements (id, part_id, type, qty, qty_before, qty_after, reference, notes, created_by) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`, m.PartID, m.Type, m.Qty, m.QtyBefore, m.QtyAfter, m.Reference, m.Notes, m.CreatedBy).Scan(&m.ID, &m.CreatedAt) }
func (r *Repository) GetMovements(ctx context.Context, partID string) ([]*StockMovement, error) { rows, err := r.db.Query(ctx, `SELECT sm.id, sm.part_id, sm.type, sm.qty, sm.qty_before, sm.qty_after, COALESCE(sm.reference,''), COALESCE(sm.notes,''), sm.created_by, sm.created_at, sp.name, u.first_name||' '||u.last_name FROM stock_movements sm JOIN spare_parts sp ON sm.part_id = sp.id JOIN users u ON sm.created_by = u.id WHERE sm.part_id=$1 ORDER BY sm.created_at DESC LIMIT 50`, partID); if err != nil { return nil, err }; defer rows.Close(); var movements []*StockMovement; for rows.Next() { m := &StockMovement{}; if err := rows.Scan(&m.ID, &m.PartID, &m.Type, &m.Qty, &m.QtyBefore, &m.QtyAfter, &m.Reference, &m.Notes, &m.CreatedBy, &m.CreatedAt, &m.PartName, &m.UserName); err != nil { return nil, err }; movements = append(movements, m) }; return movements, rows.Err() }
func (r *Repository) GetLowStock(ctx context.Context) ([]*SparePart, error) { rows, err := r.db.Query(ctx, partSelect+` WHERE sp.active=true AND sp.stock_qty <= sp.min_qty ORDER BY sp.stock_qty ASC`); if err != nil { return nil, err }; defer rows.Close(); var parts []*SparePart; for rows.Next() { p, err := scanPart(rows); if err != nil { return nil, err }; parts = append(parts, p) }; return parts, rows.Err() }
func (r *Repository) GetStats(ctx context.Context) (map[string]interface{}, error) { var total, lowStock, critical, empty int; var totalValue float64; r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true`).Scan(&total); r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= min_qty`).Scan(&lowStock); r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= critical_qty`).Scan(&critical); r.db.QueryRow(ctx, `SELECT COUNT(*) FROM spare_parts WHERE active=true AND stock_qty <= 0`).Scan(&empty); r.db.QueryRow(ctx, `SELECT COALESCE(SUM(stock_qty * price), 0) FROM spare_parts WHERE active=true`).Scan(&totalValue); return map[string]interface{}{"total": total, "low_stock": lowStock, "critical": critical, "empty": empty, "total_value": totalValue}, nil }

func (r *Repository) ListFieldDefs(ctx context.Context) ([]*CustomFieldDef, error) { if err := r.ensureCustomFieldTables(ctx); err != nil { return nil, err }; rows, err := r.db.Query(ctx, `SELECT id, name, field_type, sort_order, active, created_at FROM spare_part_field_defs WHERE active=true ORDER BY sort_order, name`); if err != nil { return nil, err }; defer rows.Close(); var defs []*CustomFieldDef; for rows.Next() { d := &CustomFieldDef{}; if err := rows.Scan(&d.ID, &d.Name, &d.FieldType, &d.SortOrder, &d.Active, &d.CreatedAt); err != nil { return nil, err }; defs = append(defs, d) }; return defs, rows.Err() }
func (r *Repository) CreateFieldDef(ctx context.Context, d *CustomFieldDef) error { if err := r.ensureCustomFieldTables(ctx); err != nil { return err }; if d.FieldType == "" { d.FieldType = "text" }; if d.SortOrder == 0 { d.SortOrder = 100 }; return r.db.QueryRow(ctx, `INSERT INTO spare_part_field_defs (id, name, field_type, sort_order) VALUES (gen_random_uuid(), $1, $2, $3) RETURNING id, active, created_at`, d.Name, d.FieldType, d.SortOrder).Scan(&d.ID, &d.Active, &d.CreatedAt) }
func (r *Repository) GetFieldValues(ctx context.Context, partID string) ([]*CustomFieldValue, error) { if err := r.ensureCustomFieldTables(ctx); err != nil { return nil, err }; rows, err := r.db.Query(ctx, `SELECT d.id, d.name, d.field_type, COALESCE(v.value, '') FROM spare_part_field_defs d LEFT JOIN spare_part_field_values v ON v.field_id=d.id AND v.part_id=$1 WHERE d.active=true ORDER BY d.sort_order, d.name`, partID); if err != nil { return nil, err }; defer rows.Close(); var values []*CustomFieldValue; for rows.Next() { v := &CustomFieldValue{}; if err := rows.Scan(&v.FieldID, &v.Name, &v.FieldType, &v.Value); err != nil { return nil, err }; values = append(values, v) }; return values, rows.Err() }
func (r *Repository) UpsertFieldValues(ctx context.Context, partID string, values map[string]string) error { if err := r.ensureCustomFieldTables(ctx); err != nil { return err }; for fieldID, value := range values { if strings.TrimSpace(fieldID) == "" { continue }; if _, err := r.db.Exec(ctx, `INSERT INTO spare_part_field_values (part_id, field_id, value, updated_at) VALUES ($1, $2, $3, NOW()) ON CONFLICT (part_id, field_id) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()`, partID, fieldID, value); err != nil { return err } }; return nil }

type Service struct{ repo *Repository }
func NewService(repo *Repository) *Service { return &Service{repo: repo} }
func (s *Service) Create(ctx context.Context, in *CreatePartInput, userID string) (*SparePart, error) { p := &SparePart{PartNumber: in.PartNumber, Name: in.Name, Description: in.Description, Category: in.Category, Manufacturer: in.Manufacturer, ManufacturerPart: in.ManufacturerPart, Unit: in.Unit, StockQty: in.InitialStock, MinQty: in.MinQty, CriticalQty: in.CriticalQty, ReorderQty: in.ReorderQty, StorageLocation: in.StorageLocation, StoragePlace: in.StoragePlace, Price: in.Price, InfrastructureID: in.InfrastructureID, CreatedBy: userID}; if p.Unit == "" { p.Unit = "Stück" }; return p, s.repo.Create(ctx, p) }
func (s *Service) Update(ctx context.Context, id string, in *UpdatePartInput) error { p := &SparePart{PartNumber: in.PartNumber, Name: in.Name, Description: in.Description, Category: in.Category, Manufacturer: in.Manufacturer, ManufacturerPart: in.ManufacturerPart, Unit: in.Unit, MinQty: in.MinQty, CriticalQty: in.CriticalQty, ReorderQty: in.ReorderQty, StorageLocation: in.StorageLocation, StoragePlace: in.StoragePlace, Price: in.Price, InfrastructureID: in.InfrastructureID}; if p.Unit == "" { p.Unit = "Stück" }; return s.repo.Update(ctx, id, p) }
func (s *Service) GetByID(ctx context.Context, id string) (*SparePart, error) { return s.repo.GetByID(ctx, id) }
func (s *Service) List(ctx context.Context, cat, status, q string) ([]*SparePart, error) { return s.repo.List(ctx, cat, status, q) }
func (s *Service) GetLowStock(ctx context.Context) ([]*SparePart, error) { return s.repo.GetLowStock(ctx) }
func (s *Service) GetMovements(ctx context.Context, partID string) ([]*StockMovement, error) { return s.repo.GetMovements(ctx, partID) }
func (s *Service) GetStats(ctx context.Context) (map[string]interface{}, error) { return s.repo.GetStats(ctx) }
func (s *Service) ListFieldDefs(ctx context.Context) ([]*CustomFieldDef, error) { return s.repo.ListFieldDefs(ctx) }
func (s *Service) CreateFieldDef(ctx context.Context, in *CreateFieldDefInput) (*CustomFieldDef, error) { d := &CustomFieldDef{Name: in.Name, FieldType: in.FieldType, SortOrder: in.SortOrder}; return d, s.repo.CreateFieldDef(ctx, d) }
func (s *Service) GetFieldValues(ctx context.Context, partID string) ([]*CustomFieldValue, error) { return s.repo.GetFieldValues(ctx, partID) }
func (s *Service) UpsertFieldValues(ctx context.Context, partID string, values map[string]string) error { return s.repo.UpsertFieldValues(ctx, partID, values) }
func (s *Service) Book(ctx context.Context, in *BookMovementInput, userID string) (*StockMovement, error) { if in.Qty <= 0 && in.Type != MovementCorrect { return nil, fmt.Errorf("menge muss größer als 0 sein") }; m := &StockMovement{PartID: in.PartID, Type: in.Type, Qty: in.Qty, Reference: in.Reference, Notes: in.Notes, CreatedBy: userID}; return m, s.repo.BookMovement(ctx, m) }

type Handler struct{ svc *Service }
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }
func (h *Handler) Routes(jwtSecret string) chi.Router { r := chi.NewRouter(); r.Use(middleware.Auth(jwtSecret)); r.Get("/", h.List); r.Post("/", h.Create); r.Get("/low", h.LowStock); r.Get("/stats", h.Stats); r.Get("/fields", h.ListFields); r.Post("/fields", h.CreateField); r.Get("/fields/{fieldID}/options", h.ListFieldOptions); r.Post("/fields/{fieldID}/options", h.CreateFieldOption); r.Get("/field-sets", h.ListFieldSets); r.Post("/field-sets", h.CreateFieldSet); r.Get("/field-sets/{setID}/fields", h.ListFieldsForSet); r.Post("/field-sets/{setID}/fields", h.CreateFieldInSet); r.Post("/book", h.Book); r.Get("/{id}", h.GetByID); r.Put("/{id}", h.Update); r.Get("/{id}/field-set", h.GetAssignedFieldSet); r.Put("/{id}/field-set", h.AssignFieldSet); r.Get("/{id}/field-sets", h.GetAssignedFieldSets); r.Put("/{id}/field-sets", h.AssignFieldSets); r.Get("/{id}/fields", h.GetFieldValues); r.Get("/{id}/fields-assigned", h.GetAssignedFieldValuesMulti); r.Put("/{id}/fields", h.UpsertFieldValues); r.Get("/{id}/movements", h.GetMovements); return r }
func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }
func decodeJSONOrForm(r *http.Request, v interface{}, apply func()) error { ct := strings.ToLower(r.Header.Get("Content-Type")); if strings.Contains(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "multipart/form-data") { if err := r.ParseForm(); err != nil { return err }; apply(); return nil }; return json.NewDecoder(r.Body).Decode(v) }
func f64(r *http.Request, name string) float64 { v, _ := strconv.ParseFloat(strings.ReplaceAll(r.FormValue(name), ",", "."), 64); return v }
func partInputFromForm(r *http.Request) CreatePartInput { return CreatePartInput{PartNumber: r.FormValue("part_number"), Name: r.FormValue("name"), Description: r.FormValue("description"), Category: r.FormValue("category"), Manufacturer: r.FormValue("manufacturer"), ManufacturerPart: r.FormValue("manufacturer_part"), Unit: r.FormValue("unit"), MinQty: f64(r, "min_qty"), CriticalQty: f64(r, "critical_qty"), ReorderQty: f64(r, "reorder_qty"), InitialStock: f64(r, "initial_stock"), StorageLocation: r.FormValue("storage_location"), StoragePlace: r.FormValue("storage_place"), Price: f64(r, "price")} }
func updateInputFromForm(r *http.Request) UpdatePartInput { c := partInputFromForm(r); return UpdatePartInput{PartNumber: c.PartNumber, Name: c.Name, Description: c.Description, Category: c.Category, Manufacturer: c.Manufacturer, ManufacturerPart: c.ManufacturerPart, Unit: c.Unit, MinQty: c.MinQty, CriticalQty: c.CriticalQty, ReorderQty: c.ReorderQty, StorageLocation: c.StorageLocation, StoragePlace: c.StoragePlace, Price: c.Price} }
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) { var in CreatePartInput; if err := decodeJSONOrForm(r, &in, func(){ in = partInputFromForm(r) }); err != nil { response.Error(w, 400, "ungültige eingabe"); return }; p, err := h.svc.Create(r.Context(), &in, uid(r)); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 201, p) }
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) { var in UpdatePartInput; if err := decodeJSONOrForm(r, &in, func(){ in = updateInputFromForm(r) }); err != nil { response.Error(w, 400, "ungültige eingabe"); return }; if err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), &in); err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, map[string]string{"status":"gespeichert"}) }
func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) { p, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id")); if err != nil { response.Error(w, 404, "nicht gefunden"); return }; response.JSON(w, 200, p) }
func (h *Handler) List(w http.ResponseWriter, r *http.Request) { parts, err := h.svc.List(r.Context(), r.URL.Query().Get("category"), r.URL.Query().Get("status"), r.URL.Query().Get("q")); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, parts) }
func (h *Handler) LowStock(w http.ResponseWriter, r *http.Request) { parts, err := h.svc.GetLowStock(r.Context()); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, parts) }
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) { stats, err := h.svc.GetStats(r.Context()); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, stats) }
func (h *Handler) Book(w http.ResponseWriter, r *http.Request) { var in BookMovementInput; if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }; m, err := h.svc.Book(r.Context(), &in, uid(r)); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 201, m) }
func (h *Handler) GetMovements(w http.ResponseWriter, r *http.Request) { movements, err := h.svc.GetMovements(r.Context(), chi.URLParam(r, "id")); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, movements) }
func (h *Handler) ListFields(w http.ResponseWriter, r *http.Request) { defs, err := h.svc.ListFieldDefs(r.Context()); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, defs) }
func (h *Handler) CreateField(w http.ResponseWriter, r *http.Request) { var in CreateFieldDefInput; if err := decodeJSONOrForm(r, &in, func(){ in.Name, in.FieldType, in.SortOrder = r.FormValue("name"), r.FormValue("field_type"), int(f64(r, "sort_order")) }); err != nil { response.Error(w, 400, "ungültige eingabe"); return }; d, err := h.svc.CreateFieldDef(r.Context(), &in); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 201, d) }
func (h *Handler) GetFieldValues(w http.ResponseWriter, r *http.Request) { values, err := h.svc.GetFieldValues(r.Context(), chi.URLParam(r, "id")); if err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, values) }
func (h *Handler) UpsertFieldValues(w http.ResponseWriter, r *http.Request) { var in UpsertFieldValuesInput; if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }; if err := h.svc.UpsertFieldValues(r.Context(), chi.URLParam(r, "id"), in.Values); err != nil { response.Error(w, 500, err.Error()); return }; response.JSON(w, 200, map[string]string{"status":"gespeichert"}) }
