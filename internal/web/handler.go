package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/users"
	"pdh/internal/modules/checklists"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/inventory"
	"pdh/internal/modules/it"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
)

// ── Template-Daten ───────────────────────────────────────────

type BaseData struct {
	Title         string
	Page          string
	ContextTitle  string
	UserName      string
	UserFirstName string
	UserLastName  string
	FaultID       string
}

type DashboardData struct {
	BaseData
	Greeting       string
	DateStr        string
	WeekNumber     int
	WeekRange      string
	Stats          DashStats
	Faults         []FaultView
	MaintenanceDue []MaintenanceView
	OpenTickets    []TicketView
	WeekPlan       bool
	WeekDays       []ShiftDay
}

type DashStats struct {
	OpenTickets     int
	CriticalTickets int
	ActiveFaults    int
	AnalyzingFaults int
	MaintenanceDue  int
	InventoryValue  string
	LowStock        int
}

type FaultView struct {
	ID              string
	Title           string
	Description     string
	Status          string
	StatusLabel     string
	StatusClass     string
	Severity        string
	SeverityClass   string
	InfraID         string
	InfraName       string
	DetectedAgo     string
	Confidence      float64
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
	RecordImageURL  string
}

type TicketView struct {
	ID              string
	Title           string
	Description     string
	InfraID         string
	InfraName       string
	Priority        string
	PriorityClass   string
	PriorityDot     string
	Status          string
	StatusLabel     string
	StatusClass     string
	CreatedAgo      string
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
	RecordImageURL  string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
}

type UserOption struct {
	ID   string
	Name string
}

type HistoryView struct {
	Action    string
	FieldName string
	OldValue  string
	NewValue  string
	Message   string
	UserName  string
	CreatedAt string
}

type RecordPeople struct {
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
}

type MaintenanceView struct {
	ID            string
	Title         string
	InfraName     string
	EstimatedMin  int
	Priority      string
	PriorityClass string
}

type ShiftDay struct {
	Short string
	Label string
	Class string
}

type FaultsPageData struct {
	BaseData
	Total          int
	Open           int
	Filter         string
	Faults         []FaultView
	RecentAnalyses []AnalysisView
	Users          []UserOption
}

type AnalysisView struct {
	FaultTitle string
	Confidence float64
}

type TicketsPageData struct {
	BaseData
	Total           int
	Open            int
	Filter          string
	Tickets         []TicketView
	CriticalTickets []TicketView
	Users           []UserOption
}

type InventoryPageData struct {
	BaseData
	Stats         InventoryStats
	Parts         []PartView
	LowStockParts []PartView
}

type InventoryStats struct {
	Total      int
	LowStock   int
	Critical   int
	Empty      int
	TotalValue string
}

