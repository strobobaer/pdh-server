// Package directory verwaltet das systemweite Hersteller-/
// Lieferantenverzeichnis (business_partners) - ein Eintrag kann
// Hersteller, Lieferant oder beides sein. Ersatzteile, Infrastruktur und
// IT-Geraete koennen einen Eintrag ueber manufacturer_id (bzw.
// supplier_id / service_partner_id) referenzieren.
package directory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Modelle ──────────────────────────────────────────────────

type Kind string

const (
	KindManufacturer Kind = "manufacturer"
	KindSupplier     Kind = "supplier"
	KindBoth         Kind = "both"
)

// Freigabestatus eines Partners (Lieferantenfreigabe).
const (
	StatusApproved    = "approved"
	StatusConditional = "conditional"
	StatusNew         = "new"
	StatusBlocked     = "blocked"
)

// Fields sind die pflegbaren Stammdaten eines Partners. Datumswerte als
// "YYYY-MM-DD" (leer = nicht gesetzt).
type Fields struct {
	// Allgemein
	Name           string `json:"name"`
	ShortName      string `json:"short_name"`
	PartnerNo      string `json:"partner_no"`
	Kind           Kind   `json:"kind"`
	Category       string `json:"category"`
	ApprovalStatus string `json:"approval_status"`
	Rating         string `json:"rating"`

	// Kontakt (Zentrale)
	ContactName     string `json:"contact_name"`
	Email           string `json:"email"`
	OrderEmail      string `json:"order_email"`
	Phone           string `json:"phone"`
	Fax             string `json:"fax"`
	ServicePhone    string `json:"service_phone"`
	ServiceEmail    string `json:"service_email"`
	EmergencyPhone  string `json:"emergency_phone"`
	SupportHotline  string `json:"support_hotline"`
	SupportHours    string `json:"support_hours"`
	SupportPhone    string `json:"support_phone"`
	SupportEmail    string `json:"support_email"`
	SparePartsPhone string `json:"spare_parts_phone"`
	SparePartsEmail string `json:"spare_parts_email"`
	Website         string `json:"website"`
	PortalURL       string `json:"portal_url"`

	// Anschrift
	Address    string `json:"address"` // Altbestand (Freitext)
	Street     string `json:"street"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Country    string `json:"country"`

	// Kaufmaennisch
	VatID              string   `json:"vat_id"`
	TaxNo              string   `json:"tax_no"`
	CommercialRegister string   `json:"commercial_register"`
	CreditorNo         string   `json:"creditor_no"`
	CustomerNo         string   `json:"customer_no"`
	IBAN               string   `json:"iban"`
	BIC                string   `json:"bic"`
	BankName           string   `json:"bank_name"`
	PaymentTerms       string   `json:"payment_terms"`
	DeliveryTerms      string   `json:"delivery_terms"`
	Currency           string   `json:"currency"`
	MinOrderValue      *float64 `json:"min_order_value"`
	LeadTimeDays       *int     `json:"lead_time_days"`

	// Vertrag & Qualitaet
	ContractNo         string `json:"contract_no"`
	ContractValidUntil string `json:"contract_valid_until"`
	Certificates       string `json:"certificates"`
	LastEvaluationAt   string `json:"last_evaluation_at"`

	Notes string `json:"notes"`
}

type Partner struct {
	ID string `json:"id"`
	Fields
	Active    bool   `json:"active"`
	CreatedBy string `json:"created_by"`
}

type CreateInput = Fields
type UpdateInput = Fields

var ErrInvalid = errors.New("ungültige eingabe")

// Normalize bereinigt Eingaben und prueft Pflichtfelder/Wertebereiche.
func (f *Fields) Normalize() error {
	trim := func(ps ...*string) {
		for _, p := range ps {
			*p = strings.TrimSpace(*p)
		}
	}
	trim(&f.Name, &f.ShortName, &f.PartnerNo, &f.Category, &f.ApprovalStatus, &f.Rating,
		&f.ContactName, &f.Email, &f.OrderEmail, &f.Phone, &f.Fax, &f.ServicePhone, &f.ServiceEmail,
		&f.EmergencyPhone, &f.SupportHotline, &f.SupportHours, &f.SupportPhone, &f.SupportEmail,
		&f.SparePartsPhone, &f.SparePartsEmail, &f.Website, &f.PortalURL, &f.Address, &f.Street, &f.PostalCode, &f.City,
		&f.Country, &f.VatID, &f.TaxNo, &f.CommercialRegister, &f.CreditorNo, &f.CustomerNo,
		&f.IBAN, &f.BIC, &f.BankName, &f.PaymentTerms, &f.DeliveryTerms, &f.Currency,
		&f.ContractNo, &f.ContractValidUntil, &f.Certificates, &f.LastEvaluationAt, &f.Notes)
	if f.Name == "" {
		return errors.New("name ist pflicht")
	}
	switch f.Kind {
	case KindManufacturer, KindSupplier, KindBoth:
	case "":
		f.Kind = KindManufacturer
	default:
		return errors.New("ungültige art")
	}
	switch f.ApprovalStatus {
	case StatusApproved, StatusConditional, StatusNew, StatusBlocked:
	case "":
		f.ApprovalStatus = StatusApproved
	default:
		return errors.New("ungültiger freigabestatus")
	}
	f.Rating = strings.ToUpper(f.Rating)
	if f.Rating != "" && f.Rating != "A" && f.Rating != "B" && f.Rating != "C" {
		return errors.New("bewertung muss A, B oder C sein")
	}
	f.Website = normalizeURL(f.Website)
	f.PortalURL = normalizeURL(f.PortalURL)
	f.IBAN = strings.ToUpper(strings.ReplaceAll(f.IBAN, " ", ""))
	f.BIC = strings.ToUpper(strings.ReplaceAll(f.BIC, " ", ""))
	f.VatID = strings.ToUpper(strings.ReplaceAll(f.VatID, " ", ""))
	f.Currency = strings.ToUpper(f.Currency)
	if f.Currency == "" {
		f.Currency = "EUR"
	}
	if len(f.Currency) != 3 {
		return errors.New("währung muss ein 3-stelliger ISO-Code sein")
	}
	if f.LeadTimeDays != nil && *f.LeadTimeDays < 0 {
		return errors.New("lieferzeit darf nicht negativ sein")
	}
	if f.MinOrderValue != nil && *f.MinOrderValue < 0 {
		return errors.New("mindestbestellwert darf nicht negativ sein")
	}
	return nil
}

func normalizeURL(u string) string {
	if u == "" || strings.Contains(u, "://") {
		return u
	}
	return "https://" + u
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

const partnerColumns = `id, name, short_name, partner_no, kind, category, approval_status, rating,
	contact_name, email, order_email, phone, fax, service_phone, service_email, emergency_phone, support_hotline, support_hours, support_phone, support_email,
	spare_parts_phone, spare_parts_email, website, portal_url,
	address, street, postal_code, city, country,
	vat_id, tax_no, commercial_register, creditor_no, customer_no, iban, bic, bank_name,
	payment_terms, delivery_terms, currency, min_order_value::float8, lead_time_days,
	contract_no, COALESCE(to_char(contract_valid_until, 'YYYY-MM-DD'), ''), certificates,
	COALESCE(to_char(last_evaluation_at, 'YYYY-MM-DD'), ''), notes, active, COALESCE(created_by::text, '')`

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanPartner(row scanner) (*Partner, error) {
	p := &Partner{}
	err := row.Scan(&p.ID, &p.Name, &p.ShortName, &p.PartnerNo, &p.Kind, &p.Category, &p.ApprovalStatus, &p.Rating,
		&p.ContactName, &p.Email, &p.OrderEmail, &p.Phone, &p.Fax, &p.ServicePhone, &p.ServiceEmail, &p.EmergencyPhone,
		&p.SupportHotline, &p.SupportHours, &p.SupportPhone, &p.SupportEmail, &p.SparePartsPhone, &p.SparePartsEmail,
		&p.Website, &p.PortalURL, &p.Address, &p.Street, &p.PostalCode, &p.City, &p.Country,
		&p.VatID, &p.TaxNo, &p.CommercialRegister, &p.CreditorNo, &p.CustomerNo, &p.IBAN, &p.BIC, &p.BankName,
		&p.PaymentTerms, &p.DeliveryTerms, &p.Currency, &p.MinOrderValue, &p.LeadTimeDays,
		&p.ContractNo, &p.ContractValidUntil, &p.Certificates, &p.LastEvaluationAt, &p.Notes, &p.Active, &p.CreatedBy)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// fieldArgs liefert die Stammdaten in Spaltenreihenfolge von fieldSQL.
func fieldArgs(f *Fields) []interface{} {
	return []interface{}{
		f.Name, f.ShortName, f.Kind, f.Category, f.ApprovalStatus, f.Rating,
		f.ContactName, f.Email, f.OrderEmail, f.Phone, f.Fax, f.ServicePhone, f.ServiceEmail, f.EmergencyPhone,
		f.SupportHotline, f.SupportHours, f.SupportPhone, f.SupportEmail, f.SparePartsPhone, f.SparePartsEmail,
		f.Website, f.PortalURL, f.Address, f.Street, f.PostalCode, f.City, f.Country,
		f.VatID, f.TaxNo, f.CommercialRegister, f.CreditorNo, f.CustomerNo, f.IBAN, f.BIC, f.BankName,
		f.PaymentTerms, f.DeliveryTerms, f.Currency, f.MinOrderValue, f.LeadTimeDays,
		f.ContractNo, f.ContractValidUntil, f.Certificates, f.LastEvaluationAt, f.Notes,
	}
}

// fieldCount = len(fieldArgs(...)); Parameter $1..$45.
const fieldCount = 45

const fieldNames = `name, short_name, kind, category, approval_status, rating,
	contact_name, email, order_email, phone, fax, service_phone, service_email, emergency_phone,
	support_hotline, support_hours, support_phone, support_email, spare_parts_phone, spare_parts_email,
	website, portal_url, address, street, postal_code, city, country,
	vat_id, tax_no, commercial_register, creditor_no, customer_no, iban, bic, bank_name,
	payment_terms, delivery_terms, currency, min_order_value, lead_time_days,
	contract_no, contract_valid_until, certificates, last_evaluation_at, notes`

// fieldValue liefert den Platzhalter fuer Feld i (1-basiert) - Datumsfelder
// werden aus Leerstrings zu NULL.
func fieldValue(i int) string {
	p := "$" + itoa(i)
	switch i {
	case 42, 44: // contract_valid_until, last_evaluation_at
		return "NULLIF(" + p + "::text, '')::date"
	}
	return p
}

func (r *Repository) Create(ctx context.Context, p *Partner) error {
	values := make([]string, fieldCount)
	for i := range values {
		values[i] = fieldValue(i + 1)
	}
	args := append(fieldArgs(&p.Fields), p.PartnerNo, nullIfEmpty(p.CreatedBy))
	return r.db.QueryRow(ctx, `
		INSERT INTO business_partners (`+fieldNames+`, partner_no, created_by)
		VALUES (`+strings.Join(values, ", ")+`,
		        COALESCE(NULLIF($46::text, ''), 'P-' || lpad(nextval('business_partner_no_seq')::text, 5, '0')),
		        $47)
		RETURNING id, partner_no, active`, args...,
	).Scan(&p.ID, &p.PartnerNo, &p.Active)
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Partner, error) {
	return scanPartner(r.db.QueryRow(ctx, `SELECT `+partnerColumns+` FROM business_partners WHERE id=$1`, id))
}

func (r *Repository) List(ctx context.Context, includeInactive bool, kind Kind) ([]*Partner, error) {
	query := `SELECT ` + partnerColumns + ` FROM business_partners WHERE 1=1`
	args := []interface{}{}
	if !includeInactive {
		query += " AND active=true"
	}
	if kind != "" {
		args = append(args, kind)
		if kind == KindManufacturer || kind == KindSupplier {
			query += " AND (kind=$1 OR kind='both')"
		} else {
			query += " AND kind=$1"
		}
	}
	query += " ORDER BY name"
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Partner
	for rows.Next() {
		p, err := scanPartner(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func itoa(n int) string {
	digits := "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return itoa(n/10) + string(digits[n%10])
}

func (r *Repository) Update(ctx context.Context, id string, in *UpdateInput) error {
	names := strings.Split(strings.Join(strings.Fields(fieldNames), " "), ",")
	sets := make([]string, len(names))
	for i, n := range names {
		sets[i] = strings.TrimSpace(n) + "=" + fieldValue(i+1)
	}
	args := append(fieldArgs(in), in.PartnerNo, id)
	_, err := r.db.Exec(ctx, `
		UPDATE business_partners SET `+strings.Join(sets, ", ")+`,
		       partner_no = CASE WHEN NULLIF($46::text, '') IS NULL THEN partner_no ELSE $46::text END,
		       updated_at = NOW()
		WHERE id = $47`, args...)
	return err
}

func (r *Repository) SetActive(ctx context.Context, id string, active bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE business_partners SET active=$2, updated_at=NOW() WHERE id=$1`, id, active)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	return r.SetActive(ctx, id, false)
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in *CreateInput, userID string) (*Partner, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	p := &Partner{Fields: *in, CreatedBy: userID}
	return p, s.repo.Create(ctx, p)
}
func (s *Service) GetByID(ctx context.Context, id string) (*Partner, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *Service) List(ctx context.Context, includeInactive bool, kind Kind) ([]*Partner, error) {
	return s.repo.List(ctx, includeInactive, kind)
}
func (s *Service) Update(ctx context.Context, id string, in *UpdateInput) error {
	if err := in.Normalize(); err != nil {
		return err
	}
	return s.repo.Update(ctx, id, in)
}
func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	return s.repo.SetActive(ctx, id, active)
}
func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

// ── Handler (JSON API unter /api/v1/directory) ────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}", h.Update) // POST statt PUT (Cloudflare/Nginx blockiert PUT)
	r.Delete("/{id}", h.Deactivate)
	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	includeInactive := r.URL.Query().Get("all") == "true"
	kind := Kind(r.URL.Query().Get("kind"))
	items, err := h.svc.List(r.Context(), includeInactive, kind)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, items)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, ErrInvalid.Error())
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	p, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, p)
}

// Update ist ein Teil-Update: nicht mitgeschickte Felder behalten ihren
// bisherigen Wert (aeltere Clients kennen nur Name/Art/Kontakt).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	in := existing.Fields
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, ErrInvalid.Error())
		return
	}
	if err := h.svc.Update(r.Context(), id, &in); err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "aktualisiert"})
}

func (h *Handler) Deactivate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Deactivate(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "deaktiviert"})
}
