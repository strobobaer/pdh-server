//go:build integration

// Planungsfolien des Infobildschirms (Migration 208): Daten je Art, Zeitraum, Abteilungsauswahl, Schnittmenge mit den
// Abteilungen des Terminals, Rechte, „abwesend“ ohne Art und Grund.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalPlan ./internal/web/
package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type gtPlanSlide struct {
	Kind string        `json:"kind"`
	Plan *planSlideData `json:"plan"`
}

// planEnv: zwei Abteilungen mit je einer Person, Daten für jede Planungsart heute, morgen und in 10 Tagen.
type planEnv struct {
	*gtEnv
	deptA, deptB       string
	pA, pB             string // Personen der Abteilungen
	today, tomorrow    time.Time
	far                time.Time
	secretNote, secret string
	model              string
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	g := newGTEnv(t)
	e := g.e
	p := &planEnv{gtEnv: g, secretNote: "Geheimer Arztbesuch Notiz", secret: "sick"}
	p.deptA, p.deptB = e.dept("PA"), e.dept("PB")
	person := func(name, dept string) string {
		u := e.user(name, "employee", false)
		e.exec(`UPDATE users SET first_name = $2, last_name = $3, department_id = $4::uuid WHERE id = $1::uuid`, u, name, "Plan", dept)
		return u
	}
	p.pA, p.pB = person("Anton", p.deptA), person("Berta", p.deptB)
	now := time.Now()
	p.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	p.tomorrow, p.far = p.today.AddDate(0, 0, 1), p.today.AddDate(0, 0, 10)

	// Schichten: Früh; Anton heute, Berta heute und morgen, Anton in 10 Tagen
	model := e.id(`INSERT INTO shift_models (name) VALUES ('PlanModell') RETURNING id::text`)
	p.model = model
	e.t.Cleanup(func() { e.exec(`DELETE FROM shift_models WHERE id = $1::uuid`, model) })
	def := e.id(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time, color) VALUES ($1::uuid, 'Frühschicht', 'F', '06:00', '14:00', '#336699') RETURNING id::text`, model)
	for _, a := range []struct {
		u string
		d time.Time
	}{{p.pA, p.today}, {p.pB, p.today}, {p.pB, p.tomorrow}, {p.pA, p.far}} {
		e.exec(`INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by) VALUES ($1::uuid, $2::uuid, $3, '', $1::uuid)`, a.u, def, a.d)
	}
	e.t.Cleanup(func() { e.exec(`DELETE FROM shift_assignments WHERE shift_id = $1::uuid`, def) })

	// Abwesenheiten (genehmigt): Anton heute und morgen (krank, mit Notiz), Berta in 10 Tagen (Urlaub); eine nicht genehmigte
	e.exec(`INSERT INTO absences (user_id, type, status, start_date, end_date, days, note) VALUES ($1::uuid, 'sick', 'approved', $2, $3, 2, $4)`, p.pA, p.today, p.tomorrow, p.secretNote)
	e.exec(`INSERT INTO absences (user_id, type, status, start_date, end_date, days, note) VALUES ($1::uuid, 'vacation', 'approved', $2, $2, 1, '')`, p.pB, p.far)
	e.exec(`INSERT INTO absences (user_id, type, status, start_date, end_date, days, note) VALUES ($1::uuid, 'training', 'pending', $2, $2, 1, '')`, p.pB, p.today)
	e.t.Cleanup(func() { e.exec(`DELETE FROM absences WHERE user_id = ANY($1::uuid[])`, []string{p.pA, p.pB}) })

	// Dienste: Bereitschaft für Berta heute, Anton morgen
	dt := e.id(`INSERT INTO duty_types (name, short_name, kind) VALUES ('PlanBereitschaft', 'PB', 'standby') RETURNING id::text`)
	e.t.Cleanup(func() { e.exec(`DELETE FROM duty_types WHERE id = $1::uuid`, dt) })
	e.exec(`INSERT INTO duty_assignments (duty_type_id, user_id, date) VALUES ($1::uuid, $2::uuid, $3), ($1::uuid, $4::uuid, $5)`, dt, p.pB, p.today, p.pA, p.tomorrow)

	// Wartung: Anlage A (Abteilung A) heute fällig, Anlage B (Abteilung B) morgen, Anlage A in 10 Tagen; erledigt wird nicht gezeigt
	iA := e.infra("PWartA ", p.deptA, "")
	iB := e.infra("PWartB ", p.deptB, "")
	task := func(title, infra, status string, due time.Time) {
		e.exec(`INSERT INTO maintenance_tasks (title, infrastructure_id, status, due_date, created_by) VALUES ($1, $2::uuid, $3::maintenance_status, $4, $5::uuid)`, title, infra, status, due.Add(10*time.Hour), p.pA)
	}
	task("W-Heute-A", iA, "open", p.today)
	task("W-Morgen-B", iB, "in_progress", p.tomorrow)
	task("W-Fern-A", iA, "open", p.far)
	task("W-Erledigt-A", iA, "done", p.today)
	e.t.Cleanup(func() { e.exec(`DELETE FROM maintenance_tasks WHERE created_by = $1::uuid`, p.pA) })

	// Das globale Terminal darf überall planen und warten (Rolle „viewer“ hat shifts.view; maintenance.view ausdrücklich)
	e.grant(g.termG, "maintenance.view", "", true)
	e.warm()
	return p
}

func (p *planEnv) plans(term, dev string) []gtPlanSlide {
	p.e.t.Helper()
	rec := p.do("GET", "/terminal/slides.json", p.cookie(term, dev), nil)
	if rec.Code != 200 {
		p.e.t.Fatalf("slides.json: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Slides []gtPlanSlide `json:"slides"`
	}
	p.e.must(json.Unmarshal(rec.Body.Bytes(), &d))
	return d.Slides
}

// flat: "Datum|Gruppe|Eintrag" aller Tage einer Folie.
func flatPlan(pl *planSlideData) []string {
	var out []string
	if pl == nil {
		return []string{"<keine Folie>"}
	}
	for _, d := range pl.Days {
		for _, g := range d.Groups {
			for _, it := range g.Items {
				out = append(out, d.Date+"|"+g.Name+"|"+it)
			}
		}
	}
	return out
}

func (p *planEnv) day(d time.Time) string { return d.Format(dayISO) }

func wantPlan(t *testing.T, what string, pl *planSlideData, want ...string) {
	t.Helper()
	got := flatPlan(pl)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n  ist     %q\n  erwartet %q", what, got, want)
	}
}

// onlyMine: nur Einträge mit eigenem Titel (W-…); die gemeinsame Testdatenbank enthält weitere Wartungen.
func onlyMine(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.Contains(s, "|W-") {
			out = append(out, s)
		}
	}
	return out
}

func (p *planEnv) clear() { p.e.exec(`DELETE FROM terminal_slides`) }

func TestTerminalPlanShifts(t *testing.T) {
	p := newPlanEnv(t)
	to, tm, far := p.day(p.today), p.day(p.tomorrow), p.day(p.far)
	slide := func(s terminalSlide) *planSlideData {
		p.clear()
		s.Kind, s.DurationSeconds, s.Active = slideKindPlanShifts, 10, true
		p.slide(s)
		got := p.plans(p.termG, p.devG)
		if len(got) != 1 || got[0].Kind != slideKindPlanShifts {
			t.Fatalf("Folien: %+v", got)
		}
		return got[0].Plan
	}
	// Zeitraum heute: beide Abteilungen, nur heute
	wantPlan(t, "heute", slide(terminalSlide{PlanRange: "today"}), to+"|Frühschicht|Anton Plan", to+"|Frühschicht|Berta Plan")
	// Abteilungsauswahl
	wantPlan(t, "heute, Abteilung A", slide(terminalSlide{PlanRange: "today", DepartmentIDs: []string{p.deptA}}), to+"|Frühschicht|Anton Plan")
	wantPlan(t, "heute, Abteilung B", slide(terminalSlide{PlanRange: "today", DepartmentIDs: []string{p.deptB}}), to+"|Frühschicht|Berta Plan")
	// nächste N Tage: 2 Tage = heute + morgen, 11 Tage = auch in 10 Tagen; lange Zeiträume nur mit Einträgen
	pl := slide(terminalSlide{PlanRange: "days", PlanDays: 2})
	wantPlan(t, "2 Tage", pl, to+"|Frühschicht|Anton Plan", to+"|Frühschicht|Berta Plan", tm+"|Frühschicht|Berta Plan")
	if len(pl.Days) != 2 || pl.Label != "nächste 2 Tage" {
		t.Errorf("2 Tage: %+v", pl)
	}
	pl = slide(terminalSlide{PlanRange: "days", PlanDays: 11})
	wantPlan(t, "11 Tage", pl, to+"|Frühschicht|Anton Plan", to+"|Frühschicht|Berta Plan", tm+"|Frühschicht|Berta Plan", far+"|Frühschicht|Anton Plan")
	if len(pl.Days) != 3 {
		t.Errorf("11 Tage: nur Tage mit Einträgen, erhalten %d", len(pl.Days))
	}
	// diese und nächste Woche enthalten heute bzw. nicht
	mon := mondayOf(p.today)
	inWeek := func(d time.Time) bool { return !d.Before(mon) && d.Before(mon.AddDate(0, 0, 7)) }
	var wantWeek []string
	for _, a := range []struct {
		d time.Time
		n string
	}{{p.today, "Anton"}, {p.today, "Berta"}, {p.tomorrow, "Berta"}, {p.far, "Anton"}} {
		if inWeek(a.d) {
			wantWeek = append(wantWeek, p.day(a.d)+"|Frühschicht|"+a.n+" Plan")
		}
	}
	pl = slide(terminalSlide{PlanRange: "week"})
	wantPlan(t, "diese Woche", pl, wantWeek...)
	if len(pl.Days) != 7 || pl.From != p.day(mon) {
		t.Errorf("Woche: %d Tage ab %s", len(pl.Days), pl.From)
	}
	pl = slide(terminalSlide{PlanRange: "next_week"})
	if pl.From != p.day(mon.AddDate(0, 0, 7)) || pl.To != p.day(mon.AddDate(0, 0, 13)) {
		t.Errorf("nächste Woche: %s bis %s", pl.From, pl.To)
	}
	// Entwürfe unveröffentlichter Pläne bleiben weg
	plan := p.e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ('Entwurf', $3::uuid, $1, $1, 'draft', $2::uuid) RETURNING id::text`, p.today, p.pA, p.model)
	t.Cleanup(func() {
		p.e.exec(`DELETE FROM shift_assignments WHERE plan_id = $1::uuid`, plan)
		p.e.exec(`DELETE FROM shift_plans WHERE id = $1::uuid`, plan)
	})
	p.e.exec(`UPDATE shift_assignments SET plan_id = $1::uuid WHERE user_id = $2::uuid AND date = $3`, plan, p.pB, p.today)
	wantPlan(t, "Entwurf", slide(terminalSlide{PlanRange: "today"}), to+"|Frühschicht|Anton Plan")
}

