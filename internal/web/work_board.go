package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/pkg/appsettings"
)

// Arbeitsbereich fuer Tickets, Stoerungen, Aufgaben und Wartungen – aufgebaut
// wie der KVP (kvp.go):
//
//	Dashboard  Kennzahlen des Jahres, Ablauf nach Status, 12-Monats-Verlauf,
//	           Anlagen, Bearbeitende, „Braucht Aufmerksamkeit“
//	Vorgaenge  Filter-Chips mit Anzahl (Alle offenen · Meine · Ohne Zuweisung ·
//	           je Status · Ueberfaellig · Archiv), Suche, Mehrfachauswahl
//	Board      Kanban nach Status, rechts die zuletzt erledigten
//	Regeln     Regeln des Moduls, Standard-Frist, Broker
//
// Die Wartung zeigt zusaetzlich Jahresplan, Plaene, Rundgaenge und Checklisten
// (maintenance_web.go); ihr Archiv mit Checklisten bleibt dort.

type workStatus struct{ Key, Label, Icon, Hint string }

type workKind struct {
	Key, Module, Table, Base, Detail, Page, Title, Context string
	Label, Plural, Items, Icon, DoLabel, DoIcon, PrioExpr  string
	ClosedExpr, DueKey, BrokerColumn                       string
	OpenStatuses, DoneStatuses                             []string
	Flow                                                   []workStatus // offene Status = Board-Spalten
	Rules                                                  []template.HTML
}

var workKinds = map[string]workKind{
	"ticket": {Key: "ticket", Module: "ticket", Table: "tickets", Base: "/tickets", Detail: "/tickets/", Page: "tickets",
		Title: "Tickets", Context: "Kritische Tickets", Label: "Ticket", Plural: "Tickets", Items: "Tickets", Icon: "ti-ticket",
		DoLabel: "Abschließen", DoIcon: "ti-circle-check", PrioExpr: "r.priority::text",
		ClosedExpr: "COALESCE(r.resolved_at, r.archived_at, r.updated_at)", DueKey: appsettings.KeyDefaultDueDaysTicket, BrokerColumn: "broker_tickets",
		OpenStatuses: []string{"open", "in_progress", "pending"}, DoneStatuses: []string{"resolved", "closed"},
		Flow: []workStatus{{"open", "Offen", "ti-circle", "neu, noch nicht begonnen"}, {"in_progress", "In Arbeit", "ti-tool", "wird bearbeitet"},
			{"pending", "Wartet", "ti-hourglass", "auf Teile, Rückmeldung …"}},
		Rules: []template.HTML{
			"<b>Ein Anliegen, ein Ticket.</b> Titel sagt was, die Beschreibung wo und seit wann.",
			"<b>Immer mit Anlage</b>, wenn es um ein Gerät oder einen Bereich geht – sonst fehlt es in der Anlagen-Historie.",
			"<b>Ohne Zuständigen geht es an die Broker.</b> Sie weisen über die Seite „Zuweisung“ zu.",
			"<b>Status aktuell halten:</b> In Arbeit, sobald jemand dran ist; Wartet nur mit Grund im Kommentar.",
			"<b>Absprachen gehören ins Ticket</b> (Kommentare) – so sehen alle Beteiligten den Stand.",
			"<b>Abschließen über den Assistenten:</b> Lösung, Zeit, Material und wer dabei war.",
		}},
	"fault": {Key: "fault", Module: "fault", Table: "faults", Base: "/faults", Detail: "/faults/", Page: "faults",
		Title: "Störungen", Context: "Copilot-Analysen", Label: "Störung", Plural: "Störungen", Items: "Störungen", Icon: "ti-alert-triangle",
		DoLabel: "Beheben", DoIcon: "ti-tool", PrioExpr: "r.severity::text",
		ClosedExpr: "COALESCE(r.resolved_at, r.archived_at, r.updated_at)", DueKey: appsettings.KeyDefaultDueDaysFault, BrokerColumn: "broker_faults",
		OpenStatuses: []string{"detected", "analyzing", "in_progress", "pending"}, DoneStatuses: []string{"resolved", "closed"},
		Flow: []workStatus{{"detected", "Erkannt", "ti-alert-triangle", "frisch gemeldet"}, {"analyzing", "Analysiert", "ti-brain", "Ursache wird gesucht"},
			{"in_progress", "In Bearbeitung", "ti-tool", "wird behoben"}, {"pending", "Wartet", "ti-hourglass", "auf Teile, Fremdfirma …"}},
		Rules: []template.HTML{
			"<b>Sicherheit zuerst.</b> Gefahr für Personen: Anlage sichern, dann melden.",
			"<b>Sofort melden – mit Anlage und Symptomen.</b> Je genauer, desto besser passen die ähnlichen Fälle.",
			"<b>Ähnliche Fälle und Copilot nutzen</b>, bevor gesucht wird – vielleicht ist die Lösung schon bekannt.",
			"<b>Schweregrad ehrlich wählen:</b> kritisch nur bei Stillstand oder Gefahr.",
			"<b>Ursache und Lösung festhalten.</b> Ohne Ursache kommt die Störung wieder.",
			"<b>Wiederkehrende Störungen</b> werden zum KVP-Vorschlag oder zum Wartungsplan.",
		}},
	"task": {Key: "task", Module: "task", Table: "tasks", Base: "/tasks", Detail: "/tasks/", Page: "tasks",
		Title: "Aufgaben", Context: "Aufgaben", Label: "Aufgabe", Plural: "Aufgaben", Items: "Aufgaben", Icon: "ti-list-check",
		DoLabel: "Erledigen", DoIcon: "ti-circle-check", PrioExpr: "r.priority::text",
		ClosedExpr: "COALESCE(r.resolved_at, r.archived_at, r.updated_at)", DueKey: appsettings.KeyDefaultDueDaysTask, BrokerColumn: "broker_tasks",
		OpenStatuses: []string{"open", "in_progress", "pending"}, DoneStatuses: []string{"resolved", "closed"},
		Flow: []workStatus{{"open", "Offen", "ti-circle", "noch nicht begonnen"}, {"in_progress", "In Arbeit", "ti-tool", "wird erledigt"},
			{"pending", "Wartet", "ti-hourglass", "blockiert"}},
		Rules: []template.HTML{
			"<b>Wer macht was bis wann?</b> Jede Aufgabe hat Zuständige und einen Termin.",
			"<b>Eine verantwortliche Person</b> steht dafür gerade, auch wenn mehrere mitarbeiten.",
			"<b>Größeres gehört in ein Projekt</b> – dort sieht man Fortschritt und Überfälliges auf einen Blick.",
			"<b>Termine realistisch setzen</b> und lieber früh verschieben als still überziehen.",
			"<b>Erledigen über den Assistenten:</b> Was wurde gemacht, wie lange, wer war dabei.",
		}},
	"maintenance": {Key: "maintenance", Module: "maintenance_task", Table: "maintenance_tasks", Base: "/maintenance", Detail: "/maintenance/tasks/", Page: "maintenance",
		Title: "Wartung", Context: "Anstehend", Label: "Wartung", Plural: "Wartungen", Items: "Aufträge", Icon: "ti-tool",
		DoLabel: "Durchführen", DoIcon: "ti-player-play", PrioExpr: "r.priority::text",
		ClosedExpr: "COALESCE(r.completed_at, r.updated_at)", DueKey: appsettings.KeyDefaultDueDaysMaintenance, BrokerColumn: "broker_maintenance",
		OpenStatuses: []string{"open", "in_progress", "pending"}, DoneStatuses: []string{"done"},
		Flow: []workStatus{{"open", "Anstehend", "ti-calendar-event", "im Vorlauf oder fällig"}, {"in_progress", "In Arbeit", "ti-tool", "Checkliste läuft"},
			{"pending", "Wartet", "ti-hourglass", "auf Teile, Stillstand …"}},
		Rules: []template.HTML{
			"<b>Wiederkehrendes gehört in einen Plan</b>, nicht in Einzelaufträge – der Plan legt den nächsten Auftrag selbst an.",
			"<b>Der Vorlauf zählt:</b> Aufträge erscheinen, sobald ihr Vorlauf beginnt – dann einplanen.",
			"<b>Checkliste vollständig ausfüllen</b>, Messwerte mit Einheit; Abweichungen mit Foto und Bemerkung.",
			"<b>Abweichung heißt handeln:</b> Störung oder Ticket direkt aus dem Auftrag anlegen.",
			"<b>Überspringen nur mit Grund</b> – er steht im Archiv.",
			"<b>Das Protokoll (PDF) ist der Nachweis</b> – es entsteht beim Abschluss automatisch.",
		}},
}