type PartView struct {
	ID           string
	PartNumber   string
	Name         string
	Manufacturer string
	Category     string
	Unit         string
	StockQty     string
	MinQty       string
	Price        string
	Status       string
	StatusLabel  string
	StatusClass  string
	StatusDot    string
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct {
	db        *pgxpool.Pool
	tmpl      *template.Template
	users     *users.Service
	shifts    *shifts.Service
	storage   *storage.Service
	infra     *infrastructure.Service
	tickets   *tickets.Service
	faults    *faults.Service
	maint     *maintenance.Service
	inv       *inventory.Service
	it        *it.Service
	time      *timetracking.Service
	checks    *checklists.Service
	jwtSecret string
}

func NewHandler(
	db *pgxpool.Pool,
	tmpl *template.Template,
	u *users.Service,
	s *shifts.Service,
	st *storage.Service,
	i *infrastructure.Service,
	t *tickets.Service,
	f *faults.Service,
	m *maintenance.Service,
	inv *inventory.Service,
	itt *it.Service,
	tt *timetracking.Service,
	ch *checklists.Service,
	jwtSecret string,
) *Handler {
	return &Handler{
		db:   db,
		tmpl: tmpl, users: u, shifts: s, storage: st, infra: i,
		tickets: t, faults: f, maint: m, inv: inv, it: itt, time: tt,
		checks:    ch,
		jwtSecret: jwtSecret,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.authMiddleware)

	r.Get("/", h.Dashboard)
	r.Get("/tickets", h.Tickets)
	r.Post("/tickets", h.CreateTicket)
	r.Get("/tickets/{id}", h.TicketDetail)
	r.Put("/tickets/{id}/status", h.UpdateTicketStatus)
	r.Post("/tickets/{id}/status-web", h.TicketStatusWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/tickets/{id}/resolve-web", h.TicketResolve)
	r.Put("/tickets/{id}/infrastructure-web", h.TicketInfrastructureWeb)
	r.Post("/tickets/{id}/comment", h.TicketAddComment)
	r.Post("/tickets/{id}/time/start", h.TicketStartTime)
	r.Get("/faults", h.Faults)
	r.Post("/faults", h.CreateFault)
	r.Get("/faults/{id}", h.FaultDetail)
	r.Post("/faults/{id}/analyze", h.AnalyzeFault)
	r.Post("/faults/{id}/resolve", h.FaultResolve)
	r.Put("/faults/{id}/infrastructure-web", h.FaultInfrastructureWeb)
	r.Post("/faults/{id}/time/start", h.FaultStartTime)
	r.Get("/inventory", h.Inventory)
	r.Post("/inventory", h.CreatePart)
	r.Get("/inventory/{id}", h.InventoryDetail)
	r.Post("/inventory/book-web", h.InventoryBookWeb)
	r.Get("/maintenance", h.Maintenance)
	r.Post("/maintenance/plans", h.MaintenanceCreatePlan)
	r.Put("/maintenance/plans/{id}/edit-web", h.MaintenancePlanEditWeb)
	r.Post("/maintenance/plans/{id}/duplicate-web", h.MaintenancePlanDuplicateWeb)
	r.Post("/maintenance/generate", h.MaintenanceGenerate)
	r.Get("/maintenance/tasks/{id}", h.MaintenanceTaskDetail)
	r.Post("/maintenance/tasks/{id}/start-web", h.MaintenanceTaskStartWeb)
	r.Post("/maintenance/tasks/{id}/complete-web", h.MaintenanceTaskCompleteWeb)
	r.Post("/maintenance/tasks/{id}/edit-web", h.MaintenanceTaskEditWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/maintenance/tasks/{id}/delete-web", h.MaintenanceTaskDeleteWeb)
	r.Post("/maintenance/tasks/{id}/time/start", h.MaintenanceTaskStartTime)
	r.Post("/maintenance/tasks/{id}/checklist", h.MaintenanceTaskChecklistWeb)
	r.Get("/shifts", h.Shifts)
	r.Get("/infrastructure", h.Infrastructure)
	r.Get("/infrastructure/{id}", h.InfraDetail)
	r.Post("/infrastructure/{id}/edit", h.InfraUpdate) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/infrastructure", h.InfraCreate)
	r.Get("/it", h.ITPage)
	r.Post("/it", h.ITCreate)
	r.Put("/it/{id}/status-web", h.ITStatusWeb)
	r.Get("/storage", h.StoragePage)
	r.Post("/storage", h.StorageCreateRoot)
	r.Post("/storage/{id}/children-web", h.StorageAddChild)
	r.Get("/checklists", h.ChecklistsPage)
	r.Get("/shifts", h.Shifts)
	r.Post("/faults/{id}/chat-web", h.FaultChatWeb)
	r.Post("/faults/{id}/time/start", h.FaultStartTime)
	r.Post("/time/start-web", h.TimeStartWeb)
	r.Post("/time/manual-web", h.TimeManualWeb)
	r.Post("/time/{id}/stop-web", h.TimeStopWeb)
	r.Delete("/time/{id}/delete-web", h.TimeDeleteWeb)
	r.Put("/records/{refType}/{id}/people", h.RecordPeopleWeb)
	r.Post("/records/{refType}/{id}/archive", h.RecordArchiveWeb)
	r.Get("/users", h.Users)
	r.Post("/users/create-web", h.UserCreateWeb)
	r.Post("/users/{id}/update-web", h.UserUpdateWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/users/{id}/role-web", h.UserRoleWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Delete("/users/{id}/deactivate-web", h.UserDeactivateWeb)
	r.Get("/time", h.TimeTracking)

	// HTMX partials
	r.Get("/api/activity", h.ActivityFeed)
	r.Get("/api/search", h.Search)

	// Auth
	r.Get("/login", h.LoginPage)
	r.Post("/login", h.LoginPost)
	r.Get("/logout", h.Logout)

	return r
}

// ── Helpers ──────────────────────────────────────────────────

func (h *Handler) render(w http.ResponseWriter, tmpl string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if tmpl == "login" {
		lt, _ := t.ParseFiles("web/templates/login.gohtml")
		lt.ExecuteTemplate(w, "login.gohtml", data)
		return
	}
	if _, err2 := t.ParseFiles("web/templates/" + tmpl + ".gohtml"); err2 != nil {
		http.Error(w, "Parse: "+err2.Error(), 500)
		return
	}
	if err3 := t.ExecuteTemplate(w, "base.gohtml", data); err3 != nil {
		http.Error(w, "Template-Fehler: "+err3.Error(), 500)
	}
}

func esc(s string) string {
	return template.HTMLEscapeString(s)
}

func jsesc(s string) string {
	return template.JSEscapeString(s)
}

func greeting() string {
	h := time.Now().Hour()
	if h < 12 {
		return "Morgen"
	}
	if h < 18 {
		return "Tag"
	}
	return "Abend"
}
func timeAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "gerade eben"
	}
	if d < time.Hour {
		return fmt.Sprintf("vor %dmin", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("vor %dh", int(d.Hours()))
	}
	return fmt.Sprintf("vor %dd", int(d.Hours()/24))
}
func severityClass(s string) string {
	switch s {
	case "critical":
		return "b-red"
	case "high":
		return "b-red"
	case "medium":
		return "b-amber"
	default:
		return "b-gray"
	}
}

func priorityClass(p string) string {
	switch p {
	case "critical":
		return "b-red"
	case "high":
		return "b-red"
	case "medium":
		return "b-amber"
	default:
		return "b-blue"
	}
}

func priorityDot(p string) string {
	switch p {
	case "critical", "high":
		return "d-red"
	case "medium":
		return "d-amber"
	default:
		return "d-blue"
	}
}

func statusClass(s string) string {
	switch s {
	case "resolved", "closed", "done":
		return "b-green"
	case "in_progress":
		return "b-blue"
	case "open", "detected":
		return "b-amber"
	default:
		return "b-gray"
	}
}

func statusLabel(s string) string {
	labels := map[string]string{
		"open": "Offen", "in_progress": "In Arbeit",
		"resolved": "Gelöst", "closed": "Geschlossen",
		"detected": "Erkannt", "analyzing": "Analysiert",
		"pending": "Ausstehend", "archive": "Archiv",
	}
	if l, ok := labels[s]; ok {
		return l
	}
	return s
}

func getUser(r *http.Request) *users.User {
	if u, ok := r.Context().Value("user").(*users.User); ok {
		return u
	}
	return &users.User{FirstName: "Gast", LastName: "", Role: "viewer"}
}

func baseData(r *http.Request, page, title, ctxTitle string) BaseData {
	u := getUser(r)
	return BaseData{
		Title: title, Page: page,
		ContextTitle:  ctxTitle,
		UserName:      u.FirstName + " " + u.LastName,
		UserFirstName: u.FirstName,
		UserLastName:  u.LastName,
	}
}

func recordTable(refType string) (string, bool) {
	switch refType {
	case "ticket":
		return "tickets", true
	case "fault":
		return "faults", true
	case "maintenance_task":
		return "maintenance_tasks", true
	default:
		return "", false
	}
}

func nullID(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func optionalID(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (h *Handler) infraName(ctx context.Context, id *string) string {
	if id == nil || strings.TrimSpace(*id) == "" || h.infra == nil {
		return ""
	}
	node, err := h.infra.GetByID(ctx, *id)
	if err != nil || node == nil {
		return ""
	}
	return node.Name
}

func (h *Handler) userOptions(ctx context.Context) []UserOption {
	list, err := h.users.List(ctx)
	if err != nil {
		return nil
	}
	opts := make([]UserOption, 0, len(list))
	for _, u := range list {
		opts = append(opts, UserOption{ID: u.ID, Name: strings.TrimSpace(u.FirstName + " " + u.LastName)})
	}
	return opts
}

func (h *Handler) recordPeople(ctx context.Context, refType, id string) RecordPeople {
	table, ok := recordTable(refType)
	if !ok || h.db == nil {
		return RecordPeople{}
	}
	var assignedID, responsibleID *string
	var assignedName, responsibleName string
	query := fmt.Sprintf(`
		SELECT r.assigned_to::text, r.responsible_to::text,
		       COALESCE(au.first_name || ' ' || au.last_name, ''),
		       COALESCE(ru.first_name || ' ' || ru.last_name, '')
		FROM %s r
		LEFT JOIN users au ON r.assigned_to = au.id
		LEFT JOIN users ru ON r.responsible_to = ru.id
		WHERE r.id=$1`, table)
	if err := h.db.QueryRow(ctx, query, id).Scan(&assignedID, &responsibleID, &assignedName, &responsibleName); err != nil {
		return RecordPeople{}
	}
	result := RecordPeople{AssignedName: assignedName, ResponsibleName: responsibleName}
	if assignedID != nil {
		result.AssignedID = *assignedID
	}
	if responsibleID != nil {
		result.ResponsibleID = *responsibleID
	}
	return result
}

func (h *Handler) recordImageURL(ctx context.Context, refType, id string) string {
	table, ok := recordTable(refType)
	if !ok || h.db == nil {
		return ""
	}
	var path *string
	query := fmt.Sprintf(`
		SELECT a.filepath
		FROM %s r
		JOIN attachments a ON a.id = r.record_image_attachment_id
		WHERE r.id=$1`, table)
	if err := h.db.QueryRow(ctx, query, id).Scan(&path); err != nil || path == nil {
		return ""
	}
	return "/uploads/" + *path
}

func (h *Handler) updateInfrastructureWeb(w http.ResponseWriter, r *http.Request, refType string) {
	table, ok := recordTable(refType)
	if !ok || h.db == nil {
		http.Error(w, "ungültiger Datensatztyp", http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
	query := fmt.Sprintf(`UPDATE %s SET infrastructure_id=NULLIF($1,'')::uuid, updated_at=NOW() WHERE id=$2`, table)
	if _, err := h.db.Exec(r.Context(), query, infraID, id); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	label := "Keine Infrastruktur"
	if infraID != "" && h.infra != nil {
		if node, err := h.infra.GetByID(r.Context(), infraID); err == nil && node != nil {
			label = node.Name
		}
	}
	u := getUser(r)
	_, _ = h.db.Exec(r.Context(), `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
		VALUES ($1, $2, 'update', 'infrastructure_id', $3, $4, 'Infrastruktur geändert')`,
		refType, id, infraID, u.ID)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px">Gespeichert: %s</div>`, esc(label))
}

func (h *Handler) recordHistory(ctx context.Context, refType, id string) []HistoryView {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT rh.action, COALESCE(rh.field_name,''), COALESCE(rh.old_value,''),
		       COALESCE(rh.new_value,''), COALESCE(rh.message,''),
		       COALESCE(u.first_name || ' ' || u.last_name, 'System'), rh.created_at
		FROM record_history rh
		LEFT JOIN users u ON rh.created_by = u.id
		WHERE rh.ref_type=$1 AND rh.ref_id=$2
		ORDER BY rh.created_at DESC
		LIMIT 30`, refType, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []HistoryView
	for rows.Next() {
		var item HistoryView
		var created time.Time
		if err := rows.Scan(&item.Action, &item.FieldName, &item.OldValue, &item.NewValue, &item.Message, &item.UserName, &created); err == nil {
			item.CreatedAt = created.Format("02.01.2006 15:04")
			list = append(list, item)
		}
	}
	return list
}

func (h *Handler) addHistory(ctx context.Context, refType, id, action, field, oldValue, newValue, message, userID string) {
	if h.db == nil {
		return
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		refType, id, action, field, oldValue, newValue, message, nullID(userID))
}

func (h *Handler) RecordPeopleWeb(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	id := chi.URLParam(r, "id")
	table, ok := recordTable(refType)
	if !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	r.ParseForm()
	u := getUser(r)
	old := h.recordPeople(r.Context(), refType, id)
	assigned := r.FormValue("assigned_to")
	responsible := r.FormValue("responsible_to")
	_, err := h.db.Exec(r.Context(), fmt.Sprintf(`
		UPDATE %s SET assigned_to=$1, responsible_to=$2, updated_at=NOW() WHERE id=$3`, table),
		nullID(assigned), nullID(responsible), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if old.AssignedID != assigned {
		h.addHistory(r.Context(), refType, id, "people", "assigned_to", old.AssignedID, assigned, "Zugewiesener Mitarbeiter geändert", u.ID)
	}
	if old.ResponsibleID != responsible {
		h.addHistory(r.Context(), refType, id, "people", "responsible_to", old.ResponsibleID, responsible, "Verantwortlicher Mitarbeiter geändert", u.ID)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Zuständigkeit gespeichert</span>`)
}

func (h *Handler) RecordArchiveWeb(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	id := chi.URLParam(r, "id")
	u := getUser(r)
	var err error
	switch refType {
	case "ticket":
		err = h.tickets.QuickResolve(r.Context(), id, "Archiviert", "", u.ID)
	case "fault":
		err = h.faults.QuickResolve(r.Context(), id, "Archiviert", "", u.ID)
	default:
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.addHistory(r.Context(), refType, id, "archive", "archived_at", "", time.Now().Format(time.RFC3339), "Datensatz archiviert", u.ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-archive"></i> Archiviert</span>`)
}

// ── Seiten ───────────────────────────────────────────────────

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	now := time.Now()
	_, week := now.ISOWeek()

	data := DashboardData{
		BaseData:   baseData(r, "dashboard", "Dashboard", "Offene Tickets"),
		Greeting:   greeting(),
		DateStr:    now.Format("Mo 02. January 2006"),
		WeekNumber: week,
		WeekRange: fmt.Sprintf("%s–%s",
			now.AddDate(0, 0, -int(now.Weekday())+1).Format("02.01"),
			now.AddDate(0, 0, 7-int(now.Weekday())).Format("02.01")),
	}

	// Störungen
	if fl, err := h.faults.List(ctx, ""); err == nil {
		for _, f := range fl {
			if f.Status == "detected" || f.Status == "in_progress" || f.Status == "analyzing" {
				data.Stats.ActiveFaults++
				if f.Status == "analyzing" {
					data.Stats.AnalyzingFaults++
				}
			}
			if len(data.Faults) < 4 && (f.Status == "detected" || f.Status == "in_progress") {
				data.Faults = append(data.Faults, FaultView{
					ID: f.ID, Title: f.Title,
					Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
					StatusClass: statusClass(string(f.Status)),
					Severity:    string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
					DetectedAgo: timeAgo(f.DetectedAt),
				})
			}
		}
	}

	// Tickets
	if tl, err := h.tickets.List(ctx, ""); err == nil {
		for _, t := range tl {
			if t.Status == "open" || t.Status == "in_progress" {
				data.Stats.OpenTickets++
				if t.Priority == "critical" {
					data.Stats.CriticalTickets++
				}
			}
			if len(data.OpenTickets) < 5 && (t.Status == "open") {
				data.OpenTickets = append(data.OpenTickets, TicketView{
					ID: t.ID, Title: t.Title,
					Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
					PriorityDot: priorityDot(string(t.Priority)),
				})
			}
		}
	}

	// Wartung fällig
	if mt, err := h.maint.GetDueToday(ctx); err == nil {
		data.Stats.MaintenanceDue = len(mt)
		for _, m := range mt {
			if len(data.MaintenanceDue) < 3 {
				data.MaintenanceDue = append(data.MaintenanceDue, MaintenanceView{
					ID: m.ID, Title: m.Title, InfraName: m.InfraName,
					EstimatedMin: 0, Priority: string(m.Priority),
					PriorityClass: priorityClass(string(m.Priority)),
				})
			}
		}
	}

	// Lager
	if stats, err := h.inv.GetStats(ctx); err == nil {
		if v, ok := stats["total_value"].(float64); ok {
			data.Stats.InventoryValue = fmt.Sprintf("%.0f", v)
		}
		if v, ok := stats["low_stock"].(int); ok {
			data.Stats.LowStock = v
		}
	}

	// Schichtplan
	monday := now.AddDate(0, 0, -int(now.Weekday())+1)
	if wp, err := h.shifts.GetWeekPlan(ctx, monday.Format("2006-01-02")); err == nil && wp != nil {
		days := []string{"Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"}
		shiftClasses := map[string]string{"F": "sf", "S": "ss", "N": "sn"}
		for i, u2 := range wp.Users {
			if i == 0 || u2.UserID == u.ID {
				data.WeekPlan = true
				for d := 0; d < 7; d++ {
					date := monday.AddDate(0, 0, d).Format("2006-01-02")
					label, class := "–", "se"
					if entry, ok := u2.Days[date]; ok && entry.ShortName != "" {
						label = entry.ShortName
						if c, ok := shiftClasses[entry.ShortName]; ok {
							class = c
						}
					}
					data.WeekDays = append(data.WeekDays, ShiftDay{Short: days[d], Label: label, Class: class})
				}
				break
			}
		}
	}

	h.render(w, "dashboard", data)
}

func (h *Handler) Faults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")

	data := FaultsPageData{
		BaseData: baseData(r, "faults", "Störungen", "Copilot-Analysen"),
		Filter:   filter,
		Users:    h.userOptions(ctx),
	}

	if fl, err := h.faults.List(ctx, faults.FaultStatus(filter)); err == nil {
		data.Total = len(fl)
		for _, f := range fl {
			if f.Status == "detected" || f.Status == "in_progress" {
				data.Open++
			}
			fv := FaultView{
				ID: f.ID, Title: f.Title, Description: f.Description,
				Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
				StatusClass: statusClass(string(f.Status)),
				Severity:    string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
				DetectedAgo: timeAgo(f.DetectedAt),
				InfraName:   h.infraName(ctx, f.InfrastructureID),
			}
			if f.InfrastructureID != nil {
				fv.InfraID = *f.InfrastructureID
			}
			data.Faults = append(data.Faults, fv)
		}
	}
	h.render(w, "faults", data)
}

func (h *Handler) Tickets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")

	data := TicketsPageData{
		BaseData: baseData(r, "tickets", "Tickets", "Kritische Tickets"),
		Filter:   filter,
		Users:    h.userOptions(ctx),
	}

	if tl, err := h.tickets.List(ctx, tickets.Status(filter)); err == nil {
		data.Total = len(tl)
		for _, t := range tl {
			if t.Status == "open" || t.Status == "in_progress" {
				data.Open++
			}
			tv := TicketView{
				ID: t.ID, Title: t.Title, Description: t.Description,
				InfraName: h.infraName(ctx, t.InfrastructureID),
				Priority:  string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
				PriorityDot: priorityDot(string(t.Priority)),
				Status:      string(t.Status), StatusLabel: statusLabel(string(t.Status)),
				StatusClass: statusClass(string(t.Status)),
				CreatedAgo:  timeAgo(t.CreatedAt),
			}
			if t.InfrastructureID != nil {
				tv.InfraID = *t.InfrastructureID
			}
			data.Tickets = append(data.Tickets, tv)
			if t.Priority == "critical" {
				data.CriticalTickets = append(data.CriticalTickets, tv)
			}
		}
	}
	h.render(w, "tickets", data)
}

func (h *Handler) Inventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := InventoryPageData{
		BaseData: baseData(r, "inventory", "Ersatzteillager", "Unter Mindestbestand"),
	}

	if stats, err := h.inv.GetStats(ctx); err == nil {
		if v, ok := stats["total"].(int); ok {
			data.Stats.Total = v
		}
		if v, ok := stats["low_stock"].(int); ok {
			data.Stats.LowStock = v
		}
		if v, ok := stats["critical"].(int); ok {
			data.Stats.Critical = v
		}
		if v, ok := stats["empty"].(int); ok {
			data.Stats.Empty = v
		}
		if v, ok := stats["total_value"].(float64); ok {
			data.Stats.TotalValue = fmt.Sprintf("%.0f", v)
		}
	}

	statusLabels := map[string]string{"ok": "OK", "low": "Niedrig", "critical": "Kritisch", "empty": "Leer"}
	statusClasses := map[string]string{"ok": "b-green", "low": "b-amber", "critical": "b-red", "empty": "b-red"}
	statusDots := map[string]string{"ok": "d-green", "low": "d-amber", "critical": "d-red", "empty": "d-red"}

	if parts, err := h.inv.List(ctx, "", "", ""); err == nil {
		for _, p := range parts {
			st := string(p.Status)
			pv := PartView{
				ID: p.ID, PartNumber: p.PartNumber, Name: p.Name,
				Manufacturer: p.Manufacturer, Category: p.Category, Unit: p.Unit,
				StockQty:        strconv.FormatFloat(p.StockQty, 'f', 1, 64),
				MinQty:          strconv.FormatFloat(p.MinQty, 'f', 1, 64),
				Price:       fmt.Sprintf("%.2f", p.Price),
				Status:      st,
				StatusLabel: statusLabels[st], StatusClass: statusClasses[st], StatusDot: statusDots[st],
			}
			data.Parts = append(data.Parts, pv)
			if st == "low" || st == "critical" || st == "empty" {
				data.LowStockParts = append(data.LowStockParts, pv)
			}
		}
	}
	h.render(w, "inventory", data)
}

func (h *Handler) CreateFault(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	symptomsRaw := r.FormValue("symptoms")
	var symptoms []string
	for _, s := range strings.Split(symptomsRaw, ",") {
		if s := strings.TrimSpace(s); s != "" {
			symptoms = append(symptoms, s)
		}
	}
	in := &faults.CreateFaultInput{
		Title:         r.FormValue("title"),
		Description:   r.FormValue("description"),
		Symptoms:      symptoms,
		Severity:      faults.Severity(r.FormValue("severity")),
		AssignedTo:    optionalID(r.FormValue("assigned_to")),
		ResponsibleTo: optionalID(r.FormValue("responsible_to")),
	}
	h.faults.Create(r.Context(), in, u.ID)
	h.Faults(w, r)
}

func (h *Handler) AnalyzeFault(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	go h.faults.Analyze(context.Background(), id)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<tr><td colspan="5" style="text-align:center;color:var(--accent);padding:12px"><i class="ti ti-brain"></i> Copilot analysiert...</td></tr>`)
}

func (h *Handler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	in := &tickets.CreateInput{
		Title:         r.FormValue("title"),
		Description:   r.FormValue("description"),
		Priority:      tickets.Priority(r.FormValue("priority")),
		AssignedTo:    optionalID(r.FormValue("assigned_to")),
		ResponsibleTo: optionalID(r.FormValue("responsible_to")),
	}
	h.tickets.Create(r.Context(), in, u.ID)
	h.Tickets(w, r)
}

func (h *Handler) UpdateTicketStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	u := getUser(r)
	h.tickets.UpdateStatus(r.Context(), id, status, u.ID)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<tr><td colspan="5" style="color:var(--green);padding:8px 12px"><i class="ti ti-check"></i> Status aktualisiert</td></tr>`)
}

func (h *Handler) CreatePart(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	minQty, _ := strconv.ParseFloat(r.FormValue("min_qty"), 64)
	in := &inventory.CreatePartInput{
		PartNumber:   r.FormValue("part_number"),
		Name:         r.FormValue("name"),
		Manufacturer: r.FormValue("manufacturer"),
		Category:     r.FormValue("category"),
		MinQty:       minQty,
	}
	h.inv.Create(r.Context(), in, u.ID)
	h.Inventory(w, r)
}

func (h *Handler) ActivityFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
<div class="act-item"><div class="act-dot d-green"></div><div class="act-text">Server läuft</div><div class="act-time">jetzt</div></div>
<div class="act-item"><div class="act-dot d-blue"></div><div class="act-text">PDH v0.7.0</div><div class="act-time">aktiv</div></div>
`)
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 2 {
		w.Write([]byte(""))
		return
	}
	w.Header().Set("Content-Type", "text/html")

	results, err := h.infra.Search(r.Context(), q)
	if err != nil || len(results) == 0 {
		fmt.Fprintf(w, `<div style="color:var(--muted);padding:4px 0;font-size:11px">Keine Ergebnisse</div>`)
		return
	}
	for _, item := range results {
		fmt.Fprintf(w, `<div class="act-item"><div class="act-dot d-blue"></div><div class="act-text">%s</div><div class="act-time" style="font-size:10px">%s</div></div>`, esc(item.Name), esc(string(item.Type)))
	}
}

// Platzhalter-Seiten

type TimePageData struct {
	BaseData
	TodayStr   string
	WeekStr    string
	TodayCount int
	Entries    []TimeEntryView
	Running    *TimeEntryView
}

type TimeEntryView struct {
	ID           string
	Description  string
	RefTypeLabel string
	RefTypeDot   string
	StartedStr   string
	EndedStr     string
	DurationStr  string
	Running      bool
	InfraName    string
}

func timeRefLabel(t timetracking.RefType) string {
	switch t {
	case timetracking.RefFault:
		return "Störung"
	case timetracking.RefTicket:
		return "Ticket"
	case timetracking.RefMaintenance:
		return "Wartung"
	case timetracking.RefProduction:
		return "Sonstiges"
	default:
		return string(t)
	}
}

func timeRefDot(t timetracking.RefType) string {
	switch t {
	case timetracking.RefFault:
		return "d-red"
	case timetracking.RefTicket:
		return "d-blue"
	case timetracking.RefMaintenance:
		return "d-amber"
	default:
		return "d-green"
	}
}

func durationText(min int) string {
	h := min / 60
	m := min % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dmin", h, m)
	}
	return fmt.Sprintf("%dmin", m)
}

func timeEntryView(e *timetracking.TimeEntry) TimeEntryView {
	v := TimeEntryView{
		ID:           e.ID,
		Description:  e.Description,
		RefTypeLabel: timeRefLabel(e.RefType),
		RefTypeDot:   timeRefDot(e.RefType),
		StartedStr:   e.StartedAt.Format("15:04"),
		Running:      e.EndedAt == nil,
		InfraName:    e.InfraName,
	}
	if e.EndedAt != nil {
		v.EndedStr = e.EndedAt.Format("15:04")
	}
	if e.DurationMin != nil {
		v.DurationStr = durationText(*e.DurationMin)
	}
	return v
}

func (h *Handler) TimeTracking(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	now := time.Now()
	weekStart := now.AddDate(0, 0, -int(now.Weekday())+1)
	if now.Weekday() == time.Sunday {
		weekStart = now.AddDate(0, 0, -6)
	}

	data := TimePageData{
		BaseData: baseData(r, "time", "Zeiterfassung", "Heute"),
		TodayStr: now.Format("02.01.2006"),
		WeekStr:  weekStart.Format("02.01.") + " - " + weekStart.AddDate(0, 0, 6).Format("02.01."),
	}
	if entries, err := h.time.ListByUser(r.Context(), u.ID, now.Format("2006-01-02"), now.Format("2006-01-02")); err == nil {
		data.TodayCount = len(entries)
		for _, e := range entries {
			view := timeEntryView(e)
			data.Entries = append(data.Entries, view)
			if view.Running && data.Running == nil {
				running := view
				data.Running = &running
			}
		}
	}
	if running, err := h.time.GetRunning(r.Context(), u.ID); err == nil && running != nil && data.Running == nil {
		view := timeEntryView(running)
		data.Running = &view
	}
	h.render(w, "timetracking", data)
}

func (h *Handler) simplePage(w http.ResponseWriter, r *http.Request, page, title, ctxTitle string) {
	h.render(w, "simple", struct{ BaseData }{baseData(r, page, title, ctxTitle)})
}

// ── Auth ──────────────────────────────────────────────────────

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login", struct{ Error string }{})
}

func (h *Handler) LoginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	token, user, err := h.users.Login(r.Context(), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		h.render(w, "login", struct{ Error string }{"Ungültige Anmeldedaten"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: user.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("pdh_token")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unerwartete signaturmethode")
			}
			return []byte(h.jwtSecret), nil
		})
		if err != nil || !token.Valid {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		userID, _ := claims["sub"].(string)
		if userID == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		user, err := h.users.GetByID(r.Context(), userID)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), "user", user)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

// ── Fault Detail ─────────────────────────────────────────────

type FaultDetailData struct {
	BaseData
	Fault         FaultDetailView
	Analysis      *faults.CopilotAnalysis
	TimeEntries   []*timetracking.TimeEntry
	RunningTime   *timetracking.TimeEntry
	SimilarFaults []SimilarFaultView
	Users         []UserOption
	History       []HistoryView
}

type FaultDetailView struct {
	ID              string
	Title           string
	Description     string
	Symptoms        []string
	Status          string
	StatusLabel     string
	StatusClass     string
	Severity        string
	SeverityClass   string
	InfraID         string
	InfraName       string
	DetectedAgo     string
	Resolution      string
	RootCause       string
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
	RecordImageURL  string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
}

type SimilarFaultView struct {
	ID         string
	Title      string
	Resolution string
}

func (h *Handler) FaultDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	fault, err := h.faults.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/faults", http.StatusFound)
		return
	}

	u := getUser(r)
	people := h.recordPeople(ctx, "fault", id)
	data := FaultDetailData{
		BaseData: BaseData{
			Title: fault.Title, Page: "faults",
			ContextTitle:  "Ähnliche Störungen",
			UserName:      u.FirstName + " " + u.LastName,
			UserFirstName: u.FirstName, UserLastName: u.LastName,
			FaultID: id,
		},
		Users:   h.userOptions(ctx),
		History: h.recordHistory(ctx, "fault", id),
		Fault: FaultDetailView{
			ID: fault.ID, Title: fault.Title,
			Description:     fault.Description,
			Symptoms:        fault.Symptoms,
			Status:          string(fault.Status),
			StatusLabel:     statusLabel(string(fault.Status)),
			StatusClass:     statusClass(string(fault.Status)),
			Severity:        string(fault.Severity),
			SeverityClass:   severityClass(string(fault.Severity)),
			InfraName:       h.infraName(ctx, fault.InfrastructureID),
			DetectedAgo:     timeAgo(fault.DetectedAt),
			AssignedID:      people.AssignedID,
			ResponsibleID:   people.ResponsibleID,
			AssignedName:    people.AssignedName,
			ResponsibleName: people.ResponsibleName,
			RecordImageURL:  h.recordImageURL(ctx, "fault", id),
			CostCenterNumber: fault.CostCenterNumber,
			CostCenterName:   fault.CostCenterName,
		},
	}
	if fault.InfrastructureID != nil {
		data.Fault.InfraID = *fault.InfrastructureID
	}
	if fault.CostCenterID != nil {
		data.Fault.CostCenterID = *fault.CostCenterID
	}
	if fault.Resolution != nil {
		data.Fault.Resolution = *fault.Resolution
	}
	if fault.RootCause != nil {
		data.Fault.RootCause = *fault.RootCause
	}

	// Analyse laden
	if a, err := h.faults.GetAnalysis(ctx, id); err == nil {
		data.Analysis = a
		for _, s := range a.SimilarFaults {
			data.SimilarFaults = append(data.SimilarFaults, SimilarFaultView{
				ID: s.ID, Title: s.Title, Resolution: s.Resolution,
			})
		}
	}

	// Zeiteinträge
	if entries, err := h.time.ListByRef(ctx, timetracking.RefFault, id); err == nil {
		data.TimeEntries = entries
	}

	// Läuft gerade?
	if running, err := h.time.GetRunning(ctx, u.ID); err == nil && running != nil {
		data.RunningTime = running
	}

	h.render(w, "fault_detail", data)
}

func (h *Handler) FaultResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	w.Header().Set("Content-Type", "text/html")
	if err := h.faults.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"), u.ID, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);padding:10px;text-align:center;font-size:13px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Störung gelöst! <a href="/faults" style="color:var(--accent)">Zurück zur Liste</a></div>`)
}

func (h *Handler) FaultInfrastructureWeb(w http.ResponseWriter, r *http.Request) {
	h.updateInfrastructureWeb(w, r, "fault")
}

func (h *Handler) FaultStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefFault,
		RefID:       id,
		Description: "Entstörung vor Ort",
	}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;margin-top:8px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;margin-top:8px"><i class="ti ti-check"></i> Zeit gestartet (ID: `+entry.ID[:8]+`...)</div>`)
}

