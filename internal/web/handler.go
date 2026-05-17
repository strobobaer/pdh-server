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
	"github.com/rs/zerolog/log"

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/shifts"
	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
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
	infra   *infrastructure.Service
	tickets *tickets.Service
	faults  *faults.Service
	maint   *maintenance.Service
	inv     *inventory.Service
	time    *timetracking.Service
}

func NewHandler(
	tmpl *template.Template,
	u *users.Service,
	s *shifts.Service,
	i *infrastructure.Service,
	t *tickets.Service,
	f *faults.Service,
	m *maintenance.Service,
	inv *inventory.Service,
	tt *timetracking.Service,
) *Handler {
	return &Handler{
		tmpl: tmpl, users: u, shifts: s, infra: i,
		tickets: t, faults: f, maint: m, inv: inv, time: tt,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.authMiddleware)

	r.Get("/", h.Dashboard)
	r.Get("/tickets", h.Tickets)
	r.Post("/tickets", h.CreateTicket)
	r.Put("/tickets/{id}/status", h.UpdateTicketStatus)
	r.Get("/faults", h.Faults)
	r.Post("/faults", h.CreateFault)
	r.Post("/faults/{id}/analyze", h.AnalyzeFault)
	r.Get("/inventory", h.Inventory)
	r.Post("/inventory", h.CreatePart)
	r.Get("/maintenance", h.Maintenance)
	r.Get("/shifts", h.Shifts)
	r.Get("/infrastructure", h.Infrastructure)
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
	files := []string{"web/templates/" + tmpl + ".gohtml"}
	if tmpl == "login" {
		// Login hat kein base - direkt rendern
		lt, _ := t.ParseFiles(files...)
		if err := lt.ExecuteTemplate(w, "login.gohtml", data); err != nil {
			http.Error(w, "Login: "+err.Error(), 500)
		}
		return
	}
	if _, err2 := t.ParseFiles(files...); err2 != nil {
		http.Error(w, "Parse: "+err2.Error(), 500); return
	}
	if err3 := t.ExecuteTemplate(w, "base", data); err3 != nil {
		log.Error().Err(err3).Str("template", tmpl).Msg("template error")
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
func (h *Handler) Maintenance(w http.ResponseWriter, r *http.Request)   { h.simplePage(w, r, "maintenance", "Wartungsplanung", "Fällige Aufträge") }
func (h *Handler) Shifts(w http.ResponseWriter, r *http.Request)        { h.simplePage(w, r, "shifts", "Schichtplanung", "Diese Woche") }
func (h *Handler) Infrastructure(w http.ResponseWriter, r *http.Request){ h.simplePage(w, r, "infrastructure", "Infrastruktur", "Topologie") }
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
