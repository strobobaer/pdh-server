package web

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Planungsfolien des Infobildschirms (Migration 208): Schichtplan, Abwesenheiten, Dienste/Bereitschaft und
// Wartungsplanung aus dem Planer. Jede Folie hat einen Zeitraum (heute, diese Woche, nächste Woche, nächste N Tage)
// und eine Auswahl von Abteilungen. Auf Terminals gilt zusätzlich die Abteilungsgrenze des Terminals
// (terminalDepartments): angezeigt wird die Schnittmenge. Sichtbar ist nur, wofür der Terminal-Benutzer in der
// Abteilung das Recht hat (shifts.view bzw. maintenance.view). Abwesenheiten zeigen nur „abwesend“ – Art und Grund
// der Abwesenheit werden nicht einmal gelesen und verlassen den Server nie. Die Folien sind dem Zwischenspeicher des
// Geräts unbekannt (nicht in CACHEABLE_KINDS) und entfallen ohne Verbindung.

const (
	slideKindPlanShifts   = "plan_shifts"   // Schichtplan je Schicht
	slideKindPlanAbsences = "plan_absences" // Abwesenheiten („abwesend“)
	slideKindPlanDuties   = "plan_duties"   // Dienste und Bereitschaft (Sonderdienste)
	slideKindPlanMaint    = "plan_maint"    // fällige Wartungen

	planRangeToday    = "today"
	planRangeWeek     = "week"
	planRangeNextWeek = "next_week"
	planRangeDays     = "days"

	planMinDays      = 1
	planMaxDays      = 31
	planDefaultDays  = 7
	planMaxDepts     = 100
	planMaxPerGroup  = 30         // Einträge je Gruppe und Tag, der Rest als „+ n weitere“
	absentLabelPlain = "abwesend" // das Einzige, was zu einer Abwesenheit hinausgeht
)

func slideIsPlan(kind string) bool {
	return kind == slideKindPlanShifts || kind == slideKindPlanAbsences || kind == slideKindPlanDuties || kind == slideKindPlanMaint
}

func validPlanRange(r string) bool {
	return r == planRangeToday || r == planRangeWeek || r == planRangeNextWeek || r == planRangeDays
}

// planRangeBounds: erster und letzter Tag (einschließlich) des Zeitraums, auf Mitternacht (Ortszeit).
func planRangeBounds(now time.Time, rng string, n int) (time.Time, time.Time) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch rng {
	case planRangeWeek:
		m := mondayOf(today)
		return m, m.AddDate(0, 0, 6)
	case planRangeNextWeek:
		m := mondayOf(today).AddDate(0, 0, 7)
		return m, m.AddDate(0, 0, 6)
	case planRangeDays:
		if n < planMinDays || n > planMaxDays {
			n = planDefaultDays
		}
		return today, today.AddDate(0, 0, n-1)
	}
	return today, today
}

// planRangeLabel: der Zeitraum in Worten (Liste und Folie).
func planRangeLabel(rng string, n int) string {
	switch rng {
	case planRangeWeek:
		return "diese Woche"
	case planRangeNextWeek:
		return "nächste Woche"
	case planRangeDays:
		if n == 1 {
			return "nächster Tag"
		}
		return fmt.Sprintf("nächste %d Tage", n)
	}
	return "heute"
}

// Inhalt einer Planungsfolie. Die Gruppen eines Tages sind je nach Art Schichten, Dienstarten, „abwesend“ oder
// „Fällig“; Items sind Namen bzw. Wartungstitel (nur Text, der Player nimmt textContent).
type planSlideGroup struct {
	Name  string   `json:"name"`
	Color string   `json:"color,omitempty"`
	Items []string `json:"items"`
	More  int      `json:"more,omitempty"`
}

type planSlideDay struct {
	Date   string              `json:"date"`
	Label  string              `json:"label"`
	Today  bool                `json:"today,omitempty"`
	Groups []planSlideGroup `json:"groups"`
}

type planSlideData struct {
	Range string            `json:"range"`
	Label string            `json:"label"`
	From  string            `json:"from"`
	To    string            `json:"to"`
	Days  []planSlideDay `json:"days"`
}

