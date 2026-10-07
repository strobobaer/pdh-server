package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Arbeitsansicht fuer Tickets, Stoerungen und Aufgaben – aufgebaut wie die
// Wartung (maintenance_web.go):
//
//	Anstehend  Karten nach Dringlichkeit (Ueberfaellig · Heute · Naechste 7 Tage ·
//	           Spaeter · Ohne Termin), Filter „Nur meine“, „Ohne Zuweisung“,
//	           Anlage, Prioritaet; grosser Knopf startet den Abschluss-Assistenten
//	Liste      die bisherige Tabelle mit Status-Reitern und Mehrfachauswahl
//	Archiv     Suche, Filter nach Anlage/Zeitraum, seitenweise, Loesung aufklappbar
//
// Aufgaben zeigen zusaetzlich ihr Projekt; Projekte sind die uebergeordnete
// Ebene (projects_handler.go).

type workKind struct {
	Key, Module, Table, Base, Page, Title, Context string
	Label, Plural, Icon, DoLabel, DoIcon, PrioExpr string
	OpenStatuses                                   []string
}

var workKinds = map[string]workKind{
	"ticket": {Key: "ticket", Module: "ticket", Table: "tickets", Base: "/tickets", Page: "tickets", Title: "Tickets", Context: "Kritische Tickets",
		Label: "Ticket", Plural: "Tickets", Icon: "ti-ticket", DoLabel: "Bearbeiten", DoIcon: "ti-player-play", PrioExpr: "r.priority::text",
		OpenStatuses: []string{"open", "in_progress", "pending"}},
	"fault": {Key: "fault", Module: "fault", Table: "faults", Base: "/faults", Page: "faults", Title: "Störungen", Context: "Copilot-Analysen",
		Label: "Störung", Plural: "Störungen", Icon: "ti-alert-triangle", DoLabel: "Beheben", DoIcon: "ti-tool", PrioExpr: "r.severity::text",
		OpenStatuses: []string{"detected", "analyzing", "in_progress", "pending"}},
	"task": {Key: "task", Module: "task", Table: "tasks", Base: "/tasks", Page: "tasks", Title: "Aufgaben", Context: "Aufgaben",
		Label: "Aufgabe", Plural: "Aufgaben", Icon: "ti-list-check", DoLabel: "Erledigen", DoIcon: "ti-circle-check", PrioExpr: "r.priority::text",
		OpenStatuses: []string{"open", "in_progress", "pending"}},
}

type workCard struct {
	ID, Title, Description, InfraName, ProjectID, ProjectName string
	Due, Status, StatusLabel, StatusClass                     string
	Priority, PriorityLabel, PriorityClass, PriorityDot, Who  string
	Overdue, Unassigned, New                                  bool
	CreatedAgo                                                string
}

type workGroup struct {
	Key, Label, Icon string
	Cards            []workCard
}

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

type WorkBoardData struct {
	BaseData
	Kind                         workKind
	View                         string
	Mine, Unassigned             bool
	Infra, Prio, Project         string
	ProjectName                  string
	InfraOptions, ProjectOptions []UserOption
	Groups                       []workGroup
	OpenCount, OverdueCount      int
	UnassignedCount              int
	Archive                      workArchive
}

