package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
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
}

var calRanges = []int{7, 14, 21, 28, 42}

type calChip struct {
	Kind, Title, Short, URL, Color, Icon, Tip string
	Pending                                   bool // beantragt / Entwurf
	SpanStart, SpanMid                        bool // Projektbalken
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

type calRow struct {
	UserID, Name, Dept string
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
	ctx := r.Context()
	me := getUser(r)
	q := r.URL.Query()
	// Auswahl: aus der Adresse, sonst die zuletzt gemerkte
	if q.Get("set") == "" && q.Get("reset") == "" {
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
		BaseData: h.baseData(r, "calendar", "Kalender", "Übersicht"),
		Kinds:    calKinds, Ranges: calRanges, Show: map[string]bool{}, Counts: map[string]int{},
		Dept: q.Get("dept"), Group: q.Get("group"), User: q.Get("user"),
	}
	d.Range = atoiDefault(q.Get("days"), 14)
	if d.Range < 1 || d.Range > 62 {
		d.Range = 14
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from := today
	if t, err := time.ParseInLocation(dayISO, q.Get("from"), time.Local); err == nil {
		from = t
	}
	to := from.AddDate(0, 0, d.Range-1)
	d.From, d.Today = from.Format(dayISO), today.Format(dayISO)
	d.Prev, d.Next = from.AddDate(0, 0, -d.Range).Format(dayISO), from.AddDate(0, 0, d.Range).Format(dayISO)
	d.Label = from.Format("02.01.") + " – " + to.Format("02.01.2006")
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
	if q.Get("set") != "" { // Auswahl merken (ohne Start – der ist immer „ab heute“, wenn nichts gewählt)
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
	// Rechte
	if !h.canPlannerView(r) {
		delete(d.Show, "shift")
		delete(d.Show, "absence")
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
		if rs, err := h.db.Query(ctx, `SELECT user_id::text, type::text, status::text, start_date, end_date FROM absences
			WHERE status IN ('approved', 'pending') AND start_date <= $2 AND end_date >= $1`, from, to); err == nil {
			for rs.Next() {
				var uid, typ, st string
				var a, b time.Time
				if rs.Scan(&uid, &typ, &st, &a, &b) != nil {
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
	if h.canPlannerView(r) {
		if rs, err := h.db.Query(ctx, `SELECT sa.user_id::text, sa.date, sd.short_name, sd.name, sd.color, to_char(sd.start_time, 'HH24:MI') || '–' || to_char(sd.end_time, 'HH24:MI'),
				(sa.plan_id IS NOT NULL AND sp.status = 'draft')
			FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
			WHERE sa.date BETWEEN $1 AND $2 AND ($3 OR sa.plan_id IS NULL OR sp.status = 'published')`, from, to, planner); err == nil {
			for rs.Next() {
				var uid, short, name, color, tm string
				var day time.Time
				var draft bool
				if rs.Scan(&uid, &day, &short, &name, &color, &tm, &draft) != nil {
					continue
				}
				hasShifts[uid] = true
				if c := cellOf(uid, day); c != nil {
					c.Shift = &calChip{Kind: "shift", Short: short, Title: name, Color: color, Tip: name + " " + tm, Pending: draft, URL: "/planner?tab=shifts&week=" + mondayOf(day).Format(dayISO)}
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
		c.Items = append(c.Items, ch)
		d.Counts[kind]++
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
				if len(ppl) == 0 {
					ppl = []string{""}
				}
				seen := map[string]bool{}
				for _, uid := range ppl {
					if seen[uid] {
						continue
					}
					seen[uid] = true
					first := true
					for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
						c := cellOf(uid, t)
						if c == nil {
							continue
						}
						ch := calChipFor(calKindByKey("project"))
						ch.Title, ch.URL = name, "/projects/"+id
						ch.Tip = "Projekt: " + name + " · " + a.Format("02.01.") + " – " + b.Format("02.01.2006")
						ch.SpanStart, ch.SpanMid = first, !first
						first = false
						c.Items = append(c.Items, ch)
					}
					d.Counts["project"]++
				}
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
				if it.Kind != "project" {
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
			sort.SliceStable(c.Items, func(a, b int) bool { return c.Items[a].Kind == "project" && c.Items[b].Kind != "project" })
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
	h.render(w, "calendar", d)
}

// Chips für Vorlagen: Tooltip enthält Konflikt
func (c calCell) Busy() bool { return c.Absence != nil || c.Shift != nil || len(c.Items) > 0 }

// calPeople: zugewiesene Person, sonst die verantwortliche (Tickets, Störungen, Wartungen).
const calPeople = `COALESCE(NULLIF(ARRAY_REMOVE(ARRAY[t.assigned_to::text], NULL), '{}'), ARRAY_REMOVE(ARRAY[t.responsible_to::text], NULL))`
