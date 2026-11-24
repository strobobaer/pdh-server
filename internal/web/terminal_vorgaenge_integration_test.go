//go:build integration

// Vorgänge am Terminal (terminal_vorgaenge.go) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'Vorgaenge|TerminalDepartments' ./internal/web/
package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
)

type vgEnv struct {
	*pdhEnv
	mux2                http.Handler
	dept, otherDept     string
	anna, berta, carla  string // Anna und Berta in der Abteilung des Terminals, Carla in einer fremden
	card                string // Kartenperson mit eigenen Rechten
	sub, foreign        string
	fault, ticket, task string
	foreignFault        string
	plainCookie         *http.Cookie // Sitzung des Terminal-Benutzers ohne Gerät
	termName            string
}

func newVgEnv(t *testing.T, grantResolve bool) *vgEnv {
	t.Helper()
	x := newPDHEnv(t)
	e := x.e
	v := &vgEnv{pdhEnv: x}
	v.dept, v.otherDept = e.dept("VGA"), e.dept("VGB")
	e.exec(`UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, x.plant, v.dept)
	person := func(name, last, dept string) string {
		id := e.user(name, "technician", false)
		e.exec(`UPDATE users SET first_name = $2, last_name = $3, department_id = $4::uuid WHERE id = $1::uuid`, id, name, last, dept)
		return id
	}
	v.anna, v.berta, v.carla = person("Anna", "VGTest", v.dept), person("Berta", "VGTest", v.dept), person("Carla", "VGTest", v.otherDept)
	v.card = person("Karla", "VGKarte", v.dept)
	for _, p := range []string{"faults.edit", "faults.resolve", "tickets.edit", "tickets.resolve", "tasks.view", "tasks.edit"} {
		e.grant(v.card, p, "", true)
	}
	for _, p := range []string{"tasks.view", "tasks.edit"} {
		e.grant(x.term, p, "", true)
	}
	if grantResolve {
		e.grant(x.term, "faults.resolve", "", true)
		e.grant(x.term, "tickets.resolve", "", true)
	}
	e.warm()
	v.sub = e.infra("VGUnter", "", x.plant)
	v.foreign = e.infra("VGFremd", "", "")
	mk := func(q string, args ...any) string { return e.id(q, args...) }
	v.fault = mk(`INSERT INTO faults (title, description, status, infrastructure_id, created_by) VALUES ('VG-Störung', '', 'detected'::fault_status, $1::uuid, $2::uuid) RETURNING id::text`, x.plant, x.person)
	v.ticket = mk(`INSERT INTO tickets (title, description, status, infrastructure_id, created_by) VALUES ('VG-Ticket', 'x', 'open'::ticket_status, $1::uuid, $2::uuid) RETURNING id::text`, v.sub, x.person)
	v.task = mk(`INSERT INTO tasks (title, status, infrastructure_id, created_by) VALUES ('VG-Aufgabe', 'open', $1::uuid, $2::uuid) RETURNING id::text`, x.plant, x.person)
	v.foreignFault = mk(`INSERT INTO faults (title, description, status, infrastructure_id, created_by) VALUES ('VG-Fremd', '', 'detected'::fault_status, $1::uuid, $2::uuid) RETURNING id::text`, v.foreign, x.person)
	t.Cleanup(func() {
		bg := context.Background()
		for _, ref := range [][2]string{{"fault", v.fault}, {"ticket", v.ticket}, {"task", v.task}, {"fault", v.foreignFault}} {
			e.pool.Exec(bg, `DELETE FROM record_history WHERE ref_type = $1 AND ref_id = $2::uuid`, ref[0], ref[1])
			e.pool.Exec(bg, `DELETE FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, ref[1])
			e.pool.Exec(bg, `DELETE FROM time_entries WHERE ref_id = $1::uuid`, ref[1])
			e.pool.Exec(bg, `DELETE FROM global_dashboard_actions WHERE ref_id = $1::uuid`, ref[1])
		}
		e.pool.Exec(bg, `DELETE FROM tasks WHERE id = $1::uuid`, v.task)
		e.pool.Exec(bg, `DELETE FROM faults WHERE id = ANY($1::uuid[])`, []string{v.fault, v.foreignFault})
		e.pool.Exec(bg, `DELETE FROM tickets WHERE id = $1::uuid`, v.ticket)
	})
	x.h.faults = faults.NewService(faults.NewRepository(e.pool), nil)
	x.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	x.h.tasks = tasks.NewService(tasks.NewRepository(e.pool))
	r := chi.NewRouter()
	r.Use(x.h.authMiddleware)
	x.h.terminalRoutes(r)
	r.Get("/complete/{type}/{id}", x.h.CompletionInfoWeb)
	r.Post("/complete/{type}/{id}", x.h.CompletionWeb)
	v.mux2 = r
	tu := &users.User{ID: x.term, Role: users.Role("terminal"), IsSystemUser: true}
	s, err := x.h.users.IssueToken(tu, time.Hour, nil)
	e.must(err)
	v.plainCookie = &http.Cookie{Name: "pdh_token", Value: s}
	var first, last, uname string
	e.must(e.pool.QueryRow(e.ctx, `SELECT first_name, last_name, username FROM users WHERE id = $1::uuid`, x.term).Scan(&first, &last, &uname))
	v.termName = displayUserName(first, last, uname)
	return v
}