type workCard struct {
	ID, Title, Description, InfraID, InfraName, ProjectID, ProjectName string
	Due, Status, StatusLabel, StatusClass                              string
	Priority, PriorityLabel, PriorityClass, PriorityDot, Who           string
	Overdue, DueNow, Unassigned, New, Mine, Upcoming                   bool // DueNow: faellig bis heute
	CreatedAgo                                                         string
}

type workChip struct {
	Key, Label, Icon, URL string
	Count                 int
	Active                bool
}

type workKPI struct{ Label, Value, Sub, Class, URL string }

type workBar struct {
	Label, Icon, Class, URL string
	Count, Pct              int
}

type workMonth struct {
	Label                          string
	Created, Done, HCreated, HDone int
}

type workDash struct {
	Year, PrevYear, NextYear int
	KPIs                     []workKPI
	Funnel                   []workBar
	Months                   []workMonth
	SumCreated, SumDone      int
	Infra, People            []workBar
	Attention                []workCard
}

type workCol struct {
	workStatus
	Cards []workCard
	More  int
}

type workLink struct{ Key, Href, Icon, Label string }

type workArchiveRow struct {
	ID, Title, InfraName, ProjectName, Status, StatusLabel, StatusClass string
	Closed, Who, Resolution, RootCause, Priority, PriorityClass         string
}

type workArchive struct {
	Q, Infra, From, To string
	Total, Page, Pages int
	PrevURL, NextURL   string
	Filtered           bool
	Rows               []workArchiveRow
}

