package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/internal/core/directory"
)

// Hersteller-/Lieferanten-Coreboard: Uebersicht aller Partner mit
// Kennzahlen, Detailseite mit vollstaendigen Stammdaten, Ansprechpartnern
// und allen Verknuepfungen (Anlagen, Ersatzteile, IT, Vorgaenge,
// Dokumente) sowie Zuweisungen in beide Richtungen. Verknuepfte
// Datensaetze oeffnen sich per data-frame im eingebetteten Viewer.

func (h *Handler) partnerService() *directory.Service {
	return directory.NewService(directory.NewRepository(h.db))
}

func (h *Handler) canViewPartners(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "directory.view") || h.canEditPartners(r)
}

func (h *Handler) canEditPartners(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "directory.edit")
}

var partnerKindLabels = map[string]string{
	"manufacturer": "Hersteller", "supplier": "Lieferant", "both": "Hersteller & Lieferant",
}

var partnerStatusLabels = map[string]string{
	directory.StatusApproved: "Freigegeben", directory.StatusConditional: "Bedingt freigegeben",
	directory.StatusNew: "Neu / in Prüfung", directory.StatusBlocked: "Gesperrt",
}

var partnerStatusClasses = map[string]string{
	directory.StatusApproved: "b-green", directory.StatusConditional: "b-amber",
	directory.StatusNew: "b-blue", directory.StatusBlocked: "b-red",
}

// Rollen einer Partner-Zuweisung je Zielmodul.
var partnerRoleLabels = map[string]string{
	"manufacturer": "Hersteller", "supplier": "Lieferant", "service": "Servicepartner",
	"responsible": "Verantwortlich", "assigned": "Zuständig",
}

const partnerContractWarnDays = 90

// ── Board ────────────────────────────────────────────────────

type PartnerRow struct {
	ID, PartnerNo, Name, ShortName, Kind, KindLabel, Category string
	StatusLabel, StatusClass, Rating, City, Phone, Email      string
	PrimaryContact, ContractUntil                             string
	ContractState                                             string // "" | soon | expired
	Active                                                    bool
	InfraCount, PartCount, ITCount, RecordCount               int
}

type partnerBoardFilter struct {
	Query, Kind, Status, Category string
}

type PartnerBoardData struct {
	BaseData
	Filter          partnerBoardFilter
	Rows            []PartnerRow
	Categories      []string
	CanEdit         bool
	Total           int
	Manufacturers   int
	Suppliers       int
	Blocked         int
	ContractsSoon   int
	ContractsDue    int
	WithoutContacts int
	Message         string
	Error           string
}