// ── Maintenance Page ─────────────────────────────────────────

type MaintenancePageData struct {
	BaseData
	TotalPlans   int
	OpenTasks    int
	Today        string
	Plans        []MaintPlanView
	Tasks        []MaintTaskView
	DueTasks     []MaintTaskView
	InfraOptions []InfraOption
	Filter       string
	Users        []UserOption
}

type InfraOption struct{ ID, Name string }

type MaintPlanView struct {
	ID            string
	Name          string
	InfraID       string
	InfraName     string
	TypeValue     string
	TypeLabel     string
	IntervalValue string
	IntervalDays  int
	IntervalLabel string
	NextDue       string
	NextDueISO    string
	Priority      string
	PriorityDot   string
}

type MaintTaskView struct {
	ID            string
	Title         string
	InfraName     string
	Status        string
	StatusLabel   string
	StatusClass   string
	Priority      string
	PriorityClass string
	PriorityDot   string
	DueDate       string
}

func maintTaskView(t *maintenance.MaintenanceTask) MaintTaskView {
	return MaintTaskView{
		ID: t.ID, Title: t.Title, InfraName: t.InfraName,
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
		StatusClass: statusClass(string(t.Status)),
		Priority:    string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot: priorityDot(string(t.Priority)),
		DueDate:     t.DueDate.Format("02.01."),
	}
}

