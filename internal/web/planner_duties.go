package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Sonderdienste und Ziehen im Schichtplan (Migration 150).
//
//   - Dienstarten (duty_types): Bereitschaft, besondere Tätigkeit oder
//     Zusatzaufgabe, optional mit nötiger Qualifikation aus dem Personalstamm
//     (user_qualifications.title, am Tag noch gültig).
//   - Einteilung tageweise (duty_assignments), unabhängig von der Schicht;
//     mehrere Dienste je Person und Tag sind möglich.
//   - Schicht- und Dienst-Einträge lassen sich im Raster ziehen: verschieben,
//     mit Strg/„Kopieren“ kopieren und am Rand über weitere Tage ziehen
//     (PlannerGridMoveWeb, PlannerGridFillWeb). Genehmigte Abwesenheit und
//     fehlende Qualifikation werden übersprungen und gemeldet.

const permDuties = "shifts.duties"

// userPhoneSQL: Telefonnummer einer Person (Alias u) – dienstliches Handy,
// sonst Durchwahl, sonst Telefon aus den Grunddaten.
const userPhoneSQL = `COALESCE(NULLIF(u.phone_mobile, ''), NULLIF(u.phone_internal, ''), NULLIF(u.phone, ''), '')`

var dutyKinds = []struct{ Key, Label, Icon string }{
	{"standby", "Bereitschaft", "ti-phone-call"},
	{"duty", "Besondere Tätigkeit", "ti-star"},
	{"task", "Zusatzaufgabe", "ti-list-check"},
}

func dutyKindLabel(k string) string {
	for _, d := range dutyKinds {
		if d.Key == k {
			return d.Label
		}
	}
	return k
}

func dutyKindIcon(k string) string {
	for _, d := range dutyKinds {
		if d.Key == k {
			return d.Icon
		}
	}
	return "ti-star"
}

type dutyType struct {
	ID, Name, Short, Color, Kind, Qualification, DeptID string
	MinStaff, Sort                                      int
	Active, ShowPhone                                   bool
	Used                                                int // Einteilungen gesamt
}

func (d dutyType) KindLabel() string { return dutyKindLabel(d.Kind) }
func (d dutyType) Icon() string      { return dutyKindIcon(d.Kind) }

