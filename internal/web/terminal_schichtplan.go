package web

// Schichtplan am Terminal (0.82.41): Kachel und Seite „Schichtplan“ zeigen heute und die Woche (diese oder
// nächste) je Abteilung des Terminals (terminal_departments.go) – Name und Schicht je Tag aus dem Planer
// (veröffentlichte Pläne), dazu die Sonderdienste. Nur lesend, ohne Anmeldung.
//
// Datenschutz: Abwesenheiten erscheinen ausschließlich als „abwesend“. Die Art (Urlaub, Krank, Fortbildung …)
// wird nicht einmal aus der Datenbank gelesen, Notizen der Einteilung ebenso wenig – der Bildschirm ist öffentlich.
//
// Recht: shifts.view des Terminal-Benutzers (nicht der Kartenperson) mit Abteilungsbezug; angeboten und
// ausgeliefert werden nur Abteilungen, die beides erfüllen (Abteilung des Terminals und Recht).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type terminalPlanDept struct {
	ID, Name string
	Sel      bool
}

type terminalPlanCell struct {
	Absent bool     // nur „abwesend“ – nie der Grund
	Shift  string   // Schichtname
	Time   string   // „06:00–14:00“
	Duties []string // Sonderdienste des Tages
}

func (c terminalPlanCell) Empty() bool { return !c.Absent && c.Shift == "" && len(c.Duties) == 0 }

type terminalPlanRow struct {
	Name  string
	Cells []terminalPlanCell // 7 Tage
}

type terminalPlanDay struct {
	Label, Short, Holiday string
	Today, Weekend        bool
}

type terminalPlanGroup struct {
	Title, Time string
	Names       []string
	Absent      bool
	Duty        bool
}

// terminalPlan: Daten der Seite /terminal/schichtplan.
type terminalPlan struct {
	Depts     []terminalPlanDept
	Dept      string // gewählte Abteilung
	DeptName  string
	Next      bool // nächste Woche
	WeekLabel string
	Days      []terminalPlanDay
	Rows      []terminalPlanRow
	Today     []terminalPlanGroup // nur diese Woche
	ShowToday bool
	Denied    bool // keine Abteilung mit Recht

	// Auswahl des Plans (terminal_shiftplan.go)
	Plans       []shiftPlanChoice // veröffentlichte Pläne, die die Woche schneiden
	PlanSel     string            // gewählter Plan, "" = alle
	PlanName    string
	PlanReq     string // Parameter plan der Anfrage ("" = nicht angegeben, „alle“ = ausdrücklich alle, sonst UUID)
	DefaultPlan string // Grundeinstellung des Terminals ("" = alle)
}

