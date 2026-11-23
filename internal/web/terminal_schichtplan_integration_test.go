//go:build integration

// Schichtplan am Terminal (terminal_schichtplan.go).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalSchichtplan ./internal/web/
package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTerminalSchichtplan(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	deptA, deptB := e.dept("SPlanA"), e.dept("SPlanB")
	e.exec(`UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, x.plant, deptA)
	mk := func(name, dept string) string {
		id := e.user(name, "user", false)
		e.exec(`UPDATE users SET first_name = $2, last_name = 'Plan', department_id = $3::uuid WHERE id = $1::uuid`, id, name, dept)
		return id
	}
	anna, bert, cara, dora := mk("Annaplan", deptA), mk("Bertplan", deptA), mk("Caraplan", deptA), mk("Doraplan", deptB)

	model := e.id(`INSERT INTO shift_models (name) VALUES ('SPModell' || $1::text) RETURNING id::text`, e.sfx)
	frueh := e.id(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time) VALUES ($1::uuid, 'Fruehschichtx', 'F', '06:00', '14:00') RETURNING id::text`, model)
	nacht := e.id(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time) VALUES ($1::uuid, 'Nachtschichtx', 'N', '22:00', '06:00') RETURNING id::text`, model)
	draftPlan := e.id(`INSERT INTO shift_plans (name, model_id, start_date, end_date, status, created_by) VALUES ('Entwurf', $1::uuid, now()::date - 30, now()::date + 30, 'draft', $2::uuid) RETURNING id::text`, model, x.term)
	duty := e.id(`INSERT INTO duty_types (name, short_name, kind) VALUES ('Ersthelferx', 'EH', 'duty') RETURNING id::text`)
	t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM shift_assignments WHERE shift_id IN ($1::uuid, $2::uuid)`, frueh, nacht)
		e.pool.Exec(bg, `DELETE FROM duty_assignments WHERE duty_type_id = $1::uuid`, duty)
		e.pool.Exec(bg, `DELETE FROM duty_types WHERE id = $1::uuid`, duty)
		e.pool.Exec(bg, `DELETE FROM absences WHERE user_id IN ($1::uuid, $2::uuid, $3::uuid)`, anna, bert, cara)
		e.pool.Exec(bg, `DELETE FROM shift_plans WHERE id = $1::uuid`, draftPlan)
		e.pool.Exec(bg, `DELETE FROM shift_definitions WHERE model_id = $1::uuid`, model)
		e.pool.Exec(bg, `DELETE FROM shift_models WHERE id = $1::uuid`, model)
	})
	today := time.Now().Format(dayISO)
	nextWeek := mondayOf(time.Now()).AddDate(0, 0, 7).Format(dayISO)
	assign := func(user, shift, day, plan string) {
		e.exec(`INSERT INTO shift_assignments (user_id, shift_id, date, created_by, plan_id) VALUES ($1::uuid, $2::uuid, $3::date, $4::uuid, NULLIF($5,'')::uuid)`, user, shift, day, x.term, plan)
	}
	assign(anna, frueh, today, "")
	assign(bert, frueh, today, "") // wegen Abwesenheit nicht gezeigt
	assign(cara, nacht, today, draftPlan)
	assign(dora, frueh, today, "")
	assign(anna, nacht, nextWeek, "")
	e.exec(`INSERT INTO duty_assignments (duty_type_id, user_id, date) VALUES ($1::uuid, $2::uuid, $3::date)`, duty, anna, today)
	e.exec(`INSERT INTO absences (user_id, type, status, start_date, end_date, days, note) VALUES ($1::uuid, 'sick', 'approved', $2::date, $2::date, 1, 'Chemotherapie')`, bert, today)

	e.grant(x.term, "shifts.view", deptA, true)
	e.warm()

	// Kachel und Seite
	if neu := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); !strings.Contains(neu, `href="/terminal/schichtplan"`) {
		t.Error("Kachel Schichtplan fehlt")
	}
	rec := x.do("GET", "/terminal/schichtplan", nil, x.devCookie)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("Seite: %d", rec.Code)
	}
	for _, want := range []string{"Annaplan", "Fruehschichtx", "06:00–14:00", "Ersthelferx", "Bertplan", "abwesend"} {
		if !strings.Contains(body, want) {
			t.Errorf("fehlt: %q", want)
		}
	}
	// nie: Grund, Notiz, Entwurf, fremde Abteilung, Schicht der abwesenden Person
	for _, bad := range []string{"Krank", "sick", "Chemotherapie", "Caraplan", "Nachtschichtx", "Doraplan"} {
		if strings.Contains(body, bad) {
			t.Errorf("darf nicht erscheinen: %q", bad)
		}
	}
	if n := strings.Count(body, "Fruehschichtx"); n != 2 { // Heute-Gruppe und Wochentabelle, nur für Anna
		t.Errorf("Fruehschichtx %d-mal, erwartet 2 (nur Anna)", n)
	}
	// nächste Woche
	next := x.do("GET", "/terminal/schichtplan?abt="+deptA+"&woche=naechste", nil, x.devCookie).Body.String()
	if !strings.Contains(next, "Nachtschichtx") || strings.Contains(next, "Fruehschichtx") {
		t.Errorf("nächste Woche:\n%s", next)
	}
	// Abteilung außerhalb der Terminal-Abteilungen
	if rec := x.do("GET", "/terminal/schichtplan?abt="+deptB, nil, x.devCookie); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Doraplan") {
		t.Errorf("fremde Abteilung: %d", rec.Code)
	}
	// in den Terminal-Abteilungen, aber ohne shifts.view dort: ebenfalls verweigert, und nicht wählbar
	e.exec(`INSERT INTO terminal_settings (user_id) VALUES ($1::uuid) ON CONFLICT (user_id) DO NOTHING`, x.term)
	e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid, $3::uuid] WHERE user_id = $1::uuid`, x.term, deptA, deptB)
	rec = x.do("GET", "/terminal/schichtplan?abt="+deptB, nil, x.devCookie)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Doraplan") {
		t.Errorf("Abteilung ohne Recht: %d", rec.Code)
	}
	e.exec(`UPDATE user_permissions SET department_id = $2::uuid WHERE user_id = $1::uuid`, x.term, deptB) // nur noch B
	e.warm()
	rec = x.do("GET", "/terminal/schichtplan?abt="+deptB, nil, x.devCookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Doraplan") || strings.Contains(rec.Body.String(), "SPlanA") {
		t.Errorf("Abteilung mit Recht (B): %d", rec.Code)
	}
	// ohne Gerätesitzung und ohne Terminal-Benutzer: nichts
	if rec := x.do("GET", "/terminal/schichtplan", nil); rec.Code == http.StatusOK {
		t.Errorf("ohne Sitzung: %d", rec.Code)
	}
	// Recht entzogen: Kachel weg, Seite ohne Daten
	e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, x.term)
	e.warm()
	if neu := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); strings.Contains(neu, `href="/terminal/schichtplan"`) {
		t.Error("Kachel ohne Recht sichtbar")
	}
	if b := x.do("GET", "/terminal/schichtplan", nil, x.devCookie).Body.String(); strings.Contains(b, "Annaplan") || strings.Contains(b, "Fruehschichtx") {
		t.Error("Daten ohne Recht")
	}
	e.grant(x.term, "shifts.view", deptA, true)
	e.warm()

	// Schalter aus: Kachel weg, Route 404
	if _, err := x.h.setTerminalFunctions(e.ctx, x.term, map[string]bool{"schichtplan": false}); err != nil {
		t.Fatal(err)
	}
	if neu := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); strings.Contains(neu, `href="/terminal/schichtplan"`) {
		t.Error("Kachel trotz ausgeschaltetem Schalter")
	}
	if rec := x.do("GET", "/terminal/schichtplan", nil, x.devCookie); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Annaplan") {
		t.Errorf("Route bei ausgeschaltetem Schalter: %d", rec.Code)
	}
}