// workArea: alles, was die Reiter brauchen (auch in maintenance.gohtml eingebettet).
type workArea struct {
	Kind                         workKind
	Tab, Chip                    string
	Chips                        []workChip
	Items                        []workCard
	Q, Prio, Infra, Project      string
	ProjectName                  string
	Filtered                     bool
	InfraOptions, ProjectOptions []UserOption
	Dash                         workDash
	Board                        []workCol
	RecentDone                   []workCard
	Archive                      workArchive
	Extra                        []workLink // weitere Reiter (Wartung: Jahresplan …)
	ExtraActive                  string
	DueDays                      int
	Brokers                      string
	CanSettings                  bool
	Notice                       string
	OpenCount, OverdueCount      int
	UnassignedCount              int
}

type WorkBoardData struct {
	BaseData
	Area workArea
}

// workRoute: Reiter und Chip aus der Adresse; alte Adressen (?view=, ?status=,
// ?unassigned=) fuehren in den passenden Chip.
func workRoute(q url.Values) (tab, chip string) {
	switch t := q.Get("tab"); t {
	case "dashboard", "items", "board", "rules":
		tab = t
	}
	chip = q.Get("chip")
	if s := q.Get("status"); s != "" && chip == "" && tab != "board" {
		chip = "s:" + s
	}
	switch q.Get("view") {
	case "archive", "done":
		chip = "archive"
	case "due", "list":
		if tab == "" {
			tab = "items"
		}
	}
	switch {
	case q.Get("unassigned") == "1" || q.Get("free") == "1":
		chip = "free"
	case q.Get("unassigned") == "true":
		chip = "noproject"
	case q.Get("mine") == "1" && chip == "":
		chip = "mine"
	}
	if tab == "" {
		tab = "dashboard"
		for _, k := range []string{"q", "project", "infra", "prio", "tag", "page"} {
			if q.Get(k) != "" {
				tab = "items"
			}
		}
		if chip != "" {
			tab = "items"
		}
	}
	if tab == "items" && chip == "" {
		chip = "open"
	}
	return tab, chip
}

func prioRank(p string) int {
	switch p {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	}
	return 3
}

// WorkBoardPage: Arbeitsbereich fuer Tickets, Stoerungen und Aufgaben.
func (h *Handler) WorkBoardPage(w http.ResponseWriter, r *http.Request, kindKey string) {
	k := workKinds[kindKey]
	d := WorkBoardData{BaseData: h.baseData(r, k.Page, k.Title, k.Context)}
	d.Area = h.buildWorkArea(r, k, "")
	if k.Key == "task" {
		d.Area.Extra = []workLink{{"projects", "/projects", "ti-timeline", "Projekte"}}
	}
	h.render(w, "work_board", d)
}

// buildWorkArea fuellt den gewaehlten Reiter; tab leer = aus der Adresse.
func (h *Handler) buildWorkArea(r *http.Request, k workKind, tab string) workArea {
	q := r.URL.Query()
	a := workArea{Kind: k, Q: strings.TrimSpace(q.Get("q")), Prio: q.Get("prio"), Infra: q.Get("infra"), Project: q.Get("project"),
		Notice: q.Get("notice")}
	a.Tab, a.Chip = workRoute(q)
	if tab != "" {
		a.Tab = tab
	}
	a.Filtered = a.Q != "" || a.Prio != "" || a.Infra != "" || a.Project != ""
	if h.db == nil {
		return a
	}
	ctx := r.Context()
	open := h.workOpenCards(r, k)
	a.countOpen(open)
	a.options(open)
	switch a.Tab {
	case "dashboard":
		a.Dash = h.workDashboard(ctx, r, k, open, &a)
	case "items":
		a.buildChips(open)
		if a.Chip == "archive" {
			if k.Key != "maintenance" {
				h.workArchiveRows(r, &a)
			}
		} else {
			a.Items = a.filterItems(open)
		}
	case "board":
		a.buildBoard(open)
		a.RecentDone = h.workRecentDone(ctx, r, k)
	case "rules":
		a.DueDays = appsettings.GetInt(ctx, h.db, k.DueKey, appsettings.DefaultDueDaysFallback)
		a.Brokers = strings.Join(h.chatNames(ctx, h.brokerIDsByColumn(ctx, k.BrokerColumn)), ", ")
		a.CanSettings = h.canManageRoles(r)
	}
	return a
}

func (h *Handler) brokerIDsByColumn(ctx context.Context, column string) []string {
	for kind, b := range brokerKinds {
		if b.column == column {
			return h.brokerIDs(ctx, kind)
		}
	}
	return nil
}

