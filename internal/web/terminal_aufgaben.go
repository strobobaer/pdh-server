package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Aufgaben- und Projektfolien des Infobildschirms (Migration 210).
//
//   - tasks: offene Aufgaben mit Fälligkeit, vom Beginn dieser Woche bis zu einem Ende (Ende dieser Woche, 2/4/8/12
//     Wochen oder festes Datum), nach Woche und Tag gruppiert. Überfällige offene Aufgaben (Fälligkeit vor heute)
//     stehen oben und sind als „überfällig“ gekennzeichnet.
//   - projects_year: Jahresband (Monatsspalten) mit je einem Balken je Projekt von Start bis Ende, Farbe nach Status,
//     Linie für heute. Höchstens projYearPageSize Balken je Seite; die Folie blättert innerhalb ihrer Anzeigedauer.
//
// Die Abteilung eines Datensatzes ist die seiner Anlage (infrastructure_department); ohne Anlage zählt er als „ohne
// Abteilung“ und erscheint nur bei einem globalen Recht (planSlideScope). Wie bei den Planungsfolien gilt die
// Schnittmenge aus der Auswahl der Folie und den Abteilungen des Terminals, und sichtbar ist, wofür der Terminal-
// Benutzer in der Abteilung tasks.view bzw. projects.view hat. Die Folien stehen nicht in CACHEABLE_KINDS und
// entfallen ohne Verbindung.

const (
	slideKindTasks        = "tasks"
	slideKindProjectsYear = "projects_year"

	taskRangeWeek = "week" // bis Ende dieser Woche
	taskRange2W   = "2w"   // diese Woche und die folgende (bis Ende der nächsten Woche)
	taskRange4W   = "4w"
	taskRange8W   = "8w"
	taskRange12W  = "12w"
	taskRangeDate = "date" // bis zu einem festen Datum (task_until)

	yearModeCurrent = "current"
	yearModeNext    = "next"
	yearModeRolling = "rolling" // ab heute 12 Monate

	taskMaxOverdue   = 30  // überfällige Aufgaben auf der Folie, der Rest als „+ n weitere“
	taskMaxItems     = 60  // Aufgaben im Zeitraum auf der Folie
	projYearPageSize = 14  // Balken je Seite
	projYearMaxBars  = 140 // Balken insgesamt (10 Seiten)
	projYearMinPage  = 4   // Sekunden je Seite, mindestens
	projFetchLimit   = 500 // Obergrenze der gelesenen Projekte
)

func slideIsWork(kind string) bool { return kind == slideKindTasks || kind == slideKindProjectsYear }

func validTaskRange(r string) bool {
	switch r {
	case taskRangeWeek, taskRange2W, taskRange4W, taskRange8W, taskRange12W, taskRangeDate:
		return true
	}
	return false
}

func validYearMode(m string) bool {
	return m == yearModeCurrent || m == yearModeNext || m == yearModeRolling
}

// parseSlideDate: JJJJ-MM-TT in Ortszeit (0 Uhr); Jahre 2000 bis 2100.
func parseSlideDate(s string) (time.Time, bool) {
	d, err := time.ParseInLocation(dayISO, s, time.Local)
	if err != nil || d.Year() < 2000 || d.Year() > 2100 {
		return time.Time{}, false
	}
	return d, true
}

// validateWorkSlide prüft und bereinigt die Felder der Aufgaben- und Projektfolie; bei anderen Arten werden sie auf
// die Vorgabe gesetzt (Migration 210).
func validateWorkSlide(s *terminalSlide) error {
	if s.Kind == slideKindTasks {
		if s.TaskRange == "" {
			s.TaskRange = taskRangeWeek
		}
		if !validTaskRange(s.TaskRange) {
			return errors.New("Ungültiger Zeitraum")
		}
		if s.TaskRange == taskRangeDate {
			if _, ok := parseSlideDate(strings.TrimSpace(s.TaskUntil)); !ok {
				return errors.New("Bitte ein gültiges Enddatum angeben (TT.MM.JJJJ)")
			}
			s.TaskUntil = strings.TrimSpace(s.TaskUntil)
		} else {
			s.TaskUntil = ""
		}
	} else {
		s.TaskRange, s.TaskUntil = taskRangeWeek, ""
	}
	if s.Kind == slideKindProjectsYear {
		if s.YearMode == "" {
			s.YearMode = yearModeCurrent
		}
		if !validYearMode(s.YearMode) {
			return errors.New("Ungültiger Jahreszeitraum")
		}
	} else {
		s.YearMode, s.OnlyActive = yearModeCurrent, false
	}
	return nil
}

