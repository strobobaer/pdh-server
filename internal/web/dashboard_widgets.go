package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Dashboard-Widgets: Jeder Benutzer stellt sich sein Dashboard aus einem
// Katalog zusammen (Schnellzugriffe, Kennzahlen, Listen, Persoenliches).
// Die Zusammenstellung liegt als JSON am Konto (users.dashboard_widgets);
// NULL = Standard. Jedes Widget laedt seinen Inhalt per htmx nach
// (GET /dashboard/w/{id}) und aktualisiert sich regelmaessig. Widgets mit
// Berechtigung erscheinen nur, wenn der Benutzer sie hat.

// WidgetInstance: ein Widget auf dem Dashboard eines Benutzers.
type WidgetInstance struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Size   int               `json:"size"` // Spalten 1–4
	Config map[string]string `json:"config,omitempty"`
}

// WidgetDef: Eintrag im Widget-Katalog.
type WidgetDef struct {
	Type, Name, Icon, Category, Desc string
	Size                             int    // Standardbreite in Spalten (1–4)
	Perm                             string // noetige Berechtigung ("" = alle)
	Config                           string // "" | "links" | "note"
	Refresh                          int    // Sekunden bis zur Aktualisierung (0 = nie)
	load                             func(h *Handler, ctx context.Context, uid string, inst WidgetInstance, can func(string) bool) (any, error)
}

const (
	catQuick    = "Schnellzugriff"
	catStats    = "Kennzahlen"
	catLists    = "Listen"
	catPersonal = "Persönlich"
	maxWidgets  = 30
)

var widgetCategories = []string{catQuick, catStats, catLists, catPersonal}

var widgetDefs = []WidgetDef{
	{Type: "quick_actions", Name: "Schnellaktionen", Icon: "ti-bolt", Category: catQuick, Size: 2, Desc: "Große Knöpfe für das, was du oft brauchst: melden, Zeit erfassen, Ersatzteil suchen …", load: loadQuickActions},
	{Type: "quick_links", Name: "Eigene Links", Icon: "ti-link", Category: catQuick, Size: 1, Config: "links", Desc: "Deine Lesezeichen – Seiten im PDH oder im Netz", load: loadQuickLinks},
	{Type: "stat_tickets", Name: "Offene Tickets", Icon: "ti-ticket", Category: catStats, Size: 1, Perm: "tickets.view", Refresh: 60, Desc: "Anzahl offener Tickets, davon kritisch", load: loadStatTickets},
	{Type: "stat_faults", Name: "Aktive Störungen", Icon: "ti-alert-triangle", Category: catStats, Size: 1, Perm: "faults.view", Refresh: 30, Desc: "Störungen, die noch nicht behoben sind, davon neu gemeldet", load: loadStatFaults},
	{Type: "stat_maintenance", Name: "Wartung fällig", Icon: "ti-tool", Category: catStats, Size: 1, Perm: "maintenance.view", Refresh: 300, Desc: "Bis heute fällige Wartungen, davon überfällig", load: loadStatMaintenance},
	{Type: "stat_stock", Name: "Nachbestellen", Icon: "ti-package", Category: catStats, Size: 1, Perm: "inventory.view", Refresh: 300, Desc: "Ersatzteile unter Mindestbestand, davon kritisch", load: loadStatStock},
	{Type: "stat_my_tasks", Name: "Meine Aufgaben", Icon: "ti-list-check", Category: catStats, Size: 1, Refresh: 120, Desc: "Deine offenen Aufgaben, davon überfällig", load: loadStatMyTasks},
	{Type: "stat_my_hours", Name: "Meine Stunden", Icon: "ti-clock", Category: catStats, Size: 1, Refresh: 120, Desc: "Erfasste Zeit diese Woche und heute", load: loadStatMyHours},
	{Type: "chart_trend", Name: "Verlauf 14 Tage", Icon: "ti-chart-bar", Category: catStats, Size: 2, Refresh: 600, Desc: "Neue Tickets und Störungen pro Tag der letzten zwei Wochen", load: loadChartTrend},
	{Type: "list_mine", Name: "Mir zugewiesen", Icon: "ti-user-check", Category: catLists, Size: 2, Refresh: 60, Desc: "Deine offenen Tickets, Störungen, Aufgaben und Wartungen – das Dringendste zuerst", load: loadListMine},
	{Type: "list_faults", Name: "Aktuelle Störungen", Icon: "ti-alert-octagon", Category: catLists, Size: 2, Perm: "faults.view", Refresh: 30, Desc: "Die neuesten offenen Störungen mit Anlage", load: loadListFaults},
	{Type: "list_maintenance", Name: "Wartungen nächste 7 Tage", Icon: "ti-calendar-check", Category: catLists, Size: 2, Perm: "maintenance.view", Refresh: 300, Desc: "Was in den nächsten Tagen fällig wird", load: loadListMaintenance},
	{Type: "list_stock", Name: "Bestellliste", Icon: "ti-shopping-cart", Category: catLists, Size: 2, Perm: "inventory.view", Refresh: 300, Desc: "Ersatzteile unter Mindestbestand mit Bestand und Lagerort", load: loadListStock},
	{Type: "my_shifts", Name: "Meine Schichten", Icon: "ti-calendar-time", Category: catPersonal, Size: 2, Refresh: 600, Desc: "Deine Schichten der nächsten 7 Tage", load: loadMyShifts},
	{Type: "note", Name: "Notizzettel", Icon: "ti-note", Category: catPersonal, Size: 1, Config: "note", Desc: "Eine persönliche Notiz, nur für dich sichtbar", load: loadNote},
}

