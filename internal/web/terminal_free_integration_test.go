//go:build integration

// Freier Terminal-Benutzer (terminal_free.go) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalFree' ./internal/web/
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

// newFreeEnv: Terminal ohne die Rechte, die das Flag ersetzt (ausdrücklich entzogen).
func newFreeEnv(t *testing.T, free bool) *vgEnv {
	v := newVgEnv(t, false)
	e := v.e
	e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, v.term)
	for _, p := range []string{"tasks.edit", "faults.edit", "faults.resolve", "tickets.edit", "tickets.resolve", "setup.edit", "downtime.edit", "hmi.control"} {
		e.grant(v.term, p, "", false)
	}
	e.grant(v.term, "faults.view", "", true)
	e.grant(v.term, "tickets.view", "", true)
	e.grant(v.term, "tasks.view", "", true)
	e.exec(`UPDATE users SET terminal_free = $2 WHERE id = $1::uuid`, v.term, free)
	e.warm()
	return v
}

func (v *vgEnv) freeReq(cookie *http.Cookie) *http.Request {
	req := httptest.NewRequest("GET", "/terminal/vorgaenge", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func TestTerminalFreeAus(t *testing.T) {
	v := newFreeEnv(t, false)
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("Flag aus, Aufgabe start: %d", rec.Code)
	}
	if st := v.status("tasks", v.task); st != "open" {
		t.Errorf("Flag aus verändert: %s", st)
	}
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("Flag aus, Störung start: %d", rec.Code)
	}
	if v.h.terminalFreeCan(v.freeReq(v.devCookie), v.term, "tasks.edit", v.plant) {
		t.Error("Flag aus: terminalFreeCan")
	}
}

func TestTerminalFreeAufgabeStartenUndBeenden(t *testing.T) {
	v := newFreeEnv(t, true)
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if st := v.status("tasks", v.task); st != "in_progress" {
		t.Errorf("Status %s", st)
	}
	if rec := v.post("/terminal/vorgaenge/beenden", "task", v.task); rec.Code != http.StatusSeeOther {
		t.Fatalf("beenden: %d %s", rec.Code, rec.Body.String())
	}
	if st := v.status("tasks", v.task); st != "resolved" {
		t.Errorf("Status %s", st)
	}
	// Unteranlage gehört zum Anlagenbaum
	if rec := v.post("/terminal/vorgaenge/start", "ticket", v.ticket, v.anna); rec.Code != http.StatusSeeOther {
		t.Errorf("Ticket in Unteranlage: %d", rec.Code)
	}
}

func TestTerminalFreeStoerungAbschlussToken(t *testing.T) {
	v := newFreeEnv(t, true)
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	tok := v.issue(t, "fault", v.fault)
	if !v.tokenOK(tok, "/complete/fault/"+v.fault, v.devCookie) {
		t.Error("Abschluss-Token gilt nicht")
	}
	auth := map[string]string{"Authorization": "Bearer " + tok}
	if rec := v.req("GET", "/complete/fault/"+v.fault, nil, v.devCookie, auth); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Errorf("Assistent: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTerminalFreeGrenzen(t *testing.T) {
	v := newFreeEnv(t, true)
	req := v.freeReq(v.devCookie)
	for _, perm := range []string{"faults.view", "faults.edit", "faults.resolve", "tickets.view", "tickets.edit", "tickets.resolve", "tasks.view", "tasks.edit", "setup.view", "setup.edit", "downtime.view", "downtime.edit", "hmi.view"} {
		if !v.h.terminalFreeCan(req, v.term, perm, v.plant) || !v.h.terminalFreeCan(req, v.term, perm, v.sub) {
			t.Errorf("%s nicht gewährt", perm)
		}
		if v.h.terminalFreeCan(req, v.term, perm, v.foreign) {
			t.Errorf("%s außerhalb des Anlagenbaums gewährt", perm)
		}
	}
	for _, perm := range []string{"hmi.control", "hmi.manage", "setup.admin", "downtime.admin", "system.manage_users", "maintenance.edit", "parts.edit"} {
		if v.h.terminalFreeCan(req, v.term, perm, v.plant) || v.h.terminalUserCan(req, v.term, perm, v.plant) {
			t.Errorf("%s gewährt", perm)
		}
	}
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.foreignFault, v.anna); rec.Code != http.StatusNotFound {
		t.Errorf("fremde Anlage: %d", rec.Code)
	}
	// Sitzung ohne Gerät: gilt nicht
	if v.h.terminalFreeCan(v.freeReq(v.plainCookie), v.term, "tasks.edit", v.plant) || v.h.terminalFreeCan(v.freeReq(nil), v.term, "tasks.edit", v.plant) {
		t.Error("ohne Gerätesitzung gewährt")
	}
	f := url.Values{"type": {"task"}, "id": {v.task}, "people": {v.anna}}
	if rec := v.req("POST", "/terminal/vorgaenge/start", f, v.plainCookie, nil); rec.Code != http.StatusForbidden {
		t.Errorf("ohne Gerät: %d", rec.Code)
	}
	// Karte: Vorgänge laufen mit den eigenen Rechten der Person, nicht mit denen des Flags
	v.pdhEnv.card(v.person)
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("Karte ohne Recht: %d", rec.Code)
	}
	v.pdhEnv.card(v.card)
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.card); rec.Code != http.StatusSeeOther {
		t.Errorf("Karte mit eigenem Recht: %d", rec.Code)
	}
}