// terminalPlanDepts: Abteilungen des Terminals, für die der Terminal-Benutzer shifts.view hat.
func (h *Handler) terminalPlanDepts(ctx context.Context, uid string) ([]terminalPlanDept, error) {
	if h.rbac == nil || h.db == nil || uid == "" {
		return nil, nil
	}
	ids, err := h.terminalDepartments(ctx, uid)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	_, role, err := h.terminalInfraLookup(ctx, uid)
	if err != nil {
		return nil, err
	}
	var ok []string
	for _, id := range ids {
		if h.rbac.HasPermissionInDepartment(uid, role, "shifts.view", []string{id}) {
			ok = append(ok, id)
		}
	}
	if len(ok) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, name FROM departments WHERE id = ANY($1::uuid[]) ORDER BY lower(name), id`, ok)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []terminalPlanDept
	for rows.Next() {
		var d terminalPlanDept
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TerminalSchichtplanPage: GET /terminal/schichtplan[?abt=<Abteilung>][&woche=naechste][&plan=<Plan>|alle]
func (h *Handler) TerminalSchichtplanPage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalSetupGate(w, r, "schichtplan")
	if !ok {
		return
	}
	depts, err := h.terminalPlanDepts(r.Context(), uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("schichtplan-abteilungen")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	pl := terminalPlan{Next: r.URL.Query().Get("woche") == "naechste"}
	pl.PlanReq = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("plan")))
	pl.DefaultPlan = h.terminalShiftPlan(r.Context(), uid)
	if len(depts) == 0 {
		pl.Denied = true
		p.Plan = pl
		h.renderTerminal(w, p)
		return
	}
	sel := r.URL.Query().Get("abt")
	if sel == "" {
		sel = depts[0].ID
	}
	found := false
	for i := range depts {
		if depts[i].ID == sel {
			depts[i].Sel, found = true, true
			pl.DeptName = depts[i].Name
		}
	}
	if !found { // fremde Abteilung: nichts ausliefern
		http.Error(w, "Diese Abteilung gehört nicht zu diesem Terminal.", http.StatusForbidden)
		return
	}
	pl.Depts, pl.Dept = depts, sel
	if err := h.terminalPlanFill(r.Context(), &pl); err != nil {
		componentLog("terminal").Error().Err(err).Msg("schichtplan laden")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	p.Plan = pl
	h.renderTerminal(w, p)
}

type terminalPlanPerson struct {
	name  string
	cells []terminalPlanCell
}

func (h *Handler) terminalPlanFill(ctx context.Context, pl *terminalPlan) error {
	now := time.Now()
	start := mondayOf(now)
	if pl.Next {
		start = start.AddDate(0, 0, 7)
	}
	end := start.AddDate(0, 0, 6)
	_, wk := start.ISOWeek()
	pl.WeekLabel = fmt.Sprintf("KW %d · %s – %s", wk, start.Format("02.01."), end.Format("02.01.2006"))
	pl.ShowToday = !pl.Next
	// Plan: Anfrage vor Grundeinstellung; gilt der Plan in dieser Woche nicht (oder ist er kein veröffentlichter), alle.
	plans, err := h.publishedShiftPlans(ctx, start, end, []string{pl.Dept}, false)
	if err != nil {
		return err
	}
	want := pl.PlanReq
	if want == "" {
		want = pl.DefaultPlan
	}
	for i := range plans {
		if plans[i].ID == want {
			plans[i].Sel, pl.PlanSel, pl.PlanName = true, plans[i].ID, plans[i].Name
		}
	}
	pl.Plans = plans
	free := h.freeDays(ctx, start, end)
	today := now.Format(dayISO)
	idx := map[string]int{}
	for i := 0; i < 7; i++ {
		t := start.AddDate(0, 0, i)
		ds := t.Format(dayISO)
		idx[ds] = i
		pl.Days = append(pl.Days, terminalPlanDay{Label: t.Format("02.01."), Short: weekdayShort[t.Weekday()], Holiday: free[ds],
			Today: ds == today, Weekend: t.Weekday() == time.Saturday || t.Weekday() == time.Sunday})
	}

	people := map[string]*terminalPlanPerson{}
	var order []string
	rows, err := h.db.Query(ctx, `SELECT id::text, TRIM(first_name || ' ' || last_name) FROM users
		WHERE active AND NOT is_bot AND NOT is_system_user AND department_id = $1::uuid
		ORDER BY lower(last_name), lower(first_name), id`, pl.Dept)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		people[id] = &terminalPlanPerson{name: name, cells: make([]terminalPlanCell, 7)}
		order = append(order, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(order) == 0 {
		return nil
	}
	// Einteilungen: nur veröffentlichte Pläne (Entwürfe sind nicht für den Aushang); keine Notiz.
	rows, err = h.db.Query(ctx, `SELECT sa.user_id::text, sa.date::text, sd.name, to_char(sd.start_time, 'HH24:MI'), to_char(sd.end_time, 'HH24:MI')
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sd.id = sa.shift_id
		LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.date BETWEEN $1 AND $2 AND sa.user_id = ANY($3::uuid[]) AND (sa.plan_id IS NULL OR sp.status = 'published')
		  AND ($4::text = '' OR sa.plan_id = NULLIF($4::text, '')::uuid)`, start, end, order, pl.PlanSel)
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid, date, name, a, b string
		if err := rows.Scan(&uid, &date, &name, &a, &b); err != nil {
			rows.Close()
			return err
		}
		if i, ok := idx[date]; ok && people[uid] != nil {
			people[uid].cells[i].Shift, people[uid].cells[i].Time = name, a+"–"+b
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Sonderdienste
	rows, err = h.db.Query(ctx, `SELECT a.user_id::text, a.date::text, t.name FROM duty_assignments a
		JOIN duty_types t ON t.id = a.duty_type_id AND t.active
		WHERE a.date BETWEEN $1 AND $2 AND a.user_id = ANY($3::uuid[]) ORDER BY t.sort_order, t.name`, start, end, order)
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid, date, name string
		if err := rows.Scan(&uid, &date, &name); err != nil {
			rows.Close()
			return err
		}
		if i, ok := idx[date]; ok && people[uid] != nil {
			people[uid].cells[i].Duties = append(people[uid].cells[i].Duties, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Abwesenheiten (genehmigt): bewusst ohne die Spalte type – der Grund verlässt den Server nicht.
	rows, err = h.db.Query(ctx, `SELECT user_id::text, start_date, end_date FROM absences
		WHERE status = 'approved' AND start_date <= $2 AND end_date >= $1 AND user_id = ANY($3::uuid[])`, start, end, order)
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid string
		var a, b time.Time
		if err := rows.Scan(&uid, &a, &b); err != nil {
			rows.Close()
			return err
		}
		for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
			if i, ok := idx[t.Format(dayISO)]; ok && people[uid] != nil {
				c := &people[uid].cells[i]
				c.Absent, c.Shift, c.Time = true, "", "" // abwesend verdrängt die Schicht
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range order {
		pp := people[id]
		used := false
		for _, c := range pp.cells {
			if !c.Empty() {
				used = true
			}
		}
		if used {
			pl.Rows = append(pl.Rows, terminalPlanRow{Name: pp.name, Cells: pp.cells})
		}
	}
	if pl.ShowToday {
		pl.Today = terminalPlanToday(pl.Rows, idx[today])
	}
	return nil
}

// terminalPlanToday: heute nach Schicht gruppiert, dann Sonderdienste, zuletzt abwesend.
func terminalPlanToday(rows []terminalPlanRow, ti int) []terminalPlanGroup {
	var groups []*terminalPlanGroup
	byKey := map[string]*terminalPlanGroup{}
	add := func(key, title, tm string, absent, duty bool, name string) {
		g := byKey[key]
		if g == nil {
			g = &terminalPlanGroup{Title: title, Time: tm, Absent: absent, Duty: duty}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.Names = append(g.Names, name)
	}
	for _, r := range rows {
		if c := r.Cells[ti]; c.Shift != "" {
			add("s:"+c.Shift+c.Time, c.Shift, c.Time, false, false, r.Name)
		}
	}
	for _, r := range rows {
		for _, d := range r.Cells[ti].Duties {
			add("d:"+d, d, "", false, true, r.Name)
		}
	}
	for _, r := range rows {
		if r.Cells[ti].Absent {
			add("a", "abwesend", "", true, false, r.Name)
		}
	}
	out := make([]terminalPlanGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out
}