func (v *vgEnv) req(method, target string, form url.Values, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if c != nil {
		req.AddCookie(c)
	}
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	rec := httptest.NewRecorder()
	v.mux2.ServeHTTP(rec, req)
	return rec
}

// finish: Fertigmelden mit gewählter Person (ohne Karte nie anonym).
func (v *vgEnv) finish(typ, id string) *httptest.ResponseRecorder {
	return v.req("POST", "/terminal/vorgaenge/beenden", url.Values{"type": {typ}, "id": {id}, "who": {v.anna}}, v.devCookie, nil)
}

func (v *vgEnv) post(path, typ, id string, people ...string) *httptest.ResponseRecorder {
	f := url.Values{"type": {typ}, "id": {id}}
	for _, p := range people {
		f.Add("people", p)
	}
	return v.req("POST", path, f, v.devCookie, nil)
}

func (v *vgEnv) status(table, id string) string {
	return v.e.id(`SELECT status::text FROM `+table+` WHERE id = $1::uuid`, id)
}

type vgHist struct {
	Via, Place, Msg string
	People          int
	By              string
}

func (v *vgEnv) lastHistory(refType, id string) (h vgHist) {
	v.e.t.Helper()
	err := v.e.pool.QueryRow(v.e.ctx, `SELECT origin_via, place, COALESCE(message, ''), cardinality(people_ids), COALESCE(created_by::text, '')
		FROM record_history WHERE ref_type = $1 AND ref_id = $2::uuid AND origin_via <> '' ORDER BY created_at DESC, id DESC LIMIT 1`, refType, id).
		Scan(&h.Via, &h.Place, &h.Msg, &h.People, &h.By)
	if err != nil {
		v.e.t.Fatalf("kein Verlaufseintrag mit Herkunft zu %s %s: %v", refType, id, err)
	}
	return h
}

