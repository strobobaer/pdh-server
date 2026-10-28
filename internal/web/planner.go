package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Planer (/planner, migrations/133): Schichtplan, Urlaub & Abwesenheit,
// Wartungsplanung, Schichtmodelle & Vorlagen, Einstellungen.
//
// Schichtplan: eine Woche, oben die Tage, links die Schichten des gewählten
// Schichtmodells (rechts wählbar). In jedem Feld stehen die eingeteilten
// Mitarbeiter, ein Klick öffnet die Auswahl. Pro Person und Tag gibt es eine
// Schicht (shift_assignments: UNIQUE user_id, date) – wer in eine andere
// Schicht gesetzt wird, wechselt dorthin.
//
// Rechte: shifts.view (ansehen), shifts.edit (einteilen, Modelle, Pläne),
// absences.request / absences.approve / absences.view_all, maintenance.edit
// (Wartung verschieben/zuweisen).

const dayISO = "2006-01-02"

func (h *Handler) canPlannerView(r *http.Request) bool { return h.hasPerm(r, "shifts.view") }
func (h *Handler) canShiftPlan(r *http.Request) bool   { return h.hasPerm(r, "shifts.edit") }
func (h *Handler) canAbsenceApprove(r *http.Request) bool {
	return h.hasPerm(r, "absences.approve")
}
func (h *Handler) canAbsenceViewAll(r *http.Request) bool {
	return h.hasPerm(r, "absences.view_all") || h.hasPerm(r, "absences.approve")
}

var weekdayShort = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

// mondayOf: Montag der Woche von t (lokal, 0 Uhr).
func mondayOf(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	off := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -off)
}

// ── Feiertage ─────────────────────────────────────────────────

var germanStates = []struct{ Code, Name string }{
	{"", "nur bundesweite Feiertage"}, {"BW", "Baden-Württemberg"}, {"BY", "Bayern"}, {"BE", "Berlin"}, {"BB", "Brandenburg"},
	{"HB", "Bremen"}, {"HH", "Hamburg"}, {"HE", "Hessen"}, {"MV", "Mecklenburg-Vorpommern"}, {"NI", "Niedersachsen"},
	{"NW", "Nordrhein-Westfalen"}, {"RP", "Rheinland-Pfalz"}, {"SL", "Saarland"}, {"SN", "Sachsen"}, {"ST", "Sachsen-Anhalt"},
	{"SH", "Schleswig-Holstein"}, {"TH", "Thüringen"},
}

// easterSunday: Gaußsche Osterformel (gregorianisch, Anonymer Algorithmus).
func easterSunday(y int) time.Time {
	a := y % 19
	b, c := y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	hh := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - hh - k) % 7
	m := (a + 11*hh + 22*l) / 451
	month := (hh + l - 7*m + 114) / 31
	day := (hh+l-7*m+114)%31 + 1
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.Local)
}