func (h *Handler) Maintenance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := maintenance.TaskStatus(r.URL.Query().Get("status"))

	data := MaintenancePageData{
		BaseData: baseData(r, "maintenance", "Wartungsplanung", "Fällige Aufträge"),
		Today:    time.Now().Format("2006-01-02"),
		Filter:   string(status),
		Users:    h.userOptions(ctx),
	}

	if plans, err := h.maint.ListPlans(ctx, ""); err == nil {
		data.TotalPlans = len(plans)
		intervalLabels := map[maintenance.Interval]string{
			"daily": "Täglich", "weekly": "Wöchentlich", "monthly": "Monatlich",
			"quarterly": "Quartalsweise", "yearly": "Jährlich",
		}
		typeLabels := map[maintenance.PlanType]string{
			"preventive": "Vorbeugend", "inspection": "Inspektion",
			"calibration": "Kalibrierung", "cleaning": "Reinigung",
		}
		for _, p := range plans {
			data.Plans = append(data.Plans, MaintPlanView{
				ID: p.ID, Name: p.Name, InfraID: p.InfrastructureID, InfraName: p.InfraName,
				TypeValue:     string(p.Type),
				TypeLabel:     typeLabels[p.Type],
				IntervalValue: string(p.Interval),
				IntervalDays:  p.IntervalDays,
				IntervalLabel: intervalLabels[p.Interval],
				NextDue:       p.NextDueAt.Format("02.01.2006"),
				NextDueISO:    p.NextDueAt.Format("2006-01-02"),
				Priority:      string(p.Priority),
				PriorityDot:   priorityDot(string(p.Priority)),
			})
		}
	}

	if tasks, err := h.maint.ListTasks(ctx, status, ""); err == nil {
		for _, t := range tasks {
			data.Tasks = append(data.Tasks, maintTaskView(t))
			if t.Status == "open" {
				data.OpenTasks++
			}
		}
	}

	if due, err := h.maint.GetDueToday(ctx); err == nil {
		for _, t := range due {
			data.DueTasks = append(data.DueTasks, maintTaskView(t))
		}
	}

	if infra, err := h.infra.List(ctx, nil, ""); err == nil {
		for _, i := range infra {
			data.InfraOptions = append(data.InfraOptions, InfraOption{ID: i.ID, Name: i.Name})
		}
	}

	h.render(w, "maintenance", data)
}

func (h *Handler) MaintenanceCreatePlan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
	if name == "" || infraID == "" {
		http.Error(w, "Name und Infrastruktur sind Pflicht", http.StatusBadRequest)
		return
	}
	u := getUser(r)
	in := &maintenance.CreatePlanInput{
		Name:             name,
		Type:             maintenance.PlanType(r.FormValue("type")),
		InfrastructureID: infraID,
		Interval:         maintenance.Interval(r.FormValue("interval")),
		Priority:         maintenance.Priority(r.FormValue("priority")),
		AssignedTo:       optionalID(r.FormValue("assigned_to")),
		FirstDueAt:       r.FormValue("first_due_at"),
	}
	if _, err := h.maint.CreatePlan(r.Context(), in, u.ID); err != nil {
		http.Error(w, "Wartungsplan konnte nicht angelegt werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/maintenance")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
}

