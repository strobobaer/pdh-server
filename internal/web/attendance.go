package web

// PDH-Seite „Anwesenheit & Arbeitszeit“ (0.82.53): Anwesenheit (Kommen, Gehen, Pausen) und Arbeitszeit (Arbeit an
// Vorgängen und Anlagen) je Person und Tag, Korrekturen mit Verlauf und CSV-Export.
//
//	GET  /attendance                        Liste (?von=, ?bis=, ?person=)
//	GET  /attendance/export.csv             dieselbe Auswahl als CSV
//	POST /attendance/event                  Buchung nachtragen
//	POST /attendance/event/{id}/edit        Buchung ändern
//	POST /attendance/event/{id}/delete      Buchung löschen
//	POST /attendance/work/{id}/edit         Arbeitszeit ändern (Beginn, Ende)
//	POST /attendance/work/{id}/delete       Arbeitszeit löschen
//
// Rechte attendance.view / attendance.edit gelten je Person nach deren Abteilung (abteilungsbezogen, rbac.Scope).
// Jede Korrektur verlangt eine Begründung und steht in attendance_history – nichts geschieht still. Der Ablauf einer
// Korrektur wird mit derselben Zustandsprüfung wie am Terminal (attNext) geprüft.

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	permAttendanceView = "attendance.view"
	permAttendanceEdit = "attendance.edit"
	attMaxDays         = 62
	attHistoryShow     = 100
	attWorkMax         = 24 * time.Hour
	attTimeFmt         = "02.01.2006 15:04"
	attInputFmt        = "2006-01-02T15:04"
)

var attViaLabel = map[string]string{vorgViaKarte: "mit Karte", vorgViaGewaehlt: "am Terminal gewählt", zeitViaPDH: "in PDH"}

// ---- Rechnen (rein) ----

type attEvent struct {
	ID, Kind, Via string
	At            time.Time
}

// attBlock: ein Anwesenheitsblock Kommen … Gehen.
type attBlock struct {
	Start, End time.Time
	PauseMin   int
	Open       bool // Gehen fehlt
}

// Net: Anwesenheit ohne Pausen in Minuten (offene Blöcke zählen nicht).
func (b attBlock) Net() int {
	if b.Open {
		return 0
	}
	return max(0, int(b.End.Sub(b.Start).Minutes())-b.PauseMin)
}

// attBlocks bildet die Blöcke aus den (nach Zeit geordneten) Buchungen; Unstimmiges wird nicht verworfen,
// sondern als offener Block sichtbar (Gehen fehlt).
func attBlocks(evs []attEvent) []attBlock {
	var out []attBlock
	var cur *attBlock
	var pauseAt time.Time
	inPause := false
	closeOpen := func() {
		if cur != nil {
			cur.Open = true
			out = append(out, *cur)
			cur, inPause = nil, false
		}
	}
	for _, e := range evs {
		switch e.Kind {
		case attKommen:
			closeOpen()
			cur = &attBlock{Start: e.At}
		case attPauseBegin:
			if cur != nil && !inPause {
				inPause, pauseAt = true, e.At
			}
		case attPauseEnde:
			if cur != nil && inPause {
				cur.PauseMin += int(e.At.Sub(pauseAt).Minutes())
				inPause = false
			}
		case attGehen:
			if cur != nil {
				if inPause {
					cur.PauseMin += int(e.At.Sub(pauseAt).Minutes())
				}
				cur.End = e.At
				out = append(out, *cur)
				cur, inPause = nil, false
			}
		}
	}
	closeOpen()
	return out
}

// attValidate prüft den Ablauf einer geordneten Folge ab dem Anfangszustand; "" = in Ordnung.
func attValidate(start attState, evs []attEvent) string {
	cur := start
	for _, e := range evs {
		next, msg := attNext(cur, e.Kind)
		if msg != "" {
			return attKindLabel[e.Kind] + " um " + e.At.In(workLocation).Format(attTimeFmt) + " passt nicht: " + msg
		}
		cur = next
	}
	return ""
}

func attSort(evs []attEvent) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
}

// ---- Zugriff ----

// attPerson: Person mit Abteilung für die Rechteprüfung.
type attPerson struct{ ID, Name, Dept string }

// attCan: attendance.view/edit für die Person (Abteilung der Person, Bereich der Rolle bzw. Einzelberechtigung).
func (h *Handler) attCan(r *http.Request, perm, dept string) bool {
	u := getUser(r)
	if h.rbac == nil || u == nil {
		return false
	}
	var depts []string
	if dept != "" {
		depts = []string{dept}
	}
	return h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), perm, depts)
}