// ── Aufgaben ──────────────────────────────────────────────────────────────────────────────────────────────────────

func taskRangeWeeks(r string) int {
	switch r {
	case taskRange2W:
		return 2
	case taskRange4W:
		return 4
	case taskRange8W:
		return 8
	case taskRange12W:
		return 12
	}
	return 1
}

// taskRangeEnd: letzter Fälligkeitstag (einschließlich). Wochen enden sonntags; „date“ nimmt das feste Datum (ein
// unbrauchbares Datum fällt auf das Ende dieser Woche zurück).
func taskRangeEnd(now time.Time, rng, until string) time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if rng == taskRangeDate {
		if d, ok := parseSlideDate(until); ok {
			return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
		}
		rng = taskRangeWeek
	}
	return mondayOf(today).AddDate(0, 0, 7*taskRangeWeeks(rng)-1)
}

func taskRangeLabel(now time.Time, rng, until string) string {
	end := taskRangeEnd(now, rng, until)
	if rng == taskRangeDate {
		if _, ok := parseSlideDate(until); ok {
			return "bis " + end.Format("02.01.2006")
		}
		rng = taskRangeWeek
	}
	if rng == taskRangeWeek || !validTaskRange(rng) {
		return "diese Woche"
	}
	_, kw := end.ISOWeek()
	return fmt.Sprintf("bis Ende KW %d", kw)
}

type taskSlideItem struct {
	Title       string   `json:"title"`
	Plant       string   `json:"plant,omitempty"`
	Assignees   []string `json:"assignees"`
	Due         string   `json:"due"` // TT.MM.
	Status      string   `json:"status"`
	StatusLabel string   `json:"status_label"`
	Overdue     bool     `json:"overdue,omitempty"`
	Color       string   `json:"color,omitempty"`
}

type taskSlideDay struct {
	Date  string          `json:"date"`
	Label string          `json:"label"`
	Today bool            `json:"today,omitempty"`
	Items []taskSlideItem `json:"items"`
}

type taskSlideWeek struct {
	Label string         `json:"label"`
	Days  []taskSlideDay `json:"days"`
}

type tasksSlideData struct {
	Label       string          `json:"label"`
	From        string          `json:"from"`
	To          string          `json:"to"`
	Overdue     []taskSlideItem `json:"overdue"`
	OverdueMore int             `json:"overdue_more,omitempty"`
	Weeks       []taskSlideWeek `json:"weeks"`
	More        int             `json:"more,omitempty"`
}

type taskRow struct {
	Due  time.Time
	Item taskSlideItem
}

