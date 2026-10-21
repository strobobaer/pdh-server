package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Urlaub & Abwesenheit im Planer: Antrag (halbe Tage, Vertretung),
// Genehmigung mit Konfliktprüfung (Schichten, Kollegen gleichzeitig weg),
// Urlaubsanspruch mit Übertrag, Teamkalender je Monat. Arbeitstage = Mo–Fr
// ohne Feiertage und Betriebsruhe.

var absenceTypeLabels = map[string]string{"vacation": "Urlaub", "sick": "Krank", "training": "Schulung", "other": "Sonstiges"}
var absenceTypeOrder = []string{"vacation", "sick", "training", "other"}
var absenceStatusLabels = map[string][2]string{
	"pending": {"beantragt", "b-amber"}, "approved": {"genehmigt", "b-green"}, "rejected": {"abgelehnt", "b-red"},
}

type absenceRow struct {
	ID, UserID, UserName, Type, TypeLabel   string
	Status, StatusLabel, StatusClass        string
	From, To, FromISO, ToISO                string
	Days                                    string
	Note, Substitute, DecisionNote, Decider string
	HalfStart, HalfEnd                      bool
	Conflicts                               []string
	Mine                                    bool
}

type entitlementRow struct {
	UserID, Name                      string
	Days, Carry, Taken, Pending, Rest float64
	DaysIn, CarryIn                   string
}

func (e entitlementRow) Q(v float64) string { return formatQty(v) }

type absenceCalRow struct {
	UserID, Name string
	Cells        []absenceCalCell
}

type absenceCalCell struct {
	Class, Title, Short string
}

type absenceTabData struct {
	Year                 int
	PrevYear, NextYear   int
	Me                   entitlementRow
	Mine                 []absenceRow
	Pending              []absenceRow
	Recent               []absenceRow
	Month, MonthLabel    string
	PrevMonth, NextMonth string
	Days                 []plannerDay
	Cal                  []absenceCalRow
	Entitlements         []entitlementRow
	Users                []UserOption
	Types                []struct{ Key, Label string }
	Today                string
}

// absenceWorkdays: Arbeitstage im Zeitraum (Mo–Fr ohne Feiertage/Betriebsruhe), halbe Tage am Rand.
func (h *Handler) absenceWorkdays(ctx context.Context, from, to time.Time, halfStart, halfEnd bool) float64 {
	free := h.freeDays(ctx, from, to)
	var n float64
	for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday || free[t.Format(dayISO)] != "" {
			continue
		}
		v := 1.0
		if (halfStart && t.Equal(from)) || (halfEnd && t.Equal(to)) {
			v = 0.5
		}
		n += v
	}
	return n
}

// entitlement: Anspruch, Übertrag, genommen (genehmigt) und beantragt im Jahr.
func (h *Handler) entitlement(ctx context.Context, uid, name string, year int) entitlementRow {
	e := entitlementRow{UserID: uid, Name: name}
	def, _ := strconv.ParseFloat(h.appSetting(ctx, "planner_vacation_default", "30"), 64)
	e.Days = def
	_ = h.db.QueryRow(ctx, `SELECT days::float8, carryover::float8 FROM vacation_entitlements WHERE user_id = $1::uuid AND year = $2`, uid, year).Scan(&e.Days, &e.Carry)
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
	dec31 := time.Date(year, 12, 31, 0, 0, 0, 0, time.Local)
	rows, err := h.db.Query(ctx, `SELECT start_date, end_date, half_start, half_end, status::text FROM absences
		WHERE user_id = $1::uuid AND type = 'vacation' AND status IN ('approved', 'pending') AND start_date <= $3 AND end_date >= $2`, uid, jan1, dec31)
	if err == nil {
		for rows.Next() {
			var a, b time.Time
			var hs, he bool
			var st string
			if rows.Scan(&a, &b, &hs, &he, &st) != nil {
				continue
			}
			// nur der Teil im Jahr zählt
			if a.Before(jan1) {
				a, hs = jan1, false
			}
			if b.After(dec31) {
				b, he = dec31, false
			}
			n := h.absenceWorkdays(ctx, a, b, hs, he)
			if st == "approved" {
				e.Taken += n
			} else {
				e.Pending += n
			}
		}
		rows.Close()
	}
	e.Rest = e.Days + e.Carry - e.Taken
	e.DaysIn, e.CarryIn = strconv.FormatFloat(e.Days, 'f', -1, 64), strconv.FormatFloat(e.Carry, 'f', -1, 64)
	return e
}