// attPeople: alle Personen, die die Anfrage mit attendance.view sehen darf.
func (h *Handler) attPeople(r *http.Request) ([]attPerson, error) {
	rows, err := h.db.Query(r.Context(), `SELECT id::text, TRIM(first_name || ' ' || last_name), COALESCE(department_id::text, '') FROM users
		WHERE active AND NOT is_bot AND NOT is_system_user ORDER BY lower(last_name), lower(first_name), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []attPerson
	for rows.Next() {
		var p attPerson
		if err := rows.Scan(&p.ID, &p.Name, &p.Dept); err != nil {
			return nil, err
		}
		if h.attCan(r, permAttendanceView, p.Dept) {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func attDenied(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Error(w, "keine berechtigung", http.StatusForbidden)
}

// ---- Auswertung ----

type attEventView struct {
	ID, Kind, Label, Time, Input, Via string
}

type attWorkView struct {
	ID, What, Start, End, StartIn, EndIn, Via string
	Minutes                                   int
	Running, Forgotten                        bool
}

// attRow: eine Person an einem Tag.
type attRow struct {
	PersonID, Person, Day, DayKey string
	Kommen, Gehen                 string
	PauseMin, PresentMin, WorkMin int
	Open, Forgotten               bool
	Planned                       string // Soll laut Arbeitszeiten der Anlage, "" = keine Angabe
	Diff                          string
	Events                        []attEventView
	Works                         []attWorkView
	CanEdit                       bool
	plannedMin                    int
	hasPlanned                    bool
}

func (r attRow) PauseText() string   { return workDuration(r.PauseMin) }
func (r attRow) PresentText() string { return workDuration(r.PresentMin) }
func (r attRow) WorkText() string    { return workDuration(r.WorkMin) }

type attHistView struct{ Person, Subject, Action, Old, New, Reason, By, At string }

type attPage struct {
	BaseData
	From, To, Person string
	People           []attPerson
	Rows             []attRow
	Hist             []attHistView
	CanEdit          bool
	Notice           string
	Kinds            []struct{ Key, Label string }
}

var attNotices = map[string]string{
	"ok": "Gespeichert – die Korrektur steht im Verlauf.", "reason": "Bitte eine Begründung eingeben.",
	"time": "Bitte eine gültige Zeit eingeben.", "kind": "Unbekannte Buchung.", "future": "Die Zeit liegt in der Zukunft.",
	"gone": "Der Eintrag existiert nicht mehr.", "running": "Laufende Arbeit kann erst nach dem Beenden korrigiert werden.",
	"order": "Das Ende liegt vor dem Beginn.", "long": "Eine Arbeitszeit darf höchstens 24 Stunden dauern.", "overlap": "Die Zeit überschneidet sich mit einer anderen Arbeit der Person.", "err": "Das hat nicht geklappt.",
}

func attRange(r *http.Request) (from, to time.Time) {
	now := time.Now().In(workLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, workLocation)
	parse := func(s string, def time.Time) time.Time {
		if t, err := time.ParseInLocation("2006-01-02", s, workLocation); err == nil {
			return t
		}
		return def
	}
	from = parse(r.URL.Query().Get("von"), today.AddDate(0, 0, -13))
	to = parse(r.URL.Query().Get("bis"), today)
	if to.Before(from) {
		from, to = to, from
	}
	if to.Sub(from) > attMaxDays*24*time.Hour {
		from = to.AddDate(0, 0, -attMaxDays)
	}
	return from, to
}

func (h *Handler) attLoad(r *http.Request, people []attPerson, from, to time.Time) ([]attRow, error) {
	ctx := r.Context()
	end := to.AddDate(0, 0, 1)
	ids := make([]string, len(people))
	pmap := map[string]attPerson{}
	for i, p := range people {
		ids[i], pmap[p.ID] = p.ID, p
	}
	type key struct{ p, d string }
	rows := map[key]*attRow{}
	get := func(pid string, t time.Time) *attRow {
		d := t.In(workLocation).Format("2006-01-02")
		k := key{pid, d}
		if rows[k] == nil {
			pp := pmap[pid]
			rows[k] = &attRow{PersonID: pid, Person: pp.Name, DayKey: d, Day: t.In(workLocation).Format("02.01.2006"), CanEdit: h.attCan(r, permAttendanceEdit, pp.Dept)}
		}
		return rows[k]
	}
	// Buchungen mit Vorlauf, damit ein Block über Mitternacht vollständig ist
	rs, err := h.db.Query(ctx, `SELECT id::text, user_id::text, kind, at, origin_via FROM attendance_events
		WHERE user_id = ANY($1::uuid[]) AND at >= $2 AND at < $3 ORDER BY user_id, at, created_at, id`, ids, from.AddDate(0, 0, -1), end.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	per := map[string][]attEvent{}
	for rs.Next() {
		var pid string
		var e attEvent
		if err := rs.Scan(&e.ID, &pid, &e.Kind, &e.At, &e.Via); err != nil {
			rs.Close()
			return nil, err
		}
		per[pid] = append(per[pid], e)
		if !e.At.Before(from) && e.At.Before(end) {
			row := get(pid, e.At)
			row.Events = append(row.Events, attEventView{ID: e.ID, Kind: e.Kind, Label: attKindLabel[e.Kind], Time: e.At.In(workLocation).Format("15:04"),
				Input: e.At.In(workLocation).Format(attInputFmt), Via: attViaLabel[e.Via]})
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for pid, evs := range per {
		for _, b := range attBlocks(evs) {
			if b.Start.Before(from) || !b.Start.Before(end) {
				continue
			}
			row := get(pid, b.Start)
			row.Kommen = b.Start.In(workLocation).Format("15:04")
			row.PauseMin += b.PauseMin
			if b.Open {
				row.Open = true
				continue
			}
			row.Gehen = b.End.In(workLocation).Format("15:04")
			row.PresentMin += b.Net()
		}
	}
	// Arbeit
	ws, err := h.db.Query(ctx, `SELECT id::text, user_id::text, ref_type, title, COALESCE(infrastructure_id::text, ''), started_at, ended_at, origin_via, forgotten
		FROM work_sessions WHERE user_id = ANY($1::uuid[]) AND started_at >= $2 AND started_at < $3 ORDER BY started_at, id`, ids, from, end)
	if err != nil {
		return nil, err
	}
	use := map[key]map[string]int{}
	for ws.Next() {
		var id, pid, rt, title, infra, via string
		var st time.Time
		var forg bool
		var en *time.Time
		if err := ws.Scan(&id, &pid, &rt, &title, &infra, &st, &en, &via, &forg); err != nil {
			ws.Close()
			return nil, err
		}
		row := get(pid, st)
		wv := attWorkView{ID: id, What: zeitKindLabelWhat(rt, title), Start: st.In(workLocation).Format("15:04"), StartIn: st.In(workLocation).Format(attInputFmt), Via: attViaLabel[via], Running: en == nil}
		wv.Forgotten = forg
		row.Forgotten = row.Forgotten || forg
		if en != nil && forg {
			wv.End, wv.EndIn = en.In(workLocation).Format("15:04"), en.In(workLocation).Format(attInputFmt)
		} else if en != nil {
			wv.End, wv.EndIn = en.In(workLocation).Format("15:04"), en.In(workLocation).Format(attInputFmt)
			wv.Minutes = max(1, int(en.Sub(st).Minutes()))
			row.WorkMin += wv.Minutes
			k := key{pid, row.DayKey}
			if use[k] == nil {
				use[k] = map[string]int{}
			}
			if infra != "" {
				use[k][infra] += wv.Minutes
			}
		}
		row.Works = append(row.Works, wv)
	}
	ws.Close()
	if err := ws.Err(); err != nil {
		return nil, err
	}
	// Soll: Arbeitszeiten der Anlage, an der die Person an dem Tag am längsten gearbeitet hat (nur wo es Zeiten gibt)
	cache := map[string]*struct {
		has bool
		min int
	}{}
	for k, m := range use {
		best, bm := "", -1
		for infra, mn := range m {
			if mn > bm || (mn == bm && infra < best) {
				best, bm = infra, mn
			}
		}
		if best == "" {
			continue
		}
		ck := best + "|" + k.d
		c := cache[ck]
		if c == nil {
			c = &struct {
				has bool
				min int
			}{}
			cache[ck] = c
			if has, err := h.plantHasWorkTimes(ctx, best); err == nil && has {
				day, _ := time.ParseInLocation("2006-01-02", k.d, workLocation)
				if iv, err := h.plantWorkIntervals(ctx, best, day, day.AddDate(0, 0, 1)); err == nil {
					c.has, c.min = true, int(plannedWorkMinutes(iv))
				}
			}
		}
		if c.has {
			row := rows[k]
			row.hasPlanned, row.plannedMin = true, c.min
			row.Planned = workDuration(c.min)
			d := row.WorkMin - c.min
			sign := "+"
			if d < 0 {
				sign, d = "−", -d
			}
			row.Diff = sign + workDuration(d)
		}
	}
	out := make([]attRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DayKey != out[j].DayKey {
			return out[i].DayKey > out[j].DayKey
		}
		return strings.ToLower(out[i].Person) < strings.ToLower(out[j].Person)
	})
	return out, nil
}

func zeitKindLabelWhat(rt, title string) string {
	switch rt {
	case "plant":
		return "Anlage " + title
	case "it_asset":
		return "IT-Asset " + title
	case "other":
		return "Sonstiges: " + title
	}
	return zeitKindOf(rt).Label + ": " + title
}

// attHistory: Verlauf der Korrekturen der sichtbaren Personen.
func (h *Handler) attHistory(ctx context.Context, people []attPerson) ([]attHistView, error) {
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = p.ID
	}
	rs, err := h.db.Query(ctx, `SELECT COALESCE(NULLIF(a.person_name, ''), TRIM(u.first_name || ' ' || u.last_name), ''), a.subject, a.action, a.old_text, a.new_text, a.reason,
			COALESCE(TRIM(b.first_name || ' ' || b.last_name), ''), a.changed_at
		FROM attendance_history a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN users b ON b.id = a.changed_by
		WHERE a.user_id = ANY($1::uuid[]) ORDER BY a.changed_at DESC, a.id DESC LIMIT $2`, ids, attHistoryShow)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []attHistView
	for rs.Next() {
		var v attHistView
		var at time.Time
		if err := rs.Scan(&v.Person, &v.Subject, &v.Action, &v.Old, &v.New, &v.Reason, &v.By, &at); err != nil {
			return nil, err
		}
		v.Subject = map[string]string{"attendance": "Anwesenheit", "work": "Arbeitszeit"}[v.Subject]
		v.Action = map[string]string{"add": "nachgetragen", "edit": "geändert", "delete": "gelöscht"}[v.Action]
		v.At = at.In(workLocation).Format(attTimeFmt)
		out = append(out, v)
	}
	return out, rs.Err()
}

// attSelect: gewählte Personen (Filter ?person=) innerhalb des erlaubten Kreises.
func attSelect(people []attPerson, sel string) []attPerson {
	if sel == "" {
		return people
	}
	for _, p := range people {
		if p.ID == sel {
			return []attPerson{p}
		}
	}
	return nil
}

// AttendancePage: GET /attendance
func (h *Handler) AttendancePage(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || !h.hasPerm(r, permAttendanceView) {
		attDenied(w, r)
		return
	}
	people, err := h.attPeople(r)
	if err != nil {
		componentLog("anwesenheit").Error().Err(err).Msg("personen laden")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	from, to := attRange(r)
	d := attPage{BaseData: h.baseData(r, "attendance", "Anwesenheit & Arbeitszeit", "Personal"), People: people,
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Person: r.URL.Query().Get("person"), Notice: attNotices[r.URL.Query().Get("n")]}
	for _, k := range attKinds {
		d.Kinds = append(d.Kinds, struct{ Key, Label string }{k, attKindLabel[k]})
	}
	shown := attSelect(people, d.Person)
	if len(shown) > 0 {
		if d.Rows, err = h.attLoad(r, shown, from, to); err == nil {
			d.Hist, err = h.attHistory(r.Context(), shown)
		}
		if err != nil {
			componentLog("anwesenheit").Error().Err(err).Msg("auswertung")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
	}
	for _, p := range shown {
		if h.attCan(r, permAttendanceEdit, p.Dept) {
			d.CanEdit = true
		}
	}
	h.render(w, "attendance", d)
}

// csvCell schützt vor Formeln in Tabellenprogrammen.
func csvCell(s string) string {
	if s != "" && strings.ContainsAny(s[:1], "=+-@\t\r") {
		return "'" + s
	}
	return s
}

// AttendanceCSV: GET /attendance/export.csv
func (h *Handler) AttendanceCSV(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || !h.hasPerm(r, permAttendanceView) {
		attDenied(w, r)
		return
	}
	people, err := h.attPeople(r)
	if err != nil {
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	from, to := attRange(r)
	var rows []attRow
	if shown := attSelect(people, r.URL.Query().Get("person")); len(shown) > 0 {
		if rows, err = h.attLoad(r, shown, from, to); err != nil {
			componentLog("anwesenheit").Error().Err(err).Msg("export")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="anwesenheit_%s_%s.csv"`, from.Format("2006-01-02"), to.Format("2006-01-02")))
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"Person", "Datum", "Kommen", "Gehen", "Pause (Min)", "Anwesenheit netto (Min)", "Arbeit (Min)", "Soll laut Arbeitszeiten (Min)", "Hinweis"})
	for _, row := range rows {
		soll, note := "", ""
		if row.hasPlanned {
			soll = strconv.Itoa(row.plannedMin)
		}
		if row.Open {
			note = "Gehen fehlt"
		}
		_ = cw.Write([]string{csvCell(row.Person), row.Day, row.Kommen, row.Gehen, strconv.Itoa(row.PauseMin), strconv.Itoa(row.PresentMin), strconv.Itoa(row.WorkMin), soll, note})
	}
	cw.Flush()
}

