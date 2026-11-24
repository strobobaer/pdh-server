package web

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata" // Europe/Berlin auch ohne Zonendatenbank des Systems (Windows)
)

// Arbeitszeiten der Anlagen: je Wochentag Schichten mit Pausen, Ausnahmen je Datum, Vererbung nach unten
// (eine Anlage ohne eigene Zeiten nutzt die der nächsten übergeordneten Anlage mit eigenen Zeiten).
// Auswertungen nutzen plantWorkIntervals (geplante Arbeitszeit ohne Pausen) und plannedWorkMinutes;
// gerechnet wird in Ortszeit Europe/Berlin, die Sommerzeitumstellung stimmt, weil jede Uhrzeit erst als
// Wanduhrzeit des Kalendertags in einen Zeitpunkt umgesetzt wird.

// workLocation ist die Ortszeit der Arbeitszeiten.
var workLocation = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Berlin"); err == nil {
		return l
	}
	return time.Local
}()

// workInterval: ein zusammenhängender Zeitraum geplanter Arbeitszeit.
type workInterval struct {
	Start, End time.Time
}

func (w workInterval) Minutes() float64 { return w.End.Sub(w.Start).Minutes() }

// plannedWorkMinutes: Summe der Minuten geplanter Arbeitszeit.
func plannedWorkMinutes(iv []workInterval) float64 {
	var m float64
	for _, w := range iv {
		m += w.Minutes()
	}
	return m
}

// workOverlapMinutes: Minuten des Zeitraums [from, to], die in geplanter Arbeitszeit liegen.
func workOverlapMinutes(iv []workInterval, from, to time.Time) float64 {
	var m float64
	for _, w := range iv {
		s, e := w.Start, w.End
		if from.After(s) {
			s = from
		}
		if to.Before(e) {
			e = to
		}
		if e.After(s) {
			m += e.Sub(s).Minutes()
		}
	}
	return m
}

// ── Plan im Speicher ─────────────────────────────────────────────────────

type workBreak struct {
	ID       string
	StartMin int // Minuten seit Mitternacht
	EndMin   int
}

type workShift struct {
	ID       string
	Name     string
	Weekday  int // 1 = Montag … 7 = Sonntag; 0 bei Schichten einer Ausnahme
	StartMin int
	EndMin   int // <= StartMin: endet am Folgetag
	Breaks   []workBreak
}

type workException struct {
	ID     string
	Day    string // JJJJ-MM-TT
	Note   string
	Closed bool
	Shifts []workShift
}

// workPlan: die wirksamen Arbeitszeiten einer Anlage.
type workPlan struct {
	Defined    bool   // irgendwo (an der Anlage oder darüber) gibt es Zeiten
	SourceID   string // Anlage, an der die Zeiten hängen
	SourceName string
	Own        bool // die Anlage selbst hat eigene Zeiten
	Shifts     []workShift
	Exceptions []workException
}

func fmtMin(m int) string { return fmt.Sprintf("%02d:%02d", m/60%24, m%60) }

var workHMRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// parseHM liest „HH:MM“ (auch „H:MM“) als Minuten seit Mitternacht.
func parseHM(s string) (int, bool) {
	m := workHMRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	return h*60 + mi, true
}

// shiftLen: Länge der Schicht in Minuten (Ende vor oder gleich Beginn = Folgetag).
func shiftLen(start, end int) int { return ((end-start)%1440 + 1440) % 1440 }

// validateShiftTimes prüft Beginn und Ende (nicht gleich).
func validateShiftTimes(start, end int) error {
	if start == end {
		return errors.New("Beginn und Ende der Schicht dürfen nicht gleich sein.")
	}
	return nil
}

// validateBreak prüft, dass die Pause vollständig in der Schicht liegt und Beginn ≠ Ende ist.
func validateBreak(shiftStart, shiftEnd, bs, be int) error {
	if bs == be {
		return errors.New("Beginn und Ende der Pause dürfen nicht gleich sein.")
	}
	off := ((bs-shiftStart)%1440 + 1440) % 1440
	if off+shiftLen(bs, be) > shiftLen(shiftStart, shiftEnd) {
		return errors.New("Die Pause muss innerhalb der Schicht liegen.")
	}
	return nil
}