func widgetDef(t string) (WidgetDef, bool) {
	for _, d := range widgetDefs {
		if d.Type == t {
			return d, true
		}
	}
	return WidgetDef{}, false
}

// defaultWidgets: Zusammenstellung fuer Benutzer ohne eigene Auswahl
// (entspricht der bisherigen Kennzahlenzeile plus Persoenliches).
func defaultWidgets() []WidgetInstance {
	types := []string{"stat_tickets", "stat_faults", "stat_maintenance", "stat_stock", "quick_actions", "list_mine"}
	var out []WidgetInstance
	for i, t := range types {
		d, _ := widgetDef(t)
		out = append(out, WidgetInstance{ID: fmt.Sprintf("d%d", i+1), Type: t, Size: d.Size})
	}
	return out
}

// ── Laden/Speichern ──────────────────────────────────────────

func (h *Handler) userWidgets(ctx context.Context, uid string) []WidgetInstance {
	var raw []byte
	if h.db != nil && uid != "" {
		_ = h.db.QueryRow(ctx, `SELECT dashboard_widgets FROM users WHERE id = $1::uuid`, uid).Scan(&raw)
	}
	if len(raw) == 0 {
		return defaultWidgets()
	}
	var list []WidgetInstance
	if err := json.Unmarshal(raw, &list); err != nil {
		return defaultWidgets()
	}
	return list
}

// sanitizeWidgets prueft eine vom Browser gesendete Zusammenstellung.
func sanitizeWidgets(in []WidgetInstance) ([]WidgetInstance, error) {
	if len(in) > maxWidgets {
		return nil, fmt.Errorf("höchstens %d Widgets", maxWidgets)
	}
	seen := map[string]bool{}
	var out []WidgetInstance
	for i, w := range in {
		d, ok := widgetDef(w.Type)
		if !ok {
			continue
		}
		if w.ID == "" || len(w.ID) > 40 || seen[w.ID] {
			w.ID = fmt.Sprintf("w%d%d", time.Now().UnixNano()%1e9, i)
		}
		seen[w.ID] = true
		if w.Size < 1 || w.Size > 4 {
			w.Size = d.Size
		}
		cfg := map[string]string{}
		switch d.Config {
		case "links":
			cfg["links"] = cleanLinks(w.Config["links"])
		case "note":
			note := w.Config["note"]
			if len([]rune(note)) > 2000 {
				note = string([]rune(note)[:2000])
			}
			cfg["note"] = note
		}
		if t := strings.TrimSpace(w.Config["title"]); t != "" {
			if len([]rune(t)) > 40 {
				t = string([]rune(t)[:40])
			}
			cfg["title"] = t
		}
		if len(cfg) > 0 {
			w.Config = cfg
		} else {
			w.Config = nil
		}
		out = append(out, w)
	}
	return out, nil
}

// widgetLink: ein Eintrag aus "Eigene Links".
type widgetLink struct{ Name, URL string }