func (h *Handler) DirectoryPage(w http.ResponseWriter, r *http.Request) {
	if !h.canViewPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	data := PartnerBoardData{
		BaseData: h.baseData(r, "directory", "Hersteller & Lieferanten", "Partner"),
		Filter: partnerBoardFilter{
			Query: strings.TrimSpace(q.Get("q")), Kind: q.Get("kind"),
			Status: q.Get("status"), Category: q.Get("category"),
		},
		CanEdit: h.canEditPartners(r),
		Message: q.Get("msg"),
		Error:   q.Get("err"),
	}

	// Kennzahlen ueber alle aktiven Partner (unabhaengig vom Filter)
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE kind IN ('manufacturer', 'both')),
		       COUNT(*) FILTER (WHERE kind IN ('supplier', 'both')),
		       COUNT(*) FILTER (WHERE approval_status = 'blocked'),
		       COUNT(*) FILTER (WHERE contract_valid_until >= CURRENT_DATE AND contract_valid_until < CURRENT_DATE + $1::int),
		       COUNT(*) FILTER (WHERE contract_valid_until < CURRENT_DATE),
		       COUNT(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM business_partner_contacts c WHERE c.partner_id = bp.id))
		FROM business_partners bp WHERE active`, partnerContractWarnDays).
		Scan(&data.Total, &data.Manufacturers, &data.Suppliers, &data.Blocked, &data.ContractsSoon, &data.ContractsDue, &data.WithoutContacts)

	if rows, err := h.db.Query(ctx, `SELECT DISTINCT category FROM business_partners WHERE category <> '' ORDER BY category`); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				data.Categories = append(data.Categories, c)
			}
		}
		rows.Close()
	}

	where := []string{"1=1"}
	args := []interface{}{partnerContractWarnDays}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	f := data.Filter
	if f.Query != "" {
		add(`(bp.name || ' ' || bp.short_name || ' ' || bp.partner_no || ' ' || bp.category || ' ' || bp.city || ' ' ||
		      bp.email || ' ' || bp.phone || ' ' || bp.creditor_no || ' ' || bp.customer_no || ' ' ||
		      COALESCE((SELECT string_agg(c.name || ' ' || c.email, ' ') FROM business_partner_contacts c WHERE c.partner_id = bp.id), '')
		     ) ILIKE $?`, likePattern(f.Query))
	}
	switch f.Kind {
	case "manufacturer", "supplier":
		add(`bp.kind IN ($?, 'both')`, f.Kind)
	}
	switch f.Status {
	case "inactive":
		where = append(where, "NOT bp.active")
	case directory.StatusApproved, directory.StatusConditional, directory.StatusNew, directory.StatusBlocked:
		add(`bp.active AND bp.approval_status = $?`, f.Status)
	case "contract":
		where = append(where, "bp.active AND bp.contract_valid_until < CURRENT_DATE + $1::int")
	default:
		data.Filter.Status = ""
		where = append(where, "bp.active")
	}
	if f.Category != "" {
		add(`bp.category = $?`, f.Category)
	}

	rows, err := h.db.Query(ctx, `
		SELECT bp.id::text, bp.partner_no, bp.name, bp.short_name, bp.kind, bp.category, bp.approval_status, bp.rating,
		       bp.city, bp.phone, bp.email, bp.active,
		       COALESCE(to_char(bp.contract_valid_until, 'DD.MM.YYYY'), ''),
		       CASE WHEN bp.contract_valid_until < CURRENT_DATE THEN 'expired'
		            WHEN bp.contract_valid_until < CURRENT_DATE + $1::int THEN 'soon' ELSE '' END,
		       COALESCE((SELECT c.name FROM business_partner_contacts c WHERE c.partner_id = bp.id
		                 ORDER BY c.is_primary DESC, c.created_at LIMIT 1), bp.contact_name),
		       (SELECT COUNT(*) FROM infrastructure i WHERE bp.id IN (i.manufacturer_id, i.supplier_id, i.service_partner_id)),
		       (SELECT COUNT(*) FROM spare_parts sp WHERE sp.active AND (sp.manufacturer_id = bp.id
		            OR EXISTS (SELECT 1 FROM spare_part_suppliers s WHERE s.part_id = sp.id AND s.partner_id = bp.id))),
		       (SELECT COUNT(*) FROM it_assets a WHERE bp.id IN (a.manufacturer_id, a.supplier_id)),
		       (SELECT COUNT(DISTINCT rp.ref_id) FROM record_external_parties rp WHERE rp.partner_id = bp.id)
		FROM business_partners bp
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY bp.name`, args...)
	if err != nil {
		data.Error = "Laden fehlgeschlagen: " + err.Error()
	} else {
		for rows.Next() {
			var p PartnerRow
			var status string
			if err := rows.Scan(&p.ID, &p.PartnerNo, &p.Name, &p.ShortName, &p.Kind, &p.Category, &status, &p.Rating,
				&p.City, &p.Phone, &p.Email, &p.Active, &p.ContractUntil, &p.ContractState, &p.PrimaryContact,
				&p.InfraCount, &p.PartCount, &p.ITCount, &p.RecordCount); err != nil {
				continue
			}
			p.KindLabel = partnerKindLabels[p.Kind]
			p.StatusLabel, p.StatusClass = partnerStatusLabels[status], partnerStatusClasses[status]
			data.Rows = append(data.Rows, p)
		}
		rows.Close()
	}
	h.render(w, "directory", data)
}

// ── Detail ───────────────────────────────────────────────────

type PartnerContactView struct {
	ID, Name, Position, Department, Phone, Mobile, Email, Notes string
	IsPrimary                                                   bool
}

type PartnerInfraRow struct {
	ID, Name, TypeLabel, Location string
	Roles                         []string
	OpenFaults                    int
}

type PartnerPartRow struct {
	ID, PartNumber, Name, Unit, Stock, Price string
	IsManufacturer, IsSupplier               bool
	SupplierPartNo, SupplierPrice            string
	MinOrderQty, LeadTime, Notes             string
	Preferred                                bool
}

type PartnerITRow struct {
	ID, Name, TypeLabel, Model, SerialNo, Status string
	Roles                                        []string
}

type PartnerRecordRow struct {
	PartyID, RefType, ModuleLabel, Title, URL string
	RoleLabel, StatusLabel, StatusClass, Date string
}

type partnerOption struct{ Value, Label string }

type partnerOptionGroup struct {
	Label   string
	Options []partnerOption
}

type PartnerDetailData struct {
	BaseData
	IsNew          bool
	CanEdit        bool
	Tab            string
	Message        string
	Error          string
	Partner        directory.Partner
	KindLabel      string
	StatusLabel    string
	StatusClass    string
	ContractState  string
	MinOrderValue  string
	LeadTimeDays   string
	Contacts       []PartnerContactView
	Infra          []PartnerInfraRow
	Parts          []PartnerPartRow
	IT             []PartnerITRow
	Records        []PartnerRecordRow
	InfraFaults    []PartnerRecordRow
	PartOptions    []partnerOption
	ITOptions      []partnerOption
	RecordOptions  []partnerOptionGroup
	PartSupplyCost string
	Links          []PartnerLinkView
	LinkKinds      interface{}
	CanCredentials bool
	History        []HistoryView
}

func (h *Handler) PartnerNewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	data := PartnerDetailData{
		BaseData: h.baseData(r, "directory", "Neuer Partner", "Partner"),
		IsNew:    true, CanEdit: true, Tab: "master",
		LinkKinds: partnerLinkKinds,
		Error:     r.URL.Query().Get("err"),
	}
	data.Partner.Kind = directory.Kind(r.URL.Query().Get("kind"))
	if data.Partner.Kind == "" {
		data.Partner.Kind = directory.KindManufacturer
	}
	data.Partner.ApprovalStatus = directory.StatusNew
	data.Partner.Currency = "EUR"
	data.Partner.Country = "Deutschland"
	h.render(w, "partner_detail", data)
}

func (h *Handler) PartnerDetail(w http.ResponseWriter, r *http.Request) {
	if !h.canViewPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	p, err := h.partnerService().GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/directory?err="+url.QueryEscape("Partner nicht gefunden"), http.StatusFound)
		return
	}
	q := r.URL.Query()
	data := PartnerDetailData{
		BaseData:    h.baseData(r, "directory", p.Name, "Partner"),
		CanEdit:     h.canEditPartners(r),
		Tab:         q.Get("tab"),
		Message:     q.Get("msg"),
		Error:       q.Get("err"),
		Partner:     *p,
		KindLabel:   partnerKindLabels[string(p.Kind)],
		StatusLabel: partnerStatusLabels[p.ApprovalStatus],
		StatusClass: partnerStatusClasses[p.ApprovalStatus],
	}
	if data.Tab == "" {
		data.Tab = "overview"
	}
	if p.MinOrderValue != nil {
		data.MinOrderValue = formatEuro(*p.MinOrderValue)
	}
	if p.LeadTimeDays != nil {
		data.LeadTimeDays = strconv.Itoa(*p.LeadTimeDays)
	}
	if p.ContractValidUntil != "" {
		if t, err := time.Parse("2006-01-02", p.ContractValidUntil); err == nil {
			today := time.Now().Truncate(24 * time.Hour)
			switch {
			case t.Before(today):
				data.ContractState = "expired"
			case t.Before(today.AddDate(0, 0, partnerContractWarnDays)):
				data.ContractState = "soon"
			}
		}
	}
	data.Contacts = h.partnerContacts(ctx, id)
	data.Infra = h.partnerInfra(ctx, id)
	data.Parts, data.PartSupplyCost = h.partnerParts(ctx, id)
	data.IT = h.partnerIT(ctx, id)
	data.Records = h.partnerRecords(ctx, id)
	data.InfraFaults = h.partnerInfraFaults(ctx, id)
	data.Links = h.partnerLinks(ctx, id)
	data.LinkKinds = partnerLinkKinds
	data.CanCredentials = h.canPartnerCredentials(r)
	data.History = h.recordHistory(ctx, "business_partner", id)
	if data.CanEdit {
		data.PartOptions = h.partnerSimpleOptions(ctx, `SELECT id::text, part_number || ' · ' || name FROM spare_parts WHERE active ORDER BY name LIMIT 3000`)
		data.ITOptions = h.partnerSimpleOptions(ctx, `SELECT id::text, name || COALESCE(' · ' || NULLIF(model, ''), '') FROM it_assets ORDER BY name LIMIT 3000`)
		data.RecordOptions = h.partnerRecordOptions(ctx)
	}
	h.render(w, "partner_detail", data)
}

func (h *Handler) partnerContacts(ctx context.Context, partnerID string) []PartnerContactView {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, name, position, department, phone, mobile, email, notes, is_primary
		FROM business_partner_contacts WHERE partner_id = $1
		ORDER BY is_primary DESC, name`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerContactView
	for rows.Next() {
		var c PartnerContactView
		if rows.Scan(&c.ID, &c.Name, &c.Position, &c.Department, &c.Phone, &c.Mobile, &c.Email, &c.Notes, &c.IsPrimary) == nil {
			list = append(list, c)
		}
	}
	return list
}