func TestTerminalFreeMitKarte(t *testing.T) {
	for _, nurKarte := range []bool{false, true} {
		v := newFreeEnv(t, true)
		if nurKarte {
			v.e.must(v.h.saveTerminalVorgaengeModus(v.e.ctx, v.term, vorgModusNurKarte, v.term))
		}
		v.pdhEnv.card(v.person) // Person ohne eigene Rechte
		req := v.freeReq(v.devCookie)
		if !v.h.terminalFreeCan(req, v.term, "setup.edit", v.plant) || v.h.terminalFreeCan(req, v.term, "hmi.control", v.plant) || v.h.terminalFreeCan(req, v.term, "setup.edit", v.foreign) {
			t.Errorf("nurKarte=%v: Terminal-Rechte mit Karte", nurKarte)
		}
		if a := v.h.terminalSetupActor(req, v.term, v.plant); !a.Card || !a.Can {
			t.Errorf("nurKarte=%v: Rüsten mit Karte %+v", nurKarte, a)
		}
		if a := v.h.terminalDowntimeActor(req, v.term, v.plant); !a.Card || !a.Can {
			t.Errorf("nurKarte=%v: Stillstand mit Karte %+v", nurKarte, a)
		}
		if !v.h.terminalReportPerm(req, v.term, "fault", v.plant) || !v.h.terminalReportPerm(req, v.term, "ticket", v.sub) {
			t.Errorf("nurKarte=%v: Melden mit Karte", nurKarte)
		}
		view, ctl := v.h.terminalHMIAccess(req, v.term, nil, v.plant, true, hmiTermEveryone, hmiTermEveryone)
		if !view || ctl {
			t.Errorf("nurKarte=%v: HMI jeder: %v/%v", nurKarte, view, ctl)
		}
		// Vorgänge mit Karte: eigene Rechte der Person, das Flag zählt nicht
		if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna); rec.Code != http.StatusForbidden {
			t.Errorf("nurKarte=%v: Vorgang mit Karte ohne Recht: %d", nurKarte, rec.Code)
		}
		// HMI „nur mit Karte“: die Person entscheidet
		person := &users.User{ID: v.person, Role: users.Role("viewer")}
		if view, _ := v.h.terminalHMIAccess(req, v.term, person, v.plant, true, hmiTermCard, hmiTermCard); view {
			t.Errorf("nurKarte=%v: HMI Karte ohne Recht der Person sichtbar", nurKarte)
		}
	}
}

