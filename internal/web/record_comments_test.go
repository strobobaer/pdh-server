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

	"pdh/internal/core/listsort"
	"pdh/internal/core/users"
)

// Kommentar-Baustein und KVP-Maßnahmen (Plan 2026-11-19-sortierung, Aufgabe 6).

// commModules: Modul -> Tabelle, Kommentar-Tabelle, Fremdschlüssel.
var commModules = []struct{ module, table, commTable, fk string }{
	{"ticket", "tickets", "ticket_comments", "ticket_id"},
	{"kvp", "kvp_ideas", "kvp_comments", "idea_id"},
}

// commRecord legt einen Datensatz des Moduls an (Anlage optional) und räumt ihn auf.
func (e *histEnv) commRecord(module string, by *users.User, infra string) string {
	e.t.Helper()
	if module == "kvp" {
		return e.row("kvp_ideas", `INSERT INTO kvp_ideas (title, created_by) VALUES ('C-Idee', $1::uuid) RETURNING id::text`, by.ID)
	}
	return e.actRecord("ticket", by, infra, "")
}

func (e *histEnv) commSeed(module, rec, text string, by *users.User, at time.Time) {
	e.t.Helper()
	for _, m := range commModules {
		if m.module == module {
			e.exec(fmt.Sprintf(`INSERT INTO %s (%s, user_id, text, created_at) VALUES ($1::uuid, $2::uuid, $3, $4)`, m.commTable, m.fk), rec, by.ID, text, at)
			return
		}
	}
	e.t.Fatalf("unbekanntes Modul %s", module)
}

