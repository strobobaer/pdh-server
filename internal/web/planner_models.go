package web

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Schichtmodelle & Vorlagen im Planer. Eine Vorlage legt in einem Schritt
// ein Schichtmodell (Schichten mit Zeiten und Farben) und das passende
// Rotationsmuster mit Mannschaften an. Danach lässt sich alles anpassen und
// per „Plan erzeugen“ auf Mitarbeiter verteilen: Mannschaft n läuft um
// (n-1) × Versatz Tage verschoben.

type tplShift struct {
	Short, Name, Start, End, Color string
	Night                          bool
	MinStaff                       int
}

type shiftTemplate struct {
	Key, Name, Description string
	Shifts                 []tplShift
	Pattern                []string // je Tag Schicht-Kürzel, "" = frei
	Crews, CrewStep        int
}

// PatternText: Muster kompakt für die Anzeige („F F S S N N – –“)
func (t shiftTemplate) PatternText() string {
	var parts []string
	for _, p := range t.Pattern {
		if p == "" {
			p = "–"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

func rep(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var (
	tplF = tplShift{"F", "Frühschicht", "06:00", "14:00", "#10B981", false, 0}
	tplS = tplShift{"S", "Spätschicht", "14:00", "22:00", "#F59E0B", false, 0}
	tplN = tplShift{"N", "Nachtschicht", "22:00", "06:00", "#6366F1", true, 0}
)

var shiftTemplates = []shiftTemplate{
	{Key: "tag", Name: "Tagschicht (Mo–Fr)", Description: "Eine Schicht an Werktagen, Wochenende frei.",
		Shifts:  []tplShift{{"T", "Tagschicht", "07:00", "15:30", "#0EA5E9", false, 0}},
		Pattern: cat(rep("T", 5), rep("", 2)), Crews: 1},
	{Key: "zwei", Name: "2-Schicht Früh/Spät", Description: "Früh- und Spätschicht Mo–Fr, wöchentlich wechselnd. Zwei Mannschaften um eine Woche versetzt.",
		Shifts:  []tplShift{tplF, tplS},
		Pattern: cat(rep("F", 5), rep("", 2), rep("S", 5), rep("", 2)), Crews: 2, CrewStep: 7},
	{Key: "drei", Name: "3-Schicht Früh/Spät/Nacht", Description: "Drei Schichten Mo–Fr, wöchentlich vorwärts wechselnd (Früh → Spät → Nacht). Drei Mannschaften.",
		Shifts:  []tplShift{tplF, tplS, tplN},
		Pattern: cat(rep("F", 5), rep("", 2), rep("S", 5), rep("", 2), rep("N", 5), rep("", 2)), Crews: 3, CrewStep: 7},
	{Key: "konti4", Name: "4-Schicht Kontischicht", Description: "Rund um die Uhr, 7 Tage: je 2 Früh, 2 Spät, 2 Nacht, 2 frei. Vier Mannschaften um je 2 Tage versetzt.",
		Shifts:  []tplShift{tplF, tplS, tplN},
		Pattern: []string{"F", "F", "S", "S", "N", "N", "", ""}, Crews: 4, CrewStep: 2},
	{Key: "konti5", Name: "5-Schicht Kontischicht", Description: "Rund um die Uhr mit kürzerer Wochenarbeitszeit: 2 Früh, 2 Spät, 2 Nacht, 4 frei. Fünf Mannschaften.",
		Shifts:  []tplShift{tplF, tplS, tplN},
		Pattern: []string{"F", "F", "S", "S", "N", "N", "", "", "", ""}, Crews: 5, CrewStep: 2},
	{Key: "panama", Name: "12-Stunden Tag/Nacht (2-2-3)", Description: "Zwei 12-Stunden-Schichten im Panama-Rhythmus (2 an, 2 frei, 3 an …). Vier Mannschaften um je eine Woche versetzt.",
		Shifts: []tplShift{{"T", "Tag (12 h)", "06:00", "18:00", "#0EA5E9", false, 0}, {"N", "Nacht (12 h)", "18:00", "06:00", "#6366F1", true, 0}},
		Pattern: []string{"T", "T", "", "", "T", "T", "T", "", "", "T", "T", "", "", "",
			"N", "N", "", "", "N", "N", "N", "", "", "N", "N", "", "", ""}, Crews: 4, CrewStep: 7},
	{Key: "bereitschaft", Name: "Rufbereitschaft wöchentlich", Description: "Eine Woche Bereitschaft, dann frei – reihum. Vier Mannschaften (Personen) um je eine Woche versetzt.",
		Shifts:  []tplShift{{"R", "Rufbereitschaft", "00:00", "23:59", "#EF4444", false, 1}},
		Pattern: cat(rep("R", 7), rep("", 21)), Crews: 4, CrewStep: 7},
}

func templateByKey(k string) *shiftTemplate {
	for i := range shiftTemplates {
		if shiftTemplates[i].Key == k {
			return &shiftTemplates[i]
		}
	}
	return nil
}

type setupShift struct {
	ID, Name, Short, Start, End, Color string
	MinStaff, Break, Sort              int
	Night, Active                      bool
	Used                               int
}

type setupPattern struct {
	ID, Name        string
	Cycle           int
	Crews, CrewStep int
	Days            []string // je Tag shift_id oder ""
	Slots           []patternSlot
	Active          bool
}

type setupModel struct {
	plannerModel
	Shifts   []setupShift
	Patterns []setupPattern
}

type setupPlan struct {
	ID, Name, Model, From, To, Status string
	Count                             int
}

type setupClosure struct{ Day, Label, Name string }

type setupTabData struct {
	Templates   []shiftTemplate
	Models      []setupModel
	Plans       []setupPlan
	Users       []plannerUser
	States      []struct{ Code, Name string }
	State       string
	VacationDef string
	Closures    []setupClosure
	Holidays    []setupClosure
	Year        int
	Today       string
	NextMonday  string
	DutyTypes   []dutyType // Sonderdienste (planner_duties.go)
	DutyKinds   []struct{ Key, Label, Icon string }
	QualTitles  []string
	CanDuties   bool
}

func (h *Handler) setupTab(r *http.Request) *setupTabData {
	ctx := r.Context()
	now := time.Now()
	d := &setupTabData{Templates: shiftTemplates, States: germanStates, Year: now.Year(), Today: now.Format(dayISO),
		NextMonday: mondayOf(now).AddDate(0, 0, 7).Format(dayISO)}
	d.State = h.appSetting(ctx, "planner_holiday_state", "")
	d.CanDuties = h.hasPerm(r, permDuties)
	d.DutyTypes, d.DutyKinds, d.QualTitles = h.dutyTypes(ctx, false), dutyKinds, h.qualificationTitles(ctx)
	d.VacationDef = h.appSetting(ctx, "planner_vacation_default", "30")
	for _, m := range h.plannerModels(ctx, true) {
		sm := setupModel{plannerModel: m}
		if rows, err := h.db.Query(ctx, `SELECT d.id::text, d.name, d.short_name, to_char(d.start_time, 'HH24:MI'), to_char(d.end_time, 'HH24:MI'),
				d.color, d.min_staff, d.break_minutes, d.sort_order, d.is_night, d.active,
				(SELECT COUNT(*) FROM shift_assignments sa WHERE sa.shift_id = d.id)
			FROM shift_definitions d WHERE d.model_id = $1::uuid ORDER BY d.sort_order, d.start_time, d.name`, m.ID); err == nil {
			for rows.Next() {
				var s setupShift
				if rows.Scan(&s.ID, &s.Name, &s.Short, &s.Start, &s.End, &s.Color, &s.MinStaff, &s.Break, &s.Sort, &s.Night, &s.Active, &s.Used) == nil {
					sm.Shifts = append(sm.Shifts, s)
				}
			}
			rows.Close()
		}
		if rows, err := h.db.Query(ctx, `SELECT p.id::text, p.name, p.cycle_length, p.crews, p.crew_step, p.active
			FROM shift_rotation_patterns p WHERE p.model_id = $1::uuid ORDER BY p.active DESC, p.name`, m.ID); err == nil {
			for rows.Next() {
				var p setupPattern
				if rows.Scan(&p.ID, &p.Name, &p.Cycle, &p.Crews, &p.CrewStep, &p.Active) == nil {
					p.Days = make([]string, p.Cycle)
					sm.Patterns = append(sm.Patterns, p)
				}
			}
			rows.Close()
		}
		for i := range sm.Patterns {
			p := &sm.Patterns[i]
			if rows, err := h.db.Query(ctx, `SELECT day_index, COALESCE(shift_id::text, '') FROM shift_rotation_pattern_days WHERE pattern_id = $1::uuid`, p.ID); err == nil {
				for rows.Next() {
					var i int
					var sid string
					if rows.Scan(&i, &sid) == nil && i >= 0 && i < len(p.Days) {
						p.Days[i] = sid
					}
				}
				rows.Close()
			}
		}
		for i := range sm.Patterns {
			sm.Patterns[i].Slots = sm.Patterns[i].slots()
		}
		d.Models = append(d.Models, sm)
	}
	if rows, err := h.db.Query(ctx, `SELECT p.id::text, p.name, COALESCE(m.name, ''), p.start_date, p.end_date, p.status::text,
			(SELECT COUNT(*) FROM shift_assignments sa WHERE sa.plan_id = p.id)
		FROM shift_plans p LEFT JOIN shift_models m ON m.id = p.model_id
		WHERE p.end_date >= CURRENT_DATE - 60 ORDER BY p.start_date DESC LIMIT 50`); err == nil {
		for rows.Next() {
			var p setupPlan
			var a, b time.Time
			if rows.Scan(&p.ID, &p.Name, &p.Model, &a, &b, &p.Status, &p.Count) == nil {
				p.From, p.To = a.Format("02.01.2006"), b.Format("02.01.2006")
				d.Plans = append(d.Plans, p)
			}
		}
		rows.Close()
	}
	d.Users = h.plannerUsers(ctx, "", "")
	if rows, err := h.db.Query(ctx, `SELECT day, name FROM planner_closures WHERE day >= CURRENT_DATE - 30 ORDER BY day`); err == nil {
		for rows.Next() {
			var t time.Time
			var c setupClosure
			if rows.Scan(&t, &c.Name) == nil {
				c.Day, c.Label = t.Format(dayISO), weekdayShort[t.Weekday()]+" "+t.Format("02.01.2006")
				d.Closures = append(d.Closures, c)
			}
		}
		rows.Close()
	}
	hol := germanHolidays(d.Year, d.State)
	for k, n := range hol {
		t := mustDate(k)
		d.Holidays = append(d.Holidays, setupClosure{Day: k, Label: weekdayShort[t.Weekday()] + " " + t.Format("02.01."), Name: n})
	}
	sortClosures(d.Holidays)
	return d
}

func sortClosures(c []setupClosure) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Day < c[j-1].Day; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (h *Handler) requirePlan(w http.ResponseWriter, r *http.Request) bool {
	if !h.canShiftPlan(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return false
	}
	return true
}

// PlannerTemplateApplyWeb: POST /planner/templates/{key}/apply
func (h *Handler) PlannerTemplateApplyWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	t := templateByKey(chi.URLParam(r, "key"))
	if t == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	name := firstNonEmpty(strings.TrimSpace(r.FormValue("name")), t.Name)
	uid := getUser(r).ID
	tx, err := h.db.Begin(ctx)
	if err != nil {
		plannerBack(w, r, "models", "", "", err)
		return
	}
	defer tx.Rollback(ctx)
	var modelID string
	if err = tx.QueryRow(ctx, `INSERT INTO shift_models (name, description, active, template_key) VALUES ($1, $2, true, $3) RETURNING id::text`,
		truncateRunes(name, 100), t.Description, t.Key).Scan(&modelID); err != nil {
		plannerBack(w, r, "models", "", "", err)
		return
	}
	ids := map[string]string{}
	for i, s := range t.Shifts {
		var id string
		if err = tx.QueryRow(ctx, `INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time, color, is_night, min_staff, sort_order)
			VALUES ($1::uuid, $2, $3, $4::time, $5::time, $6, $7, $8, $9) RETURNING id::text`,
			modelID, s.Name, s.Short, s.Start, s.End, s.Color, s.Night, s.MinStaff, i).Scan(&id); err != nil {
			plannerBack(w, r, "models", "", "", err)
			return
		}
		ids[s.Short] = id
	}
	var patternID string
	if err = tx.QueryRow(ctx, `INSERT INTO shift_rotation_patterns (model_id, name, cycle_length, active, created_by, crews, crew_step, template_key)
		VALUES ($1::uuid, $2, $3, true, $4::uuid, $5, $6, $7) RETURNING id::text`,
		modelID, "Rhythmus "+t.Name, len(t.Pattern), nullID(uid), t.Crews, t.CrewStep, t.Key).Scan(&patternID); err != nil {
		plannerBack(w, r, "models", "", "", err)
		return
	}
	for i, p := range t.Pattern {
		if p == "" {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO shift_rotation_pattern_days (pattern_id, day_index, shift_id) VALUES ($1::uuid, $2, $3::uuid)`,
			patternID, i, ids[p]); err != nil {
			plannerBack(w, r, "models", "", "", err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		plannerBack(w, r, "models", "", "", err)
		return
	}
	componentLog("planer").Info().Str("vorlage", t.Key).Str("modell", modelID).Str("von", uid).Msg("schichtmodell aus vorlage")
	plannerBack(w, r, "models", "&open="+modelID, fmt.Sprintf("Schichtmodell „%s“ mit %d Schichten und Rotationsmuster angelegt – jetzt anpassen oder unten einen Plan erzeugen", name, len(t.Shifts)), nil)
}

// PlannerModelSaveWeb: POST /planner/models/{id} (id=new: leeres Modell anlegen)
func (h *Handler) PlannerModelSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	name := truncateRunes(strings.TrimSpace(r.FormValue("name")), 100)
	if name == "" {
		plannerBack(w, r, "models", "", "", errors.New("Bitte einen Namen angeben"))
		return
	}
	desc := truncateRunes(strings.TrimSpace(r.FormValue("description")), 500)
	if id == "new" {
		err := h.db.QueryRow(ctx, `INSERT INTO shift_models (name, description, active) VALUES ($1, $2, true) RETURNING id::text`, name, desc).Scan(&id)
		plannerBack(w, r, "models", "&open="+id, "Schichtmodell angelegt – jetzt Schichten hinzufügen", err)
		return
	}
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	_, err := h.db.Exec(ctx, `UPDATE shift_models SET name = $2, description = $3, active = $4 WHERE id = $1::uuid`, id, name, desc, r.FormValue("active") == "1")
	plannerBack(w, r, "models", "&open="+id, "Schichtmodell gespeichert", err)
}

// PlannerModelDeleteWeb: POST /planner/models/{id}/delete – nur ohne Einteilungen.
func (h *Handler) PlannerModelDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	var used int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM shift_assignments sa JOIN shift_definitions d ON d.id = sa.shift_id WHERE d.model_id = $1::uuid`, id).Scan(&used)
	if used > 0 {
		plannerBack(w, r, "models", "&open="+id, "", fmt.Errorf("Das Modell hat %d Einteilungen – bitte stattdessen deaktivieren", used))
		return
	}
	tx, err := h.db.Begin(ctx)
	if err == nil {
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `DELETE FROM shift_plans WHERE model_id = $1::uuid`, id); err == nil {
			if _, err = tx.Exec(ctx, `DELETE FROM shift_rotation_patterns WHERE model_id = $1::uuid`, id); err == nil {
				if _, err = tx.Exec(ctx, `DELETE FROM shift_models WHERE id = $1::uuid`, id); err == nil {
					err = tx.Commit(ctx)
				}
			}
		}
	}
	plannerBack(w, r, "models", "", "Schichtmodell gelöscht", err)
}

func shiftFromForm(r *http.Request) (setupShift, error) {
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	s := setupShift{Name: truncateRunes(v("name"), 100), Short: truncateRunes(v("short_name"), 10), Start: v("start_time"), End: v("end_time"),
		Color: v("color"), MinStaff: atoiDefault(v("min_staff"), 0), Break: atoiDefault(v("break_minutes"), 0), Sort: atoiDefault(v("sort_order"), 0),
		Night: r.FormValue("is_night") == "1", Active: r.FormValue("active") != "0"}
	if v("min_staff") == "0" {
		s.MinStaff = 0
	}
	if s.Name == "" || s.Short == "" {
		return s, errors.New("Name und Kürzel sind Pflicht")
	}
	if !hhmmRe.MatchString(s.Start) || !hhmmRe.MatchString(s.End) {
		return s, errors.New("Beginn und Ende bitte als HH:MM")
	}
	if !colorRe.MatchString(s.Color) {
		s.Color = "#3B82F6"
	}
	if s.End <= s.Start && s.End != "00:00" {
		s.Night = true // endet am Folgetag
	}
	return s, nil
}

// PlannerShiftAddWeb: POST /planner/models/{id}/shifts
func (h *Handler) PlannerShiftAddWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	s, err := shiftFromForm(r)
	if err == nil {
		_, err = h.db.Exec(r.Context(), `INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time, color, is_night, min_staff, break_minutes, sort_order)
			VALUES ($1::uuid, $2, $3, $4::time, $5::time, $6, $7, $8, $9, $10)`, id, s.Name, s.Short, s.Start, s.End, s.Color, s.Night, s.MinStaff, s.Break, s.Sort)
	}
	plannerBack(w, r, "models", "&open="+id, "Schicht hinzugefügt", err)
}

// PlannerShiftSaveWeb: POST /planner/shifts/{id} (action=delete löscht, nur wenn unbenutzt)
func (h *Handler) PlannerShiftSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	var model string
	if err := h.db.QueryRow(ctx, `SELECT model_id::text FROM shift_definitions WHERE id = $1::uuid`, id).Scan(&model); err != nil {
		plannerBack(w, r, "models", "", "", errors.New("Schicht nicht gefunden"))
		return
	}
	if r.FormValue("action") == "delete" {
		var used int
		_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM shift_assignments WHERE shift_id = $1::uuid`, id).Scan(&used)
		if used > 0 {
			plannerBack(w, r, "models", "&open="+model, "", fmt.Errorf("Die Schicht hat %d Einteilungen – bitte stattdessen deaktivieren", used))
			return
		}
		_, err := h.db.Exec(ctx, `DELETE FROM shift_rotation_pattern_days WHERE shift_id = $1::uuid`, id)
		if err == nil {
			_, err = h.db.Exec(ctx, `DELETE FROM shift_definitions WHERE id = $1::uuid`, id)
		}
		plannerBack(w, r, "models", "&open="+model, "Schicht gelöscht", err)
		return
	}
	s, err := shiftFromForm(r)
	if err == nil {
		_, err = h.db.Exec(ctx, `UPDATE shift_definitions SET name = $2, short_name = $3, start_time = $4::time, end_time = $5::time, color = $6,
			is_night = $7, min_staff = $8, break_minutes = $9, sort_order = $10, active = $11 WHERE id = $1::uuid`,
			id, s.Name, s.Short, s.Start, s.End, s.Color, s.Night, s.MinStaff, s.Break, s.Sort, s.Active)
	}
	plannerBack(w, r, "models", "&open="+model, "Schicht gespeichert", err)
}

// PlannerPatternSaveWeb: POST /planner/patterns/{id} (id=new mit model_id) –
// Name, Zykluslänge, Mannschaften, Versatz und je Tag die Schicht (day_0 …).
func (h *Handler) PlannerPatternSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	model := v("model_id")
	if r.FormValue("action") == "delete" && id != "new" {
		_, err := h.db.Exec(ctx, `DELETE FROM shift_rotation_patterns WHERE id = $1::uuid`, id)
		plannerBack(w, r, "models", "&open="+model, "Rotationsmuster gelöscht", err)
		return
	}
	name := truncateRunes(v("name"), 100)
	cycle := atoiDefault(v("cycle_length"), 7)
	crews := atoiDefault(v("crews"), 1)
	step := atoiDefault(v("crew_step"), 0)
	if v("crew_step") == "0" {
		step = 0
	}
	if name == "" || cycle < 1 || cycle > 84 || crews < 1 || crews > 12 || step < 0 || step > cycle {
		plannerBack(w, r, "models", "&open="+model, "", errors.New("Name, Zykluslänge (1–84 Tage), Mannschaften (1–12) und Versatz prüfen"))
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		plannerBack(w, r, "models", "&open="+model, "", err)
		return
	}
	defer tx.Rollback(ctx)
	if id == "new" {
		err = tx.QueryRow(ctx, `INSERT INTO shift_rotation_patterns (model_id, name, cycle_length, active, created_by, crews, crew_step)
			VALUES ($1::uuid, $2, $3, true, $4::uuid, $5, $6) RETURNING id::text`, model, name, cycle, nullID(getUser(r).ID), crews, step).Scan(&id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE shift_rotation_patterns SET name = $2, cycle_length = $3, crews = $4, crew_step = $5, active = $6 WHERE id = $1::uuid`,
			id, name, cycle, crews, step, r.FormValue("active") != "0")
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM shift_rotation_pattern_days WHERE pattern_id = $1::uuid`, id)
	}
	for i := 0; err == nil && i < cycle; i++ {
		sid := r.FormValue("day_" + strconv.Itoa(i))
		if !uuidInPathRe.MatchString(sid) {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO shift_rotation_pattern_days (pattern_id, day_index, shift_id)
			SELECT $1::uuid, $2, d.id FROM shift_definitions d WHERE d.id = $3::uuid AND d.model_id = $4::uuid`, id, i, sid, model)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	plannerBack(w, r, "models", "&open="+model, "Rotationsmuster gespeichert", err)
}

