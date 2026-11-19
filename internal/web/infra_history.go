package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/listsort"
)

// Anlagen-Historie der Infrastruktur-Stammkarte: fuehrt Stoerungen,
// Tickets, Aufgaben, Wartungsauftraege, Projekte, deren Massnahmen, die
// darueber verbrauchten Ersatzteile und die Anlagen-Kommentare zu einer
// gemeinsamen Zeitleiste zusammen - filterbar nach Modul, Zeitraum,
// Status und Freitext, optional inklusive aller Unteranlagen.

// infraHistoryModule beschreibt ein Modul der Anlagen-Historie
// (Filter-Chip, Icon, Beschriftung).
type infraHistoryModule struct {
	Key   string
	Label string
	Icon  string
	Dot   string
}

var infraHistoryModules = []infraHistoryModule{
	{"fault", "Störungen", "ti-alert-triangle", "d-red"},
	{"ticket", "Tickets", "ti-ticket", "d-amber"},
	{"task", "Aufgaben", "ti-checkbox", "d-blue"},
	{"maintenance", "Wartung", "ti-tool", "d-green"},
	{"project", "Projekte", "ti-briefcase", "d-blue"},
	{setupRef, "Rüstzeiten", "ti-arrows-exchange-2", "d-blue"},
	{"action", "Maßnahmen", "ti-list-check", "d-blue"},
	{"part", "Ersatzteile", "ti-package", "d-amber"},
	{"comment", "Kommentare", "ti-message", "d-blue"},
}

func infraHistoryModuleByKey(key string) (infraHistoryModule, bool) {
	for _, m := range infraHistoryModules {
		if m.Key == key {
			return m, true
		}
	}
	return infraHistoryModule{}, false
}

// Bezugstypen, auf die ein Anlagen-Kommentar verweisen darf, mit
// Tabelle, Titelspalte und Detail-URL.
var infraCommentRefTypes = map[string]struct {
	Table, TitleCol, URLPrefix, Label string
}{
	"fault":            {"faults", "title", "/faults/", "Störung"},
	"ticket":           {"tickets", "title", "/tickets/", "Ticket"},
	"task":             {"tasks", "title", "/tasks/", "Aufgabe"},
	"maintenance_task": {"maintenance_tasks", "title", "/maintenance/tasks/", "Wartung"},
	"project":          {"projects", "name", "/projects/", "Projekt"},
}

const (
	infraHistoryPageLimit   = 200
	infraHistoryExportLimit = 10000
)

// infraHistoryGroupLimit: Für „Häufigste zuerst“ und „Gleiches zusammen“ wird über die gefilterte
// Menge gerechnet, nicht nur über die ersten Zeilen der Seite (Variable, damit Tests sie senken können).
var infraHistoryGroupLimit = 2000

// infraHistoryFilter sind die Filterparameter der Anlagen-Historie
// (aus der Query-String des Filterformulars).
type infraHistoryFilter struct {
	Modules         []string
	Query           string
	From            *time.Time
	To              *time.Time
	FromValue       string
	ToValue         string
	State           string // "" | open | closed
	IncludeChildren bool
	// Sort: Sortierart der Zeitleiste (leer = neueste zuerst, so auch beim CSV-Export).
	Sort listsort.Mode
}

func parseInfraHistoryFilter(r *http.Request) infraHistoryFilter {
	q := r.URL.Query()
	f := infraHistoryFilter{
		Query: strings.TrimSpace(q.Get("q")),
		State: q.Get("state"),
		// Beim ersten Laden (ohne Formular) Unteranlagen standardmaessig
		// einbeziehen; danach entscheidet die Checkbox.
		IncludeChildren: q.Get("children") == "1" || q.Get("filtered") == "",
	}
	if f.State != "open" && f.State != "closed" {
		f.State = ""
	}
	for _, m := range q["module"] {
		if _, ok := infraHistoryModuleByKey(m); ok {
			f.Modules = append(f.Modules, m)
		}
	}
	if len(f.Modules) == 0 && q.Get("filtered") == "" {
		for _, m := range infraHistoryModules {
			f.Modules = append(f.Modules, m.Key)
		}
	}
	if t, err := time.Parse("2006-01-02", q.Get("from")); err == nil {
		f.From, f.FromValue = &t, q.Get("from")
	}
	if t, err := time.Parse("2006-01-02", q.Get("to")); err == nil {
		f.To, f.ToValue = &t, q.Get("to")
	}
	return f
}

func (f infraHistoryFilter) HasModule(key string) bool {
	for _, m := range f.Modules {
		if m == key {
			return true
		}
	}
	return false
}

// likePattern maskiert LIKE-Platzhalter, damit "%"/"_" im Suchtext
// woertlich gesucht werden.
func likePattern(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return "%" + s + "%"
}

// infraScopeCTE ist die rekursive Anlagen-Auswahl: die Anlage selbst und
// - wenn $2 true ist - alle Unteranlagen.
const infraScopeCTE = `
	scope AS (
		SELECT id FROM infrastructure WHERE id = $1::uuid
		UNION ALL
		SELECT i.id FROM infrastructure i JOIN scope s ON i.parent_id = s.id WHERE $2::bool
	)`

