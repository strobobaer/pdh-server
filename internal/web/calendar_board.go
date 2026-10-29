package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdh/internal/core/users"
)

// Kalender-Dashboard (/calendar): Zeilen = Mitarbeiter, Spalten = Tage
// (Standard 14 ab heute). Zeigt Abwesenheiten, Schichten, fällige Wartungen,
// Prüfpflichten, Tickets, Störungen, Aufgaben und laufende Projekte.
// Überschneidungen – abwesend und trotzdem Schicht oder fälliger Auftrag –
// sind rot umrandet; ein fälliger Auftrag an einem Tag ohne Schicht gelb.
// Was angezeigt wird, Abteilung, Gruppe und Person wählt man oben; die
// Auswahl merkt sich PDH je Person (app_settings user:<id>:calendar).

type calKind struct{ Key, Label, Icon, Color string }

var calKinds = []calKind{
	{"absence", "Urlaub & Abwesenheit", "ti-beach", "#10B981"},
	{"shift", "Schichten", "ti-calendar-time", "#3B82F6"},
	{"maintenance", "Wartungen", "ti-tool", "#C99A3C"},
	{"obligation", "Prüfpflichten", "ti-clipboard-check", "#6366F1"},
	{"ticket", "Tickets", "ti-ticket", "#0EA5E9"},
	{"fault", "Störungen", "ti-alert-triangle", "#F97316"},
	{"task", "Aufgaben", "ti-list-check", "#8B5CF6"},
	{"project", "Projekte", "ti-timeline", "#64748B"},
	{"duty", "Sonderdienste", "ti-star", "#F59E0B"},
}

var calRanges = []int{7, 14, 21, 28, 42}

type calChip struct {
	Kind, Title, Short, URL, Color, Icon, Tip string
	Pending                                   bool // beantragt / Entwurf
	SpanStart, SpanMid                        bool // Projektbalken
	// Ziehen im Kalender (widgets/grid_drag.gohtml): Art, Datensatz, Zeile
	Drag, ID, Row string // Drag: shift | duty | absence | item ("" = fest)
}

type calCell struct {
	Date     string
	Weekend  bool
	Holiday  string
	Today    bool
	Absence  *calChip
	Shift    *calChip
	Items    []calChip
	Conflict string // rot: Überschneidung
	Warn     string // gelb: Auftrag ohne Schicht
}

// calWeek: Kopf je Kalenderwoche.
type calWeek struct {
	Label, Range string
	Current      bool
}

type calRow struct {
	UserID, Name, Dept string
	URL                string // Projektzeile: Link zum Projekt
	Cells              []calCell
	Conflicts          int
	busy               bool
}

type CalendarData struct {
	BaseData
	Days              []plannerDay
	Rows              []calRow
	Kinds             []calKind
	Show              map[string]bool
	Ranges            []int
	Range             int
	From, Prev, Next  string
	Today             string
	Label             string
	Depts, Groups     []partnerOption
	Users             []UserOption
	Dept, Group, User string
	OnlyBusy          bool
	Conflicts, Warns  int
	Counts            map[string]int
	LoadErrors        []string // Arten, die nicht geladen werden konnten (statt still zu fehlen)
	Embed             bool     // als Baustein im Leitstand (/global/calendar)
	ThisWeek          string   // Montag der laufenden Woche
	Weeks             []calWeek
	ProjectRows       []calRow // Projekte ganz oben, ohne Personen-Zuordnung
	Board             string   // Leitstand-Adresse im Baustein
}

func calChipFor(k calKind) calChip { return calChip{Kind: k.Key, Color: k.Color, Icon: k.Icon} }

func calKindByKey(key string) calKind {
	for _, k := range calKinds {
		if k.Key == key {
			return k
		}
	}
	return calKind{}
}