func TestVorgaengeStartOhneKarteWaehlt(t *testing.T) {
	v := newVgEnv(t, true)
	// Liste: Vorgänge der Anlage und Unteranlage mit Status, nichts Fremdes
	list := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil)
	if list.Code != 200 {
		t.Fatalf("liste: %d %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	for _, want := range []string{"VG-Störung", "VG-Ticket", "VG-Aufgabe", "Offen"} {
		if !strings.Contains(body, want) {
			t.Errorf("Liste ohne %q", want)
		}
	}
	if strings.Contains(body, "VG-Fremd") {
		t.Error("Vorgang einer fremden Anlage in der Liste")
	}
	// Fenster: Auswahl nur mit Personen der Abteilung des Terminals
	win := v.req("GET", "/terminal/vorgaenge?sel=fault:"+v.fault, nil, v.devCookie, nil).Body.String()
	if !strings.Contains(win, `name="people" value="`+v.anna+`"`) || !strings.Contains(win, `value="`+v.berta+`"`) {
		t.Error("Auswahl ohne die Mitarbeitenden der Abteilung")
	}
	if strings.Contains(win, v.carla) {
		t.Error("Auswahl zeigt Person einer fremden Abteilung")
	}
	// Starten: Störung
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna, v.berta); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if st := v.status("faults", v.fault); st != "in_progress" {
		t.Errorf("Status %s", st)
	}
	if as := v.e.id(`SELECT assigned_to::text FROM faults WHERE id = $1::uuid`, v.fault); as != v.anna {
		t.Errorf("zugewiesen: %s", as)
	}
	if n := v.e.id(`SELECT COUNT(*)::text FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, v.fault); n != "2" {
		t.Errorf("gewählte Personen: %s", n)
	}
	h := v.lastHistory("fault", v.fault)
	if h.Via != vorgViaGewaehlt || h.People != 2 || h.Place != v.termName || h.By != v.term || !strings.Contains(h.Msg, "am Terminal gewählt") {
		t.Errorf("Verlauf: %+v", h)
	}
	// Aufgabe: beide Personen zugewiesen
	if rec := v.post("/terminal/vorgaenge/start", "task", v.task, v.anna, v.berta); rec.Code != http.StatusSeeOther {
		t.Fatalf("start aufgabe: %d %s", rec.Code, rec.Body.String())
	}
	if n := v.e.id(`SELECT COUNT(*)::text FROM task_assignees WHERE task_id = $1::uuid`, v.task); n != "2" || v.status("tasks", v.task) != "in_progress" {
		t.Errorf("Aufgabe: Zugewiesene %s, Status %s", n, v.status("tasks", v.task))
	}
	// Liste zeigt „In Arbeit“ mit den Personen
	if b := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil).Body.String(); !strings.Contains(b, "In Arbeit") || !strings.Contains(b, "Anna VGTest") {
		t.Error("Liste zeigt Arbeitsstand nicht")
	}
}

func TestVorgaengeGrenzen(t *testing.T) {
	v := newVgEnv(t, true)
	// Person außerhalb der Abteilungen des Terminals
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna, v.carla); rec.Code != http.StatusForbidden {
		t.Errorf("fremde Abteilung: %d", rec.Code)
	}
	if v.status("faults", v.fault) != "detected" {
		t.Error("Vorgang trotz Ablehnung verändert")
	}
	// Vorgang außerhalb der Anlage des Terminals
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.foreignFault, v.anna); rec.Code != http.StatusNotFound {
		t.Errorf("fremde Anlage: %d", rec.Code)
	}
	if rec := v.req("GET", "/terminal/vorgaenge?sel=fault:"+v.foreignFault, nil, v.devCookie, nil); strings.Contains(rec.Body.String(), `action="/terminal/vorgaenge/start"`) {
		t.Error("Fenster zu fremdem Vorgang")
	}
	// ohne Gerätesitzung: 403 bei jedem Schreibweg
	for _, p := range []string{"start", "warten", "beenden"} {
		f := url.Values{"type": {"fault"}, "id": {v.fault}, "people": {v.anna}}
		if rec := v.req("POST", "/terminal/vorgaenge/"+p, f, v.plainCookie, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Gerätesitzung: %d", p, rec.Code)
		}
	}
	// ohne Person
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=person") || v.status("faults", v.fault) != "detected" {
		t.Errorf("ohne Person: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// unbekannte Art
	if rec := v.post("/terminal/vorgaenge/start", "unbekannt", v.fault, v.anna); rec.Code != http.StatusBadRequest {
		t.Errorf("unbekannte Art: %d", rec.Code)
	}
	// Terminal darf nicht beenden, wenn ihm das Recht fehlt (Rolle Terminal hat kein *.resolve)
	v2 := newVgEnv(t, false)
	v2.post("/terminal/vorgaenge/start", "fault", v2.fault, v2.anna)
	if rec := v2.post("/terminal/vorgaenge/beenden", "fault", v2.fault); rec.Code != http.StatusForbidden {
		t.Errorf("beenden ohne Recht: %d", rec.Code)
	}
}

func TestVorgaengeNurKarte(t *testing.T) {
	v := newVgEnv(t, true)
	v.e.must(v.h.saveTerminalVorgaengeModus(v.e.ctx, v.term, vorgModusNurKarte, v.term))
	// ohne Karte: nur Ansicht, kein Schreiben
	page := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil).Body.String()
	if !strings.Contains(page, "Nur Ansicht") || strings.Contains(page, `name="people"`) || !strings.Contains(page, "VG-Störung") {
		t.Error("nur_karte ohne Karte: erwartet Ansicht ohne Bedienung")
	}
	for _, p := range []string{"start", "warten", "beenden"} {
		if rec := v.post("/terminal/vorgaenge/"+p, "fault", v.fault, v.anna); rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Karte: %d", p, rec.Code)
		}
	}
	if v.status("faults", v.fault) != "detected" {
		t.Error("ohne Karte verändert")
	}
	// Karte einer Person ohne Recht (Betrachter): ihre eigenen Rechte gelten, nicht die des Terminals
	v.pdhEnv.card(v.person)
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("Karte ohne Recht: %d", rec.Code)
	}
	// Karte mit Recht: Person ist vorgewählt, handelt selbst, Herkunft „mit Karte“
	v.pdhEnv.card(v.card)
	win := v.req("GET", "/terminal/vorgaenge?sel=fault:"+v.fault, nil, v.devCookie, nil).Body.String()
	if strings.Contains(win, `value="`+v.berta+`" checked`) || !strings.Contains(win, `value="`+v.berta+`"`) {
		t.Error("Auswahl: Berta unerwartet vorgewählt")
	}
	// die Kartenperson steht in der Abteilung des Terminals und ist vorgewählt
	if !strings.Contains(win, `value="`+v.card+`" checked`) {
		t.Errorf("Kartenperson nicht vorgewählt: %s", win)
	}
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.card, v.berta); rec.Code != http.StatusSeeOther {
		t.Fatalf("start mit Karte: %d %s", rec.Code, rec.Body.String())
	}
	h := v.lastHistory("fault", v.fault)
	if h.Via != vorgViaKarte || h.By != v.card || h.People != 2 || !strings.Contains(h.Msg, "mit Karte") {
		t.Errorf("Verlauf mit Karte: %+v", h)
	}
	if as := v.e.id(`SELECT assigned_to::text FROM faults WHERE id = $1::uuid`, v.fault); as != v.card {
		t.Errorf("zugewiesen: %s", as)
	}
	// Status der Kartenperson: ihr Vorgang mit Beenden und Warten zur Hand
	st := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil).Body.String()
	if !strings.Contains(st, "Meine Vorgänge") || !strings.Contains(st, `action="/terminal/vorgaenge/beenden"`) || !strings.Contains(st, `action="/terminal/vorgaenge/warten"`) {
		t.Error("Status der Kartenperson ohne Beenden/Warten")
	}
	// andere Karte: nicht ihr Vorgang
	v.pdhEnv.card(v.anna)
	if st := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil).Body.String(); strings.Contains(st, "Meine Vorgänge") {
		t.Error("fremde Karte sieht „Meine Vorgänge“")
	}
}

func TestVorgaengeKarteGiltAuchOhneKartenzwang(t *testing.T) {
	v := newVgEnv(t, true) // ohne_karte, aber Karte steckt: die Person handelt mit ihren Rechten
	v.pdhEnv.card(v.person)
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna); rec.Code != http.StatusForbidden {
		t.Errorf("Karte ohne Recht, ohne_karte: %d", rec.Code)
	}
}

func TestVorgaengeWartenBehaeltZuweisung(t *testing.T) {
	v := newVgEnv(t, true)
	// offener Vorgang: Warten nicht möglich
	if rec := v.post("/terminal/vorgaenge/warten", "ticket", v.ticket); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=gone") || v.status("tickets", v.ticket) != "open" {
		t.Errorf("warten auf offenem Vorgang: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	v.post("/terminal/vorgaenge/start", "ticket", v.ticket, v.anna, v.berta)
	if rec := v.post("/terminal/vorgaenge/warten", "ticket", v.ticket); rec.Code != http.StatusSeeOther {
		t.Fatalf("warten: %d %s", rec.Code, rec.Body.String())
	}
	if v.status("tickets", v.ticket) != "pending" {
		t.Errorf("Status %s", v.status("tickets", v.ticket))
	}
	if as := v.e.id(`SELECT assigned_to::text FROM tickets WHERE id = $1::uuid`, v.ticket); as != v.anna {
		t.Errorf("Zuweisung weg: %s", as)
	}
	if n := v.e.id(`SELECT COUNT(*)::text FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, v.ticket); n != "2" {
		t.Errorf("Personen weg: %s", n)
	}
	h := v.lastHistory("ticket", v.ticket)
	if h.Via != vorgViaGewaehlt || h.People != 2 || !strings.Contains(h.Msg, "wartet") {
		t.Errorf("Verlauf: %+v", h)
	}
	if b := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil).Body.String(); !strings.Contains(b, "Wartet") {
		t.Error("Liste zeigt „Wartet“ nicht")
	}
	// wartende Vorgänge lassen sich wieder starten, die gewählten Personen sind vorbelegt
	win := v.req("GET", "/terminal/vorgaenge?sel=ticket:"+v.ticket, nil, v.devCookie, nil).Body.String()
	if !strings.Contains(win, `value="`+v.anna+`" checked`) || !strings.Contains(win, `value="`+v.berta+`" checked`) {
		t.Error("wartender Vorgang: Personen nicht vorbelegt")
	}
	// Aufgabe: ebenso
	v.post("/terminal/vorgaenge/start", "task", v.task, v.anna)
	v.post("/terminal/vorgaenge/warten", "task", v.task)
	if v.status("tasks", v.task) != "pending" || v.e.id(`SELECT COUNT(*)::text FROM task_assignees WHERE task_id = $1::uuid`, v.task) != "1" {
		t.Error("Aufgabe wartet nicht oder verlor die Zuweisung")
	}
}