// infraStockMovesCTE sind alle Lagerbewegungen, die ueber einen Vorgang
// (Stoerung/Ticket/Aufgabe/Wartung) einer Anlage im Scope zugeordnet sind.
// Entnahmen zaehlen positiv, Rueckbuchungen ('in') negativ.
const infraStockMovesCTE = `
	moves AS (
		SELECT sm.id, sm.part_id, sm.created_at, sm.created_by,
		       CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END AS net_qty,
		       COALESCE(f.infrastructure_id, t.infrastructure_id, ta.infrastructure_id, m.infrastructure_id) AS infra_id,
		       COALESCE('Störung: ' || f.title, 'Ticket: ' || t.title, 'Aufgabe: ' || ta.title, 'Wartung: ' || m.title, '') AS parent_title,
		       COALESCE(sm.fault_id, sm.ticket_id, sm.task_id, sm.maintenance_task_id) AS record_id
		FROM stock_movements sm
		LEFT JOIN faults f ON f.id = sm.fault_id
		LEFT JOIN tickets t ON t.id = sm.ticket_id
		LEFT JOIN tasks ta ON ta.id = sm.task_id
		LEFT JOIN maintenance_tasks m ON m.id = sm.maintenance_task_id
		WHERE sm.type IN ('out', 'in')
		  AND COALESCE(f.infrastructure_id, t.infrastructure_id, ta.infrastructure_id, m.infrastructure_id) IN (SELECT id FROM scope)
	)`