var infraTypeLabels = map[string]string{"building": "Gebäude", "line": "Linie", "plant": "Anlage", "device": "Gerät"}

func (h *Handler) partnerInfra(ctx context.Context, partnerID string) []PartnerInfraRow {
	rows, err := h.db.Query(ctx, `
		SELECT i.id::text, i.name, i.type::text, COALESCE(i.location, ''),
		       COALESCE(i.manufacturer_id = $1, false), COALESCE(i.supplier_id = $1, false),
		       COALESCE(i.service_partner_id = $1, false),
		       (SELECT COUNT(*) FROM faults f WHERE f.infrastructure_id = i.id AND f.status NOT IN ('resolved', 'closed'))
		FROM infrastructure i
		WHERE $1::uuid IN (i.manufacturer_id, i.supplier_id, i.service_partner_id)
		ORDER BY i.name`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerInfraRow
	for rows.Next() {
		var row PartnerInfraRow
		var typ string
		var isM, isS, isSvc bool
		if rows.Scan(&row.ID, &row.Name, &typ, &row.Location, &isM, &isS, &isSvc, &row.OpenFaults) != nil {
			continue
		}
		row.TypeLabel = infraTypeLabels[typ]
		if isM {
			row.Roles = append(row.Roles, "manufacturer")
		}
		if isS {
			row.Roles = append(row.Roles, "supplier")
		}
		if isSvc {
			row.Roles = append(row.Roles, "service")
		}
		list = append(list, row)
	}
	return list
}

func optFloat(v *float64, f func(float64) string) string {
	if v == nil {
		return ""
	}
	return f(*v)
}

func (h *Handler) partnerParts(ctx context.Context, partnerID string) ([]PartnerPartRow, string) {
	rows, err := h.db.Query(ctx, `
		SELECT sp.id::text, sp.part_number, sp.name, sp.unit, sp.stock_qty::float8, sp.price::float8,
		       COALESCE(sp.manufacturer_id = $1, false), s.id IS NOT NULL,
		       COALESCE(s.supplier_part_no, ''), s.price::float8, s.min_order_qty::float8, s.lead_time_days,
		       COALESCE(s.preferred, false), COALESCE(s.notes, '')
		FROM spare_parts sp
		LEFT JOIN spare_part_suppliers s ON s.part_id = sp.id AND s.partner_id = $1
		WHERE sp.active AND (sp.manufacturer_id = $1 OR s.id IS NOT NULL)
		ORDER BY sp.name`, partnerID)
	if err != nil {
		return nil, ""
	}
	defer rows.Close()
	var list []PartnerPartRow
	var stockValue float64
	for rows.Next() {
		var p PartnerPartRow
		var stock, price float64
		var sPrice, minQty *float64
		var lead *int
		if rows.Scan(&p.ID, &p.PartNumber, &p.Name, &p.Unit, &stock, &price, &p.IsManufacturer, &p.IsSupplier,
			&p.SupplierPartNo, &sPrice, &minQty, &lead, &p.Preferred, &p.Notes) != nil {
			continue
		}
		p.Stock = formatQty(stock)
		p.Price = formatEuro(price)
		p.SupplierPrice = optFloat(sPrice, formatEuro)
		p.MinOrderQty = optFloat(minQty, formatQty)
		if lead != nil {
			p.LeadTime = strconv.Itoa(*lead) + " Tage"
		}
		stockValue += stock * price
		list = append(list, p)
	}
	return list, formatEuro(stockValue)
}

var itTypeLabels = map[string]string{
	"pc": "PC", "laptop": "Laptop", "server": "Server", "switch": "Switch", "router": "Router",
	"printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "plc": "SPS", "hmi": "HMI", "other": "Sonstiges",
}

func (h *Handler) partnerIT(ctx context.Context, partnerID string) []PartnerITRow {
	rows, err := h.db.Query(ctx, `
		SELECT a.id::text, a.name, a.type::text, COALESCE(a.model, ''), COALESCE(a.serial_no, ''), a.status::text,
		       COALESCE(a.manufacturer_id = $1, false), COALESCE(a.supplier_id = $1, false)
		FROM it_assets a WHERE $1::uuid IN (a.manufacturer_id, a.supplier_id)
		ORDER BY a.name`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerITRow
	for rows.Next() {
		var a PartnerITRow
		var typ string
		var isM, isS bool
		if rows.Scan(&a.ID, &a.Name, &typ, &a.Model, &a.SerialNo, &a.Status, &isM, &isS) != nil {
			continue
		}
		a.TypeLabel = itTypeLabels[typ]
		if a.TypeLabel == "" {
			a.TypeLabel = typ
		}
		if isM {
			a.Roles = append(a.Roles, "manufacturer")
		}
		if isS {
			a.Roles = append(a.Roles, "supplier")
		}
		list = append(list, a)
	}
	return list
}

// recordRefURL liefert Detail-URL und Modulname eines Vorgangs.
func recordRefURL(refType, id string) (string, string) {
	if def, ok := infraCommentRefTypes[refType]; ok {
		return def.URLPrefix + id, def.Label
	}
	return "", refType
}

func (h *Handler) partnerRecords(ctx context.Context, partnerID string) []PartnerRecordRow {
	rows, err := h.db.Query(ctx, `
		SELECT rp.id::text, rp.ref_type, rp.ref_id::text, rp.role, to_char(rp.created_at, 'DD.MM.YYYY'),
		       COALESCE(t.title, f.title, m.title, ta.title, p.name, '(gelöscht)'),
		       COALESCE(t.status::text, f.status::text, m.status::text, ta.status, p.status, '')
		FROM record_external_parties rp
		LEFT JOIN tickets t ON rp.ref_type = 'ticket' AND t.id = rp.ref_id
		LEFT JOIN faults f ON rp.ref_type = 'fault' AND f.id = rp.ref_id
		LEFT JOIN maintenance_tasks m ON rp.ref_type = 'maintenance_task' AND m.id = rp.ref_id
		LEFT JOIN tasks ta ON rp.ref_type = 'task' AND ta.id = rp.ref_id
		LEFT JOIN projects p ON rp.ref_type = 'project' AND p.id = rp.ref_id
		WHERE rp.partner_id = $1
		ORDER BY rp.created_at DESC LIMIT 300`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerRecordRow
	for rows.Next() {
		var rec PartnerRecordRow
		var refID, role, status string
		if rows.Scan(&rec.PartyID, &rec.RefType, &refID, &role, &rec.Date, &rec.Title, &status) != nil {
			continue
		}
		rec.URL, rec.ModuleLabel = recordRefURL(rec.RefType, refID)
		rec.RoleLabel = partnerRoleLabels[role]
		if status != "" {
			rec.StatusLabel, rec.StatusClass = statusLabel(status), statusClass(status)
		}
		list = append(list, rec)
	}
	return list
}

// partnerInfraFaults: Stoerungen an Anlagen, fuer die der Partner
// Hersteller/Lieferant/Service ist - Grundlage der Lieferantenbewertung.
func (h *Handler) partnerInfraFaults(ctx context.Context, partnerID string) []PartnerRecordRow {
	rows, err := h.db.Query(ctx, `
		SELECT f.id::text, f.title, f.status::text, to_char(f.created_at, 'DD.MM.YYYY'), i.name
		FROM faults f JOIN infrastructure i ON i.id = f.infrastructure_id
		WHERE $1::uuid IN (i.manufacturer_id, i.supplier_id, i.service_partner_id)
		ORDER BY f.created_at DESC LIMIT 50`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerRecordRow
	for rows.Next() {
		var rec PartnerRecordRow
		var id, status, infraName string
		if rows.Scan(&id, &rec.Title, &status, &rec.Date, &infraName) != nil {
			continue
		}
		rec.URL, rec.ModuleLabel = "/faults/"+id, "Störung"
		rec.RoleLabel = infraName
		rec.StatusLabel, rec.StatusClass = statusLabel(status), statusClass(status)
		list = append(list, rec)
	}
	return list
}

func (h *Handler) partnerSimpleOptions(ctx context.Context, query string) []partnerOption {
	rows, err := h.db.Query(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []partnerOption
	for rows.Next() {
		var o partnerOption
		if rows.Scan(&o.Value, &o.Label) == nil {
			list = append(list, o)
		}
	}
	return list
}

// partnerRecordOptions: offene Vorgaenge, denen der Partner als externe
// Firma zugewiesen werden kann (gruppiert nach Modul).
func (h *Handler) partnerRecordOptions(ctx context.Context) []partnerOptionGroup {
	rows, err := h.db.Query(ctx, `
		SELECT ref_type, id, title FROM (
			SELECT 'fault' AS ref_type, id::text AS id, title::text AS title, created_at FROM faults
			 WHERE status NOT IN ('resolved', 'closed') AND archived_at IS NULL
			UNION ALL SELECT 'ticket', id::text, title, created_at FROM tickets
			 WHERE status NOT IN ('resolved', 'closed') AND archived_at IS NULL
			UNION ALL SELECT 'maintenance_task', id::text, title, created_at FROM maintenance_tasks
			 WHERE status IN ('open', 'in_progress') AND archived_at IS NULL
			UNION ALL SELECT 'task', id::text, title, created_at FROM tasks
			 WHERE status NOT IN ('resolved', 'closed') AND archived_at IS NULL
			UNION ALL SELECT 'project', id::text, name, created_at FROM projects WHERE status <> 'completed'
		) x ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	order := []string{"fault", "ticket", "maintenance_task", "task", "project"}
	byType := map[string][]partnerOption{}
	for rows.Next() {
		var refType, id, title string
		if rows.Scan(&refType, &id, &title) == nil {
			byType[refType] = append(byType[refType], partnerOption{Value: refType + ":" + id, Label: title})
		}
	}
	var groups []partnerOptionGroup
	for _, t := range order {
		if len(byType[t]) > 0 {
			groups = append(groups, partnerOptionGroup{Label: infraCommentRefTypes[t].Label, Options: byType[t]})
		}
	}
	return groups
}

// ── Schreiben ────────────────────────────────────────────────

func parseOptFloat(s string) (*float64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "€", ""), " ", ""))
	if s == "" {
		return nil, nil
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("ungültige Zahl: %s", s)
	}
	return &v, nil
}

func parseOptInt(s string) (*int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("ungültige Ganzzahl: %s", s)
	}
	return &v, nil
}

func partnerFieldsFromForm(r *http.Request) (directory.Fields, error) {
	v := r.FormValue
	f := directory.Fields{
		Name: v("name"), ShortName: v("short_name"), PartnerNo: v("partner_no"),
		Kind: directory.Kind(v("kind")), Category: v("category"),
		ApprovalStatus: v("approval_status"), Rating: v("rating"),
		ContactName: v("contact_name"), Email: v("email"), OrderEmail: v("order_email"),
		Phone: v("phone"), Fax: v("fax"), ServicePhone: v("service_phone"), ServiceEmail: v("service_email"),
		EmergencyPhone: v("emergency_phone"),
		SupportHotline: v("support_hotline"), SupportHours: v("support_hours"),
		SupportPhone: v("support_phone"), SupportEmail: v("support_email"),
		SparePartsPhone: v("spare_parts_phone"), SparePartsEmail: v("spare_parts_email"),
		Website: v("website"), PortalURL: v("portal_url"),
		Address: v("address"), Street: v("street"), PostalCode: v("postal_code"), City: v("city"), Country: v("country"),
		VatID: v("vat_id"), TaxNo: v("tax_no"), CommercialRegister: v("commercial_register"),
		CreditorNo: v("creditor_no"), CustomerNo: v("customer_no"),
		IBAN: v("iban"), BIC: v("bic"), BankName: v("bank_name"),
		PaymentTerms: v("payment_terms"), DeliveryTerms: v("delivery_terms"), Currency: v("currency"),
		ContractNo: v("contract_no"), ContractValidUntil: v("contract_valid_until"),
		Certificates: v("certificates"), LastEvaluationAt: v("last_evaluation_at"), Notes: v("notes"),
	}
	var err error
	if f.MinOrderValue, err = parseOptFloat(v("min_order_value")); err != nil {
		return f, err
	}
	if f.LeadTimeDays, err = parseOptInt(v("lead_time_days")); err != nil {
		return f, err
	}
	for _, d := range []string{f.ContractValidUntil, f.LastEvaluationAt} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return f, fmt.Errorf("ungültiges Datum: %s", d)
			}
		}
	}
	return f, nil
}

func partnerRedirect(w http.ResponseWriter, r *http.Request, id, tab, msg string, err error) {
	target := "/directory/" + id + "?tab=" + url.QueryEscape(tab)
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyDBError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func friendlyDBError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "idx_business_partners_partner_no"):
		return "Partnernummer ist bereits vergeben"
	case strings.Contains(s, "violates foreign key"):
		return "Verknüpfter Datensatz existiert nicht (mehr)"
	case strings.Contains(s, "invalid input syntax for type uuid"):
		return "Bitte einen Eintrag auswählen"
	}
	return s
}

func (h *Handler) PartnerCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	in, err := partnerFieldsFromForm(r)
	if err == nil {
		var p *directory.Partner
		if p, err = h.partnerService().Create(r.Context(), &in, getUser(r).ID); err == nil {
			h.syncPrimaryContactFromPartner(r.Context(), p.ID, &in)
			partnerRedirect(w, r, p.ID, "overview", "Partner angelegt", nil)
			return
		}
	}
	http.Redirect(w, r, "/directory/new?err="+url.QueryEscape(friendlyDBError(err)), http.StatusSeeOther)
}

// syncPrimaryContactFromPartner legt beim Anlegen aus dem Feld
// "Kontaktperson" direkt einen Hauptansprechpartner an.
func (h *Handler) syncPrimaryContactFromPartner(ctx context.Context, partnerID string, f *directory.Fields) {
	if strings.TrimSpace(f.ContactName) == "" {
		return
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO business_partner_contacts (partner_id, name, phone, email, is_primary)
		SELECT $1::uuid, $2::text, $3::text, $4::text, true
		WHERE NOT EXISTS (SELECT 1 FROM business_partner_contacts WHERE partner_id = $1::uuid)`,
		partnerID, f.ContactName, f.Phone, f.Email)
}

func (h *Handler) PartnerUpdateWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	in, err := partnerFieldsFromForm(r)
	if err == nil {
		err = h.partnerService().Update(r.Context(), id, &in)
	}
	if err != nil {
		partnerRedirect(w, r, id, "master", "", err)
		return
	}
	_, _ = h.db.Exec(r.Context(), `INSERT INTO record_history (ref_type, ref_id, action, created_by, message)
		VALUES ('business_partner', $1, 'update', $2, 'Stammdaten geändert')`, id, nullID(getUser(r).ID))
	partnerRedirect(w, r, id, "overview", "Stammdaten gespeichert", nil)
}

func (h *Handler) PartnerActiveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	active := r.FormValue("active") == "1"
	err := h.partnerService().SetActive(r.Context(), id, active)
	msg := "Partner deaktiviert"
	if active {
		msg = "Partner reaktiviert"
	}
	partnerRedirect(w, r, id, "overview", msg, err)
}

// PartnerContactSaveWeb legt einen Ansprechpartner an oder aendert ihn
// (contact_id gesetzt). Der Hauptansprechpartner wird zusaetzlich in
// business_partners.contact_name gespiegelt (Picker/API-Altbestand).
func (h *Handler) PartnerContactSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	if v("name") == "" {
		partnerRedirect(w, r, id, "contacts", "", errors.New("Name des Ansprechpartners fehlt"))
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		partnerRedirect(w, r, id, "contacts", "", err)
		return
	}
	defer tx.Rollback(ctx)
	primary := r.FormValue("is_primary") == "on"
	var contactID string
	if cid := v("contact_id"); cid != "" {
		_, err = tx.Exec(ctx, `
			UPDATE business_partner_contacts SET name=$1, position=$2, department=$3, phone=$4, mobile=$5,
			       email=$6, notes=$7, is_primary=$8, updated_at=NOW()
			WHERE id=$9 AND partner_id=$10`,
			v("name"), v("position"), v("department"), v("phone"), v("mobile"), v("email"), v("notes"), primary, cid, id)
		contactID = cid
	} else {
		// Der erste Ansprechpartner wird automatisch Hauptansprechpartner.
		err = tx.QueryRow(ctx, `
			INSERT INTO business_partner_contacts (partner_id, name, position, department, phone, mobile, email, notes, is_primary, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
			        $9 OR NOT EXISTS (SELECT 1 FROM business_partner_contacts WHERE partner_id = $1), $10)
			RETURNING id::text`,
			id, v("name"), v("position"), v("department"), v("phone"), v("mobile"), v("email"), v("notes"), primary,
			nullID(getUser(r).ID)).Scan(&contactID)
	}
	if err == nil && primary {
		_, err = tx.Exec(ctx, `UPDATE business_partner_contacts SET is_primary = false WHERE partner_id = $1 AND id <> $2`, id, contactID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE business_partners SET contact_name = COALESCE(
			    (SELECT name FROM business_partner_contacts WHERE partner_id = $1 AND is_primary LIMIT 1), contact_name),
			    updated_at = NOW()
			WHERE id = $1`, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	partnerRedirect(w, r, id, "contacts", "Ansprechpartner gespeichert", err)
}

func (h *Handler) PartnerContactDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, cid := chi.URLParam(r, "id"), chi.URLParam(r, "contactId")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM business_partner_contacts WHERE id = $1 AND partner_id = $2`, cid, id)
	partnerRedirect(w, r, id, "contacts", "Ansprechpartner entfernt", err)
}

// partnerSupplyConditions sind die Lieferkonditionen einer
// Ersatzteil-Lieferanten-Zuordnung.
type partnerSupplyConditions struct {
	SupplierPartNo string
	Price          *float64
	MinOrderQty    *float64
	LeadTimeDays   *int
	Preferred      bool
	Notes          string
}

func supplyConditionsFromForm(r *http.Request) (partnerSupplyConditions, error) {
	c := partnerSupplyConditions{
		SupplierPartNo: strings.TrimSpace(r.FormValue("supplier_part_no")),
		Preferred:      r.FormValue("preferred") == "on",
		Notes:          strings.TrimSpace(r.FormValue("supply_notes")),
	}
	var err error
	if c.Price, err = parseOptFloat(r.FormValue("supply_price")); err != nil {
		return c, err
	}
	if c.MinOrderQty, err = parseOptFloat(r.FormValue("min_order_qty")); err != nil {
		return c, err
	}
	c.LeadTimeDays, err = parseOptInt(r.FormValue("supply_lead_time"))
	return c, err
}

// partnerAssignColumn liefert Tabelle/Spalte einer Stammdaten-Zuweisung.
func partnerAssignColumn(target, role string) (table, column string, ok bool) {
	switch target + ":" + role {
	case "infra:manufacturer":
		return "infrastructure", "manufacturer_id", true
	case "infra:supplier":
		return "infrastructure", "supplier_id", true
	case "infra:service":
		return "infrastructure", "service_partner_id", true
	case "it:manufacturer":
		return "it_assets", "manufacturer_id", true
	case "it:supplier":
		return "it_assets", "supplier_id", true
	case "part:manufacturer":
		return "spare_parts", "manufacturer_id", true
	}
	return "", "", false
}

// assignPartner verknuepft einen Partner mit einem Datensatz eines
// anderen Moduls (Anlage, IT-Geraet, Ersatzteil, Vorgang).
func (h *Handler) assignPartner(ctx context.Context, r *http.Request, partnerID, target, role, targetID string) error {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return errors.New("Bitte einen Eintrag auswählen")
	}
	if table, column, ok := partnerAssignColumn(target, role); ok {
		tag, err := h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1, updated_at = NOW() WHERE id = $2`, table, column), partnerID, targetID)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("Datensatz nicht gefunden")
		}
		return err
	}
	switch target + ":" + role {
	case "part:supplier":
		c, err := supplyConditionsFromForm(r)
		if err != nil {
			return err
		}
		tx, err := h.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `
			INSERT INTO spare_part_suppliers (part_id, partner_id, supplier_part_no, price, min_order_qty, lead_time_days, preferred, notes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (part_id, partner_id) DO UPDATE SET supplier_part_no = EXCLUDED.supplier_part_no,
			    price = EXCLUDED.price, min_order_qty = EXCLUDED.min_order_qty, lead_time_days = EXCLUDED.lead_time_days,
			    preferred = EXCLUDED.preferred, notes = EXCLUDED.notes, updated_at = NOW()`,
			targetID, partnerID, c.SupplierPartNo, c.Price, c.MinOrderQty, c.LeadTimeDays, c.Preferred, c.Notes,
			nullID(getUser(r).ID)); err != nil {
			return err
		}
		if c.Preferred {
			if _, err := tx.Exec(ctx, `UPDATE spare_part_suppliers SET preferred = false WHERE part_id = $1 AND partner_id <> $2`, targetID, partnerID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	case "record:responsible", "record:assigned":
		parts := strings.SplitN(targetID, ":", 2)
		if len(parts) != 2 {
			return errors.New("ungültiger Vorgang")
		}
		def, ok := infraCommentRefTypes[parts[0]]
		if !ok {
			return errors.New("ungültiger Vorgangstyp")
		}
		var exists bool
		if err := h.db.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1::uuid)`, def.Table), parts[1]).Scan(&exists); err != nil || !exists {
			return errors.New("Vorgang nicht gefunden")
		}
		// Kontakt aus dem Hauptansprechpartner bzw. der Zentrale
		_, err := h.db.Exec(ctx, `
			INSERT INTO record_external_parties (ref_type, ref_id, role, kind, name, contact, partner_id, created_by)
			SELECT $1::text, $2::uuid, $3::text, 'company', bp.name,
			       LEFT(CONCAT_WS(' · ', NULLIF(c.name, ''), NULLIF(COALESCE(NULLIF(c.phone, ''), NULLIF(bp.support_hotline, ''), NULLIF(bp.support_phone, ''), NULLIF(bp.service_phone, ''), bp.phone), ''),
			                      NULLIF(COALESCE(NULLIF(c.email, ''), NULLIF(bp.support_email, ''), NULLIF(bp.service_email, ''), bp.email), '')), 300),
			       bp.id, $5::uuid
			FROM business_partners bp
			LEFT JOIN LATERAL (SELECT name, phone, email FROM business_partner_contacts
			                   WHERE partner_id = bp.id ORDER BY is_primary DESC, created_at LIMIT 1) c ON true
			WHERE bp.id = $4::uuid
			  AND NOT EXISTS (SELECT 1 FROM record_external_parties x
			                  WHERE x.ref_type = $1::text AND x.ref_id = $2::uuid AND x.role = $3::text AND x.partner_id = bp.id)`,
			parts[0], parts[1], role, partnerID, nullID(getUser(r).ID))
		if err == nil {
			_, _ = h.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
				SELECT $1::text, $2::uuid, 'update', 'external_party', name, $3::uuid, 'Externe Firma zugewiesen' FROM business_partners WHERE id = $4::uuid`,
				parts[0], parts[1], nullID(getUser(r).ID), partnerID)
		}
		return err
	}
	return errors.New("ungültige Zuweisung")
}

func (h *Handler) unassignPartner(ctx context.Context, partnerID, target, role, targetID string) error {
	if table, column, ok := partnerAssignColumn(target, role); ok {
		_, err := h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = NULL, updated_at = NOW() WHERE id = $1 AND %s = $2`, table, column, column), targetID, partnerID)
		return err
	}
	switch target {
	case "part":
		_, err := h.db.Exec(ctx, `DELETE FROM spare_part_suppliers WHERE part_id = $1 AND partner_id = $2`, targetID, partnerID)
		return err
	case "record":
		_, err := h.db.Exec(ctx, `DELETE FROM record_external_parties WHERE id = $1 AND partner_id = $2`, targetID, partnerID)
		return err
	}
	return errors.New("ungültige Zuweisung")
}