// groupTaskRows teilt die Aufgaben (nach Fälligkeit sortiert) in überfällige (vor heute) und Woche/Tag-Gruppen und
// begrenzt die Anzahl. Aufgaben nach to gehören nicht in den Zeitraum.
func groupTaskRows(rows []taskRow, today, to time.Time) *tasksSlideData {
	out := &tasksSlideData{Overdue: []taskSlideItem{}, Weeks: []taskSlideWeek{}}
	var curWeek *taskSlideWeek
	var curDay *taskSlideDay
	shown := 0
	for _, r := range rows {
		d := time.Date(r.Due.Year(), r.Due.Month(), r.Due.Day(), 0, 0, 0, 0, today.Location())
		it := r.Item
		it.Due = d.Format("02.01.")
		if d.Before(today) {
			it.Overdue = true
			if len(out.Overdue) < taskMaxOverdue {
				out.Overdue = append(out.Overdue, it)
			} else {
				out.OverdueMore++
			}
			continue
		}
		if d.After(to) {
			continue
		}
		if shown >= taskMaxItems {
			out.More++
			continue
		}
		shown++
		mon := mondayOf(d)
		_, kw := d.ISOWeek()
		wl := fmt.Sprintf("KW %d · %s–%s", kw, mon.Format("02.01."), mon.AddDate(0, 0, 6).Format("02.01."))
		if curWeek == nil || curWeek.Label != wl {
			out.Weeks = append(out.Weeks, taskSlideWeek{Label: wl, Days: []taskSlideDay{}})
			curWeek, curDay = &out.Weeks[len(out.Weeks)-1], nil
		}
		k := d.Format(dayISO)
		if curDay == nil || curDay.Date != k {
			curWeek.Days = append(curWeek.Days, taskSlideDay{Date: k, Label: shortWeekday("de", d.Weekday()) + " " + d.Format("02.01."), Today: d.Equal(today), Items: []taskSlideItem{}})
			curDay = &curWeek.Days[len(curWeek.Days)-1]
		}
		curDay.Items = append(curDay.Items, it)
	}
	return out
}

// tasksSlide baut den Inhalt einer Aufgabenfolie für den Terminal-Benutzer uid.
func (h *Handler) tasksSlide(ctx context.Context, uid, role string, s terminalSlide, now time.Time) (*tasksSlideData, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := taskRangeEnd(now, s.TaskRange, s.TaskUntil)
	sc, err := h.planSlideScope(ctx, uid, role, "tasks.view", s.DepartmentIDs)
	if err != nil {
		return nil, err
	}
	var rows []taskRow
	var overdueTotal, rangeTotal int
	if !sc.empty() {
		if rows, overdueTotal, rangeTotal, err = h.taskRows(ctx, today, to, sc); err != nil {
			return nil, err
		}
	}
	out := groupTaskRows(rows, today, to)
	// „+ n weitere“ aus den Zählungen der Abfragen, nicht aus der Zahl der gelesenen Zeilen
	out.OverdueMore = max(out.OverdueMore, overdueTotal-len(out.Overdue))
	out.More = max(out.More, rangeTotal-taskShown(out))
	out.Label = taskRangeLabel(now, s.TaskRange, s.TaskUntil)
	out.From, out.To = mondayOf(today).Format(dayISO), to.Format(dayISO)
	return out, nil
}

// taskShown: Zahl der Aufgaben in den Wochen einer Folie.
func taskShown(d *tasksSlideData) int {
	n := 0
	for _, w := range d.Weeks {
		for _, day := range w.Days {
			n += len(day.Items)
		}
	}
	return n
}

