//go:build integration

// Auswahl des Schichtplans am Terminal (terminal_shiftplan.go): Seite /terminal/schichtplan?plan=…, Grundeinstellung
// je Terminal, Benutzerseite und Planungsfolie „Schichtplan“.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run ShiftPlan ./internal/web/
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestTerminalSchichtplanPlanChoice(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	deptA, deptB := e.dept("SPCA"), e.dept("SPCB")
	e.exec(`UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, x.plant, deptA)
	mk := func(first, dept string) string {
		id := e.user(first, "user", false)
		e.exec(`UPDATE users SET first_name = $2, last_name = 'Wahl', department_id = $3::uuid WHERE id = $1::uuid`, id, first, dept)
		return id
	}
	egon, fritz, gunnar, dora := mk("Egonwahl", deptA), mk("Fritzwahl", deptA), mk("Gunnarwahl", deptA), mk("Dorawahl", deptB)

	model := e.id(`INSERT INTO shift_models (name) VALUES ('SPCModell' || $1::text) RETURNING id::text`, e.sfx)
	frueh := e.id(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time) VALUES ($1::uuid, 'Fruehwahl', 'F', '06:00', '14:00') RETURNING id::text`, model)
	plan := func(name, status string, from, to int) string {
		return e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ($1 || $5::text, $2::uuid, now()::date + $3::int, now()::date + $4::int, $6::shift_plan_status, $7::uuid) RETURNING id::text`,
			name, model, from, to, e.sfx, status, x.term)
	}
	p1 := plan("PlanEins", "published", -30, 30)
	p2 := plan("PlanZwei", "published", -30, 30)
	draft := plan("PlanEntwurf", "draft", -30, 30)
	old := plan("PlanAlt", "published", -60, -40)
	t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM shift_assignments WHERE shift_id = $1::uuid`, frueh)
		e.pool.Exec(bg, `DELETE FROM shift_plans WHERE model_id = $1::uuid`, model)
		e.pool.Exec(bg, `DELETE FROM shift_definitions WHERE model_id = $1::uuid`, model)
		e.pool.Exec(bg, `DELETE FROM shift_models WHERE id = $1::uuid`, model)
	})
	today := time.Now().Format(dayISO)
	assign := func(user, planID string) {
		e.exec(`INSERT INTO shift_assignments (user_id, shift_id, date, created_by, plan_id) VALUES ($1::uuid, $2::uuid, $3::date, $4::uuid, $5::uuid)`, user, frueh, today, x.term, planID)
	}
	assign(egon, p1)
	assign(fritz, p2)
	assign(gunnar, draft)
	assign(dora, p1) // andere Abteilung: nie am Terminal dieser Abteilung
	e.grant(x.term, "shifts.view", deptA, true)
	e.warm()

	get := func(target string) string {
		t.Helper()
		rec := x.do("GET", target, nil, x.devCookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", target, rec.Code)
		}
		return rec.Body.String()
	}
	want := func(what, body string, in, out []string) {
		t.Helper()
		for _, s := range in {
			if !strings.Contains(body, s) {
				t.Errorf("%s: fehlt %q", what, s)
			}
		}
		for _, s := range out {
			if strings.Contains(body, s) {
				t.Errorf("%s: darf nicht erscheinen: %q", what, s)
			}
		}
	}
	never := []string{"Gunnarwahl", "Dorawahl", "PlanEntwurf", "plan=" + draft, "plan=" + old}

	// ohne Angabe: alle veröffentlichten Pläne (wie bisher), Knöpfe für beide Pläne und „Alle“, nie Entwurf/abgelaufen
	body := get("/terminal/schichtplan")
	want("alle", body, []string{"Egonwahl", "Fritzwahl", "plan=" + p1, "plan=" + p2, ">Alle</a>", "PlanEins", "PlanZwei"}, never)
	if strings.Count(body, `t-pplans`) < 1 {
		t.Error("Planknöpfe fehlen")
	}
	// ein Plan
	body = get("/terminal/schichtplan?plan=" + p1)
	want("Plan 1", body, []string{"Egonwahl", "· PlanEins"}, append([]string{"Fritzwahl"}, never[:3]...))
	body = get("/terminal/schichtplan?plan=" + strings.ToUpper(p2))
	want("Plan 2 (Großbuchstaben)", body, []string{"Fritzwahl"}, []string{"Egonwahl"})
	// Entwurf, abgelaufener Plan, Unsinn: wie „Alle“, ohne Entwurf
	for _, q := range []string{draft, old, "keine-uuid", "00000000-0000-0000-0000-000000000000"} {
		want("Plan="+q, get("/terminal/schichtplan?plan="+q), []string{"Egonwahl", "Fritzwahl"}, never[:3])
	}
	// nächste Woche mit Plan: Zuteilungen liegen nur heute
	want("nächste Woche", get("/terminal/schichtplan?woche=naechste&plan="+p1), nil, []string{"Egonwahl", "Fritzwahl"})
	// Plan-Parameter ändert die Abteilungsgrenze nicht
	if rec := x.do("GET", "/terminal/schichtplan?abt="+deptB+"&plan="+p1, nil, x.devCookie); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Dorawahl") {
		t.Errorf("fremde Abteilung mit Plan: %d", rec.Code)
	}

	// Grundeinstellung: Plan 2 beim Öffnen
	e.must(x.h.saveTerminalShiftPlan(e.ctx, x.term, p2, x.person))
	if got := x.h.terminalShiftPlan(e.ctx, x.term); got != p2 {
		t.Fatalf("Grundeinstellung: %q", got)
	}
	body = get("/terminal/schichtplan")
	want("Grundplan", body, []string{"Fritzwahl", "· PlanZwei", "plan=alle"}, []string{"Egonwahl"})
	want("Grundplan, ausdrücklich alle", get("/terminal/schichtplan?plan=alle"), []string{"Egonwahl", "Fritzwahl"}, never[:3])
	want("Grundplan, anderer Plan", get("/terminal/schichtplan?plan="+p1), []string{"Egonwahl"}, []string{"Fritzwahl"})
	// Grundplan gilt in der Woche nicht: alle
	e.exec(`UPDATE terminal_settings SET shift_plan_id = $2::uuid WHERE user_id = $1::uuid`, x.term, old)
	want("Grundplan abgelaufen", get("/terminal/schichtplan"), []string{"Egonwahl", "Fritzwahl"}, never[:3])
	e.exec(`UPDATE terminal_settings SET shift_plan_id = $2::uuid WHERE user_id = $1::uuid`, x.term, draft)
	want("Grundplan Entwurf", get("/terminal/schichtplan"), []string{"Egonwahl", "Fritzwahl"}, never[:3])
	// unbekannter Plan lässt sich nicht einstellen, „Alle“ schon
	if err := x.h.saveTerminalShiftPlan(e.ctx, x.term, "00000000-0000-0000-0000-000000000000", x.person); err != errShiftPlanGone {
		t.Errorf("unbekannter Plan: %v", err)
	}
	e.must(x.h.saveTerminalShiftPlan(e.ctx, x.term, "", x.person))
	if got := x.h.terminalShiftPlan(e.ctx, x.term); got != "" {
		t.Errorf("zurück auf Alle: %q", got)
	}
	// Plan wird gelöscht: Einstellung fällt auf „Alle“
	e.must(x.h.saveTerminalShiftPlan(e.ctx, x.term, p2, x.person))
	e.exec(`DELETE FROM shift_plans WHERE id = $1::uuid`, p2)
	if got := x.h.terminalShiftPlan(e.ctx, x.term); got != "" {
		t.Errorf("nach Löschen des Plans: %q", got)
	}
	// nur noch ein veröffentlichter Plan: keine Planknöpfe
	body = get("/terminal/schichtplan")
	if strings.Contains(body, `t-pplans`) {
		t.Error("Planknöpfe bei nur einem Plan")
	}
	want("ein Plan", body, []string{"Egonwahl"}, never[:3])
}