// ---- Korrekturen ----

func attBack(w http.ResponseWriter, r *http.Request, code string) {
	q := url.Values{"n": {code}}
	for _, k := range []string{"von", "bis", "person"} {
		if v := r.FormValue("back_" + k); v != "" {
			q.Set(k, v)
		}
	}
	http.Redirect(w, r, "/attendance?"+q.Encode(), http.StatusSeeOther)
}

func attParseTime(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(attInputFmt, strings.TrimSpace(s), workLocation)
	return t, err == nil
}

func (h *Handler) attHist(ctx context.Context, tx pgx.Tx, user, subject, action, ref, oldT, newT, reason, by string) error {
	_, err := tx.Exec(ctx, `INSERT INTO attendance_history (user_id, person_name, subject, action, ref_id, old_text, new_text, reason, changed_by)
		VALUES ($1::uuid, COALESCE((SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid), ''), $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8::uuid)`, user, subject, action, ref, oldT, newT, reason, by)
	return err
}

func attEvText(kind string, at time.Time) string {
	return attKindLabel[kind] + " " + at.In(workLocation).Format(attTimeFmt)
}

// attDeptOf: Abteilung der Person.
func (h *Handler) attDeptOf(ctx context.Context, uid string) (string, bool) {
	var d string
	if err := h.db.QueryRow(ctx, `SELECT COALESCE(department_id::text, '') FROM users WHERE id = $1::uuid AND NOT is_bot AND NOT is_system_user`, uid).Scan(&d); err != nil {
		return "", false
	}
	return d, true
}

