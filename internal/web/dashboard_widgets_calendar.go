package web

import (
	"context"
	"sort"
	"time"
)

// Widgets „Mein Kalender“ und „Schichtplan Team“ (dashboard_widgets.go).

// calWidgetItem: ein Eintrag im Kalender-Widget.
type calWidgetItem struct {
	Icon, Color, Title, Tip, URL string
	Pending                      bool
	sort                         int
}

// calWidgetDay: ein Tag im Kalender-Widget.
type calWidgetDay struct {
	Day, Date      string
	Today, Weekend bool
	Items          []calWidgetItem
}

// loadMyCalendar: die eigenen Termine der nächsten 7 Tage – Schicht,
// Abwesenheit, fällige Vorgänge (Überfälliges steht bei heute) und
// Prüfpflichten, für die man verantwortlich ist.
func loadMyCalendar(h *Handler, ctx context.Context, uid string, _ WidgetInstance, can func(string) bool) (any, error) {
	lang := ctxLang(ctx)
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	days := make([]calWidgetDay, 7)
	idx := map[string]int{}
	for i := range days {
		d := today.AddDate(0, 0, i)
		days[i] = calWidgetDay{Day: shortWeekday(lang, d.Weekday()), Date: d.Format("02.01."), Today: i == 0,
			Weekend: d.Weekday() == time.Saturday || d.Weekday() == time.Sunday}
		idx[d.Format(dayISO)] = i
	}
	to := today.AddDate(0, 0, 6)
	add := func(day time.Time, it calWidgetItem) {
		if day.Before(today) {
			day = today
		}
		if i, ok := idx[day.Format(dayISO)]; ok {
			days[i].Items = append(days[i].Items, it)
		}
	}
	// Schichten (nur veröffentlichte bzw. ohne Plan)
	if rs, err := h.db.Query(ctx, `SELECT sa.date, sd.name, COALESCE(sd.color, ''), to_char(sd.start_time, 'HH24:MI') || '–' || to_char(sd.end_time, 'HH24:MI')
		FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.user_id = $1::uuid AND sa.date BETWEEN $2 AND $3 AND (sa.plan_id IS NULL OR sp.status = 'published')`, uid, today, to); err == nil {
		for rs.Next() {
			var d time.Time
			var name, color, tm string
			if rs.Scan(&d, &name, &color, &tm) == nil {
				if !hexColorRe.MatchString(color) {
					color = calKindByKey("shift").Color
				}
				add(d, calWidgetItem{Icon: "ti-calendar-time", Color: color, Title: name + " " + tm, URL: "/planner?tab=shifts&week=" + mondayOf(d).Format(dayISO)})
			}
		}
		rs.Close()
	}
	// Abwesenheiten
	if rs, err := h.db.Query(ctx, `SELECT type::text, status::text, GREATEST(start_date, $2::date), LEAST(end_date, $3::date) FROM absences
		WHERE user_id = $1::uuid AND status IN ('approved', 'pending') AND start_date <= $3 AND end_date >= $2`, uid, today, to); err == nil {
		for rs.Next() {
			var typ, st string
			var a, b time.Time
			if rs.Scan(&typ, &st, &a, &b) != nil {
				continue
			}
			k := calKindByKey("absence")
			it := calWidgetItem{Icon: k.Icon, Color: k.Color, Title: tr(lang, absenceTypeLabels[typ]), URL: "/planner?tab=absences", Pending: st == "pending", sort: -1}
			if typ == "sick" {
				it.Color = "#EF4444"
			}
			if it.Pending {
				it.Tip = tr(lang, "beantragt")
			}
			for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
				add(d, it)
			}
		}
		rs.Close()
	}
	// fällige Vorgänge, die mir zugewiesen sind (Aufgaben: alle Beteiligten)
	type src struct{ kind, perm, table, open, url, mine string }
	srcs := []src{
		{"fault", "faults.view", "faults", "t.status::text IN ('detected', 'analyzing', 'in_progress', 'pending')", "/faults/", "t.assigned_to = $1::uuid"},
		{"ticket", "tickets.view", "tickets", "t.status::text IN ('open', 'in_progress', 'pending')", "/tickets/", "t.assigned_to = $1::uuid"},
		{"maintenance", "maintenance.view", "maintenance_tasks", "t.status::text IN ('open', 'in_progress', 'pending')", "/maintenance/tasks/", "t.assigned_to = $1::uuid"},
		{"task", "", "tasks", "t.status IN ('open', 'in_progress', 'pending')", "/tasks/",
			"(EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = t.id AND ta.user_id = $1::uuid) OR t.responsible_to = $1::uuid)"},
	}
	for _, s := range srcs {
		if s.perm != "" && !can(s.perm) {
			continue
		}
		rs, err := h.db.Query(ctx, `SELECT t.id::text, t.title, t.due_date FROM `+s.table+` t
			WHERE `+s.open+` AND t.archived_at IS NULL AND t.due_date IS NOT NULL AND t.due_date::date <= $2 AND `+s.mine+`
			ORDER BY t.due_date LIMIT 50`, uid, to)
		if err != nil {
			componentLog("dashboard").Warn().Err(err).Str("art", s.kind).Msg("kalender-widget")
			continue
		}
		k := calKindByKey(s.kind)
		for rs.Next() {
			var id, title string
			var due time.Time
			if rs.Scan(&id, &title, &due) != nil {
				continue
			}
			due = due.Local()
			it := calWidgetItem{Icon: k.Icon, Color: k.Color, Title: title, Tip: tr(lang, k.Label), URL: s.url + id, sort: 2}
			if due.Before(today) {
				it.Title, it.Tip, it.sort = "⚠ "+title, tr(lang, "überfällig seit")+" "+due.Format("02.01."), 1
			}
			add(due, it)
		}
		rs.Close()
	}
	// Prüfpflichten, für die ich verantwortlich bin
	if can("infrastructure.view") {
		if rs, err := h.db.Query(ctx, `SELECT o.infrastructure_id::text, o.title, COALESCE(i.name, ''), o.next_due
			FROM infra_obligations o JOIN infrastructure i ON i.id = o.infrastructure_id
			WHERE o.responsible_to = $1::uuid AND o.next_due BETWEEN $2 AND $3`, uid, today, to); err == nil {
			k := calKindByKey("obligation")
			for rs.Next() {
				var infraID, title, infra string
				var due time.Time
				if rs.Scan(&infraID, &title, &infra, &due) == nil {
					add(due, calWidgetItem{Icon: k.Icon, Color: k.Color, Title: title, Tip: infra, URL: "/infrastructure/" + infraID + "?tab=checks", sort: 3})
				}
			}
			rs.Close()
		}
	}
	for i := range days {
		sort.SliceStable(days[i].Items, func(a, b int) bool { return days[i].Items[a].sort < days[i].Items[b].sort })
	}
	return days, nil
}

