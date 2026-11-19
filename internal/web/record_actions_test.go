package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
)

// Maßnahmen-Baustein (Plan 2026-11-19-sortierung, Aufgabe 5): Störung, Ticket, Aufgabe,
// Wartung zeigen ihre Maßnahmen als einen gemeinsamen Baustein mit Sortier-Leiste.

// actModules: Modul -> Tabelle, Maßnahmen-Tabelle, Fremdschlüssel.
var actModules = []struct{ module, table, actTable, fk string }{
	{"fault", "faults", "fault_actions", "fault_id"},
	{"ticket", "tickets", "ticket_actions", "ticket_id"},
	{"task", "tasks", "task_actions", "task_id"},
	{"maintenance_task", "maintenance_tasks", "maintenance_task_actions", "task_id"},
}

// newActEnv: wie die Historien-Umgebung, zusätzlich mit den vier Diensten der Module.
func newActEnv(t *testing.T) *histEnv {
	t.Helper()
	e := newHistEnv(t)
	e.h.faults = faults.NewService(faults.NewRepository(e.pool), nil)
	e.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	e.h.tasks = tasks.NewService(tasks.NewRepository(e.pool))
	e.h.maint = maintenance.NewService(maintenance.NewRepository(e.pool))
	return e
}

// actRecord legt einen Datensatz des Moduls an (Anlage und Status optional) und räumt ihn auf.
func (e *histEnv) actRecord(module string, by *users.User, infra, status string) string {
	e.t.Helper()
	var table, q string
	switch module {
	case "fault":
		table, q = "faults", `INSERT INTO faults (title, infrastructure_id, created_by) VALUES ('A-Störung', NULLIF($1,'')::uuid, $2::uuid) RETURNING id::text`
		status = ""
	case "ticket":
		table, q = "tickets", `INSERT INTO tickets (title, infrastructure_id, created_by, status) VALUES ('A-Ticket', NULLIF($1,'')::uuid, $2::uuid, COALESCE(NULLIF($3,''),'open')::ticket_status) RETURNING id::text`
	case "task":
		table, q = "tasks", `INSERT INTO tasks (title, description, priority, infrastructure_id, created_by, status) VALUES ('A-Aufgabe', 'x', 'medium', NULLIF($1,'')::uuid, $2::uuid, COALESCE(NULLIF($3,''),'open')) RETURNING id::text`
	case "maintenance_task":
		table, q = "maintenance_tasks", `INSERT INTO maintenance_tasks (title, type, infrastructure_id, priority, status, due_date, created_by)
			VALUES ('A-Wartung', 'preventive', $1::uuid, 'medium', 'open', NOW(), $2::uuid) RETURNING id::text`
		if infra == "" {
			infra = e.infra("Wartungsanlage", "", "")
		}
		status = ""
	}
	args := []any{infra, by.ID}
	if strings.Contains(q, "$3") {
		args = append(args, status)
	}
	return e.row(table, q, args...)
}

// actSeed trägt eine Maßnahme ein.
func (e *histEnv) actSeed(module, rec, text string, by *users.User, at time.Time) {
	e.t.Helper()
	for _, m := range actModules {
		if m.module == module {
			e.exec(fmt.Sprintf(`INSERT INTO %s (%s, description, created_by, created_at) VALUES ($1::uuid, $2, $3::uuid, $4)`, m.actTable, m.fk), rec, text, by.ID, at)
			return
		}
	}
	e.t.Fatalf("unbekanntes Modul %s", module)
}