// attCheckSeq prüft die Folge der Buchungen um die geänderte Zeit herum (±3 Tage) nach der Änderung.
// skip: Kennung der Buchung, die entfällt oder ersetzt wird; add: neue oder geänderte Buchung (kann leer sein).
func (h *Handler) attCheckSeq(ctx context.Context, tx pgx.Tx, uid, skip string, add *attEvent, around time.Time) (string, error) {
	lo, hi := around.AddDate(0, 0, -3), around.AddDate(0, 0, 3)
	var prev *string
	var prevKind string
	err := tx.QueryRow(ctx, `SELECT kind FROM attendance_events WHERE user_id = $1::uuid AND at < $2 AND id <> COALESCE(NULLIF($3, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY at DESC, created_at DESC LIMIT 1`, uid, lo, skip).Scan(&prevKind)
	start := attAbwesend
	if err == nil {
		prev = &prevKind
		start = attStateOf(*prev, time.Now(), time.Now())
	} else if err != pgx.ErrNoRows {
		return "", err
	}
	rs, err := tx.Query(ctx, `SELECT id::text, kind, at FROM attendance_events WHERE user_id = $1::uuid AND at >= $2 AND at <= $3 AND id <> COALESCE(NULLIF($4, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY at, created_at, id`, uid, lo, hi, skip)
	if err != nil {
		return "", err
	}
	var evs []attEvent
	for rs.Next() {
		var e attEvent
		if err := rs.Scan(&e.ID, &e.Kind, &e.At); err != nil {
			rs.Close()
			return "", err
		}
		evs = append(evs, e)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return "", err
	}
	if add != nil {
		evs = append(evs, *add)
	}
	attSort(evs)
	return attValidate(start, evs), nil
}