func (h *Handler) MaintenancePlanEditWeb(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	intervalDays, _ := strconv.Atoi(r.FormValue("interval_days"))
	estimatedMin, _ := strconv.Atoi(r.FormValue("estimated_min"))
	in := &maintenance.UpdatePlanInput{
		Name:             strings.TrimSpace(r.FormValue("name")),
		Description:      strings.TrimSpace(r.FormValue("description")),
		Type:             maintenance.PlanType(r.FormValue("type")),
		InfrastructureID: strings.TrimSpace(r.FormValue("infrastructure_id")),
		Interval:         maintenance.Interval(r.FormValue("interval")),
		IntervalDays:     intervalDays,
		EstimatedMin:     estimatedMin,
		Priority:         maintenance.Priority(r.FormValue("priority")),
		NextDueAt:        r.FormValue("next_due_at"),
	}
	if in.Name == "" {
		http.Error(w, "Name ist Pflicht", http.StatusBadRequest)
		return
	}
	if err := h.maint.UpdatePlan(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		http.Error(w, "Wartungsplan konnte nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gespeichert</span>`))
}

func (h *Handler) MaintenancePlanDuplicateWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if _, err := h.maint.DuplicatePlan(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		http.Error(w, "Wartungsplan konnte nicht dupliziert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) MaintenanceGenerate(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	count, err := h.maint.GenerateTasks(r.Context(), u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;padding:8px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px;background:rgba(16,185,129,.1);border-radius:8px;margin-bottom:12px"><i class="ti ti-check"></i> %d Aufträge generiert</div>`, count)
}

type MaintenanceTaskDetailData struct {
	BaseData
	Task           MaintenanceTaskDetailView
	TimeEntries    []*timetracking.TimeEntry
	Running        *timetracking.TimeEntry
	ChecklistItems []struct{}
	Users          []UserOption
	History        []HistoryView
}

type MaintenanceTaskDetailView struct {
	ID              string
	Title           string
	Description     string
	TypeLabel       string
	InfraID         string
	InfraName       string
	Priority        string
	PriorityClass   string
	PriorityDot     string
	Status          string
	StatusLabel     string
	StatusClass     string
	DueDate         string
	DueDateISO      string
	StartedAt       string
	CompletedAt     string
	DurationStr     string
	Notes           string
	AssignedID      string
	ResponsibleID   string
	AssigneeName    string
	ResponsibleName string
	RecordImageURL  string
	CreatedAt       string
	CanStart        bool
	CanComplete     bool
}

func maintenanceTypeLabel(t maintenance.PlanType) string {
	labels := map[maintenance.PlanType]string{
		"preventive": "Vorbeugend", "inspection": "Inspektion",
		"calibration": "Kalibrierung", "cleaning": "Reinigung",
	}
	if label, ok := labels[t]; ok {
		return label
	}
	return string(t)
}

func maintenanceTaskDetailView(t *maintenance.MaintenanceTask) MaintenanceTaskDetailView {
	v := MaintenanceTaskDetailView{
		ID:            t.ID,
		Title:         t.Title,
		Description:   t.Description,
		TypeLabel:     maintenanceTypeLabel(t.Type),
		InfraID:       t.InfrastructureID,
		InfraName:     t.InfraName,
		Priority:      string(t.Priority),
		PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot:   priorityDot(string(t.Priority)),
		Status:        string(t.Status),
		StatusLabel:   statusLabel(string(t.Status)),
		StatusClass:   statusClass(string(t.Status)),
		DueDate:       t.DueDate.Format("02.01.2006"),
		DueDateISO:    t.DueDate.Format("2006-01-02"),
		Notes:         t.Notes,
		AssigneeName:  t.AssigneeName,
		CreatedAt:     t.CreatedAt.Format("02.01.2006 15:04"),
		CanStart:      t.Status == maintenance.TaskOpen,
		CanComplete:   t.Status == maintenance.TaskOpen || t.Status == maintenance.TaskInProgress,
	}
	if t.StartedAt != nil {
		v.StartedAt = t.StartedAt.Format("02.01.2006 15:04")
	}
	if t.CompletedAt != nil {
		v.CompletedAt = t.CompletedAt.Format("02.01.2006 15:04")
	}
	if t.DurationMin != nil {
		v.DurationStr = durationText(*t.DurationMin)
	}
	return v
}

func (h *Handler) MaintenanceTaskDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.maint.GetTaskByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/maintenance", http.StatusFound)
		return
	}

	u := getUser(r)
	data := MaintenanceTaskDetailData{
		BaseData: baseData(r, "maintenance", task.Title, "Auftrag"),
		Task:     maintenanceTaskDetailView(task),
		Users:    h.userOptions(r.Context()),
		History:  h.recordHistory(r.Context(), "maintenance_task", id),
	}
	people := h.recordPeople(r.Context(), "maintenance_task", id)
	data.Task.AssignedID = people.AssignedID
	data.Task.ResponsibleID = people.ResponsibleID
	data.Task.AssigneeName = people.AssignedName
	data.Task.ResponsibleName = people.ResponsibleName
	data.Task.RecordImageURL = h.recordImageURL(r.Context(), "maintenance_task", id)
	if entries, err := h.time.ListByRef(r.Context(), timetracking.RefMaintenance, id); err == nil {
		data.TimeEntries = entries
	}
	if running, err := h.time.GetRunning(r.Context(), u.ID); err == nil && running != nil &&
		running.RefType == timetracking.RefMaintenance && running.RefID == id {
		data.Running = running
	}
	h.render(w, "maintenance_detail", data)
}

func (h *Handler) MaintenanceTaskStartWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if err := h.maint.StartTask(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Gestartet</div>`)
}

func (h *Handler) MaintenanceTaskCompleteWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	duration, _ := strconv.Atoi(r.FormValue("duration_min"))
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	in := &maintenance.CompleteTaskInput{
		Notes:       r.FormValue("notes"),
		DurationMin: duration,
	}
	w.Header().Set("Content-Type", "text/html")
	if err := h.maint.CompleteTaskValidated(r.Context(), chi.URLParam(r, "id"), u.ID, in, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Abgeschlossen</div>`)
}

func (h *Handler) MaintenanceTaskEditWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	in := &maintenance.UpdateTaskInput{
		Title:            r.FormValue("title"),
		Description:      r.FormValue("description"),
		InfrastructureID: strings.TrimSpace(r.FormValue("infrastructure_id")),
		Priority:         maintenance.Priority(r.FormValue("priority")),
		DueDate:          r.FormValue("due_date"),
		Notes:            r.FormValue("notes"),
		CostCenterID:     optionalID(r.FormValue("cost_center_id")),
	}
	if err := h.maint.UpdateTask(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Gespeichert</div>`)
}

func (h *Handler) MaintenanceTaskDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if err := h.maint.DeleteTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusFound)
}

func (h *Handler) MaintenanceTaskStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefMaintenance,
		RefID:       id,
		Description: "Wartungsauftrag bearbeiten",
	}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px">Zeit gestartet (%s)</div>`, entry.ID[:8])
}

func (h *Handler) MaintenanceTaskChecklistWeb(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/maintenance/tasks/"+chi.URLParam(r, "id"), http.StatusFound)
}

// ── Shifts Page ───────────────────────────────────────────────

type ShiftsPageData struct {
	BaseData
	WeekNumber int
	WeekRange  string
	PrevWeek   string
	NextWeek   string
	Days       []WeekDay
	Users      []shifts.UserWeekPlan
	ShiftDefs  []ShiftDefView
	Absences   []AbsenceView
	ShiftMap   []ShiftEntry
}

type WeekDay struct {
	Short    string
	Date     string
	DateFull string
}

type ShiftDefView struct {
	Name      string
	ShortName string
	StartTime string
	EndTime   string
	Class     string
}

type AbsenceView struct {
	UserName    string
	TypeLabel   string
	TypeDot     string
	StartDate   string
	EndDate     string
	Days        int
	StatusLabel string
	StatusClass string
}

type ShiftEntry struct {
	UserID string
	Date   string
	Label  string
	Class  string
}

func (h *Handler) Shifts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	weekParam := r.URL.Query().Get("week")
	if weekParam == "" {
		weekParam = time.Now().Format("2006-01-02")
	}

	t, _ := time.Parse("2006-01-02", weekParam)
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := t.AddDate(0, 0, -(wd - 1))
	sunday := monday.AddDate(0, 0, 6)
	_, week := monday.ISOWeek()

	dayNames := []string{"Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"}
	data := ShiftsPageData{
		BaseData:   baseData(r, "shifts", "Schichtplanung", "Abwesenheiten"),
		WeekNumber: week,
		WeekRange:  monday.Format("02.01") + "–" + sunday.Format("02.01.2006"),
		PrevWeek:   monday.AddDate(0, 0, -7).Format("2006-01-02"),
		NextWeek:   monday.AddDate(0, 0, 7).Format("2006-01-02"),
	}

	for d := 0; d < 7; d++ {
		day := monday.AddDate(0, 0, d)
		data.Days = append(data.Days, WeekDay{
			Short:    dayNames[d],
			Date:     day.Format("02.01"),
			DateFull: day.Format("2006-01-02"),
		})
	}

	shiftClasses := map[string]string{"F": "sf", "S": "ss", "N": "sn"}

	if wp, err := h.shifts.GetWeekPlan(ctx, monday.Format("2006-01-02")); err == nil && wp != nil {
		data.Users = wp.Users
		for _, u := range wp.Users {
			for date, entry := range u.Days {
				class := shiftClasses[entry.ShortName]
				if class == "" {
					class = "se"
				}
				data.ShiftMap = append(data.ShiftMap, ShiftEntry{
					UserID: u.UserID, Date: date,
					Label: entry.ShortName, Class: class,
				})
			}
		}
	}

	if models, err := h.shifts.ListModels(ctx); err == nil && len(models) > 0 {
		if defs, err := h.shifts.ListShifts(ctx, models[0].ID); err == nil {
			for _, d := range defs {
				class := shiftClasses[d.ShortName]
				data.ShiftDefs = append(data.ShiftDefs, ShiftDefView{
					Name: d.Name, ShortName: d.ShortName,
					StartTime: d.StartTime, EndTime: d.EndTime, Class: class,
				})
			}
		}
	}

	absTypeLabels := map[string]string{"vacation": "Urlaub", "sick": "Krank", "training": "Schulung", "other": "Sonstiges"}
	absTypeDots := map[string]string{"vacation": "d-blue", "sick": "d-red", "training": "d-green", "other": "d-amber"}
	if abs, err := h.shifts.ListAbsences(ctx, "", ""); err == nil {
		for _, a := range abs {
			data.Absences = append(data.Absences, AbsenceView{
				UserName:    a.UserName,
				TypeLabel:   absTypeLabels[string(a.Type)],
				TypeDot:     absTypeDots[string(a.Type)],
				StartDate:   a.StartDate,
				EndDate:     a.EndDate,
				Days:        a.Days,
				StatusLabel: statusLabel(string(a.Status)),
				StatusClass: statusClass(string(a.Status)),
			})
		}
	}

	h.render(w, "shifts", data)
}