// germanHolidays: gesetzliche Feiertage eines Jahres (Bund + Bundesland).
func germanHolidays(y int, state string) map[string]string {
	out := map[string]string{}
	add := func(t time.Time, name string) { out[t.Format(dayISO)] = name }
	fix := func(m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	in := func(states ...string) bool {
		for _, s := range states {
			if s == state {
				return true
			}
		}
		return false
	}
	e := easterSunday(y)
	add(fix(time.January, 1), "Neujahr")
	add(e.AddDate(0, 0, -2), "Karfreitag")
	add(e.AddDate(0, 0, 1), "Ostermontag")
	add(fix(time.May, 1), "Tag der Arbeit")
	add(e.AddDate(0, 0, 39), "Christi Himmelfahrt")
	add(e.AddDate(0, 0, 50), "Pfingstmontag")
	add(fix(time.October, 3), "Tag der Deutschen Einheit")
	add(fix(time.December, 25), "1. Weihnachtstag")
	add(fix(time.December, 26), "2. Weihnachtstag")
	if in("BW", "BY", "ST") {
		add(fix(time.January, 6), "Heilige Drei Könige")
	}
	if in("BE", "MV") {
		add(fix(time.March, 8), "Internationaler Frauentag")
	}
	if in("BB") {
		add(e, "Ostersonntag")
		add(e.AddDate(0, 0, 49), "Pfingstsonntag")
	}
	if in("BW", "BY", "HE", "NW", "RP", "SL") {
		add(e.AddDate(0, 0, 60), "Fronleichnam")
	}
	if in("SL") {
		add(fix(time.August, 15), "Mariä Himmelfahrt")
	}
	if in("TH") {
		add(fix(time.September, 20), "Weltkindertag")
	}
	if in("BB", "HB", "HH", "MV", "NI", "SN", "ST", "SH", "TH") {
		add(fix(time.October, 31), "Reformationstag")
	}
	if in("BW", "BY", "NW", "RP", "SL") {
		add(fix(time.November, 1), "Allerheiligen")
	}
	if in("SN") { // Mittwoch vor dem 23. November
		d := fix(time.November, 22)
		for d.Weekday() != time.Wednesday {
			d = d.AddDate(0, 0, -1)
		}
		add(d, "Buß- und Bettag")
	}
	return out
}

// freeDays: Feiertage (Bundesland aus den Einstellungen) und Betriebsruhe
// im Zeitraum, Datum → Bezeichnung.
func (h *Handler) freeDays(ctx context.Context, from, to time.Time) map[string]string {
	state := h.appSetting(ctx, "planner_holiday_state", "")
	out := map[string]string{}
	for y := from.Year(); y <= to.Year(); y++ {
		for d, n := range germanHolidays(y, state) {
			out[d] = n
		}
	}
	if rows, err := h.db.Query(ctx, `SELECT day::text, name FROM planner_closures WHERE day BETWEEN $1 AND $2`, from, to); err == nil {
		for rows.Next() {
			var d, n string
			if rows.Scan(&d, &n) == nil {
				out[d] = n
			}
		}
		rows.Close()
	}
	return out
}

// ── Schichtplan ───────────────────────────────────────────────

type plannerDay struct {
	Date, Label, Short, Holiday string
	Weekend, Today              bool
}

type plannerShift struct {
	ID, Name, Short, Color, Time string
	MinStaff                     int
}

type plannerPerson struct {
	ID, Name, Short string
	Draft           bool   // aus einem unveröffentlichten Plan
	Note            string // Notiz an der Zuweisung
}

type plannerCell struct {
	People []plannerPerson
	Under  bool // Mindestbesetzung unterschritten
}

type plannerRow struct {
	Shift plannerShift
	Cells []plannerCell
}

type plannerModel struct {
	ID, Name, Description string
	Shifts                int
	Active                bool
}

type plannerUser struct {
	ID, Name, Dept string
}

// plannerDayInfo: wer an einem Tag wo eingeteilt oder abwesend ist (für die Auswahl).
type plannerDayInfo struct {
	Shift   map[string]string `json:"shift"`   // user → Schicht-Kürzel (alle Modelle)
	ShiftID map[string]string `json:"shiftId"` // user → shift_id
	Absent  map[string]string `json:"absent"`  // user → Grund bzw. „abwesend“
}

func (h *Handler) plannerModels(ctx context.Context, all bool) []plannerModel {
	where := "WHERE m.active"
	if all {
		where = ""
	}
	rows, err := h.db.Query(ctx, `SELECT m.id::text, m.name, COALESCE(m.description, ''), m.active,
			(SELECT COUNT(*) FROM shift_definitions d WHERE d.model_id = m.id AND d.active)
		FROM shift_models m `+where+` ORDER BY m.active DESC, m.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []plannerModel
	for rows.Next() {
		var m plannerModel
		if rows.Scan(&m.ID, &m.Name, &m.Description, &m.Active, &m.Shifts) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (h *Handler) plannerShifts(ctx context.Context, modelID string) []plannerShift {
	rows, err := h.db.Query(ctx, `SELECT id::text, name, short_name, color, to_char(start_time, 'HH24:MI'), to_char(end_time, 'HH24:MI'), min_staff
		FROM shift_definitions WHERE model_id = $1::uuid AND active ORDER BY sort_order, start_time, name`, modelID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []plannerShift
	for rows.Next() {
		var s plannerShift
		var a, b string
		if rows.Scan(&s.ID, &s.Name, &s.Short, &s.Color, &a, &b, &s.MinStaff) == nil {
			s.Time = a + "–" + b
			out = append(out, s)
		}
	}
	return out
}

// plannerUsers: aktive Mitarbeiter (optional nach Abteilung / Gruppe gefiltert).
func (h *Handler) plannerUsers(ctx context.Context, dept, group string) []plannerUser {
	rows, err := h.db.Query(ctx, `SELECT u.id::text, TRIM(u.first_name || ' ' || u.last_name), COALESCE(d.name, '')
		FROM users u LEFT JOIN departments d ON d.id = u.department_id
		WHERE u.active AND u.id <> $3::uuid
		  AND ($1 = '' OR u.department_id::text = $1)
		  AND ($2 = '' OR EXISTS (SELECT 1 FROM user_group_members gm WHERE gm.user_id = u.id AND gm.group_id::text = $2))
		ORDER BY u.last_name, u.first_name`, dept, group, pdhSystemUserID)
	if err != nil {
		componentLog("planer").Warn().Err(err).Msg("mitarbeiter laden")
		return nil
	}
	defer rows.Close()
	var out []plannerUser
	for rows.Next() {
		var u plannerUser
		if rows.Scan(&u.ID, &u.Name, &u.Dept) == nil {
			out = append(out, u)
		}
	}
	return out
}

func nameInitials(name string) string {
	var b strings.Builder
	for _, p := range strings.Fields(name) {
		r := []rune(p)
		b.WriteRune(r[0])
	}
	return strings.ToUpper(b.String())
}

type PlannerData struct {
	BaseData
	Tab            string
	Message, Error string
	CanPlan        bool
	CanApprove     bool
	CanViewAll     bool
	CanRequest     bool
	CanMaintEdit   bool
	CanMaintView   bool

	// Schichtplan
	Week, PrevWeek, NextWeek, ThisWeek string
	WeekLabel                          string
	Days                               []plannerDay
	Models                             []plannerModel
	ModelID                            string
	Rows                               []plannerRow
	Users                              []plannerUser
	DayInfo                            map[string]plannerDayInfo
	Depts, Groups                      []partnerOption
	Dept, Group                        string
	DraftCount                         int
	DutyRows                           []plannerDutyRow // Sonderdienste (planner_duties.go)
	CanDuties                          bool

	// weitere Reiter (planner_absences.go, planner_models.go, planner_maint.go)
	Abs   *absenceTabData
	Maint *maintTabData
	Setup *setupTabData
}

// PlannerPage: GET /planner?tab=shifts|absences|maintenance|models|settings
func (h *Handler) PlannerPage(w http.ResponseWriter, r *http.Request) {
	if !h.canPlannerView(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := PlannerData{
		BaseData: h.baseData(r, "planner", "Planer", "Urlaub, Schichten & Wartung"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Error: q.Get("err"),
		CanPlan: h.canShiftPlan(r), CanApprove: h.canAbsenceApprove(r), CanViewAll: h.canAbsenceViewAll(r),
		CanRequest: h.hasPerm(r, "absences.request"), CanMaintEdit: h.hasPerm(r, "maintenance.edit"),
		CanMaintView: h.hasPerm(r, "maintenance.view") || h.hasPerm(r, "maintenance.edit"),
	}
	switch d.Tab {
	case "absences", "maintenance", "models", "settings":
	default:
		d.Tab = "shifts"
	}
	if (d.Tab == "models" || d.Tab == "settings") && !d.CanPlan {
		d.Tab = "shifts"
	}
	if d.Tab == "maintenance" && !d.CanMaintView {
		d.Tab = "shifts"
	}
	switch d.Tab {
	case "shifts":
		h.fillShiftWeek(r, &d)
	case "absences":
		d.Abs = h.absenceTab(r)
	case "maintenance":
		d.Maint = h.maintTab(r)
	case "models", "settings":
		d.Setup = h.setupTab(r)
	}
	h.render(w, "planner", d)
}

func (h *Handler) fillShiftWeek(r *http.Request, d *PlannerData) {
	ctx := r.Context()
	q := r.URL.Query()
	start := mondayOf(time.Now())
	if t, err := time.ParseInLocation(dayISO, q.Get("week"), time.Local); err == nil {
		start = mondayOf(t)
	}
	end := start.AddDate(0, 0, 6)
	d.Week, d.ThisWeek = start.Format(dayISO), mondayOf(time.Now()).Format(dayISO)
	d.PrevWeek, d.NextWeek = start.AddDate(0, 0, -7).Format(dayISO), start.AddDate(0, 0, 7).Format(dayISO)
	_, wk := start.ISOWeek()
	d.WeekLabel = fmt.Sprintf("KW %d · %s – %s", wk, start.Format("02.01."), end.Format("02.01.2006"))
	free := h.freeDays(ctx, start, end)
	today := time.Now().Format(dayISO)
	for i := 0; i < 7; i++ {
		t := start.AddDate(0, 0, i)
		ds := t.Format(dayISO)
		d.Days = append(d.Days, plannerDay{Date: ds, Label: t.Format("02.01."), Short: weekdayShort[t.Weekday()],
			Holiday: free[ds], Weekend: t.Weekday() == time.Saturday || t.Weekday() == time.Sunday, Today: ds == today})
	}

	d.Models = h.plannerModels(ctx, false)
	d.ModelID = q.Get("model")
	if d.ModelID == "" {
		d.ModelID = h.userPref(ctx, getUser(r).ID, "planner_model")
	}
	found := false
	for _, m := range d.Models {
		if m.ID == d.ModelID {
			found = true
		}
	}
	if !found && len(d.Models) > 0 {
		d.ModelID = d.Models[0].ID
	}
	if q.Get("model") != "" && found {
		h.setUserPref(ctx, getUser(r).ID, "planner_model", d.ModelID)
	}
	d.Dept, d.Group = q.Get("dept"), q.Get("group")
	d.Depts, d.Groups = h.plannerDeptOptions(ctx), h.plannerGroupOptions(ctx)
	d.Users = h.plannerUsers(ctx, d.Dept, d.Group)
	inList := map[string]bool{}
	for _, u := range d.Users {
		inList[u.ID] = true
	}

	shifts := h.plannerShifts(ctx, d.ModelID)
	idx := map[string]int{}
	for i, s := range shifts {
		idx[s.ID] = i
		row := plannerRow{Shift: s, Cells: make([]plannerCell, 7)}
		d.Rows = append(d.Rows, row)
	}
	d.DayInfo = map[string]plannerDayInfo{}
	for _, day := range d.Days {
		d.DayInfo[day.Date] = plannerDayInfo{Shift: map[string]string{}, ShiftID: map[string]string{}, Absent: map[string]string{}}
	}
	// Zuweisungen der Woche (alle Modelle – für „ist schon in Schicht X“)
	rows, err := h.db.Query(ctx, `
		SELECT sa.user_id::text, sa.shift_id::text, sa.date::text, sa.note, sd.short_name,
		       TRIM(u.first_name || ' ' || u.last_name), (sa.plan_id IS NOT NULL AND sp.status = 'draft')
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sd.id = sa.shift_id
		JOIN users u ON u.id = sa.user_id
		LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.date BETWEEN $1 AND $2 AND ($3 OR sa.plan_id IS NULL OR sp.status = 'published')
		ORDER BY u.last_name, u.first_name`, start, end, d.CanPlan)
	if err != nil {
		d.Error = friendlyDBError(err)
	} else {
		for rows.Next() {
			var uid, sid, date, note, short, name string
			var draft bool
			if rows.Scan(&uid, &sid, &date, &note, &short, &name, &draft) != nil {
				continue
			}
			if info, ok := d.DayInfo[date]; ok {
				info.Shift[uid], info.ShiftID[uid] = short, sid
			}
			i, ok := idx[sid]
			if !ok || (len(inList) > 0 && !inList[uid] && (d.Dept != "" || d.Group != "")) {
				continue
			}
			col := int(mustDate(date).Sub(start).Hours()/24 + 0.5)
			if col < 0 || col > 6 {
				continue
			}
			if draft {
				d.DraftCount++
			}
			c := &d.Rows[i].Cells[col]
			c.People = append(c.People, plannerPerson{ID: uid, Name: name, Short: nameInitials(name), Draft: draft, Note: note})
		}
		rows.Close()
	}
	for i := range d.Rows {
		for j := range d.Rows[i].Cells {
			c := &d.Rows[i].Cells[j]
			c.Under = d.Rows[i].Shift.MinStaff > 0 && len(c.People) < d.Rows[i].Shift.MinStaff
		}
	}
	h.fillDutyWeek(r, d, start)
	// Abwesenheiten der Woche (genehmigt und beantragt)
	viewAll := d.CanViewAll
	if arows, err := h.db.Query(ctx, `SELECT user_id::text, type::text, status::text, start_date, end_date FROM absences
		WHERE status IN ('approved', 'pending') AND start_date <= $2 AND end_date >= $1`, start, end); err == nil {
		for arows.Next() {
			var uid, typ, st string
			var a, b time.Time
			if arows.Scan(&uid, &typ, &st, &a, &b) != nil {
				continue
			}
			label := "abwesend"
			if viewAll || uid == getUser(r).ID {
				label = absenceTypeLabels[typ]
			}
			if st == "pending" {
				label += " (beantragt)"
			}
			for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
				if info, ok := d.DayInfo[t.Format(dayISO)]; ok {
					info.Absent[uid] = label
				}
			}
		}
		arows.Close()
	}
}

func mustDate(s string) time.Time {
	t, _ := time.ParseInLocation(dayISO, s, time.Local)
	return t
}

func (h *Handler) plannerDeptOptions(ctx context.Context) []partnerOption {
	return h.simpleOptions(ctx, `SELECT id::text, name FROM departments WHERE active ORDER BY name`)
}
func (h *Handler) plannerGroupOptions(ctx context.Context) []partnerOption {
	return h.simpleOptions(ctx, `SELECT id::text, name FROM user_groups WHERE active ORDER BY name`)
}

func (h *Handler) simpleOptions(ctx context.Context, sql string, args ...any) []partnerOption {
	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []partnerOption
	for rows.Next() {
		var o partnerOption
		if rows.Scan(&o.Value, &o.Label) == nil {
			out = append(out, o)
		}
	}
	return out
}

// userPref / setUserPref: kleine Merker je Benutzer in app_settings.
func (h *Handler) userPref(ctx context.Context, uid, key string) string {
	if uid == "" {
		return ""
	}
	return h.appSetting(ctx, "user:"+uid+":"+key, "")
}
func (h *Handler) setUserPref(ctx context.Context, uid, key, value string) {
	if uid != "" && !h.Mirror().On { // Spiegel: Ansicht nicht merken
		_ = h.setAppSetting(ctx, "user:"+uid+":"+key, value)
	}
}

// PlannerCellWeb: POST /planner/shifts/cell – JSON {date, shift_id, user_ids[]}
// setzt die Besetzung eines Feldes: Ausgewählte kommen in diese Schicht
// (wechseln ggf. aus einer anderen), Abgewählte werden aus ihr entfernt.
func (h *Handler) PlannerCellWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canShiftPlan(r) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung zum Einteilen."})
		return
	}
	var in struct {
		Date    string   `json:"date"`
		ShiftID string   `json:"shift_id"`
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return
	}
	if _, err := time.Parse(dayISO, in.Date); err != nil || !uuidInPathRe.MatchString(in.ShiftID) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Datum oder Schicht ungültig"})
		return
	}
	want := map[string]bool{}
	for _, u := range in.UserIDs {
		if uuidInPathRe.MatchString(u) {
			want[u] = true
		}
	}
	ctx := r.Context()
	uid := getUser(r).ID
	tx, err := h.db.Begin(ctx)
	if err != nil {
		completionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer tx.Rollback(ctx)
	// bisher in diesem Feld
	var removed int
	tag, err := tx.Exec(ctx, `DELETE FROM shift_assignments WHERE date = $1::date AND shift_id = $2::uuid AND NOT (user_id::text = ANY($3))`,
		in.Date, in.ShiftID, keys(want))
	if err == nil {
		removed = int(tag.RowsAffected())
		for u := range want {
			if _, err = tx.Exec(ctx, `INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by)
				VALUES ($1::uuid, $2::uuid, $3::date, '', $4::uuid)
				ON CONFLICT (user_id, date) DO UPDATE SET shift_id = EXCLUDED.shift_id, created_by = EXCLUDED.created_by,
					plan_id = CASE WHEN shift_assignments.shift_id = EXCLUDED.shift_id THEN shift_assignments.plan_id END`,
				u, in.ShiftID, in.Date, nullID(uid)); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	componentLog("planer").Info().Str("tag", in.Date).Str("schicht", in.ShiftID).Int("besetzt", len(want)).Int("entfernt", removed).Str("von", uid).Msg("schicht besetzt")
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PlannerCopyWeekWeb: POST /planner/shifts/copy-week – Besetzung der Vorwoche
// (gewähltes Modell) in die Woche übernehmen; vorhandene Einträge bleiben.
func (h *Handler) PlannerCopyWeekWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canShiftPlan(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	week, model := r.FormValue("week"), r.FormValue("model")
	back := "/planner?tab=shifts&week=" + url.QueryEscape(week) + "&model=" + url.QueryEscape(model)
	start, err := time.ParseInLocation(dayISO, week, time.Local)
	if err != nil || !uuidInPathRe.MatchString(model) {
		http.Redirect(w, r, back+"&err="+url.QueryEscape("Woche oder Modell ungültig"), http.StatusSeeOther)
		return
	}
	start = mondayOf(start)
	tag, err := h.db.Exec(r.Context(), `
		INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by)
		SELECT sa.user_id, sa.shift_id, sa.date + 7, '', $3::uuid
		FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id AND sd.model_id = $2::uuid
		LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.date BETWEEN $1::date - 7 AND $1::date - 1 AND (sa.plan_id IS NULL OR sp.status = 'published')
		  AND NOT EXISTS (SELECT 1 FROM absences a WHERE a.user_id = sa.user_id AND a.status = 'approved'
		                  AND sa.date + 7 BETWEEN a.start_date AND a.end_date)
		ON CONFLICT (user_id, date) DO NOTHING`, start, model, nullID(getUser(r).ID))
	if err != nil {
		http.Redirect(w, r, back+"&err="+url.QueryEscape(friendlyDBError(err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(fmt.Sprintf("%d Einteilungen aus der Vorwoche übernommen (vorhandene und Urlaubstage bleiben unberührt)", tag.RowsAffected())), http.StatusSeeOther)
}

var errNoPlannerPerm = errors.New("keine Berechtigung")

func plannerBack(w http.ResponseWriter, r *http.Request, tab, extra, msg string, err error) {
	target := "/planner?tab=" + url.QueryEscape(tab) + extra
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyPartError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