func (h *Handler) dutyTypes(ctx context.Context, activeOnly bool) []dutyType {
	q := `SELECT t.id::text, t.name, t.short_name, t.color, t.kind, t.qualification, COALESCE(t.department_id::text, ''),
		t.min_staff, t.sort_order, t.active, t.show_phone, (SELECT COUNT(*) FROM duty_assignments a WHERE a.duty_type_id = t.id)
		FROM duty_types t`
	if activeOnly {
		q += ` WHERE t.active`
	}
	rows, err := h.db.Query(ctx, q+` ORDER BY t.sort_order, lower(t.name)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dutyType
	for rows.Next() {
		var t dutyType
		if rows.Scan(&t.ID, &t.Name, &t.Short, &t.Color, &t.Kind, &t.Qualification, &t.DeptID, &t.MinStaff, &t.Sort, &t.Active, &t.ShowPhone, &t.Used) == nil {
			if !hexColorRe.MatchString(t.Color) {
				t.Color = "#F59E0B"
			}
			out = append(out, t)
		}
	}
	return out
}

// qualificationTitles: alle Qualifikationen im Personalstamm (für die Auswahl).
func (h *Handler) qualificationTitles(ctx context.Context) []string {
	var out []string
	rows, err := h.db.Query(ctx, `SELECT DISTINCT title FROM user_qualifications WHERE title <> ''
		UNION SELECT DISTINCT qualification FROM duty_types WHERE qualification <> '' ORDER BY 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// qualifiedUsers: Personen mit gültiger Qualifikation am Tag (nil = keine nötig).
func (h *Handler) qualifiedUsers(ctx context.Context, qualification string, day time.Time) map[string]bool {
	if strings.TrimSpace(qualification) == "" {
		return nil
	}
	out := map[string]bool{}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT user_id::text FROM user_qualifications
		WHERE lower(title) = lower($1) AND (valid_until IS NULL OR valid_until >= $2::date)`, qualification, day)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// absentOn: genehmigte Abwesenheit der Person am Tag ("" = anwesend).
func (h *Handler) absentOn(ctx context.Context, userID string, day time.Time) string {
	var typ string
	_ = h.db.QueryRow(ctx, `SELECT type::text FROM absences WHERE user_id = $1::uuid AND status = 'approved'
		AND $2::date BETWEEN start_date AND end_date LIMIT 1`, userID, day).Scan(&typ)
	if typ == "" {
		return ""
	}
	return absenceTypeLabels[typ]
}

// canPlanUser: darf die angemeldete Person diese Person einteilen?
// Rechte auf Abteilungsebene gelten für die Abteilung der eingeteilten Person.
func (h *Handler) canPlanUser(r *http.Request, perm, userID string) bool {
	if !h.hasPerm(r, perm) {
		return false
	}
	var dept string
	_ = h.db.QueryRow(r.Context(), `SELECT COALESCE(department_id::text, '') FROM users WHERE id = $1::uuid`, userID).Scan(&dept)
	return h.hasPermIn(r, perm, deptList(dept))
}

// ── Raster im Schichtplan ────────────────────────────────────

type plannerDutyRow struct {
	Type      dutyType
	Cells     []plannerCell
	Qualified []string // Personen mit der nötigen Qualifikation (für die Auswahl)
}

// fillDutyWeek: Zeilen für die Sonderdienste der Woche.
func (h *Handler) fillDutyWeek(r *http.Request, d *PlannerData, start time.Time) {
	ctx := r.Context()
	d.CanDuties = h.hasPerm(r, permDuties)
	types := h.dutyTypes(ctx, true)
	if len(types) == 0 {
		return
	}
	idx := map[string]int{}
	for i, t := range types {
		idx[t.ID] = i
		row := plannerDutyRow{Type: t, Cells: make([]plannerCell, 7)}
		if q := h.qualifiedUsers(ctx, t.Qualification, start); q != nil {
			row.Qualified = keys(q)
		}
		d.DutyRows = append(d.DutyRows, row)
	}
	inList := map[string]bool{}
	for _, u := range d.Users {
		inList[u.ID] = true
	}
	rows, err := h.db.Query(ctx, `SELECT a.duty_type_id::text, a.user_id::text, a.date::text, a.note, TRIM(u.first_name || ' ' || u.last_name), `+userPhoneSQL+`
		FROM duty_assignments a JOIN users u ON u.id = a.user_id
		WHERE a.date BETWEEN $1 AND $2 ORDER BY u.last_name, u.first_name`, start, start.AddDate(0, 0, 6))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tid, uid, date, note, name string
		var phone string
		if rows.Scan(&tid, &uid, &date, &note, &name, &phone) != nil {
			continue
		}
		i, ok := idx[tid]
		if !ok || ((d.Dept != "" || d.Group != "") && !inList[uid]) {
			continue
		}
		col := int(mustDate(date).Sub(start).Hours()/24 + 0.5)
		if col < 0 || col > 6 {
			continue
		}
		c := &d.DutyRows[i].Cells[col]
		p := plannerPerson{ID: uid, Name: name, Short: nameInitials(name), Note: note}
		if d.DutyRows[i].Type.ShowPhone {
			p.Phone = phone
		}
		c.People = append(c.People, p)
	}
	for i := range d.DutyRows {
		for j := range d.DutyRows[i].Cells {
			c := &d.DutyRows[i].Cells[j]
			c.Under = d.DutyRows[i].Type.MinStaff > 0 && len(c.People) < d.DutyRows[i].Type.MinStaff
		}
	}
}

// dutyTypeByID: Dienstart (aktiv).
func (h *Handler) dutyTypeByID(ctx context.Context, id string) (*dutyType, error) {
	for _, t := range h.dutyTypes(ctx, true) {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, errors.New("Dienstart nicht gefunden")
}

// assignDuty: Person für einen Dienst an einem Tag eintragen (prüft Abwesenheit und Qualifikation).
func (h *Handler) assignDuty(ctx context.Context, t *dutyType, userID string, day time.Time, by string) error {
	if why := h.absentOn(ctx, userID, day); why != "" {
		return fmt.Errorf("%s: %s", day.Format("02.01."), why)
	}
	if q := h.qualifiedUsers(ctx, t.Qualification, day); q != nil && !q[userID] {
		return fmt.Errorf("%s: Qualifikation „%s“ fehlt oder ist abgelaufen", day.Format("02.01."), t.Qualification)
	}
	_, err := h.db.Exec(ctx, `INSERT INTO duty_assignments (duty_type_id, user_id, date, created_by) VALUES ($1::uuid, $2::uuid, $3::date, $4::uuid)
		ON CONFLICT (duty_type_id, user_id, date) DO NOTHING`, t.ID, userID, day, nullID(by))
	return err
}

// assignShift: Person in eine Schicht an einem Tag eintragen. overwrite=false
// lässt eine andere Schicht der Person an dem Tag stehen (wird gemeldet).
func (h *Handler) assignShift(ctx context.Context, shiftID, userID string, day time.Time, by string, overwrite bool) error {
	if why := h.absentOn(ctx, userID, day); why != "" {
		return fmt.Errorf("%s: %s", day.Format("02.01."), why)
	}
	if !overwrite {
		var other string
		_ = h.db.QueryRow(ctx, `SELECT sd.name FROM shift_assignments sa JOIN shift_definitions sd ON sd.id = sa.shift_id
			WHERE sa.user_id = $1::uuid AND sa.date = $2::date AND sa.shift_id <> $3::uuid`, userID, day, shiftID).Scan(&other)
		if other != "" {
			return fmt.Errorf("%s: schon in %s", day.Format("02.01."), other)
		}
	}
	_, err := h.db.Exec(ctx, `INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by)
		VALUES ($1::uuid, $2::uuid, $3::date, '', $4::uuid)
		ON CONFLICT (user_id, date) DO UPDATE SET shift_id = EXCLUDED.shift_id, created_by = EXCLUDED.created_by,
			plan_id = CASE WHEN shift_assignments.shift_id = EXCLUDED.shift_id THEN shift_assignments.plan_id END`,
		userID, shiftID, day, nullID(by))
	return err
}

type gridReq struct {
	Kind     string `json:"kind"` // shift | duty
	UserID   string `json:"user_id"`
	FromDate string `json:"from_date"`
	FromRow  string `json:"from_row"`
	ToDate   string `json:"to_date"`
	ToRow    string `json:"to_row"`
	ToUser   string `json:"to_user"` // Kalender: auf eine andere Person ziehen ("" = dieselbe)
	Copy     bool   `json:"copy"`
}

func (h *Handler) readGridReq(w http.ResponseWriter, r *http.Request) (*gridReq, time.Time, time.Time, bool) {
	var in gridReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return nil, time.Time{}, time.Time{}, false
	}
	from, err1 := time.ParseInLocation(dayISO, in.FromDate, time.Local)
	to, err2 := time.ParseInLocation(dayISO, in.ToDate, time.Local)
	if err1 != nil || err2 != nil || !uuidInPathRe.MatchString(in.UserID) || !uuidInPathRe.MatchString(in.FromRow) || (in.ToRow != "" && !uuidInPathRe.MatchString(in.ToRow)) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Datum, Person oder Zeile ungültig"})
		return nil, from, to, false
	}
	if in.ToRow == "" {
		in.ToRow = in.FromRow
	}
	if in.ToUser == "" {
		in.ToUser = in.UserID
	} else if !uuidInPathRe.MatchString(in.ToUser) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Person ungültig"})
		return nil, from, to, false
	}
	perm := "shifts.edit"
	if in.Kind == "duty" {
		perm = permDuties
	} else {
		in.Kind = "shift"
	}
	if !h.canPlanUser(r, perm, in.UserID) || !h.canPlanUser(r, perm, in.ToUser) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung, diese Person einzuteilen."})
		return nil, from, to, false
	}
	return &in, from, to, true
}

// PlannerGridMoveWeb: POST /planner/grid/move – Eintrag verschieben oder kopieren
// (anderer Tag und/oder andere Schicht bzw. andere Dienstart).
func (h *Handler) PlannerGridMoveWeb(w http.ResponseWriter, r *http.Request) {
	in, from, to, ok := h.readGridReq(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	by := getUser(r).ID
	if from.Equal(to) && in.FromRow == in.ToRow && in.ToUser == in.UserID {
		completionJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	var err error
	if in.Kind == "duty" {
		var t *dutyType
		if t, err = h.dutyTypeByID(ctx, in.ToRow); err == nil {
			if err = h.assignDuty(ctx, t, in.ToUser, to, by); err == nil && !in.Copy {
				_, err = h.db.Exec(ctx, `DELETE FROM duty_assignments WHERE duty_type_id = $1::uuid AND user_id = $2::uuid AND date = $3::date`, in.FromRow, in.UserID, from)
			}
		}
	} else {
		// beim Verschieben auf einen anderen Tag erst den alten Eintrag entfernen,
		// sonst würde derselbe Tag (andere Schicht) als „schon belegt“ gelten
		if !in.Copy && from.Equal(to) && in.ToUser == in.UserID {
			err = h.assignShift(ctx, in.ToRow, in.UserID, to, by, true)
		} else if err = h.assignShift(ctx, in.ToRow, in.ToUser, to, by, false); err == nil && !in.Copy {
			_, err = h.db.Exec(ctx, `DELETE FROM shift_assignments WHERE shift_id = $1::uuid AND user_id = $2::uuid AND date = $3::date`, in.FromRow, in.UserID, from)
		}
	}
	if err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	componentLog("planer").Info().Str("art", in.Kind).Str("person", in.UserID).Str("von", in.FromDate).Str("nach", in.ToDate).Bool("kopie", in.Copy).Str("durch", by).Msg("eintrag gezogen")
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

// PlannerGridFillWeb: POST /planner/grid/fill – Eintrag am Rand über weitere
// Tage ziehen: die Person kommt an jedem Tag von from_date bis to_date in
// dieselbe Schicht bzw. denselben Dienst. Übersprungene Tage werden gemeldet.
func (h *Handler) PlannerGridFillWeb(w http.ResponseWriter, r *http.Request) {
	in, from, to, ok := h.readGridReq(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	by := getUser(r).ID
	if to.Before(from) {
		from, to = to, from
	}
	if to.Sub(from) > 62*24*time.Hour {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Höchstens zwei Monate auf einmal."})
		return
	}
	var t *dutyType
	if in.Kind == "duty" {
		var err error
		if t, err = h.dutyTypeByID(ctx, in.FromRow); err != nil {
			completionJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	added := 0
	var skipped []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		var err error
		if t != nil {
			err = h.assignDuty(ctx, t, in.UserID, d, by)
		} else {
			err = h.assignShift(ctx, in.FromRow, in.UserID, d, by, false)
		}
		if err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		added++
	}
	componentLog("planer").Info().Str("art", in.Kind).Str("person", in.UserID).Str("von", from.Format(dayISO)).Str("bis", to.Format(dayISO)).Int("tage", added).Str("durch", by).Msg("eintrag verlängert")
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "added": added, "skipped": skipped})
}

// PlannerDutyCellWeb: POST /planner/duties/cell – JSON {date, duty_id, user_ids[]}
// setzt die Besetzung eines Dienst-Feldes.
func (h *Handler) PlannerDutyCellWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permDuties) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung für Sonderdienste."})
		return
	}
	var in struct {
		Date    string   `json:"date"`
		DutyID  string   `json:"duty_id"`
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return
	}
	ctx := r.Context()
	day, err := time.ParseInLocation(dayISO, in.Date, time.Local)
	t, terr := h.dutyTypeByID(ctx, in.DutyID)
	if err != nil || terr != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Datum oder Dienst ungültig"})
		return
	}
	want := map[string]bool{}
	for _, u := range in.UserIDs {
		if uuidInPathRe.MatchString(u) {
			want[u] = true
		}
	}
	by := getUser(r).ID
	if _, err := h.db.Exec(ctx, `DELETE FROM duty_assignments WHERE duty_type_id = $1::uuid AND date = $2::date AND NOT (user_id::text = ANY($3))`,
		t.ID, day, keys(want)); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	var skipped []string
	for u := range want {
		if !h.canPlanUser(r, permDuties, u) {
			skipped = append(skipped, "keine Berechtigung für diese Person")
			continue
		}
		if err := h.assignDuty(ctx, t, u, day, by); err != nil {
			skipped = append(skipped, err.Error())
		}
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "skipped": skipped})
}

// PlannerDutyTypeSaveWeb: POST /planner/duty-types/{id} (id = new) – Dienstart anlegen/ändern.
func (h *Handler) PlannerDutyTypeSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permDuties) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id := chi.URLParam(r, "id")
	name := truncateRunes(strings.TrimSpace(r.FormValue("name")), 80)
	if name == "" {
		plannerBack(w, r, "settings", "", "", errors.New("Bitte einen Namen eingeben"))
		return
	}
	kind := r.FormValue("kind")
	if dutyKindLabel(kind) == kind {
		kind = "duty"
	}
	color := r.FormValue("color")
	if !hexColorRe.MatchString(color) {
		color = "#F59E0B"
	}
	minStaff, _ := strconv.Atoi(r.FormValue("min_staff"))
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	args := []any{name, truncateRunes(strings.TrimSpace(r.FormValue("short_name")), 10), color, kind,
		truncateRunes(strings.TrimSpace(r.FormValue("qualification")), 200), max(0, minStaff), sortOrder, r.FormValue("active") == "on", r.FormValue("show_phone") == "on"}
	var err error
	if id == "new" {
		_, err = h.db.Exec(r.Context(), `INSERT INTO duty_types (name, short_name, color, kind, qualification, min_staff, sort_order, active, show_phone)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8 OR true, $9)`, args...)
	} else if uuidInPathRe.MatchString(id) {
		_, err = h.db.Exec(r.Context(), `UPDATE duty_types SET name = $1, short_name = $2, color = $3, kind = $4, qualification = $5,
			min_staff = $6, sort_order = $7, active = $8, show_phone = $9 WHERE id = $10::uuid`, append(args, id)...)
	}
	plannerBack(w, r, "settings", "", "Dienstart „"+name+"“ gespeichert", err)
}

// PlannerDutyTypeDeleteWeb: POST /planner/duty-types/{id}/delete – mit
// Einteilungen nur abschalten (die Historie bleibt), sonst löschen.
func (h *Handler) PlannerDutyTypeDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permDuties) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	tag, err := h.db.Exec(ctx, `DELETE FROM duty_types t WHERE t.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM duty_assignments a WHERE a.duty_type_id = t.id)`, id)
	msg := "Dienstart gelöscht"
	if err == nil && tag.RowsAffected() == 0 {
		_, err = h.db.Exec(ctx, `UPDATE duty_types SET active = false WHERE id = $1::uuid`, id)
		msg = "Dienstart hat Einteilungen – sie wurde abgeschaltet statt gelöscht"
	}
	plannerBack(w, r, "settings", "", msg, err)
}

// PlannerAbsenceExtendWeb: POST /planner/absences/{id}/extend – JSON {to_date}:
// Abwesenheit im Kalender am Rand ziehen. Vor dem Beginn = früher anfangen,
// sonst endet sie an diesem Tag (länger oder kürzer). Genehmigende dürfen
// alle, sonst nur die eigenen beantragten.
func (h *Handler) PlannerAbsenceExtendWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ToDate string `json:"to_date"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || !uuidInPathRe.MatchString(id) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return
	}
	to, err := time.ParseInLocation(dayISO, in.ToDate, time.Local)
	if err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Datum ungültig"})
		return
	}
	ctx := r.Context()
	me := getUser(r)
	var uid, status, typ string
	var start, end time.Time
	var hs, he bool
	if err := h.db.QueryRow(ctx, `SELECT user_id::text, status::text, type::text, start_date, end_date, half_start, half_end FROM absences WHERE id = $1::uuid`, id).
		Scan(&uid, &status, &typ, &start, &end, &hs, &he); err != nil {
		completionJSON(w, http.StatusNotFound, map[string]any{"error": "Abwesenheit nicht gefunden"})
		return
	}
	if !h.canAbsenceApprove(r) && !(uid == me.ID && status == "pending" && h.hasPerm(r, "absences.request")) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Nur Genehmigende oder die Person selbst (solange beantragt) können das ändern."})
		return
	}
	if to.Before(start) {
		start = to
	} else {
		end = to
	}
	if end.Sub(start) > 366*24*time.Hour {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Höchstens ein Jahr."})
		return
	}
	days := h.absenceWorkdays(ctx, start, end, hs, he)
	if _, err := h.db.Exec(ctx, `UPDATE absences SET start_date = $2, end_date = $3, days = $4 WHERE id = $1::uuid`, id, start, end, days); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	componentLog("planer").Info().Str("abwesenheit", id).Str("von", start.Format(dayISO)).Str("bis", end.Format(dayISO)).Str("durch", me.ID).Msg("abwesenheit im kalender geändert")
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "days": days, "type": absenceTypeLabels[typ]})
}
