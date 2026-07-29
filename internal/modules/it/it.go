package it

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type AssetType string
type AssetStatus string

const (
	TypeServer      AssetType = "server"
	TypeNetwork     AssetType = "network"
	TypeWorkstation AssetType = "workstation"
	TypePrinter     AssetType = "printer"
	TypePhone       AssetType = "phone"
	TypeTablet      AssetType = "tablet"
	TypeOther       AssetType = "other"

	StatusActive      AssetStatus = "active"
	StatusInactive    AssetStatus = "inactive"
	StatusMaintenance AssetStatus = "maintenance"
	StatusRetired     AssetStatus = "retired"
)

type Asset struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Type             AssetType   `json:"type"`
	Status           AssetStatus `json:"status"`
	Hostname         string      `json:"hostname,omitempty"`
	IPAddress        string      `json:"ip_address,omitempty"`
	MACAddress       string      `json:"mac_address,omitempty"`
	Manufacturer     string      `json:"manufacturer,omitempty"`
	Model            string      `json:"model,omitempty"`
	SerialNo         string      `json:"serial_no,omitempty"`
	Location         string      `json:"location,omitempty"`
	OS               string      `json:"os,omitempty"`
	PurchasedAt      *string     `json:"purchased_at,omitempty"`
	WarrantyUntil    *string     `json:"warranty_until,omitempty"`
	AssignedTo       *string     `json:"assigned_to,omitempty"`
	InfrastructureID *string     `json:"infrastructure_id,omitempty"`
	Notes            string      `json:"notes,omitempty"`
	CreatedBy        string      `json:"created_by"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	AssigneeName     string      `json:"assignee_name,omitempty"`
	InfraName        string      `json:"infra_name,omitempty"`
}

type CreateAssetInput struct {
	Name             string    `json:"name"`
	Type             AssetType `json:"type"`
	Hostname         string    `json:"hostname"`
	IPAddress        string    `json:"ip_address"`
	Manufacturer     string    `json:"manufacturer"`
	Model            string    `json:"model"`
	SerialNo         string    `json:"serial_no"`
	Location         string    `json:"location"`
	OS               string    `json:"os"`
	AssignedTo       *string   `json:"assigned_to,omitempty"`
	InfrastructureID *string   `json:"infrastructure_id,omitempty"`
	Notes            string    `json:"notes"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, a *Asset) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO it_assets (id, name, type, status, hostname, ip_address, mac_address,
		  manufacturer, model, serial_no, location, os, assigned_to, infrastructure_id, notes, created_by)
		VALUES (gen_random_uuid(), $1, $2, 'active', $3, $4, '', $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at`,
		a.Name, a.Type, a.Hostname, a.IPAddress,
		a.Manufacturer, a.Model, a.SerialNo, a.Location, a.OS,
		a.AssignedTo, a.InfrastructureID, a.Notes, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return err
	}
	if a.InfrastructureID != nil {
		_ = r.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id=$1`, *a.InfrastructureID).Scan(&a.InfraName)
	}
	return nil
}

// GetByID liefert ein einzelnes Asset inkl. Fälligkeits-/Garantiedatum,
// zugewiesenem Nutzer und Infrastruktur-Namen (für die Detailseite).
func (r *Repository) GetByID(ctx context.Context, id string) (*Asset, error) {
	a := &Asset{}
	var purchasedAt, warrantyUntil *time.Time
	err := r.db.QueryRow(ctx, `SELECT a.id, a.name, a.type, a.status,
		COALESCE(a.hostname,''), COALESCE(a.ip_address,''), COALESCE(a.mac_address,''),
		COALESCE(a.manufacturer,''), COALESCE(a.model,''), COALESCE(a.serial_no,''),
		COALESCE(a.location,''), COALESCE(a.os,''), a.purchased_at, a.warranty_until,
		a.assigned_to, a.infrastructure_id,
		COALESCE(a.notes,''), a.created_by, a.created_at, a.updated_at,
		COALESCE(u.first_name||' '||u.last_name,''), COALESCE(i.name,'')
		FROM it_assets a
		LEFT JOIN users u ON a.assigned_to = u.id
		LEFT JOIN infrastructure i ON a.infrastructure_id = i.id
		WHERE a.id=$1`, id).Scan(&a.ID, &a.Name, &a.Type, &a.Status,
		&a.Hostname, &a.IPAddress, &a.MACAddress,
		&a.Manufacturer, &a.Model, &a.SerialNo,
		&a.Location, &a.OS, &purchasedAt, &warrantyUntil,
		&a.AssignedTo, &a.InfrastructureID,
		&a.Notes, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.AssigneeName, &a.InfraName)
	if err != nil {
		return nil, err
	}
	if purchasedAt != nil {
		s := purchasedAt.Format("2006-01-02")
		a.PurchasedAt = &s
	}
	if warrantyUntil != nil {
		s := warrantyUntil.Format("2006-01-02")
		a.WarrantyUntil = &s
	}
	return a, nil
}

// UpdateDetailsInput fasst die auf der Detailseite bearbeitbaren Felder
// zusammen. Wird immer komplett aus dem vorausgefüllten Formular gesendet,
// deshalb hier bewusst kein partielles COALESCE-Update wie bei den
// gezielten Einzelfeld-Endpunkten andernorts.
type UpdateDetailsInput struct {
	Name             string
	Type             AssetType
	Hostname         string
	IPAddress        string
	MACAddress       string
	Manufacturer     string
	Model            string
	SerialNo         string
	Location         string
	OS               string
	PurchasedAt      *string
	WarrantyUntil    *string
	AssignedTo       *string
	InfrastructureID *string
	Notes            string
}