// taskRows: offene Aufgaben mit Fälligkeit in Anlagen der erlaubten Abteilungen bzw. – mit globalem Recht – ohne
// Anlage oder Abteilung. Überfällige (vor heute) und Aufgaben im Zeitraum (heute bis to) haben je eigene Grenzen
// und Zählungen, damit viele überfällige Aufgaben die des Zeitraums nicht verdrängen.
func (h *Handler) taskRows(ctx context.Context, today, to time.Time, sc planScope) (rows []taskRow, overdueTotal, rangeTotal int, err error) {
	depts := sc.Depts
	if depts == nil {
		depts = []string{}
	}
	const q = `SELECT t.due_date::text, t.title, t.status::text, COALESCE(t.color, ''), COALESCE(i.name, ''),
			COALESCE((SELECT array_agg(TRIM(u.first_name || ' ' || u.last_name) ORDER BY lower(u.last_name), lower(u.first_name))
			          FROM task_assignees ta JOIN users u ON u.id = ta.user_id WHERE ta.task_id = t.id), '{}')::text[],
			COUNT(*) OVER ()
		FROM tasks t LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
		WHERE t.status::text IN ('open', 'in_progress', 'pending') AND t.archived_at IS NULL
		  AND t.due_date IS NOT NULL AND t.due_date >= $1::date AND t.due_date <= $2::date
		  AND (EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = t.infrastructure_id AND x.department_id = ANY($3::uuid[]))
		       OR ($4::boolean AND NOT EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = t.infrastructure_id)))
		ORDER BY t.due_date, lower(t.title)
		LIMIT $5`
	scan := func(lo, hi time.Time, limit int) (int, error) {
		r, err := h.db.Query(ctx, q, lo, hi, depts, sc.NoDept, limit)
		if err != nil {
			return 0, err
		}
		defer r.Close()
		total := 0
		for r.Next() {
			var due, title, status, color, plant string
			var who []string
			if err := r.Scan(&due, &title, &status, &color, &plant, &who, &total); err != nil {
				return 0, err
			}
			d, err := time.ParseInLocation(dayISO, due, time.Local)
			if err != nil {
				continue
			}
			if !hexColorRe.MatchString(color) {
				color = ""
			}
			names := []string{}
			for _, n := range who {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			rows = append(rows, taskRow{Due: d, Item: taskSlideItem{Title: title, Plant: plant, Assignees: names, Status: status, StatusLabel: statusLabel(status), Color: color}})
		}
		return total, r.Err()
	}
	if overdueTotal, err = scan(time.Date(1970, 1, 1, 0, 0, 0, 0, today.Location()), today.AddDate(0, 0, -1), taskMaxOverdue); err != nil {
		return nil, 0, 0, err
	}
	if !to.Before(today) {
		if rangeTotal, err = scan(today, to, taskMaxItems); err != nil {
			return nil, 0, 0, err
		}
	}
	return rows, overdueTotal, rangeTotal, nil
}

// ── Projekt-Jahresband ────────────────────────────────────────────────────────────────────────────────────────────

var monthShortDE = [...]string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"}

// projectStatusColors: feste Farben je Projektstatus (Projekte haben keine eigene Farbe).
var projectStatusColors = map[string]string{
	"planning": "#8a94a6", "active": "#2f6fde", "paused": "#e08a00", "completed": "#2e9e5b",
}

func projectBarColor(status string) string {
	if c, ok := projectStatusColors[status]; ok {
		return c
	}
	return "#8a94a6"
}

// yearBandBounds: erster und letzter Tag des Bandes. current/next = Kalenderjahr, rolling = ab heute 12 Monate
// (kann zwei Kalenderjahre überspannen).
func yearBandBounds(now time.Time, mode string) (time.Time, time.Time) {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch mode {
	case yearModeNext:
		return time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, loc), time.Date(now.Year()+1, 12, 31, 0, 0, 0, 0, loc)
	case yearModeRolling:
		return today, today.AddDate(1, 0, 0).AddDate(0, 0, -1)
	}
	return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc), time.Date(now.Year(), 12, 31, 0, 0, 0, 0, loc)
}

func yearModeLabel(now time.Time, mode string) string {
	from, to := yearBandBounds(now, mode)
	if mode == yearModeRolling {
		return from.Format("02.01.2006") + " – " + to.Format("02.01.2006")
	}
	return fmt.Sprint(from.Year())
}

// dayDiff: ganze Kalendertage von a nach b (unabhängig von Sommerzeit).
func dayDiff(a, b time.Time) int {
	ua := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	ub := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

type projMonth struct {
	Label string  `json:"label"`
	L     float64 `json:"l"` // linker Rand in % des Bandes
	W     float64 `json:"w"`
}

type projBar struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	StatusLabel string  `json:"status_label"`
	Color       string  `json:"color"`
	Start       string  `json:"start"` // TT.MM.JJJJ
	End         string  `json:"end"`
	L           float64 `json:"l"` // linker Rand in % des Bandes
	W           float64 `json:"w"`
	ClipL       bool    `json:"clip_l,omitempty"` // beginnt vor dem Band
	ClipR       bool    `json:"clip_r,omitempty"` // endet nach dem Band
}