// workAt: Wanduhrzeit min (Minuten seit Mitternacht) am Kalendertag y-m-d (+ dayOff Tage) in Ortszeit.
func workAt(y int, m time.Month, d, dayOff, min int, loc *time.Location) time.Time {
	return time.Date(y, m, d+dayOff, min/60, min%60, 0, 0, loc)
}

// segments: Arbeitszeit einer Schicht, die am Kalendertag y-m-d beginnt, ohne Pausen.
func (s workShift) segments(y int, m time.Month, d int, loc *time.Location) []workInterval {
	endOff := 0
	if s.EndMin <= s.StartMin {
		endOff = 1
	}
	start := workAt(y, m, d, 0, s.StartMin, loc)
	end := workAt(y, m, d, endOff, s.EndMin, loc)
	if !end.After(start) {
		return nil
	}
	segs := []workInterval{{start, end}}
	for _, b := range s.Breaks {
		bOff := 0
		if endOff == 1 && b.StartMin < s.StartMin {
			bOff = 1 // die Pause liegt nach Mitternacht
		}
		bEndOff := bOff
		if b.EndMin <= b.StartMin {
			bEndOff++
		}
		bs := workAt(y, m, d, bOff, b.StartMin, loc)
		be := workAt(y, m, d, bEndOff, b.EndMin, loc)
		if !be.After(bs) {
			continue
		}
		var next []workInterval
		for _, w := range segs {
			if !be.After(w.Start) || !bs.Before(w.End) {
				next = append(next, w)
				continue
			}
			if bs.After(w.Start) {
				next = append(next, workInterval{w.Start, bs})
			}
			if be.Before(w.End) {
				next = append(next, workInterval{be, w.End})
			}
		}
		segs = next
	}
	return segs
}

// shiftsFor: die Schichten, die am Kalendertag beginnen (eine Ausnahme ersetzt den Wochentag).
func (p *workPlan) shiftsFor(y int, m time.Month, d int, loc *time.Location) []workShift {
	day := time.Date(y, m, d, 12, 0, 0, 0, loc)
	key := day.Format("2006-01-02")
	for _, e := range p.Exceptions {
		if e.Day == key {
			if e.Closed {
				return nil
			}
			return e.Shifts
		}
	}
	wd := int(day.Weekday())
	if wd == 0 {
		wd = 7
	}
	var out []workShift
	for _, s := range p.Shifts {
		if s.Weekday == wd {
			out = append(out, s)
		}
	}
	return out
}

// closedDay: der Kalendertag ist als Ausnahme „geschlossen“ eingetragen.
func (p *workPlan) closedDay(y int, m time.Month, d int, loc *time.Location) bool {
	key := time.Date(y, m, d, 12, 0, 0, 0, loc).Format("2006-01-02")
	for _, e := range p.Exceptions {
		if e.Day == key {
			return e.Closed
		}
	}
	return false
}