// Benutzerseite des Terminals: Auswahl „Schichtplan beim Öffnen“ mit eigenem Marker.
func TestTerminalShiftPlanUserDetailForm(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("spmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("spplain", "viewer", false)
	term := e.user("spterm", "viewer", true)
	model := e.id(`INSERT INTO shift_models (name) VALUES ('SPFModell' || $1::text) RETURNING id::text`, e.sfx)
	pub := e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ('FormPlanPub' || $2::text, $1::uuid, now()::date, now()::date + 10, 'published', $3::uuid) RETURNING id::text`, model, e.sfx, mgr)
	dr := e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ('FormPlanEntwurf' || $2::text, $1::uuid, now()::date, now()::date + 10, 'draft', $3::uuid) RETURNING id::text`, model, e.sfx, mgr)
	t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM shift_plans WHERE model_id = $1::uuid`, model)
		e.pool.Exec(bg, `DELETE FROM shift_models WHERE id = $1::uuid`, model)
	})
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Get("/users/{id}", h.UserDetailPage)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	do := func(method, target, body, uid string) *httptest.ResponseRecorder {
		role := "viewer"
		if uid == mgr {
			role = "admin"
		}
		req := withUser(httptest.NewRequest(method, target, strings.NewReader(body)), &users.User{ID: uid, Role: users.Role(role)})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	page := do("GET", "/users/"+term+"?tab=master", "", mgr).Body.String()
	for _, want := range []string{`name="terminal_shift_plan"`, `value="` + pub + `"`, "FormPlanPub"} {
		if !strings.Contains(page, want) {
			t.Errorf("Formular ohne %q", want)
		}
	}
	if strings.Contains(page, "FormPlanEntwurf") {
		t.Error("Entwurf in der Auswahl")
	}
	const sent = "terminal_shift_plan_sent=1"
	do("POST", "/users/"+term+"/master", sent+"&terminal_shift_plan="+pub, mgr)
	if got := h.terminalShiftPlan(e.ctx, term); got != pub {
		t.Fatalf("Plan nicht gespeichert: %q", got)
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND message LIKE 'Schichtplan beim Öffnen%'`, term).Scan(&n))
	if n != 1 {
		t.Errorf("Verlaufseintrag: %d", n)
	}
	if b := do("GET", "/users/"+term+"?tab=master", "", mgr).Body.String(); !strings.Contains(b, `value="`+pub+`" selected`) {
		t.Error("gespeicherter Plan nicht gewählt")
	}
	// ohne Marker, ohne Recht: unverändert
	do("POST", "/users/"+term+"/master", "work_location=Halle", mgr)
	do("POST", "/users/"+term+"/master", sent+"&terminal_shift_plan=", plain)
	if got := h.terminalShiftPlan(e.ctx, term); got != pub {
		t.Errorf("fälschlich verändert: %q", got)
	}
	// Plan im Formular ohne Entwurf-Prüfung: unbekannte Plan-ID wird nicht übernommen
	do("POST", "/users/"+term+"/master", sent+"&terminal_shift_plan=00000000-0000-0000-0000-000000000000", mgr)
	if got := h.terminalShiftPlan(e.ctx, term); got != pub {
		t.Errorf("unbekannter Plan übernommen: %q", got)
	}
	// zurück auf Alle
	do("POST", "/users/"+term+"/master", sent+"&terminal_shift_plan=", mgr)
	if got := h.terminalShiftPlan(e.ctx, term); got != "" {
		t.Errorf("zurück auf Alle: %q", got)
	}
	_ = dr
}