func (e *histEnv) actCount(module, rec string) int {
	e.t.Helper()
	for _, m := range actModules {
		if m.module == module {
			var n int
			if err := e.pool.QueryRow(e.ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = $1::uuid`, m.actTable, m.fk), rec).Scan(&n); err != nil {
				e.t.Fatal(err)
			}
			return n
		}
	}
	e.t.Fatalf("unbekanntes Modul %s", module)
	return 0
}

// actRouter: die Routen des Bausteins hinter der Abteilungssperre der echten Routen.
func (e *histEnv) actRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(e.h.DepartmentScopeMiddleware)
	r.Get("/records/actions/{module}/{id}", e.h.RecordActionsWeb)
	r.Post("/records/actions/{module}/{id}", e.h.RecordActionsAddWeb)
	return r
}

func (e *histEnv) actDo(u *users.User, method, target string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if method == "POST" {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	w := httptest.NewRecorder()
	e.actRouter().ServeHTTP(w, req)
	return w
}

var actTitleRe = regexp.MustCompile(`<div class="li-title">([^<]*)</div>`)

// actTitles: die Titel in der Reihenfolge des Erscheinens (Gruppenköpfe und Einträge).
func actTitles(body string) []string {
	var out []string
	for _, m := range actTitleRe.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func TestActionsBlockSorts(t *testing.T) {
	e := newActEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, m := range actModules {
		rec := e.actRecord(m.module, anna, "", "")
		e.actSeed(m.module, rec, "filter  gereinigt!", bert, base)
		e.actSeed(m.module, rec, "Lager gefettet", anna, base.AddDate(0, 0, 1))
		e.actSeed(m.module, rec, "Filter gereinigt", anna, base.AddDate(0, 0, 2))
		e.actSeed(m.module, rec, "Sicherung getauscht", bert, base.AddDate(0, 0, 3))
		url := "/records/actions/" + m.module + "/" + rec
		for _, c := range []struct{ mode, want string }{
			{"old", "filter  gereinigt!|Lager gefettet|Filter gereinigt|Sicherung getauscht"},
			{"new", "Sicherung getauscht|Filter gereinigt|Lager gefettet|filter  gereinigt!"},
			// Häufigste: die Gruppe (2×, Schreibweise egal) trägt die Schreibweise des jüngsten Eintrags
			{"freq", "2× Filter gereinigt|Filter gereinigt|filter  gereinigt!|Sicherung getauscht|Lager gefettet"},
			{"same", "2× Filter gereinigt|Filter gereinigt|filter  gereinigt!|Lager gefettet|Sicherung getauscht"},
			{"person", "Filter gereinigt|Lager gefettet|Sicherung getauscht|filter  gereinigt!"},
		} {
			w := e.actDo(anna, "GET", url+"?ls_actions="+c.mode, nil)
			if w.Code != 200 {
				t.Fatalf("%s %s: %d %s", m.module, c.mode, w.Code, w.Body.String())
			}
			if got := strings.Join(actTitles(w.Body.String()), "|"); got != c.want {
				t.Errorf("%s %s: Reihenfolge %q, erwartet %q", m.module, c.mode, got, c.want)
			}
			if !strings.Contains(w.Body.String(), `id="rec-actions"`) || !strings.Contains(w.Body.String(), `hx-target="#rec-actions"`) {
				t.Errorf("%s %s: Karte oder Leiste fehlt", m.module, c.mode)
			}
		}
		freq := e.actDo(anna, "GET", url+"?ls_actions=freq", nil).Body.String()
		if !strings.Contains(freq, `<details class="rh-group">`) || !strings.Contains(freq, "zuletzt 03.03.2026") || strings.Contains(freq, "1× ") {
			t.Errorf("%s: Gruppe falsch: %s", m.module, freq)
		}
		// Standard: älteste zuerst (Tabelle „Listenarten“); eine unbekannte Art ändert daran nichts
		def := e.actDo(bert, "GET", url+"?ls_actions=;D"+"ROP", nil)
		if got := strings.Join(actTitles(def.Body.String()), "|"); got != "filter  gereinigt!|Lager gefettet|Filter gereinigt|Sicherung getauscht" {
			t.Errorf("%s: Standard %q", m.module, got)
		}
	}
}

// Leere Texte, Satzzeichen und Sonderzeichen: eigene Gruppe, nichts unmaskiert.
func TestActionsBlockEscapes(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Esra", "Escape")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rec := e.actRecord("fault", u, "", "")
	e.actSeed("fault", rec, `<script>alert(1)</script>`, u, base)
	e.actSeed("fault", rec, `<script>alert(1)</script>`, u, base.AddDate(0, 0, 1))
	e.actSeed("fault", rec, `!!!`, u, base.AddDate(0, 0, 2))
	e.actSeed("fault", rec, `...`, u, base.AddDate(0, 0, 3))
	w := e.actDo(u, "GET", "/records/actions/fault/"+rec+"?ls_actions=freq", nil)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("Ausgabe nicht maskiert: %d %s", w.Code, body)
	}
	if !strings.Contains(body, "2× (ohne Text)") || !strings.Contains(body, "2× &lt;script&gt;") {
		t.Errorf("Gruppen fehlen: %s", body)
	}
	// auch ein frisch eingetragener Text kommt maskiert zurück
	e.grant(u, "faults.edit", "")
	p := e.actDo(u, "POST", "/records/actions/fault/"+rec, url.Values{"description": {`<img src=x onerror=alert(2)>`}})
	if p.Code != 200 || strings.Contains(p.Body.String(), "<img src=x") || !strings.Contains(p.Body.String(), "&lt;img src=x") {
		t.Errorf("POST nicht maskiert: %d %s", p.Code, p.Body.String())
	}
}

func TestActionsBlockAdd(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Axel", "Anleger")
	for _, perm := range []string{"faults.edit", "tickets.edit", "tasks.edit"} {
		e.grant(u, perm, "")
	}
	for _, m := range actModules[:3] { // Wartung trägt beim Abschließen ein
		rec := e.actRecord(m.module, u, "", "")
		w := e.actDo(u, "POST", "/records/actions/"+m.module+"/"+rec+"?ls_actions=new", url.Values{"description": {"  Filter getauscht  "}})
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", m.module, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `id="rec-actions"`) || strings.Join(actTitles(w.Body.String()), "|") != "Filter getauscht" {
			t.Errorf("%s: der Baustein kommt nicht neu zurück: %s", m.module, w.Body.String())
		}
		var by, text string
		if err := e.pool.QueryRow(e.ctx, fmt.Sprintf(`SELECT created_by::text, description FROM %s WHERE %s = $1::uuid`, m.actTable, m.fk), rec).Scan(&by, &text); err != nil {
			t.Fatalf("%s: %v", m.module, err)
		}
		if by != u.ID || text != "Filter getauscht" {
			t.Errorf("%s: gespeichert %q von %q", m.module, text, by)
		}
		// leer, nur Leerzeichen, zu lang: 400 mit Klartext, nichts gespeichert
		for name, v := range map[string]string{"leer": "", "Leerzeichen": "   ", "zu lang": strings.Repeat("x", 4001)} {
			b := e.actDo(u, "POST", "/records/actions/"+m.module+"/"+rec, url.Values{"description": {v}})
			if b.Code != http.StatusBadRequest || strings.TrimSpace(b.Body.String()) == "" {
				t.Errorf("%s %s: %d %q", m.module, name, b.Code, b.Body.String())
			}
		}
		if n := e.actCount(m.module, rec); n != 1 {
			t.Errorf("%s: %d Maßnahmen, erwartet 1", m.module, n)
		}
		// genau 4000 Zeichen gehen
		if b := e.actDo(u, "POST", "/records/actions/"+m.module+"/"+rec, url.Values{"description": {strings.Repeat("ä", 4000)}}); b.Code != 200 {
			t.Errorf("%s: 4000 Zeichen: %d", m.module, b.Code)
		}
	}
}

func TestActionsBlockPerms(t *testing.T) {
	e := newActEnv(t)
	none := e.user("Nora", "Nichts")
	a, b := e.dept("A"), e.dept("B")
	infraA, infraB := e.infra("AnlageA", "", a), e.infra("AnlageB", "", b)
	maker := e.user("Mia", "Macher")
	row := regexp.MustCompile(`<form[^>]*hx-post="/records/actions/`)
	// ohne Anmeldung nie Inhalt
	if w := e.actDo(&users.User{Role: "viewer"}, "GET", "/records/actions/fault/0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b", nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Anmeldung: %d", w.Code)
	}
	if w := e.actDo(none, "GET", "/records/actions/gibtsnicht/0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b", nil); w.Code != http.StatusNotFound {
		t.Errorf("unbekanntes Modul: %d", w.Code)
	}
	if w := e.actDo(none, "GET", "/records/actions/fault/0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b", nil); w.Code != http.StatusNotFound {
		t.Errorf("Datensatz fehlt: %d", w.Code)
	}
	for _, m := range actModules[:3] {
		rec := e.actRecord(m.module, maker, "", "")
		editPerm := map[string]string{"fault": "faults.edit", "ticket": "tickets.edit", "task": "tasks.edit"}[m.module]
		url := "/records/actions/" + m.module + "/" + rec
		// Lesen wie die Detailseite: jeder Angemeldete, aber ohne Schreibrecht keine Eingabezeile und POST 403
		w := e.actDo(none, "GET", url, nil)
		if w.Code != 200 || row.MatchString(w.Body.String()) {
			t.Errorf("%s ohne Schreibrecht: %d, Eingabezeile %v", m.module, w.Code, row.MatchString(w.Body.String()))
		}
		if p := e.actDo(none, "POST", url, map[string][]string{"description": {"x"}}); p.Code != http.StatusForbidden || e.actCount(m.module, rec) != 0 {
			t.Errorf("%s POST ohne Recht: %d", m.module, p.Code)
		}
		// mit Recht überall
		ok := e.user("Willi", "Writer"+m.module[:3])
		e.grant(ok, editPerm, "")
		if w := e.actDo(ok, "GET", url, nil); !row.MatchString(w.Body.String()) {
			t.Errorf("%s mit %s: Eingabezeile fehlt", m.module, editPerm)
		}
		// Recht nur in Abteilung B: für einen Datensatz in A weder Zeile noch POST, in B beides
		inB := e.user("Bea", "Nurb"+m.module[:3])
		e.grant(inB, editPerm, b)
		recA, recB := e.actRecord(m.module, maker, infraA, ""), e.actRecord(m.module, maker, infraB, "")
		if w := e.actDo(inB, "GET", "/records/actions/"+m.module+"/"+recA, nil); w.Code != 200 || row.MatchString(w.Body.String()) {
			t.Errorf("%s Datensatz in A, Recht in B: %d, Zeile %v", m.module, w.Code, row.MatchString(w.Body.String()))
		}
		if p := e.actDo(inB, "POST", "/records/actions/"+m.module+"/"+recA, map[string][]string{"description": {"x"}}); p.Code != http.StatusForbidden || e.actCount(m.module, recA) != 0 {
			t.Errorf("%s POST fremde Abteilung: %d", m.module, p.Code)
		}
		if w := e.actDo(inB, "GET", "/records/actions/"+m.module+"/"+recB, nil); !row.MatchString(w.Body.String()) {
			t.Errorf("%s Datensatz in B: Eingabezeile fehlt", m.module)
		}
		if p := e.actDo(inB, "POST", "/records/actions/"+m.module+"/"+recB, map[string][]string{"description": {"ok"}}); p.Code != 200 {
			t.Errorf("%s POST eigene Abteilung: %d", m.module, p.Code)
		}
		// Beteiligte (hier: der Ersteller) dürfen wie über die Schnittstelle auch ohne das Recht
		if w := e.actDo(maker, "GET", url, nil); !row.MatchString(w.Body.String()) {
			t.Errorf("%s: Beteiligter ohne Recht sieht keine Eingabezeile", m.module)
		}
		if p := e.actDo(maker, "POST", url, map[string][]string{"description": {"beteiligt"}}); p.Code != 200 {
			t.Errorf("%s POST Beteiligter: %d", m.module, p.Code)
		}
	}
	// Lesen in fremder Abteilung: die Sperre der Route (scopePaths) gilt auch für den Baustein
	role := "act" + e.sfx
	e.exec(`INSERT INTO roles (key, label, department_id) VALUES ($1, 'Abteilung A', $2::uuid)`, role, a)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
	inRole := e.user("Rosa", "Rolle")
	inRole.Role = users.Role(role)
	for _, m := range actModules {
		recA, recB := e.actRecord(m.module, maker, infraA, ""), e.actRecord(m.module, maker, infraB, "")
		if w := e.actDo(inRole, "GET", "/records/actions/"+m.module+"/"+recA, nil); w.Code != 200 {
			t.Errorf("%s eigene Abteilung lesen: %d", m.module, w.Code)
		}
		if w := e.actDo(inRole, "GET", "/records/actions/"+m.module+"/"+recB, nil); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "rec-actions") {
			t.Errorf("%s fremde Abteilung lesen: %d", m.module, w.Code)
		}
	}
}

// Eingabezeile nur, wo sie heute steht: Störung immer (bei Schreibrecht), ein gelöstes Ticket nicht,
// eine Aufgabe nur offen, Wartung nie – und der POST folgt derselben Regel.
func TestActionsBlockInputRules(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Ina", "Input")
	for _, p := range []string{"faults.edit", "tickets.edit", "tasks.edit", "maintenance.edit"} {
		e.grant(u, p, "")
	}
	row := regexp.MustCompile(`<form[^>]*hx-post="/records/actions/`)
	post := func(module, rec string) int {
		return e.actDo(u, "POST", "/records/actions/"+module+"/"+rec, map[string][]string{"description": {"x"}}).Code
	}
	check := func(label, module, rec string, wantRow bool, wantPost int) {
		t.Helper()
		w := e.actDo(u, "GET", "/records/actions/"+module+"/"+rec, nil)
		if w.Code != 200 || row.MatchString(w.Body.String()) != wantRow {
			t.Errorf("%s: Zeile %v, erwartet %v (%d)", label, row.MatchString(w.Body.String()), wantRow, w.Code)
		}
		if got := post(module, rec); got != wantPost {
			t.Errorf("%s: POST %d, erwartet %d", label, got, wantPost)
		}
	}
	check("Störung", "fault", e.actRecord("fault", u, "", ""), true, 200)
	check("Ticket offen", "ticket", e.actRecord("ticket", u, "", "in_progress"), true, 200)
	for _, st := range []string{"resolved", "closed"} {
		check("Ticket "+st, "ticket", e.actRecord("ticket", u, "", st), false, http.StatusConflict)
	}
	for _, st := range []string{"open", "in_progress", "pending"} {
		check("Aufgabe "+st, "task", e.actRecord("task", u, "", st), true, 200)
	}
	for _, st := range []string{"resolved", "closed"} {
		check("Aufgabe "+st, "task", e.actRecord("task", u, "", st), false, http.StatusConflict)
	}
	// Wartung: Baustein immer, nur lesend – auch bei vorhandenem Schreibrecht
	m := e.actRecord("maintenance_task", u, "", "")
	e.actSeed("maintenance_task", m, "Rundgang ohne Befund", u, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	w := e.actDo(u, "GET", "/records/actions/maintenance_task/"+m, nil)
	if w.Code != 200 || row.MatchString(w.Body.String()) || strings.Join(actTitles(w.Body.String()), "|") != "Rundgang ohne Befund" {
		t.Errorf("Wartung: %d %s", w.Code, w.Body.String())
	}
	if got := post("maintenance_task", m); got != http.StatusConflict || e.actCount("maintenance_task", m) != 1 {
		t.Errorf("Wartung POST: %d", got)
	}
}

// Abgewiesene Eingaben: über htmx 200 mit dem Klartext im Baustein (nichts gespeichert), sonst der
// echte Status; nach dem Eintragen behält das Feld den Fokus.
func TestActionsBlockRefuseInline(t *testing.T) {
	e := newActEnv(t)
	maker, none := e.user("Mia", "Macher"), e.user("Nora", "Nichts")
	writer := e.user("Willi", "Writer")
	e.grant(writer, "faults.edit", "")
	e.grant(writer, "tickets.edit", "")
	hx := []string{"HX-Request", "true"}
	fault := e.actRecord("fault", maker, "", "")
	closed := e.actRecord("ticket", maker, "", "closed")
	check := func(label string, u *users.User, module, rec string, form url.Values, wantCode int, wantText string) {
		t.Helper()
		target := "/records/actions/" + module + "/" + rec
		plain := e.actDo(u, "POST", target, form)
		if plain.Code != wantCode || strings.TrimSpace(plain.Body.String()) != wantText {
			t.Errorf("%s ohne htmx: %d %q, erwartet %d %q", label, plain.Code, plain.Body.String(), wantCode, wantText)
		}
		h := e.actDo(u, "POST", target, form, hx...)
		if h.Code != 200 || !strings.Contains(h.Body.String(), `id="rec-actions"`) || !strings.Contains(h.Body.String(), `role="alert"`) || !strings.Contains(h.Body.String(), wantText) {
			t.Errorf("%s mit htmx: %d %s", label, h.Code, h.Body.String())
		}
		if n := e.actCount(module, rec); n != 0 {
			t.Errorf("%s: %d Maßnahmen gespeichert", label, n)
		}
	}
	check("ohne Recht", none, "fault", fault, url.Values{"description": {"x"}}, http.StatusForbidden, "Dafür fehlt die Berechtigung.")
	check("gelöstes Ticket", writer, "ticket", closed, url.Values{"description": {"x"}}, http.StatusConflict, "Hier können keine Maßnahmen eingetragen werden.")
	check("leer", writer, "fault", fault, url.Values{"description": {"  "}}, http.StatusBadRequest, "Bitte eine Maßnahme eingeben.")
	long := strings.Repeat("x", 4001)
	check("zu lang", writer, "fault", fault, url.Values{"description": {long}}, http.StatusBadRequest, "Die Maßnahme darf höchstens 4000 Zeichen haben.")
	if h := e.actDo(writer, "POST", "/records/actions/fault/"+fault, url.Values{"description": {long}}, hx...); !strings.Contains(h.Body.String(), `value="`+long+`"`) {
		t.Error("zu lang: der Text bleibt nicht im Feld")
	}
	// Erfolg: Fokus bleibt im Feld, ein reines Laden setzt ihn nicht
	ok := e.actDo(writer, "POST", "/records/actions/fault/"+fault, url.Values{"description": {"Filter getauscht"}}, hx...)
	if ok.Code != 200 || !strings.Contains(ok.Body.String(), "autofocus") || strings.Contains(ok.Body.String(), `role="alert"`) {
		t.Errorf("Erfolg: %d %s", ok.Code, ok.Body.String())
	}
	if g := e.actDo(writer, "GET", "/records/actions/fault/"+fault, nil); strings.Contains(g.Body.String(), "autofocus") {
		t.Error("Laden setzt den Fokus")
	}
}

// Die Seiten laden den Baustein, die alten Seiten-Skripte sind weg.
func TestActionsSlotOnPages(t *testing.T) {
	for page, module := range map[string]string{"fault": "fault", "ticket": "ticket", "task": "task", "maintenance": "maintenance_task"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", page+"_detail.gohtml"))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `template "record-actions-slot" (dict "Module" "`+module+`"`) {
			t.Errorf("%s_detail: record-actions-slot fehlt", page)
		}
		for _, old := range []string{"loadFaultActions", "addFaultAction", "loadTicketActions", "addTicketAction", "loadTaskActions", "addTaskAction", "-actions-list", "-new-action"} {
			if strings.Contains(s, old) {
				t.Errorf("%s_detail: %q ist noch da", page, old)
			}
		}
	}
	// nichts sonst ruft die alten Skripte
	err := filepath.Walk(filepath.Join("..", "..", "web"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		for _, old := range []string{"loadFaultActions", "addFaultAction", "loadTicketActions", "addTicketAction", "loadTaskActions", "addTaskAction"} {
			if strings.Contains(string(b), old) {
				t.Errorf("%s ruft %s noch auf", p, old)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ohne Recht: leiser Hinweis statt der globalen Fehlermeldung
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&sb, "record-actions-slot", map[string]any{"Module": "fault", "ID": "x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `hx-get="/records/actions/fault/x"`) || !strings.Contains(sb.String(), "data-quiet-denied=") {
		t.Errorf("Platzhalter: %s", sb.String())
	}
}