// parseLinks: Zeilen "Name | Adresse"; nur PDH-Pfade (/…) oder http(s).
func parseLinks(s string) []widgetLink {
	var out []widgetLink
	for _, line := range strings.Split(s, "\n") {
		name, u, ok := strings.Cut(line, "|")
		if !ok {
			u, name = line, ""
		}
		name, u = strings.TrimSpace(name), strings.TrimSpace(u)
		if u == "" || !safeWidgetURL(u) {
			continue
		}
		if name == "" {
			name = u
		}
		out = append(out, widgetLink{Name: name, URL: u})
		if len(out) == 12 {
			break
		}
	}
	return out
}

func safeWidgetURL(u string) bool {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return true
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

func cleanLinks(s string) string {
	var lines []string
	for _, l := range parseLinks(s) {
		lines = append(lines, l.Name+" | "+l.URL)
	}
	return strings.Join(lines, "\n")
}

// ── Ansicht ─────────────────────────────────────────────────

// DashboardWidgetView: ein Widget fuer die Dashboard-Seite.
type DashboardWidgetView struct {
	WidgetInstance
	Def   WidgetDef
	Title string
}

// WidgetCatalogGroup: Katalog nach Kategorien (nur erlaubte Widgets).
type WidgetCatalogGroup struct {
	Name  string
	Items []WidgetDef
}

func (h *Handler) canFn(r *http.Request) func(string) bool {
	u := getUser(r)
	return func(perm string) bool {
		return perm == "" || (h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), perm))
	}
}

func (h *Handler) dashboardWidgetViews(r *http.Request) ([]DashboardWidgetView, []WidgetCatalogGroup, string) {
	can := h.canFn(r)
	var views []DashboardWidgetView
	for _, w := range h.userWidgets(r.Context(), getUser(r).ID) {
		d, ok := widgetDef(w.Type)
		if !ok || !can(d.Perm) {
			continue
		}
		title := d.Name
		if t := w.Config["title"]; t != "" {
			title = t
		}
		views = append(views, DashboardWidgetView{WidgetInstance: w, Def: d, Title: title})
	}
	var cat []WidgetCatalogGroup
	for _, c := range widgetCategories {
		g := WidgetCatalogGroup{Name: c}
		for _, d := range widgetDefs {
			if d.Category == c && can(d.Perm) {
				g.Items = append(g.Items, d)
			}
		}
		if len(g.Items) > 0 {
			cat = append(cat, g)
		}
	}
	data, _ := json.Marshal(views)
	return views, cat, string(data)
}

// widgetBody: Daten fuer das Fragment eines Widgets.
type widgetBody struct {
	Def  WidgetDef
	Inst WidgetInstance
	Data any
	Err  string
}

// DashboardWidgetWeb: GET /dashboard/w/{id} – Inhalt eines Widgets.
func (h *Handler) DashboardWidgetWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	uid := getUser(r).ID
	can := h.canFn(r)
	var inst *WidgetInstance
	for _, x := range h.userWidgets(r.Context(), uid) {
		if x.ID == id {
			x := x
			inst = &x
			break
		}
	}
	// Vorschau aus dem Katalog: /dashboard/w/new?type=…
	if inst == nil && id == "new" {
		if d, ok := widgetDef(r.URL.Query().Get("type")); ok {
			inst = &WidgetInstance{ID: "new", Type: d.Type, Size: d.Size}
		}
	}
	if inst == nil {
		h.renderFragment(w, "dw-body", widgetBody{Err: "Widget nicht gefunden – bitte Seite neu laden."})
		return
	}
	d, _ := widgetDef(inst.Type)
	if !can(d.Perm) {
		h.renderFragment(w, "dw-body", widgetBody{Def: d, Err: "Keine Berechtigung."})
		return
	}
	data, err := d.load(h, r.Context(), uid, *inst, can)
	b := widgetBody{Def: d, Inst: *inst, Data: data}
	if err != nil {
		b.Err = "Konnte nicht geladen werden."
		componentLog("system").Warn().Err(err).Str("widget", inst.Type).Msg("dashboard-widget")
	}
	h.renderFragment(w, "dw-body", b)
}