// ── Ticket Detail ─────────────────────────────────────────────

type TicketDetailData struct {
	BaseData
	Ticket         TicketView
	Comments       []CommentView
	CommentCount   int
	TimeEntries    []*timetracking.TimeEntry
	RunningTime    *timetracking.TimeEntry
	StatusOptions  []StatusOption
	RelatedTickets []TicketView
	Users          []UserOption
	History        []HistoryView
}

type CommentView struct {
	ID         string
	UserName   string
	Text       string
	CreatedAgo string
}

type StatusOption struct {
	Value string
	Label string
}

func (h *Handler) TicketDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u := getUser(r)

	t, err := h.tickets.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/tickets", http.StatusFound)
		return
	}

	data := TicketDetailData{
		BaseData: baseData(r, "tickets", t.Title, "Ähnliche Tickets"),
		Users:    h.userOptions(ctx),
		History:  h.recordHistory(ctx, "ticket", id),
		Ticket: TicketView{
			ID: t.ID, Title: t.Title, Description: t.Description,
			InfraName: h.infraName(ctx, t.InfrastructureID),
			Priority:  string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
			PriorityDot: priorityDot(string(t.Priority)),
			Status:      string(t.Status), StatusLabel: statusLabel(string(t.Status)),
			StatusClass: statusClass(string(t.Status)),
			CreatedAgo:  timeAgo(t.CreatedAt),
			CostCenterNumber: t.CostCenterNumber,
			CostCenterName:   t.CostCenterName,
		},
		StatusOptions: []StatusOption{
			{"open", "Offen"}, {"in_progress", "In Arbeit"},
			{"resolved", "Gelöst"}, {"closed", "Geschlossen"},
		},
	}
	if t.InfrastructureID != nil {
		data.Ticket.InfraID = *t.InfrastructureID
	}
	if t.CostCenterID != nil {
		data.Ticket.CostCenterID = *t.CostCenterID
	}
	people := h.recordPeople(ctx, "ticket", id)
	data.Ticket.AssignedID = people.AssignedID
	data.Ticket.ResponsibleID = people.ResponsibleID
	data.Ticket.AssignedName = people.AssignedName
	data.Ticket.ResponsibleName = people.ResponsibleName
	data.Ticket.RecordImageURL = h.recordImageURL(ctx, "ticket", id)

	if running, err := h.time.GetRunning(ctx, u.ID); err == nil {
		data.RunningTime = running
	}
	if entries, err := h.time.ListByRef(ctx, timetracking.RefTicket, id); err == nil {
		data.TimeEntries = entries
	}
	if tl, err := h.tickets.List(ctx, "open"); err == nil {
		for _, t2 := range tl {
			if t2.ID != id && len(data.RelatedTickets) < 5 {
				data.RelatedTickets = append(data.RelatedTickets, TicketView{
					ID: t2.ID, Title: t2.Title,
					PriorityDot: priorityDot(string(t2.Priority)),
					StatusLabel: statusLabel(string(t2.Status)),
					StatusClass: statusClass(string(t2.Status)),
				})
			}
		}
	}
	h.render(w, "ticket_detail", data)
}

func (h *Handler) TicketAddComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	r.ParseForm()
	c, err := h.tickets.AddComment(r.Context(), id, u.ID, r.FormValue("text"))
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler</div>`)
		return
	}
	initial := "?"
	if r := []rune(u.FirstName); len(r) > 0 {
		initial = string(r[:1])
	}
	fmt.Fprintf(w, `<div style="display:flex;gap:10px;padding:10px 0;border-bottom:1px solid var(--border)">
		<div style="width:30px;height:30px;background:var(--accent);border-radius:50%%;display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:600;flex-shrink:0;color:#fff">%s</div>
		<div><div style="font-size:12px;font-weight:500">%s · <span style="color:var(--muted);font-weight:400">gerade eben</span></div>
		<div style="font-size:13px;margin-top:4px">%s</div></div></div>`,
		esc(initial), esc(u.FirstName+" "+u.LastName), esc(c.Text))
}

func (h *Handler) TicketStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	u := getUser(r)
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.UpdateStatus(r.Context(), id, status, u.ID); err != nil {
		fmt.Fprintf(w, `<span class="badge b-red" title="%s">Nicht möglich</span>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClass(string(status)), statusLabel(string(status)))
}

func (h *Handler) TicketResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"), u.ID, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);padding:10px;text-align:center;font-size:13px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Ticket gelöst! <a href="/tickets" style="color:var(--accent)">Zurück zur Liste</a></div>`)
}

func (h *Handler) TicketInfrastructureWeb(w http.ResponseWriter, r *http.Request) {
	h.updateInfrastructureWeb(w, r, "ticket")
}

func (h *Handler) TicketStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{RefType: timetracking.RefTicket, RefID: id, Description: "Ticket bearbeiten"}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;margin-top:8px">⏱ Zeit gestartet (%s)</div>`, entry.ID[:8])
}

// ── Inventory Detail ──────────────────────────────────────────

type InventoryDetailData struct {
	BaseData
	Part          PartView
	Movements     []*inventory.StockMovement
	LowStockParts []PartView
}

func (h *Handler) InventoryDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	p, err := h.inv.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/inventory", http.StatusFound)
		return
	}

	statusLabels := map[string]string{"ok": "OK", "low": "Niedrig", "critical": "Kritisch", "empty": "Leer"}
	statusClasses := map[string]string{"ok": "b-green", "low": "b-amber", "critical": "b-red", "empty": "b-red"}
	statusDots := map[string]string{"ok": "d-green", "low": "d-amber", "critical": "d-red", "empty": "d-red"}
	st := string(p.Status)

	pv := PartView{
		ID: p.ID, PartNumber: p.PartNumber, Name: p.Name,
		Manufacturer: p.Manufacturer, Category: p.Category, Unit: p.Unit,
		StockQty: fmt.Sprintf("%.2f", p.StockQty),
		MinQty:   fmt.Sprintf("%.2f", p.MinQty),
		Price:    fmt.Sprintf("%.2f", p.Price),
		Status:   st, StatusLabel: statusLabels[st],
		StatusClass: statusClasses[st], StatusDot: statusDots[st],
	}

	data := InventoryDetailData{
		BaseData: baseData(r, "inventory", p.Name, "Unter Mindestbestand"),
		Part:     pv,
	}

	if mv, err := h.inv.GetMovements(ctx, id); err == nil {
		data.Movements = mv
	}
	if parts, err := h.inv.GetLowStock(ctx); err == nil {
		for _, lp := range parts {
			if lp.ID != id && len(data.LowStockParts) < 5 {
				lst := string(lp.Status)
				data.LowStockParts = append(data.LowStockParts, PartView{
					ID: lp.ID, Name: lp.Name,
					StockQty: fmt.Sprintf("%.1f", lp.StockQty),
					Status:   lst, StatusDot: statusDots[lst],
				})
			}
		}
	}
	h.render(w, "inventory_detail", data)
}