const absenceSelect = `
	SELECT a.id::text, a.user_id::text, TRIM(u.first_name || ' ' || u.last_name), a.type::text, a.status::text,
	       a.start_date, a.end_date, a.days::float8, COALESCE(a.note, ''), a.half_start, a.half_end,
	       COALESCE(TRIM(s.first_name || ' ' || s.last_name), ''), a.decision_note, COALESCE(TRIM(ap.first_name || ' ' || ap.last_name), '')
	FROM absences a
	JOIN users u ON u.id = a.user_id
	LEFT JOIN users s ON s.id = a.substitute_id
	LEFT JOIN users ap ON ap.id = a.approved_by`

func (h *Handler) loadAbsences(ctx context.Context, where string, args ...any) []absenceRow {
	rows, err := h.db.Query(ctx, absenceSelect+" WHERE "+where, args...)
	if err != nil {
		componentLog("planer").Warn().Err(err).Msg("abwesenheiten laden")
		return nil
	}
	defer rows.Close()
	var out []absenceRow
	for rows.Next() {
		var a absenceRow
		var from, to time.Time
		var days float64
		if rows.Scan(&a.ID, &a.UserID, &a.UserName, &a.Type, &a.Status, &from, &to, &days, &a.Note, &a.HalfStart, &a.HalfEnd,
			&a.Substitute, &a.DecisionNote, &a.Decider) != nil {
			continue
		}
		a.TypeLabel = absenceTypeLabels[a.Type]
		s := absenceStatusLabels[a.Status]
		a.StatusLabel, a.StatusClass = s[0], s[1]
		a.From, a.To, a.FromISO, a.ToISO = from.Format("02.01.2006"), to.Format("02.01.2006"), from.Format(dayISO), to.Format(dayISO)
		a.Days = formatQty(days)
		out = append(out, a)
	}
	return out
}

// absenceConflicts: Schichten im Zeitraum und Kollegen (gleiche Abteilung), die gleichzeitig weg sind.
func (h *Handler) absenceConflicts(ctx context.Context, a absenceRow) []string {
	var out []string
	var n int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM shift_assignments WHERE user_id = $1::uuid AND date BETWEEN $2::date AND $3::date`,
		a.UserID, a.FromISO, a.ToISO).Scan(&n)
	if n > 0 {
		out = append(out, fmt.Sprintf("%d Schicht(en) eingeteilt – werden beim Genehmigen entfernt, wenn gewünscht", n))
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT TRIM(o.first_name || ' ' || o.last_name)
		FROM absences x JOIN users o ON o.id = x.user_id JOIN users me ON me.id = $1::uuid
		WHERE x.user_id <> $1::uuid AND x.status IN ('approved', 'pending') AND x.start_date <= $3::date AND x.end_date >= $2::date
		  AND me.department_id IS NOT NULL AND o.department_id = me.department_id
		ORDER BY 1 LIMIT 8`, a.UserID, a.FromISO, a.ToISO)
	if err == nil {
		var names []string
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				names = append(names, s)
			}
		}
		rows.Close()
		if len(names) > 0 {
			out = append(out, "gleichzeitig abwesend (Abteilung): "+strings.Join(names, ", "))
		}
	}
	return out
}

