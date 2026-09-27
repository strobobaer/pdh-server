package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
)

type GlobalBoardItem struct {
	ID         string `json:"id"`
	TypeKey    string `json:"type_key"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	LastAction string `json:"last_action"`
	Priority   string `json:"priority"`
	DueDate    string `json:"due_date"`
	Assignee   string `json:"assignee"`
	DetailURL  string `json:"detail_url"`
}

type GlobalDashboardData struct {
	Items          []GlobalBoardItem `json:"items"`
	Workers        []UserOption      `json:"workers"`
	Infrastructure []UserOption      `json:"infrastructure"`
}

func (h *Handler) GlobalDashboardRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.GlobalDashboard)
	r.Get("/data", h.GlobalDashboardData)
	r.Post("/actions", h.GlobalDashboardAction)
	r.Post("/create", h.GlobalDashboardCreate)
	r.Post("/settings", h.GlobalDashboardSettingsWeb)
	return r
}

const (
	globalDashboardDefaultTitle    = "PDH · Leitstand"
	globalDashboardDefaultSubtitle = "Störungen · Tickets · Wartungen · Aufgaben"
)

// GlobalDashboardPageData steuert, ob der Leitstand einen Weg zurueck in
// den normalen PDH-Modus zeigt und ob die Ueberschrift bearbeitet werden
// darf: die Seite liegt bewusst ausserhalb von authMiddleware
// (oeffentliches Wandmonitor-Board), daher muss sie selbst pruefen, ob
// bereits eine gueltige, ausreichend berechtigte Sitzung besteht.
type GlobalDashboardPageData struct {
	LoggedIn       bool
	Title          string
	Subtitle       string
	CanEditHeading bool
}

// canEditGlobalDashboardHeading gilt fuer dieselbe Schranke wie die
// uebrigen Core-Einstellungen (system.manage_roles) - die Leitstand-Seite
// selbst ist oeffentlich, daher wird hier explizit ueber sessionUser()
// geprueft statt ueber den (auf dieser Route nie gesetzten) Request-
// Context von authMiddleware.
func (h *Handler) canEditGlobalDashboardHeading(r *http.Request) bool {
	user := h.sessionUser(r)
	if user == nil {
		return false
	}
	return h.rbac.HasPermissionForUser(user.ID, string(user.Role), "system.manage_roles")
}

func (h *Handler) GlobalDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tmpl, err := template.ParseFiles("web/templates/global_dashboard.gohtml")
	if err != nil {
		http.Error(w, "Dashboard-Vorlage konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ctx := r.Context()
	data := GlobalDashboardPageData{
		LoggedIn:       h.sessionUser(r) != nil,
		Title:          h.getUpdateSetting(ctx, "global_dashboard_title", globalDashboardDefaultTitle),
		Subtitle:       h.getUpdateSetting(ctx, "global_dashboard_subtitle", globalDashboardDefaultSubtitle),
		CanEditHeading: h.canEditGlobalDashboardHeading(r),
	}
	if err := tmpl.ExecuteTemplate(w, "global_dashboard.gohtml", data); err != nil {
		http.Error(w, "Dashboard konnte nicht gerendert werden", http.StatusInternalServerError)
	}
}

// GlobalDashboardSettingsWeb speichert die Ueberschrift/den Untertitel
// des Leitstands (app_settings, wie die uebrigen Core-Einstellungen) -
// gilt fuer alle Betrachter, auch nicht angemeldete.
func (h *Handler) GlobalDashboardSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditGlobalDashboardHeading(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = globalDashboardDefaultTitle
	}
	if len([]rune(title)) > 120 {
		http.Error(w, "Überschrift darf höchstens 120 Zeichen lang sein", http.StatusBadRequest)
		return
	}
	subtitle := strings.TrimSpace(r.FormValue("subtitle"))
	if len([]rune(subtitle)) > 200 {
		http.Error(w, "Untertitel darf höchstens 200 Zeichen lang sein", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := h.setUpdateSetting(ctx, "global_dashboard_title", title); err != nil {
		http.Error(w, "Überschrift konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	if err := h.setUpdateSetting(ctx, "global_dashboard_subtitle", subtitle); err != nil {
		http.Error(w, "Untertitel konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GlobalDashboardData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	data := GlobalDashboardData{
		Items:          make([]GlobalBoardItem, 0),
		Workers:        make([]UserOption, 0),
		Infrastructure: make([]UserOption, 0),
	}

	rows, err := h.db.Query(ctx, `
		SELECT item.id, item.ref_type, item.title, item.status, COALESCE(latest.action, ''), item.priority,
		       item.due_date, item.assignee, item.created_at, item.detail_url
		FROM (
			SELECT f.id::text AS id, 'fault'::text AS ref_type, f.title, f.status::text AS status,
			       f.severity::text AS priority, COALESCE(f.due_date::date::text, '') AS due_date,
			       COALESCE(u.first_name || ' ' || u.last_name, '') AS assignee,
			       f.created_at, '/faults/' || f.id::text AS detail_url
			FROM faults f LEFT JOIN users u ON u.id = f.assigned_to
			WHERE f.status IN ('detected', 'analyzing', 'in_progress')
			UNION ALL
			SELECT t.id::text, 'ticket', t.title, t.status::text, t.priority::text,
			       COALESCE(t.due_date::date::text, ''),
			       COALESCE(u.first_name || ' ' || u.last_name, ''),
			       t.created_at, '/tickets/' || t.id::text
			FROM tickets t LEFT JOIN users u ON u.id = t.assigned_to
			WHERE t.status IN ('open', 'in_progress', 'pending')
			UNION ALL
			SELECT m.id::text, 'maintenance', m.title, m.status::text, m.priority::text,
			       m.due_date::date::text,
			       COALESCE(u.first_name || ' ' || u.last_name, ''),
			       m.created_at, '/maintenance/tasks/' || m.id::text
			FROM maintenance_tasks m LEFT JOIN users u ON u.id = m.assigned_to
			WHERE m.status IN ('open', 'in_progress')
			UNION ALL
			SELECT t.id::text, 'task', t.title, t.status::text, t.priority::text,
			       COALESCE(t.due_date::text, ''),
			       COALESCE(u.first_name || ' ' || u.last_name, ''),
			       t.created_at, '/tasks/' || t.id::text
			FROM tasks t LEFT JOIN users u ON u.id = t.assigned_to
			WHERE t.status IN ('open', 'in_progress')
		) item
		LEFT JOIN LATERAL (
			SELECT action FROM global_dashboard_actions a
			WHERE a.ref_type = item.ref_type AND a.ref_id = item.id::uuid
			ORDER BY a.created_at DESC LIMIT 1
		) latest ON true
		ORDER BY NULLIF(item.due_date, '') NULLS LAST, item.created_at, item.title`)
	if err != nil {
		http.Error(w, "Aufgaben konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item GlobalBoardItem
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.TypeKey, &item.Title, &item.Status, &item.LastAction, &item.Priority,
			&item.DueDate, &item.Assignee, &createdAt, &item.DetailURL); err != nil {
			http.Error(w, "Aufgaben konnten nicht gelesen werden", http.StatusInternalServerError)
			return
		}
		item.Status = globalStatusLabel(item.Status)
		if item.LastAction == "wait" && item.DueDate > time.Now().Format("2006-01-02") {
			item.Status = "Wartet"
		}
		item.Type = globalTypeLabel(item.TypeKey)
		data.Items = append(data.Items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "Aufgaben konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}

	userRows, err := h.db.Query(ctx, `SELECT id::text, first_name, last_name FROM users WHERE active = true AND is_system_user = false ORDER BY last_name, first_name`)
	if err != nil {
		http.Error(w, "Mitarbeiter konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	defer userRows.Close()
	for userRows.Next() {
		var id, firstName, lastName string
		if err := userRows.Scan(&id, &firstName, &lastName); err != nil {
			http.Error(w, "Mitarbeiter konnten nicht gelesen werden", http.StatusInternalServerError)
			return
		}
		data.Workers = append(data.Workers, UserOption{ID: id, Name: strings.TrimSpace(firstName + " " + lastName)})
	}
	if err := userRows.Err(); err != nil {
		http.Error(w, "Mitarbeiter konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}

	infraRows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path
			FROM infrastructure WHERE parent_id IS NULL AND active = true
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name
			FROM infrastructure i JOIN tree ON i.parent_id = tree.id
			WHERE i.active = true
		)
		SELECT id::text, path FROM tree ORDER BY path`)
	if err != nil {
		http.Error(w, "Infrastruktur konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	defer infraRows.Close()
	for infraRows.Next() {
		var id, path string
		if err := infraRows.Scan(&id, &path); err != nil {
			http.Error(w, "Infrastruktur konnte nicht gelesen werden", http.StatusInternalServerError)
			return
		}
		data.Infrastructure = append(data.Infrastructure, UserOption{ID: id, Name: path})
	}
	if err := infraRows.Err(); err != nil {
		http.Error(w, "Infrastruktur konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		return
	}
}

// isGlobalBoardDepartment entscheidet, wer das Leitstand-Terminal per
// RFID-Karte tatsaechlich bedienen darf (Annehmen/Fertig/Verwerfen/
// Warten) - bewusst weiterhin auf Instandhaltung/IT beschraenkt, anders
// als die Zuweisung selbst (die aktuell noch fuer alle Mitarbeiter offen
// ist, siehe GlobalDashboardData).
func isGlobalBoardDepartment(department string) bool {
	if strings.Contains(strings.ToLower(department), "instandhaltung") {
		return true
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(department), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if word == "it" || word == "informationstechnik" {
			return true
		}
	}
	return false
}

func globalStatusLabel(status string) string {
	labels := map[string]string{
		"open": "Offen", "detected": "Erkannt", "analyzing": "In Analyse",
		"in_progress": "In Arbeit", "pending": "Wartet", "resolved": "Erledigt",
		"closed": "Geschlossen", "done": "Erledigt", "skipped": "Übersprungen",
	}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}

func globalTypeLabel(refType string) string {
	labels := map[string]string{
		"fault": "Störung", "ticket": "Ticket", "maintenance": "Wartung", "task": "Aufgabe",
	}
	return labels[refType]
}
