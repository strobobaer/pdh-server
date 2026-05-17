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

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/it"
	"pdh/internal/modules/inventory"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
)

// ── Template-Daten ───────────────────────────────────────────

type BaseData struct {
	Title        string
	Page         string
	ContextTitle string
	UserName     string
	UserFirstName string
	UserLastName  string
	FaultID      string
}

type DashboardData struct {
	BaseData
	Greeting      string
	DateStr       string
	WeekNumber    int
	WeekRange     string
	Stats         DashStats
	Faults        []FaultView
	MaintenanceDue []MaintenanceView
	OpenTickets   []TicketView
	WeekPlan      bool
	WeekDays      []ShiftDay
}

type DashStats struct {
	OpenTickets      int
	CriticalTickets  int
	ActiveFaults     int
	AnalyzingFaults  int
	MaintenanceDue   int
	InventoryValue   string
	LowStock         int
}

type FaultView struct {
	ID            string
	Title         string
	Status        string
	StatusLabel   string
	StatusClass   string
	Severity      string
	SeverityClass string
	InfraName     string
	DetectedAgo   string
	Confidence    float64
}

type TicketView struct {
	ID            string
	Title         string
	Description   string
	Priority      string
	PriorityClass string
	PriorityDot   string
	Status        string
	StatusLabel   string
	StatusClass   string
	CreatedAgo    string
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
	ID              string
	PartNumber      string
	Name            string
	Manufacturer    string
	Category        string
	Unit            string
	StockQty        string
	MinQty          string
	StorageLocation string
	StoragePlace    string
	Price           string
	Status          string
	StatusLabel     string
	StatusClass     string
	StatusDot       string
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct {
	tmpl    *template.Template
	users   *users.Service
	shifts  *shifts.Service
	storage *storage.Service
	infra   *infrastructure.Service
	tickets *tickets.Service
	faults  *faults.Service
	maint   *maintenance.Service
	inv     *inventory.Service
	it      *it.Service
	time    *timetracking.Service
}

func NewHandler(
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
) *Handler {
	return &Handler{
		tmpl: tmpl, users: u, shifts: s,
		storage: st, infra: i,
		tickets: t, faults: f, maint: m, inv: inv,
		it:  itt, time: tt,
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
	r.Put("/tickets/{id}/status-web", h.TicketStatusWeb)
	r.Post("/tickets/{id}/comment", h.TicketAddComment)
	r.Post("/tickets/{id}/time/start", h.TicketStartTime)
	r.Get("/faults", h.Faults)
	r.Post("/faults", h.CreateFault)
	r.Get("/faults/{id}", h.FaultDetail)
	r.Post("/faults/{id}/analyze", h.AnalyzeFault)
	r.Post("/faults/{id}/resolve", h.FaultResolve)
	r.Post("/faults/{id}/time/start", h.FaultStartTime)
	r.Get("/inventory", h.Inventory)
	r.Post("/inventory", h.CreatePart)
	r.Get("/inventory/{id}", h.InventoryDetail)
	r.Post("/inventory/book-web", h.InventoryBookWeb)
	r.Get("/maintenance", h.Maintenance)
	r.Post("/maintenance/plans", h.MaintenanceCreatePlan)
	r.Post("/maintenance/generate", h.MaintenanceGenerate)
	r.Get("/shifts", h.Shifts)
	r.Get("/infrastructure", h.Infrastructure)
	r.Get("/infrastructure/{id}", h.InfraDetail)
	r.Put("/infrastructure/{id}/edit", h.InfraUpdate)
	r.Get("/it", h.ITPage)
	r.Post("/it", h.ITCreate)
	r.Put("/it/{id}/status-web", h.ITStatusWeb)
	r.Get("/storage", h.StoragePage)
	r.Post("/storage", h.StorageCreateWarehouse)
	r.Post("/storage/{id}/locations-web", h.StorageAddLocation)
	r.Post("/storage/locations/{id}/places-web", h.StorageAddPlace)
	r.Get("/users", h.Users)
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
	if err != nil { http.Error(w, err.Error(), 500); return }
	if tmpl == "login" {
		lt, _ := t.ParseFiles("web/templates/login.gohtml")
		lt.ExecuteTemplate(w, "login.gohtml", data)
		return
	}
	if _, err2 := t.ParseFiles("web/templates/" + tmpl + ".gohtml"); err2 != nil {
		http.Error(w, "Parse: "+err2.Error(), 500); return
	}
	if err3 := t.ExecuteTemplate(w, "base.gohtml", data); err3 != nil {
		http.Error(w, "Template-Fehler: "+err3.Error(), 500)
	}
}

func greeting() string {
	h := time.Now().Hour()
	if h < 12 { return "Morgen" }
	if h < 18 { return "Tag" }
	return "Abend"
}
func timeAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute { return "gerade eben" }
	if d < time.Hour { return fmt.Sprintf("vor %dmin", int(d.Minutes())) }
	if d < 24*time.Hour { return fmt.Sprintf("vor %dh", int(d.Hours())) }
	return fmt.Sprintf("vor %dd", int(d.Hours()/24))
}
func severityClass(s string) string {
	switch s {
	case "critical": return "b-red"
	case "high":     return "b-red"
	case "medium":   return "b-amber"
	default:         return "b-gray"
	}
}

func priorityClass(p string) string {
	switch p {
	case "critical": return "b-red"
	case "high":     return "b-red"
	case "medium":   return "b-amber"
	default:         return "b-blue"
	}
}

func priorityDot(p string) string {
	switch p {
	case "critical", "high": return "d-red"
	case "medium":            return "d-amber"
	default:                  return "d-blue"
	}
}

func statusClass(s string) string {
	switch s {
	case "resolved", "closed", "done": return "b-green"
	case "in_progress":                return "b-blue"
	case "open", "detected":           return "b-amber"
	default:                           return "b-gray"
	}
}

func statusLabel(s string) string {
	labels := map[string]string{
		"open": "Offen", "in_progress": "In Arbeit",
		"resolved": "Gelöst", "closed": "Geschlossen",
		"detected": "Erkannt", "analyzing": "Analysiert",
		"pending": "Ausstehend",
	}
	if l, ok := labels[s]; ok { return l }
	return s
}

func getUser(r *http.Request) *users.User {
	if u, ok := r.Context().Value("user").(*users.User); ok { return u }
	return &users.User{FirstName: "Gast", LastName: "", Role: "viewer"}
}

func baseData(r *http.Request, page, title, ctxTitle string) BaseData {
	u := getUser(r)
	return BaseData{
		Title: title, Page: page,
		ContextTitle: ctxTitle,
		UserName:      u.FirstName + " " + u.LastName,
		UserFirstName: u.FirstName,
		UserLastName:  u.LastName,
	}
}

// ── Seiten ───────────────────────────────────────────────────

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	now := time.Now()
	_, week := now.ISOWeek()

	data := DashboardData{
		BaseData:  baseData(r, "dashboard", "Dashboard", "Offene Tickets"),
		Greeting:  greeting(),
		DateStr:   now.Format("Mo 02. January 2006"),
		WeekNumber: week,
		WeekRange: fmt.Sprintf("%s–%s",
			now.AddDate(0,0,-int(now.Weekday())+1).Format("02.01"),
			now.AddDate(0,0,7-int(now.Weekday())).Format("02.01")),
	}

	// Störungen
	if fl, err := h.faults.List(ctx, ""); err == nil {
		for _, f := range fl {
			if f.Status == "detected" || f.Status == "in_progress" || f.Status == "analyzing" {
				data.Stats.ActiveFaults++
				if f.Status == "analyzing" { data.Stats.AnalyzingFaults++ }
			}
			if len(data.Faults) < 4 && (f.Status == "detected" || f.Status == "in_progress") {
				data.Faults = append(data.Faults, FaultView{
					ID: f.ID, Title: f.Title,
					Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
					StatusClass: statusClass(string(f.Status)),
					Severity: string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
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
				if t.Priority == "critical" { data.Stats.CriticalTickets++ }
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
		days := []string{"Mo","Di","Mi","Do","Fr","Sa","So"}
		shiftClasses := map[string]string{"F":"sf","S":"ss","N":"sn"}
		for i, u2 := range wp.Users {
			if i == 0 || u2.UserID == u.ID {
				data.WeekPlan = true
				for d := 0; d < 7; d++ {
					date := monday.AddDate(0, 0, d).Format("2006-01-02")
					label, class := "–", "se"
					if entry, ok := u2.Days[date]; ok && entry.ShortName != "" {
						label = entry.ShortName
						if c, ok := shiftClasses[entry.ShortName]; ok { class = c }
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
	}

	if fl, err := h.faults.List(ctx, faults.FaultStatus(filter)); err == nil {
		data.Total = len(fl)
		for _, f := range fl {
			if f.Status == "detected" || f.Status == "in_progress" { data.Open++ }
			data.Faults = append(data.Faults, FaultView{
				ID: f.ID, Title: f.Title,
				Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
				StatusClass: statusClass(string(f.Status)),
				Severity: string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
				DetectedAgo: timeAgo(f.DetectedAt),
			})
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
	}

	if tl, err := h.tickets.List(ctx, tickets.Status(filter)); err == nil {
		data.Total = len(tl)
		for _, t := range tl {
			if t.Status == "open" || t.Status == "in_progress" { data.Open++ }
			tv := TicketView{
				ID: t.ID, Title: t.Title, Description: t.Description,
				Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
				PriorityDot: priorityDot(string(t.Priority)),
				Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
				StatusClass: statusClass(string(t.Status)),
				CreatedAgo: timeAgo(t.CreatedAt),
			}
			data.Tickets = append(data.Tickets, tv)
			if t.Priority == "critical" { data.CriticalTickets = append(data.CriticalTickets, tv) }
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
		if v, ok := stats["total"].(int); ok { data.Stats.Total = v }
		if v, ok := stats["low_stock"].(int); ok { data.Stats.LowStock = v }
		if v, ok := stats["critical"].(int); ok { data.Stats.Critical = v }
		if v, ok := stats["empty"].(int); ok { data.Stats.Empty = v }
		if v, ok := stats["total_value"].(float64); ok { data.Stats.TotalValue = fmt.Sprintf("%.0f", v) }
	}

	statusLabels := map[string]string{"ok":"OK","low":"Niedrig","critical":"Kritisch","empty":"Leer"}
	statusClasses := map[string]string{"ok":"b-green","low":"b-amber","critical":"b-red","empty":"b-red"}
	statusDots := map[string]string{"ok":"d-green","low":"d-amber","critical":"d-red","empty":"d-red"}

	if parts, err := h.inv.List(ctx, "", "", ""); err == nil {
		for _, p := range parts {
			st := string(p.Status)
			pv := PartView{
				ID: p.ID, PartNumber: p.PartNumber, Name: p.Name,
				Manufacturer: p.Manufacturer, Category: p.Category, Unit: p.Unit,
				StockQty: strconv.FormatFloat(p.StockQty, 'f', 1, 64),
				MinQty:   strconv.FormatFloat(p.MinQty, 'f', 1, 64),
				StorageLocation: p.StorageLocation, StoragePlace: p.StoragePlace,
				Status: st,
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
		if s := strings.TrimSpace(s); s != "" { symptoms = append(symptoms, s) }
	}
	in := &faults.CreateFaultInput{
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		Symptoms:    symptoms,
		Severity:    faults.Severity(r.FormValue("severity")),
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
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		Priority:    tickets.Priority(r.FormValue("priority")),
	}
	h.tickets.Create(r.Context(), in, u.ID)
	h.Tickets(w, r)
}

func (h *Handler) UpdateTicketStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	h.tickets.UpdateStatus(r.Context(), id, status)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<tr><td colspan="5" style="color:var(--green);padding:8px 12px"><i class="ti ti-check"></i> Status aktualisiert</td></tr>`)
}

func (h *Handler) CreatePart(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	minQty, _ := strconv.ParseFloat(r.FormValue("min_qty"), 64)
	initStock, _ := strconv.ParseFloat(r.FormValue("initial_stock"), 64)
	in := &inventory.CreatePartInput{
		PartNumber:      r.FormValue("part_number"),
		Name:            r.FormValue("name"),
		Manufacturer:    r.FormValue("manufacturer"),
		Category:        r.FormValue("category"),
		StorageLocation: r.FormValue("storage_location"),
		StoragePlace:    r.FormValue("storage_place"),
		MinQty:          minQty,
		InitialStock:    initStock,
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
	if len(q) < 2 { w.Write([]byte("")); return }
	w.Header().Set("Content-Type", "text/html")

	results, err := h.infra.Search(r.Context(), q)
	if err != nil || len(results) == 0 {
		fmt.Fprintf(w, `<div style="color:var(--muted);padding:4px 0;font-size:11px">Keine Ergebnisse</div>`)
		return
	}
	for _, item := range results {
		fmt.Fprintf(w, `<div class="act-item"><div class="act-dot d-blue"></div><div class="act-text">%s</div><div class="act-time" style="font-size:10px">%s</div></div>`, item.Name, item.Type)
	}
}

// Platzhalter-Seiten

func (h *Handler) Users(w http.ResponseWriter, r *http.Request)         { h.simplePage(w, r, "users", "Benutzer", "Aktive User") }
func (h *Handler) TimeTracking(w http.ResponseWriter, r *http.Request)  { h.simplePage(w, r, "time", "Zeiterfassung", "Heute") }

func (h *Handler) simplePage(w http.ResponseWriter, r *http.Request, page, title, ctxTitle string) {
	h.render(w, "simple", struct{BaseData}{baseData(r, page, title, ctxTitle)})
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
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: user.ID, Path: "/", MaxAge: 86400})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" { next.ServeHTTP(w, r); return }
		cookie, err := r.Cookie("pdh_token")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		// User-ID aus Cookie lesen
		if uidCookie, err := r.Cookie("pdh_user_id"); err == nil {
			if user, err := h.users.GetByID(r.Context(), uidCookie.Value); err == nil {
				ctx := context.WithValue(r.Context(), "user", user)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ── Fault Detail ─────────────────────────────────────────────

type FaultDetailData struct {
	BaseData
	Fault        FaultDetailView
	Analysis     *faults.CopilotAnalysis
	TimeEntries  []*timetracking.TimeEntry
	RunningTime  *timetracking.TimeEntry
	SimilarFaults []SimilarFaultView
}

type FaultDetailView struct {
	ID          string
	Title       string
	Description string
	Symptoms    []string
	Status      string
	StatusLabel string
	StatusClass string
	Severity    string
	SeverityClass string
	InfraName   string
	DetectedAgo string
	Resolution  string
	RootCause   string
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
	data := FaultDetailData{
		BaseData: BaseData{
			Title: fault.Title, Page: "faults",
			ContextTitle: "Ähnliche Störungen",
			UserName: u.FirstName + " " + u.LastName,
			UserFirstName: u.FirstName, UserLastName: u.LastName,
			FaultID: id,
		},
		Fault: FaultDetailView{
			ID: fault.ID, Title: fault.Title,
			Description: fault.Description,
			Symptoms: fault.Symptoms,
			Status: string(fault.Status),
			StatusLabel: statusLabel(string(fault.Status)),
			StatusClass: statusClass(string(fault.Status)),
			Severity: string(fault.Severity),
			SeverityClass: severityClass(string(fault.Severity)),
			DetectedAgo: timeAgo(fault.DetectedAt),
		},
	}
	if fault.Resolution != nil { data.Fault.Resolution = *fault.Resolution }
	if fault.RootCause != nil  { data.Fault.RootCause = *fault.RootCause }

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
	h.faults.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"))
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Störung gelöst! <a href="/faults" style="color:var(--accent)">Zurück zur Liste</a></div>`)
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
	TotalPlans int
	OpenTasks  int
	Today      string
	Plans      []MaintPlanView
	Tasks      []MaintTaskView
	DueTasks   []MaintTaskView
	InfraOptions []InfraOption
}

type InfraOption struct{ ID, Name string }

type MaintPlanView struct {
	ID            string
	Name          string
	InfraName     string
	TypeLabel     string
	IntervalLabel string
	NextDue       string
	Priority      string
	PriorityDot   string
}

type MaintTaskView struct {
	ID           string
	Title        string
	InfraName    string
	Status       string
	StatusLabel  string
	StatusClass  string
	Priority     string
	PriorityClass string
	PriorityDot  string
	DueDate      string
}

func maintTaskView(t *maintenance.MaintenanceTask) MaintTaskView {
	return MaintTaskView{
		ID: t.ID, Title: t.Title, InfraName: t.InfraName,
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
		StatusClass: statusClass(string(t.Status)),
		Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot: priorityDot(string(t.Priority)),
		DueDate: t.DueDate.Format("02.01."),
	}
}

func (h *Handler) Maintenance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := maintenance.TaskStatus(r.URL.Query().Get("status"))

	data := MaintenancePageData{
		BaseData: baseData(r, "maintenance", "Wartungsplanung", "Fällige Aufträge"),
		Today:    time.Now().Format("2006-01-02"),
	}

	if plans, err := h.maint.ListPlans(ctx, ""); err == nil {
		data.TotalPlans = len(plans)
		intervalLabels := map[maintenance.Interval]string{
			"daily":"Täglich","weekly":"Wöchentlich","monthly":"Monatlich",
			"quarterly":"Quartalsweise","yearly":"Jährlich",
		}
		typeLabels := map[maintenance.PlanType]string{
			"preventive":"Vorbeugend","inspection":"Inspektion",
			"calibration":"Kalibrierung","cleaning":"Reinigung",
		}
		for _, p := range plans {
			data.Plans = append(data.Plans, MaintPlanView{
				ID: p.ID, Name: p.Name, InfraName: p.InfraName,
				TypeLabel: typeLabels[p.Type],
				IntervalLabel: intervalLabels[p.Interval],
				NextDue: p.NextDueAt.Format("02.01.2006"),
				Priority: string(p.Priority),
				PriorityDot: priorityDot(string(p.Priority)),
			})
		}
	}

	if tasks, err := h.maint.ListTasks(ctx, status, ""); err == nil {
		for _, t := range tasks {
			data.Tasks = append(data.Tasks, maintTaskView(t))
			if t.Status == "open" { data.OpenTasks++ }
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
	r.ParseForm()
	u := getUser(r)
	in := &maintenance.CreatePlanInput{
		Name:             r.FormValue("name"),
		Type:             maintenance.PlanType(r.FormValue("type")),
		InfrastructureID: r.FormValue("infrastructure_id"),
		Interval:         maintenance.Interval(r.FormValue("interval")),
		Priority:         maintenance.Priority(r.FormValue("priority")),
		FirstDueAt:       r.FormValue("first_due_at"),
	}
	h.maint.CreatePlan(r.Context(), in, u.ID)
	h.Maintenance(w, r)
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
	UserName   string
	TypeLabel  string
	TypeDot    string
	StartDate  string
	EndDate    string
	Days       int
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
	if weekParam == "" { weekParam = time.Now().Format("2006-01-02") }

	t, _ := time.Parse("2006-01-02", weekParam)
	wd := int(t.Weekday()); if wd == 0 { wd = 7 }
	monday := t.AddDate(0, 0, -(wd-1))
	sunday := monday.AddDate(0, 0, 6)
	_, week := monday.ISOWeek()

	dayNames := []string{"Mo","Di","Mi","Do","Fr","Sa","So"}
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

	shiftClasses := map[string]string{"F":"sf","S":"ss","N":"sn"}

	if wp, err := h.shifts.GetWeekPlan(ctx, monday.Format("2006-01-02")); err == nil && wp != nil {
		data.Users = wp.Users
		for _, u := range wp.Users {
			for date, entry := range u.Days {
				class := shiftClasses[entry.ShortName]
				if class == "" { class = "se" }
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

	absTypeLabels := map[string]string{"vacation":"Urlaub","sick":"Krank","training":"Schulung","other":"Sonstiges"}
	absTypeDots   := map[string]string{"vacation":"d-blue","sick":"d-red","training":"d-green","other":"d-amber"}
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
}

type CommentView struct {
	ID        string
	UserName  string
	Text      string
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
	if err != nil { http.Redirect(w, r, "/tickets", http.StatusFound); return }

	data := TicketDetailData{
		BaseData: baseData(r, "tickets", t.Title, "Ähnliche Tickets"),
		Ticket: TicketView{
			ID: t.ID, Title: t.Title, Description: t.Description,
			Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
			PriorityDot: priorityDot(string(t.Priority)),
			Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
			StatusClass: statusClass(string(t.Status)),
			CreatedAgo: timeAgo(t.CreatedAt),
		},
		StatusOptions: []StatusOption{
			{"open","Offen"},{"in_progress","In Arbeit"},
			{"resolved","Gelöst"},{"closed","Geschlossen"},
		},
	}

	if running, err := h.time.GetRunning(ctx, u.ID); err == nil { data.RunningTime = running }
	if entries, err := h.time.ListByRef(ctx, timetracking.RefTicket, id); err == nil { data.TimeEntries = entries }
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
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red)">Fehler</div>`); return }
	fmt.Fprintf(w, `<div style="display:flex;gap:10px;padding:10px 0;border-bottom:1px solid var(--border)">
		<div style="width:30px;height:30px;background:var(--accent);border-radius:50%;display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:600;flex-shrink:0;color:#fff">%s</div>
		<div><div style="font-size:12px;font-weight:500">%s · <span style="color:var(--muted);font-weight:400">gerade eben</span></div>
		<div style="font-size:13px;margin-top:4px">%s</div></div></div>`,
		string([]rune(u.FirstName)[:1]), u.FirstName+" "+u.LastName, c.Text)
}

func (h *Handler) TicketStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	h.tickets.UpdateStatus(r.Context(), id, status)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClass(string(status)), statusLabel(string(status)))
}

func (h *Handler) TicketStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{RefType: timetracking.RefTicket, RefID: id, Description: "Ticket bearbeiten"}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`); return }
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
	if err != nil { http.Redirect(w, r, "/inventory", http.StatusFound); return }

	statusLabels := map[string]string{"ok":"OK","low":"Niedrig","critical":"Kritisch","empty":"Leer"}
	statusClasses := map[string]string{"ok":"b-green","low":"b-amber","critical":"b-red","empty":"b-red"}
	statusDots := map[string]string{"ok":"d-green","low":"d-amber","critical":"d-red","empty":"d-red"}
	st := string(p.Status)

	pv := PartView{
		ID: p.ID, PartNumber: p.PartNumber, Name: p.Name,
		Manufacturer: p.Manufacturer, Category: p.Category, Unit: p.Unit,
		StockQty: fmt.Sprintf("%.2f", p.StockQty),
		MinQty:   fmt.Sprintf("%.2f", p.MinQty),
		StorageLocation: p.StorageLocation, StoragePlace: p.StoragePlace,
		Price: fmt.Sprintf("%.2f", p.Price),
		Status: st, StatusLabel: statusLabels[st],
		StatusClass: statusClasses[st], StatusDot: statusDots[st],
	}

	data := InventoryDetailData{
		BaseData: baseData(r, "inventory", p.Name, "Unter Mindestbestand"),
		Part: pv,
	}

	if mv, err := h.inv.GetMovements(ctx, id); err == nil { data.Movements = mv }
	if parts, err := h.inv.GetLowStock(ctx); err == nil {
		for _, lp := range parts {
			if lp.ID != id && len(data.LowStockParts) < 5 {
				lst := string(lp.Status)
				data.LowStockParts = append(data.LowStockParts, PartView{
					ID: lp.ID, Name: lp.Name,
					StockQty: fmt.Sprintf("%.1f", lp.StockQty),
					Status: lst, StatusDot: statusDots[lst],
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
		PartID:    r.FormValue("part_id"),
		Type:      inventory.MovementType(r.FormValue("type")),
		Qty:       qty,
		Reference: r.FormValue("reference"),
		Notes:     r.FormValue("notes"),
	}
	mv, err := h.inv.Book(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: `+err.Error()+`</div>`); return }
	typeLabel := map[inventory.MovementType]string{"in":"Zugang","out":"Abgang","correction":"Korrektur"}
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
	Children     []InfraNodeView
}

func infraNodeView(i *infrastructure.Infrastructure) InfraNodeView {
	icons := map[infrastructure.InfraType]string{
		"building":"🏭","line":"🔄","plant":"⚙️","device":"🔌",
	}
	labels := map[infrastructure.InfraType]string{
		"building":"Gebäude","line":"Linie","plant":"Anlage","device":"Gerät",
	}
	bgs := map[infrastructure.InfraType]string{
		"building":"rgba(99,102,241,.2)","line":"rgba(79,110,247,.2)",
		"plant":"rgba(16,185,129,.2)","device":"rgba(245,158,11,.2)",
	}
	v := InfraNodeView{
		ID: i.ID, Name: i.Name,
		TypeLabel: labels[i.Type], TypeIcon: icons[i.Type],
		TypeBg: bgs[i.Type],
		Location: i.Location, Manufacturer: i.Manufacturer,
		SerialNo: i.SerialNo,
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
	}
	if parentID != "" { in.ParentID = &parentID }
	h.infra.Create(r.Context(), in)
	h.Infrastructure(w, r)
}

// ── Storage Page ──────────────────────────────────────────────

type StoragePageData struct {
	BaseData
	Warehouses []*storage.Warehouse
	Stats      map[string]int
}

func (h *Handler) StoragePage(w http.ResponseWriter, r *http.Request) {
	data := StoragePageData{
		BaseData: baseData(r, "storage", "Lagerverwaltung", "Übersicht"),
		Stats:    map[string]int{},
	}
	if list, err := h.storage.ListWarehouses(r.Context()); err == nil { data.Warehouses = list }
	if stats, err := h.storage.GetStats(r.Context()); err == nil { data.Stats = stats }
	h.render(w, "storage", data)
}

func (h *Handler) StorageCreateWarehouse(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	wh, err := h.storage.CreateWarehouse(r.Context(), r.FormValue("name"), r.FormValue("description"), r.FormValue("location"), u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: `+err.Error()+`</div>`); return }
	fmt.Fprintf(w, `<div class="card" style="margin-bottom:14px">
		<div style="display:flex;align-items:center;gap:12px">
		<div style="width:36px;height:36px;background:rgba(79,110,247,.2);border-radius:10px;display:flex;align-items:center;justify-content:center;font-size:20px">🏭</div>
		<div><div style="font-size:15px;font-weight:500">%s</div><div style="font-size:12px;color:var(--muted)">%s</div></div></div>
		<div id="locs-%s" style="margin-top:14px"><div style="color:var(--muted);font-size:12px;text-align:center;padding:16px">Noch keine Lagerorte</div></div>
		</div>`, wh.Name, wh.Location, wh.ID)
}

func (h *Handler) StorageAddLocation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	loc, err := h.storage.CreateLocation(r.Context(), id, r.FormValue("name"), r.FormValue("description"))
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red)">Fehler</div>`); return }
	fmt.Fprintf(w, `<div style="border:1px solid var(--border);border-radius:8px;margin-bottom:8px;overflow:hidden">
		<div style="display:flex;align-items:center;gap:8px;padding:10px 12px;background:var(--bg3)">
		<div style="width:24px;height:24px;background:rgba(16,185,129,.2);border-radius:6px;display:flex;align-items:center;justify-content:center">📦</div>
		<div style="flex:1"><div style="font-size:13px;font-weight:500">%s</div><div style="font-size:11px;color:var(--muted)">%s</div></div>
		<button class="btn" style="font-size:11px;padding:3px 8px" onclick="showAddPlace('%s')"><i class="ti ti-plus"></i>Platz</button></div>
		<div id="add-place-%s" style="display:none;padding:10px;background:var(--bg3);border-top:1px solid var(--border)">
		<form hx-post="/storage/locations/%s/places-web" hx-target="#places-%s" hx-swap="beforeend" style="display:flex;gap:6px">
		<input name="name" class="form-input" placeholder="z.B. Fach 1" required style="flex:1;font-size:12px">
		<input name="capacity" class="form-input" placeholder="Kapazität" style="width:100px;font-size:12px">
		<button type="submit" class="btn btn-primary" style="font-size:11px">OK</button></form></div>
		<div id="places-%s" style="padding:8px 12px;display:grid;grid-template-columns:repeat(auto-fill,minmax(120px,1fr));gap:6px">
		<div style="color:var(--muted);font-size:11px;padding:8px 0;grid-column:1/-1">Noch keine Plätze</div></div></div>`,
		loc.Name, loc.Description, loc.ID, loc.ID, loc.ID, loc.ID, loc.ID)
}

func (h *Handler) StorageAddPlace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	p, err := h.storage.CreatePlace(r.Context(), id, r.FormValue("name"), r.FormValue("description"), r.FormValue("capacity"))
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, `<div style="color:var(--red)">Fehler</div>`); return }
	fmt.Fprintf(w, `<div style="background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:8px;text-align:center;cursor:pointer" onclick="selectPlace('%s','%s')">
		<div style="font-size:11px;font-weight:500;color:var(--text)">%s</div>
		%s</div>`,
		p.ID, p.Name, p.Name,
		func() string {
			if r.FormValue("capacity") != "" {
				return `<div style="font-size:10px;color:var(--muted);margin-top:2px">` + r.FormValue("capacity") + `</div>`
			}
			return ""
		}())
}

// ── IT-Infrastruktur Page ─────────────────────────────────────

type ITPageData struct {
	BaseData
	Assets  []ITAssetView
	Stats   map[string]int
	Filter  string
}

type ITAssetView struct {
	ID          string
	Name        string
	TypeLabel   string
	TypeIcon    string
	StatusLabel string
	StatusClass string
	IPAddress   string
	Hostname    string
	Manufacturer string
	Model       string
	SerialNo    string
	Location    string
}

func (h *Handler) ITPage(w http.ResponseWriter, r *http.Request) {
	data := ITPageData{
		BaseData: baseData(r, "it", "IT-Infrastruktur", "Übersicht"),
		Filter:   r.URL.Query().Get("type"),
		Stats:    map[string]int{},
	}
	typeLabels := map[string]string{"server":"Server","network":"Netzwerk","workstation":"Workstation","printer":"Drucker","phone":"Telefon","tablet":"Tablet","other":"Sonstiges"}
	typeIcons  := map[string]string{"server":"🖥️","network":"🌐","workstation":"💻","printer":"🖨️","phone":"📱","tablet":"📟","other":"📦"}
	statusLabels := map[string]string{"active":"Aktiv","inactive":"Inaktiv","maintenance":"Wartung","retired":"Außer Dienst"}
	statusClasses := map[string]string{"active":"b-green","inactive":"b-gray","maintenance":"b-amber","retired":"b-red"}

	if list, err := h.it.List(r.Context(), it.AssetType(data.Filter), ""); err == nil {
		for _, a := range list {
			data.Assets = append(data.Assets, ITAssetView{
				ID: a.ID, Name: a.Name,
				TypeLabel: typeLabels[string(a.Type)], TypeIcon: typeIcons[string(a.Type)],
				StatusLabel: statusLabels[string(a.Status)], StatusClass: statusClasses[string(a.Status)],
				IPAddress: a.IPAddress, Hostname: a.Hostname,
				Manufacturer: a.Manufacturer, Model: a.Model,
				SerialNo: a.SerialNo, Location: a.Location,
			})
		}
	}
	if stats, err := h.it.GetStats(r.Context()); err == nil { data.Stats = stats }
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
		OS: r.FormValue("os"), Notes: r.FormValue("notes"),
	}
	a, err := h.it.Create(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil { fmt.Fprintf(w, "<tr><td>Fehler: %s</td></tr>", err.Error()); return }
	icons := map[string]string{"server":"🖥️","network":"🌐","workstation":"💻","printer":"🖨️","phone":"📱","tablet":"📟","other":"📦"}
	labels := map[string]string{"server":"Server","network":"Netzwerk","workstation":"Workstation","printer":"Drucker","phone":"Telefon","tablet":"Tablet","other":"Sonstiges"}
	t := string(a.Type)
	fmt.Fprintf(w, "<tr><td><b>%s</b><br><small>%s %s</small></td><td>%s %s</td><td>%s</td><td>%s</td><td><span class=\"badge b-green\">Aktiv</span></td><td></td></tr>",
		a.Name, a.Manufacturer, a.Model, icons[t], labels[t], a.IPAddress, a.Location)
}

func (h *Handler) ITStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := it.AssetStatus(r.FormValue("status"))
	h.it.UpdateStatus(r.Context(), id, status)
	w.Header().Set("Content-Type", "text/html")
	statusClasses := map[string]string{"active":"b-green","inactive":"b-gray","maintenance":"b-amber","retired":"b-red"}
	statusLabels := map[string]string{"active":"Aktiv","inactive":"Inaktiv","maintenance":"Wartung","retired":"Außer Dienst"}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClasses[string(status)], statusLabels[string(status)])
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
	if err != nil { http.Redirect(w, r, "/infrastructure", http.StatusFound); return }

	data := InfraDetailData{
		BaseData: baseData(r, "infrastructure", node.Name, "Untergeordnete Anlagen"),
		Node:     infraNodeView(node),
	}

	// Kinder laden
	if children, err := h.infra.List(ctx, &id, ""); err == nil {
		for _, c := range children {
			data.Children = append(data.Children, infraNodeView(c))
		}
	}

	// Übergeordnet laden
	if node.ParentID != nil {
		if parent, err := h.infra.GetByID(ctx, *node.ParentID); err == nil {
			pv := infraNodeView(parent)
			data.Parent = &pv
		}
	}

	// Upload-Karte + Attach-Liste inline rendern
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, _ := h.tmpl.Clone()
	t.ParseFiles("web/templates/infra_detail.gohtml")
	t.ExecuteTemplate(w, "base.gohtml", data)
}

func (h *Handler) InfraUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	in := &infrastructure.UpdateInput{
		Name:         r.FormValue("name"),
		Location:     r.FormValue("location"),
		Manufacturer: r.FormValue("manufacturer"),
		SerialNo:     r.FormValue("serial_no"),
		Model:        r.FormValue("model"),
	}
	err := h.infra.Update(r.Context(), id, in)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: `+err.Error()+`</div>`)
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px 0"><i class="ti ti-check"></i> Gespeichert</div>`)
}