func TestVorgaengeBeendenAufgabe(t *testing.T) {
	v := newVgEnv(t, true)
	if rec := v.finish("task", v.task); rec.Code != http.StatusConflict {
		t.Errorf("beenden ohne Start: %d", rec.Code)
	}
	v.post("/terminal/vorgaenge/start", "task", v.task, v.anna, v.berta)
	rec := v.finish("task", v.task)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=beendet") {
		t.Fatalf("beenden: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if v.status("tasks", v.task) != "resolved" {
		t.Errorf("Status %s", v.status("tasks", v.task))
	}
	h := v.lastHistory("task", v.task)
	if h.Via != vorgViaGewaehlt || h.People != 2 || !strings.Contains(h.Msg, "erledigt") {
		t.Errorf("Verlauf: %+v", h)
	}
}

func TestVorgaengeBeendenStoerungMitAssistent(t *testing.T) {
	v := newVgEnv(t, true)
	v.post("/terminal/vorgaenge/start", "ticket", v.ticket, v.anna, v.berta)
	rec := v.finish("ticket", v.ticket)
	if rec.Code != http.StatusOK {
		t.Fatalf("beenden: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token, Who string
		Pre        []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("keine Token-Antwort: %s", rec.Body.String())
	}
	if len(out.Pre) != 1 || out.Pre[0] != v.berta || !strings.HasPrefix(out.Who, "Anna") {
		t.Errorf("Assistent: Akteur %q, vorgewählt %v", out.Who, out.Pre)
	}
	auth := map[string]string{"Authorization": "Bearer " + out.Token}
	// der Assistent läuft mit dem Token an der Gerätesitzung des Terminals: Daten laden …
	if rec := v.req("GET", "/complete/ticket/"+v.ticket, nil, v.devCookie, auth); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("assistent laden: %d %s", rec.Code, rec.Body.String())
	}
	// … ohne Gerätesitzung (anderer Client) oder mit Sitzung ohne Gerät gilt das Token nicht
	if v.tokenOK(out.Token, "/complete/ticket/"+v.ticket, nil) || v.tokenOK(out.Token, "/complete/ticket/"+v.ticket, v.plainCookie) {
		t.Error("Token gilt ohne Gerätesitzung des Terminals")
	}
	if rec := v.req("GET", "/complete/ticket/"+v.ticket, nil, nil, auth); rec.Code == 200 {
		t.Error("Assistent ohne Sitzung erreichbar")
	}
	// … das Token gilt nur für diesen Vorgang
	if v.tokenOK(out.Token, "/complete/fault/"+v.fault, v.devCookie) {
		t.Error("Token gilt für einen anderen Vorgang")
	}
	// … und abschließen (Zeit, Maßnahme und Verlauf tragen den Hinweis „nicht selbst angemeldet“)
	body := `{"no_parts":true,"minutes":15,"comment":"Lager getauscht","finish":true,"colleagues":[]}`
	req := httptest.NewRequest("POST", "/complete/ticket/"+v.ticket, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+out.Token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(v.devCookie)
	done := httptest.NewRecorder()
	v.mux2.ServeHTTP(done, req)
	if done.Code != 200 || !strings.Contains(done.Body.String(), `"closed":true`) {
		t.Fatalf("abschluss: %d %s", done.Code, done.Body.String())
	}
	if v.status("tickets", v.ticket) != "resolved" {
		t.Errorf("Status %s", v.status("tickets", v.ticket))
	}
	h := v.lastHistory("ticket", v.ticket)
	if h.Via != vorgViaGewaehlt || h.People != 2 || h.Place != v.termName || !strings.Contains(h.Msg, "abgeschlossen") || !strings.Contains(h.Msg, "nicht selbst angemeldet") {
		t.Errorf("Verlauf: %+v", h)
	}
	if d := v.e.id(`SELECT description FROM time_entries WHERE ref_id = $1::uuid ORDER BY started_at DESC LIMIT 1`, v.ticket); !strings.Contains(d, "nicht selbst angemeldet") {
		t.Errorf("Zeiteintrag ohne Hinweis: %q", d)
	}
	if d := v.e.id(`SELECT string_agg(description, ' | ') FROM ticket_actions WHERE ticket_id = $1::uuid`, v.ticket); !strings.Contains(d, "nicht selbst angemeldet") {
		t.Errorf("Maßnahme ohne Hinweis: %q", d)
	}
	if n := v.e.id(`SELECT COUNT(*)::text FROM global_dashboard_actions WHERE ref_id = $1::uuid`, v.ticket); n != "0" {
		t.Errorf("Terminal-Abschluss steht als Leitstand-Aktion: %s", n)
	}
	// einmalig: nach dem Abschluss ist das Token verbraucht
	if v.tokenOK(out.Token, "/complete/ticket/"+v.ticket, v.devCookie) {
		t.Error("Token nach dem Abschluss noch gültig")
	}
	// mit Karte: Herkunft „mit Karte“, die Person ist der Akteur
	v.pdhEnv.card(v.card)
	v.post("/terminal/vorgaenge/start", "fault", v.fault, v.card)
	rec = v.post("/terminal/vorgaenge/beenden", "fault", v.fault)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"who":"Karla VGKarte"`) {
		t.Errorf("beenden mit Karte: %d %s", rec.Code, rec.Body.String())
	}
}

// tokenOK: erkennt der Server das Abschluss-Token für diesen Pfad und diese Sitzung an?
func (v *vgEnv) tokenOK(token, path string, c *http.Cookie) bool {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if c != nil {
		req.AddCookie(c)
	}
	return v.h.boardCompleteClaims(req) != nil
}

func (v *vgEnv) issue(t *testing.T, typ, id string) string {
	t.Helper()
	rec := v.finish(typ, id)
	var out struct{ Token string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" {
		t.Fatalf("kein Token: %d %s", rec.Code, rec.Body.String())
	}
	return out.Token
}

func TestVorgaengeTokenGrenzen(t *testing.T) {
	v := newVgEnv(t, true)
	// nur das neueste Token je Vorgang gilt
	v.post("/terminal/vorgaenge/start", "ticket", v.ticket, v.anna)
	t1 := v.issue(t, "ticket", v.ticket)
	path := "/complete/ticket/" + v.ticket
	if !v.tokenOK(t1, path, v.devCookie) {
		t.Fatal("frisches Token gilt nicht")
	}
	t2 := v.issue(t, "ticket", v.ticket)
	if v.tokenOK(t1, path, v.devCookie) || !v.tokenOK(t2, path, v.devCookie) {
		t.Error("nur das neueste Token darf gelten")
	}
	// Laufzeit 5 Minuten
	parts := strings.Split(t2, ".")
	raw, _ := base64URLDecode(parts[1])
	var cl struct{ Exp, Iat int64 }
	_ = json.Unmarshal(raw, &cl)
	if d := cl.Exp - cl.Iat; d != int64(terminalCompleteTTL.Seconds()) {
		t.Errorf("Laufzeit %d s", d)
	}
	// mit Karte: nach der Kartenabmeldung (oder anderer Karte) gilt das Token nicht mehr
	v.pdhEnv.card(v.card)
	v.post("/terminal/vorgaenge/start", "fault", v.fault, v.card)
	tk := v.issue(t, "fault", v.fault)
	fpath := "/complete/fault/" + v.fault
	if !v.tokenOK(tk, fpath, v.devCookie) {
		t.Fatal("Kartentoken gilt nicht")
	}
	v.pdhEnv.card(v.anna)
	if v.tokenOK(tk, fpath, v.devCookie) {
		t.Error("Token gilt bei anderer Kartenperson")
	}
	v.pdhEnv.card(v.card)
	if !v.tokenOK(tk, fpath, v.devCookie) {
		t.Fatal("Kartentoken gilt nach erneuter Karte nicht")
	}
	v.e.must(v.h.cardSessionEnd(v.e.ctx, v.term))
	if v.tokenOK(tk, fpath, v.devCookie) {
		t.Error("Token gilt nach der Kartenabmeldung")
	}
	// das Gerät eines anderen Terminals reicht nicht
	if v.tokenOK(tk, fpath, v.otherCookieForeign(t)) {
		t.Error("Token gilt an einem fremden Terminal")
	}
}

// otherCookieForeign: Gerätesitzung eines anderen Terminal-Benutzers.
func (v *vgEnv) otherCookieForeign(t *testing.T) *http.Cookie {
	t.Helper()
	e := v.e
	term2 := e.user("vgterm2", "terminal", true)
	dev := e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi3', $2) RETURNING id::text`, term2, hashDeviceSecret("pdht_c"+e.sfx))
	t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM terminal_devices WHERE id = $1::uuid`, dev) })
	tok, err := v.h.users.IssueToken(&users.User{ID: term2, Role: users.Role("terminal"), IsSystemUser: true}, time.Hour, map[string]interface{}{"device": dev})
	e.must(err)
	return &http.Cookie{Name: "pdh_token", Value: tok}
}

func TestVorgaengeNeustartMeldetEntfernte(t *testing.T) {
	v := newVgEnv(t, true)
	v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna, v.berta)
	v.post("/terminal/vorgaenge/warten", "fault", v.fault)
	v.post("/terminal/vorgaenge/start", "fault", v.fault, v.anna)
	h := v.lastHistory("fault", v.fault)
	if !strings.Contains(h.Msg, "nicht mehr dabei: Berta VGTest") {
		t.Errorf("Verlauf nennt entfernte Person nicht: %s", h.Msg)
	}
}

func TestVorgaengeSchalterAus(t *testing.T) {
	v := newVgEnv(t, true)
	if b := v.req("GET", "/terminal/neu", nil, v.devCookie, nil).Body.String(); !strings.Contains(b, `href="/terminal/vorgaenge"`) {
		t.Error("Kachel „Vorgänge“ fehlt bei eingeschaltetem Schalter (Grundwert an)")
	}
	if _, err := v.h.setTerminalFunctions(v.e.ctx, v.term, map[string]bool{"vorgaenge": false}); err != nil {
		t.Fatal(err)
	}
	if b := v.req("GET", "/terminal/neu", nil, v.devCookie, nil).Body.String(); strings.Contains(b, `href="/terminal/vorgaenge"`) {
		t.Error("Kachel trotz ausgeschaltetem Schalter")
	}
	if rec := v.req("GET", "/terminal/vorgaenge", nil, v.devCookie, nil); rec.Code != http.StatusNotFound {
		t.Errorf("Seite: %d", rec.Code)
	}
	for _, p := range []string{"start", "warten", "beenden"} {
		if rec := v.post("/terminal/vorgaenge/"+p, "fault", v.fault, v.anna); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if v.status("faults", v.fault) != "detected" {
		t.Error("Vorgang trotz ausgeschalteter Funktion verändert")
	}
	// der Terminal-Stand liefert den Schalter auch ohne ihn zu kennen (PDH-Schalter, Vertrag bleibt)
	fn, _, err := v.h.terminalFnPayload(v.e.ctx, v.term)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := fn["vorgaenge"].(map[string]any); !ok || m["enabled"] != false {
		t.Errorf("functions.vorgaenge: %v", fn["vorgaenge"])
	}
}

func TestTerminalDepartmentsHelper(t *testing.T) {
	v := newVgEnv(t, true)
	ctx := v.e.ctx
	get := func() []string {
		d, err := v.h.terminalDepartments(ctx, v.term)
		v.e.must(err)
		return d
	}
	// Vorgabe: Abteilung der Anlage
	if d := get(); len(d) != 1 || d[0] != v.dept {
		t.Errorf("Vorgabe: %v", d)
	}
	// eingestellt
	v.e.must(v.h.saveTerminalDepartmentsForm(ctx, []string{v.otherDept, "kein-uuid"}, v.term, v.term))
	if d := get(); len(d) != 1 || d[0] != v.otherDept {
		t.Errorf("eingestellt: %v", d)
	}
	people, err := v.h.terminalDepartmentPeople(ctx, v.term)
	v.e.must(err)
	// Carla (eingestellte Abteilung) ist dabei, Anna und Berta (nicht mehr eingestellt) nicht; die Instandhaltung
	// der Testdatenbank kommt immer dazu
	got := map[string]bool{}
	for _, p := range people {
		got[p.ID] = true
	}
	if !got[v.carla] || got[v.anna] || got[v.berta] {
		t.Errorf("Personen: %v", people)
	}
	// nichts angehakt = Vorgabe
	v.e.must(v.h.saveTerminalDepartmentsForm(ctx, nil, v.term, v.term))
	if d := get(); len(d) != 1 || d[0] != v.dept {
		t.Errorf("zurück auf Vorgabe: %v", d)
	}
	// Betriebsart: Grundwert ohne_karte
	if m := v.h.terminalVorgaengeModus(ctx, v.term); m != vorgModusOhneKarte {
		t.Errorf("Betriebsart: %s", m)
	}
}

func base64URLDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// Auswahl der Personen: Die Personen der Abteilung „Instandhaltung“ stehen immer in der Liste – nicht nur ersatzweise –
// und sind (wie jede Person der Auswahl) auch die erlaubten Zuweisungen.
func TestVorgaengeAuswahlInstandhaltungImmer(t *testing.T) {
	v := newVgEnv(t, true)
	e := v.e
	ctx := context.Background()
	// vorhandene Instandhaltungs-Abteilungen der Testdatenbank für diesen Test ausblenden
	old, err := v.h.queryStrings(ctx, `SELECT id::text FROM departments WHERE active AND lower(btrim(name)) = 'instandhaltung'`)
	e.must(err)
	e.exec(`UPDATE departments SET active = false WHERE id = ANY($1::uuid[])`, old)
	t.Cleanup(func() { _, _ = e.pool.Exec(ctx, `UPDATE departments SET active = true WHERE id = ANY($1::uuid[])`, old) })

	pick := func() map[string]bool {
		ids := map[string]bool{}
		people, err := v.h.terminalPickPeople(ctx, v.term)
		e.must(err)
		for _, p := range people {
			ids[p.ID] = true
		}
		return ids
	}
	// ohne Abteilung Instandhaltung: nur die Abteilung des Terminals
	if ids := pick(); !ids[v.anna] || !ids[v.berta] || ids[v.carla] {
		t.Errorf("ohne Instandhaltung: %v", ids)
	}
	// mit Instandhaltung: die Personen kommen dazu, obwohl die Abteilung des Terminals Personen hat
	maint := e.id(`INSERT INTO departments (name) VALUES ('  INSTANDHALTUNG ') RETURNING id::text`)
	t.Cleanup(func() { _, _ = e.pool.Exec(ctx, `DELETE FROM departments WHERE id = $1::uuid`, maint) })
	mp := e.user("Ina", "technician", false)
	e.exec(`UPDATE users SET first_name = 'Ina', last_name = 'VGTest', department_id = $2::uuid WHERE id = $1::uuid`, mp, maint)
	if ids := pick(); !ids[v.anna] || !ids[v.berta] || !ids[mp] || ids[v.carla] {
		t.Errorf("mit Instandhaltung: %v", ids)
	}
	win := v.req("GET", "/terminal/vorgaenge?sel=fault:"+v.fault, nil, v.devCookie, nil).Body.String()
	if !strings.Contains(win, `name="people" value="`+mp+`"`) || !strings.Contains(win, `value="`+v.anna+`"`) {
		t.Error("Fenster ohne Instandhaltung oder ohne Abteilung des Terminals")
	}
	// Zuweisung: genau die Auswahl ist erlaubt
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, v.carla); rec.Code != http.StatusForbidden {
		t.Errorf("Person anderer Abteilung: %d", rec.Code)
	}
	if rec := v.post("/terminal/vorgaenge/start", "fault", v.fault, mp, v.anna); rec.Code != http.StatusSeeOther {
		t.Errorf("Person aus Instandhaltung: %d %s", rec.Code, rec.Body.String())
	}
	// ist die Person inaktiv, fällt sie aus der Auswahl
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, mp)
	if ids := pick(); ids[mp] {
		t.Error("inaktive Person der Instandhaltung in der Auswahl")
	}
}