// workView: ohne Angabe die Kartenansicht, mit Status-Filter (alte Links) die Liste.
func workView(r *http.Request) string {
	q := r.URL.Query()
	switch v := q.Get("view"); v {
	case "due", "list", "archive":
		return v
	}
	if q.Get("status") != "" || q.Get("unassigned") != "" {
		return "list"
	}
	return "due"
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

// WorkBoardPage rendert „Anstehend“ bzw. „Archiv“ einer Vorgangsart.
func (h *Handler) WorkBoardPage(w http.ResponseWriter, r *http.Request, kindKey, view string) {
	k := workKinds[kindKey]
	q := r.URL.Query()
	d := WorkBoardData{
		BaseData: h.baseData(r, k.Page, k.Title, k.Context),
		Kind:     k, View: view,
		Mine: q.Get("mine") == "1", Unassigned: q.Get("free") == "1",
		Infra: q.Get("infra"), Prio: q.Get("prio"), Project: q.Get("project"),
	}
	if view == "archive" {
		h.workArchiveRows(r, &d)
	} else {
		h.workDueGroups(r, &d)
	}
	h.render(w, "work_board", d)
}

func (h *Handler) workBaseSQL(k workKind) string {
	who := `COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name, '')`
	unassigned := `r.assigned_to IS NULL AND r.assigned_group_id IS NULL`
	mine := `(r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs
	project := `'', ''`
	joins := ``
	if k.Key == "task" {
		who = `COALESCE((SELECT string_agg(TRIM(au.first_name || ' ' || au.last_name), ', ' ORDER BY au.last_name)
		          FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = r.id), '')`
		who = `COALESCE(NULLIF(` + who + `, ''), NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name, '')`
		unassigned += ` AND NOT EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id)`
		mine += ` OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $1::uuid)`
		project = `COALESCE(r.project_id::text, ''), COALESCE(p.name, '')`
		joins = ` LEFT JOIN projects p ON p.id = r.project_id`
	}
	mine += `)`
	return fmt.Sprintf(`
		SELECT r.id::text, r.title, COALESCE(r.description, ''), r.status::text, %s, r.due_date,
		       COALESCE(r.infrastructure_id::text, ''), COALESCE(i.name, ''), %s, %s, (%s), %s, r.created_at
		  FROM %s r
		  LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		  LEFT JOIN users u ON u.id = r.assigned_to
		  LEFT JOIN user_groups g ON g.id = r.assigned_group_id%s`,
		k.PrioExpr, who, mine, unassigned, project, k.Table, joins)
}

func (h *Handler) workDueGroups(r *http.Request, d *WorkBoardData) {
	ctx := r.Context()
	k := d.Kind
	uid := getUser(r).ID
	statuses := "'" + strings.Join(k.OpenStatuses, "','") + "'"
	rows, err := h.db.Query(ctx, h.workBaseSQL(k)+`
		 WHERE r.archived_at IS NULL AND r.status::text IN (`+statuses+`)
		 ORDER BY r.due_date NULLS LAST, r.created_at`, nullID(uid))
	if err != nil {
		componentLog("arbeitsansicht").Error().Err(err).Str("art", k.Key).Msg("vorgaenge laden")
		return
	}
	defer rows.Close()
	tagIDs := h.categoryFilterIDs(r, k.Module)
	scopeIDs := h.scopeAllowedIDs(r, k.Module)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	weekEnd := todayStart.AddDate(0, 0, 8)
	groups := []workGroup{
		{Key: "overdue", Label: "Überfällig", Icon: "ti-alert-triangle"},
		{Key: "today", Label: "Heute", Icon: "ti-calendar-event"},
		{Key: "week", Label: "Nächste 7 Tage", Icon: "ti-calendar-week"},
		{Key: "later", Label: "Später", Icon: "ti-calendar-time"},
		{Key: "nodue", Label: "Ohne Termin", Icon: "ti-calendar-off"},
	}
	idx := map[string]int{"overdue": 0, "today": 1, "week": 2, "later": 3, "nodue": 4}
	infra, projects := map[string]string{}, map[string]string{}
	for rows.Next() {
		var c workCard
		var due *time.Time
		var infraID string
		var mine bool
		var created time.Time
		if rows.Scan(&c.ID, &c.Title, &c.Description, &c.Status, &c.Priority, &due, &infraID, &c.InfraName, &c.Who, &mine, &c.Unassigned,
			&c.ProjectID, &c.ProjectName, &created) != nil {
			continue
		}
		if tagIDs != nil && !tagIDs[c.ID] || scopeIDs != nil && !scopeIDs[c.ID] {
			continue
		}
		if infraID != "" {
			infra[infraID] = c.InfraName
		}
		if c.ProjectID != "" {
			projects[c.ProjectID] = c.ProjectName
		}
		if c.Unassigned {
			d.UnassignedCount++
		}
		if d.Mine && !mine || d.Unassigned && !c.Unassigned || d.Infra != "" && infraID != d.Infra ||
			d.Prio != "" && c.Priority != d.Prio || d.Project != "" && c.ProjectID != d.Project {
			continue
		}
		key := "nodue"
		if due != nil {
			dl := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.Local)
			c.Due = dl.Format("02.01.2006")
			switch {
			case dl.Before(todayStart):
				key = "overdue"
			case dl.Equal(todayStart):
				key = "today"
			case dl.Before(weekEnd):
				key = "week"
			default:
				key = "later"
			}
		}
		c.Overdue = key == "overdue"
		c.New = c.Status == "detected" || (c.Status == "open" && c.Unassigned)
		c.StatusLabel, c.StatusClass = statusLabel(c.Status), statusClass(c.Status)
		c.PriorityLabel, c.PriorityDot = severityWord(c.Priority), priorityDot(c.Priority)
		if k.Key == "fault" {
			c.PriorityClass = severityClass(c.Priority)
		} else {
			c.PriorityClass = priorityClass(c.Priority)
		}
		c.CreatedAgo = timeAgo(created)
		g := &groups[idx[key]]
		g.Cards = append(g.Cards, c)
		d.OpenCount++
		if c.Overdue {
			d.OverdueCount++
		}
	}
	for _, g := range groups {
		if len(g.Cards) == 0 {
			continue
		}
		// innerhalb der Gruppe: Wichtigstes zuerst, dann nach Termin (bereits sortiert)
		cards := g.Cards
		for i := 1; i < len(cards); i++ {
			for j := i; j > 0 && prioRank(cards[j].Priority) < prioRank(cards[j-1].Priority); j-- {
				cards[j], cards[j-1] = cards[j-1], cards[j]
			}
		}
		d.Groups = append(d.Groups, g)
	}
	d.InfraOptions = sortedOptions(infra)
	d.ProjectOptions = sortedOptions(projects)
	d.ProjectName = projects[d.Project]
}

func sortedOptions(m map[string]string) []UserOption {
	out := make([]UserOption, 0, len(m))
	for id, name := range m {
		out = append(out, UserOption{ID: id, Name: name})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && strings.ToLower(out[j].Name) < strings.ToLower(out[j-1].Name); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

const workArchivePageSize = 50

func (h *Handler) workArchiveRows(r *http.Request, d *WorkBoardData) {
	ctx := r.Context()
	k := d.Kind
	q := r.URL.Query()
	a := &d.Archive
	a.Q, a.Infra, a.From, a.To = strings.TrimSpace(q.Get("q")), q.Get("infra"), q.Get("from"), q.Get("to")
	a.Filtered = a.Q != "" || a.Infra != "" || a.From != "" || a.To != "" || d.Project != ""
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
	if d.Project != "" && k.Key == "task" {
		where = append(where, "r.project_id::text = "+arg(d.Project))
	}
	closed := "COALESCE(r.resolved_at, r.archived_at, r.updated_at)"
	if _, err := time.Parse("2006-01-02", a.From); err == nil {
		where = append(where, closed+" >= "+arg(a.From)+"::date")
	}
	if _, err := time.Parse("2006-01-02", a.To); err == nil {
		where = append(where, closed+" < "+arg(a.To)+"::date + 1")
	}
	cond := " WHERE " + strings.Join(where, " AND ")
	project := `''`
	joins := ``
	if k.Key == "task" {
		project, joins = `COALESCE(p.name, '')`, ` LEFT JOIN projects p ON p.id = r.project_id`
	}
	from := ` FROM ` + k.Table + ` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id` + joins
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*)`+from+cond, args...).Scan(&a.Total)
	rows, err := h.db.Query(ctx, `
		SELECT r.id::text, r.title, COALESCE(i.name, ''), `+project+`, r.status::text, `+k.PrioExpr+`,
		       to_char(`+closed+`, 'DD.MM.YYYY'), COALESCE(r.resolution, ''), COALESCE(r.root_cause, ''),
		       COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), '')`+from+`
		  LEFT JOIN users u ON u.id = r.assigned_to`+cond+`
		 ORDER BY `+closed+` DESC LIMIT `+strconv.Itoa(workArchivePageSize)+` OFFSET `+strconv.Itoa((a.Page-1)*workArchivePageSize), args...)
	if err != nil {
		componentLog("arbeitsansicht").Error().Err(err).Str("art", k.Key).Msg("archiv laden")
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
		v.Set("view", "archive")
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
	d.InfraOptions = sortedOptions(opts)
}