var partnerTargetTabs = map[string]string{"infra": "infra", "part": "parts", "it": "it", "record": "records"}

// safeReturn erlaubt nur lokale Ruecksprungziele.
func safeReturn(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.Contains(s, "\\") {
		return s
	}
	return ""
}

func (h *Handler) PartnerAssignWeb(w http.ResponseWriter, r *http.Request) {
	h.partnerAssignmentWeb(w, r, chi.URLParam(r, "id"), true)
}

func (h *Handler) PartnerUnassignWeb(w http.ResponseWriter, r *http.Request) {
	h.partnerAssignmentWeb(w, r, chi.URLParam(r, "id"), false)
}

func (h *Handler) partnerAssignmentWeb(w http.ResponseWriter, r *http.Request, partnerID string, assign bool) {
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	target, role := r.FormValue("target"), r.FormValue("role")
	targetID := r.FormValue("target_id")
	if target == "infra" && targetID == "" {
		targetID = r.FormValue("infrastructure_id") // aus dem Infrastruktur-Baum-Picker
	}
	var err error
	msg := "Zuweisung entfernt"
	if assign {
		err = h.assignPartner(r.Context(), r, partnerID, target, role, targetID)
		msg = "Zuweisung gespeichert"
	} else {
		err = h.unassignPartner(r.Context(), partnerID, target, role, targetID)
	}
	if ret := safeReturn(r.FormValue("return")); ret != "" {
		sep := "?"
		if strings.Contains(ret, "?") {
			sep = "&"
		}
		if err != nil {
			ret += sep + "err=" + url.QueryEscape(friendlyDBError(err))
		}
		http.Redirect(w, r, ret, http.StatusSeeOther)
		return
	}
	partnerRedirect(w, r, partnerID, partnerTargetTabs[target], msg, err)
}