// workBaseSQL: offene Vorgaenge einer Art mit Zustaendigen, „meine“, „frei“,
// Projekt und (Wartung) „noch nicht im Vorlauf“.
func (h *Handler) workBaseSQL(k workKind) string {
	who := `COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name, '')`
	unassigned := `r.assigned_to IS NULL AND r.assigned_group_id IS NULL`
	mine := `(r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs
	project := `'', ''`
	joins := ``
	userJoin := `r.assigned_to`
	upcoming := `false`
	switch k.Key {
	case "task":
		// Aufgaben haben keine Spalte assigned_to (migrations/062), sondern task_assignees
		userJoin = `NULL::uuid`
		unassigned = `r.assigned_group_id IS NULL AND NOT EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id)`
		mine = `(r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs +
			` OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $1::uuid)`
		who = `COALESCE(NULLIF((SELECT string_agg(TRIM(au.first_name || ' ' || au.last_name), ', ' ORDER BY au.last_name)
		          FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = r.id), ''), g.name, '')`
		project = `COALESCE(r.project_id::text, ''), COALESCE(p.name, '')`
		joins = ` LEFT JOIN projects p ON p.id = r.project_id`
	case "maintenance":
		// wie „Anstehend“ frueher: noch nicht begonnen, Termin nach 7 Tagen und Vorlauf noch nicht erreicht
		upcoming = `(r.status = 'open' AND r.due_date::date > CURRENT_DATE + 7
		   AND r.due_date::date - COALESCE((SELECT mp.lead_days FROM maintenance_plans mp WHERE mp.id = r.plan_id), 0) > CURRENT_DATE)`
	}
	mine += `)`
	// „meine“ und „noch nicht im Vorlauf“ mit COALESCE: bei leeren Spalten
	// (z. B. ohne Verantwortlichen oder Termin) ergibt der Vergleich NULL, die
	// Zeile liesse sich nicht lesen und der Vorgang fehlte in der Liste
	return fmt.Sprintf(`
		SELECT r.id::text, r.title, COALESCE(r.description, ''), r.status::text, %s, r.due_date,
		       COALESCE(r.infrastructure_id::text, ''), COALESCE(i.name, ''), %s, COALESCE(%s, false), (%s), %s, r.created_at, COALESCE(%s, false)
		  FROM %s r
		  LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		  LEFT JOIN users u ON u.id = %s
		  LEFT JOIN user_groups g ON g.id = r.assigned_group_id%s`,
		k.PrioExpr, who, mine, unassigned, project, upcoming, k.Table, userJoin, joins)
}

// workOpenCards: alle offenen Vorgaenge (Abteilungs-Sichtbarkeit und Kategorien beachtet).
func (h *Handler) workOpenCards(r *http.Request, k workKind) []workCard {
	if h.db == nil {
		return nil
	}
	ctx := r.Context()
	statuses := "'" + strings.Join(k.OpenStatuses, "','") + "'"
	rows, err := h.db.Query(ctx, h.workBaseSQL(k)+`
		 WHERE r.archived_at IS NULL AND r.status::text IN (`+statuses+`)
		 ORDER BY r.due_date NULLS LAST, r.created_at`, nullID(getUser(r).ID))
	if err != nil {
		componentLog("arbeitsbereich").Error().Err(err).Str("art", k.Key).Msg("vorgaenge laden")
		return nil
	}
	defer rows.Close()
	tagIDs := h.categoryFilterIDs(r, k.Module)
	scopeIDs := h.scopeAllowedIDs(r, k.Module)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var out []workCard
	for rows.Next() {
		var c workCard
		var due *time.Time
		var created time.Time
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Status, &c.Priority, &due, &c.InfraID, &c.InfraName, &c.Who, &c.Mine, &c.Unassigned,
			&c.ProjectID, &c.ProjectName, &created, &c.Upcoming); err != nil {
			componentLog("arbeitsbereich").Warn().Err(err).Str("art", k.Key).Msg("vorgang lesen")
			continue
		}
		if tagIDs != nil && !tagIDs[c.ID] || scopeIDs != nil && !scopeIDs[c.ID] {
			continue
		}
		if due != nil {
			dl := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.Local)
			c.Due = dl.Format("02.01.2006")
			c.Overdue = dl.Before(today)
			c.DueNow = !dl.After(today)
		}
		c.New = c.Status == "detected" || (c.Status == "open" && c.Unassigned)
		c.StatusLabel, c.StatusClass = statusLabel(c.Status), statusClass(c.Status)
		if k.Key == "maintenance" && c.Status == "open" {
			c.StatusLabel = "Anstehend"
		}
		c.PriorityLabel, c.PriorityDot = severityWord(c.Priority), priorityDot(c.Priority)
		if k.Key == "fault" {
			c.PriorityClass = severityClass(c.Priority)
		} else {
			c.PriorityClass = priorityClass(c.Priority)
		}
		c.CreatedAgo = timeAgo(created)
		out = append(out, c)
	}
	return out
}

func (a *workArea) countOpen(open []workCard) {
	for _, c := range open {
		if c.Upcoming {
			continue
		}
		a.OpenCount++
		if c.Overdue {
			a.OverdueCount++
		}
		if c.Unassigned {
			a.UnassignedCount++
		}
	}
}

// chipMatch: gehoert der Vorgang zum Chip?
func chipMatch(chip string, c workCard) bool {
	if chip == "planned" {
		return c.Upcoming
	}
	if c.Upcoming {
		return false
	}
	switch chip {
	case "open":
		return true
	case "mine":
		return c.Mine
	case "free":
		return c.Unassigned
	case "overdue":
		return c.Overdue
	case "due":
		return c.DueNow
	case "noproject":
		return c.ProjectID == ""
	}
	if s, ok := strings.CutPrefix(chip, "s:"); ok {
		return c.Status == s
	}
	return false
}