// planScope: die Abteilungen, deren Daten die Folie für diesen Terminal-Benutzer zeigen darf.
type planScope struct {
	Depts  []string // Abteilungen (IDs), für die das Recht gilt
	NoDept bool     // auch Personen/Anlagen ohne Abteilung (nur ohne Auswahl und ohne Terminal-Grenze)
}

func (p planScope) empty() bool { return len(p.Depts) == 0 && !p.NoDept }

// planSlideScope bestimmt die Abteilungen einer Folie: Auswahl der Folie (leer = alle) geschnitten mit den
// Abteilungen des Terminals (leer = keine Grenze bekannt), davon die, in denen der Terminal-Benutzer perm hat.
func (h *Handler) planSlideScope(ctx context.Context, uid, role, perm string, selected []string) (planScope, error) {
	if h.rbac == nil || uid == "" {
		return planScope{}, nil
	}
	all, err := h.queryStrings(ctx, `SELECT id::text FROM departments WHERE active`)
	if err != nil {
		return planScope{}, err
	}
	term, err := h.terminalDepartments(ctx, uid)
	if err != nil {
		return planScope{}, err
	}
	var sc planScope
	for _, d := range all {
		if len(selected) > 0 && !slices.Contains(selected, d) {
			continue
		}
		if len(term) > 0 && !slices.Contains(term, d) {
			continue
		}
		if h.rbac.HasPermissionInDepartment(uid, role, perm, []string{d}) {
			sc.Depts = append(sc.Depts, d)
		}
	}
	if len(selected) == 0 && len(term) == 0 {
		// Ohne Abteilung nur, wenn das Recht überall gilt (nicht nur in einzelnen Abteilungen)
		if all, _ := h.rbac.Scope(uid, role, perm); all {
			sc.NoDept = true
		}
	}
	return sc, nil
}

type planAdder func(day, key, name, color, item string)

// planSlide baut den Inhalt einer Planungsfolie für den Terminal-Benutzer uid.
func (h *Handler) planSlide(ctx context.Context, uid, role string, s terminalSlide, now time.Time) (*planSlideData, error) {
	from, to := planRangeBounds(now, s.PlanRange, s.PlanDays)
	pl := &planSlideData{Range: s.PlanRange, Label: planRangeLabel(s.PlanRange, s.PlanDays), From: from.Format(dayISO), To: to.Format(dayISO), Days: []planSlideDay{}}
	perm := "shifts.view"
	if s.Kind == slideKindPlanMaint {
		perm = "maintenance.view"
	}
	sc, err := h.planSlideScope(ctx, uid, role, perm, s.DepartmentIDs)
	if err != nil {
		return nil, err
	}
	// Schichtplan: nur ein veröffentlichter Plan, der den Zeitraum schneidet; sonst alle (terminal_shiftplan.go)
	shiftPlan := ""
	if s.Kind == slideKindPlanShifts && s.ShiftPlanID != "" {
		plans, err := h.publishedShiftPlans(ctx, from, to, sc.Depts, sc.NoDept)
		if err != nil {
			return nil, err
		}
		for _, p := range plans {
			if p.ID == normShiftPlanID(s.ShiftPlanID) {
				shiftPlan = p.ID
				pl.Label += " · " + p.Name
			}
		}
	}
	byDay := map[string]map[string]*planSlideGroup{} // Tag → Gruppenschlüssel
	order := map[string][]string{}                      // Tag → Reihenfolge der Gruppen
	add := func(day, key, name, color, item string) {
		if byDay[day] == nil {
			byDay[day] = map[string]*planSlideGroup{}
		}
		g := byDay[day][key]
		if g == nil {
			g = &planSlideGroup{Name: name, Color: color, Items: []string{}}
			byDay[day][key] = g
			order[day] = append(order[day], key)
		}
		if len(g.Items) < planMaxPerGroup {
			g.Items = append(g.Items, item)
		} else {
			g.More++
		}
	}
	if !sc.empty() {
		depts := sc.Depts
		if depts == nil {
			depts = []string{}
		}
		switch s.Kind {
		case slideKindPlanShifts:
			err = h.planShiftRows(ctx, from, to, depts, sc.NoDept, shiftPlan, add)
		case slideKindPlanAbsences:
			err = h.planAbsenceRows(ctx, from, to, depts, sc.NoDept, add)
		case slideKindPlanDuties:
			err = h.planDutyRows(ctx, from, to, depts, sc.NoDept, add)
		case slideKindPlanMaint:
			err = h.planMaintRows(ctx, from, to, now, depts, sc.NoDept, add)
		}
		if err != nil {
			return nil, err
		}
	}
	today := now.Format(dayISO)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		k := d.Format(dayISO)
		if len(byDay[k]) == 0 && to.Sub(from) > 6*24*time.Hour {
			continue // lange Zeiträume: nur Tage mit Einträgen
		}
		day := planSlideDay{Date: k, Label: shortWeekday("de", d.Weekday()) + " " + d.Format("02.01."), Today: k == today, Groups: []planSlideGroup{}}
		for _, key := range order[k] {
			day.Groups = append(day.Groups, *byDay[k][key])
		}
		pl.Days = append(pl.Days, day)
	}
	return pl, nil
}