func attReason(r *http.Request) (string, bool) {
	s := strings.TrimSpace(r.FormValue("reason"))
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:200])
	}
	return s, s != ""
}

// attGate: Recht attendance.edit für die Person; liefert Person und Begründung.
func (h *Handler) attGate(w http.ResponseWriter, r *http.Request, uid string) (reason string, ok bool) {
	if h.db == nil || !h.hasPerm(r, permAttendanceEdit) {
		attDenied(w, r)
		return "", false
	}
	_ = r.ParseForm()
	dept, found := h.attDeptOf(r.Context(), uid)
	if !found {
		attDenied(w, r)
		return "", false
	}
	if !h.attCan(r, permAttendanceEdit, dept) {
		attDenied(w, r)
		return "", false
	}
	rs, okR := attReason(r)
	if !okR {
		attBack(w, r, "reason")
		return "", false
	}
	return rs, true
}

// attFail: Meldung zur ungültigen Folge als Seite (der Text gehört nicht in die Adresse).
func (h *Handler) attFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Error(w, msg, http.StatusUnprocessableEntity)
}

// AttendanceEventAdd: POST /attendance/event (user, kind, at, reason)
func (h *Handler) AttendanceEventAdd(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	uid := r.FormValue("user")
	if !uuidInPathRe.MatchString(uid) {
		attBack(w, r, "gone")
		return
	}
	reason, ok := h.attGate(w, r, uid)
	if !ok {
		return
	}
	kind := r.FormValue("kind")
	at, okT := attParseTime(r.FormValue("at"))
	if !attKindOK(kind) {
		attBack(w, r, "kind")
		return
	}
	if !okT {
		attBack(w, r, "time")
		return
	}
	if at.After(time.Now().Add(5 * time.Minute)) {
		attBack(w, r, "future")
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		attBack(w, r, "err")
		return
	}
	defer tx.Rollback(ctx)
	if err := zeitLock(ctx, tx, uid); err != nil {
		attBack(w, r, "err")
		return
	}
	if msg, err := h.attCheckSeq(ctx, tx, uid, "", &attEvent{Kind: kind, At: at}, at); err != nil {
		attBack(w, r, "err")
		return
	} else if msg != "" {
		h.attFail(w, r, msg)
		return
	}
	var id string
	by := getUser(r).ID
	if err := tx.QueryRow(ctx, `INSERT INTO attendance_events (user_id, kind, at, origin_via, created_by) VALUES ($1::uuid, $2, $3, $4, $5::uuid) RETURNING id::text`,
		uid, kind, at, zeitViaPDH, by).Scan(&id); err != nil {
		attBack(w, r, "err")
		return
	}
	if err := h.attHist(ctx, tx, uid, "attendance", "add", id, "", attEvText(kind, at), reason, by); err != nil || tx.Commit(ctx) != nil {
		attBack(w, r, "err")
		return
	}
	attBack(w, r, "ok")
}