func (a *workArea) chipURL(chip string) string {
	v := url.Values{}
	v.Set("tab", "items")
	v.Set("chip", chip)
	if chip != "archive" {
		for key, val := range map[string]string{"q": a.Q, "prio": a.Prio, "infra": a.Infra, "project": a.Project} {
			if val != "" {
				v.Set(key, val)
			}
		}
	}
	return a.Kind.Base + "?" + v.Encode()
}

func (a *workArea) buildChips(open []workCard) {
	count := func(chip string) int {
		n := 0
		for _, c := range open {
			if chipMatch(chip, c) {
				n++
			}
		}
		return n
	}
	add := func(key, label, icon string, n int) {
		a.Chips = append(a.Chips, workChip{Key: key, Label: label, Icon: icon, URL: a.chipURL(key), Count: n, Active: a.Chip == key})
	}
	add("open", "Alle offenen", "ti-list", count("open"))
	add("mine", "Meine", "ti-user", count("mine"))
	add("free", "Ohne Zuweisung", "ti-inbox", count("free"))
	for _, s := range a.Kind.Flow {
		add("s:"+s.Key, s.Label, s.Icon, count("s:"+s.Key))
	}
	add("overdue", "Überfällig", "ti-alarm", count("overdue"))
	if a.Kind.Key == "task" {
		add("noproject", "Ohne Projekt", "ti-folder-off", count("noproject"))
	}
	if a.Kind.Key == "maintenance" {
		add("due", "Fällig", "ti-calendar-due", count("due")) // wie die Dashboard-Kachel „Wartung fällig“
		add("planned", "Geplant", "ti-calendar-time", count("planned"))
	}
	add("archive", "Archiv", "ti-archive", -1)
}

// filterItems: Chip plus Suche, Prioritaet, Anlage, Projekt.
func (a *workArea) filterItems(open []workCard) []workCard {
	q := strings.ToLower(a.Q)
	var out []workCard
	for _, c := range open {
		if !chipMatch(a.Chip, c) || !a.passes(c) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Title+" "+c.Description+" "+c.InfraName+" "+c.Who+" "+c.ProjectName), q) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (a *workArea) passes(c workCard) bool {
	return (a.Prio == "" || c.Priority == a.Prio) && (a.Infra == "" || c.InfraID == a.Infra) && (a.Project == "" || c.ProjectID == a.Project)
}

func (a *workArea) options(open []workCard) {
	infra, projects := map[string]string{}, map[string]string{}
	for _, c := range open {
		if c.InfraID != "" {
			infra[c.InfraID] = c.InfraName
		}
		if c.ProjectID != "" {
			projects[c.ProjectID] = c.ProjectName
		}
	}
	a.InfraOptions = sortedOptions(infra)
	a.ProjectOptions = sortedOptions(projects)
	a.ProjectName = projects[a.Project]
}

const workBoardColLimit = 60

func (a *workArea) buildBoard(open []workCard) {
	for _, s := range a.Kind.Flow {
		col := workCol{workStatus: s}
		for _, c := range open {
			if c.Status != s.Key || c.Upcoming || !a.passes(c) {
				continue
			}
			col.Cards = append(col.Cards, c)
		}
		// Ueberfaelliges, dann Wichtigstes zuerst, sonst nach Termin (bereits sortiert)
		sort.SliceStable(col.Cards, func(i, j int) bool {
			if col.Cards[i].Overdue != col.Cards[j].Overdue {
				return col.Cards[i].Overdue
			}
			return prioRank(col.Cards[i].Priority) < prioRank(col.Cards[j].Priority)
		})
		if len(col.Cards) > workBoardColLimit {
			col.More = len(col.Cards) - workBoardColLimit
			col.Cards = col.Cards[:workBoardColLimit]
		}
		a.Board = append(a.Board, col)
	}
}

// workScopeCond: Abteilungs-Sichtbarkeit als SQL-Bedingung (IDs als Parameter).
func workScopeCond(scope map[string]bool, args *[]any) string {
	if scope == nil {
		return ""
	}
	ids := make([]string, 0, len(scope))
	for id, ok := range scope {
		if ok {
			ids = append(ids, id)
		}
	}
	*args = append(*args, ids)
	return fmt.Sprintf(" AND r.id::text = ANY($%d::text[])", len(*args))
}

func (h *Handler) workRecentDone(ctx context.Context, r *http.Request, k workKind) []workCard {
	var args []any
	cond := workScopeCond(h.scopeAllowedIDs(r, k.Module), &args)
	rows, err := h.db.Query(ctx, `SELECT r.id::text, r.title, COALESCE(i.name, ''), r.status::text, to_char(`+k.ClosedExpr+`, 'DD.MM.')
		FROM `+k.Table+` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		WHERE r.status::text IN ('`+strings.Join(k.DoneStatuses, "','")+`') AND `+k.ClosedExpr+` >= NOW() - INTERVAL '7 days'`+cond+`
		ORDER BY `+k.ClosedExpr+` DESC LIMIT 15`, args...)
	if err != nil {
		componentLog("arbeitsbereich").Warn().Err(err).Str("art", k.Key).Msg("zuletzt erledigte")
		return nil
	}
	defer rows.Close()
	var out []workCard
	for rows.Next() {
		var c workCard
		if rows.Scan(&c.ID, &c.Title, &c.InfraName, &c.Status, &c.Due) == nil {
			c.StatusLabel, c.StatusClass = statusLabel(c.Status), statusClass(c.Status)
			out = append(out, c)
		}
	}
	return out
}