func (h *Handler) InventoryBookWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	qty, _ := strconv.ParseFloat(r.FormValue("qty"), 64)
	in := &inventory.BookMovementInput{
		PartID:        r.FormValue("part_id"),
		Type:          inventory.MovementType(r.FormValue("type")),
		Qty:           qty,
		StorageNodeID: r.FormValue("storage_node_id"),
		Reference:     r.FormValue("reference"),
		Notes:         r.FormValue("notes"),
	}
	mv, err := h.inv.Book(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: `+err.Error()+`</div>`)
		return
	}
	typeLabel := map[inventory.MovementType]string{"in": "Zugang", "out": "Abgang", "correction": "Korrektur", "inventory": "Inventur"}
	fmt.Fprintf(w, `<div style="display:flex;align-items:center;gap:12px;padding:8px 0;border-bottom:1px solid var(--border);font-size:13px">
		<div style="font-weight:500;flex:1">%s · Bestand: %.2f</div>
		<div style="font-weight:600;color:var(--green)">%.2f</div></div>`,
		typeLabel[mv.Type], mv.QtyAfter, mv.Qty)
}

// ── Infrastructure Page ───────────────────────────────────────

type InfraPageData struct {
	BaseData
	Tree     []InfraNodeView
	AllNodes []InfraNodeView
	Stats    map[string]int
}

type InfraNodeView struct {
	ID           string
	Name         string
	TypeLabel    string
	TypeIcon     string
	TypeBg       string
	Location     string
	Manufacturer string
	SerialNo     string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	Children     []InfraNodeView
}

func infraNodeView(i *infrastructure.Infrastructure) InfraNodeView {
	icons := map[infrastructure.InfraType]string{
		"building": "🏭", "line": "🔄", "plant": "⚙️", "device": "🔌",
	}
	labels := map[infrastructure.InfraType]string{
		"building": "Gebäude", "line": "Linie", "plant": "Anlage", "device": "Gerät",
	}
	bgs := map[infrastructure.InfraType]string{
		"building": "rgba(99,102,241,.2)", "line": "rgba(79,110,247,.2)",
		"plant": "rgba(16,185,129,.2)", "device": "rgba(245,158,11,.2)",
	}
	v := InfraNodeView{
		ID: i.ID, Name: i.Name,
		TypeLabel: labels[i.Type], TypeIcon: icons[i.Type],
		TypeBg:   bgs[i.Type],
		Location: i.Location, Manufacturer: i.Manufacturer,
		SerialNo: i.SerialNo,
		CostCenterNumber: i.CostCenterNumber, CostCenterName: i.CostCenterName,
	}
	if i.CostCenterID != nil {
		v.CostCenterID = *i.CostCenterID
	}
	for _, c := range i.Children {
		v.Children = append(v.Children, infraNodeView(c))
	}
	return v
}

func flattenNodes(nodes []InfraNodeView) []InfraNodeView {
	var flat []InfraNodeView
	for _, n := range nodes {
		flat = append(flat, InfraNodeView{ID: n.ID, Name: n.Name, TypeIcon: n.TypeIcon, TypeLabel: n.TypeLabel})
		flat = append(flat, flattenNodes(n.Children)...)
	}
	return flat
}

func (h *Handler) Infrastructure(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := InfraPageData{
		BaseData: baseData(r, "infrastructure", "Infrastruktur", "Schnellzugriff"),
	}

	if tree, err := h.infra.GetTree(ctx); err == nil {
		for _, i := range tree {
			data.Tree = append(data.Tree, infraNodeView(i))
		}
		data.AllNodes = flattenNodes(data.Tree)
	}

	if stats, err := h.infra.GetStats(ctx); err == nil {
		data.Stats = stats
	}

	h.render(w, "infrastructure", data)
}

func (h *Handler) InfraCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	parentID := r.FormValue("parent_id")
	in := &infrastructure.CreateInput{
		Name:         r.FormValue("name"),
		Type:         infrastructure.InfraType(r.FormValue("type")),
		Location:     r.FormValue("location"),
		Manufacturer: r.FormValue("manufacturer"),
		SerialNo:     r.FormValue("serial_no"),
		Description:  r.FormValue("description"),
		CostCenterID: optionalID(r.FormValue("cost_center_id")),
	}
	if parentID != "" {
		in.ParentID = &parentID
	}
	h.infra.Create(r.Context(), in)
	h.Infrastructure(w, r)
}

// ── Storage Page ──────────────────────────────────────────────
// Generischer Lagerort-Baum (lagerort -> regal -> fach -> platz), ersetzt die
// frühere feste 3-Ebenen-Struktur (Warehouse -> Location -> Place).

type StorageNodeView struct {
	ID             string
	Name           string
	Type           string
	TypeLabel      string
	TypeIcon       string
	Description    string
	Location       string
	Capacity       string
	CurrentParts   int
	ChildType      string
	ChildTypeLabel string
	Children       []StorageNodeView
}

type StoragePageData struct {
	BaseData
	Tree  []StorageNodeView
	Stats map[string]int
}

var storageTypeIcons = map[storage.NodeType]string{
	storage.TypeLagerort: "🏭",
	storage.TypeRegal:    "🗄️",
	storage.TypeFach:     "📁",
	storage.TypePlatz:    "📦",
}

var storageTypeLabels = map[storage.NodeType]string{
	storage.TypeLagerort: "Lagerort",
	storage.TypeRegal:    "Regal",
	storage.TypeFach:     "Fach",
	storage.TypePlatz:    "Platz",
}

// storageChildTypes: welcher Typ darf als Nächstes unter diesem Typ angelegt
// werden (steuert das "+"-Button-Label im Template). Platz ist die unterste
// Ebene und hat bewusst keinen Eintrag.
var storageChildTypes = map[storage.NodeType]storage.NodeType{
	storage.TypeLagerort: storage.TypeRegal,
	storage.TypeRegal:    storage.TypeFach,
	storage.TypeFach:     storage.TypePlatz,
}

func storageNodeView(n *storage.Node) StorageNodeView {
	v := StorageNodeView{
		ID:           n.ID,
		Name:         n.Name,
		Type:         string(n.Type),
		TypeLabel:    storageTypeLabels[n.Type],
		TypeIcon:     storageTypeIcons[n.Type],
		Description:  n.Description,
		Location:     n.Location,
		Capacity:     n.Capacity,
		CurrentParts: n.CurrentParts,
	}
	if childType, ok := storageChildTypes[n.Type]; ok {
		v.ChildType = string(childType)
		v.ChildTypeLabel = storageTypeLabels[childType]
	}
	for _, c := range n.Children {
		v.Children = append(v.Children, storageNodeView(c))
	}
	return v
}

func (h *Handler) StoragePage(w http.ResponseWriter, r *http.Request) {
	data := StoragePageData{
		BaseData: baseData(r, "storage", "Lagerverwaltung", "Übersicht"),
		Stats:    map[string]int{},
	}
	if tree, err := h.storage.GetTree(r.Context()); err == nil {
		for _, n := range tree {
			data.Tree = append(data.Tree, storageNodeView(n))
		}
	}
	if stats, err := h.storage.GetStats(r.Context()); err == nil {
		data.Stats = stats
	}
	h.render(w, "storage", data)
}

func (h *Handler) StorageCreateRoot(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	_, err := h.storage.Create(r.Context(), nil, r.FormValue("name"), "lagerort", r.FormValue("description"), r.FormValue("location"), "", u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // FIX: sonst haelt htmx den Fehler fuer einen Erfolg
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) StorageAddChild(w http.ResponseWriter, r *http.Request) {
	parentID := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	_, err := h.storage.Create(r.Context(), &parentID, r.FormValue("name"), r.FormValue("type"), r.FormValue("description"), "", r.FormValue("capacity"), u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // FIX: sonst haelt htmx den Fehler fuer einen Erfolg
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Users Page ────────────────────────────────────────────────

type ITPageData struct {
	BaseData
	Assets []ITAssetView
	Stats  map[string]int
	Filter string
}

type ITAssetView struct {
	ID           string
	Name         string
	TypeLabel    string
	TypeIcon     string
	StatusLabel  string
	StatusClass  string
	IPAddress    string
	Hostname     string
	Manufacturer string
	Model        string
	SerialNo     string
	Location     string
	InfraName    string
	Notes        string
}

type UsersPageData struct {
	BaseData
	Users       []UserView
	TotalUsers  int
	ActiveUsers int
	Filter      string
	RoleStats   []RoleStat
}

type UserView struct {
	ID         string
	Username   string
	Email      string
	FirstName  string
	LastName   string
	FullName   string
	Initials   string
	AvatarBg   string
	RoleValue  string
	RoleLabel  string
	RoleClass  string
	Department string
	Phone      string
	Active     bool

	OnCallDuty      bool
	ShiftLocksmith1 bool
	ShiftLocksmith2 bool
	Sharpening      bool
	HeatingFill     bool
	ShiftLeader     bool
}

type RoleStat struct {
	Icon  string
	Label string
	Count int
}

func userView(u *users.User) UserView {
	roleLabels := map[users.Role]string{
		"admin": "Administrator", "manager": "Manager", "technician": "Techniker",
		"worker": "Mitarbeiter", "viewer": "Betrachter",
	}
	roleClasses := map[users.Role]string{
		"admin": "b-red", "manager": "b-blue", "technician": "b-green",
		"worker": "b-gray", "viewer": "b-gray",
	}
	avatarBgs := []string{"#6366f1", "#10b981", "#f59e0b", "#ef4444", "#3b82f6", "#8b5cf6", "#ec4899"}
	initials := ""
	if len(u.FirstName) > 0 {
		initials += string([]rune(u.FirstName)[:1])
	}
	if len(u.LastName) > 0 {
		initials += string([]rune(u.LastName)[:1])
	}
	bg := avatarBgs[(len(u.FirstName)+len(u.LastName))%len(avatarBgs)]

	return UserView{
		ID: u.ID, Username: u.Username, Email: u.Email,
		FirstName: u.FirstName, LastName: u.LastName,
		FullName: u.FirstName + " " + u.LastName,
		Initials: initials, AvatarBg: bg,
		RoleValue: string(u.Role),
		RoleLabel: roleLabels[u.Role], RoleClass: roleClasses[u.Role],
		Department: u.Department, Phone: u.Phone, Active: u.Active,
		OnCallDuty: u.OnCallDuty, ShiftLocksmith1: u.ShiftLocksmith1, ShiftLocksmith2: u.ShiftLocksmith2,
		Sharpening: u.Sharpening, HeatingFill: u.HeatingFill, ShiftLeader: u.ShiftLeader,
	}
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("role")
	data := UsersPageData{
		BaseData: baseData(r, "users", "Benutzerverwaltung", "Rollen"),
		Filter:   filter,
	}

	allUsers, err := h.users.List(ctx)
	if err == nil {
		roleCounts := map[string]int{}
		for _, u := range allUsers {
			if !u.Active {
				continue
			}
			data.ActiveUsers++
			roleCounts[string(u.Role)]++
			if filter == "" || string(u.Role) == filter {
				data.Users = append(data.Users, userView(u))
			}
		}
		data.TotalUsers = len(allUsers)
		roleIcons := map[string]string{"admin": "👑", "manager": "📋", "technician": "🔧", "worker": "👷", "viewer": "👁️"}
		roleNames := map[string]string{"admin": "Admin", "manager": "Manager", "technician": "Techniker", "worker": "Mitarbeiter", "viewer": "Betrachter"}
		for _, role := range []string{"admin", "manager", "technician", "worker", "viewer"} {
			if roleCounts[role] > 0 {
				data.RoleStats = append(data.RoleStats, RoleStat{Icon: roleIcons[role], Label: roleNames[role], Count: roleCounts[role]})
			}
		}
	}
	h.render(w, "users", data)
}

func (h *Handler) UserCreateWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	in := &users.CreateUserInput{
		Username:   r.FormValue("username"),
		Email:      r.FormValue("email"),
		Password:   r.FormValue("password"),
		FirstName:  r.FormValue("first_name"),
		LastName:   r.FormValue("last_name"),
		Role:       users.Role(r.FormValue("role")),
		Department: r.FormValue("department"),
		Phone:      r.FormValue("phone"),
	}
	u, err := h.users.Register(r.Context(), in)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<tr><td colspan="6" style="color:var(--red);padding:10px">Fehler: `+esc(err.Error())+`</td></tr>`)
		return
	}
	v := userView(u)
	fmt.Fprintf(w, `<tr id="user-row-%s">
		<td><div style="display:flex;align-items:center;gap:10px">
			<div style="width:34px;height:34px;border-radius:50%%;background:%s;display:flex;align-items:center;justify-content:center;font-size:13px;font-weight:600;color:#fff">%s</div>
			<div><div style="font-weight:500">%s</div><div style="font-size:11px;color:var(--muted)">@%s</div></div></div></td>
		<td><span class="badge %s">%s</span></td>
		<td style="font-size:13px;color:var(--muted)">%s</td>
		<td style="font-size:12px">%s</td>
		<td><span style="font-size:12px;color:var(--green)">● Aktiv</span></td>
		<td></td></tr>`,
		esc(v.ID), esc(v.AvatarBg), esc(v.Initials), esc(v.FullName), esc(v.Username),
		esc(v.RoleClass), esc(v.RoleLabel), esc(v.Department), esc(v.Email))
}