// DashboardWidgetsSaveWeb: POST /dashboard/widgets (JSON-Liste; leer = Standard)
func (h *Handler) DashboardWidgetsSaveWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	w.Header().Set("Content-Type", "application/json")
	fail := func(err error) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
	}
	if uid == "" {
		fail(errors.New("nicht angemeldet"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		fail(err)
		return
	}
	var req struct {
		Reset   bool             `json:"reset"`
		Widgets []WidgetInstance `json:"widgets"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		fail(errors.New("ungültige Daten"))
		return
	}
	if req.Reset {
		_, err = h.db.Exec(r.Context(), `UPDATE users SET dashboard_widgets = NULL WHERE id = $1::uuid`, uid)
	} else {
		list, e := sanitizeWidgets(req.Widgets)
		if e != nil {
			fail(e)
			return
		}
		if list == nil {
			list = []WidgetInstance{}
		}
		data, _ := json.Marshal(list)
		_, err = h.db.Exec(r.Context(), `UPDATE users SET dashboard_widgets = $1::jsonb WHERE id = $2::uuid`, string(data), uid)
	}
	if err != nil {
		fail(err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// DashboardNoteSaveWeb: POST /dashboard/w/{id}/note – Text eines Notizzettels
// direkt aus dem Widget speichern (ohne "Dashboard anpassen").
func (h *Handler) DashboardNoteSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	uid := getUser(r).ID
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": msg})
	}
	if uid == "" {
		fail(http.StatusUnauthorized, "nicht angemeldet")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		fail(http.StatusBadRequest, "ungültige Daten")
		return
	}
	list := h.userWidgets(r.Context(), uid)
	found := false
	for i := range list {
		if list[i].ID == id && list[i].Type == "note" {
			if list[i].Config == nil {
				list[i].Config = map[string]string{}
			}
			list[i].Config["note"] = req.Note
			found = true
			break
		}
	}
	if !found {
		fail(http.StatusNotFound, "Notizzettel nicht gefunden – bitte Seite neu laden")
		return
	}
	list, err := sanitizeWidgets(list)
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	data, _ := json.Marshal(list)
	if _, err := h.db.Exec(r.Context(), `UPDATE users SET dashboard_widgets = $1::jsonb WHERE id = $2::uuid`, string(data), uid); err != nil {
		fail(http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// ── Inhalte ─────────────────────────────────────────────────

// statData: Kennzahl mit Zusatzzeile.
type statData struct {
	Value string
	Sub   string
	Color string // blue|red|amber|green|accent
	URL   string
	Alert bool // Zusatzwert > 0 hervorheben
}

func count(ctx context.Context, h *Handler, q string, args ...any) int {
	var n int
	_ = h.db.QueryRow(ctx, q, args...).Scan(&n)
	return n
}

func loadStatTickets(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var open, crit int
	cond, args := scopeSQL(h.scopeForUserID(ctx, uid), "ticket", "r", 0)
	err := h.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE priority = 'critical') FROM tickets r
		WHERE status IN ('open','in_progress','pending') AND archived_at IS NULL`+cond, args...).Scan(&open, &crit)
	return statData{Value: fmt.Sprint(open), Sub: fmt.Sprintf("%d kritisch", crit), Color: "blue", URL: "/tickets", Alert: crit > 0}, err
}

func loadStatFaults(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var active, fresh int
	cond, args := scopeSQL(h.scopeForUserID(ctx, uid), "fault", "r", 0)
	err := h.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'detected') FROM faults r
		WHERE status IN ('detected','analyzing','in_progress') AND archived_at IS NULL`+cond, args...).Scan(&active, &fresh)
	return statData{Value: fmt.Sprint(active), Sub: fmt.Sprintf("%d neu gemeldet", fresh), Color: "red", URL: "/faults", Alert: fresh > 0}, err
}

func loadStatMaintenance(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var due, overdue int
	cond, args := scopeSQL(h.scopeForUserID(ctx, uid), "maintenance_task", "r", 0)
	err := h.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE due_date < CURRENT_DATE) FROM maintenance_tasks r
		WHERE status::text IN ('open','in_progress') AND due_date <= CURRENT_DATE AND archived_at IS NULL`+cond, args...).Scan(&due, &overdue)
	return statData{Value: fmt.Sprint(due), Sub: fmt.Sprintf("%d überfällig", overdue), Color: "amber", URL: "/maintenance", Alert: overdue > 0}, err
}

