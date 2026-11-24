package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reiter „Arbeitszeiten“ an der Anlage (Infrastruktur, Detailseite): Schichten und Pausen je Wochentag,
// Ausnahmen je Datum, Vererbung nach unten wie bei den Rüsttasten. Rechnen: work_times.go.

var workUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var workDayNames = [...]string{"", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag", "Sonntag"}

type workBreakView struct{ ID, Start, End string }

type workShiftView struct {
	ID, Name, Start, End string
	Overnight            bool
	Len                  string // Länge ohne Pausen, z. B. „7 Std. 30 Min.“
	Breaks               []workBreakView
}

type workDayView struct {
	Weekday int
	Name    string
	Shifts  []workShiftView
}

type workExcView struct {
	ID, Day, Label, Note string
	Closed               bool
	Shifts               []workShiftView
}

type workModelOption struct{ ID, Name string }

// workInfraTab: Daten des Reiters „Arbeitszeiten“.
type workInfraTab struct {
	Defined    bool
	Own        bool
	SourceName string // Anlage, deren Zeiten gelten (bei geerbten Zeiten)
	Days       []workDayView
	Exceptions []workExcView
	Models     []workModelOption // Schichtmodelle des Planers zum Übernehmen
	Count      int               // Schichten je Woche
	Week       string            // Arbeitszeit je Woche ohne Pausen
}

func workDuration(min int) string {
	if min < 60 {
		return fmt.Sprintf("%d Min.", min)
	}
	if min%60 == 0 {
		return fmt.Sprintf("%d Std.", min/60)
	}
	return fmt.Sprintf("%d Std. %d Min.", min/60, min%60)
}

func (s workShift) netMinutes() int {
	n := shiftLen(s.StartMin, s.EndMin)
	for _, r := range s.breakRanges() {
		n -= r[1] - r[0]
	}
	if n < 0 {
		n = 0
	}
	return n
}

func (s workShift) view() workShiftView {
	v := workShiftView{ID: s.ID, Name: s.Name, Start: fmtMin(s.StartMin), End: fmtMin(s.EndMin), Overnight: s.EndMin <= s.StartMin, Len: workDuration(s.netMinutes())}
	for _, b := range s.Breaks {
		v.Breaks = append(v.Breaks, workBreakView{b.ID, fmtMin(b.StartMin), fmtMin(b.EndMin)})
	}
	return v
}

func (h *Handler) infraWorkTab(ctx context.Context, infraID string, models bool) workInfraTab {
	var t workInfraTab
	if h.db == nil {
		return t
	}
	p, err := h.loadWorkPlan(ctx, infraID)
	if err != nil {
		componentLog("arbeitszeit").Warn().Err(err).Str("anlage", infraID).Msg("arbeitszeiten laden")
		return t
	}
	t.Defined, t.Own = p.Defined, p.Own
	if !p.Own {
		t.SourceName = p.SourceName
	}
	for wd := 1; wd <= 7; wd++ {
		d := workDayView{Weekday: wd, Name: workDayNames[wd]}
		for _, s := range p.Shifts {
			if s.Weekday == wd {
				d.Shifts = append(d.Shifts, s.view())
				t.Count++
			}
		}
		t.Days = append(t.Days, d)
	}
	week := 0
	for _, s := range p.Shifts {
		week += s.netMinutes()
	}
	t.Week = workDuration(week)
	for _, e := range p.Exceptions {
		v := workExcView{ID: e.ID, Day: e.Day, Note: e.Note, Closed: e.Closed, Label: e.Day}
		if d, err := time.Parse("2006-01-02", e.Day); err == nil {
			v.Label = workDayNames[(int(d.Weekday())+6)%7+1][:2] + ", " + d.Format("02.01.2006")
		}
		for _, s := range e.Shifts {
			v.Shifts = append(v.Shifts, s.view())
		}
		t.Exceptions = append(t.Exceptions, v)
	}
	for _, m := range h.workModels(ctx, models) {
		if m.Shifts > 0 {
			t.Models = append(t.Models, workModelOption{m.ID, m.Name})
		}
	}
	return t
}

// InfraWorkTimesWeb: POST /infrastructure/{id}/work-times-web (action=…) – Arbeitszeiten der Anlage pflegen.
// Recht: Infrastruktur bearbeiten in der Abteilung der Anlage (Schranke in perm_gate.go, hier nochmals geprüft).
// Aktionen: own_on, own_off, shift_add, shift_del, break_add, break_del, exc_add, exc_del, model_copy.
func (h *Handler) InfraWorkTimesWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.setupInfraGuard(w, r)
	if !ok {
		return
	}
	back := "/infrastructure/" + id + "?tab=work"
	if h.db == nil {
		infraRedirect(w, r, back, "", errors.New("Die Arbeitszeiten sind nicht verfügbar."))
		return
	}
	ctx := r.Context()
	act := r.FormValue("action")
	msg, err := h.workTimesAction(ctx, id, act, r)
	if err != nil {
		var ue workInputError
		if !errors.As(err, &ue) {
			componentLog("arbeitszeit").Error().Err(err).Str("anlage", id).Str("aktion", act).Msg("arbeitszeiten speichern")
			err = errors.New("Die Arbeitszeiten konnten nicht gespeichert werden.")
		}
		infraRedirect(w, r, back, "", err)
		return
	}
	h.addHistory(ctx, "infrastructure", id, "update", "Arbeitszeiten", "", "", msg, getUser(r).ID)
	infraRedirect(w, r, back, msg, nil)
}