func TestTerminalPlanDepartmentIntersectionAndPermission(t *testing.T) {
	p := newPlanEnv(t)
	to := p.day(p.today)
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true})                                            // alle Abteilungen
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DepartmentIDs: []string{p.deptA, p.deptB}, DurationSeconds: 10, Active: true}) // beide gewählt
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DepartmentIDs: []string{p.deptB}, DurationSeconds: 10, Active: true})          // nur B
	both := []string{to + "|Frühschicht|Anton Plan", to + "|Frühschicht|Berta Plan"}
	check := func(what string, got []gtPlanSlide, w0, w1, w2 []string) {
		t.Helper()
		if len(got) != 3 {
			t.Fatalf("%s: %d Folien", what, len(got))
		}
		wantPlan(t, what+" Folie alle", got[0].Plan, w0...)
		wantPlan(t, what+" Folie A+B", got[1].Plan, w1...)
		wantPlan(t, what+" Folie B", got[2].Plan, w2...)
	}
	// ohne Abteilungsgrenze am Terminal
	check("ohne Terminalgrenze", p.plans(p.termG, p.devG), both, both, []string{both[1]})
	// Terminal auf Abteilung A begrenzt: Schnittmenge
	p.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid] WHERE user_id = $1::uuid`, p.termG, p.deptA)
	check("Terminal nur A", p.plans(p.termG, p.devG), []string{both[0]}, []string{both[0]}, nil) // Folie B ∩ Terminal A = leer
	// Terminal auf A und B: wie ohne Grenze
	p.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid, $3::uuid] WHERE user_id = $1::uuid`, p.termG, p.deptA, p.deptB)
	check("Terminal A+B", p.plans(p.termG, p.devG), both, both, []string{both[1]})
	p.e.exec(`UPDATE terminal_settings SET abteilungen = NULL WHERE user_id = $1::uuid`, p.termG)

	// Recht in der Abteilung: nur B darf der Terminal-Benutzer Planer ansehen
	p.e.grant(p.termNoTickets, "shifts.view", p.deptB, true)
	p.e.warm()
	check("Recht nur B", p.plans(p.termNoTickets, p.devNT), []string{both[1]}, []string{both[1]}, []string{both[1]})
	// ... und in Abteilung A (Folie B hat dann nichts)
	p.e.exec(`UPDATE user_permissions SET department_id = $2::uuid WHERE user_id = $1::uuid AND department_id = $3::uuid`, p.termNoTickets, p.deptA, p.deptB)
	p.e.warm()
	check("Recht nur A", p.plans(p.termNoTickets, p.devNT), []string{both[0]}, []string{both[0]}, nil)
	// Verbot: gar nichts
	p.e.exec(`UPDATE user_permissions SET granted = false, department_id = NULL WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'shifts.view')`, p.termNoTickets)
	p.e.warm()
	check("ohne Recht", p.plans(p.termNoTickets, p.devNT), nil, nil, nil)
}