// personInDept: SQL-Bedingung für die Abteilung einer Person (u.department_id) mit den Parametern $a (Abteilungen)
// und $b (auch ohne Abteilung).
func personInDept(a, b int) string {
	return fmt.Sprintf(`(u.department_id = ANY($%d::uuid[]) OR ($%d::boolean AND u.department_id IS NULL))`, a, b)
}

func (h *Handler) planShiftRows(ctx context.Context, from, to time.Time, depts []string, noDept bool, planID string, add planAdder) error {
	rows, err := h.db.Query(ctx, `SELECT sa.date::text, sd.id::text, sd.name, COALESCE(sd.color, ''), TRIM(u.first_name || ' ' || u.last_name)
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sd.id = sa.shift_id
		JOIN users u ON u.id = sa.user_id
		LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.date BETWEEN $1::date AND $2::date AND (sa.plan_id IS NULL OR sp.status = 'published')
		  AND u.active AND NOT u.is_system_user AND `+personInDept(3, 4)+`
		  AND ($5::text = '' OR sa.plan_id = NULLIF($5::text, '')::uuid)
		ORDER BY sa.date, sd.sort_order, sd.start_time, sd.name, lower(u.last_name), lower(u.first_name)`, from, to, depts, noDept, planID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day, id, name, color, person string
		if err := rows.Scan(&day, &id, &name, &color, &person); err != nil {
			return err
		}
		if !hexColorRe.MatchString(color) {
			color = ""
		}
		add(day, id, name, color, person)
	}
	return rows.Err()
}

// planAbsenceRows: genehmigte Abwesenheiten je Tag. Art und Notiz stehen nicht in der Abfrage.
func (h *Handler) planAbsenceRows(ctx context.Context, from, to time.Time, depts []string, noDept bool, add planAdder) error {
	rows, err := h.db.Query(ctx, `SELECT TRIM(u.first_name || ' ' || u.last_name), GREATEST(a.start_date, $1::date), LEAST(a.end_date, $2::date)
		FROM absences a JOIN users u ON u.id = a.user_id
		WHERE a.status = 'approved' AND a.start_date <= $2::date AND a.end_date >= $1::date
		  AND u.active AND NOT u.is_system_user AND `+personInDept(3, 4)+`
		ORDER BY lower(u.last_name), lower(u.first_name)`, from, to, depts, noDept)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var person string
		var a, b time.Time
		if err := rows.Scan(&person, &a, &b); err != nil {
			return err
		}
		for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
			add(d.Format(dayISO), "absent", absentLabelPlain, "", person)
		}
	}
	return rows.Err()
}