func loadStatStock(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var low, crit int
	err := h.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE stock_qty <= min_qty), COUNT(*) FILTER (WHERE critical_qty > 0 AND stock_qty <= critical_qty)
		FROM spare_parts WHERE active AND hidden_at IS NULL AND min_qty > 0`).Scan(&low, &crit)
	return statData{Value: fmt.Sprint(low), Sub: fmt.Sprintf("%d kritisch", crit), Color: "green", URL: "/inventory", Alert: crit > 0}, err
}

const myTasksWhere = `(EXISTS (SELECT 1 FROM task_assignees a WHERE a.task_id = t.id AND a.user_id = $1::uuid)
	     OR t.assigned_group_id IN ` + myGroupIDs + `)
	AND t.status IN ('open','in_progress') AND t.archived_at IS NULL`

func loadStatMyTasks(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var open, overdue int
	err := h.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE t.due_date < CURRENT_DATE) FROM tasks t WHERE `+myTasksWhere, uid).Scan(&open, &overdue)
	return statData{Value: fmt.Sprint(open), Sub: fmt.Sprintf("%d überfällig", overdue), Color: "accent", URL: "/tasks", Alert: overdue > 0}, err
}

func fmtHours(min int) string {
	return fmt.Sprintf("%d:%02d h", min/60, min%60)
}

