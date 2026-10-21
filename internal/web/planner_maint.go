package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Wartungsplanung im Planer: Monatskalender mit offenen Wartungsaufträgen
// (Fälligkeit) und Prüfpflichten der Anlagen. Zu jedem Auftrag prüft PDH, ob
// die zugewiesene Person an dem Tag abwesend ist oder keine Schicht hat, und
// zeigt je Tag, wie viele Leute im Dienst sind. Verschieben und Zuweisen mit
// maintenance.edit – die Auswahl nennt, wer an dem Tag verfügbar ist.

type maintItem struct {
	ID, Kind, Title, Infra, URL string
	Assignee, AssigneeID, Group string
	Minutes                     int
	Warn, WarnClass             string
	Overdue                     bool
	Date                        string
}

type maintDay struct {
	plannerDay
	InMonth bool
	OnDuty  int
	Minutes int
	Items   []maintItem
	Avail   []UserOption // verfügbar (im Dienst, nicht abwesend)
}

type maintTabData struct {
	Month, MonthLabel, PrevMonth, NextMonth string
	Weeks                                   [][]maintDay
	Overdue                                 []maintItem
	Users                                   []UserOption
	Warnings                                int
	Avail                                   map[string][]UserOption // Tag → verfügbar (für die Auswahl im Dialog)
}