// teamShiftCell / teamShiftRow: eine Zeile im Widget „Schichtplan Team“.
type teamShiftCell struct {
	Short, Name, Color, Absence string
	Today, Weekend, Draft       bool
}

type teamShiftRow struct {
	Name  string
	Me    bool
	Cells []teamShiftCell
}

type teamShiftView struct {
	Dept string
	Days []shiftDayView
	Rows []teamShiftRow
}

// loadTeamShifts: Schichten der eigenen Abteilung für die nächsten 7 Tage.
// Entwürfe sieht nur, wer Schichten planen darf; den Grund einer Abwesenheit
// nur, wer alle Abwesenheiten sehen darf.
func loadTeamShifts(h *Handler, ctx context.Context, uid string, _ WidgetInstance, can func(string) bool) (any, error) {
	lang := ctxLang(ctx)
	v := teamShiftView{}
	var dept string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(u.department_id::text, ''), COALESCE(d.name, '') FROM users u LEFT JOIN departments d ON d.id = u.department_id WHERE u.id = $1::uuid`, uid).Scan(&dept, &v.Dept)
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	for i := 0; i < 7; i++ {
		d := today.AddDate(0, 0, i)
		v.Days = append(v.Days, shiftDayView{Day: shortWeekday(lang, d.Weekday()), Date: d.Format("02.01."), Today: i == 0})
	}
	rows, err := h.db.Query(ctx, `SELECT u.id::text, TRIM(u.first_name || ' ' || u.last_name) FROM users u
		WHERE u.active AND NOT u.is_system_user AND (u.id = $1::uuid OR ($2 <> '' AND u.department_id = NULLIF($2, '')::uuid))
		ORDER BY (u.id = $1::uuid) DESC, lower(u.last_name), lower(u.first_name) LIMIT 40`, uid, dept)
	if err != nil {
		return nil, err
	}
	pos := map[string]int{}
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) != nil {
			continue
		}
		r := teamShiftRow{Name: name, Me: id == uid, Cells: make([]teamShiftCell, 7)}
		for i := range r.Cells {
			wd := today.AddDate(0, 0, i).Weekday()
			r.Cells[i] = teamShiftCell{Today: i == 0, Weekend: wd == time.Saturday || wd == time.Sunday}
		}
		pos[id] = len(v.Rows)
		v.Rows = append(v.Rows, r)
	}
	rows.Close()
	if len(v.Rows) == 0 {
		return v, nil
	}
	ids := make([]string, 0, len(pos))
	for id := range pos {
		ids = append(ids, id)
	}
	to := today.AddDate(0, 0, 6)
	cell := func(id string, d time.Time) *teamShiftCell {
		p, ok := pos[id]
		i := int(d.Sub(today).Hours()/24 + 0.5)
		if !ok || i < 0 || i > 6 {
			return nil
		}
		return &v.Rows[p].Cells[i]
	}
	if rs, err := h.db.Query(ctx, `SELECT sa.user_id::text, sa.date, sd.short_name, sd.name, COALESCE(sd.color, ''), (sa.plan_id IS NOT NULL AND sp.status = 'draft')
		FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.user_id = ANY($1::uuid[]) AND sa.date BETWEEN $2 AND $3 AND ($4 OR sa.plan_id IS NULL OR sp.status = 'published')`,
		ids, today, to, can("shifts.edit")); err == nil {
		for rs.Next() {
			var id, short, name, color string
			var d time.Time
			var draft bool
			if rs.Scan(&id, &d, &short, &name, &color, &draft) != nil {
				continue
			}
			if c := cell(id, d); c != nil {
				if !hexColorRe.MatchString(color) {
					color = ""
				}
				c.Short, c.Name, c.Color, c.Draft = short, name, color, draft
			}
		}
		rs.Close()
	}
	viewAll := can("absences.view_all") || can("absences.approve")
	if rs, err := h.db.Query(ctx, `SELECT user_id::text, type::text, GREATEST(start_date, $2::date), LEAST(end_date, $3::date) FROM absences
		WHERE user_id = ANY($1::uuid[]) AND status = 'approved' AND start_date <= $3 AND end_date >= $2`, ids, today, to); err == nil {
		for rs.Next() {
			var id, typ string
			var a, b time.Time
			if rs.Scan(&id, &typ, &a, &b) != nil {
				continue
			}
			label := tr(lang, "abwesend")
			if viewAll || id == uid {
				label = tr(lang, absenceTypeLabels[typ])
			}
			for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
				if c := cell(id, d); c != nil {
					c.Absence = label
				}
			}
		}
		rs.Close()
	}
	return v, nil
}