func (r *Repository) UpdateDetails(ctx context.Context, id string, in *UpdateDetailsInput) error {
	_, err := r.db.Exec(ctx, `
		UPDATE it_assets SET
			name=$1, type=$2, hostname=$3, ip_address=$4, mac_address=$5,
			manufacturer=$6, model=$7, serial_no=$8, location=$9, os=$10,
			purchased_at=$11, warranty_until=$12, assigned_to=$13, infrastructure_id=$14,
			notes=$15, updated_at=NOW()
		WHERE id=$16`,
		in.Name, in.Type, in.Hostname, in.IPAddress, in.MACAddress,
		in.Manufacturer, in.Model, in.SerialNo, in.Location, in.OS,
		in.PurchasedAt, in.WarrantyUntil, in.AssignedTo, in.InfrastructureID,
		in.Notes, id)
	return err
}

func (r *Repository) List(ctx context.Context, assetType AssetType, status AssetStatus) ([]*Asset, error) {
	query := `SELECT a.id, a.name, a.type, a.status,
		COALESCE(a.hostname,''), COALESCE(a.ip_address,''), COALESCE(a.mac_address,''),
		COALESCE(a.manufacturer,''), COALESCE(a.model,''), COALESCE(a.serial_no,''),
		COALESCE(a.location,''), COALESCE(a.os,''), a.assigned_to, a.infrastructure_id,
		COALESCE(a.notes,''), a.created_by, a.created_at, a.updated_at,
		COALESCE(u.first_name||' '||u.last_name,''), COALESCE(i.name,'')
		FROM it_assets a
		LEFT JOIN users u ON a.assigned_to = u.id
		LEFT JOIN infrastructure i ON a.infrastructure_id = i.id
		WHERE 1=1`
	args := []interface{}{}
	n := 1
	if assetType != "" {
		query += ` AND a.type=$` + string(rune('0'+n))
		args = append(args, assetType)
		n++
	}
	if status != "" {
		query += ` AND a.status=$` + string(rune('0'+n))
		args = append(args, status)
	}
	query += " ORDER BY a.type, a.name"
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Asset
	for rows.Next() {
		a := &Asset{}
		rows.Scan(&a.ID, &a.Name, &a.Type, &a.Status,
			&a.Hostname, &a.IPAddress, &a.MACAddress,
			&a.Manufacturer, &a.Model, &a.SerialNo,
			&a.Location, &a.OS, &a.AssignedTo, &a.InfrastructureID,
			&a.Notes, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.AssigneeName, &a.InfraName)
		list = append(list, a)
	}
	return list, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status AssetStatus) error {
	_, err := r.db.Exec(ctx, `UPDATE it_assets SET status=$1, updated_at=NOW() WHERE id=$2`, status, id)
	return err
}

func (r *Repository) GetStats(ctx context.Context) (map[string]int, error) {
	var total, active, servers, network int
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM it_assets`).Scan(&total)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM it_assets WHERE status='active'`).Scan(&active)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM it_assets WHERE type='server'`).Scan(&servers)
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM it_assets WHERE type='network'`).Scan(&network)
	return map[string]int{"total": total, "active": active, "servers": servers, "network": network}, nil
}

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }
func (s *Service) Create(ctx context.Context, in *CreateAssetInput, userID string) (*Asset, error) {
	a := &Asset{Name: in.Name, Type: in.Type, Hostname: in.Hostname,
		IPAddress: in.IPAddress, Manufacturer: in.Manufacturer, Model: in.Model,
		SerialNo: in.SerialNo, Location: in.Location, OS: in.OS,
		AssignedTo: in.AssignedTo, InfrastructureID: in.InfrastructureID, Notes: in.Notes, CreatedBy: userID}
	return a, s.repo.Create(ctx, a)
}
func (s *Service) List(ctx context.Context, t AssetType, st AssetStatus) ([]*Asset, error) {
	return s.repo.List(ctx, t, st)
}
func (s *Service) GetByID(ctx context.Context, id string) (*Asset, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *Service) UpdateDetails(ctx context.Context, id string, in *UpdateDetailsInput) error {
	return s.repo.UpdateDetails(ctx, id, in)
}
func (s *Service) UpdateStatus(ctx context.Context, id string, status AssetStatus) error {
	return s.repo.UpdateStatus(ctx, id, status)
}
func (s *Service) GetStats(ctx context.Context) (map[string]int, error) { return s.repo.GetStats(ctx) }

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/stats", h.Stats)
	r.Put("/{id}/status", h.UpdateStatus)
	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context(), AssetType(r.URL.Query().Get("type")), AssetStatus(r.URL.Query().Get("status")))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, list)
}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateAssetInput
	json.NewDecoder(r.Body).Decode(&in)
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	a, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, a)
}
func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status AssetStatus `json:"status"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	h.svc.UpdateStatus(r.Context(), chi.URLParam(r, "id"), in.Status)
	response.JSON(w, 200, map[string]string{"status": string(in.Status)})
}
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, _ := h.svc.GetStats(r.Context())
	response.JSON(w, 200, stats)
}