// infraHistoryBaseSQL liefert alle Historien-Eintraege (ohne Modulfilter)
// als CTE "filtered". Parameter: $1 Anlage, $2 Unteranlagen, $3 Suche,
// $4 von, $5 bis, $6 Status.
const infraHistoryBaseSQL = `
WITH RECURSIVE` + infraScopeCTE + `,` + infraStockMovesCTE + `,
	entries AS (
		SELECT 'fault'::text AS module, f.id::text AS id, f.title::text AS title,
		       COALESCE(f.description, '') || ' ' || COALESCE(f.resolution, '') || ' ' || COALESCE(f.root_cause, '') AS body,
		       f.status::text AS status, f.created_at AS occurred_at, f.resolved_at AS closed_at,
		       '/faults/' || f.id AS url, f.created_by AS author_id, f.infrastructure_id AS infra_id,
		       ''::text AS parent_title, NULL::numeric AS qty, ''::text AS unit, false AS pinned
		FROM faults f WHERE f.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'ticket', t.id::text, t.title, COALESCE(t.description, '') || ' ' || COALESCE(t.resolution, ''),
		       t.status::text, t.created_at, t.resolved_at, '/tickets/' || t.id, t.created_by, t.infrastructure_id,
		       '', NULL, '', false
		FROM tickets t WHERE t.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'task', ta.id::text, ta.title, COALESCE(ta.description, '') || ' ' || COALESCE(ta.resolution, ''),
		       ta.status, ta.created_at, ta.resolved_at, '/tasks/' || ta.id, ta.created_by, ta.infrastructure_id,
		       COALESCE('Projekt: ' || p.name, ''), NULL, '', false
		FROM tasks ta LEFT JOIN projects p ON p.id = ta.project_id
		WHERE ta.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'maintenance', m.id::text, m.title, COALESCE(m.description, '') || ' ' || COALESCE(m.notes, ''),
		       m.status::text, COALESCE(m.completed_at, m.due_date), m.completed_at, '/maintenance/tasks/' || m.id,
		       m.created_by, m.infrastructure_id, COALESCE('Plan: ' || mp.name, ''), NULL, '', false
		FROM maintenance_tasks m LEFT JOIN maintenance_plans mp ON mp.id = m.plan_id
		WHERE m.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'project', p.id::text, p.name, COALESCE(p.description, ''),
		       p.status, p.created_at, CASE WHEN p.status = 'completed' THEN p.updated_at END, '/projects/' || p.id,
		       p.created_by, p.infrastructure_id, '', NULL, '', false
		FROM projects p WHERE p.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'setup', s.id::text, ` + setupTitleSQL + `, COALESCE(s.note, '') || ' ' || sr.name,
		       s.status, s.started_at, s.ended_at, '/setup/' || s.id, s.created_by, s.infrastructure_id,
		       '', NULL, '', false
		FROM setup_times s JOIN setup_reasons sr ON sr.id = s.reason_id
		WHERE s.infrastructure_id IN (SELECT id FROM scope) AND s.archived_at IS NULL
		UNION ALL
		SELECT 'action', a.id::text, f.title, a.description, '', a.created_at, NULL, '/faults/' || f.id,
		       a.created_by, f.infrastructure_id, 'Störung', NULL, '', false
		FROM fault_actions a JOIN faults f ON f.id = a.fault_id WHERE f.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'action', a.id::text, t.title, a.description, '', a.created_at, NULL, '/tickets/' || t.id,
		       a.created_by, t.infrastructure_id, 'Ticket', NULL, '', false
		FROM ticket_actions a JOIN tickets t ON t.id = a.ticket_id WHERE t.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'action', a.id::text, ta.title, a.description, '', a.created_at, NULL, '/tasks/' || ta.id,
		       a.created_by, ta.infrastructure_id, 'Aufgabe', NULL, '', false
		FROM task_actions a JOIN tasks ta ON ta.id = a.task_id WHERE ta.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'action', a.id::text, m.title, a.description, '', a.created_at, NULL, '/maintenance/tasks/' || m.id,
		       a.created_by, m.infrastructure_id, 'Wartung', NULL, '', false
		FROM maintenance_task_actions a JOIN maintenance_tasks m ON m.id = a.task_id WHERE m.infrastructure_id IN (SELECT id FROM scope)
		UNION ALL
		SELECT 'part', mv.id::text, sp.part_number || ' · ' || sp.name,
		       COALESCE(sp.manufacturer, '') || ' ' || COALESCE(sp.manufacturer_part, ''),
		       '', mv.created_at, NULL, '/inventory/' || sp.id, mv.created_by, mv.infra_id,
		       mv.parent_title, mv.net_qty, sp.unit, false
		FROM moves mv JOIN spare_parts sp ON sp.id = mv.part_id
		UNION ALL
		SELECT 'comment', c.id::text, '', c.text, '', c.created_at, NULL,
		       CASE c.ref_type
		         WHEN 'fault' THEN '/faults/' || c.ref_id
		         WHEN 'ticket' THEN '/tickets/' || c.ref_id
		         WHEN 'task' THEN '/tasks/' || c.ref_id
		         WHEN 'maintenance_task' THEN '/maintenance/tasks/' || c.ref_id
		         WHEN 'project' THEN '/projects/' || c.ref_id
		         ELSE '' END,
		       c.created_by, c.infrastructure_id,
		       COALESCE('Störung: ' || cf.title, 'Ticket: ' || ct.title, 'Aufgabe: ' || cta.title,
		                'Wartung: ' || cm.title, 'Projekt: ' || cp.name, ''),
		       NULL, '', c.pinned
		FROM infrastructure_comments c
		LEFT JOIN faults cf ON c.ref_type = 'fault' AND cf.id = c.ref_id
		LEFT JOIN tickets ct ON c.ref_type = 'ticket' AND ct.id = c.ref_id
		LEFT JOIN tasks cta ON c.ref_type = 'task' AND cta.id = c.ref_id
		LEFT JOIN maintenance_tasks cm ON c.ref_type = 'maintenance_task' AND cm.id = c.ref_id
		LEFT JOIN projects cp ON c.ref_type = 'project' AND cp.id = c.ref_id
		WHERE c.infrastructure_id IN (SELECT id FROM scope)
	),
	filtered AS (
		SELECT e.module, e.id, e.title, e.body, e.status, e.occurred_at, e.closed_at, e.url,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), 'System') AS author,
		       COALESCE(e.author_id::text, '') AS author_id,
		       e.infra_id::text AS infra_id, COALESCE(i.name, '') AS infra_name,
		       e.parent_title, e.qty, e.unit, e.pinned
		FROM entries e
		LEFT JOIN users u ON u.id = e.author_id
		LEFT JOIN infrastructure i ON i.id = e.infra_id
		WHERE ($3::text = '' OR (e.title || ' ' || e.body || ' ' || e.parent_title || ' ' ||
		        COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '') || ' ' || COALESCE(i.name, '')) ILIKE $3::text)
		  AND ($4::date IS NULL OR e.occurred_at >= $4::date)
		  AND ($5::date IS NULL OR e.occurred_at < $5::date + 1)
		  AND ($6::text = ''
		       OR e.module NOT IN ('fault', 'ticket', 'task', 'maintenance', 'project', 'setup')
		       OR (($6::text = 'closed') = (e.status IN ('resolved', 'closed', 'done', 'skipped', 'completed', 'fault'))))` + infraHistoryHideMark + `
	)`

// infraHistoryHideMark: Platzhalter am Ende der Bedingung von "filtered"; dort
// kommen die Projekte hinein, die die Person nicht sehen darf (infraHistorySQL).
const infraHistoryHideMark = ` /*HIDE*/`

// infraHistorySQL: die Historien-Abfrage ohne die Projekt-Einträge (und die
// Kommentare an Projekten), die hidden (Projekt-IDs) nennt. So zählen Zähler,
// Seitengrenze und Gruppen nur Sichtbares. Die IDs stammen aus der Datenbank
// und werden trotzdem als UUID geprüft; eine ungültige wird übersprungen.
func infraHistorySQL(hidden []string) string {
	var urls []string
	for _, id := range hidden {
		if bareUUIDRe.MatchString(id) {
			urls = append(urls, `'/projects/`+id+`'`)
		}
	}
	if len(urls) == 0 {
		return infraHistoryBaseSQL
	}
	return strings.Replace(infraHistoryBaseSQL, infraHistoryHideMark, ` AND e.url <> ALL(ARRAY[`+strings.Join(urls, ",")+`]::text[])`, 1)
}