var workMonthNames = []string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"}

// workDashboard: Kennzahlen des Jahres wie im KVP-Dashboard.
func (h *Handler) workDashboard(ctx context.Context, r *http.Request, k workKind, open []workCard, a *workArea) workDash {
	d := workDash{Year: time.Now().Year()}
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && y > 2000 && y < 2200 {
		d.Year = y
	}
	d.PrevYear, d.NextYear = d.Year-1, d.Year+1
	from := time.Date(d.Year, 1, 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(1, 0, 0)
	done := "r.status::text IN ('" + strings.Join(k.DoneStatuses, "','") + "')"
	inYear := k.ClosedExpr + " >= $1 AND " + k.ClosedExpr + " < $2"
	scope := h.scopeAllowedIDs(r, k.Module)
	args := []any{from, to}
	cond := workScopeCond(scope, &args)

	var created, finished, onTime, withDue int
	var avgDays float64
	if err := h.db.QueryRow(ctx, `SELECT
		COUNT(*) FILTER (WHERE r.created_at >= $1 AND r.created_at < $2),
		COUNT(*) FILTER (WHERE `+done+` AND `+inYear+`),
		COUNT(*) FILTER (WHERE `+done+` AND `+inYear+` AND r.due_date IS NOT NULL),
		COUNT(*) FILTER (WHERE `+done+` AND `+inYear+` AND r.due_date IS NOT NULL AND (`+k.ClosedExpr+`)::date <= r.due_date::date),
		COALESCE(AVG(EXTRACT(EPOCH FROM `+k.ClosedExpr+` - r.created_at) / 86400) FILTER (WHERE `+done+` AND `+inYear+`), 0)::float8
		FROM `+k.Table+` r WHERE true`+cond, args...).Scan(&created, &finished, &withDue, &onTime, &avgDays); err != nil {
		componentLog("arbeitsbereich").Warn().Err(err).Str("art", k.Key).Msg("kennzahlen")
	}

	quote := "–"
	if withDue > 0 {
		quote = strconv.Itoa(onTime*100/withDue) + " %"
	}
	lead := "–"
	if finished > 0 {
		lead = strings.Replace(strconv.FormatFloat(avgDays, 'f', 1, 64), ".", ",", 1) + " Tage"
	}
	overdueClass, freeClass := "", ""
	if a.OverdueCount > 0 {
		overdueClass = "red"
	}
	if a.UnassignedCount > 0 {
		freeClass = "amber"
	}
	year := strconv.Itoa(d.Year)
	d.KPIs = []workKPI{
		{"Offen", strconv.Itoa(a.OpenCount), "jetzt", "", a.chipURL("open")},
		{"Überfällig", strconv.Itoa(a.OverdueCount), "Termin überschritten", overdueClass, a.chipURL("overdue")},
		{"Ohne Zuweisung", strconv.Itoa(a.UnassignedCount), "weder Person noch Gruppe", freeClass, a.chipURL("free")},
		{"Angelegt " + year, strconv.Itoa(created), k.Plural, "", ""},
		{"Erledigt " + year, strconv.Itoa(finished), quote + " termingerecht", "green", a.chipURL("archive")},
		{"Ø Durchlaufzeit", lead, "Anlage bis Abschluss " + year, "", ""},
	}

	// Ablauf: offene je Status + erledigt im Jahr
	counts := map[string]int{}
	planned := 0
	for _, c := range open {
		if c.Upcoming {
			planned++
			continue
		}
		counts[c.Status]++
	}
	top := finished
	if planned > top {
		top = planned
	}
	for _, s := range k.Flow {
		if counts[s.Key] > top {
			top = counts[s.Key]
		}
	}
	if k.Key == "maintenance" {
		d.Funnel = append(d.Funnel, workBar{"Geplant (vor dem Vorlauf)", "ti-calendar-time", "b-gray", a.chipURL("planned"), planned, pct(planned, top)})
	}
	for _, s := range k.Flow {
		d.Funnel = append(d.Funnel, workBar{s.Label, s.Icon, statusClass(s.Key), a.chipURL("s:" + s.Key), counts[s.Key], pct(counts[s.Key], top)})
	}
	d.Funnel = append(d.Funnel, workBar{"Erledigt " + year, "ti-circle-check", "b-green", a.chipURL("archive"), finished, pct(finished, top)})

	// letzte 12 Monate: angelegt / erledigt
	var margs []any
	mcond := workScopeCond(scope, &margs)
	if rows, err := h.db.Query(ctx, `SELECT EXTRACT(MONTH FROM m)::int,
		(SELECT COUNT(*) FROM `+k.Table+` r WHERE date_trunc('month', r.created_at) = m`+mcond+`)::int,
		(SELECT COUNT(*) FROM `+k.Table+` r WHERE `+done+` AND date_trunc('month', `+k.ClosedExpr+`) = m`+mcond+`)::int
		FROM generate_series(date_trunc('month', NOW()) - INTERVAL '11 months', date_trunc('month', NOW()), INTERVAL '1 month') m
		ORDER BY m`, margs...); err == nil {
		for rows.Next() {
			var mo workMonth
			var n int
			if rows.Scan(&n, &mo.Created, &mo.Done) == nil && n >= 1 && n <= 12 {
				mo.Label = workMonthNames[n-1]
				d.Months = append(d.Months, mo)
				d.SumCreated += mo.Created
				d.SumDone += mo.Done
			}
		}
		rows.Close()
	} else {
		componentLog("arbeitsbereich").Warn().Err(err).Str("art", k.Key).Msg("monatsverlauf")
	}
	mmax := 1
	for _, m := range d.Months {
		mmax = max(mmax, m.Created, m.Done)
	}
	for i := range d.Months {
		d.Months[i].HCreated, d.Months[i].HDone = pct(d.Months[i].Created, mmax), pct(d.Months[i].Done, mmax)
	}

	// Anlagen mit den meisten Vorgaengen im Jahr
	if rows, err := h.db.Query(ctx, `SELECT i.id::text, i.name, COUNT(*)::int FROM `+k.Table+` r JOIN infrastructure i ON i.id = r.infrastructure_id
		WHERE r.created_at >= $1 AND r.created_at < $2`+cond+` GROUP BY 1, 2 ORDER BY 3 DESC, 2 LIMIT 6`, args...); err == nil {
		for rows.Next() {
			var id string
			var b workBar
			if rows.Scan(&id, &b.Label, &b.Count) == nil {
				b.URL = k.Base + "?tab=items&chip=open&infra=" + url.QueryEscape(id)
				d.Infra = append(d.Infra, b)
			}
		}
		rows.Close()
	}
	// wer hat im Jahr am meisten erledigt (Person oder Gruppe)
	pq := `SELECT COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name), COUNT(*)::int FROM ` + k.Table + ` r
		LEFT JOIN users u ON u.id = r.assigned_to LEFT JOIN user_groups g ON g.id = r.assigned_group_id
		WHERE ` + done + ` AND ` + inYear + ` AND (r.assigned_to IS NOT NULL OR r.assigned_group_id IS NOT NULL)` + cond + `
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 6`
	if k.Key == "task" {
		pq = `SELECT TRIM(u.first_name || ' ' || u.last_name), COUNT(*)::int FROM tasks r
			JOIN task_assignees ta ON ta.task_id = r.id JOIN users u ON u.id = ta.user_id
			WHERE ` + done + ` AND ` + inYear + cond + `
			GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 6`
	}
	if rows, err := h.db.Query(ctx, pq, args...); err == nil {
		for rows.Next() {
			var b workBar
			if rows.Scan(&b.Label, &b.Count) == nil {
				d.People = append(d.People, b)
			}
		}
		rows.Close()
	} else {
		componentLog("arbeitsbereich").Warn().Err(err).Str("art", k.Key).Msg("bearbeitende")
	}
	for _, list := range [][]workBar{d.Infra, d.People} {
		best := 0
		for _, b := range list {
			best = max(best, b.Count)
		}
		for i := range list {
			list[i].Pct = pct(list[i].Count, best)
		}
	}

	// Braucht Aufmerksamkeit: Ueberfaelliges, dann Dringendes ohne Zustaendigen
	for _, c := range open {
		if c.Overdue && !c.Upcoming {
			d.Attention = append(d.Attention, c)
		}
	}
	for _, c := range open {
		if !c.Overdue && !c.Upcoming && c.Unassigned && prioRank(c.Priority) <= 1 {
			d.Attention = append(d.Attention, c)
		}
	}
	if len(d.Attention) > 10 {
		d.Attention = d.Attention[:10]
	}
	return d
}

// WorkSettingsWeb: POST /work/{kind}/settings – Standard-Frist des Moduls.
func (h *Handler) WorkSettingsWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := workKinds[chi.URLParam(r, "kind")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	back := k.Base + "?tab=rules&notice="
	days, err := strconv.Atoi(r.FormValue("due_days"))
	if err != nil || days < 1 || days > 365 {
		http.Redirect(w, r, back+url.QueryEscape("Die Standard-Frist muss zwischen 1 und 365 Tagen liegen"), http.StatusSeeOther)
		return
	}
	if err := h.setUpdateSetting(r.Context(), k.DueKey, strconv.Itoa(days)); err != nil {
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back+url.QueryEscape("Standard-Frist gespeichert"), http.StatusSeeOther)
}

func sortedOptions(m map[string]string) []UserOption {
	out := make([]UserOption, 0, len(m))
	for id, name := range m {
		out = append(out, UserOption{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

const workArchivePageSize = 50

// workArchiveRows: Chip „Archiv“ fuer Tickets, Stoerungen und Aufgaben.
func (h *Handler) workArchiveRows(r *http.Request, area *workArea) {
	ctx := r.Context()
	k := area.Kind
	q := r.URL.Query()
	a := &area.Archive
	a.Q, a.Infra, a.From, a.To = strings.TrimSpace(q.Get("q")), q.Get("infra"), q.Get("from"), q.Get("to")
	a.Filtered = a.Q != "" || a.Infra != "" || a.From != "" || a.To != "" || area.Project != ""
	a.Page, _ = strconv.Atoi(q.Get("page"))
	if a.Page < 1 {
		a.Page = 1
	}
	where := []string{"(r.archived_at IS NOT NULL OR r.status::text IN ('resolved','closed'))"}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if a.Q != "" {
		p := arg("%" + a.Q + "%")
		where = append(where, "(r.title ILIKE "+p+" OR COALESCE(r.description,'') ILIKE "+p+" OR COALESCE(i.name,'') ILIKE "+p+" OR COALESCE(r.resolution,'') ILIKE "+p+")")
	}
	if a.Infra != "" {
		where = append(where, "r.infrastructure_id::text = "+arg(a.Infra))
	}
	if area.Project != "" && k.Key == "task" {
		where = append(where, "r.project_id::text = "+arg(area.Project))
	}
	closed := k.ClosedExpr
	if _, err := time.Parse("2006-01-02", a.From); err == nil {
		where = append(where, closed+" >= "+arg(a.From)+"::date")
	}
	if _, err := time.Parse("2006-01-02", a.To); err == nil {
		where = append(where, closed+" < "+arg(a.To)+"::date + 1")
	}
	cond := " WHERE " + strings.Join(where, " AND ")
	project := `''`
	joins := ``
	who := `COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name, '')`
	userJoin := `r.assigned_to`
	if k.Key == "task" {
		project, joins = `COALESCE(p.name, '')`, ` LEFT JOIN projects p ON p.id = r.project_id`
		userJoin = `NULL::uuid` // Aufgaben: task_assignees statt assigned_to
		who = `COALESCE((SELECT string_agg(TRIM(au.first_name || ' ' || au.last_name), ', ' ORDER BY au.last_name)
		          FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = r.id), g.name, '')`
	}
	from := ` FROM ` + k.Table + ` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id` + joins
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*)`+from+cond, args...).Scan(&a.Total)
	rows, err := h.db.Query(ctx, `
		SELECT r.id::text, r.title, COALESCE(i.name, ''), `+project+`, r.status::text, `+k.PrioExpr+`,
		       to_char(`+closed+`, 'DD.MM.YYYY'), COALESCE(r.resolution, ''), COALESCE(r.root_cause, ''),
		       `+who+from+`
		  LEFT JOIN users u ON u.id = `+userJoin+`
		  LEFT JOIN user_groups g ON g.id = r.assigned_group_id`+cond+`
		 ORDER BY `+closed+` DESC LIMIT `+strconv.Itoa(workArchivePageSize)+` OFFSET `+strconv.Itoa((a.Page-1)*workArchivePageSize), args...)
	if err != nil {
		componentLog("arbeitsbereich").Error().Err(err).Str("art", k.Key).Msg("archiv laden")
		return
	}
	defer rows.Close()
	scopeIDs := h.scopeAllowedIDs(r, k.Module)
	for rows.Next() {
		var x workArchiveRow
		if rows.Scan(&x.ID, &x.Title, &x.InfraName, &x.ProjectName, &x.Status, &x.Priority, &x.Closed, &x.Resolution, &x.RootCause, &x.Who) != nil {
			continue
		}
		if scopeIDs != nil && !scopeIDs[x.ID] {
			continue
		}
		x.StatusLabel, x.StatusClass = statusLabel(x.Status), statusClass(x.Status)
		if k.Key == "fault" {
			x.PriorityClass = severityClass(x.Priority)
		} else {
			x.PriorityClass = priorityClass(x.Priority)
		}
		x.Priority = severityWord(x.Priority)
		a.Rows = append(a.Rows, x)
	}
	a.Pages = (a.Total + workArchivePageSize - 1) / workArchivePageSize
	page := func(n int) string {
		v := url.Values{}
		v.Set("tab", "items")
		v.Set("chip", "archive")
		for _, key := range []string{"q", "infra", "from", "to", "project"} {
			if s := q.Get(key); s != "" {
				v.Set(key, s)
			}
		}
		v.Set("page", strconv.Itoa(n))
		return k.Base + "?" + v.Encode()
	}
	if a.Page > 1 {
		a.PrevURL = page(a.Page - 1)
	}
	if a.Page < a.Pages {
		a.NextURL = page(a.Page + 1)
	}
	// Anlagen fuer den Filter
	opts := map[string]string{}
	if rs, err := h.db.Query(ctx, `SELECT DISTINCT i.id::text, i.name FROM `+k.Table+` r JOIN infrastructure i ON i.id = r.infrastructure_id
		WHERE r.archived_at IS NOT NULL OR r.status::text IN ('resolved','closed')`); err == nil {
		for rs.Next() {
			var id, n string
			if rs.Scan(&id, &n) == nil {
				opts[id] = n
			}
		}
		rs.Close()
	}
	area.InfraOptions = sortedOptions(opts)
}