func (e *histEnv) commCount(module, rec string) int {
	e.t.Helper()
	for _, m := range commModules {
		if m.module == module {
			var n int
			if err := e.pool.QueryRow(e.ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = $1::uuid`, m.commTable, m.fk), rec).Scan(&n); err != nil {
				e.t.Fatal(err)
			}
			return n
		}
	}
	e.t.Fatalf("unbekanntes Modul %s", module)
	return 0
}

// commRouter: Baustein und alter Ticket-Weg hinter der Abteilungssperre der echten Routen.
func (e *histEnv) commRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(e.h.DepartmentScopeMiddleware)
	r.Get("/records/comments/{module}/{id}", e.h.RecordCommentsWeb)
	r.Post("/records/comments/{module}/{id}", e.h.RecordCommentsAddWeb)
	r.Post("/tickets/{id}/comment", e.h.TicketCommentLegacy)
	return r
}

func (e *histEnv) commDo(u *users.User, method, target string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
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
	e.commRouter().ServeHTTP(w, req)
	return w
}

var commTextRe = regexp.MustCompile(`<div class="rc-text"[^>]*>([^<]*)</div>`)

func commTexts(body string) []string {
	var out []string
	for _, m := range commTextRe.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func TestCommentsBlockSorts(t *testing.T) {
	e := newActEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, m := range commModules {
		rec := e.commRecord(m.module, anna, "")
		e.commSeed(m.module, rec, "filter  gereinigt!", bert, base)
		e.commSeed(m.module, rec, "Lager gefettet", anna, base.AddDate(0, 0, 1))
		e.commSeed(m.module, rec, "Filter gereinigt", anna, base.AddDate(0, 0, 2))
		e.commSeed(m.module, rec, "Sicherung getauscht", bert, base.AddDate(0, 0, 3))
		url := "/records/comments/" + m.module + "/" + rec
		for _, c := range []struct{ mode, want string }{
			{"old", "filter  gereinigt!|Lager gefettet|Filter gereinigt|Sicherung getauscht"},
			{"new", "Sicherung getauscht|Filter gereinigt|Lager gefettet|filter  gereinigt!"},
			{"freq", "2× Filter gereinigt|Filter gereinigt|filter  gereinigt!|Sicherung getauscht|Lager gefettet"},
			{"same", "2× Filter gereinigt|Filter gereinigt|filter  gereinigt!|Lager gefettet|Sicherung getauscht"},
			{"person", "Filter gereinigt|Lager gefettet|Sicherung getauscht|filter  gereinigt!"},
		} {
			w := e.commDo(anna, "GET", url+"?ls_comments="+c.mode, nil)
			if w.Code != 200 {
				t.Fatalf("%s %s: %d %s", m.module, c.mode, w.Code, w.Body.String())
			}
			if got := strings.Join(commTexts(w.Body.String()), "|"); got != c.want {
				t.Errorf("%s %s: Reihenfolge %q, erwartet %q", m.module, c.mode, got, c.want)
			}
			if !strings.Contains(w.Body.String(), `id="rec-comments"`) || !strings.Contains(w.Body.String(), `hx-target="#rec-comments"`) {
				t.Errorf("%s %s: Karte oder Leiste fehlt", m.module, c.mode)
			}
		}
		freq := e.commDo(anna, "GET", url+"?ls_comments=freq", nil).Body.String()
		if !strings.Contains(freq, `<details class="rh-group">`) || !strings.Contains(freq, "zuletzt 03.03.2026") || strings.Contains(freq, "1× ") {
			t.Errorf("%s: Gruppe falsch: %s", m.module, freq)
		}
		// Standard: älteste zuerst; eine unbekannte Art ändert daran nichts
		def := e.commDo(bert, "GET", url+"?ls_comments=;D"+"ROP", nil)
		if got := strings.Join(commTexts(def.Body.String()), "|"); got != "filter  gereinigt!|Lager gefettet|Filter gereinigt|Sicherung getauscht" {
			t.Errorf("%s: Standard %q", m.module, got)
		}
		// „Nach Termin“ gibt es bei Kommentaren nicht
		due := e.commDo(bert, "GET", url+"?ls_comments=due", nil)
		if got := strings.Join(commTexts(due.Body.String()), "|"); got != "filter  gereinigt!|Lager gefettet|Filter gereinigt|Sicherung getauscht" {
			t.Errorf("%s: unerlaubte Art due: %q", m.module, got)
		}
	}
}

func TestCommentsBlockEscapes(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Esra", "Escape")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rec := e.commRecord("kvp", u, "")
	e.commSeed("kvp", rec, `<script>alert(1)</script>`, u, base)
	e.commSeed("kvp", rec, `<script>alert(1)</script>`, u, base.AddDate(0, 0, 1))
	e.commSeed("kvp", rec, `!!!`, u, base.AddDate(0, 0, 2))
	e.commSeed("kvp", rec, `...`, u, base.AddDate(0, 0, 3))
	w := e.commDo(u, "GET", "/records/comments/kvp/"+rec+"?ls_comments=freq", nil)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("Ausgabe nicht maskiert: %d %s", w.Code, body)
	}
	if !strings.Contains(body, "2× (ohne Text)") || !strings.Contains(body, "2× &lt;script&gt;") {
		t.Errorf("Gruppen fehlen: %s", body)
	}
	p := e.commDo(u, "POST", "/records/comments/kvp/"+rec, url.Values{"text": {`<img src=x onerror=alert(2)>`}})
	if p.Code != 200 || strings.Contains(p.Body.String(), "<img src=x") || !strings.Contains(p.Body.String(), "&lt;img src=x") {
		t.Errorf("POST nicht maskiert: %d %s", p.Code, p.Body.String())
	}
}

func TestCommentsBlockAdd(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Carla", "Kommentar")
	for _, m := range commModules {
		rec := e.commRecord(m.module, u, "")
		target := "/records/comments/" + m.module + "/" + rec
		w := e.commDo(u, "POST", target+"?ls_comments=new", url.Values{"text": {"  Teil bestellt  "}})
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", m.module, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `id="rec-comments"`) || strings.Join(commTexts(w.Body.String()), "|") != "Teil bestellt" {
			t.Errorf("%s: der Baustein kommt nicht neu zurück: %s", m.module, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "autofocus") {
			t.Errorf("%s: das Eingabefeld behält den Fokus nicht", m.module)
		}
		var by, text string
		if err := e.pool.QueryRow(e.ctx, fmt.Sprintf(`SELECT user_id::text, text FROM %s WHERE %s = $1::uuid`, m.commTable, m.fk), rec).Scan(&by, &text); err != nil {
			t.Fatalf("%s: %v", m.module, err)
		}
		if by != u.ID || text != "Teil bestellt" {
			t.Errorf("%s: gespeichert %q von %q", m.module, text, by)
		}
		// leer, nur Leerzeichen, zu lang: 400 mit Klartext, nichts gespeichert
		for name, v := range map[string]string{"leer": "", "Leerzeichen": "   ", "zu lang": strings.Repeat("x", 4001)} {
			b := e.commDo(u, "POST", target, url.Values{"text": {v}})
			if b.Code != http.StatusBadRequest || strings.TrimSpace(b.Body.String()) == "" || strings.Contains(b.Body.String(), "<") {
				t.Errorf("%s %s: %d %q", m.module, name, b.Code, b.Body.String())
			}
			// über htmx: der Klartext steht im Baustein (sonst nur „HTTP 400“)
			h := e.commDo(u, "POST", target, url.Values{"text": {v}}, "HX-Request", "true")
			if h.Code != 200 || !strings.Contains(h.Body.String(), `id="rec-comments"`) || !strings.Contains(h.Body.String(), `role="alert"`) {
				t.Errorf("%s %s htmx: %d %s", m.module, name, h.Code, h.Body.String())
			}
		}
		if n := e.commCount(m.module, rec); n != 1 {
			t.Errorf("%s: %d Kommentare, erwartet 1", m.module, n)
		}
		// genau 4000 Zeichen gehen
		if b := e.commDo(u, "POST", target, url.Values{"text": {strings.Repeat("ä", 4000)}}); b.Code != 200 {
			t.Errorf("%s: 4000 Zeichen: %d", m.module, b.Code)
		}
	}
}

func TestCommentsBlockPerms(t *testing.T) {
	e := newActEnv(t)
	none := e.user("Nora", "Nichts")
	maker := e.user("Mia", "Macher")
	a, b := e.dept("A"), e.dept("B")
	infraA, infraB := e.infra("AnlageA", "", a), e.infra("AnlageB", "", b)
	const missing = "0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b"
	if w := e.commDo(&users.User{Role: "viewer"}, "GET", "/records/comments/ticket/"+missing, nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Anmeldung lesen: %d", w.Code)
	}
	if w := e.commDo(&users.User{Role: "viewer"}, "POST", "/records/comments/ticket/"+missing, url.Values{"text": {"x"}}); w.Code != http.StatusForbidden {
		t.Errorf("ohne Anmeldung schreiben: %d", w.Code)
	}
	for _, c := range []string{"/records/comments/fault/" + missing, "/records/comments/gibtsnicht/" + missing,
		"/records/comments/ticket/" + missing, "/records/comments/kvp/" + missing, "/records/comments/kvp/kein-uuid"} {
		if w := e.commDo(none, "GET", c, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, erwartet 404", c, w.Code)
		}
		if w := e.commDo(none, "POST", c, url.Values{"text": {"x"}}); w.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d, erwartet 404", c, w.Code)
		}
	}
	// KVP: wie die Detailseite – jeder Angemeldete liest und kommentiert, auch ohne jedes Recht
	idea := e.commRecord("kvp", maker, "")
	if w := e.commDo(none, "GET", "/records/comments/kvp/"+idea, nil); w.Code != 200 {
		t.Errorf("KVP lesen: %d", w.Code)
	}
	if p := e.commDo(none, "POST", "/records/comments/kvp/"+idea, url.Values{"text": {"Frage"}}); p.Code != 200 || e.commCount("kvp", idea) != 1 {
		t.Errorf("KVP schreiben: %d", p.Code)
	}
	// Ticket ohne Recht: wie die Detailseite jeder Angemeldete (ohne Abteilungssperre)
	tk := e.commRecord("ticket", maker, "")
	if w := e.commDo(none, "GET", "/records/comments/ticket/"+tk, nil); w.Code != 200 {
		t.Errorf("Ticket lesen: %d", w.Code)
	}
	if p := e.commDo(none, "POST", "/records/comments/ticket/"+tk, url.Values{"text": {"Frage"}}); p.Code != 200 || e.commCount("ticket", tk) != 1 {
		t.Errorf("Ticket schreiben: %d", p.Code)
	}
	// Abteilung: die Sperre der Route (scopePaths) gilt für Lesen und Schreiben
	role := "com" + e.sfx
	e.exec(`INSERT INTO roles (key, label, department_id) VALUES ($1, 'Abteilung A', $2::uuid)`, role, a)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
	inRole := e.user("Rosa", "Rolle")
	inRole.Role = users.Role(role)
	recA, recB := e.commRecord("ticket", maker, infraA), e.commRecord("ticket", maker, infraB)
	e.commSeed("ticket", recB, "geheim", maker, time.Now())
	if w := e.commDo(inRole, "GET", "/records/comments/ticket/"+recA, nil); w.Code != 200 {
		t.Errorf("eigene Abteilung lesen: %d", w.Code)
	}
	if p := e.commDo(inRole, "POST", "/records/comments/ticket/"+recA, url.Values{"text": {"ok"}}); p.Code != 200 {
		t.Errorf("eigene Abteilung schreiben: %d", p.Code)
	}
	if w := e.commDo(inRole, "GET", "/records/comments/ticket/"+recB, nil); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "geheim") {
		t.Errorf("fremde Abteilung lesen: %d", w.Code)
	}
	if p := e.commDo(inRole, "POST", "/records/comments/ticket/"+recB, url.Values{"text": {"x"}}); p.Code != http.StatusForbidden || e.commCount("ticket", recB) != 1 {
		t.Errorf("fremde Abteilung schreiben: %d", p.Code)
	}
}

func TestCommentInitialUmlaut(t *testing.T) {
	for in, want := range map[string]string{"Özlem Yilmaz": "Ö", "özlem": "Ö", "Éva": "É", "  Åke": "Å", "": "?", "   ": "?"} {
		if got := commentInitial(in); got != want {
			t.Errorf("commentInitial(%q) = %q, erwartet %q", in, got, want)
		}
	}
	e := newActEnv(t)
	u := e.user("Özlem", "Yilmaz")
	tk := e.commRecord("ticket", u, "")
	e.commSeed("ticket", tk, "Hallo", u, time.Now())
	w := e.commDo(u, "GET", "/records/comments/ticket/"+tk, nil)
	if !strings.Contains(w.Body.String(), ">Ö</div>") || strings.Contains(w.Body.String(), "\xc3</div>") {
		t.Errorf("Anfangsbuchstabe: %s", w.Body.String())
	}
}

// Der alte Weg /tickets/{id}/comment bleibt: derselbe Speicherort, dieselben Regeln.
func TestTicketCommentOldRoute(t *testing.T) {
	e := newActEnv(t)
	u := e.user("Olga", "Alt")
	tk := e.commRecord("ticket", u, "")
	w := e.commDo(u, "POST", "/tickets/"+tk+"/comment", url.Values{"text": {"über den alten Weg"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "über den alten Weg") || e.commCount("ticket", tk) != 1 {
		t.Errorf("alter Weg: %d %s", w.Code, w.Body.String())
	}
	if w := e.commDo(u, "POST", "/tickets/"+tk+"/comment", url.Values{"text": {" "}}); w.Code != http.StatusBadRequest || e.commCount("ticket", tk) != 1 {
		t.Errorf("alter Weg leer: %d", w.Code)
	}
}

// Die Seiten laden den Baustein; die alten Eingabefelder sind weg.
func TestCommentsSlotOnPages(t *testing.T) {
	for page, module := range map[string]string{"ticket": "ticket", "kvp": "kvp"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", page+"_detail.gohtml"))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `template "record-comments-slot" (dict "Module" "`+module+`"`) {
			t.Errorf("%s_detail: record-comments-slot fehlt", page)
		}
		if strings.Contains(s, "comments-list") || strings.Contains(s, "/comment\"") {
			t.Errorf("%s_detail: altes Kommentarformular ist noch da", page)
		}
	}
}

// ── KVP-Maßnahmen ──

func kvpActTitles(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<div class="kvp-act-title">([^<]*)</div>`).FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func TestKVPActionsSort(t *testing.T) {
	e := newActEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	idea := e.commRecord("kvp", anna, "")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seed := func(text string, resp *users.User, due string, at time.Time) {
		e.exec(`INSERT INTO kvp_actions (idea_id, description, responsible_id, due_date, created_by, created_at)
			VALUES ($1::uuid, $2, $3::uuid, NULLIF($4,'')::date, $3::uuid, $5)`, idea, text, resp.ID, due, at)
	}
	seed("Filter reinigen", anna, "2026-04-05", base)
	seed("Lager fetten", bert, "2026-04-01", base.AddDate(0, 0, 1))
	seed("filter  reinigen!", anna, "", base.AddDate(0, 0, 2))
	seed("Sicherung", bert, "2026-04-03", base.AddDate(0, 0, 3))

	list := e.h.kvpActions(e.ctx, idea, false)
	if len(list) != 4 {
		t.Fatalf("%d Maßnahmen geladen", len(list))
	}
	for _, a := range list {
		if a.CreatedAt.IsZero() || a.CreatedByName == "" {
			t.Errorf("Erstellung fehlt: %+v", a)
		}
	}
	tmpl := loadTestTemplates(t)
	k := &kvpIdea{ID: idea, Number: 1, Title: "Regal", Status: "do", Category: "safety", CreatedAt: time.Now()}
	for _, c := range []struct{ mode, want string }{
		{"due", "Lager fetten|Sicherung|Filter reinigen|filter  reinigen!"},
		{"new", "Sicherung|filter  reinigen!|Lager fetten|Filter reinigen"},
		{"old", "Filter reinigen|Lager fetten|filter  reinigen!|Sicherung"},
		{"freq", "2× filter  reinigen!|filter  reinigen!|Filter reinigen|Sicherung|Lager fetten"},
		{"same", "2× filter  reinigen!|filter  reinigen!|Filter reinigen|Lager fetten|Sicherung"},
		{"person", "filter  reinigen!|Filter reinigen|Sicherung|Lager fetten"},
	} {
		req := httptest.NewRequest("GET", "/kvp/"+idea+"?tab=pdca&ls_kvp_actions="+c.mode, nil)
		mode := e.h.sortMode(req, "kvp_actions")
		if string(mode) != c.mode {
			t.Fatalf("Art %s nicht erkannt: %s", c.mode, mode)
		}
		d := KVPDetailData{Idea: k, CanWork: true, Scores: []int{1, 2, 3, 4, 5}, Actions: list,
			ActionEntries: arrangeKVPActions(list, mode),
			ActionBar:     e.h.sortBar(req, "kvp_actions", mode, "/kvp/"+idea+"?tab=pdca", "")}
		out := renderPage(t, tmpl, "kvp_detail", d)
		if got := strings.Join(kvpActTitles(out), "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		// Abhaken und Löschen bleiben in jeder Zeile, auch in aufgeklappten Gruppen
		for _, a := range list {
			for _, f := range []string{`action="/kvp/` + idea + `/actions/` + a.ID + `/toggle"`, `action="/kvp/` + idea + `/actions/` + a.ID + `/delete"`} {
				if strings.Count(out, f) != 1 {
					t.Errorf("%s: %s kommt %d-mal vor", c.mode, f, strings.Count(out, f))
				}
			}
		}
		if !strings.Contains(out, `class="sortbar"`) || !strings.Contains(out, "ls_kvp_actions="+c.mode) && c.mode != "due" {
			t.Errorf("%s: Leiste fehlt oder verliert die Art", c.mode)
		}
	}
	// Abfrageparameter bleiben beim Umschalten (Seitenmodus)
	req := httptest.NewRequest("GET", "/kvp/"+idea+"?tab=pdca", nil)
	if h := e.h.sortBar(req, "kvp_actions", listsort.Due, "/kvp/"+idea+"?tab=pdca", "").Href(listsort.Newest); !strings.Contains(h, "tab=pdca") || !strings.Contains(h, "ls_kvp_actions=new") {
		t.Errorf("Link: %s", h)
	}
	// Assistent, Abhak-Ansicht und A3-Druck bleiben nach Termin: kvpActions ändert seine Reihenfolge nicht
	if got := fmt.Sprint(list[0].Description, "|", list[3].Description); got != "Lager fetten|filter  reinigen!" {
		t.Errorf("kvpActions nicht mehr nach Termin: %s", got)
	}
}