// workInputError: Eingabefehler, dessen Text der Person gezeigt wird.
type workInputError struct{ msg string }

func (e workInputError) Error() string { return e.msg }

func inputErr(msg string) error { return workInputError{msg} }

// Grenzen je Anlage (serverseitig geprüft).
const (
	workMaxShiftsPerDay   = 10  // Schichten je Wochentag bzw. je Ausnahme
	workMaxBreaksPerShift = 10  // Pausen je Schicht
	workMaxExceptions     = 400 // Ausnahmen je Anlage
)

var errWorkNotOwn = inputErr("Bitte zuerst „Eigene Arbeitszeiten“ einschalten.")

// workTx führt fn in einer Transaktion aus, in der die „eigene Zeiten“-Zeile der Anlage gesperrt ist
// (so kann niemand gleichzeitig die eigenen Zeiten entfernen oder die Grenzen umgehen).
func (h *Handler) workTx(ctx context.Context, id string, fn func(tx pgx.Tx) error) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM plant_work_times WHERE infrastructure_id = $1::uuid FOR UPDATE`, id).Scan(&one); err != nil {
		if isNoRows(err) {
			return errWorkNotOwn
		}
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) workTimesAction(ctx context.Context, id, act string, r *http.Request) (string, error) {
	switch act {
	case "own_on":
		return "Eigene Arbeitszeiten eingeschaltet", h.workOwnOn(ctx, id)
	case "own_off":
		return "Eigene Arbeitszeiten entfernt, es gelten wieder die der übergeordneten Anlage", h.workOwnOff(ctx, id)
	}
	var msg string
	err := h.workTx(ctx, id, func(tx pgx.Tx) error {
		switch act {
		case "shift_add":
			msg = "Schicht gespeichert"
			return workShiftAdd(ctx, tx, id, r)
		case "shift_del":
			msg = "Schicht gelöscht"
			return workDelete(ctx, tx, `DELETE FROM plant_shifts WHERE id = $1::uuid AND infrastructure_id = $2::uuid`, r.FormValue("shift_id"), id)
		case "break_add":
			msg = "Pause gespeichert"
			return workBreakAdd(ctx, tx, id, r)
		case "break_del":
			msg = "Pause gelöscht"
			return workDelete(ctx, tx, `DELETE FROM plant_shift_breaks b USING plant_shifts s WHERE b.id = $1::uuid AND s.id = b.shift_id AND s.infrastructure_id = $2::uuid`, r.FormValue("break_id"), id)
		case "exc_add":
			msg = "Ausnahme gespeichert"
			return workExcAdd(ctx, tx, id, r)
		case "exc_del":
			msg = "Ausnahme gelöscht"
			return workDelete(ctx, tx, `DELETE FROM plant_work_exceptions WHERE id = $1::uuid AND infrastructure_id = $2::uuid`, r.FormValue("exception_id"), id)
		case "model_copy":
			msg = "Schichten aus dem Schichtmodell übernommen"
			if !h.canPlannerView(r) {
				return inputErr("Für die Schichtmodelle fehlt das Recht „Schichtplanung ansehen“.")
			}
			return workModelCopy(ctx, tx, id, r)
		}
		return inputErr("Unbekannte Aktion.")
	})
	return msg, err
}

func workDelete(ctx context.Context, tx pgx.Tx, q, key, infra string) error {
	if !workUUIDRe.MatchString(key) {
		return inputErr("Eintrag nicht gefunden.")
	}
	tag, err := tx.Exec(ctx, q, key, infra)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return inputErr("Eintrag nicht gefunden.")
	}
	return nil
}

// workOwnOn: die Anlage bekommt eigene Zeiten, zunächst als Kopie der bisher wirksamen (geerbten).
func (h *Handler) workOwnOn(ctx context.Context, id string) error {
	cur, err := h.loadWorkPlan(ctx, id)
	if err != nil || cur.Own {
		return err
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO plant_work_times (infrastructure_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 { // gerade eben von jemand anderem eingeschaltet
		return nil
	}
	if cur.Defined {
		for _, s := range cur.Shifts {
			if err := workInsertShift(ctx, tx, id, strconv.Itoa(s.Weekday), "", s); err != nil {
				return err
			}
		}
		for _, e := range cur.Exceptions {
			var eid string
			if err := tx.QueryRow(ctx, `INSERT INTO plant_work_exceptions (infrastructure_id, day, closed, note) VALUES ($1::uuid, $2::date, $3, $4) RETURNING id::text`,
				id, e.Day, e.Closed, e.Note).Scan(&eid); err != nil {
				return err
			}
			for _, s := range e.Shifts {
				if err := workInsertShift(ctx, tx, id, "", eid, s); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit(ctx)
}

func workInsertShift(ctx context.Context, tx pgx.Tx, infra, weekday, excID string, s workShift) error {
	var sid string
	if err := tx.QueryRow(ctx, `INSERT INTO plant_shifts (infrastructure_id, weekday, exception_id, name, start_time, end_time, sort)
		VALUES ($1::uuid, NULLIF($2, '')::smallint, NULLIF($3, '')::uuid, $4, $5::time, $6::time, $7) RETURNING id::text`,
		infra, weekday, excID, s.Name, fmtMin(s.StartMin), fmtMin(s.EndMin), s.StartMin).Scan(&sid); err != nil {
		return err
	}
	for _, b := range s.Breaks {
		if _, err := tx.Exec(ctx, `INSERT INTO plant_shift_breaks (shift_id, start_time, end_time) VALUES ($1::uuid, $2::time, $3::time)`,
			sid, fmtMin(b.StartMin), fmtMin(b.EndMin)); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) workOwnOff(ctx context.Context, id string) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM plant_shifts WHERE infrastructure_id = $1::uuid`,
		`DELETE FROM plant_work_exceptions WHERE infrastructure_id = $1::uuid`,
		`DELETE FROM plant_work_times WHERE infrastructure_id = $1::uuid`,
	} {
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// workDayTarget: "1".."7" = Wochentag, JJJJ-MM-TT = Ausnahme (muss mit eigenen Schichten angelegt sein).
// Prüft zugleich die Grenze der Schichten je Tag.
func workDayTarget(ctx context.Context, tx pgx.Tx, infra, day string) (weekday, excID string, err error) {
	day = strings.TrimSpace(day)
	var n int
	if v, e := strconv.Atoi(day); e == nil && v >= 1 && v <= 7 {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plant_shifts WHERE infrastructure_id = $1::uuid AND weekday = $2::smallint`, infra, v).Scan(&n); err != nil {
			return "", "", err
		}
		if n >= workMaxShiftsPerDay {
			return "", "", inputErr(fmt.Sprintf("Je Wochentag sind höchstens %d Schichten möglich.", workMaxShiftsPerDay))
		}
		return day, "", nil
	}
	if _, e := time.Parse("2006-01-02", day); e != nil {
		return "", "", inputErr("Bitte einen Wochentag wählen.")
	}
	var closed bool
	e := tx.QueryRow(ctx, `SELECT id::text, closed FROM plant_work_exceptions WHERE infrastructure_id = $1::uuid AND day = $2::date`, infra, day).Scan(&excID, &closed)
	if isNoRows(e) {
		return "", "", inputErr("Für dieses Datum gibt es keine Ausnahme.")
	}
	if e != nil {
		return "", "", e
	}
	if closed {
		return "", "", inputErr("Die Ausnahme ist als „geschlossen“ angelegt und hat keine Schichten.")
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plant_shifts WHERE exception_id = $1::uuid`, excID).Scan(&n); err != nil {
		return "", "", err
	}
	if n >= workMaxShiftsPerDay {
		return "", "", inputErr(fmt.Sprintf("Je Ausnahme sind höchstens %d Schichten möglich.", workMaxShiftsPerDay))
	}
	return "", excID, nil
}

func workShiftAdd(ctx context.Context, tx pgx.Tx, id string, r *http.Request) error {
	st, ok1 := parseHM(r.FormValue("start"))
	en, ok2 := parseHM(r.FormValue("end"))
	if !ok1 || !ok2 {
		return inputErr("Bitte Beginn und Ende als Uhrzeit (HH:MM) eintragen.")
	}
	if err := validateShiftTimes(st, en); err != nil {
		return inputErr(err.Error())
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if len([]rune(name)) > 60 {
		return inputErr("Der Name der Schicht darf höchstens 60 Zeichen lang sein.")
	}
	wd, exc, err := workDayTarget(ctx, tx, id, r.FormValue("day"))
	if err != nil {
		return err
	}
	return workInsertShift(ctx, tx, id, wd, exc, workShift{Name: name, StartMin: st, EndMin: en})
}

func workBreakAdd(ctx context.Context, tx pgx.Tx, id string, r *http.Request) error {
	sid := r.FormValue("shift_id")
	bs, ok1 := parseHM(r.FormValue("start"))
	be, ok2 := parseHM(r.FormValue("end"))
	if !workUUIDRe.MatchString(sid) {
		return inputErr("Schicht nicht gefunden.")
	}
	if !ok1 || !ok2 {
		return inputErr("Bitte Beginn und Ende der Pause als Uhrzeit (HH:MM) eintragen.")
	}
	var s workShift
	err := tx.QueryRow(ctx, `SELECT `+workMinExpr("start_time")+`, `+workMinExpr("end_time")+` FROM plant_shifts WHERE id = $1::uuid AND infrastructure_id = $2::uuid`, sid, id).
		Scan(&s.StartMin, &s.EndMin)
	if isNoRows(err) {
		return inputErr("Schicht nicht gefunden.")
	}
	if err != nil {
		return err
	}
	if err := validateBreak(s.StartMin, s.EndMin, bs, be); err != nil {
		return inputErr(err.Error())
	}
	rows, err := tx.Query(ctx, `SELECT `+workMinExpr("start_time")+`, `+workMinExpr("end_time")+` FROM plant_shift_breaks WHERE shift_id = $1::uuid`, sid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b workBreak
		if err := rows.Scan(&b.StartMin, &b.EndMin); err != nil {
			rows.Close()
			return err
		}
		s.Breaks = append(s.Breaks, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(s.Breaks) >= workMaxBreaksPerShift {
		return inputErr(fmt.Sprintf("Je Schicht sind höchstens %d Pausen möglich.", workMaxBreaksPerShift))
	}
	if s.breakOverlaps(bs, be) {
		return inputErr("Die Pause überschneidet sich mit einer anderen Pause dieser Schicht.")
	}
	_, err = tx.Exec(ctx, `INSERT INTO plant_shift_breaks (shift_id, start_time, end_time) VALUES ($1::uuid, $2::time, $3::time)`, sid, fmtMin(bs), fmtMin(be))
	return err
}

func workExcAdd(ctx context.Context, tx pgx.Tx, id string, r *http.Request) error {
	day := strings.TrimSpace(r.FormValue("day"))
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return inputErr("Bitte ein Datum wählen.")
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len([]rune(note)) > 80 {
		return inputErr("Die Bemerkung darf höchstens 80 Zeichen lang sein.")
	}
	closed := r.FormValue("mode") != "times"
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plant_work_exceptions WHERE infrastructure_id = $1::uuid`, id).Scan(&n); err != nil {
		return err
	}
	if n >= workMaxExceptions {
		return inputErr(fmt.Sprintf("Je Anlage sind höchstens %d Ausnahmen möglich. Bitte alte Ausnahmen löschen.", workMaxExceptions))
	}
	var same int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plant_work_exceptions WHERE infrastructure_id = $1::uuid AND day = $2::date`, id, day).Scan(&same); err != nil {
		return err
	}
	if same > 0 {
		return inputErr("Für dieses Datum gibt es schon eine Ausnahme.")
	}
	_, err := tx.Exec(ctx, `INSERT INTO plant_work_exceptions (infrastructure_id, day, closed, note) VALUES ($1::uuid, $2::date, $3, $4)`, id, day, closed, note)
	return err
}

// workModelCopy übernimmt die Schichten eines Schichtmodells des Planers (Beginn, Ende) an die gewählten
// Wochentage; Schichten, die es dort schon gibt (gleicher Beginn und gleiches Ende), werden übersprungen.
// Das Modell kennt nur die Pausendauer, keine Pausenzeiten: Pausen bleiben zu ergänzen.
func workModelCopy(ctx context.Context, tx pgx.Tx, id string, r *http.Request) error {
	mid := r.FormValue("model_id")
	if !workUUIDRe.MatchString(mid) {
		return inputErr("Bitte ein Schichtmodell wählen.")
	}
	var days []int
	for _, d := range r.Form["weekday"] {
		if n, err := strconv.Atoi(d); err == nil && n >= 1 && n <= 7 {
			days = append(days, n)
		}
	}
	if len(days) == 0 {
		return inputErr("Bitte mindestens einen Wochentag wählen.")
	}
	rows, err := tx.Query(ctx, `SELECT d.name, `+workMinExpr("d.start_time")+`, `+workMinExpr("d.end_time")+`
		  FROM shift_definitions d JOIN shift_models m ON m.id = d.model_id
		 WHERE d.model_id = $1::uuid AND d.active AND m.active ORDER BY d.sort_order, d.start_time, d.name`, mid)
	if err != nil {
		return err
	}
	var defs []workShift
	for rows.Next() {
		var s workShift
		if err := rows.Scan(&s.Name, &s.StartMin, &s.EndMin); err != nil {
			rows.Close()
			return err
		}
		if s.StartMin != s.EndMin {
			defs = append(defs, s)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(defs) == 0 {
		return inputErr("Das Schichtmodell hat keine Schichten mit Zeiten.")
	}
	added := 0
	for _, d := range days {
		have := map[[2]int]bool{}
		n := 0
		erows, err := tx.Query(ctx, `SELECT `+workMinExpr("start_time")+`, `+workMinExpr("end_time")+` FROM plant_shifts WHERE infrastructure_id = $1::uuid AND weekday = $2::smallint`, id, d)
		if err != nil {
			return err
		}
		for erows.Next() {
			var a, b int
			if err := erows.Scan(&a, &b); err != nil {
				erows.Close()
				return err
			}
			have[[2]int{a, b}] = true
			n++
		}
		erows.Close()
		if err := erows.Err(); err != nil {
			return err
		}
		for _, s := range defs {
			if have[[2]int{s.StartMin, s.EndMin}] {
				continue
			}
			if n >= workMaxShiftsPerDay {
				return inputErr(fmt.Sprintf("Je Wochentag sind höchstens %d Schichten möglich.", workMaxShiftsPerDay))
			}
			if err := workInsertShift(ctx, tx, id, strconv.Itoa(d), "", s); err != nil {
				return err
			}
			have[[2]int{s.StartMin, s.EndMin}] = true
			n++
			added++
		}
	}
	if added == 0 {
		return inputErr("Die Schichten des Modells gibt es an den gewählten Wochentagen schon.")
	}
	return nil
}

// workModels: Schichtmodelle zum Übernehmen, nur wer die Schichtplanung ansehen darf bekommt sie.
func (h *Handler) workModels(ctx context.Context, allowed bool) []plannerModel {
	if !allowed {
		return nil
	}
	return h.plannerModels(ctx, false)
}
