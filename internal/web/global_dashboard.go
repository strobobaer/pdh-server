package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"pdh/pkg/appsettings"
)

type GlobalBoardItem struct {
	ID               string `json:"id"`
	TypeKey          string `json:"type_key"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Status           string `json:"status"`     // Anzeige (deutsch)
	StatusKey        string `json:"status_key"` // Code (open, in_progress, …) fuer die Logik im Browser
	LastAction       string `json:"last_action"`
	Priority         string `json:"priority"`
	DueDate          string `json:"due_date"`
	Assignee         string `json:"assignee"`
	Creator          string `json:"creator"` // wer den Vorgang angelegt bzw. gemeldet hat
	DetailURL        string `json:"detail_url"`
	InfrastructureID string `json:"infrastructure_id"`
	// Upcoming: geplante Wartung (offen, faellig erst nach heute) – z. B. der
	// Folgeauftrag eines Plans direkt nach dem Abschluss; die Tabelle blendet sie aus
	Upcoming bool `json:"upcoming"`
}

type GlobalDashboardData struct {
	Items          []GlobalBoardItem `json:"items"`
	Workers        []UserOption      `json:"workers"`
	Infrastructure []UserOption      `json:"infrastructure"`
	GanttItems     []GanttItem       `json:"gantt_items"`
	// Actors: Instandhaltung/IT zur Auswahl „Wer führt aus?“ – leer, wenn an
	// diesem Gerät die RFID-Karte Pflicht ist (global_dashboard_actor.go)
	Actors []UserOption `json:"actors"`
}

func (h *Handler) GlobalDashboardRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.LangMiddleware) // Sprache fuer Meldungen des Erstellungs-Assistenten
	r.Get("/", h.GlobalDashboard)
	r.Get("/b/{slug}", h.GlobalDashboardBoard) // bestimmter Leitstand (leitstand_boards.go)
	r.Get("/data", h.GlobalDashboardData)
	r.Post("/actions", h.GlobalDashboardAction)
	r.Post("/complete-start", h.GlobalDashboardCompleteStart)
	r.Post("/open", h.GlobalDashboardOpen)
	r.Post("/create", h.GlobalDashboardCreate)
	r.Post("/photos/{kind}/{id}", h.GlobalPhotosWeb) // Fotos zum gerade angelegten Vorgang
	r.Post("/settings", h.GlobalDashboardSettingsWeb)
	r.Post("/maintenance/{id}/start", h.GlobalDashboardMaintenanceStart)
	return r
}

// Geplante Wartungen: offene Wartungsauftraege, die erst nach heute faellig
// sind (z. B. der Folgeauftrag eines Plans direkt nach dem Abschluss). Die
// Leitstand-Tabelle zeigt sie nur mit dem Knopf „Geplante Wartungen“.

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
	LoggedIn                  bool
	Title                     string
	Subtitle                  string
	Board                     *leitstand              // angezeigter Leitstand (leitstand_boards.go)
	Blocks                    []boardBlockView        // Bausteine in Reihenfolge
	Core                      map[string]template.CSS // feste Bausteine (create, vorgaenge, zeitstrahl) → Stil (Reihenfolge, Breite) oder "" = aus
	Boards                    []*leitstand            // alle aktiven Leitstände (Links, Weiterblättern)
	CanManageBoards           bool
	CanEditHeading            bool
	CanDiscard                bool
	CanCreatePrivileged       bool // Aufgaben/Wartungen anlegen - Administratoren und Manager
	CanChat                   bool // angemeldet mit Chat-Berechtigung: Chat-Knopf im Kopf
	DefaultDueDaysTicket      int
	DefaultDueDaysTask        int
	DefaultDueDaysMaintenance int
	Timeline                  TimelineStyle // Zeitstrahl-Darstellung aus dem Theming
	Row                       int           // Zeilenhoehe (%) aus dem Konto als Vorgabe; je Geraet im Browser verstellbar
	ThemeCSS                  template.CSS  // Farbschemata aus dem Erscheinungsbild (wie im übrigen PDH)
	PaletteID                 string        // Farbschema: immer der Firmenstandard (gemeinsamer Hallenbildschirm)
	StyleID                   string        // Design-Stil: immer der Firmenstandard (ui_style.go)
}

// canEditGlobalDashboardHeading gilt fuer dieselbe Schranke wie die
// uebrigen Core-Einstellungen (system.manage_roles) - die Leitstand-Seite
// selbst ist oeffentlich, daher wird hier explizit ueber sessionUser()
// geprueft statt ueber den (auf dieser Route nie gesetzten) Request-
// Context von authMiddleware.
func (h *Handler) canEditGlobalDashboardHeading(r *http.Request) bool {
	return h.canManageLeitstand(r) // Recht „Leitstand verwalten“
}

// isGlobalBoardAdminOrManager gilt sowohl fuer den Verwerfen-Button im
// Leitstand als auch fuer das Anlegen von Aufgaben/Wartungen dort: Recht
// „Leitstand verwalten“ (leitstand.manage, anfangs admin und manager).
// Der Leitstand liegt ausserhalb von authMiddleware, daher ueber die Sitzung.
func (h *Handler) isGlobalBoardAdminOrManager(r *http.Request) bool {
	user := h.sessionUser(r)
	if user == nil || h.rbac == nil {
		return false
	}
	return h.rbac.HasPermissionForUser(user.ID, string(user.Role), "leitstand.manage")
}

// canUseChat: Sitzungsperson darf den Chat nutzen (Leitstand liegt ausserhalb von authMiddleware).
func (h *Handler) canUseChat(r *http.Request) bool {
	user := h.sessionUser(r)
	return user != nil && h.rbac.HasPermissionForUser(user.ID, string(user.Role), "chat.use")
}

func (h *Handler) GlobalDashboard(w http.ResponseWriter, r *http.Request) {
	h.renderLeitstand(w, r, h.resolveLeitstand(r))
}

// GlobalDashboardBoard: /global/b/{slug} – merkt sich den Leitstand an
// diesem Gerät, damit /global/ (z. B. nach dem Abmelden) wieder dort landet.
func (h *Handler) GlobalDashboardBoard(w http.ResponseWriter, r *http.Request) {
	b := h.boardBySlug(r, chi.URLParam(r, "slug"))
	if b == nil {
		http.Error(w, "Leitstand nicht gefunden oder nicht aktiv", http.StatusNotFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: leitstandCookie, Value: b.Slug, Path: "/", MaxAge: 365 * 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	h.renderLeitstand(w, r, b)
}

// boardBlockView: Baustein mit Katalogeintrag und CSS-Stil.
type boardBlockView struct {
	BoardBlock
	Def   boardBlockDef
	Style template.CSS // order und Breite im Raster
	Title string
	Links []*leitstand // Baustein „Andere Leitstände“
}

func (h *Handler) renderLeitstand(w http.ResponseWriter, r *http.Request, board *leitstand) {
	w.Header().Set("Cache-Control", "no-store")
	tmpl, err := h.globalDashboardTemplate(r)
	if err != nil {
		http.Error(w, "Dashboard-Vorlage konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ctx := r.Context()
	// Zurueck am Leitstand: eine Kurzsitzung (Oeffnen per Karte) endet hier,
	// damit die naechste Person nicht unter fremdem Namen weiterarbeitet.
	loggedIn := h.sessionUser(r) != nil
	if isBoardVisit(r) {
		endBoardVisit(w)
		loggedIn = false
	}
	brand := h.branding()
	data := GlobalDashboardPageData{
		LoggedIn:                  loggedIn,
		Title:                     board.Name,
		Subtitle:                  board.Subtitle,
		Board:                     board,
		CanManageBoards:           loggedIn && h.canManageLeitstand(r),
		CanEditHeading:            h.canEditGlobalDashboardHeading(r),
		CanDiscard:                h.isGlobalBoardAdminOrManager(r),
		CanCreatePrivileged:       h.isGlobalBoardAdminOrManager(r),
		CanChat:                   loggedIn && h.canUseChat(r),
		DefaultDueDaysTicket:      appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTask:        appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysMaintenance: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback),
		Timeline:                  brand.Timeline,
		ThemeCSS:                  brand.ThemeCSS(),
		PaletteID:                 brand.ThemeID(),
		StyleID:                   brand.StyleID(),
		Row:                       h.sessionRowHeight(r, loggedIn),
		Core:                      map[string]template.CSS{"create": "", "vorgaenge": "", "zeitstrahl": ""},
	}
	for _, b := range h.listLeitstaende(ctx) {
		if b.Active || b.IsDefault {
			data.Boards = append(data.Boards, b)
		}
	}
	for i, bl := range board.Blocks {
		d, _ := boardBlockDefFor(bl.Type)
		// nur Zahlen aus der geprüften Zusammenstellung (sanitizeBoardBlocks)
		v := boardBlockView{BoardBlock: bl, Def: d, Title: d.Name, Style: template.CSS(fmt.Sprintf("order:%d;--ls-span:%d", i+1, bl.Size))}
		if t := bl.Config["title"]; t != "" {
			v.Title = t
		}
		if _, core := data.Core[bl.Type]; core {
			data.Core[bl.Type] = v.Style
			continue
		}
		if bl.Type == "links" {
			for _, slug := range strings.Split(bl.Config["boards"], ",") {
				for _, o := range data.Boards {
					if o.Slug == slug && o.ID != board.ID {
						v.Links = append(v.Links, o)
					}
				}
			}
		}
		data.Blocks = append(data.Blocks, v)
	}
	if err := tmpl.ExecuteTemplate(w, "global_dashboard.gohtml", data); err != nil {
		http.Error(w, "Dashboard konnte nicht gerendert werden", http.StatusInternalServerError)
	}
}

// sessionRowHeight: Zeilenhoehe der angemeldeten Person (appearance.go),
// sonst 100 %. Der Leitstand liegt ausserhalb von authMiddleware.
func (h *Handler) sessionRowHeight(r *http.Request, loggedIn bool) int {
	row := 0
	if u := h.sessionUser(r); loggedIn && u != nil && h.db != nil {
		_ = h.db.QueryRow(r.Context(), `SELECT ui_row FROM users WHERE id = $1::uuid`, u.ID).Scan(&row)
	}
	if !validRow(row) {
		row = defaultRow
	}
	return row
}

// globalDashboardTemplate: Leitstand-Seite mit den gemeinsamen Widgets (u. a.
// Erstellungs-Assistent und Assistenten-Optik) und den Template-Funktionen.
func (h *Handler) globalDashboardTemplate(r *http.Request) (*template.Template, error) {
	var t *template.Template
	if h.tmpl != nil {
		c, err := h.tmpl.Clone()
		if err != nil {
			return nil, err
		}
		t = bindLang(c, h.requestLang(r))
	} else {
		var err error
		if t, err = template.New("global").Funcs(TemplateFuncs()).ParseGlob("web/templates/widgets/*.gohtml"); err != nil {
			return nil, err
		}
	}
	return t.ParseFiles("web/templates/global_dashboard.gohtml")
}

// GlobalDashboardSettingsWeb speichert die Ueberschrift/den Untertitel
// des angezeigten Leitstands (Tabelle leitstaende) – gilt fuer alle
// Betrachter, auch nicht angemeldete.
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
	// Überschrift des angezeigten Leitstands (?b=, sonst Standard)
	board := h.defaultLeitstand(r.Context())
	if b := h.boardBySlug(r, r.FormValue("b")); b != nil {
		board = b
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE leitstaende SET name = $1, subtitle = $2, updated_at = NOW() WHERE slug = $3`,
		title, subtitle, board.Slug); err != nil {
		http.Error(w, "Überschrift konnte nicht gespeichert werden", http.StatusInternalServerError)
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
		Actors:         make([]UserOption, 0),
		GanttItems:     h.buildDashboardGantt(ctx, time.Now()),
	}
	if data.GanttItems == nil {
		data.GanttItems = make([]GanttItem, 0)
	}

	rows, err := h.db.Query(ctx, `
		SELECT item.id, item.ref_type, item.title, item.description, item.status, COALESCE(latest.action, ''), item.priority,
		       item.due_date, item.assignee, item.creator, item.created_at, item.detail_url, COALESCE(item.infrastructure_id::text, '')
		FROM (
			SELECT f.id::text AS id, 'fault'::text AS ref_type, f.title, COALESCE(f.description, '') AS description, f.status::text AS status,
			       f.severity::text AS priority, COALESCE(f.due_date::date::text, '') AS due_date,
			       COALESCE(u.first_name || ' ' || u.last_name, g.name, '') AS assignee,
			       COALESCE((SELECT cu.first_name || ' ' || cu.last_name FROM users cu WHERE cu.id = f.created_by), '') AS creator,
			       f.created_at, '/faults/' || f.id::text AS detail_url, f.infrastructure_id
			FROM faults f LEFT JOIN users u ON u.id = f.assigned_to LEFT JOIN user_groups g ON g.id = f.assigned_group_id
			WHERE f.status IN ('detected', 'analyzing', 'in_progress', 'pending') AND f.archived_at IS NULL
			UNION ALL
			SELECT t.id::text, 'ticket', t.title, COALESCE(t.description, ''), t.status::text, t.priority::text,
			       COALESCE(t.due_date::date::text, ''),
			       COALESCE(u.first_name || ' ' || u.last_name, g.name, ''),
			       COALESCE((SELECT cu.first_name || ' ' || cu.last_name FROM users cu WHERE cu.id = t.created_by), ''),
			       t.created_at, '/tickets/' || t.id::text, t.infrastructure_id
			FROM tickets t LEFT JOIN users u ON u.id = t.assigned_to LEFT JOIN user_groups g ON g.id = t.assigned_group_id
			WHERE t.status IN ('open', 'in_progress', 'pending') AND t.archived_at IS NULL
			UNION ALL
			SELECT m.id::text, 'maintenance', m.title, COALESCE(m.description, ''), m.status::text, m.priority::text,
			       m.due_date::date::text,
			       COALESCE(u.first_name || ' ' || u.last_name, g.name, ''),
			       COALESCE((SELECT cu.first_name || ' ' || cu.last_name FROM users cu WHERE cu.id = m.created_by), ''),
			       m.created_at, '/maintenance/tasks/' || m.id::text, m.infrastructure_id
			FROM maintenance_tasks m LEFT JOIN users u ON u.id = m.assigned_to LEFT JOIN user_groups g ON g.id = m.assigned_group_id
			WHERE m.status IN ('open', 'in_progress', 'pending') AND m.archived_at IS NULL
			UNION ALL
			SELECT t.id::text, 'task', t.title, COALESCE(t.description, ''), t.status::text, t.priority::text,
			       COALESCE(t.due_date::text, ''),
			       COALESCE((
			           SELECT string_agg(au.first_name || ' ' || au.last_name, ', ' ORDER BY au.last_name, au.first_name)
			           FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = t.id
			       ), (SELECT g.name FROM user_groups g WHERE g.id = t.assigned_group_id), ''),
			       COALESCE((SELECT cu.first_name || ' ' || cu.last_name FROM users cu WHERE cu.id = t.created_by), ''),
			       t.created_at, '/tasks/' || t.id::text, t.infrastructure_id
			FROM tasks t
			WHERE t.status IN ('open', 'in_progress', 'pending') AND t.archived_at IS NULL
		) item
		LEFT JOIN LATERAL (
			SELECT action FROM global_dashboard_actions a
			WHERE a.ref_type = item.ref_type AND a.ref_id = item.id::uuid
			ORDER BY a.created_at DESC LIMIT 1
		) latest ON true
		ORDER BY NULLIF(item.due_date, '') NULLS LAST, item.created_at, item.title`)
	if err != nil {
		log.Error().Err(err).Msg("leitstand: /global/data hauptabfrage fehlgeschlagen")
		http.Error(w, "Aufgaben konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	// Leitstand (?b=): Vorgangsarten und Zuordnung zu Abteilung/Gruppe
	board := h.resolveLeitstand(r)
	if slug := r.URL.Query().Get("b"); slug != "" {
		if b := h.boardBySlug(r, slug); b != nil {
			board = b
		}
	}
	boardIDs := h.boardAllowedIDs(ctx, board)
	boardShows := func(kind, id string) bool {
		if !board.HasType(kind) {
			return false
		}
		return boardIDs == nil || boardIDs[kind][id]
	}
	gantt := data.GanttItems[:0]
	for _, g := range data.GanttItems {
		if boardShows(g.RefType, g.ID) {
			gantt = append(gantt, g)
		}
	}
	data.GanttItems = gantt
	// Abteilungsrechte: nur fuer angemeldete, eingeschraenkte Personen (die
	// oeffentliche Tafel ohne Anmeldung bleibt vollstaendig)
	var scopeIDs map[string]map[string]bool
	if u := h.sessionUser(r); u != nil {
		if sc := h.deptScopeFor(ctx, u.ID, string(u.Role)); sc != nil {
			scopeIDs = map[string]map[string]bool{
				"fault": h.allowedIDsFor(ctx, sc, "fault"), "ticket": h.allowedIDsFor(ctx, sc, "ticket"),
				"maintenance": h.allowedIDsFor(ctx, sc, "maintenance_task"), "task": h.allowedIDsFor(ctx, sc, "task"),
			}
			data.GanttItems = h.scopeGantt(ctx, sc, data.GanttItems)
		}
	}
	// Vorlauf der Plaene: eine Wartung gilt ab (Faelligkeit − Vorlauf) als anstehend
	leadDays := map[string]int{}
	if lrows, err := h.db.Query(ctx, `SELECT mt.id::text, COALESCE(mp.lead_days, 0) FROM maintenance_tasks mt
		LEFT JOIN maintenance_plans mp ON mp.id = mt.plan_id WHERE mt.status = 'open'`); err == nil {
		for lrows.Next() {
			var id string
			var d int
			if lrows.Scan(&id, &d) == nil {
				leadDays[id] = d
			}
		}
		lrows.Close()
	}
	today := time.Now()
	for rows.Next() {
		var item GlobalBoardItem
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.TypeKey, &item.Title, &item.Description, &item.Status, &item.LastAction, &item.Priority,
			&item.DueDate, &item.Assignee, &item.Creator, &createdAt, &item.DetailURL, &item.InfrastructureID); err != nil {
			log.Error().Err(err).Msg("leitstand: /global/data zeile lesen fehlgeschlagen")
			http.Error(w, "Aufgaben konnten nicht gelesen werden", http.StatusInternalServerError)
			return
		}
		if scopeIDs != nil {
			if allowed := scopeIDs[item.TypeKey]; allowed != nil && !allowed[item.ID] {
				continue
			}
		}
		if !boardShows(item.TypeKey, item.ID) {
			continue
		}
		item.StatusKey = item.Status
		item.Status = globalStatusLabel(item.Status)
		item.Type = globalTypeLabel(item.TypeKey)
		if item.TypeKey == "maintenance" && item.StatusKey == "open" {
			if due, err := time.ParseInLocation("2006-01-02", item.DueDate, time.Local); err == nil {
				item.Upcoming = due.AddDate(0, 0, -leadDays[item.ID]).After(today)
			}
		}
		data.Items = append(data.Items, item)
	}
	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("leitstand: /global/data zeilen-iteration fehlgeschlagen")
		http.Error(w, "Aufgaben konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}

	userRows, err := h.db.Query(ctx, `SELECT id::text, first_name, last_name FROM users WHERE active = true AND is_system_user = false ORDER BY last_name, first_name`)
	if err != nil {
		log.Error().Err(err).Msg("leitstand: /global/data mitarbeiter-abfrage fehlgeschlagen")
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

	if h.boardPickAllowed(r) {
		data.Actors = h.boardActors(ctx)
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