func TestTerminalFreeVollerAbschluss(t *testing.T) {
	v := newFreeEnv(t, true)
	v.post("/terminal/vorgaenge/start", "task", v.task, v.anna)
	if rec := v.post("/terminal/vorgaenge/beenden", "task", v.task); rec.Code != http.StatusSeeOther || v.status("tasks", v.task) != "resolved" {
		t.Fatalf("Aufgabe beenden: %d %s", rec.Code, v.status("tasks", v.task))
	}
	v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna)
	tok := v.issue(t, "fault", v.fault)
	body := `{"no_parts":true,"minutes":10,"comment":"behoben","finish":true,"colleagues":[]}`
	req := httptest.NewRequest("POST", "/complete/fault/"+v.fault, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(v.devCookie)
	rec := httptest.NewRecorder()
	v.mux2.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"closed":true`) {
		t.Fatalf("Abschluss: %d %s", rec.Code, rec.Body.String())
	}
	if st := v.status("faults", v.fault); st != "resolved" {
		t.Errorf("Störung: %s", st)
	}
}

func TestTerminalFreeNurKarte(t *testing.T) {
	v := newFreeEnv(t, true)
	v.e.must(v.h.saveTerminalVorgaengeModus(v.e.ctx, v.term, vorgModusNurKarte, v.term))
	if v.h.terminalFreeCan(v.freeReq(v.devCookie), v.term, "tasks.edit", v.plant) {
		t.Error("nur_karte: gewährt")
	}
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("nur_karte ohne Karte: %d", rec.Code)
	}
	if st := v.status("tasks", v.task); st != "open" {
		t.Errorf("verändert: %s", st)
	}
}

func TestTerminalFreeSetupDowntimeHMI(t *testing.T) {
	v := newFreeEnv(t, true)
	if a := v.h.terminalSetupActor(v.freeReq(v.devCookie), v.term, v.plant); !a.Can {
		t.Error("Rüsten: nicht erlaubt")
	}
	if a := v.h.terminalDowntimeActor(v.freeReq(v.devCookie), v.term, v.plant); !a.Can {
		t.Error("Stillstand: nicht erlaubt")
	}
	if a := v.h.terminalSetupActor(v.freeReq(v.plainCookie), v.term, v.plant); a.Can {
		t.Error("Rüsten ohne Gerät erlaubt")
	}
	view, ctl := v.h.terminalHMIAccess(v.freeReq(v.devCookie), v.term, nil, v.plant, true, hmiTermEveryone, hmiTermEveryone)
	if !view || ctl {
		t.Errorf("HMI: ansehen %v, bedienen %v", view, ctl)
	}
	// Störungen und Tickets sind ohne Rollenrecht sichtbar
	v.e.exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1::uuid AND permission_id IN (SELECT id FROM permissions WHERE key IN ('faults.view','tickets.view'))`, v.term)
	v.e.warm()
	if fl, tk := v.h.terminalPerms(v.freeReq(v.devCookie), v.term, "terminal", v.plant); !fl || !tk {
		t.Errorf("Ansehen: Störungen %v, Tickets %v", fl, tk)
	}
	if fl, tk := v.h.terminalPerms(v.freeReq(v.plainCookie), v.term, "terminal", v.plant); fl || tk {
		t.Error("Ansehen ohne Gerät gewährt")
	}
}

func TestTerminalFreeGrund(t *testing.T) {
	v := newFreeEnv(t, false)
	body := v.req("GET", "/terminal/vorgaenge?sel=task:"+v.task, nil, v.devCookie, nil).Body.String()
	if !strings.Contains(body, "Keine Berechtigung:") || !strings.Contains(body, "in Abteilung") || strings.Contains(body, `name="people"`) {
		t.Errorf("Grund fehlt: %s", body)
	}
	if !strings.Contains(body, `v-nope" href="/terminal/vorgaenge?sel=task`) || !strings.Contains(body, v.task) {
		t.Error("Zeile nicht antippbar")
	}
	v.e.must(v.h.saveTerminalVorgaengeModus(v.e.ctx, v.term, vorgModusNurKarte, v.term))
	body = v.req("GET", "/terminal/vorgaenge?sel=fault:"+v.fault, nil, v.devCookie, nil).Body.String()
	if !strings.Contains(body, "Nur mit Karte – bitte Karte vorhalten") {
		t.Errorf("Kartenhinweis fehlt: %s", body)
	}
}

func TestTerminalFreeSpeichern(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("tfmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("tfplain", "viewer", false)
	term := e.user("tfterm", "viewer", true)
	person := e.user("tfperson", "viewer", false)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	do := func(uid, by, body string) {
		req := httptest.NewRequest("POST", "/users/"+uid+"/master", strings.NewReader(body))
		role := "viewer"
		if by == mgr {
			role = "admin"
		}
		req = withUser(req, &users.User{ID: by, Role: users.Role(role)})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	flag := func(uid string) string {
		return e.id(`SELECT terminal_free::text FROM users WHERE id = $1::uuid`, uid)
	}
	do(term, plain, "terminal_free_sent=1&terminal_free=on")
	if flag(term) != "false" {
		t.Error("ohne Recht gesetzt")
	}
	do(term, mgr, "work_location=Halle") // älteres Formular ohne Marker
	if flag(term) != "false" {
		t.Error("ohne Marker gesetzt")
	}
	do(person, mgr, "terminal_free_sent=1&terminal_free=on")
	if flag(person) != "false" {
		t.Error("bei normalem Benutzer gesetzt")
	}
	do(term, mgr, "terminal_free_sent=1&terminal_free=on")
	if flag(term) != "true" {
		t.Error("nicht gesetzt")
	}
	if n := e.id(`SELECT COUNT(*)::text FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND field_name = 'Freier Terminal-Benutzer'`, term); n != "1" {
		t.Errorf("Verlauf: %s", n)
	}
	do(term, mgr, "work_location=Halle")
	if flag(term) != "true" {
		t.Error("ohne Marker zurückgesetzt")
	}
	do(term, mgr, "terminal_free_sent=1")
	if flag(term) != "false" {
		t.Error("nicht abgeschaltet")
	}
}