func (h *Handler) maintTab(r *http.Request) *maintTabData {
	ctx := r.Context()
	q := r.URL.Query()
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if t, err := time.ParseInLocation("2006-01", q.Get("month"), time.Local); err == nil {
		month = t
	}
	d := &maintTabData{Month: month.Format("2006-01"), MonthLabel: germanMonth(month.Month()) + " " + strconv.Itoa(month.Year()),
		PrevMonth: month.AddDate(0, -1, 0).Format("2006-01"), NextMonth: month.AddDate(0, 1, 0).Format("2006-01"), Avail: map[string][]UserOption{}}
	first := mondayOf(month)
	last := month.AddDate(0, 1, -1)
	end := mondayOf(last).AddDate(0, 0, 6)
	free := h.freeDays(ctx, first, end)
	today := now.Format(dayISO)

	// Dienst und Abwesenheit je Tag
	onShift := map[string]map[string]bool{} // Tag → Person
	absent := map[string]map[string]string{}
	if rows, err := h.db.Query(ctx, `SELECT sa.date::text, sa.user_id::text FROM shift_assignments sa LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
		WHERE sa.date BETWEEN $1 AND $2 AND (sa.plan_id IS NULL OR sp.status = 'published')`, first, end); err == nil {
		for rows.Next() {
			var day, uid string
			if rows.Scan(&day, &uid) == nil {
				if onShift[day] == nil {
					onShift[day] = map[string]bool{}
				}
				onShift[day][uid] = true
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT user_id::text, type::text, start_date, end_date FROM absences
		WHERE status = 'approved' AND start_date <= $2 AND end_date >= $1`, first, end); err == nil {
		for rows.Next() {
			var uid, typ string
			var a, b time.Time
			if rows.Scan(&uid, &typ, &a, &b) != nil {
				continue
			}
			for t := a; !t.After(b); t = t.AddDate(0, 0, 1) {
				k := t.Format(dayISO)
				if absent[k] == nil {
					absent[k] = map[string]string{}
				}
				absent[k][uid] = absenceTypeLabels[typ]
			}
		}
		rows.Close()
	}
	hasShifts := map[string]bool{} // Person hat im Zeitraum überhaupt Schichten (sonst keine „keine Schicht“-Warnung)
	for _, m := range onShift {
		for u := range m {
			hasShifts[u] = true
		}
	}
	d.Users = h.userOptions(ctx)
	names := map[string]string{}
	for _, u := range d.Users {
		names[u.ID] = u.Name
	}

	byDay := map[string][]maintItem{}
	check := func(it *maintItem) {
		if it.AssigneeID == "" {
			if it.Group == "" {
				it.Warn, it.WarnClass = "niemand zugewiesen", "b-amber"
			}
			return
		}
		if why := absent[it.Date][it.AssigneeID]; why != "" {
			it.Warn, it.WarnClass = it.Assignee+" ist abwesend ("+why+")", "b-red"
		} else if hasShifts[it.AssigneeID] && !onShift[it.Date][it.AssigneeID] {
			it.Warn, it.WarnClass = it.Assignee+" hat keine Schicht", "b-amber"
		}
	}
	// offene Wartungsaufträge (Abteilungsrechte beachten)
	args := []any{first, end}
	scope, sargs := scopeSQL(h.requestScope(r), "maintenance_task", "t", len(args))
	args = append(args, sargs...)
	rows, err := h.db.Query(ctx, `
		SELECT t.id::text, t.title, COALESCE(i.name, ''), COALESCE(t.assigned_to::text, ''), COALESCE(g.name, ''),
		       COALESCE(p.estimated_min, 0), t.due_date
		FROM maintenance_tasks t
		LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
		LEFT JOIN user_groups g ON g.id = t.assigned_group_id
		LEFT JOIN maintenance_plans p ON p.id = t.plan_id
		WHERE t.status::text IN ('open', 'in_progress', 'pending') AND t.archived_at IS NULL
		  AND (t.due_date::date BETWEEN $1 AND $2 OR t.due_date::date < CURRENT_DATE)`+scope+`
		ORDER BY t.due_date, t.title LIMIT 1000`, args...)
	if err == nil {
		for rows.Next() {
			var it maintItem
			var due time.Time
			if rows.Scan(&it.ID, &it.Title, &it.Infra, &it.AssigneeID, &it.Group, &it.Minutes, &due) != nil {
				continue
			}
			it.Kind, it.URL = "maintenance", "/maintenance/tasks/"+it.ID
			it.Date = due.Local().Format(dayISO)
			it.Assignee = names[it.AssigneeID]
			check(&it)
			if it.Date < today {
				it.Overdue = true
				d.Overdue = append(d.Overdue, it)
				continue
			}
			byDay[it.Date] = append(byDay[it.Date], it)
		}
		rows.Close()
	} else {
		componentLog("planer").Warn().Err(err).Msg("wartungskalender")
	}
	// Prüfpflichten der Anlagen
	if h.hasPerm(r, "infrastructure.view") || h.hasPerm(r, "infrastructure.obligations") {
		if rows, err := h.db.Query(ctx, `SELECT o.infrastructure_id::text, o.title, COALESCE(i.name, ''), COALESCE(o.responsible_to::text, ''), o.next_due
			FROM infra_obligations o JOIN infrastructure i ON i.id = o.infrastructure_id
			WHERE o.next_due BETWEEN $1 AND $2 ORDER BY o.next_due`, first, end); err == nil {
			for rows.Next() {
				var it maintItem
				var due time.Time
				var infraID string
				if rows.Scan(&infraID, &it.Title, &it.Infra, &it.AssigneeID, &due) != nil {
					continue
				}
				it.Kind, it.URL, it.Date = "obligation", "/infrastructure/"+infraID+"?tab=checks", due.Format(dayISO)
				it.Assignee = names[it.AssigneeID]
				if it.AssigneeID != "" {
					check(&it)
				}
				byDay[it.Date] = append(byDay[it.Date], it)
			}
			rows.Close()
		}
	}
	for t := first; !t.After(end); t = t.AddDate(0, 0, 7) {
		var week []maintDay
		for i := 0; i < 7; i++ {
			day := t.AddDate(0, 0, i)
			ds := day.Format(dayISO)
			md := maintDay{plannerDay: plannerDay{Date: ds, Label: day.Format("2"), Short: weekdayShort[day.Weekday()], Holiday: free[ds],
				Weekend: day.Weekday() == time.Saturday || day.Weekday() == time.Sunday, Today: ds == today},
				InMonth: day.Month() == month.Month(), Items: byDay[ds]}
			for uid := range onShift[ds] {
				if absent[ds][uid] == "" {
					md.OnDuty++
					md.Avail = append(md.Avail, UserOption{ID: uid, Name: names[uid]})
				}
			}
			for _, it := range md.Items {
				md.Minutes += it.Minutes
				if it.Warn != "" {
					d.Warnings++
				}
			}
			d.Avail[ds] = md.Avail
			week = append(week, md)
		}
		d.Weeks = append(d.Weeks, week)
	}
	return d
}

// Hours: geplante Arbeitszeit eines Tages
func (m maintDay) Hours() string {
	if m.Minutes == 0 {
		return ""
	}
	return strings.Replace(strconv.FormatFloat(float64(m.Minutes)/60, 'f', 1, 64), ".", ",", 1) + " h"
}

// PlannerMaintSaveWeb: POST /planner/maintenance/{id} – Fälligkeit verschieben
// und/oder zuweisen (assigned_to leer = unverändert, "-" = niemand).
func (h *Handler) PlannerMaintSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, "maintenance.edit") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	if !uuidInPathRe.MatchString(id) || !h.recordInScope(ctx, h.requestScope(r), "maintenance_task", id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	month := r.FormValue("month")
	back := func(msg string, err error) { plannerBack(w, r, "maintenance", "&month="+month, msg, err) }
	due, err := time.ParseInLocation(dayISO, r.FormValue("due_date"), time.Local)
	if err != nil {
		back("", errors.New("Datum ungültig"))
		return
	}
	assignee := r.FormValue("assigned_to")
	var title, oldDue string
	if err := h.db.QueryRow(ctx, `SELECT title, to_char(due_date, 'DD.MM.YYYY') FROM maintenance_tasks WHERE id = $1::uuid`, id).Scan(&title, &oldDue); err != nil {
		back("", errors.New("Wartungsauftrag nicht gefunden"))
		return
	}
	switch {
	case assignee == "":
		_, err = h.db.Exec(ctx, `UPDATE maintenance_tasks SET due_date = $2 WHERE id = $1::uuid`, id, due)
	case assignee == "-":
		_, err = h.db.Exec(ctx, `UPDATE maintenance_tasks SET due_date = $2, assigned_to = NULL WHERE id = $1::uuid`, id, due)
	case uuidInPathRe.MatchString(assignee):
		_, err = h.db.Exec(ctx, `UPDATE maintenance_tasks SET due_date = $2, assigned_to = $3::uuid WHERE id = $1::uuid`, id, due, assignee)
	default:
		err = errors.New("Mitarbeiter ungültig")
	}
	if err != nil {
		back("", err)
		return
	}
	uid := getUser(r).ID
	if newDue := due.Format("02.01.2006"); newDue != oldDue {
		h.addHistory(ctx, "maintenance_task", id, "update", "Fälligkeit", oldDue, newDue, "Im Planer verschoben", uid)
	}
	if uuidInPathRe.MatchString(assignee) {
		h.addHistory(ctx, "maintenance_task", id, "update", "Zugewiesen", "", assignee, "Im Planer zugewiesen", uid)
		if assignee != uid {
			h.systemNotify(ctx, []string{assignee}, fmt.Sprintf("🔧 Dir wurde die Wartung **%s** am %s zugewiesen. [Öffnen](/maintenance/tasks/%s)", title, due.Format("02.01.2006"), id))
		}
	}
	back(fmt.Sprintf("„%s“ auf %s geplant", title, due.Format("02.01.2006")), nil)
}