func (h *Handler) absenceTab(r *http.Request) *absenceTabData {
	ctx := r.Context()
	q := r.URL.Query()
	u := getUser(r)
	now := time.Now()
	d := &absenceTabData{Year: atoiDefault(q.Get("year"), now.Year()), Today: now.Format(dayISO)}
	d.PrevYear, d.NextYear = d.Year-1, d.Year+1
	for _, k := range absenceTypeOrder {
		d.Types = append(d.Types, struct{ Key, Label string }{k, absenceTypeLabels[k]})
	}
	var myName string
	_ = h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid`, u.ID).Scan(&myName)
	d.Me = h.entitlement(ctx, u.ID, myName, d.Year)
	d.Users = h.userOptions(ctx)
	d.Mine = h.loadAbsences(ctx, `a.user_id = $1::uuid AND a.end_date >= $2 ORDER BY a.start_date DESC LIMIT 50`, u.ID,
		time.Date(d.Year, 1, 1, 0, 0, 0, 0, time.Local))
	for i := range d.Mine {
		d.Mine[i].Mine = true
	}
	approve, viewAll := h.canAbsenceApprove(r), h.canAbsenceViewAll(r)
	if approve {
		d.Pending = h.loadAbsences(ctx, `a.status = 'pending' ORDER BY a.start_date LIMIT 200`)
		for i := range d.Pending {
			d.Pending[i].Conflicts = h.absenceConflicts(ctx, d.Pending[i])
		}
		d.Recent = h.loadAbsences(ctx, `a.status <> 'pending' AND a.end_date >= CURRENT_DATE - 30 ORDER BY a.start_date DESC LIMIT 100`)
		for _, uo := range h.userOptions(ctx) {
			d.Entitlements = append(d.Entitlements, h.entitlement(ctx, uo.ID, uo.Name, d.Year))
		}
	}

	// Teamkalender (Monat)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if t, err := time.ParseInLocation("2006-01", q.Get("month"), time.Local); err == nil {
		month = t
	}
	last := month.AddDate(0, 1, -1)
	d.Month, d.MonthLabel = month.Format("2006-01"), germanMonth(month.Month())+" "+strconv.Itoa(month.Year())
	d.PrevMonth, d.NextMonth = month.AddDate(0, -1, 0).Format("2006-01"), month.AddDate(0, 1, 0).Format("2006-01")
	free := h.freeDays(ctx, month, last)
	for t := month; !t.After(last); t = t.AddDate(0, 0, 1) {
		ds := t.Format(dayISO)
		d.Days = append(d.Days, plannerDay{Date: ds, Label: t.Format("2"), Short: weekdayShort[t.Weekday()], Holiday: free[ds],
			Weekend: t.Weekday() == time.Saturday || t.Weekday() == time.Sunday, Today: ds == d.Today})
	}
	users := h.plannerUsers(ctx, q.Get("dept"), q.Get("group"))
	rowOf := map[string]int{}
	for i, pu := range users {
		rowOf[pu.ID] = i
		d.Cal = append(d.Cal, absenceCalRow{UserID: pu.ID, Name: pu.Name, Cells: make([]absenceCalCell, len(d.Days))})
	}
	rows, err := h.db.Query(ctx, `SELECT user_id::text, type::text, status::text, start_date, end_date FROM absences
		WHERE status IN ('approved', 'pending') AND start_date <= $2 AND end_date >= $1`, month, last)
	if err == nil {
		for rows.Next() {
			var uid, typ, st string
			var a, b time.Time
			if rows.Scan(&uid, &typ, &st, &a, &b) != nil {
				continue
			}
			i, ok := rowOf[uid]
			if !ok {
				continue
			}
			label, cls := "abwesend", "ab-other"
			if viewAll || uid == u.ID {
				label, cls = absenceTypeLabels[typ], "ab-"+typ
			}
			short := string([]rune(label)[0])
			if st == "pending" {
				cls += " ab-pending"
				label += " (beantragt)"
			}
			for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
				if t.Before(month) || t.After(last) {
					continue
				}
				j := t.Day() - 1
				d.Cal[i].Cells[j] = absenceCalCell{Class: cls, Title: label, Short: short}
			}
		}
		rows.Close()
	}
	// nur Zeilen mit Einträgen, außer man filtert gezielt
	if q.Get("dept") == "" && q.Get("group") == "" {
		var keep []absenceCalRow
		for _, row := range d.Cal {
			for _, c := range row.Cells {
				if c.Class != "" {
					keep = append(keep, row)
					break
				}
			}
		}
		d.Cal = keep
	}
	return d
}

func germanMonth(m time.Month) string {
	return [...]string{"", "Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}[m]
}

// absenceApprovers: Vorgesetzte(r) der Person, sonst alle mit „Abwesenheiten genehmigen“.
func (h *Handler) absenceApprovers(ctx context.Context, uid string) []string {
	var mgr string
	if h.db.QueryRow(ctx, `SELECT COALESCE(manager_id::text, '') FROM users WHERE id = $1::uuid`, uid).Scan(&mgr) == nil && mgr != "" {
		return []string{mgr}
	}
	var ids []string
	rows, err := h.db.Query(ctx, `SELECT u.id::text FROM users u
		WHERE u.active AND u.id <> $1::uuid AND (
		  EXISTS (SELECT 1 FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
		          WHERE up.user_id = u.id AND p.key = 'absences.approve' AND up.granted)
		  OR (EXISTS (SELECT 1 FROM roles ro JOIN role_permissions rp ON rp.role_id = ro.id JOIN permissions p ON p.id = rp.permission_id
		              WHERE ro.key = u.role AND p.key = 'absences.approve')
		      AND NOT EXISTS (SELECT 1 FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
		                      WHERE up.user_id = u.id AND p.key = 'absences.approve' AND NOT up.granted)))
		LIMIT 20`, uid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// PlannerAbsenceCreateWeb: POST /planner/absences
func (h *Handler) PlannerAbsenceCreateWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	me := getUser(r).ID
	uid := v("user_id")
	if uid == "" {
		uid = me
	}
	back := func(msg string, err error) { plannerBack(w, r, "absences", "", msg, err) }
	forOther := uid != me
	if (forOther && !h.canAbsenceApprove(r)) || (!forOther && !h.hasPerm(r, "absences.request")) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	typ := v("type")
	if absenceTypeLabels[typ] == "" {
		back("", errors.New("Bitte die Art wählen"))
		return
	}
	from, err1 := time.ParseInLocation(dayISO, v("start_date"), time.Local)
	to, err2 := time.ParseInLocation(dayISO, v("end_date"), time.Local)
	if err1 != nil || err2 != nil || to.Before(from) {
		back("", errors.New("Zeitraum ungültig – „bis“ darf nicht vor „von“ liegen"))
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		back("", errors.New("Höchstens ein Jahr am Stück"))
		return
	}
	halfStart, halfEnd := r.FormValue("half_start") == "1", r.FormValue("half_end") == "1"
	if from.Equal(to) && halfStart && halfEnd {
		halfEnd = false
	}
	var overlap int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM absences WHERE user_id = $1::uuid AND status IN ('approved', 'pending')
		AND start_date <= $3 AND end_date >= $2`, uid, from, to).Scan(&overlap)
	if overlap > 0 {
		back("", errors.New("Im Zeitraum gibt es schon eine Abwesenheit – bitte diese ändern oder stornieren"))
		return
	}
	days := h.absenceWorkdays(ctx, from, to, halfStart, halfEnd)
	if days == 0 && typ == "vacation" {
		back("", errors.New("Im Zeitraum liegt kein Arbeitstag (Wochenende/Feiertag)"))
		return
	}
	// Krankmeldungen und Einträge durch Genehmigende gelten sofort
	status := "pending"
	if typ == "sick" || forOther {
		status = "approved"
	}
	sub := v("substitute_id")
	if !uuidInPathRe.MatchString(sub) {
		sub = ""
	}
	var id string
	err := h.db.QueryRow(ctx, `INSERT INTO absences (user_id, type, status, start_date, end_date, days, note, half_start, half_end,
			substitute_id, created_by, approved_by, decided_at)
		VALUES ($1::uuid, $2::text::absence_type, $3::text::absence_status, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid, $11::uuid,
			CASE WHEN $3::text = 'approved' THEN $11::uuid END, CASE WHEN $3::text = 'approved' THEN NOW() END)
		RETURNING id::text`, uid, typ, status, from, to, days, truncateRunes(v("note"), 1000), halfStart, halfEnd, sub, nullID(me)).Scan(&id)
	if err != nil {
		back("", err)
		return
	}
	msg := fmt.Sprintf("%s vom %s bis %s erfasst (%s Arbeitstage)", absenceTypeLabels[typ], from.Format("02.01."), to.Format("02.01.2006"), formatQty(days))
	if status == "pending" {
		msg = fmt.Sprintf("%s vom %s bis %s beantragt (%s Arbeitstage) – die Genehmigung wird benachrichtigt", absenceTypeLabels[typ], from.Format("02.01."), to.Format("02.01.2006"), formatQty(days))
		if typ == "vacation" {
			if e := h.entitlement(ctx, uid, "", from.Year()); e.Rest-e.Pending < 0 {
				msg += fmt.Sprintf(" · Achtung: Resturlaub %s Tage wird überschritten", formatQty(e.Rest-e.Pending+days))
			}
		}
		var name string
		_ = h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid`, uid).Scan(&name)
		h.systemNotify(ctx, h.absenceApprovers(ctx, uid), fmt.Sprintf("🏖️ **%s** beantragt %s vom %s bis %s (%s Arbeitstage). [Im Planer prüfen](/planner?tab=absences)",
			name, absenceTypeLabels[typ], from.Format("02.01.2006"), to.Format("02.01.2006"), formatQty(days)))
	} else if forOther {
		h.systemNotify(ctx, []string{uid}, fmt.Sprintf("📅 Für dich wurde %s vom %s bis %s eingetragen.", absenceTypeLabels[typ], from.Format("02.01.2006"), to.Format("02.01.2006")))
	}
	componentLog("planer").Info().Str("abwesenheit", id).Str("art", typ).Str("status", status).Str("fuer", uid).Str("von", me).Msg("abwesenheit erfasst")
	back(msg, nil)
}

// PlannerAbsenceDecideWeb: POST /planner/absences/{id}/decide (decision=approve|reject)
func (h *Handler) PlannerAbsenceDecideWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canAbsenceApprove(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	approve := r.FormValue("decision") == "approve"
	status := "rejected"
	if approve {
		status = "approved"
	}
	note := truncateRunes(strings.TrimSpace(r.FormValue("decision_note")), 500)
	me := getUser(r).ID
	var uid, typ string
	var from, to time.Time
	err := h.db.QueryRow(ctx, `UPDATE absences SET status = $2::text::absence_status, approved_by = $3::uuid, decided_at = NOW(), decision_note = $4
		WHERE id = $1::uuid AND status = 'pending' RETURNING user_id::text, type::text, start_date, end_date`, id, status, nullID(me), note).Scan(&uid, &typ, &from, &to)
	if err != nil {
		plannerBack(w, r, "absences", "", "", errors.New("Antrag nicht gefunden oder schon entschieden"))
		return
	}
	msg := "Antrag abgelehnt"
	if approve {
		msg = "Antrag genehmigt"
		if r.FormValue("remove_shifts") == "1" {
			if tag, err := h.db.Exec(ctx, `DELETE FROM shift_assignments WHERE user_id = $1::uuid AND date BETWEEN $2 AND $3`, uid, from, to); err == nil && tag.RowsAffected() > 0 {
				msg += fmt.Sprintf(" – %d Schicht(en) im Zeitraum entfernt", tag.RowsAffected())
			}
		}
	}
	text := fmt.Sprintf("✅ Dein Antrag (%s %s – %s) wurde **genehmigt**.", absenceTypeLabels[typ], from.Format("02.01."), to.Format("02.01.2006"))
	if !approve {
		text = fmt.Sprintf("❌ Dein Antrag (%s %s – %s) wurde **abgelehnt**.", absenceTypeLabels[typ], from.Format("02.01."), to.Format("02.01.2006"))
	}
	if note != "" {
		text += " Begründung: " + note
	}
	h.systemNotify(ctx, []string{uid}, text)
	componentLog("planer").Info().Str("abwesenheit", id).Str("status", status).Str("von", me).Msg("abwesenheit entschieden")
	plannerBack(w, r, "absences", "", msg, nil)
}

// PlannerAbsenceCancelWeb: POST /planner/absences/{id}/cancel – eigene
// beantragte (oder künftige) Abwesenheit zurückziehen; Genehmigende dürfen alle stornieren.
func (h *Handler) PlannerAbsenceCancelWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	me := getUser(r).ID
	var uid, status string
	var from time.Time
	if err := h.db.QueryRow(ctx, `SELECT user_id::text, status::text, start_date FROM absences WHERE id = $1::uuid`, id).Scan(&uid, &status, &from); err != nil {
		plannerBack(w, r, "absences", "", "", errors.New("Abwesenheit nicht gefunden"))
		return
	}
	own := uid == me && (status == "pending" || from.After(time.Now()))
	if !own && !h.canAbsenceApprove(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(ctx, `DELETE FROM absences WHERE id = $1::uuid`, id)
	if err == nil && uid != me {
		h.systemNotify(ctx, []string{uid}, fmt.Sprintf("🗑️ Deine Abwesenheit ab %s wurde storniert.", from.Format("02.01.2006")))
	}
	plannerBack(w, r, "absences", "", "Abwesenheit storniert", err)
}

// PlannerEntitlementWeb: POST /planner/entitlements – Anspruch/Übertrag eines Jahres.
func (h *Handler) PlannerEntitlementWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canAbsenceApprove(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	uid := r.FormValue("user_id")
	year := atoiDefault(r.FormValue("year"), time.Now().Year())
	days, err1 := parseQty(firstNonEmpty(r.FormValue("days"), "0"))
	carry, err2 := parseQty(firstNonEmpty(r.FormValue("carryover"), "0"))
	if !uuidInPathRe.MatchString(uid) || err1 != nil || err2 != nil || days < 0 || days > 400 || carry < -400 || carry > 400 {
		plannerBack(w, r, "absences", "&year="+strconv.Itoa(year), "", errors.New("Angaben ungültig"))
		return
	}
	_, err := h.db.Exec(r.Context(), `INSERT INTO vacation_entitlements (user_id, year, days, carryover) VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (user_id, year) DO UPDATE SET days = EXCLUDED.days, carryover = EXCLUDED.carryover`, uid, year, days, carry)
	plannerBack(w, r, "absences", "&year="+strconv.Itoa(year), "Urlaubsanspruch gespeichert", err)
}