func loadStatMyHours(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var week, today int
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(m), 0)::int, COALESCE(SUM(m) FILTER (WHERE d = CURRENT_DATE), 0)::int FROM (
			SELECT started_at::date AS d,
			       COALESCE(duration_min, EXTRACT(EPOCH FROM (COALESCE(ended_at, NOW()) - started_at))::int / 60) AS m
			FROM time_entries WHERE user_id = $1::uuid AND NOT pending AND started_at >= date_trunc('week', NOW())
		) x`, uid).Scan(&week, &today)
	return statData{Value: fmtHours(week), Sub: "heute " + fmtHours(today), Color: "blue", URL: "/time"}, err
}

// trendData: Saeulen je Tag (Tickets/Stoerungen).
type trendDay struct {
	Label           string
	Tickets, Faults int
	HT, HF          int // Hoehe in Prozent
}
type trendData struct {
	Days                    []trendDay
	SumTickets, SumFaults   int
	ShowTickets, ShowFaults bool
}

func loadChartTrend(h *Handler, ctx context.Context, uid string, _ WidgetInstance, can func(string) bool) (any, error) {
	d := trendData{ShowTickets: can("tickets.view"), ShowFaults: can("faults.view")}
	sc := h.scopeForUserID(ctx, uid)
	tCond, args := scopeSQL(sc, "ticket", "tk", 0)
	fCond, _ := scopeSQL(sc, "fault", "fl", 0) // gleiche Platzhalter $1/$2
	rows, err := h.db.Query(ctx, `
		SELECT g::date,
		       (SELECT COUNT(*) FROM tickets tk WHERE tk.created_at::date = g::date`+tCond+`)::int,
		       (SELECT COUNT(*) FROM faults fl WHERE COALESCE(fl.detected_at, fl.created_at)::date = g::date`+fCond+`)::int
		FROM generate_series(CURRENT_DATE - 13, CURRENT_DATE, interval '1 day') g ORDER BY 1`, args...)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	max := 1
	wd := []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
	for rows.Next() {
		var day time.Time
		var t, f int
		if err := rows.Scan(&day, &t, &f); err != nil {
			return d, err
		}
		if !d.ShowTickets {
			t = 0
		}
		if !d.ShowFaults {
			f = 0
		}
		d.SumTickets += t
		d.SumFaults += f
		if t > max {
			max = t
		}
		if f > max {
			max = f
		}
		d.Days = append(d.Days, trendDay{Label: wd[day.Weekday()] + " " + day.Format("02."), Tickets: t, Faults: f})
	}
	for i := range d.Days {
		d.Days[i].HT = d.Days[i].Tickets * 100 / max
		d.Days[i].HF = d.Days[i].Faults * 100 / max
	}
	return d, rows.Err()
}

// listItem: Zeile in Listen-Widgets.
type listItem struct {
	Icon, Kind, Title, Sub, URL, Badge, BadgeClass string
}

func loadListMine(h *Handler, ctx context.Context, uid string, _ WidgetInstance, can func(string) bool) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT * FROM (
			SELECT 'ticket' AS k, id::text, title::text, priority::text AS priority, due_date::date AS due_date, status::text FROM tickets
			 WHERE (assigned_to = $1::uuid OR responsible_to = $1::uuid OR assigned_group_id IN `+myGroupIDs+`) AND status IN ('open','in_progress','pending') AND archived_at IS NULL
			UNION ALL
			SELECT 'fault', id::text, title::text, severity::text, due_date::date, status::text FROM faults
			 WHERE (assigned_to = $1::uuid OR responsible_to = $1::uuid OR assigned_group_id IN `+myGroupIDs+`) AND status IN ('detected','analyzing','in_progress') AND archived_at IS NULL
			UNION ALL
			SELECT 'task', t.id::text, t.title::text, t.priority::text, t.due_date::date, t.status::text FROM tasks t WHERE `+myTasksWhere+`
			UNION ALL
			SELECT 'maintenance', id::text, title::text, priority::text, due_date::date, status::text FROM maintenance_tasks
			 WHERE (assigned_to = $1::uuid OR responsible_to = $1::uuid OR assigned_group_id IN `+myGroupIDs+`) AND status::text IN ('open','in_progress') AND archived_at IS NULL
		) x ORDER BY (due_date IS NULL), due_date, (priority = 'critical') DESC LIMIT 8`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := map[string][3]string{
		"ticket": {"ti-ticket", "Ticket", "/tickets/"}, "fault": {"ti-alert-triangle", "Störung", "/faults/"},
		"task": {"ti-list-check", "Aufgabe", "/tasks/"}, "maintenance": {"ti-tool", "Wartung", "/maintenance/tasks/"},
	}
	perm := map[string]string{"ticket": "tickets.view", "fault": "faults.view", "task": "", "maintenance": "maintenance.view"}
	var out []listItem
	today := time.Now().Truncate(24 * time.Hour)
	for rows.Next() {
		var k, id, title, prio, status string
		var due *time.Time
		if err := rows.Scan(&k, &id, &title, &prio, &due, &status); err != nil {
			return nil, err
		}
		if !can(perm[k]) {
			continue
		}
		m := meta[k]
		it := listItem{Icon: m[0], Kind: m[1], Title: title, URL: m[2] + id, Sub: m[1] + " · " + statusLabel(status)}
		if due != nil {
			it.Badge = due.Format("02.01.")
			it.BadgeClass = "b-gray"
			if due.Before(today) {
				it.Badge, it.BadgeClass = "überfällig", "b-red"
			} else if due.Equal(today) || due.Format("2006-01-02") == today.Format("2006-01-02") {
				it.Badge, it.BadgeClass = "heute", "b-amber"
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func loadListFaults(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	cond, args := scopeSQL(h.scopeForUserID(ctx, uid), "fault", "f", 0)
	rows, err := h.db.Query(ctx, `
		SELECT f.id::text, f.title, f.severity::text, f.status::text, COALESCE(i.name, ''), COALESCE(f.detected_at, f.created_at)
		FROM faults f LEFT JOIN infrastructure i ON i.id = f.infrastructure_id
		WHERE f.status IN ('detected','analyzing','in_progress') AND f.archived_at IS NULL`+cond+`
		ORDER BY COALESCE(f.detected_at, f.created_at) DESC LIMIT 6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []listItem
	for rows.Next() {
		var id, title, sev, status, infra string
		var at time.Time
		if err := rows.Scan(&id, &title, &sev, &status, &infra, &at); err != nil {
			return nil, err
		}
		sub := statusLabel(status) + " · " + timeAgo(at)
		if infra != "" {
			sub = infra + " · " + sub
		}
		out = append(out, listItem{Icon: "ti-alert-triangle", Title: title, Sub: sub, URL: "/faults/" + id, Badge: sev, BadgeClass: severityClass(sev)})
	}
	return out, rows.Err()
}

func loadListMaintenance(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	cond, args := scopeSQL(h.scopeForUserID(ctx, uid), "maintenance_task", "m", 0)
	rows, err := h.db.Query(ctx, `
		SELECT m.id::text, m.title, COALESCE(i.name, ''), m.due_date::date
		FROM maintenance_tasks m LEFT JOIN infrastructure i ON i.id = m.infrastructure_id
		WHERE m.status::text IN ('open','in_progress') AND m.archived_at IS NULL AND m.due_date <= CURRENT_DATE + 7`+cond+`
		ORDER BY m.due_date LIMIT 6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []listItem
	today := time.Now().Format("2006-01-02")
	for rows.Next() {
		var id, title, infra string
		var due time.Time
		if err := rows.Scan(&id, &title, &infra, &due); err != nil {
			return nil, err
		}
		it := listItem{Icon: "ti-tool", Title: title, Sub: infra, URL: "/maintenance/tasks/" + id, Badge: due.Format("02.01."), BadgeClass: "b-gray"}
		if d := due.Format("2006-01-02"); d < today {
			it.Badge, it.BadgeClass = "überfällig", "b-red"
		} else if d == today {
			it.Badge, it.BadgeClass = "heute", "b-amber"
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func loadListStock(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, name, COALESCE(part_number, ''), stock_qty::float8, min_qty::float8, critical_qty::float8, COALESCE(unit, ''), COALESCE(storage_location, '')
		FROM spare_parts WHERE active AND hidden_at IS NULL AND min_qty > 0 AND stock_qty <= min_qty
		ORDER BY (critical_qty > 0 AND stock_qty <= critical_qty) DESC, stock_qty - min_qty LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []listItem
	for rows.Next() {
		var id, name, pn, unit, loc string
		var stock, min, crit float64
		if err := rows.Scan(&id, &name, &pn, &stock, &min, &crit, &unit, &loc); err != nil {
			return nil, err
		}
		sub := strings.Trim(pn+" · "+loc, " ·")
		it := listItem{Icon: "ti-package", Title: name, Sub: sub, URL: "/inventory/" + id,
			Badge: fmt.Sprintf("%s / %s %s", formatQty(stock), formatQty(min), unit), BadgeClass: "b-amber"}
		if crit > 0 && stock <= crit {
			it.BadgeClass = "b-red"
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// shiftDayView: Schicht an einem Tag.
type shiftDayView struct {
	Day, Date, Name, Short, Time, Color string
	Today                               bool
}

func loadMyShifts(h *Handler, ctx context.Context, uid string, _ WidgetInstance, _ func(string) bool) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT g::date, COALESCE(d.name, ''), COALESCE(d.short_name, ''), COALESCE(to_char(d.start_time, 'HH24:MI') || '–' || to_char(d.end_time, 'HH24:MI'), ''), COALESCE(d.color, '')
		FROM generate_series(CURRENT_DATE, CURRENT_DATE + 6, interval '1 day') g
		LEFT JOIN LATERAL (
			SELECT sd.* FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id
			WHERE sa.user_id = $1::uuid AND sa.date = g::date LIMIT 1
		) d ON true ORDER BY 1`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wd := []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
	var out []shiftDayView
	today := time.Now().Format("2006-01-02")
	for rows.Next() {
		var day time.Time
		var v shiftDayView
		if err := rows.Scan(&day, &v.Name, &v.Short, &v.Time, &v.Color); err != nil {
			return nil, err
		}
		if !hexColorRe.MatchString(v.Color) {
			v.Color = ""
		}
		v.Day, v.Date, v.Today = wd[day.Weekday()], day.Format("02.01."), day.Format("2006-01-02") == today
		out = append(out, v)
	}
	return out, rows.Err()
}

func loadNote(_ *Handler, _ context.Context, _ string, inst WidgetInstance, _ func(string) bool) (any, error) {
	return inst.Config["note"], nil
}

func loadQuickLinks(_ *Handler, _ context.Context, _ string, inst WidgetInstance, _ func(string) bool) (any, error) {
	return parseLinks(inst.Config["links"]), nil
}

// quickAction: Knopf im Widget "Schnellaktionen".
type quickAction struct{ Name, Icon, URL, Color string }

func loadQuickActions(_ *Handler, _ context.Context, _ string, _ WidgetInstance, can func(string) bool) (any, error) {
	all := []struct {
		quickAction
		perm string
	}{
		{quickAction{"Neue Zuweisung", "ti-square-rounded-plus", "/assignments/new", "accent"}, ""},
		{quickAction{"Störung melden", "ti-alert-triangle", "/faults", "red"}, "faults.view"},
		{quickAction{"Ticket anlegen", "ti-ticket", "/tickets", "blue"}, "tickets.view"},
		{quickAction{"Zeit erfassen", "ti-clock-play", "/time", "green"}, ""},
		{quickAction{"Ersatzteil suchen", "ti-package", "/inventory", "amber"}, "inventory.view"},
		{quickAction{"Wartung", "ti-tool", "/maintenance", "amber"}, "maintenance.view"},
		{quickAction{"Chat", "ti-messages", "/chat", "blue"}, "chat.use"},
		{quickAction{"Handbuch", "ti-book", "/help", "muted"}, ""},
	}
	var out []quickAction
	for _, a := range all {
		if can(a.perm) {
			out = append(out, a.quickAction)
		}
	}
	return out, nil
}