// CalendarPage: GET /calendar?from=&days=&show=…&dept=&group=&user=&busy=
func (h *Handler) CalendarPage(w http.ResponseWriter, r *http.Request) {
	d := h.buildCalendar(r, nil)
	d.BaseData = h.baseData(r, "calendar", "Kalender", "Übersicht")
	h.render(w, "calendar", d)
}

// CalendarBoardFragment: GET /global/calendar?b=<leitstand>&… – derselbe
// Kalender als Baustein „Kalender“ im Leitstand (auch ohne Anmeldung lesbar;
// Ziehen nur mit Anmeldung und Recht). Voreinstellung: Abteilung/Gruppe und
// Vorgangsarten des Leitstands.
func (h *Handler) CalendarBoardFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	board := h.resolveLeitstand(r)
	if b := h.boardBySlug(r, r.URL.Query().Get("b")); b != nil {
		board = b
	}
	if u := h.sessionUser(r); u != nil {
		r = r.WithContext(context.WithValue(r.Context(), "user", u))
	}
	d := h.buildCalendar(r, board)
	h.renderFragment(w, "calendar-board", d)
}

// buildCalendar: Daten des Kalenders – für die Seite (board = nil) oder als
// Leitstand-Baustein.
func (h *Handler) buildCalendar(r *http.Request, board *leitstand) CalendarData {
	ctx := r.Context()
	me := getUser(r)
	if me == nil {
		me = &users.User{}
	}
	q := r.URL.Query()
	embed := board != nil
	// Auswahl: aus der Adresse, sonst die zuletzt gemerkte (nur auf der Seite)
	if embed {
		if q.Get("set") == "" {
			if board.DepartmentID != "" {
				q.Set("dept", board.DepartmentID)
			}
			if board.GroupID != "" {
				q.Set("group", board.GroupID)
			}
			q["show"] = append([]string{"shift", "duty", "absence"}, board.Types...)
			if q.Get("days") == "" {
				q.Set("days", "7")
			}
		}
	} else if q.Get("set") == "" && q.Get("reset") == "" {
		if saved := h.userPref(ctx, me.ID, "calendar"); saved != "" {
			if sv, err := url.ParseQuery(saved); err == nil {
				for k, v := range sv {
					if _, ok := q[k]; !ok {
						q[k] = v
					}
				}
			}
		}
	} else if q.Get("reset") != "" {
		q = url.Values{}
		h.setUserPref(ctx, me.ID, "calendar", "")
	}
	d := CalendarData{
		Kinds: calKinds, Ranges: calRanges, Show: map[string]bool{}, Counts: map[string]int{},
		Dept: q.Get("dept"), Group: q.Get("group"), User: q.Get("user"), Embed: embed,
	}
	if embed {
		d.Board = board.Slug
	}
	// immer ganze Wochen ab Montag (1–6 Wochen)
	d.Range = atoiDefault(q.Get("days"), 14)
	if d.Range < 7 || d.Range > 42 {
		d.Range = 14
	}
	d.Range = (d.Range + 6) / 7 * 7
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from := mondayOf(today)
	if t, err := time.ParseInLocation(dayISO, q.Get("from"), time.Local); err == nil {
		from = mondayOf(t)
	}
	d.ThisWeek = mondayOf(today).Format(dayISO)
	to := from.AddDate(0, 0, d.Range-1)
	d.From, d.Today = from.Format(dayISO), today.Format(dayISO)
	d.Prev, d.Next = from.AddDate(0, 0, -d.Range).Format(dayISO), from.AddDate(0, 0, d.Range).Format(dayISO)
	d.Label = from.Format("02.01.") + " – " + to.Format("02.01.2006")
	for w := from; w.Before(to); w = w.AddDate(0, 0, 7) {
		_, kw := w.ISOWeek()
		d.Weeks = append(d.Weeks, calWeek{Label: fmt.Sprintf("KW %d", kw), Range: w.Format("02.01.") + " – " + w.AddDate(0, 0, 6).Format("02.01."), Current: w.Format(dayISO) == d.ThisWeek})
	}
	if shows := q["show"]; len(shows) > 0 {
		for _, s := range shows {
			d.Show[s] = true
		}
	} else {
		for _, k := range calKinds {
			d.Show[k.Key] = true
		}
	}
	d.OnlyBusy = q.Get("busy") != "0"
	if q.Get("set") != "" && !embed { // Auswahl merken (ohne Start – der ist immer „ab heute“, wenn nichts gewählt)
		keep := url.Values{}
		for _, k := range []string{"days", "dept", "group", "user", "busy"} {
			if v := q.Get(k); v != "" {
				keep.Set(k, v)
			}
		}
		for s := range d.Show {
			keep.Add("show", s)
		}
		h.setUserPref(ctx, me.ID, "calendar", keep.Encode())
	}
	// Rechte – am Leitstand sind Schichten, Dienste und Abwesenheit (ohne Grund) für alle sichtbar
	if !h.canPlannerView(r) && !embed {
		delete(d.Show, "shift")
		delete(d.Show, "absence")
		delete(d.Show, "duty")
	}
	if !h.hasPerm(r, "infrastructure.view") && !h.hasPerm(r, "infrastructure.obligations") {
		delete(d.Show, "obligation")
	}
	viewAll := h.canAbsenceViewAll(r)

	free := h.freeDays(ctx, from, to)
	for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
		ds := t.Format(dayISO)
		d.Days = append(d.Days, plannerDay{Date: ds, Label: t.Format("02.01."), Short: weekdayShort[t.Weekday()], Holiday: free[ds],
			Weekend: t.Weekday() == time.Saturday || t.Weekday() == time.Sunday, Today: ds == d.Today})
	}
	d.Depts, d.Groups, d.Users = h.plannerDeptOptions(ctx), h.plannerGroupOptions(ctx), h.userOptions(ctx)

	// Personen (Filter Abteilung/Gruppe/Person)
	people := h.plannerUsers(ctx, d.Dept, d.Group)
	rows := map[string]*calRow{}
	var order []*calRow
	newRow := func(id, name, dept string) *calRow {
		row := &calRow{UserID: id, Name: name, Dept: dept, Cells: make([]calCell, len(d.Days))}
		for i, day := range d.Days {
			row.Cells[i] = calCell{Date: day.Date, Weekend: day.Weekend, Holiday: day.Holiday, Today: day.Today}
		}
		rows[id] = row
		order = append(order, row)
		return row
	}
	for _, p := range people {
		if d.User != "" && p.ID != d.User {
			continue
		}
		newRow(p.ID, p.Name, p.Dept)
	}
	nobody := &calRow{UserID: "", Name: "Ohne Person", Cells: make([]calCell, len(d.Days))}
	for i, day := range d.Days {
		nobody.Cells[i] = calCell{Date: day.Date, Weekend: day.Weekend, Holiday: day.Holiday, Today: day.Today}
	}
	col := func(t time.Time) int {
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
		return int(t.Sub(from).Hours()/24 + 0.5)
	}
	cellOf := func(uid string, t time.Time) *calCell {
		c := col(t)
		if c < 0 || c >= len(d.Days) {
			return nil
		}
		if uid == "" {
			return &nobody.Cells[c]
		}
		row := rows[uid]
		if row == nil {
			return nil
		}
		return &row.Cells[c]
	}
	scope := h.requestScope(r)
	planner := h.canShiftPlan(r)

	if d.Show["absence"] {
		canApprove := h.canAbsenceApprove(r)
		if rs, err := h.db.Query(ctx, `SELECT id::text, user_id::text, type::text, status::text, start_date, end_date FROM absences
			WHERE status IN ('approved', 'pending') AND start_date <= $2 AND end_date >= $1`, from, to); err == nil {
			for rs.Next() {
				var aid, uid, typ, st string
				var a, b time.Time
				if rs.Scan(&aid, &uid, &typ, &st, &a, &b) != nil {
					continue
				}
				label := "abwesend"
				if viewAll || uid == me.ID {
					label = absenceTypeLabels[typ]
				}
				ch := calChipFor(calKindByKey("absence"))
				ch.Title, ch.Short, ch.Pending, ch.URL = label, label, st == "pending", "/planner?tab=absences"
				if st == "pending" {
					ch.Tip = label + " (beantragt)"
				} else {
					ch.Tip = label
				}
				if typ == "sick" && (viewAll || uid == me.ID) {
					ch.Color = "#EF4444"
				}
				if canApprove || (uid == me.ID && st == "pending") {
					ch.Drag, ch.ID = "absence", aid
				}
				for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
					if c := cellOf(uid, t); c != nil {
						cc := ch
						c.Absence = &cc
						d.Counts["absence"]++
					}
				}
			}
			rs.Close()
		}
	}
	// Schichten immer laden (für Konflikte/„keine Schicht“), anzeigen nur wenn gewählt
	hasShifts := map[string]bool{}
	if h.canPlannerView(r) || embed {
		if rs, err := h.db.Query(ctx, `SELECT sa.user_id::text, sa.shift_id::text, sa.date, sd.short_name, sd.name, sd.color, to_char(sd.start_time, 'HH24:MI') || '–' || to_char(sd.end_time, 'HH24:MI'),
				(sa.plan_id IS NOT NULL AND sp.status = 'draft')
			FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
			WHERE sa.date BETWEEN $1 AND $2 AND ($3 OR sa.plan_id IS NULL OR sp.status = 'published')`, from, to, planner); err == nil {
			for rs.Next() {
				var uid, sid, short, name, color, tm string
				var day time.Time
				var draft bool
				if rs.Scan(&uid, &sid, &day, &short, &name, &color, &tm, &draft) != nil {
					continue
				}
				hasShifts[uid] = true
				if c := cellOf(uid, day); c != nil {
					c.Shift = &calChip{Kind: "shift", Short: short, Title: name, Color: color, Tip: name + " " + tm, Pending: draft, URL: "/planner?tab=shifts&week=" + mondayOf(day).Format(dayISO)}
					if planner {
						c.Shift.Drag, c.Shift.Row = "shift", sid
					}
					if d.Show["shift"] {
						d.Counts["shift"]++
					}
				}
			}
			rs.Close()
		}
	}
	addItem := func(kind, uid, title, link, tip string, day time.Time) {
		c := cellOf(uid, day)
		if c == nil {
			return
		}
		ch := calChipFor(calKindByKey(kind))
		ch.Title, ch.URL, ch.Tip = title, link, tip
		if id, ok := calMovable[kind]; ok && me.ID != "" && h.hasPerm(r, id.perm) && strings.HasPrefix(link, id.url) {
			ch.Drag, ch.ID = "item", strings.TrimPrefix(link, id.url)
		}
		c.Items = append(c.Items, ch)
		d.Counts[kind]++
	}
	// Sonderdienste (planner_duties.go)
	if d.Show["duty"] && (h.canPlannerView(r) || embed) {
		canDuty := h.hasPerm(r, permDuties)
		if rs, err := h.db.Query(ctx, `SELECT a.user_id::text, a.duty_type_id::text, a.date, t.name, t.short_name, t.color, t.qualification,
				CASE WHEN t.show_phone THEN `+userPhoneSQL+` ELSE '' END
			FROM duty_assignments a JOIN duty_types t ON t.id = a.duty_type_id JOIN users u ON u.id = a.user_id WHERE a.date BETWEEN $1 AND $2`, from, to); err == nil {
			for rs.Next() {
				var uid, tid, name, short, color, qual, phone string
				var day time.Time
				if rs.Scan(&uid, &tid, &day, &name, &short, &color, &qual, &phone) != nil {
					continue
				}
				c := cellOf(uid, day)
				if c == nil {
					continue
				}
				ch := calChipFor(calKindByKey("duty"))
				if hexColorRe.MatchString(color) {
					ch.Color = color
				}
				ch.Title, ch.URL, ch.Tip = firstNonEmpty(short, name), "/planner?tab=shifts&week="+mondayOf(day).Format(dayISO), name
				if qual != "" {
					ch.Tip += " · " + qual
				}
				if phone != "" {
					ch.Title += " · " + phone
					ch.Tip += " · Tel. " + phone
				}
				if canDuty {
					ch.Drag, ch.Row = "duty", tid
				}
				c.Items = append(c.Items, ch)
				d.Counts["duty"]++
			}
			rs.Close()
		}
	}
	// fällige Vorgänge: Person = zugewiesen (Aufgaben: alle Beteiligten), sonst „Ohne Person“
	type src struct{ kind, table, open, url, people string }
	srcs := []src{
		{"maintenance", "maintenance_tasks", "t.status::text IN ('open', 'in_progress', 'pending')", "/maintenance/tasks/", calPeople},
		{"ticket", "tickets", "t.status::text IN ('open', 'in_progress', 'pending')", "/tickets/", calPeople},
		{"fault", "faults", "t.status::text IN ('detected', 'analyzing', 'in_progress', 'pending')", "/faults/", calPeople},
		{"task", "tasks", "t.status IN ('open', 'in_progress', 'pending')", "/tasks/",
			// Aufgaben haben mehrere Zuständige (task_assignees, Migration 062 – tasks.assigned_to gibt es
			// nicht mehr); ohne Zuständige gilt die verantwortliche Person
			"COALESCE(NULLIF(ARRAY(SELECT ta.user_id::text FROM task_assignees ta WHERE ta.task_id = t.id), '{}'), ARRAY_REMOVE(ARRAY[t.responsible_to::text], NULL))"},
	}
	refType := map[string]string{"maintenance": "maintenance_task", "ticket": "ticket", "fault": "fault", "task": "task"}
	for _, s := range srcs {
		if !d.Show[s.kind] {
			continue
		}
		// Stichtag: Fälligkeit, ohne Fälligkeit der Tag der Anlage. Überfälliges
		// (Stichtag vor heute, noch offen) steht in der Spalte „heute“ –
		// sofern heute im Zeitraum liegt. So fehlt kein offener Vorgang.
		args := []any{from, to, today}
		sc, sa := scopeSQL(scope, refType[s.kind], "t", len(args))
		args = append(args, sa...)
		rs, err := h.db.Query(ctx, `SELECT t.id::text, t.title, COALESCE(t.due_date, t.created_at), t.due_date IS NULL, `+s.people+`, COALESCE(g.name, ''), COALESCE(i.name, '')
			FROM `+s.table+` t LEFT JOIN user_groups g ON g.id = t.assigned_group_id LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
			WHERE `+s.open+` AND t.archived_at IS NULL
			  AND (COALESCE(t.due_date, t.created_at)::date BETWEEN $1 AND $2
			       OR (COALESCE(t.due_date, t.created_at)::date < $3::date AND $3::date BETWEEN $1 AND $2))`+sc+`
			ORDER BY 3 LIMIT 2000`, args...)
		if err != nil {
			componentLog("kalender").Warn().Err(err).Str("art", s.kind).Msg("laden")
			d.LoadErrors = append(d.LoadErrors, calKindByKey(s.kind).Label+": "+friendlyDBError(err))
			continue
		}
		for rs.Next() {
			var id, title, group, infra string
			var due time.Time
			var noDue bool
			var ppl []string
			if rs.Scan(&id, &title, &due, &noDue, &ppl, &group, &infra) != nil {
				continue
			}
			due = due.Local()
			tip := calKindByKey(s.kind).Label + ": " + title
			switch {
			case due.Before(today):
				if noDue {
					tip += " · ohne Fälligkeit, angelegt am " + due.Format("02.01.2006")
				} else {
					tip += " · überfällig seit " + due.Format("02.01.2006")
					title = "⚠ " + title
				}
				due = today
			case noDue:
				tip += " · ohne Fälligkeit (Tag der Anlage)"
			}
			if infra != "" {
				tip += " · " + infra
			}
			if group != "" {
				tip += " · Gruppe " + group
			}
			if len(ppl) == 0 {
				addItem(s.kind, "", title, s.url+id, tip, due)
				continue
			}
			for _, uid := range ppl {
				addItem(s.kind, uid, title, s.url+id, tip, due)
			}
		}
		rs.Close()
	}
	if d.Show["obligation"] {
		if rs, err := h.db.Query(ctx, `SELECT o.infrastructure_id::text, o.title, COALESCE(i.name, ''), COALESCE(o.responsible_to::text, ''), o.next_due
			FROM infra_obligations o JOIN infrastructure i ON i.id = o.infrastructure_id WHERE o.next_due BETWEEN $1 AND $2`, from, to); err == nil {
			for rs.Next() {
				var infraID, title, infra, uid string
				var due time.Time
				if rs.Scan(&infraID, &title, &infra, &uid, &due) == nil {
					addItem("obligation", uid, title, "/infrastructure/"+infraID+"?tab=checks", "Prüfpflicht: "+title+" · "+infra, due)
				}
			}
			rs.Close()
		}
	}
	if d.Show["project"] {
		args := []any{from, to}
		sc, sa := scopeSQL(scope, "project", "p", len(args))
		args = append(args, sa...)
		if rs, err := h.db.Query(ctx, `SELECT p.id::text, p.name, COALESCE(p.start_date, p.created_at::date), COALESCE(p.end_date, p.start_date, p.created_at::date),
				ARRAY_REMOVE(ARRAY[p.responsible_to::text, p.assigned_to::text], NULL)
			FROM projects p WHERE p.status NOT IN ('completed', 'cancelled')
			  AND COALESCE(p.start_date, p.created_at::date) <= $2 AND COALESCE(p.end_date, p.start_date, p.created_at::date) >= $1`+sc+`
			ORDER BY 3 LIMIT 500`, args...); err == nil {
			for rs.Next() {
				var id, name string
				var a, b time.Time
				var ppl []string
				if rs.Scan(&id, &name, &a, &b, &ppl) != nil {
					continue
				}
				_ = ppl
				// je Projekt eine eigene Zeile ganz oben – ohne Personen-Zuordnung
				row := calRow{Name: name, URL: "/projects/" + id, Cells: make([]calCell, len(d.Days))}
				first := true
				for i, day := range d.Days {
					row.Cells[i] = calCell{Date: day.Date, Weekend: day.Weekend, Holiday: day.Holiday, Today: day.Today}
					t := mustDate(day.Date)
					if t.Before(a) || t.After(b) {
						continue
					}
					ch := calChipFor(calKindByKey("project"))
					ch.Title, ch.URL = name, "/projects/"+id
					ch.Tip = "Projekt: " + name + " · " + a.Format("02.01.") + " – " + b.Format("02.01.2006")
					ch.SpanStart, ch.SpanMid = first, !first
					first = false
					row.Cells[i].Items = append(row.Cells[i].Items, ch)
				}
				d.ProjectRows = append(d.ProjectRows, row)
				d.Counts["project"]++
			}
			rs.Close()
		}
	}

	// Überschneidungen und Hinweise
	check := func(row *calRow) {
		for i := range row.Cells {
			c := &row.Cells[i]
			due := 0
			for _, it := range c.Items {
				if it.Kind != "project" && it.Kind != "duty" {
					due++
				}
			}
			if c.Absence != nil || c.Shift != nil || len(c.Items) > 0 {
				row.busy = true
			}
			if c.Absence != nil {
				var why []string
				if c.Shift != nil {
					why = append(why, "Schicht "+c.Shift.Short)
				}
				if due > 0 {
					why = append(why, strconv.Itoa(due)+" fällige(r) Auftrag/Aufträge")
				}
				if len(why) > 0 {
					c.Conflict = c.Absence.Tip + " – aber " + strings.Join(why, " und ")
					row.Conflicts++
					d.Conflicts++
				}
			} else if due > 0 && hasShifts[row.UserID] && c.Shift == nil && row.UserID != "" {
				c.Warn = "Fällig an einem Tag ohne Schicht"
				d.Warns++
			}
			if !d.Show["shift"] {
				c.Shift = nil
			}
			if !d.Show["absence"] {
				c.Absence = nil
			}
			// Projekte zuerst, dann Sonderdienste, dann fällige Vorgänge
			rank := func(k string) int {
				switch k {
				case "project":
					return 0
				case "duty":
					return 1
				}
				return 2
			}
			sort.SliceStable(c.Items, func(a, b int) bool { return rank(c.Items[a].Kind) < rank(c.Items[b].Kind) })
		}
	}
	for _, row := range order {
		check(row)
	}
	check(nobody)
	for _, row := range order {
		if !d.OnlyBusy || row.busy || d.User != "" {
			d.Rows = append(d.Rows, *row)
		}
	}
	if nobody.busy && d.User == "" {
		d.Rows = append(d.Rows, *nobody)
	}
	return d
}