// breakRanges: die Pausen als zusammengefasste Bereiche in Minuten ab Schichtbeginn (auf die Schicht begrenzt).
func (s workShift) breakRanges() [][2]int {
	l := shiftLen(s.StartMin, s.EndMin)
	var r [][2]int
	for _, b := range s.Breaks {
		off := ((b.StartMin-s.StartMin)%1440 + 1440) % 1440
		end := off + shiftLen(b.StartMin, b.EndMin)
		if end > l {
			end = l
		}
		if end > off {
			r = append(r, [2]int{off, end})
		}
	}
	sort.Slice(r, func(i, j int) bool { return r[i][0] < r[j][0] })
	var out [][2]int
	for _, x := range r {
		if n := len(out); n > 0 && x[0] <= out[n-1][1] {
			if x[1] > out[n-1][1] {
				out[n-1][1] = x[1]
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// breakOverlaps: überschneidet die neue Pause bs–be eine vorhandene Pause der Schicht?
func (s workShift) breakOverlaps(bs, be int) bool {
	off := ((bs-s.StartMin)%1440 + 1440) % 1440
	end := off + shiftLen(bs, be)
	for _, b := range s.Breaks {
		o := ((b.StartMin-s.StartMin)%1440 + 1440) % 1440
		e := o + shiftLen(b.StartMin, b.EndMin)
		if off < e && o < end {
			return true
		}
	}
	return false
}

// intervals: geplante Arbeitszeit ohne Pausen im Zeitraum [from, to], sortiert und zusammengefasst.
func (p *workPlan) intervals(from, to time.Time, loc *time.Location) []workInterval {
	if p == nil || !p.Defined || !to.After(from) {
		return nil
	}
	lf, lt := from.In(loc), to.In(loc)
	// Eine Schicht des Vortags kann in den Zeitraum hineinreichen.
	day := time.Date(lf.Year(), lf.Month(), lf.Day()-1, 12, 0, 0, 0, loc)
	last := time.Date(lt.Year(), lt.Month(), lt.Day(), 12, 0, 0, 0, loc)
	var all []workInterval
	for n := 0; !day.After(last) && n < 4000; n++ {
		y, m, d := day.Date()
		for _, s := range p.shiftsFor(y, m, d, loc) {
			// Eine Schicht über Mitternacht endet an einem als „geschlossen“ markierten Folgetag um Mitternacht.
			cut := time.Time{}
			if s.EndMin <= s.StartMin && p.closedDay(y, m, d+1, loc) {
				cut = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
			}
			for _, w := range s.segments(y, m, d, loc) {
				if !cut.IsZero() {
					if !w.Start.Before(cut) {
						continue
					}
					if w.End.After(cut) {
						w.End = cut
					}
				}
				if w.Start.Before(from) {
					w.Start = from
				}
				if w.End.After(to) {
					w.End = to
				}
				if w.End.After(w.Start) {
					all = append(all, w)
				}
			}
		}
		day = time.Date(y, m, d+1, 12, 0, 0, 0, loc)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	var out []workInterval
	for _, w := range all {
		if n := len(out); n > 0 && !w.Start.After(out[n-1].End) {
			if w.End.After(out[n-1].End) {
				out[n-1].End = w.End
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// ── Speicher ─────────────────────────────────────────────────────────────

// plantWorkIntervals liefert die geplante Arbeitszeit der Anlage im Zeitraum [from, to] ohne Pausen
// (Ortszeit Europe/Berlin, sommerzeitfest). Zeiten gelten nach unten vererbt; ohne Zeiten an der Anlage
// und darüber ist die Liste leer (plantHasWorkTimes unterscheidet das von „nie Arbeitszeit“).
func (h *Handler) plantWorkIntervals(ctx context.Context, infraID string, from, to time.Time) ([]workInterval, error) {
	p, err := h.loadWorkPlan(ctx, infraID)
	if err != nil {
		return nil, err
	}
	return p.intervals(from, to, workLocation), nil
}

// plantHasWorkTimes: gibt es für die Anlage (eigene oder geerbte) Arbeitszeiten?
func (h *Handler) plantHasWorkTimes(ctx context.Context, infraID string) (bool, error) {
	p, err := h.loadWorkPlan(ctx, infraID)
	if err != nil {
		return false, err
	}
	return p.Defined, nil
}

// loadWorkPlan lädt die wirksamen Zeiten der Anlage: die der Anlage selbst oder der nächsten darüber.
func (h *Handler) loadWorkPlan(ctx context.Context, infraID string) (*workPlan, error) {
	p := &workPlan{}
	if h.db == nil {
		return p, nil
	}
	var depth int
	err := h.db.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid
			UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.d < 50)
		SELECT up.id::text, up.name, up.d FROM up JOIN plant_work_times w ON w.infrastructure_id = up.id ORDER BY up.d LIMIT 1`, infraID).
		Scan(&p.SourceID, &p.SourceName, &depth)
	if err != nil {
		if isNoRows(err) {
			return p, nil
		}
		return nil, err
	}
	p.Defined, p.Own = true, depth == 0
	return p, h.fillWorkPlan(ctx, p)
}

// loadWorkPlans lädt die wirksamen Zeiten mehrerer Anlagen mit wenigen Abfragen (je Quelle einmal).
func (h *Handler) loadWorkPlans(ctx context.Context, ids []string) (map[string]*workPlan, error) {
	out := map[string]*workPlan{}
	for _, id := range ids {
		out[id] = &workPlan{}
	}
	if h.db == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT id AS origin, id, parent_id, 0 AS d FROM infrastructure WHERE id = ANY($1::uuid[])
			UNION ALL SELECT up.origin, i.id, i.parent_id, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.d < 50)
		SELECT DISTINCT ON (up.origin) up.origin::text, up.id::text, inf.name FROM up
		  JOIN plant_work_times w ON w.infrastructure_id = up.id JOIN infrastructure inf ON inf.id = up.id
		 ORDER BY up.origin, up.d`, ids)
	if err != nil {
		return nil, err
	}
	type src struct{ id, name string }
	of := map[string]src{}
	for rows.Next() {
		var o string
		var x src
		if err := rows.Scan(&o, &x.id, &x.name); err != nil {
			rows.Close()
			return nil, err
		}
		of[o] = x
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	loaded := map[string]*workPlan{}
	for o, x := range of {
		base := loaded[x.id]
		if base == nil {
			base = &workPlan{Defined: true, SourceID: x.id, SourceName: x.name}
			if err := h.fillWorkPlan(ctx, base); err != nil {
				return nil, err
			}
			loaded[x.id] = base
		}
		c := *base
		c.Own = o == x.id
		out[o] = &c
	}
	return out, nil
}

func workMinExpr(col string) string {
	return "(EXTRACT(HOUR FROM " + col + ")*60 + EXTRACT(MINUTE FROM " + col + "))::int"
}

func (h *Handler) fillWorkPlan(ctx context.Context, p *workPlan) error {
	rows, err := h.db.Query(ctx, `SELECT s.id::text, COALESCE(s.weekday, 0)::int, COALESCE(s.exception_id::text, ''), s.name,
			`+workMinExpr("s.start_time")+`, `+workMinExpr("s.end_time")+`
		  FROM plant_shifts s WHERE s.infrastructure_id = $1::uuid ORDER BY s.weekday NULLS LAST, s.start_time, s.sort, s.id`, p.SourceID)
	if err != nil {
		return err
	}
	shifts := map[string]*workShift{}
	excShifts := map[string][]string{}
	var weekly []string
	for rows.Next() {
		var s workShift
		var exc string
		if err := rows.Scan(&s.ID, &s.Weekday, &exc, &s.Name, &s.StartMin, &s.EndMin); err != nil {
			rows.Close()
			return err
		}
		c := s
		shifts[s.ID] = &c
		if exc == "" {
			weekly = append(weekly, s.ID)
		} else {
			excShifts[exc] = append(excShifts[exc], s.ID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	brows, err := h.db.Query(ctx, `SELECT b.id::text, b.shift_id::text, `+workMinExpr("b.start_time")+`, `+workMinExpr("b.end_time")+`
		  FROM plant_shift_breaks b JOIN plant_shifts s ON s.id = b.shift_id WHERE s.infrastructure_id = $1::uuid ORDER BY b.start_time, b.id`, p.SourceID)
	if err != nil {
		return err
	}
	for brows.Next() {
		var b workBreak
		var sid string
		if err := brows.Scan(&b.ID, &sid, &b.StartMin, &b.EndMin); err != nil {
			brows.Close()
			return err
		}
		if s := shifts[sid]; s != nil {
			s.Breaks = append(s.Breaks, b)
		}
	}
	brows.Close()
	if err := brows.Err(); err != nil {
		return err
	}
	for _, id := range weekly {
		p.Shifts = append(p.Shifts, *shifts[id])
	}
	erows, err := h.db.Query(ctx, `SELECT id::text, day::text, closed, note FROM plant_work_exceptions WHERE infrastructure_id = $1::uuid ORDER BY day`, p.SourceID)
	if err != nil {
		return err
	}
	for erows.Next() {
		var e workException
		if err := erows.Scan(&e.ID, &e.Day, &e.Closed, &e.Note); err != nil {
			erows.Close()
			return err
		}
		for _, id := range excShifts[e.ID] {
			e.Shifts = append(e.Shifts, *shifts[id])
		}
		p.Exceptions = append(p.Exceptions, e)
	}
	erows.Close()
	return erows.Err()
}