func TestTerminalPlanAbsencesOnlyAbwesend(t *testing.T) {
	p := newPlanEnv(t)
	to, tm, far := p.day(p.today), p.day(p.tomorrow), p.day(p.far)
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanAbsences, PlanRange: "days", PlanDays: 11, DurationSeconds: 10, Active: true})
	p.slide(terminalSlide{Kind: slideKindPlanAbsences, PlanRange: "days", PlanDays: 11, DepartmentIDs: []string{p.deptB}, DurationSeconds: 10, Active: true})
	got := p.plans(p.termG, p.devG)
	if len(got) != 2 {
		t.Fatalf("Folien: %d", len(got))
	}
	// genehmigt: Anton heute und morgen, Berta in 10 Tagen; die offene Schulung von Berta zählt nicht
	wantPlan(t, "alle", got[0].Plan, to+"|abwesend|Anton Plan", tm+"|abwesend|Anton Plan", far+"|abwesend|Berta Plan")
	wantPlan(t, "Abteilung B", got[1].Plan, far+"|abwesend|Berta Plan")
	// Art, Grund und Notiz verlassen den Server nie: weder in slides.json noch im Folien-JSON
	rec := p.do("GET", "/terminal/slides.json", p.cookie(p.termG, p.devG), nil)
	body := strings.ToLower(rec.Body.String())
	for _, bad := range []string{strings.ToLower(p.secretNote), "arztbesuch", "sick", "krank", "vacation", "urlaub", "training", "schulung", "pending", "approved", "note", "reason", "grund"} {
		if strings.Contains(body, bad) {
			t.Errorf("slides.json enthält %q: %s", bad, rec.Body.String())
		}
	}
	// Auch die Terminalseite selbst (HTML) trägt nichts davon
	page := strings.ToLower(p.do("GET", "/terminal", p.cookie(p.termG, p.devG), nil).Body.String())
	for _, bad := range []string{strings.ToLower(p.secretNote), "arztbesuch"} {
		if strings.Contains(page, bad) {
			t.Errorf("Terminalseite enthält %q", bad)
		}
	}
}