// Folie „Schichtplan“ mit Plan.
func TestTerminalPlanShiftsPlanChoice(t *testing.T) {
	p := newPlanEnv(t)
	to := p.day(p.today)
	mkPlan := func(name, status string, from, until int) string {
		id := p.e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ($1, $2::uuid, now()::date + $3::int, now()::date + $4::int, $5::shift_plan_status, $6::uuid) RETURNING id::text`,
			name, p.model, from, until, status, p.pA)
		return id
	}
	p1, p2 := mkPlan("FolieEins", "published", -5, 5), mkPlan("FolieZwei", "published", -5, 5)
	dr := mkPlan("FolieEntwurf", "draft", -5, 5)
	far := mkPlan("FolieSpaeter", "published", 40, 50)
	t.Cleanup(func() {
		p.e.exec(`DELETE FROM shift_assignments WHERE plan_id = ANY($1::uuid[])`, []string{p1, p2, dr, far})
		p.e.exec(`DELETE FROM shift_plans WHERE id = ANY($1::uuid[])`, []string{p1, p2, dr, far})
	})
	// Anton heute im Plan 1, Berta heute im Plan 2 (die Zuteilungen von newPlanEnv ohne Plan werden umgehängt)
	p.e.exec(`UPDATE shift_assignments SET plan_id = $1::uuid WHERE user_id = $2::uuid AND date = $3`, p1, p.pA, p.today)
	p.e.exec(`UPDATE shift_assignments SET plan_id = $1::uuid WHERE user_id = $2::uuid AND date = $3`, p2, p.pB, p.today)
	slide := func(plan string) *planSlideData {
		p.clear()
		p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true, ShiftPlanID: plan})
		got := p.plans(p.termG, p.devG)
		if len(got) != 1 {
			t.Fatalf("Folien: %+v", got)
		}
		return got[0].Plan
	}
	both := []string{to + "|Frühschicht|Anton Plan", to + "|Frühschicht|Berta Plan"}
	wantPlan(t, "alle", slide(""), both...)
	pl := slide(p1)
	wantPlan(t, "Plan 1", pl, to+"|Frühschicht|Anton Plan")
	if pl.Label != "heute · FolieEins" {
		t.Errorf("Beschriftung: %q", pl.Label)
	}
	wantPlan(t, "Plan 2", slide(p2), to+"|Frühschicht|Berta Plan")
	// Entwurf und Plan außerhalb des Zeitraums: alle (Entwurf bleibt wie bisher ausgeblendet)
	p.e.exec(`UPDATE shift_assignments SET plan_id = $1::uuid WHERE user_id = $2::uuid AND date = $3`, dr, p.pB, p.today)
	wantPlan(t, "Entwurf als Plan", slide(dr), to+"|Frühschicht|Anton Plan")
	wantPlan(t, "Plan außerhalb", slide(far), to+"|Frühschicht|Anton Plan")
	// Abteilungsauswahl bleibt wirksam
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true, ShiftPlanID: p1, DepartmentIDs: []string{p.deptB}})
	wantPlan(t, "Plan 1, nur Abteilung B", p.plans(p.termG, p.devG)[0].Plan)
	// Speichern: unbekannter Plan, falsche ID, andere Folienart
	h := p.h
	bad := terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true, ShiftPlanID: "00000000-0000-0000-0000-000000000000"}
	if err := h.saveTerminalSlide(p.e.ctx, &bad); err != errShiftPlanGone {
		t.Errorf("unbekannter Plan: %v", err)
	}
	bad.ShiftPlanID = "kein-plan"
	if err := h.saveTerminalSlide(p.e.ctx, &bad); err == nil || !strings.Contains(err.Error(), "Ungültiger Schichtplan") {
		t.Errorf("falsche ID: %v", err)
	}
	abs := terminalSlide{Kind: slideKindPlanAbsences, PlanRange: "today", DurationSeconds: 10, Active: true, ShiftPlanID: p1}
	p.e.must(h.saveTerminalSlide(p.e.ctx, &abs))
	got, err := h.getTerminalSlide(p.e.ctx, abs.ID)
	p.e.must(err)
	if got.ShiftPlanID != "" {
		t.Errorf("Plan an anderer Folienart: %q", got.ShiftPlanID)
	}
	// Plan gelöscht: Folie zeigt wieder alle
	p.clear()
	p.slide(terminalSlide{Kind: slideKindPlanShifts, PlanRange: "today", DurationSeconds: 10, Active: true, ShiftPlanID: p2})
	p.e.exec(`DELETE FROM shift_plans WHERE id = $1::uuid`, p2)
	pl = p.plans(p.termG, p.devG)[0].Plan
	wantPlan(t, "Plan gelöscht", pl, to+"|Frühschicht|Anton Plan")
}