// attEventOf lädt eine Buchung.
func (h *Handler) attEventOf(ctx context.Context, tx pgx.Tx, id string) (uid, kind string, at time.Time, err error) {
	err = tx.QueryRow(ctx, `SELECT user_id::text, kind, at FROM attendance_events WHERE id = $1::uuid FOR UPDATE`, id).Scan(&uid, &kind, &at)
	return
}

// AttendanceEventEdit: POST /attendance/event/{id}/edit (kind, at, reason)
func (h *Handler) AttendanceEventEdit(w http.ResponseWriter, r *http.Request) {
	h.attEventChange(w, r, false)
}

// AttendanceEventDelete: POST /attendance/event/{id}/delete (reason)
func (h *Handler) AttendanceEventDelete(w http.ResponseWriter, r *http.Request) {
	h.attEventChange(w, r, true)
}

func (h *Handler) attEventChange(w http.ResponseWriter, r *http.Request, del bool) {
	id := chi.URLParam(r, "id")
	if h.db == nil || !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	var owner string
	if err := h.db.QueryRow(r.Context(), `SELECT user_id::text FROM attendance_events WHERE id = $1::uuid`, id).Scan(&owner); err != nil {
		attDenied(w, r) // gleiche Antwort, ob der Eintrag existiert oder nicht
		return
	}
	reason, ok := h.attGate(w, r, owner)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		attBack(w, r, "err")
		return
	}
	defer tx.Rollback(ctx)
	if err := zeitLock(ctx, tx, owner); err != nil {
		attBack(w, r, "err")
		return
	}
	uid, kind, at, err := h.attEventOf(ctx, tx, id)
	if err != nil {
		attBack(w, r, "gone")
		return
	}
	by := getUser(r).ID
	if del {
		if msg, err := h.attCheckSeq(ctx, tx, uid, id, nil, at); err != nil {
			attBack(w, r, "err")
			return
		} else if msg != "" {
			h.attFail(w, r, "Löschen nicht möglich: "+msg)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM attendance_events WHERE id = $1::uuid`, id); err != nil {
			attBack(w, r, "err")
			return
		}
		if err := h.attHist(ctx, tx, uid, "attendance", "delete", id, attEvText(kind, at), "", reason, by); err != nil || tx.Commit(ctx) != nil {
			attBack(w, r, "err")
			return
		}
		attBack(w, r, "ok")
		return
	}
	nk := r.FormValue("kind")
	nat, okT := attParseTime(r.FormValue("at"))
	if !attKindOK(nk) {
		attBack(w, r, "kind")
		return
	}
	if !okT {
		attBack(w, r, "time")
		return
	}
	if nat.After(time.Now().Add(5 * time.Minute)) {
		attBack(w, r, "future")
		return
	}
	if msg, err := h.attCheckSeq(ctx, tx, uid, id, &attEvent{ID: id, Kind: nk, At: nat}, nat); err != nil {
		attBack(w, r, "err")
		return
	} else if msg != "" {
		h.attFail(w, r, msg)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE attendance_events SET kind = $2, at = $3 WHERE id = $1::uuid`, id, nk, nat); err != nil {
		attBack(w, r, "err")
		return
	}
	if err := h.attHist(ctx, tx, uid, "attendance", "edit", id, attEvText(kind, at), attEvText(nk, nat), reason, by); err != nil || tx.Commit(ctx) != nil {
		attBack(w, r, "err")
		return
	}
	attBack(w, r, "ok")
}