func TestTerminalPlanDuties(t *testing.T) {
	p := newPlanEnv(t)
	to, tm := p.day(p.today), p.day(p.tomorrow)
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanDuties, PlanRange: "days", PlanDays: 3, DurationSeconds: 10, Active: true})
	p.slide(terminalSlide{Kind: slideKindPlanDuties, PlanRange: "today", DurationSeconds: 10, Active: true, DepartmentIDs: []string{p.deptA}})
	got := p.plans(p.termG, p.devG)
	wantPlan(t, "3 Tage", got[0].Plan, to+"|PlanBereitschaft|Berta Plan", tm+"|PlanBereitschaft|Anton Plan")
	wantPlan(t, "heute, Abteilung A", got[1].Plan)
	// ohne Recht „Planer ansehen“ nichts
	p.e.grant(p.termNoTickets, "shifts.view", "", false)
	p.e.warm()
	if got := p.plans(p.termNoTickets, p.devNT); len(got) != 2 || len(flatPlan(got[0].Plan)) != 0 {
		t.Errorf("ohne Recht: %+v", got)
	}
}

func TestTerminalPlanMaintenance(t *testing.T) {
	p := newPlanEnv(t)
	to, tm := p.day(p.today), p.day(p.tomorrow)
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "days", PlanDays: 3, DurationSeconds: 10, Active: true})
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "days", PlanDays: 3, DepartmentIDs: []string{p.deptB}, DurationSeconds: 10, Active: true})
	got := p.plans(p.termG, p.devG)
	if len(got) != 2 {
		t.Fatalf("Folien: %d", len(got))
	}
	// erledigte und ferne Aufträge fehlen; der Eintrag nennt Titel und Anlage
	var all []string
	for _, s := range onlyMine(flatPlan(got[0].Plan)) {
		if i := strings.Index(s, " · "); i > 0 {
			s = s[:i]
		}
		all = append(all, s)
	}
	if strings.Join(all, ",") != to+"|Fällig|W-Heute-A,"+tm+"|Fällig|W-Morgen-B" {
		t.Errorf("Wartung alle: %q", flatPlan(got[0].Plan))
	}
	if f := onlyMine(flatPlan(got[1].Plan)); len(f) != 1 || !strings.HasPrefix(f[0], tm+"|Fällig|W-Morgen-B · PWartB") {
		t.Errorf("Wartung Abteilung B: %q", f)
	}
	// Terminal auf Abteilung A begrenzt: Schnittmenge mit B ist leer
	p.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid] WHERE user_id = $1::uuid`, p.termG, p.deptA)
	got = p.plans(p.termG, p.devG)
	if f := onlyMine(flatPlan(got[1].Plan)); strings.Contains(strings.Join(f, ","), "Überfällig") {
		t.Errorf("Terminal nur A, Folie B: %q", f)
	}
	if f := onlyMine(flatPlan(got[0].Plan)); len(f) != 1 || !strings.HasPrefix(f[0], to+"|Fällig|W-Heute-A") {
		t.Errorf("Terminal nur A, Folie alle: %q", f)
	}
	p.e.exec(`UPDATE terminal_settings SET abteilungen = NULL WHERE user_id = $1::uuid`, p.termG)
	// Wartung ansehen nur in Abteilung B
	p.e.grant(p.termNoTickets, "maintenance.view", p.deptB, true)
	p.e.warm()
	got = p.plans(p.termNoTickets, p.devNT)
	if f := onlyMine(flatPlan(got[0].Plan)); len(f) != 1 || !strings.HasPrefix(f[0], tm+"|Fällig|W-Morgen-B") {
		t.Errorf("Recht nur B: %q", f)
	}
	// Die Version von slides.json hängt nicht von den Plandaten ab (wie bei den Statistikfolien)
	v1 := p.do("GET", "/terminal/slides.json", p.cookie(p.termG, p.devG), nil).Body.String()
	p.e.exec(`UPDATE maintenance_tasks SET title = 'W-Neu' WHERE title = 'W-Heute-A' AND created_by = $1::uuid`, p.pA)
	v2 := p.do("GET", "/terminal/slides.json", p.cookie(p.termG, p.devG), nil).Body.String()
	var a, b struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(v1), &a)
	_ = json.Unmarshal([]byte(v2), &b)
	if a.Version == "" || a.Version != b.Version {
		t.Errorf("Version: %q / %q", a.Version, b.Version)
	}
}

// Planungsfolien bleiben aus dem Zwischenspeicher: die Seite kennt sie nicht als zwischenspeicherbar.
func TestTerminalPlanSlidesJSONKinds(t *testing.T) {
	p := newPlanEnv(t)
	p.clear()
	for _, k := range planKinds {
		p.slide(terminalSlide{Kind: k, PlanRange: "week", DurationSeconds: 10, Active: true})
	}
	got := p.plans(p.termG, p.devG)
	if len(got) != 4 {
		t.Fatalf("Folien: %d", len(got))
	}
	for i, k := range planKinds {
		if got[i].Kind != k || got[i].Plan == nil || len(got[i].Plan.Days) != 7 || got[i].Plan.Label != "diese Woche" {
			t.Errorf("%s: %+v", k, got[i])
		}
	}
}

// Überfällige offene Wartungen erscheinen unter heute als „Überfällig“, sofern der Zeitraum heute enthält.
func TestTerminalPlanMaintenanceOverdue(t *testing.T) {
	p := newPlanEnv(t)
	iA := p.e.infra("POverA ", p.deptA, "")
	p.e.exec(`INSERT INTO maintenance_tasks (title, infrastructure_id, status, due_date, created_by) VALUES ('W-Alt', $1::uuid, 'open', $2, $3::uuid)`, iA, p.today.AddDate(0, 0, -3).Add(10*time.Hour), p.pA)
	t.Cleanup(func() { p.e.exec(`DELETE FROM maintenance_tasks WHERE created_by = $1::uuid`, p.pA) })
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "today", DurationSeconds: 10, Active: true})
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "next_week", DurationSeconds: 10, Active: true})
	got := p.plans(p.termG, p.devG)
	f := onlyMine(flatPlan(got[0].Plan))
	if len(f) != 2 || !strings.HasPrefix(f[0], p.day(p.today)+"|Überfällig|W-Alt · POverA") || !strings.Contains(f[0], "(fällig ") || !strings.HasPrefix(f[1], p.day(p.today)+"|Fällig|W-Heute-A") {
		t.Errorf("heute: %q", f)
	}
	if f := onlyMine(flatPlan(got[1].Plan)); strings.Contains(strings.Join(f, ","), "Überfällig") {
		t.Errorf("nächste Woche ohne Überfälliges: %q", f)
	}
}

// Recht nur in einer Abteilung: Personen ohne Abteilung erscheinen nicht.
func TestTerminalPlanNoDeptNeedsGlobalRight(t *testing.T) {
	p := newPlanEnv(t)
	u := p.e.user("Carl", "employee", false)
	p.e.exec(`UPDATE users SET first_name = 'Carl', last_name = 'Plan', department_id = NULL WHERE id = $1::uuid`, u)
	def := p.e.id(`SELECT id::text FROM shift_definitions WHERE model_id = $1::uuid LIMIT 1`, p.model)
	p.e.exec(`INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by) VALUES ($1::uuid, $2::uuid, $3, '', $1::uuid)`, u, def, p.today)
	t.Cleanup(func() { p.e.exec(`DELETE FROM shift_assignments WHERE user_id = $1::uuid`, u) })
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true})
	has := func(term, dev string) bool {
		return strings.Contains(strings.Join(flatPlan(p.plans(term, dev)[0].Plan), ","), "Carl Plan")
	}
	if !has(p.termG, p.devG) {
		t.Error("globales Recht: Person ohne Abteilung fehlt")
	}
	p.e.grant(p.termNoTickets, "shifts.view", p.deptA, true)
	p.e.warm()
	if has(p.termNoTickets, p.devNT) {
		t.Error("Recht nur in Abteilung A: Person ohne Abteilung erscheint")
	}
}

// Geplante Wartungstermine (Jahresplan) erscheinen als „Geplant“; beauftragte nur als „Fällig“.
func TestTerminalPlanMaintenanceVisits(t *testing.T) {
	p := newPlanEnv(t)
	e := p.e
	to, tm := p.day(p.today), p.day(p.tomorrow)
	iA := e.id(`SELECT id::text FROM infrastructure WHERE name = 'PWartA ' || $1`, e.sfx)
	plan := func(name string) string {
		id := e.id(`INSERT INTO maintenance_plans (name, infrastructure_id, created_by) VALUES ($1, $2::uuid, $3::uuid) RETURNING id::text`, name, iA, p.pA)
		e.t.Cleanup(func() { e.exec(`DELETE FROM maintenance_plans WHERE id = $1::uuid`, id) })
		return id
	}
	planned, ordered := plan("W-Plan-A"), plan("W-Auftrag-A")
	e.exec(`INSERT INTO maintenance_visits (plan_id, calc_day, day, state) VALUES ($1::uuid, $2, $2, 'planned')`, planned, p.tomorrow.Format(dayISO))
	e.exec(`INSERT INTO maintenance_visits (plan_id, calc_day, day, state) VALUES ($1::uuid, $2, $2, 'ordered')`, ordered, p.today.Format(dayISO))
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "days", PlanDays: 3, DurationSeconds: 10, Active: true})
	p.slide(terminalSlide{Kind: slideKindPlanMaint, PlanRange: "days", PlanDays: 3, DepartmentIDs: []string{p.deptB}, DurationSeconds: 10, Active: true})
	got := p.plans(p.termG, p.devG)
	if len(got) != 2 {
		t.Fatalf("Folien: %d", len(got))
	}
	all := strings.Join(onlyMine(flatPlan(got[0].Plan)), ",")
	if !strings.Contains(all, tm+"|Geplant|W-Plan-A · PWartA") {
		t.Errorf("geplanter Termin fehlt: %q", all)
	}
	if strings.Contains(all, "W-Auftrag-A") {
		t.Errorf("beauftragter Termin doppelt: %q", all)
	}
	if !strings.Contains(all, to+"|Fällig|W-Heute-A") {
		t.Errorf("Aufträge fehlen: %q", all)
	}
	if b := strings.Join(onlyMine(flatPlan(got[1].Plan)), ","); strings.Contains(b, "W-Plan-A") {
		t.Errorf("Folie Abteilung B zeigt Termin aus A: %q", b)
	}
}
