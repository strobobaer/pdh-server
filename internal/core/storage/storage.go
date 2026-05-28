package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Modelle ──────────────────────────────────────────────────

type Warehouse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Location    string     `json:"location,omitempty"`
	Active      bool       `json:"active"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	Locations   []Location `json:"locations,omitempty"`
	PartCount   int        `json:"part_count,omitempty"`
}

type Location struct {
	ID          string  `json:"id"`
	WarehouseID string  `json:"warehouse_id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Places      []Place `json:"places,omitempty"`
}

type Place struct {
	ID                string `json:"id"`
	StorageLocationID string `json:"storage_location_id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Capacity          string `json:"capacity,omitempty"`
	CurrentParts      int    `json:"current_parts"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) CreateWarehouse(ctx context.Context, w *Warehouse) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO warehouses (id, name, description, location, created_by)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4) RETURNING id, created_at`,
		w.Name, w.Description, w.Location, w.CreatedBy,
	).Scan(&w.ID, &w.CreatedAt)
}

func (r *Repository) UpdateWarehouse(ctx context.Context, id string, w *Warehouse) error {
	_, err := r.db.Exec(ctx,
		`UPDATE warehouses SET name=$1, description=$2, location=$3 WHERE id=$4 AND active=true`,
		w.Name, w.Description, w.Location, id)
	return err
}

func (r *Repository) ListWarehouses(ctx context.Context) ([]*Warehouse, error) {
	rows, err := r.db.Query(ctx,
		`SELECT w.id, w.name, COALESCE(w.description,''), COALESCE(w.location,''),
		        w.active, w.created_by, w.created_at,
		        COUNT(sp.id) as part_count
		 FROM warehouses w
		 LEFT JOIN storage_locations sl ON sl.warehouse_id = w.id
		 LEFT JOIN storage_places sp ON sp.storage_location_id = sl.id
		 WHERE w.active=true
		 GROUP BY w.id ORDER BY w.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Warehouse
	for rows.Next() {
		w := &Warehouse{}
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.Location, &w.Active, &w.CreatedBy, &w.CreatedAt, &w.PartCount); err != nil {
			return nil, err
		}
		if tree, err := r.GetWarehouseTree(ctx, w.ID); err == nil && tree != nil {
			tree.PartCount = w.PartCount
			list = append(list, tree)
		} else {
			list = append(list, w)
		}
	}
	return list, rows.Err()
}

func (r *Repository) GetWarehouseTree(ctx context.Context, id string) (*Warehouse, error) {
	w := &Warehouse{}
	err := r.db.QueryRow(ctx,
		`SELECT id, name, COALESCE(description,''), COALESCE(location,''), active, created_by, created_at
		 FROM warehouses WHERE id=$1`, id).
		Scan(&w.ID, &w.Name, &w.Description, &w.Location, &w.Active, &w.CreatedBy, &w.CreatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx,
		`SELECT id, warehouse_id, name, COALESCE(description,'') FROM storage_locations WHERE warehouse_id=$1 ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		loc := Location{}
		if err := rows.Scan(&loc.ID, &loc.WarehouseID, &loc.Name, &loc.Description); err != nil {
			return nil, err
		}
		prows, err := r.db.Query(ctx,
			`SELECT id, storage_location_id, name, COALESCE(description,''), COALESCE(capacity,''), current_parts
			 FROM storage_places WHERE storage_location_id=$1 ORDER BY name`, loc.ID)
		if err != nil {
			return nil, err
		}
		for prows.Next() {
			p := Place{}
			if err := prows.Scan(&p.ID, &p.StorageLocationID, &p.Name, &p.Description, &p.Capacity, &p.CurrentParts); err != nil {
				prows.Close()
				return nil, err
			}
			loc.Places = append(loc.Places, p)
		}
		if err := prows.Err(); err != nil {
			prows.Close()
			return nil, err
		}
		prows.Close()
		w.PartCount += len(loc.Places)
		w.Locations = append(w.Locations, loc)
	}
	return w, rows.Err()
}

func (r *Repository) CreateLocation(ctx context.Context, l *Location) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO storage_locations (id, warehouse_id, name, description)
		 VALUES (gen_random_uuid(), $1, $2, $3) RETURNING id`,
		l.WarehouseID, l.Name, l.Description,
	).Scan(&l.ID)
}

func (r *Repository) UpdateLocation(ctx context.Context, id string, l *Location) error {
	_, err := r.db.Exec(ctx,
		`UPDATE storage_locations SET name=$1, description=$2 WHERE id=$3`,
		l.Name, l.Description, id)
	return err
}

func (r *Repository) CreatePlace(ctx context.Context, p *Place) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO storage_places (id, storage_location_id, name, description, capacity)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4) RETURNING id`,
		p.StorageLocationID, p.Name, p.Description, p.Capacity,
	).Scan(&p.ID)
}

func (r *Repository) UpdatePlace(ctx context.Context, id string, p *Place) error {
	_, err := r.db.Exec(ctx,
		`UPDATE storage_places SET name=$1, description=$2, capacity=$3 WHERE id=$4`,
		p.Name, p.Description, p.Capacity, id)
	return err
}

func (r *Repository) DeleteWarehouse(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE warehouses SET active=false WHERE id=$1`, id)
	return err
}