// AttendanceWorkEdit: POST /attendance/work/{id}/edit (start, end, reason)
func (h *Handler) AttendanceWorkEdit(w http.ResponseWriter, r *http.Request) {
	h.attWorkChange(w, r, false)
}

// AttendanceWorkDelete: POST /attendance/work/{id}/delete (reason)
func (h *Handler) AttendanceWorkDelete(w http.ResponseWriter, r *http.Request) {
	h.attWorkChange(w, r, true)
}

func (h *Handler) attWorkChange(w http.ResponseWriter, r *http.Request, del bool) {
	id := chi.URLParam(r, "id")
	if h.db == nil || !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	var owner string
	if err := h.db.QueryRow(ctx, `SELECT user_id::text FROM work_sessions WHERE id = $1::uuid`, id).Scan(&owner); err != nil {
		attDenied(w, r) // gleiche Antwort, ob der Eintrag existiert oder nicht
		return
	}
	reason, ok := h.attGate(w, r, owner)
	if !ok {
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		attBack(w, r, "err")
		return
	}
	defer tx.Rollback(ctx)
	var rt, title, refID, infra, via string
	var st time.Time
	var en *time.Time
	var entry *string
	var forgotten, pending bool
	var assetID, activityID, text string
	if err := tx.QueryRow(ctx, `SELECT w.ref_type, w.title, w.ref_id::text, COALESCE(w.infrastructure_id::text, ''), w.started_at, w.ended_at, w.time_entry_id::text, w.forgotten, w.origin_via,
			COALESCE((SELECT t.pending FROM time_entries t WHERE t.id = w.time_entry_id), false),
			COALESCE(w.it_asset_id::text, ''), COALESCE(w.activity_id::text, ''), COALESCE(w.description, '')
		FROM work_sessions w WHERE w.id = $1::uuid FOR UPDATE OF w`, id).Scan(&rt, &title, &refID, &infra, &st, &en, &entry, &forgotten, &via, &pending, &assetID, &activityID, &text); err != nil {
		attBack(w, r, "gone")
		return
	}
	_ = via
	if en == nil {
		attBack(w, r, "running")
		return
	}
	// Zeiteintrag am Vorgang: nur solange unbestätigt oder mit dem Recht am Vorgang (Abteilung der Anlage) änderbar
	if zeitTargetKind(rt) == "" && (entry == nil || !pending) {
		u := getUser(r)
		if h.rbac == nil || u == nil || !h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), zeitKindOf(rt).EditPerm, h.recordDepartments(ctx, rt, refID)) {
			http.Error(w, "Diese Zeit gehört zu einem Vorgang und ist bestätigt – bitte direkt am Vorgang korrigieren (dort braucht es das Bearbeitungsrecht für den Vorgang).", http.StatusForbidden)
			return
		}
	}
	what := zeitKindLabelWhat(rt, title)
	oldT := fmt.Sprintf("%s, %s–%s", what, st.In(workLocation).Format(attTimeFmt), en.In(workLocation).Format("15:04"))
	if forgotten {
		oldT += " (vergessen)"
	}
	by := getUser(r).ID
	itemHist := func(action, newT string) {
		if zeitTargetKind(rt) != "" {
			return
		}
		msg := "Arbeitszeit in PDH " + action + ": " + oldT
		if newT != "" {
			msg += " → " + newT
		}
		h.vorgHistory(ctx, historyRef(rt), refID, "time", "Arbeitszeit", oldT, newT, msg+" – "+reason, by, zeitViaPDH, "PDH", []string{owner})
	}
	if del {
		if entry != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM time_entries WHERE id = $1::uuid`, *entry); err != nil {
				attBack(w, r, "err")
				return
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_sessions WHERE id = $1::uuid`, id); err != nil {
			attBack(w, r, "err")
			return
		}
		if err := h.attHist(ctx, tx, owner, "work", "delete", id, oldT, "", reason, by); err != nil || tx.Commit(ctx) != nil {
			attBack(w, r, "err")
			return
		}
		itemHist("gelöscht", "")
		attBack(w, r, "ok")
		return
	}
	ns, ok1 := attParseTime(r.FormValue("start"))
	ne, ok2 := attParseTime(r.FormValue("end"))
	if !ok1 || !ok2 {
		attBack(w, r, "time")
		return
	}
	if !ne.After(ns) {
		attBack(w, r, "order")
		return
	}
	if ne.Sub(ns) > attWorkMax {
		attBack(w, r, "long")
		return
	}
	if ne.After(time.Now().Add(5 * time.Minute)) {
		attBack(w, r, "future")
		return
	}
	var clash bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_sessions WHERE user_id = $1::uuid AND id <> $2::uuid AND started_at < $4 AND COALESCE(ended_at, 'infinity'::timestamptz) > $3)`, owner, id, ns, ne).Scan(&clash); err != nil {
		attBack(w, r, "err")
		return
	}
	if clash {
		attBack(w, r, "overlap")
		return
	}
	mins := max(1, int(ne.Sub(ns).Minutes()))
	if _, err := tx.Exec(ctx, `UPDATE work_sessions SET started_at = $2, ended_at = $3, forgotten = false WHERE id = $1::uuid`, id, ns, ne); err != nil {
		attBack(w, r, "err")
		return
	}
	if entry != nil {
		if _, err := tx.Exec(ctx, `UPDATE time_entries SET started_at = $2, ended_at = $3, duration_min = $4 WHERE id = $1::uuid`, *entry, ns, ne, mins); err != nil {
			attBack(w, r, "err")
			return
		}
	} else if tk := zeitTargetKind(rt); tk != "" {
		// Anlage, IT-Asset, Sonstiges: nur eine vergessene Sitzung, nun geprüft, bekommt jetzt ihren Zeiteintrag
		// (ältere Anlagen-Sitzungen ohne Buchung bleiben ohne)
		if forgotten {
			desc := h.zeitSessionDescription(ctx, tk, refID, title, text)
			var eid string
			if err := tx.QueryRow(ctx, `INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, pending, infrastructure_id, origin_via, target_kind, it_asset_id)
				VALUES (pdh_uuid_v7(), $1::uuid, 'production'::time_ref_type, $2::uuid, $3, $4, $5, $6, false, NULLIF($7, '')::uuid, $8, $9, NULLIF($10, '')::uuid) RETURNING id::text`,
				owner, refID, desc, ns, ne, mins, infra, zeitViaPDH, tk, assetID).Scan(&eid); err != nil {
				attBack(w, r, "err")
				return
			}
			if err := setTimeActivityQ(ctx, tx, eid, activityID); err != nil {
				attBack(w, r, "err")
				return
			}
			if _, err := tx.Exec(ctx, `UPDATE work_sessions SET time_entry_id = $2::uuid WHERE id = $1::uuid`, id, eid); err != nil {
				attBack(w, r, "err")
				return
			}
		}
	} else {
		// vergessene Arbeit, nun geprüft: der Zeiteintrag am Vorgang entsteht jetzt
		desc := "Arbeit am Terminal: " + zeitKindOf(rt).Label + ": " + title + " – in PDH geprüft und korrigiert"
		var eid string
		if err := tx.QueryRow(ctx, `INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, pending, infrastructure_id, origin_via)
			VALUES (pdh_uuid_v7(), $1::uuid, $2::time_ref_type, $3::uuid, $4, $5, $6, $7, false, NULLIF($8, '')::uuid, $9) RETURNING id::text`,
			owner, rt, refID, desc, ns, ne, mins, infra, zeitViaPDH).Scan(&eid); err != nil {
			attBack(w, r, "err")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE work_sessions SET time_entry_id = $2::uuid WHERE id = $1::uuid`, id, eid); err != nil {
			attBack(w, r, "err")
			return
		}
	}
	newT := fmt.Sprintf("%s, %s–%s", what, ns.In(workLocation).Format(attTimeFmt), ne.In(workLocation).Format("15:04"))
	if err := h.attHist(ctx, tx, owner, "work", "edit", id, oldT, newT, reason, by); err != nil || tx.Commit(ctx) != nil {
		attBack(w, r, "err")
		return
	}
	itemHist("geändert", newT)
	attBack(w, r, "ok")
}