// hiddenProjectIDs: alle Projekte, die die Person nicht sehen darf (bei Recht
// überall: keine). Bei einem Fehler wird mit dem Fehler abgebrochen, damit die
// Historie nie ungefiltert erscheint.
func (h *Handler) hiddenProjectIDs(r *http.Request) ([]string, error) {
	if h.viewsAll(r, "project") {
		return nil, nil
	}
	if h.db == nil {
		return nil, errViewNoDB
	}
	rows, err := h.db.Query(r.Context(), `SELECT id::text FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	canView := h.viewFilter(r, "project")
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !canView(id) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// InfraHistoryEntry ist ein Eintrag der Anlagen-Zeitleiste.
type InfraHistoryEntry struct {
	Module      string
	ModuleLabel string
	Icon        string
	Dot         string
	ID          string
	Title       string
	Body        string
	StatusLabel string
	StatusClass string
	OccurredAt  time.Time
	DateLabel   string
	ClosedLabel string
	DetailURL   string
	Author      string
	AuthorID    string
	InfraID     string
	InfraName   string
	FromChild   bool
	ParentTitle string
	QtyLabel    string
	Pinned      bool
	CanEdit     bool
}

// InfraPartUsage ist eine Zeile der Ersatzteil-Auswertung einer Anlage.
type InfraPartUsage struct {
	PartID     string
	PartNumber string
	Name       string
	Unit       string
	QtyLabel   string
	Records    int
	LastUsed   string
	CostLabel  string
}

type infraHistoryModuleCount struct {
	infraHistoryModule
	Count  int
	Active bool
}

// InfraHistoryGroup ist eine Gruppe gleicher Einträge („Häufigste zuerst“,
// „Gleiches zusammen“). Mit einem Eintrag wird sie als gewöhnliche Zeile gezeigt.
type InfraHistoryGroup struct {
	Label   string
	Count   int
	Last    string
	Entries []InfraHistoryEntry
}

// InfraHistoryResult ist das Datenmodell des Historien-Fragments.
type InfraHistoryResult struct {
	InfraID    string
	Filter     infraHistoryFilter
	Modules    []infraHistoryModuleCount
	Pinned     []InfraHistoryEntry
	Entries    []InfraHistoryEntry
	Groups     []InfraHistoryGroup // nur in den gruppierenden Sortierarten
	Total      int
	Truncated  bool // Seite zeigt nicht alle (neueste bzw. älteste Zeilen)
	Capped     bool // beim Gruppieren zählten nur die neuesten Cap Einträge
	Cap        int
	Parts      []InfraPartUsage
	PartsCost  string
	ExportURL  string
	IsFiltered bool
	Bar        SortBar
	Lang       string
}

// Language: Sprache für die Vorlagenfunktion t beim Ausgeben.
func (r *InfraHistoryResult) Language() string { return r.Lang }

// infraHistoryItems: die Sortiereinträge zu den Zeilen (gleiche Reihenfolge).
func infraHistoryItems(entries []InfraHistoryEntry) []listsort.Item {
	items := make([]listsort.Item, len(entries))
	for i, e := range entries {
		person := ""
		if e.AuthorID != "" { // ohne Benutzer („System“) steht der Eintrag bei „Nach Person“ am Ende
			person = e.Author
		}
		items[i] = listsort.Item{At: e.OccurredAt, Key: infraHistoryKey(e), Label: infraHistoryLabel(e), Person: person, Kind: e.ModuleLabel}
	}
	return items
}

// infraHistoryOrdered: die Zeilen in der Reihenfolge der Sortierart (ohne Gruppen).
func infraHistoryOrdered(entries []InfraHistoryEntry, mode listsort.Mode) []InfraHistoryEntry {
	out := make([]InfraHistoryEntry, 0, len(entries))
	for _, i := range listsort.Order(infraHistoryItems(entries), mode) {
		out = append(out, entries[i])
	}
	return out
}

// infraHistoryGroups: gleiche Einträge zu Gruppen zusammengefasst.
func infraHistoryGroups(entries []InfraHistoryEntry, mode listsort.Mode) []InfraHistoryGroup {
	var out []InfraHistoryGroup
	for _, g := range listsort.Groups(infraHistoryItems(entries), mode) {
		grp := InfraHistoryGroup{Label: g.Label, Count: g.Count, Last: g.Last.Local().Format("02.01.2006")}
		for _, i := range g.Idx {
			grp.Entries = append(grp.Entries, entries[i])
		}
		// „Maßnahme · “ nur, wenn die ganze Gruppe aus Maßnahmen besteht (Maßnahme und Kommentar
		// gleichen Textes landen in einer Gruppe).
		if allInfraActions(grp.Entries) && grp.Label != grp.Entries[0].ModuleLabel {
			grp.Label = "Maßnahme · " + grp.Label
		}
		out = append(out, grp)
	}
	return out
}

// allInfraActions: wahr, wenn jeder Eintrag eine Maßnahme ist.
func allInfraActions(es []InfraHistoryEntry) bool {
	for _, e := range es {
		if e.Module != "action" {
			return false
		}
	}
	return len(es) > 0
}

// infraHistoryKey: „gleich“ heißt gleicher Wortschlüssel. Bei Maßnahmen,
// Kommentaren und Lagerbewegungen zählt der Text mit, sonst nur der Titel.
func infraHistoryKey(e InfraHistoryEntry) string {
	switch e.Module {
	case "action", "comment":
		// Der eigene Text ist der Text; der Titel ist der des Vorgangs, an dem er hängt.
		// So zeigt „Häufigste zuerst“, was an dieser Anlage über Vorgänge hinweg wiederkehrt.
		if k := listsort.Key(e.Body); k != "" {
			return k
		}
		return listsort.Key(e.Title)
	case "part":
		return listsort.Key(e.Title + " " + e.Body)
	}
	return listsort.Key(e.Title)
}

// infraHistoryLabel: Gruppentitel, wahr für jeden Eintrag der Gruppe (der
// Schlüssel enthält den Titel bzw. Text, nicht aber die Art des Bezugs).
func infraHistoryLabel(e InfraHistoryEntry) string {
	var l string
	switch e.Module {
	case "comment", "action":
		l = groupTitle(e.Body)
	default:
		l = groupTitle(e.Title)
	}
	if strings.TrimSpace(l) == "" {
		return e.ModuleLabel
	}
	return l
}

func (f infraHistoryFilter) args(infraID string) []interface{} {
	search := ""
	if f.Query != "" {
		search = likePattern(f.Query)
	}
	var from, to interface{}
	if f.From != nil {
		from = *f.From
	}
	if f.To != nil {
		to = *f.To
	}
	return []interface{}{infraID, f.IncludeChildren, search, from, to, f.State}
}

func (h *Handler) canModerateInfraComment(r *http.Request, authorID string) bool {
	u := getUser(r)
	if u.ID != "" && u.ID == authorID {
		return true
	}
	return h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_users")
}

func formatQty(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return strings.Replace(s, ".", ",", 1)
}

func formatEuro(v float64) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', 2, 64), ".", ",", 1) + " €"
}

// loadInfraHistory fuehrt die Historien-Abfrage aus (limit <= 0: ohne
// Modul-Zaehler und Ersatzteil-Auswertung, fuer den Export).
func (h *Handler) loadInfraHistory(ctx context.Context, r *http.Request, infraID string, f infraHistoryFilter, limit int) (*InfraHistoryResult, error) {
	res := &InfraHistoryResult{InfraID: infraID, Filter: f}
	args := f.args(infraID)
	// Ansichtsrecht: Projekte (und Kommentare an Projekten), die die Person nicht sehen darf, fehlen
	// schon in der Abfrage – Zähler, Seitengrenze und Gruppen zählen nur Sichtbares.
	hidden, err := h.hiddenProjectIDs(r)
	if err != nil {
		componentLog("rechte").Error().Err(err).Msg("anlagen-historie: nicht sichtbare projekte laden")
		return nil, err
	}
	baseSQL := infraHistorySQL(hidden)

	// Modul-Zaehler (ohne Modulfilter, damit die Chips zeigen, was es gibt)
	counts := map[string]int{}
	if rows, err := h.db.Query(ctx, baseSQL+` SELECT module, COUNT(*) FROM filtered GROUP BY module`, args...); err == nil {
		for rows.Next() {
			var m string
			var n int
			if rows.Scan(&m, &n) == nil {
				counts[m] = n
			}
		}
		rows.Close()
	} else {
		return nil, err
	}
	for _, m := range infraHistoryModules {
		res.Modules = append(res.Modules, infraHistoryModuleCount{m, counts[m.Key], f.HasModule(m.Key)})
		if f.HasModule(m.Key) {
			res.Total += counts[m.Key]
		}
	}

	// Sortierart: der Export bleibt immer nach Datum (neueste zuerst).
	mode := f.Sort
	if mode == "" || limit <= 0 {
		mode = listsort.Newest
	}
	fetch := limit
	if fetch <= 0 {
		fetch = infraHistoryExportLimit
	} else if mode.Grouped() {
		fetch = infraHistoryGroupLimit
	}
	// „Älteste zuerst“ sortiert in der Datenbank, damit wirklich die ältesten
	// erscheinen; ORDER BY kommt nur aus diesen festen Bausteinen.
	orderBy := "occurred_at DESC, id DESC"
	if mode == listsort.Oldest {
		orderBy = "occurred_at ASC, id ASC"
	}
	listArgs := append(args, f.Modules)
	rows, err := h.db.Query(ctx, baseSQL+fmt.Sprintf(`
		SELECT module, id, title, body, status, occurred_at, closed_at, url, author, author_id,
		       infra_id, infra_name, parent_title, qty::float8, unit, pinned
		FROM filtered WHERE module = ANY($7::text[])
		ORDER BY %s LIMIT %d`, orderBy, fetch), listArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e InfraHistoryEntry
		var status, authorID, unit string
		var closedAt *time.Time
		var qty *float64
		if err := rows.Scan(&e.Module, &e.ID, &e.Title, &e.Body, &status, &e.OccurredAt, &closedAt,
			&e.DetailURL, &e.Author, &authorID, &e.InfraID, &e.InfraName, &e.ParentTitle, &qty, &unit, &e.Pinned); err != nil {
			continue
		}
		e.AuthorID = authorID
		if mod, ok := infraHistoryModuleByKey(e.Module); ok {
			e.ModuleLabel, e.Icon, e.Dot = mod.Label, mod.Icon, mod.Dot
		}
		e.Body = strings.TrimSpace(e.Body)
		if status != "" {
			e.StatusLabel, e.StatusClass = statusLabel(status), statusClass(status)
			if e.Module == setupRef {
				e.StatusLabel = setupStatusText(status)
			}
		}
		e.DateLabel = e.OccurredAt.Local().Format("02.01.2006 15:04")
		if closedAt != nil {
			e.ClosedLabel = closedAt.Local().Format("02.01.2006")
		}
		e.FromChild = e.InfraID != infraID
		if qty != nil {
			e.QtyLabel = formatQty(*qty) + " " + unit
		}
		if e.Module == "comment" {
			e.CanEdit = h.canModerateInfraComment(r, authorID)
		}
		res.Entries = append(res.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Vor dem Abteilungsfilter merken, ob die Abfrage ihre Grenze erreicht hat (der Zähler danach
	// zählt nur noch die sichtbaren Einträge).
	hitLimit := res.Total > len(res.Entries)
	// Abteilungsrechte: Vorgaenge (und deren Massnahmen/Teile/Kommentare)
	// fremder Abteilungen ausblenden; Zaehler dann aus den sichtbaren Eintraegen
	if s := h.requestScope(r); s != nil {
		allowed := map[string]bool{}
		kept := res.Entries[:0]
		removed := 0
		for _, e := range res.Entries {
			if refType, rid, ok := scopeTarget(e.DetailURL); ok {
				key := refType + "|" + rid
				ok2, seen := allowed[key]
				if !seen {
					ok2 = h.recordInScope(ctx, s, refType, rid)
					allowed[key] = ok2
				}
				if !ok2 {
					removed++
					continue
				}
			}
			kept = append(kept, e)
		}
		res.Entries = kept
		if removed > 0 {
			vis := map[string]int{}
			for _, e := range kept {
				vis[e.Module]++
			}
			res.Total = 0
			for i := range res.Modules {
				res.Modules[i].Count = vis[res.Modules[i].Key]
				if res.Modules[i].Active {
					res.Total += res.Modules[i].Count
				}
			}
		}
	}
	if limit <= 0 {
		return res, nil
	}
	// Gezählt und gruppiert wird, was nach dem Abteilungsfilter übrig ist.
	if more := hitLimit || res.Total > len(res.Entries); mode.Grouped() {
		res.Capped, res.Cap = more, infraHistoryGroupLimit
		res.Groups = infraHistoryGroups(res.Entries, mode)
	} else {
		res.Truncated = more
		if mode == listsort.Person || mode == listsort.Kind {
			res.Entries = infraHistoryOrdered(res.Entries, mode)
		}
	}

	// Angeheftete Kommentare - unabhaengig von Filtern immer sichtbar
	if prow, err := h.db.Query(ctx, `
		SELECT c.id::text, c.text, c.created_at, COALESCE(c.created_by::text, ''),
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), 'System'),
		       CASE WHEN c.ref_type = 'project' THEN c.ref_id::text ELSE '' END
		FROM infrastructure_comments c LEFT JOIN users u ON u.id = c.created_by
		WHERE c.infrastructure_id = $1 AND c.pinned
		ORDER BY c.created_at DESC`, infraID); err == nil {
		hiddenSet := map[string]bool{}
		for _, id := range hidden {
			hiddenSet[id] = true
		}
		for prow.Next() {
			var e InfraHistoryEntry
			var authorID, projRef string
			if prow.Scan(&e.ID, &e.Body, &e.OccurredAt, &authorID, &e.Author, &projRef) == nil && !hiddenSet[projRef] {
				e.Module, e.Pinned = "comment", true
				e.DateLabel = e.OccurredAt.Local().Format("02.01.2006 15:04")
				e.CanEdit = h.canModerateInfraComment(r, authorID)
				res.Pinned = append(res.Pinned, e)
			}
		}
		prow.Close()
	}

	// Ersatzteil-Auswertung (gleicher Scope, Zeitraum und Suchtext)
	search, from, to := args[2], args[3], args[4]
	prows, err := h.db.Query(ctx, `
		WITH RECURSIVE`+infraScopeCTE+`,`+infraStockMovesCTE+`
		SELECT sp.id::text, sp.part_number, sp.name, sp.unit,
		       SUM(mv.net_qty)::float8, COUNT(DISTINCT mv.record_id), MAX(mv.created_at),
		       (SUM(mv.net_qty) * sp.price)::float8
		FROM moves mv JOIN spare_parts sp ON sp.id = mv.part_id
		WHERE ($3::text = '' OR (sp.part_number || ' ' || sp.name || ' ' || COALESCE(sp.manufacturer, '') || ' ' ||
		        COALESCE(sp.manufacturer_part, '') || ' ' || mv.parent_title) ILIKE $3::text)
		  AND ($4::date IS NULL OR mv.created_at >= $4::date)
		  AND ($5::date IS NULL OR mv.created_at < $5::date + 1)
		GROUP BY sp.id, sp.part_number, sp.name, sp.unit, sp.price
		HAVING SUM(mv.net_qty) <> 0
		ORDER BY MAX(mv.created_at) DESC`, infraID, f.IncludeChildren, search, from, to)
	if err == nil {
		var total float64
		for prows.Next() {
			var p InfraPartUsage
			var qty, cost float64
			var last time.Time
			if prows.Scan(&p.PartID, &p.PartNumber, &p.Name, &p.Unit, &qty, &p.Records, &last, &cost) != nil {
				continue
			}
			p.QtyLabel = formatQty(qty)
			p.LastUsed = last.Local().Format("02.01.2006")
			p.CostLabel = formatEuro(cost)
			total += cost
			res.Parts = append(res.Parts, p)
		}
		prows.Close()
		res.PartsCost = formatEuro(total)
	}
	return res, nil
}

// InfraHistoryWeb liefert das Historien-Fragment (htmx) der Stammkarte.
func (h *Handler) InfraHistoryWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	id := chi.URLParam(r, "id")
	f := parseInfraHistoryFilter(r)
	f.Sort = h.sortMode(r, "timeline")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.db == nil {
		fmt.Fprint(w, `<div style="color:var(--red);font-size:12px">Keine Datenbank</div>`)
		return
	}
	res, err := h.loadInfraHistory(r.Context(), r, id, f, infraHistoryPageLimit)
	if err != nil {
		componentLog("anlagen").Error().Err(err).Str("anlage", id).Msg("anlagen-historie laden")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	// Export und Sortier-Leiste behalten alle Filter; die Sortierart setzt die Leiste selbst.
	q := r.URL.Query()
	q.Del("ls_timeline")
	exportQuery := r.URL.Query()
	exportQuery.Del("ls_timeline")
	exportQuery.Set("filtered", "1")
	res.ExportURL = fmt.Sprintf("/infrastructure/%s/history.csv?%s", id, exportQuery.Encode())
	res.IsFiltered = f.Query != "" || f.From != nil || f.To != nil || f.State != "" || len(f.Modules) != len(infraHistoryModules)
	res.Lang = h.requestLang(r)
	res.Bar = h.sortBar(r, "timeline", f.Sort, r.URL.Path+"?"+q.Encode(), "#ih-res")
	h.renderFragment(w, "infra-history-results", res)
}

// InfraHistoryCSV exportiert die gefilterte Anlagen-Historie als CSV
// (Semikolon-getrennt, UTF-8 mit BOM fuer Excel).
func (h *Handler) InfraHistoryCSV(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	id := chi.URLParam(r, "id")
	if h.db == nil {
		http.Error(w, "Keine Datenbank", http.StatusServiceUnavailable)
		return
	}
	res, err := h.loadInfraHistory(r.Context(), r, id, parseInfraHistoryFilter(r), 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := "anlage"
	if node, err := h.infra.GetByID(r.Context(), id); err == nil && node != nil {
		name = node.Name
	}
	filename := fmt.Sprintf("historie_%s_%s.csv", sanitizeFilename(name), time.Now().Format("20060102"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Write([]byte("\xef\xbb\xbf"))
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.Write([]string{"Datum", "Modul", "Anlage", "Titel", "Bezug", "Status", "Abgeschlossen", "Menge", "Text", "Benutzer"})
	for _, e := range res.Entries {
		cw.Write([]string{e.DateLabel, e.ModuleLabel, e.InfraName, e.Title, e.ParentTitle, e.StatusLabel,
			e.ClosedLabel, e.QtyLabel, e.Body, e.Author})
	}
	cw.Flush()
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "anlage"
	}
	return b.String()
}

// InfraCommentRefOption ist ein waehlbarer Bezug fuer einen Anlagen-Kommentar.
type InfraCommentRefOption struct {
	Value string // "<ref_type>:<id>"
	Label string
}

// infraCommentRefOptions listet die juengsten Vorgaenge der Anlage als
// moegliche Bezuege eines Kommentars.
func (h *Handler) infraCommentRefOptions(r *http.Request, infraID string) []InfraCommentRefOption {
	ctx := r.Context()
	if h.db == nil {
		return nil
	}
	canViewProject := h.viewFilter(r, "project")
	rows, err := h.db.Query(ctx, `
		SELECT ref_type, id, title FROM (
			SELECT 'fault' AS ref_type, id::text AS id, title::text AS title, created_at FROM faults WHERE infrastructure_id = $1
			UNION ALL SELECT 'ticket', id::text, title, created_at FROM tickets WHERE infrastructure_id = $1
			UNION ALL SELECT 'task', id::text, title, created_at FROM tasks WHERE infrastructure_id = $1
			UNION ALL SELECT 'maintenance_task', id::text, title, COALESCE(completed_at, due_date) FROM maintenance_tasks WHERE infrastructure_id = $1
			UNION ALL SELECT 'project', id::text, name, created_at FROM projects WHERE infrastructure_id = $1
		) x ORDER BY created_at DESC LIMIT 100`, infraID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []InfraCommentRefOption
	for rows.Next() {
		var refType, id, title string
		if rows.Scan(&refType, &id, &title) != nil {
			continue
		}
		if refType == "project" && !canViewProject(id) {
			continue
		}
		list = append(list, InfraCommentRefOption{
			Value: refType + ":" + id,
			Label: infraCommentRefTypes[refType].Label + ": " + title,
		})
	}
	return list
}

// parseInfraCommentRef prueft einen Bezug "<ref_type>:<id>" und stellt
// sicher, dass der Vorgang zur Anlage (oder einer Unteranlage) gehoert.
func (h *Handler) parseInfraCommentRef(ctx context.Context, infraID, raw string) (refType, refID interface{}, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, nil
	}
	parts := strings.SplitN(raw, ":", 2)
	def, ok := infraCommentRefTypes[parts[0]]
	if len(parts) != 2 || !ok {
		return nil, nil, fmt.Errorf("ungültiger Bezug")
	}
	var exists bool
	query := fmt.Sprintf(`
		WITH RECURSIVE`+infraScopeCTE+`
		SELECT EXISTS (SELECT 1 FROM %s WHERE id = $3::uuid AND infrastructure_id IN (SELECT id FROM scope))`, def.Table)
	if err := h.db.QueryRow(ctx, query, infraID, true, parts[1]).Scan(&exists); err != nil || !exists {
		return nil, nil, fmt.Errorf("Bezug gehört nicht zu dieser Anlage")
	}
	return parts[0], parts[1], nil
}

func infraHistoryRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Trigger", "infra-history-refresh")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func infraCommentError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Retarget", "#infra-comment-result")
	w.Header().Set("HX-Reswap", "innerHTML")
	fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(msg))
}

// InfraCommentAddWeb legt einen Kommentar an der Anlage an.
func (h *Handler) InfraCommentAddWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		infraCommentError(w, "Kommentar ist leer")
		return
	}
	refType, refID, err := h.parseInfraCommentRef(r.Context(), id, r.FormValue("ref"))
	if err != nil {
		infraCommentError(w, err.Error())
		return
	}
	u := getUser(r)
	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO infrastructure_comments (infrastructure_id, ref_type, ref_id, text, pinned, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, refType, refID, text, r.FormValue("pinned") == "on", nullID(u.ID)); err != nil {
		infraCommentError(w, "Fehler: "+err.Error())
		return
	}
	infraHistoryRefresh(w)
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Kommentar gespeichert</div>`)
}