func (h *Handler) planDutyRows(ctx context.Context, from, to time.Time, depts []string, noDept bool, add planAdder) error {
	rows, err := h.db.Query(ctx, `SELECT da.date::text, dt.id::text, dt.name, COALESCE(dt.color, ''), TRIM(u.first_name || ' ' || u.last_name)
		FROM duty_assignments da
		JOIN duty_types dt ON dt.id = da.duty_type_id AND dt.active
		JOIN users u ON u.id = da.user_id
		WHERE da.date BETWEEN $1::date AND $2::date AND u.active AND NOT u.is_system_user AND `+personInDept(3, 4)+`
		ORDER BY da.date, dt.sort_order, dt.name, lower(u.last_name), lower(u.first_name)`, from, to, depts, noDept)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day, id, name, color, person string
		if err := rows.Scan(&day, &id, &name, &color, &person); err != nil {
			return err
		}
		if !hexColorRe.MatchString(color) {
			color = ""
		}
		add(day, id, name, color, person)
	}
	return rows.Err()
}

// planMaintRows: offene Wartungsaufträge, die im Zeitraum fällig sind, in Anlagen der erlaubten Abteilungen.
// Enthält der Zeitraum heute, erscheinen überfällige (früher fällige, noch offene) unter heute als „Überfällig“.
// Fälligkeitstage zählen in der Ortszeit des Servers, nicht der Sitzungs-Zeitzone der Datenbank.
func (h *Handler) planMaintRows(ctx context.Context, from, to, now time.Time, depts []string, noDept bool, add planAdder) error {
	loc := from.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	lower := from
	withOverdue := !today.Before(from) && !today.After(to)
	if withOverdue {
		lower = time.Date(1970, 1, 1, 0, 0, 0, 0, loc)
	}
	rows, err := h.db.Query(ctx, `SELECT t.due_date, t.title, i.name
		FROM maintenance_tasks t JOIN infrastructure i ON i.id = t.infrastructure_id
		WHERE t.status::text IN ('open', 'in_progress', 'pending') AND t.archived_at IS NULL
		  AND i.active AND i.hidden_at IS NULL AND t.due_date >= $1 AND t.due_date < $2
		  AND (EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = t.infrastructure_id AND x.department_id = ANY($3::uuid[]))
		       OR ($4::boolean AND NOT EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = t.infrastructure_id)))
		ORDER BY t.due_date, lower(i.name), lower(t.title)`, lower, to.AddDate(0, 0, 1), depts, noDept)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var due time.Time
		var title, infra string
		if err := rows.Scan(&due, &title, &infra); err != nil {
			return err
		}
		item := title
		if strings.TrimSpace(infra) != "" {
			item += " · " + infra
		}
		d := due.In(loc)
		if withOverdue && d.Before(today) { // überfällig
			add(today.Format(dayISO), "overdue", "Überfällig", "#d13438", item+" (fällig "+d.Format("02.01.")+")")
			continue
		}
		add(d.Format(dayISO), "due", "Fällig", "", item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return h.planVisitRows(ctx, from, to, depts, noDept, add)
}

// planVisitRows: geplante Wartungstermine (Jahresplan, noch kein Auftrag) im
// Zeitraum als „Geplant“ – gleiche Abteilungsregel wie bei den Aufträgen,
// nie „Überfällig“.
func (h *Handler) planVisitRows(ctx context.Context, from, to time.Time, depts []string, noDept bool, add planAdder) error {
	rows, err := h.db.Query(ctx, `SELECT v.day, mp.name, i.name
		FROM maintenance_visits v JOIN maintenance_plans mp ON mp.id = v.plan_id JOIN infrastructure i ON i.id = mp.infrastructure_id
		WHERE v.state = 'planned' AND mp.active AND i.active AND i.hidden_at IS NULL
		  AND v.day BETWEEN $1::date AND $2::date
		  AND (EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = mp.infrastructure_id AND x.department_id = ANY($3::uuid[]))
		       OR ($4::boolean AND NOT EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = mp.infrastructure_id)))
		ORDER BY v.day, lower(i.name), lower(mp.name)`, from.Format(dayISO), to.Format(dayISO), depts, noDept)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var plan, infra string
		if err := rows.Scan(&day, &plan, &infra); err != nil {
			return err
		}
		item := plan
		if strings.TrimSpace(infra) != "" {
			item += " · " + infra
		}
		add(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, from.Location()).Format(dayISO), "planned", "Geplant", "", item)
	}
	return rows.Err()
}