// calMovable: Vorgänge, deren Fälligkeit sich im Kalender per Ziehen verschieben lässt.
var calMovable = map[string]struct{ url, table, ref, perm string }{
	"ticket":      {"/tickets/", "tickets", "ticket", "tickets.edit"},
	"fault":       {"/faults/", "faults", "fault", "faults.edit"},
	"task":        {"/tasks/", "tasks", "task", "tasks.edit"},
	"maintenance": {"/maintenance/tasks/", "maintenance_tasks", "maintenance_task", "maintenance.edit"},
}

// CalendarMoveWeb: POST /calendar/move – JSON {kind, id, to_date}: Fälligkeit
// eines Vorgangs auf einen anderen Tag legen (Uhrzeit bleibt).
func (h *Handler) CalendarMoveWeb(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		ToDate string `json:"to_date"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return
	}
	m, ok := calMovable[in.Kind]
	to, err := time.ParseInLocation(dayISO, in.ToDate, time.Local)
	if !ok || err != nil || !uuidInPathRe.MatchString(in.ID) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Vorgang oder Datum ungültig"})
		return
	}
	u := getUser(r)
	if !h.hasPermRec(r, m.perm, m.ref, in.ID) && !h.recordInvolved(r.Context(), u.ID, m.ref, in.ID) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung, diesen Vorgang zu ändern."})
		return
	}
	ctx := r.Context()
	var old string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(to_char(due_date, 'DD.MM.YYYY'), '') FROM `+m.table+` WHERE id = $1::uuid`, in.ID).Scan(&old)
	q := `UPDATE ` + m.table + ` SET due_date = CASE WHEN due_date IS NULL THEN $2::date + time '12:00' ELSE due_date + ($2::date - due_date::date) END, updated_at = NOW() WHERE id = $1::uuid`
	if m.table == "tasks" {
		q = `UPDATE tasks SET due_date = $2::date, updated_at = NOW() WHERE id = $1::uuid`
	}
	if _, err := h.db.Exec(ctx, q, in.ID, to); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	h.addHistory(ctx, m.ref, in.ID, "update", "due_date", old, to.Format("02.01.2006"), "Fälligkeit im Kalender verschoben", u.ID)
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

// Chips für Vorlagen: Tooltip enthält Konflikt
func (c calCell) Busy() bool { return c.Absence != nil || c.Shift != nil || len(c.Items) > 0 }

// calPeople: zugewiesene Person, sonst die verantwortliche (Tickets, Störungen, Wartungen).
const calPeople = `COALESCE(NULLIF(ARRAY_REMOVE(ARRAY[t.assigned_to::text], NULL), '{}'), ARRAY_REMOVE(ARRAY[t.responsible_to::text], NULL))`