// infraCommentAuthor liefert den Ersteller eines Kommentars der Anlage.
func (h *Handler) infraCommentAuthor(ctx context.Context, infraID, commentID string) (string, bool) {
	var author string
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(created_by::text, '') FROM infrastructure_comments
		WHERE id = $1 AND infrastructure_id = $2`, commentID, infraID).Scan(&author)
	return author, err == nil
}

// InfraCommentEditWeb aendert Text (und optional Anheftung) eines Kommentars.
func (h *Handler) InfraCommentEditWeb(w http.ResponseWriter, r *http.Request) {
	id, cid := chi.URLParam(r, "id"), chi.URLParam(r, "commentId")
	author, ok := h.infraCommentAuthor(r.Context(), id, cid)
	if !ok || !h.canModerateInfraComment(r, author) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		http.Error(w, "Kommentar ist leer", http.StatusBadRequest)
		return
	}
	if _, err := h.db.Exec(r.Context(), `
		UPDATE infrastructure_comments SET text = $1, edited_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND infrastructure_id = $3`, text, cid, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infraHistoryRefresh(w)
}

// InfraCommentPinWeb heftet einen Kommentar an bzw. loest ihn.
func (h *Handler) InfraCommentPinWeb(w http.ResponseWriter, r *http.Request) {
	id, cid := chi.URLParam(r, "id"), chi.URLParam(r, "commentId")
	author, ok := h.infraCommentAuthor(r.Context(), id, cid)
	if !ok || !h.canModerateInfraComment(r, author) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(r.Context(), `
		UPDATE infrastructure_comments SET pinned = NOT pinned, updated_at = NOW()
		WHERE id = $1 AND infrastructure_id = $2`, cid, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infraHistoryRefresh(w)
}

// InfraCommentDeleteWeb loescht einen Kommentar (Ersteller oder Admin).
func (h *Handler) InfraCommentDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, cid := chi.URLParam(r, "id"), chi.URLParam(r, "commentId")
	author, ok := h.infraCommentAuthor(r.Context(), id, cid)
	if !ok || !h.canModerateInfraComment(r, author) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM infrastructure_comments WHERE id = $1 AND infrastructure_id = $2`, cid, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infraHistoryRefresh(w)
}