// PlannerGenerateWeb: POST /planner/plans/generate – Plan aus einem
// Rotationsmuster: user=…, crew_<user>=n; vorhandene Einteilungen und
// genehmigte Abwesenheiten bleiben unberührt.
func (h *Handler) PlannerGenerateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	patternID := v("pattern_id")
	var model string
	var cycle, step int
	if err := h.db.QueryRow(ctx, `SELECT model_id::text, cycle_length, crew_step FROM shift_rotation_patterns WHERE id = $1::uuid`, patternID).Scan(&model, &cycle, &step); err != nil || cycle < 1 {
		plannerBack(w, r, "models", "", "", errors.New("Bitte ein Rotationsmuster wählen"))
		return
	}
	from, err1 := time.ParseInLocation(dayISO, v("start_date"), time.Local)
	to, err2 := time.ParseInLocation(dayISO, v("end_date"), time.Local)
	if err1 != nil || err2 != nil || to.Before(from) || to.Sub(from) > 400*24*time.Hour {
		plannerBack(w, r, "models", "&open="+model, "", errors.New("Zeitraum ungültig (höchstens gut ein Jahr)"))
		return
	}
	days := map[int]string{}
	if rows, err := h.db.Query(ctx, `SELECT day_index, shift_id::text FROM shift_rotation_pattern_days WHERE pattern_id = $1::uuid AND shift_id IS NOT NULL`, patternID); err == nil {
		for rows.Next() {
			var i int
			var s string
			if rows.Scan(&i, &s) == nil {
				days[i] = s
			}
		}
		rows.Close()
	}
	type member struct {
		id     string
		offset int
	}
	var members []member
	for _, u := range r.Form["user"] {
		if !uuidInPathRe.MatchString(u) {
			continue
		}
		crew := atoiDefault(r.FormValue("crew_"+u), 1)
		members = append(members, member{u, (crew - 1) * step})
	}
	if len(members) == 0 {
		plannerBack(w, r, "models", "&open="+model, "", errors.New("Bitte Mitarbeiter auswählen"))
		return
	}
	uid := getUser(r).ID
	publish := r.FormValue("publish") == "1"
	name := firstNonEmpty(v("name"), fmt.Sprintf("Plan %s – %s", from.Format("02.01."), to.Format("02.01.2006")))
	tx, err := h.db.Begin(ctx)
	if err != nil {
		plannerBack(w, r, "models", "&open="+model, "", err)
		return
	}
	defer tx.Rollback(ctx)
	var planID string
	status := "draft"
	if publish {
		status = "published"
	}
	if err = tx.QueryRow(ctx, `INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by, published_by, published_at)
		VALUES ($1, $2::uuid, $3, $4, $5::text::shift_plan_status, $6::uuid, CASE WHEN $5::text = 'published' THEN $6::uuid END, CASE WHEN $5::text = 'published' THEN NOW() END)
		RETURNING id::text`, truncateRunes(name, 200), model, from, to, status, nullID(uid)).Scan(&planID); err != nil {
		plannerBack(w, r, "models", "&open="+model, "", err)
		return
	}
	created := 0
	for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
		off := int(t.Sub(from).Hours()/24 + 0.5)
		for _, m := range members {
			sid, ok := days[((off-m.offset)%cycle+cycle)%cycle]
			if !ok {
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by, plan_id)
				SELECT $1::uuid, $2::uuid, $3::date, '', $4::uuid, $5::uuid
				WHERE NOT EXISTS (SELECT 1 FROM absences a WHERE a.user_id = $1::uuid AND a.status = 'approved' AND $3::date BETWEEN a.start_date AND a.end_date)
				ON CONFLICT (user_id, date) DO NOTHING`, m.id, sid, t.Format(dayISO), nullID(uid), planID)
			if err != nil {
				plannerBack(w, r, "models", "&open="+model, "", err)
				return
			}
			created += int(tag.RowsAffected())
		}
	}
	if err = tx.Commit(ctx); err != nil {
		plannerBack(w, r, "models", "&open="+model, "", err)
		return
	}
	msg := fmt.Sprintf("Plan „%s“ erzeugt: %d Einteilungen", name, created)
	if publish {
		msg += " – veröffentlicht"
	} else {
		msg += " – als Entwurf (nur Planende sehen ihn, gestrichelt), unten veröffentlichen"
	}
	componentLog("planer").Info().Str("plan", planID).Int("einteilungen", created).Int("personen", len(members)).Str("von", uid).Msg("plan erzeugt")
	plannerBack(w, r, "models", "&open="+model, msg, nil)
}

// PlannerPlanActionWeb: POST /planner/plans/{id}/{action} (publish|unpublish|delete)
func (h *Handler) PlannerPlanActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	id, action := chi.URLParam(r, "id"), chi.URLParam(r, "action")
	uid := getUser(r).ID
	var err error
	var msg string
	switch action {
	case "publish":
		_, err = h.db.Exec(ctx, `UPDATE shift_plans SET status = 'published', published_by = $2::uuid, published_at = NOW() WHERE id = $1::uuid`, id, nullID(uid))
		msg = "Plan veröffentlicht – alle sehen ihn im Schichtplan"
	case "unpublish":
		_, err = h.db.Exec(ctx, `UPDATE shift_plans SET status = 'draft' WHERE id = $1::uuid`, id)
		msg = "Plan zurück auf Entwurf"
	case "delete":
		// Einteilungen des Plans entfallen (Kaskade), von Hand geänderte gehören keinem Plan mehr
		_, err = h.db.Exec(ctx, `DELETE FROM shift_plans WHERE id = $1::uuid`, id)
		msg = "Plan und seine Einteilungen gelöscht"
	default:
		http.NotFound(w, r)
		return
	}
	plannerBack(w, r, "models", "", msg, err)
}

// PlannerSettingsWeb: POST /planner/settings – Bundesland, Standard-Urlaub.
func (h *Handler) PlannerSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	state := r.FormValue("state")
	ok := false
	for _, s := range germanStates {
		if s.Code == state {
			ok = true
		}
	}
	vac, err := parseQty(firstNonEmpty(r.FormValue("vacation_default"), "30"))
	if !ok || err != nil || vac < 0 || vac > 100 {
		plannerBack(w, r, "settings", "", "", errors.New("Angaben ungültig"))
		return
	}
	if err = h.setAppSetting(ctx, "planner_holiday_state", state); err == nil {
		err = h.setAppSetting(ctx, "planner_vacation_default", strconv.FormatFloat(vac, 'f', -1, 64))
	}
	plannerBack(w, r, "settings", "", "Einstellungen gespeichert", err)
}

// PlannerClosureWeb: POST /planner/closures (day, name) bzw. /planner/closures/{day}/delete
func (h *Handler) PlannerClosureWeb(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlan(w, r) {
		return
	}
	ctx := r.Context()
	if day := chi.URLParam(r, "day"); day != "" {
		_, err := h.db.Exec(ctx, `DELETE FROM planner_closures WHERE day = $1::date`, day)
		plannerBack(w, r, "settings", "", "Betriebsruhetag entfernt", err)
		return
	}
	_ = r.ParseForm()
	from, err1 := time.ParseInLocation(dayISO, r.FormValue("day"), time.Local)
	to := from
	if s := r.FormValue("day_to"); s != "" {
		var err error
		if to, err = time.ParseInLocation(dayISO, s, time.Local); err != nil {
			err1 = err
		}
	}
	name := truncateRunes(strings.TrimSpace(r.FormValue("name")), 120)
	if err1 != nil || name == "" || to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		plannerBack(w, r, "settings", "", "", errors.New("Datum und Bezeichnung angeben (höchstens zwei Monate am Stück)"))
		return
	}
	n := 0
	for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
		if _, err := h.db.Exec(ctx, `INSERT INTO planner_closures (day, name, created_by) VALUES ($1, $2, $3::uuid)
			ON CONFLICT (day) DO UPDATE SET name = EXCLUDED.name`, t, name, nullID(getUser(r).ID)); err != nil {
			plannerBack(w, r, "settings", "", "", err)
			return
		}
		n++
	}
	plannerBack(w, r, "settings", "", fmt.Sprintf("%d Betriebsruhetag(e) eingetragen", n), nil)
}

// patternSlot: ein Tag im Rotationsmuster (für das Formular).
type patternSlot struct {
	Index        int
	Label, Shift string
}

var weekdayMoFirst = [...]string{"Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"}

func (p setupPattern) slots() []patternSlot {
	out := make([]patternSlot, len(p.Days))
	for i, s := range p.Days {
		out[i] = patternSlot{Index: i, Label: fmt.Sprintf("Tag %d · %s", i+1, weekdayMoFirst[i%7]), Shift: s}
	}
	return out
}