type projYearPage struct {
	Bars []projBar `json:"bars"`
}

type projLegend struct {
	Label string `json:"label"`
	Color string `json:"color"`
}

type projectsYearData struct {
	Label       string         `json:"label"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	Months      []projMonth    `json:"months"`
	Today       *float64       `json:"today,omitempty"` // Lage der Heute-Linie in %, fehlt außerhalb des Bandes
	Total       int            `json:"total"`
	Seconds     int            `json:"seconds"` // tatsächliche Anzeigedauer der Folie
	PageSeconds int            `json:"page_seconds"`
	More        int            `json:"more,omitempty"` // Projekte über projYearMaxBars hinaus
	Legend      []projLegend   `json:"legend"`
	Pages       []projYearPage `json:"pages"`
}

// yearBandMonths: die Monatsspalten des Bandes (der erste und der letzte Monat können angeschnitten sein).
func yearBandMonths(from, to time.Time) []projMonth {
	total := float64(dayDiff(from, to) + 1)
	var out []projMonth
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, from.Location()); !m.After(to); m = m.AddDate(0, 1, 0) {
		a, b := m, m.AddDate(0, 1, -1)
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		label := monthShortDE[m.Month()-1]
		if len(out) == 0 || m.Month() == time.January {
			label += fmt.Sprintf(" %d", m.Year())
		}
		out = append(out, projMonth{Label: label, L: float64(dayDiff(from, a)) / total * 100, W: float64(dayDiff(a, b)+1) / total * 100})
	}
	return out
}

// projBarFor: der Balken eines Projekts im Band; ok = false, wenn das Projekt das Band nicht berührt. Fehlt ein Ende,
// zählt das andere für beide; ein Ende vor dem Start wird getauscht. Sehr kurze Balken bekommen eine Mindestbreite.
func projBarFor(name, status string, start, end, from, to time.Time) (projBar, bool) {
	if end.Before(start) {
		start, end = end, start
	}
	if end.Before(from) || start.After(to) {
		return projBar{}, false
	}
	b := projBar{Name: name, Status: status, StatusLabel: projectStatusLabel(status), Color: projectBarColor(status),
		Start: start.Format("02.01.2006"), End: end.Format("02.01.2006")}
	cs, ce := start, end
	if cs.Before(from) {
		cs, b.ClipL = from, true
	}
	if ce.After(to) {
		ce, b.ClipR = to, true
	}
	total := float64(dayDiff(from, to) + 1)
	b.L = float64(dayDiff(from, cs)) / total * 100
	b.W = float64(dayDiff(cs, ce)+1) / total * 100
	if b.W < 0.8 {
		b.W = 0.8
	}
	if b.L+b.W > 100 {
		b.L = 100 - b.W
	}
	return b, true
}

// pageProjectBars: je size Balken eine Seite (letzte Seite kürzer). Ohne Balken keine Seite.
func pageProjectBars(bars []projBar, size int) []projYearPage {
	if size < 1 {
		size = projYearPageSize
	}
	pages := []projYearPage{}
	for i := 0; i < len(bars); i += size {
		j := min(i+size, len(bars))
		pages = append(pages, projYearPage{Bars: bars[i:j:j]})
	}
	return pages
}

// projEffectiveSeconds: Anzeigedauer der Projektfolie. Die Seitenzahl hängt von den Daten ab und lässt sich beim
// Speichern nicht prüfen; deshalb verlängert sich die Anzeige auf mindestens projYearMinPage Sekunden je Seite
// (höchstens bis zur Obergrenze der Foliendauer), sodass jede Seite zu sehen ist.
func projEffectiveSeconds(duration, pages int) int {
	return min(max(duration, pages*projYearMinPage), slideMaxSeconds)
}

// projPageSeconds: Sekunden je Seite bei der tatsächlichen Anzeigedauer, mindestens projYearMinPage.
func projPageSeconds(duration, pages int) int {
	if pages < 1 {
		return duration
	}
	return max(projYearMinPage, projEffectiveSeconds(duration, pages)/pages)
}

// projectsYearSlide baut den Inhalt einer Projektfolie für den Terminal-Benutzer uid.
func (h *Handler) projectsYearSlide(ctx context.Context, uid, role string, s terminalSlide, now time.Time) (*projectsYearData, error) {
	from, to := yearBandBounds(now, s.YearMode)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	out := &projectsYearData{Label: yearModeLabel(now, s.YearMode), From: from.Format(dayISO), To: to.Format(dayISO),
		Months: yearBandMonths(from, to), Legend: []projLegend{}, Pages: []projYearPage{}}
	if !today.Before(from) && !today.After(to) {
		v := (float64(dayDiff(from, today)) + 0.5) / float64(dayDiff(from, to)+1) * 100
		out.Today = &v
	}
	sc, err := h.planSlideScope(ctx, uid, role, permProjectsView, s.DepartmentIDs)
	if err != nil {
		return nil, err
	}
	var bars []projBar
	if !sc.empty() {
		if bars, out.Total, err = h.projectBars(ctx, from, to, s.OnlyActive, sc); err != nil {
			return nil, err
		}
	}
	out.More = out.Total - len(bars)
	seen := map[string]bool{}
	for _, st := range []string{"planning", "active", "paused", "completed"} {
		for _, b := range bars {
			if b.Status == st && !seen[st] {
				seen[st] = true
				out.Legend = append(out.Legend, projLegend{Label: b.StatusLabel, Color: b.Color})
			}
		}
	}
	out.Pages = pageProjectBars(bars, projYearPageSize)
	out.Seconds = projEffectiveSeconds(s.DurationSeconds, len(out.Pages))
	out.PageSeconds = projPageSeconds(s.DurationSeconds, len(out.Pages))
	return out, nil
}

// projectBars: die Projekte, die das Band berühren, nach Start sortiert; total zählt auch die über projYearMaxBars.
func (h *Handler) projectBars(ctx context.Context, from, to time.Time, onlyActive bool, sc planScope) ([]projBar, int, error) {
	depts := sc.Depts
	if depts == nil {
		depts = []string{}
	}
	rows, err := h.db.Query(ctx, `SELECT p.name, p.status::text, COALESCE(p.start_date, p.end_date)::text, COALESCE(p.end_date, p.start_date)::text
		FROM projects p
		WHERE (p.start_date IS NOT NULL OR p.end_date IS NOT NULL)
		  AND LEAST(COALESCE(p.start_date, p.end_date), COALESCE(p.end_date, p.start_date)) <= $2::date
		  AND GREATEST(COALESCE(p.start_date, p.end_date), COALESCE(p.end_date, p.start_date)) >= $1::date
		  AND (NOT $5::boolean OR p.status = 'active')
		  AND (EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = p.infrastructure_id AND x.department_id = ANY($3::uuid[]))
		       OR ($4::boolean AND NOT EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = p.infrastructure_id)))
		ORDER BY LEAST(COALESCE(p.start_date, p.end_date), COALESCE(p.end_date, p.start_date)), lower(p.name)
		LIMIT $6`, from, to, depts, sc.NoDept, onlyActive, projFetchLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var bars []projBar
	total := 0
	for rows.Next() {
		var name, status, a, b string
		if err := rows.Scan(&name, &status, &a, &b); err != nil {
			return nil, 0, err
		}
		sd, err1 := time.ParseInLocation(dayISO, a, time.Local)
		ed, err2 := time.ParseInLocation(dayISO, b, time.Local)
		if err1 != nil || err2 != nil {
			continue
		}
		bar, ok := projBarFor(name, status, sd, ed, from, to)
		if !ok {
			continue
		}
		total++
		if len(bars) < projYearMaxBars {
			bars = append(bars, bar)
		}
	}
	return bars, total, rows.Err()
}
