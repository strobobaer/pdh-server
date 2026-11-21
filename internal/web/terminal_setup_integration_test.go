//go:build integration

// Rüsttasten: Vererbung von Halle/Linie auf Anlagen darunter (Easy-Modus-Baustein) und die
// Terminalseite /terminal/rusten. Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalSetup ./internal/web/
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
	"pdh/internal/modules/ruesten"
)

func TestTerminalSetupInheritedButtons(t *testing.T) {
	e := newATEnv(t)
	hall := e.id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "RTHalle"+e.sfx)
	line := e.id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "RTLinie"+e.sfx, hall)
	plant := e.infra("RTAnlage", "", line)
	// Aufräumen läuft rückwärts: erst Anlage (infra()), dann hier Rüstzeiten, Zuordnungen, Tasten, Linie, Halle.
	e.t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM setup_times WHERE infrastructure_id = $1::uuid`, plant)
		e.pool.Exec(bg, `DELETE FROM infra_setup_buttons WHERE infrastructure_id = ANY($1::uuid[])`, []string{hall, line, plant})
		e.pool.Exec(bg, `DELETE FROM setup_buttons WHERE name LIKE '%'||$1`, e.sfx)
	})
	e.t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, line)
		e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, hall)
	})
	st := ruesten.NewStore(e.pool)
	b := &ruesten.Button{Name: "Formatwechsel" + e.sfx, ReasonID: e.id(`SELECT id::text FROM setup_reasons WHERE key = 'changeover'`), Active: true}
	e.must(st.SaveButton(e.ctx, b))
	e.must(st.SetInfraButtons(e.ctx, hall, []string{b.ID}))

	term := e.user("rtterm", "terminal", true)
	person := e.user("rtperson", "terminal", false)
	noedit := e.user("rtnoedit", "viewer", false)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, plant)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	terminalTemplatePath = "../../web/templates/terminal.gohtml"

	// Easy-Modus-Baustein: an der Halle zugeordnet, an der Anlage darunter sichtbar
	if bar := h.setupBarFor(e.ctx, "de", plant, "/e/"+plant+"/setup", "", false, ""); !bar.Show || len(bar.Buttons) != 1 || bar.Buttons[0].ID != b.ID {
		t.Fatalf("geerbte Rüsttaste fehlt am Easy-Baustein: %+v", bar)
	}

	r := chi.NewRouter()
	h.terminalRoutes(r)
	do := func(u *users.User, method, target string, form url.Values) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		req = req.WithContext(context.WithValue(req.Context(), "user", u))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	tu := &users.User{ID: term, Role: users.Role("terminal"), IsSystemUser: true}

	// Ohne Anmeldung: Aufforderung zur Anmeldung, keine Bedienung, nie der bloße Terminal-Benutzer
	if body := do(tu, "GET", "/terminal/rusten", nil).Body.String(); !strings.Contains(body, "Bitte zuerst anmelden") || strings.Contains(body, "/terminal/rusten/start") {
		t.Error("ohne Anmeldung: Aufforderung fehlt oder Start angeboten")
	}
	do(tu, "POST", "/terminal/rusten/start", url.Values{"button_id": {b.ID}})
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1::uuid`, plant).Scan(&n))
	if n != 0 {
		t.Fatalf("ohne Anmeldung wurde gerüstet: %d", n)
	}
	e.exec(`INSERT INTO terminal_card_sessions (terminal_user_id, person_id) VALUES ($1::uuid, $2::uuid)`, term, person)
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })

	// Neu: Kachel „Rüsten“ unterhalb von Störung/Ticket
	if body := do(tu, "GET", "/terminal/neu", nil).Body.String(); !strings.Contains(body, `href="/terminal/rusten"`) {
		t.Error("Seite Neu ohne Rüsten-Kachel")
	}
	page := do(tu, "GET", "/terminal/rusten", nil).Body.String()
	if !strings.Contains(page, "Formatwechsel"+e.sfx) || !strings.Contains(page, `action="/terminal/rusten/start"`) || strings.Contains(page, "/e/"+plant) {
		t.Fatalf("Rüsten-Seite: Taste/Formular fehlt oder Verweis auf Easy-Modus:\n%s", page)
	}
	if rec := do(tu, "POST", "/terminal/rusten/start", url.Values{"button_id": {b.ID}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d", rec.Code)
	}
	if page = do(tu, "GET", "/terminal/rusten", nil).Body.String(); !strings.Contains(page, "Rüsten: ") || !strings.Contains(page, `action="/terminal/rusten/finish"`) {
		t.Error("laufendes Rüsten nicht sichtbar")
	}
	if rec := do(tu, "POST", "/terminal/rusten/finish", url.Values{}); rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "setup=") {
		t.Errorf("finish: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := do(tu, "GET", "/terminal/rusten?nachtragen=1", nil).Body.String(); !strings.Contains(body, `name="ago"`) {
		t.Error("Nachtragen-Ansicht fehlt")
	}
	rec := do(tu, "POST", "/terminal/rusten/add", url.Values{"button_id": {b.ID}, "ago": {"60"}, "dur": {"30"}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "setup=added") {
		t.Errorf("add: %d %s", rec.Code, loc)
	}
	rec = do(tu, "POST", "/terminal/rusten/add", url.Values{"button_id": {b.ID}, "ago": {"15"}, "dur": {"30"}})
	if !strings.Contains(rec.Header().Get("Location"), "setup=badtime") {
		t.Errorf("Dauer länger als Beginn zurück: %s", rec.Header().Get("Location"))
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1::uuid`, plant).Scan(&n))
	if n != 2 {
		t.Errorf("Rüstzeiten: %d, erwartet 2", n)
	}

	// Akteur ist die angemeldete Person, nicht der Terminal-Benutzer
	var bad int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1::uuid AND created_by <> $2::uuid`, plant, person).Scan(&bad))
	if bad != 0 {
		t.Errorf("%d Rüstzeiten nicht von der angemeldeten Person erstellt", bad)
	}

	// angemeldete Person ohne setup.edit: keine Bedienung, keine neue Rüstzeit
	e.grant(noedit, "setup.edit", "", false)
	e.warm()
	e.exec(`UPDATE terminal_card_sessions SET person_id = $2::uuid WHERE terminal_user_id = $1::uuid`, term, noedit)
	if body := do(tu, "GET", "/terminal/rusten", nil).Body.String(); !strings.Contains(body, "Keine Berechtigung") {
		t.Error("ohne setup.edit: Hinweis fehlt")
	}
	do(tu, "POST", "/terminal/rusten/start", url.Values{"button_id": {b.ID}})
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1::uuid`, plant).Scan(&n))
	if n != 2 {
		t.Errorf("ohne Recht wurde gerüstet: %d", n)
	}
}