// ── Rueckverlinkung aus anderen Modulen ──────────────────────

// PartnerLink ist ein verlinkter Partner auf einer Modul-Detailseite.
type PartnerLink struct {
	ID, Name, Role, RoleLabel string
}

// PartSupplierView ist ein Lieferant auf der Ersatzteil-Detailseite.
type PartSupplierView struct {
	PartnerID, Name, SupplierPartNo, Price, MinOrderQty, LeadTime, Notes string
	OrderEmail, Phone                                                    string
	Preferred, Blocked                                                   bool
}

func (h *Handler) partSuppliers(ctx context.Context, partID string) []PartSupplierView {
	rows, err := h.db.Query(ctx, `
		SELECT bp.id::text, bp.name, s.supplier_part_no, s.price::float8, s.min_order_qty::float8, s.lead_time_days,
		       s.notes, s.preferred, bp.approval_status = 'blocked',
		       COALESCE(NULLIF(bp.spare_parts_email, ''), NULLIF(bp.order_email, ''), bp.email),
		       COALESCE(NULLIF(bp.spare_parts_phone, ''), bp.phone), bp.lead_time_days
		FROM spare_part_suppliers s JOIN business_partners bp ON bp.id = s.partner_id
		WHERE s.part_id = $1
		ORDER BY s.preferred DESC, s.price NULLS LAST, bp.name`, partID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartSupplierView
	for rows.Next() {
		var s PartSupplierView
		var price, minQty *float64
		var lead, defaultLead *int
		if rows.Scan(&s.PartnerID, &s.Name, &s.SupplierPartNo, &price, &minQty, &lead, &s.Notes, &s.Preferred,
			&s.Blocked, &s.OrderEmail, &s.Phone, &defaultLead) != nil {
			continue
		}
		s.Price = optFloat(price, formatEuro)
		s.MinOrderQty = optFloat(minQty, formatQty)
		if lead == nil {
			lead = defaultLead
		}
		if lead != nil {
			s.LeadTime = strconv.Itoa(*lead) + " Tage"
		}
		list = append(list, s)
	}
	return list
}

// supplierOptions listet aktive Lieferanten fuer Zuweisungs-Auswahlen.
func (h *Handler) supplierOptions(ctx context.Context) []partnerOption {
	return h.partnerSimpleOptions(ctx, `
		SELECT id::text, name || CASE WHEN approval_status = 'blocked' THEN ' (gesperrt)' ELSE '' END
		FROM business_partners WHERE active AND kind IN ('supplier', 'both') ORDER BY name`)
}

// infraPartnerLinks liefert Hersteller/Lieferant/Servicepartner einer Anlage.
func (h *Handler) infraPartnerLinks(ctx context.Context, infraID string) (links []PartnerLink, supplierID, serviceID string) {
	rows, err := h.db.Query(ctx, `
		SELECT r.role, bp.id::text, bp.name FROM infrastructure i
		CROSS JOIN LATERAL (VALUES ('manufacturer', i.manufacturer_id), ('supplier', i.supplier_id),
		                           ('service', i.service_partner_id)) AS r(role, pid)
		JOIN business_partners bp ON bp.id = r.pid
		WHERE i.id = $1`, infraID)
	if err != nil {
		return nil, "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var l PartnerLink
		if rows.Scan(&l.Role, &l.ID, &l.Name) != nil {
			continue
		}
		l.RoleLabel = partnerRoleLabels[l.Role]
		switch l.Role {
		case "supplier":
			supplierID = l.ID
		case "service":
			serviceID = l.ID
		}
		links = append(links, l)
	}
	return links, supplierID, serviceID
}

// saveInfraPartnerRoles speichert Lieferant/Servicepartner einer Anlage
// (Hersteller laeuft ueber das bestehende manufacturer_id-Feld).
func (h *Handler) saveInfraPartnerRoles(ctx context.Context, r *http.Request, infraID string) error {
	if _, ok := r.Form["supplier_id"]; !ok {
		return nil
	}
	_, err := h.db.Exec(ctx, `
		UPDATE infrastructure SET supplier_id = NULLIF($1, '')::uuid, service_partner_id = NULLIF($2, '')::uuid
		WHERE id = $3`, strings.TrimSpace(r.FormValue("supplier_id")), strings.TrimSpace(r.FormValue("service_partner_id")), infraID)
	return err
}

func (h *Handler) itSupplierLink(ctx context.Context, assetID string) (link *PartnerLink) {
	var l PartnerLink
	if err := h.db.QueryRow(ctx, `
		SELECT bp.id::text, bp.name FROM it_assets a JOIN business_partners bp ON bp.id = a.supplier_id
		WHERE a.id = $1`, assetID).Scan(&l.ID, &l.Name); err != nil {
		return nil
	}
	l.Role, l.RoleLabel = "supplier", partnerRoleLabels["supplier"]
	return &l
}

func (h *Handler) saveITSupplier(ctx context.Context, r *http.Request, assetID string) error {
	if _, ok := r.Form["supplier_id"]; !ok {
		return nil
	}
	_, err := h.db.Exec(ctx, `UPDATE it_assets SET supplier_id = NULLIF($1, '')::uuid WHERE id = $2`,
		strings.TrimSpace(r.FormValue("supplier_id")), assetID)
	return err
}

// PartSupplierAssignWeb ordnet einem Ersatzteil von dessen Detailseite
// aus einen Lieferanten zu (gleiche Logik wie im Coreboard).
func (h *Handler) PartSupplierAssignWeb(w http.ResponseWriter, r *http.Request) {
	partID := chi.URLParam(r, "id")
	back := "/inventory/" + partID
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	err := h.assignPartner(r.Context(), r, r.FormValue("partner_id"), "part", "supplier", partID)
	if err != nil {
		back += "?err=" + url.QueryEscape(friendlyDBError(err))
	}
	http.Redirect(w, r, back+"#suppliers", http.StatusSeeOther)
}

func (h *Handler) PartSupplierRemoveWeb(w http.ResponseWriter, r *http.Request) {
	partID, partnerID := chi.URLParam(r, "id"), chi.URLParam(r, "partnerId")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	back := "/inventory/" + partID
	if err := h.unassignPartner(r.Context(), partnerID, "part", "supplier", partID); err != nil {
		back += "?err=" + url.QueryEscape(friendlyDBError(err))
	}
	http.Redirect(w, r, back+"#suppliers", http.StatusSeeOther)
}