func (h *Handler) UserUpdateWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := &users.User{
		ID:              id,
		FirstName:       r.FormValue("first_name"),
		LastName:        r.FormValue("last_name"),
		Role:            users.Role(r.FormValue("role")),
		Department:      r.FormValue("department"),
		Phone:           r.FormValue("phone"),
		OnCallDuty:      r.FormValue("on_call_duty") == "on",
		ShiftLocksmith1: r.FormValue("shift_locksmith_1") == "on",
		ShiftLocksmith2: r.FormValue("shift_locksmith_2") == "on",
		Sharpening:      r.FormValue("sharpening") == "on",
		HeatingFill:     r.FormValue("heating_fill") == "on",
		ShiftLeader:     r.FormValue("shift_leader") == "on",
	}
	h.users.Update(r.Context(), u)
	h.Users(w, r)
}

func (h *Handler) UserDeactivateWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.users.Deactivate(r.Context(), id)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<tr id="user-row-%s" style="opacity:.5">
		<td colspan="5" style="color:var(--muted);font-size:12px;padding:12px">Benutzer deaktiviert</td>
		<td></td></tr>`, id)
}

func (h *Handler) UserRoleWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "nicht gefunden", 404)
		return
	}
	u.Role = users.Role(r.FormValue("role"))
	h.users.Update(r.Context(), u)
	h.Users(w, r)
}

// ── Wiederhergestellte Handler ────────────────────────────────

func (h *Handler) FaultChatWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	message := r.FormValue("message")
	if message == "" {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "")
		return
	}
	reply, err := h.faults.Chat(r.Context(), id, "", message, nil)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Copilot nicht erreichbar</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="margin-bottom:10px"><div style="font-size:10px;color:var(--muted);margin-bottom:3px">Copilot</div><div style="color:var(--text);line-height:1.5;font-size:12px">`+esc(reply)+`</div></div>`)
}

func (h *Handler) TimeStopWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	h.time.Stop(r.Context(), id, u.ID, time.Time{})
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px 0"><i class="ti ti-check"></i> Zeit gestoppt</div>`)
}

func parseDateTimeLocal(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

func (h *Handler) TimeStartWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20) // FIX: r.ParseForm() parst kein multipart/form-data (FormData+fetch)
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefType(r.FormValue("ref_type")),
		RefID:       r.FormValue("ref_id"),
		Description: r.FormValue("description"),
	}
	if infraID := r.FormValue("infrastructure_id"); infraID != "" {
		in.InfrastructureID = &infraID
	}
	if in.RefType == "" || in.RefID == "" {
		http.Error(w, "Typ und Bezug fehlen", http.StatusBadRequest)
		return
	}
	if in.Description == "" {
		in.Description = timeRefLabel(in.RefType)
	}
	if _, err := h.time.Start(r.Context(), in, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TimeManualWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20) // FIX: r.ParseForm() parst kein multipart/form-data (FormData+fetch)
	u := getUser(r)
	startedAt, err := parseDateTimeLocal(r.FormValue("started_at"))
	if err != nil || startedAt.IsZero() {
		http.Error(w, "Startzeit ist ungültig", http.StatusBadRequest)
		return
	}
	endedAt, err := parseDateTimeLocal(r.FormValue("ended_at"))
	if err != nil || endedAt.IsZero() || !endedAt.After(startedAt) {
		http.Error(w, "Endzeit ist ungültig", http.StatusBadRequest)
		return
	}
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefType(r.FormValue("ref_type")),
		RefID:       r.FormValue("ref_id"),
		Description: r.FormValue("description"),
		StartedAt:   startedAt,
		EndedAt:     &endedAt,
	}
	if infraID := r.FormValue("infrastructure_id"); infraID != "" {
		in.InfrastructureID = &infraID
	}
	if in.RefType == "" || in.RefID == "" {
		http.Error(w, "Typ und Bezug fehlen", http.StatusBadRequest)
		return
	}
	if in.Description == "" {
		in.Description = timeRefLabel(in.RefType)
	}
	if _, err := h.time.Start(r.Context(), in, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TimeDeleteWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if err := h.time.Delete(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ChecklistsPage(w http.ResponseWriter, r *http.Request) {
	type checklistView struct {
		ID          string
		Name        string
		Description string
		Category    string
	}
	data := struct {
		BaseData
		Total      int
		Checklists []checklistView
	}{BaseData: baseData(r, "checklists", "Checklisten-Vorlagen", "Feldtypen")}
	if h.checks != nil {
		if list, err := h.checks.List(r.Context(), r.URL.Query().Get("category")); err == nil {
			data.Total = len(list)
			for _, c := range list {
				data.Checklists = append(data.Checklists, checklistView{
					ID: c.ID, Name: c.Name, Description: c.Description, Category: c.Category,
				})
			}
		}
	}
	h.render(w, "checklist_builder", data)
}

func (h *Handler) InfraDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	type InfraDetailData struct {
		BaseData
		Node     InfraNodeView
		Children []InfraNodeView
		Parent   *InfraNodeView
	}
	node, err := h.infra.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/infrastructure", http.StatusFound)
		return
	}
	data := InfraDetailData{
		BaseData: baseData(r, "infrastructure", node.Name, "Untergeordnete Anlagen"),
		Node:     infraNodeView(node),
	}
	if children, err := h.infra.List(ctx, &id, ""); err == nil {
		for _, c := range children {
			data.Children = append(data.Children, infraNodeView(c))
		}
	}
	if node.ParentID != nil {
		if parent, err := h.infra.GetByID(ctx, *node.ParentID); err == nil {
			pv := infraNodeView(parent)
			data.Parent = &pv
		}
	}
	t, _ := h.tmpl.Clone()
	t.ParseFiles("web/templates/infra_detail.gohtml")
	t.ExecuteTemplate(w, "base.gohtml", data)
}

func (h *Handler) InfraUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	in := &infrastructure.UpdateInput{
		Name: r.FormValue("name"), Location: r.FormValue("location"),
		Manufacturer: r.FormValue("manufacturer"), SerialNo: r.FormValue("serial_no"),
		Model: r.FormValue("model"), CostCenterID: optionalID(r.FormValue("cost_center_id")),
	}
	err := h.infra.Update(r.Context(), id, in)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px 0"><i class="ti ti-check"></i> Gespeichert</div>`)
}

func (h *Handler) ITPage(w http.ResponseWriter, r *http.Request) {
	data := ITPageData{
		BaseData: baseData(r, "it", "IT-Infrastruktur", "Übersicht"),
		Filter:   r.URL.Query().Get("type"), Stats: map[string]int{},
	}
	typeLabels := map[string]string{"server": "Server", "network": "Netzwerk", "workstation": "Workstation", "printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "other": "Sonstiges"}
	typeIcons := map[string]string{"server": "🖥️", "network": "🌐", "workstation": "💻", "printer": "🖨️", "phone": "📱", "tablet": "📟", "other": "📦"}
	statusLabels := map[string]string{"active": "Aktiv", "inactive": "Inaktiv", "maintenance": "Wartung", "retired": "Außer Dienst"}
	statusClasses := map[string]string{"active": "b-green", "inactive": "b-gray", "maintenance": "b-amber", "retired": "b-red"}
	if list, err := h.it.List(r.Context(), it.AssetType(data.Filter), ""); err == nil {
		for _, a := range list {
			data.Assets = append(data.Assets, ITAssetView{
				ID: a.ID, Name: a.Name,
				TypeLabel: typeLabels[string(a.Type)], TypeIcon: typeIcons[string(a.Type)],
				StatusLabel: statusLabels[string(a.Status)], StatusClass: statusClasses[string(a.Status)],
				IPAddress: a.IPAddress, Hostname: a.Hostname,
				Manufacturer: a.Manufacturer, Model: a.Model, SerialNo: a.SerialNo, Location: a.Location,
				InfraName: a.InfraName, Notes: a.Notes,
			})
		}
	}
	if stats, err := h.it.GetStats(r.Context()); err == nil {
		data.Stats = stats
	}
	h.render(w, "it", data)
}

func (h *Handler) ITCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	in := &it.CreateAssetInput{
		Name: r.FormValue("name"), Type: it.AssetType(r.FormValue("type")),
		Hostname: r.FormValue("hostname"), IPAddress: r.FormValue("ip_address"),
		Manufacturer: r.FormValue("manufacturer"), Model: r.FormValue("model"),
		SerialNo: r.FormValue("serial_no"), Location: r.FormValue("location"),
		OS: r.FormValue("os"), InfrastructureID: optionalID(r.FormValue("infrastructure_id")), Notes: r.FormValue("notes"),
	}
	a, err := h.it.Create(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, "<tr><td>Fehler: %s</td></tr>", err.Error())
		return
	}
	icons := map[string]string{"server": "🖥️", "network": "🌐", "workstation": "💻", "printer": "🖨️", "phone": "📱", "tablet": "📟", "other": "📦"}
	labels := map[string]string{"server": "Server", "network": "Netzwerk", "workstation": "Workstation", "printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "other": "Sonstiges"}
	t := string(a.Type)
	fmt.Fprintf(w, "<tr><td><b>%s</b><div style=\"font-size:11px;color:var(--muted)\">%s</div><div style=\"font-size:11px;color:var(--muted)\">%s</div></td><td>%s %s</td><td>%s</td><td>%s</td><td><span class=\"badge b-green\">Aktiv</span></td><td></td></tr>",
		esc(a.Name), esc(a.InfraName), esc(a.Notes), esc(icons[t]), esc(labels[t]), esc(a.IPAddress), esc(a.Location))
}

func (h *Handler) ITStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := it.AssetStatus(r.FormValue("status"))
	h.it.UpdateStatus(r.Context(), id, status)
	w.Header().Set("Content-Type", "text/html")
	statusClasses := map[string]string{"active": "b-green", "inactive": "b-gray", "maintenance": "b-amber", "retired": "b-red"}
	statusLabels := map[string]string{"active": "Aktiv", "inactive": "Inaktiv", "maintenance": "Wartung", "retired": "Außer Dienst"}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClasses[string(status)], statusLabels[string(status)])
}
