//go:build integration

// Störung/Ticket am Terminal: eigene Seiten, nur mit Anmeldung (Karte/manuell) und Rechten des Terminal-Benutzers.
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/tickets"
)

func TestTerminalReportFaultAndTicket(t *testing.T) {
	e := newATEnv(t)
	plant := e.infra("RMAnlage", "", "")
	sub := e.infra("RMUnter", "", plant)
	deep := e.infra("RMTief", "", sub)
	term := e.user("rmterm", "terminal", true)
	person := e.user("rmperson", "terminal", false)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, plant)
	e.warm()
	e.t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term)
		e.pool.Exec(bg, `DELETE FROM record_history WHERE created_by = $1::uuid`, person)
		e.pool.Exec(bg, `DELETE FROM faults WHERE infrastructure_id = ANY($1::uuid[])`, []string{plant, sub, deep})
		e.pool.Exec(bg, `DELETE FROM tickets WHERE infrastructure_id = ANY($1::uuid[])`, []string{plant, sub, deep})
	})
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t),
		faults: faults.NewService(faults.NewRepository(e.pool), nil), tickets: tickets.NewService(tickets.NewRepository(e.pool))}
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	r := chi.NewRouter()
	h.terminalRoutes(r)
	tu := &users.User{ID: term, Role: users.Role("terminal"), IsSystemUser: true}
	do := func(method, target string, form url.Values) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		req = req.WithContext(context.WithValue(req.Context(), "user", tu))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	count := func(table string) int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[])`, []string{plant, sub}).Scan(&n))
		return n
	}
	form := func(extra url.Values) url.Values {
		v := url.Values{"infra": {plant}, "text": {"Förderband steht still"}, "kind": {"mechanical"}, "state": {"stopped"}}
		for k, x := range extra {
			v[k] = x
		}
		return v
	}

	// Anlagenwahl in Ebenen: oben nur Terminal-Anlage und direkte Unteranlagen, Texte erst nach der Wahl
	var textID string
	e.must(e.pool.QueryRow(e.ctx, `INSERT INTO easy_report_texts (text, report_type, kind, sort) VALUES ('Lager klemmt', 'both', 'mechanical', 9999) RETURNING id::text`).Scan(&textID))
	e.exec(`INSERT INTO infra_easy_texts (infrastructure_id, text_id) VALUES ($1::uuid, $2::uuid)`, sub, textID)
	t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM easy_report_texts WHERE id = $1::uuid`, textID) })

	top := do("GET", "/terminal/stoerung", nil).Body.String()
	if !strings.Contains(top, "?nav="+sub) || strings.Contains(top, deep) || strings.Contains(top, `name="kind"`) || strings.Contains(top, "Lager klemmt") || strings.Contains(top, "Bitte zuerst anmelden") {
		t.Error("Anlagenwahl oben: Ebene falsch, Formular oder Meldetexte schon sichtbar, oder Anmeldung verlangt")
	}
	if b := do("GET", "/terminal/stoerung?nav="+sub, nil).Body.String(); !strings.Contains(b, "?infra="+deep) || !strings.Contains(b, "?infra="+sub) || strings.Contains(b, "Lager klemmt") {
		t.Error("Ebene Unteranlage: Wahl der Unteranlage oder der Tiefen-Anlage fehlt")
	}
	if b := do("GET", "/terminal/stoerung?infra="+sub, nil).Body.String(); !strings.Contains(b, `name="kind"`) || !strings.Contains(b, "Lager klemmt") || !strings.Contains(b, `class="t-rzname"`) {
		t.Error("nach Wahl: Formular, Meldetext oder Namensfeld fehlt")
	}
	if b := do("GET", "/terminal/stoerung?infra="+plant, nil).Body.String(); strings.Contains(b, "Lager klemmt") {
		t.Error("Meldetext der Unteranlage erscheint an der Terminal-Anlage")
	}
	// ohne Anmeldung: Terminal-Benutzer meldet, Kürzel steht in der Beschreibung; ohne Anlagenwahl nichts
	if loc := do("POST", "/terminal/stoerung", form(url.Values{"infra": {""}})).Header().Get("Location"); !strings.Contains(loc, "m=plant") {
		t.Errorf("ohne Anlagenwahl: %s", loc)
	}
	if loc := do("POST", "/terminal/stoerung", form(url.Values{"infra": {plant}, "name": {"MK7"}})).Header().Get("Location"); !strings.Contains(loc, "done=fault") {
		t.Fatalf("Störung ohne Anmeldung: %s", loc)
	}
	if loc := do("POST", "/terminal/ticket", form(url.Values{"infra": {deep}, "name": {"MK7"}, "text_id": {textID}, "state": {"running"}})).Header().Get("Location"); !strings.Contains(loc, "done=ticket") {
		t.Fatalf("Ticket ohne Anmeldung: %s", loc)
	}
	var by, desc string
	e.must(e.pool.QueryRow(e.ctx, `SELECT created_by::text, description FROM faults WHERE infrastructure_id = $1::uuid`, plant).Scan(&by, &desc))
	if by != term || !strings.Contains(desc, "MK7") {
		t.Errorf("Störung ohne Anmeldung: Ersteller %s (erwartet Terminal %s), Beschreibung %q", by, term, desc)
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT created_by::text, description FROM tickets WHERE infrastructure_id = $1::uuid`, deep).Scan(&by, &desc))
	if by != term || !strings.Contains(desc, "MK7") || !strings.Contains(desc, "Lager klemmt") {
		t.Errorf("Ticket ohne Anmeldung: Ersteller %s, Beschreibung %q", by, desc)
	}
	e.exec(`DELETE FROM faults WHERE infrastructure_id = ANY($1::uuid[])`, []string{plant, sub, deep})
	e.exec(`DELETE FROM tickets WHERE infrastructure_id = ANY($1::uuid[])`, []string{plant, sub, deep})

	// angemeldet: Seite mit Formular (Anlage und Unteranlage), Störung und Ticket werden von der Person angelegt
	e.exec(`INSERT INTO terminal_card_sessions (terminal_user_id, person_id) VALUES ($1::uuid, $2::uuid)`, term, person)
	if b := do("GET", "/terminal/stoerung?infra="+sub, nil).Body.String(); !strings.Contains(b, `name="kind"`) || !strings.Contains(b, `value="`+sub+`"`) || strings.Contains(b, "/e/"+plant) {
		t.Error("Meldeseite: Formular, Unteranlage fehlt oder Verweis auf Easy-Modus")
	}
	if loc := do("POST", "/terminal/stoerung", form(nil)).Header().Get("Location"); !strings.Contains(loc, "done=fault") {
		t.Fatalf("Störung: %s", loc)
	}
	if loc := do("POST", "/terminal/ticket", form(url.Values{"infra": {sub}, "state": {"running"}})).Header().Get("Location"); !strings.Contains(loc, "done=ticket") {
		t.Fatalf("Ticket: %s", loc)
	}
	var stopped bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT created_by::text, plant_stopped FROM faults WHERE infrastructure_id = $1::uuid`, plant).Scan(&by, &stopped))
	if by != person || !stopped {
		t.Errorf("Störung: Ersteller %s (erwartet %s), Anlage steht (Alarm) = %v", by, person, stopped)
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT created_by::text FROM tickets WHERE infrastructure_id = $1::uuid`, sub).Scan(&by))
	if by != person {
		t.Errorf("Ticket: Ersteller %s", by)
	}
	// fremde Anlage und ungültige Eingabe werden abgewiesen
	other := e.infra("RMFremd", "", "")
	if loc := do("POST", "/terminal/stoerung", form(url.Values{"infra": {other}})).Header().Get("Location"); !strings.Contains(loc, "m=perm") {
		t.Errorf("fremde Anlage: %s", loc)
	}
	if loc := do("POST", "/terminal/stoerung", url.Values{"infra": {plant}, "kind": {"mechanical"}, "state": {"running"}}).Header().Get("Location"); !strings.Contains(loc, "m=input") {
		t.Errorf("ohne Text: %s", loc)
	}

	// Terminal-Benutzer ohne faults.edit: Seite „Keine Berechtigung“, nichts angelegt
	e.grant(term, "faults.edit", "", false)
	e.warm()
	if b := do("GET", "/terminal/stoerung", nil).Body.String(); !strings.Contains(b, "Keine Berechtigung") {
		t.Error("ohne faults.edit: Hinweis fehlt")
	}
	before := count("faults")
	do("POST", "/terminal/stoerung", form(nil))
	if count("faults") != before {
		t.Error("ohne faults.edit wurde gemeldet")
	}
	if loc := do("POST", "/terminal/ticket", form(nil)).Header().Get("Location"); !strings.Contains(loc, "done=ticket") {
		t.Errorf("Ticket bleibt erlaubt: %s", loc)
	}
}

func TestTerminalReportCrossSiteRefused(t *testing.T) {
	e := newATEnv(t)
	plant := e.infra("RMCross", "", "")
	term := e.user("rmcterm", "terminal", true)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, plant)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	h.terminalRoutes(r)
	for _, p := range []string{"/terminal/stoerung", "/terminal/ticket"} {
		req := httptest.NewRequest("POST", p, strings.NewReader("text=abc&kind=mechanical&state=running"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: term, Role: users.Role("terminal"), IsSystemUser: true}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-site: %d", p, rec.Code)
		}
	}
}