func (r *Repository) DeleteLocation(ctx context.Context, id string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM storage_places WHERE storage_location_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM storage_locations WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) DeletePlace(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM storage_places WHERE id=$1`, id)
	return err
}

func (r *Repository) GetStats(ctx context.Context) (map[string]int, error) {
	var wh, locs, places int
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM warehouses WHERE active=true`).Scan(&wh)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM storage_locations sl JOIN warehouses w ON sl.warehouse_id=w.id WHERE w.active=true`).Scan(&locs)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM storage_places sp JOIN storage_locations sl ON sp.storage_location_id=sl.id JOIN warehouses w ON sl.warehouse_id=w.id WHERE w.active=true`).Scan(&places)
	return map[string]int{"warehouses": wh, "locations": locs, "places": places}, nil
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) CreateWarehouse(ctx context.Context, name, desc, loc, userID string) (*Warehouse, error) {
	w := &Warehouse{Name: name, Description: desc, Location: loc, CreatedBy: userID}
	return w, s.repo.CreateWarehouse(ctx, w)
}

func (s *Service) UpdateWarehouse(ctx context.Context, id, name, desc, loc string) error {
	return s.repo.UpdateWarehouse(ctx, id, &Warehouse{Name: name, Description: desc, Location: loc})
}

func (s *Service) ListWarehouses(ctx context.Context) ([]*Warehouse, error) {
	return s.repo.ListWarehouses(ctx)
}
func (s *Service) GetTree(ctx context.Context, id string) (*Warehouse, error) {
	return s.repo.GetWarehouseTree(ctx, id)
}
func (s *Service) GetStats(ctx context.Context) (map[string]int, error) { return s.repo.GetStats(ctx) }

func (s *Service) CreateLocation(ctx context.Context, warehouseID, name, desc string) (*Location, error) {
	l := &Location{WarehouseID: warehouseID, Name: name, Description: desc}
	return l, s.repo.CreateLocation(ctx, l)
}

func (s *Service) UpdateLocation(ctx context.Context, id, name, desc string) error {
	return s.repo.UpdateLocation(ctx, id, &Location{Name: name, Description: desc})
}

func (s *Service) CreatePlace(ctx context.Context, locationID, name, desc, capacity string) (*Place, error) {
	p := &Place{StorageLocationID: locationID, Name: name, Description: desc, Capacity: capacity}
	return p, s.repo.CreatePlace(ctx, p)
}

func (s *Service) UpdatePlace(ctx context.Context, id, name, desc, capacity string) error {
	return s.repo.UpdatePlace(ctx, id, &Place{Name: name, Description: desc, Capacity: capacity})
}

func (s *Service) DeleteWarehouse(ctx context.Context, id string) error {
	return s.repo.DeleteWarehouse(ctx, id)
}

func (s *Service) DeleteLocation(ctx context.Context, id string) error {
	return s.repo.DeleteLocation(ctx, id)
}

func (s *Service) DeletePlace(ctx context.Context, id string) error {
	return s.repo.DeletePlace(ctx, id)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.CreateWarehouse)
	r.Get("/{id}", h.GetTree)
	r.Put("/{id}", h.UpdateWarehouse)
	r.Delete("/{id}", h.Delete)
	r.Post("/{id}/locations", h.CreateLocation)
	r.Put("/locations/{id}", h.UpdateLocation)
	r.Delete("/locations/{id}", h.DeleteLocation)
	r.Post("/locations/{id}/places", h.CreatePlace)
	r.Put("/places/{id}", h.UpdatePlace)
	r.Delete("/places/{id}", h.DeletePlace)
	r.Get("/stats", h.Stats)
	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func formOrJSON(r *http.Request, out interface{}, applyForm func()) error {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			return err
		}
		applyForm()
		return nil
	}
	return json.NewDecoder(r.Body).Decode(out)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListWarehouses(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, list)
}

func (h *Handler) CreateWarehouse(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description, Location string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description, in.Location = r.FormValue("name"), r.FormValue("description"), r.FormValue("location") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	wh, err := h.svc.CreateWarehouse(r.Context(), in.Name, in.Description, in.Location, uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, wh)
}

func (h *Handler) UpdateWarehouse(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description, Location string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description, in.Location = r.FormValue("name"), r.FormValue("description"), r.FormValue("location") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdateWarehouse(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description, in.Location); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GetTree(w http.ResponseWriter, r *http.Request) {
	tree, err := h.svc.GetTree(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, tree)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteWarehouse(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) DeleteLocation(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteLocation(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) DeletePlace(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePlace(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) CreateLocation(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description = r.FormValue("name"), r.FormValue("description") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	loc, err := h.svc.CreateLocation(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, loc)
}

func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description = r.FormValue("name"), r.FormValue("description") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdateLocation(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) CreatePlace(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description, Capacity string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description, in.Capacity = r.FormValue("name"), r.FormValue("description"), r.FormValue("capacity") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	p, err := h.svc.CreatePlace(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description, in.Capacity)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) UpdatePlace(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description, Capacity string }
	if err := formOrJSON(r, &in, func() { in.Name, in.Description, in.Capacity = r.FormValue("name"), r.FormValue("description"), r.FormValue("capacity") }); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdatePlace(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description, in.Capacity); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, _ := h.svc.GetStats(r.Context())
	response.JSON(w, 200, stats)
}

var _ = fmt.Sprintf
