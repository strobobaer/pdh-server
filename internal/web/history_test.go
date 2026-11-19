package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

// histEnv: Datenbank, Rechtedienst und ein Handler mit Vorlagen für die Historie.
type histEnv struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	rb   *rbac.Service
	h    *Handler
	sfx  string
	n    int
}

func newHistEnv(t *testing.T) *histEnv {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	e := &histEnv{t: t, ctx: ctx, pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.rb = rbac.NewService(rbac.NewRepository(pool))
	e.h = &Handler{db: pool, rbac: e.rb, tmpl: loadTestTemplates(t)}
	return e
}

func (e *histEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *histEnv) id(q string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.pool.QueryRow(e.ctx, q, args...).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// user legt einen Benutzer an; Rolle "nopermrole" gibt es nicht und bringt keine Rechte mit.
func (e *histEnv) user(first, last string) *users.User {
	e.t.Helper()
	e.n++
	name := fmt.Sprintf("%s%s%d", strings.ToLower(first), e.sfx, e.n)
	uid := e.id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', $2, $3, 'viewer', '', '') RETURNING id::text`, name, first, last)
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM record_history WHERE created_by = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM user_permissions WHERE user_id = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	return &users.User{ID: uid, Role: "nopermrole", FirstName: first, LastName: last}
}

func (e *histEnv) dept(name string) string {
	e.t.Helper()
	d := e.id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, name+e.sfx)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, d) })
	return d
}

// grant: Recht für den Benutzer, dept "" = überall.
func (e *histEnv) grant(u *users.User, perm, dept string) {
	e.t.Helper()
	e.exec(`INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
		SELECT $1::uuid, id, true, NULLIF($3,'')::uuid FROM permissions WHERE key = $2`, u.ID, perm, dept)
	if err := e.rb.Warm(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// order legt eine Bestellung (Modul purchase_order) an, optional in einer Abteilung.
func (e *histEnv) order(dept string) string {
	e.t.Helper()
	e.n++
	id := e.id(`INSERT INTO purchase_orders (number, department_id) VALUES ($1, NULLIF($2,'')::uuid) RETURNING id::text`, fmt.Sprintf("H-%s-%d", e.sfx, e.n), dept)
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM record_history WHERE ref_id = $1::uuid`, id)
		_, _ = e.pool.Exec(bg, `DELETE FROM purchase_orders WHERE id = $1::uuid`, id)
	})
	return id
}

// hist trägt einen Historieneintrag einer Bestellung ein.
func (e *histEnv) hist(ref, action, field, msg, newValue string, by *users.User, at time.Time) {
	e.t.Helper()
	e.histT("purchase_order", ref, action, field, msg, newValue, by, at)
}

func (e *histEnv) histT(refType, ref, action, field, msg, newValue string, by *users.User, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, message, created_by, created_at)
		VALUES ($6, $1::uuid, $2, NULLIF($3,''), $4, $5, $7::uuid, $8)`, ref, action, field, newValue, msg, refType, by.ID, at)
}

// row legt einen Datensatz an (Störung, Ticket, Ersatzteil) und räumt ihn samt Historie auf.
func (e *histEnv) row(table, insert string, args ...any) string {
	e.t.Helper()
	id := e.id(insert, args...)
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM record_history WHERE ref_id = $1::uuid`, id)
		_, _ = e.pool.Exec(bg, `DELETE FROM `+table+` WHERE id = $1::uuid`, id)
	})
	return id
}

// get ruft den Historienbaustein über den Router auf.
func (e *histEnv) get(u *users.User, url string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := chi.NewRouter()
	r.Get("/records/history/{module}/{id}", e.h.RecordHistoryWeb)
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var histValueRe = regexp.MustCompile(`w\d\d`)

// values: die Werte w01… in der Reihenfolge ihres Erscheinens.
func histValues(body string) []string { return histValueRe.FindAllString(body, -1) }

func TestHistoryFragmentSorts(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	e.grant(anna, "procurement.view", "")
	id := e.order("")
	base := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return base.AddDate(0, 0, n) }
	e.hist(id, "create", "", "Angelegt", "w01", bert, day(0))
	e.hist(id, "status", "status", "Status geändert", "w02", anna, day(1))
	e.hist(id, "status", "status", "Wartet bis 05.12.2026 (Leitstand)", "w03", bert, day(3))
	e.hist(id, "update", "title", "Titel geändert", "w04", anna, day(4))
	e.hist(id, "status", "status", "Status geändert", "w05", anna, day(5))

	url := "/records/history/purchase_order/" + id
	for _, c := range []struct{ mode, want string }{
		{"new", "w05 w04 w03 w02 w01"},
		{"old", "w01 w02 w03 w04 w05"},
		{"freq", "w05 w03 w02 w04 w01"},   // Status ×3 (jüngster zuerst), dann Einzelne neueste zuerst
		{"same", "w01 w04 w05 w03 w02"},   // nach angezeigter Beschriftung: Angelegt, Geändert · Titel, Status geändert
		{"person", "w05 w04 w02 w03 w01"}, // Anna, dann Bert; je neueste zuerst
		{"kind", "w01 w04 w05 w03 w02"},   // dieselbe Beschriftung wie in „Gleiches zusammen“
	} {
		w := e.get(anna, url+"?ls_history="+c.mode)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", c.mode, w.Code, w.Body.String())
		}
		if got := strings.Join(histValues(w.Body.String()), " "); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		if !strings.Contains(w.Body.String(), `id="rec-history"`) || !strings.Contains(w.Body.String(), `hx-target="#rec-history"`) {
			t.Errorf("%s: Karte oder Leiste fehlt", c.mode)
		}
	}
	freq := e.get(anna, url+"?ls_history=freq").Body.String()
	for _, want := range []string{"3× Status geändert", "<details", "zuletzt 06.03.2026"} {
		if !strings.Contains(freq, want) {
			t.Errorf("freq: %q fehlt in %s", want, freq)
		}
	}
	// die Gruppe trägt die feste Beschriftung, nie die Meldung eines Einzeleintrags
	sums := regexp.MustCompile(`(?s)<summary.*?</summary>`).FindAllString(freq, -1)
	if len(sums) != 1 || !strings.Contains(sums[0], "3× Status geändert") || strings.Contains(sums[0], "Wartet bis") {
		t.Errorf("Gruppenkopf: %q", sums)
	}
	if !strings.Contains(freq, "Wartet bis 05.12.2026 (Leitstand)") {
		t.Errorf("Einzeleintrag muss seine eigene Meldung zeigen")
	}
	same := e.get(anna, url+"?ls_history=same").Body.String()
	if a, b, c := strings.Index(same, "Angelegt"), strings.Index(same, "Titel geändert"), strings.Index(same, "3× Status geändert"); !(a >= 0 && a < b && b < c) {
		t.Errorf("Gleiches zusammen folgt nicht den angezeigten Beschriftungen: %d %d %d", a, b, c)
	}
	if strings.Contains(freq, "1× ") {
		t.Errorf("Gruppe mit einem Eintrag muss eine gewöhnliche Zeile sein: %s", freq)
	}
	// manipulierter Parameter zählt als nicht gesetzt: es gilt die gemerkte Wahl (hier „neueste“)
	e.get(anna, url+"?ls_history=new")
	bad := e.get(anna, url+"?ls_history=;D"+"ROP")
	if got := strings.Join(histValues(bad.Body.String()), " "); got != "w05 w04 w03 w02 w01" {
		t.Errorf("unbekannte Art: %q", got)
	}
}

func seedMany(e *histEnv, id string, u *users.User, n int, same bool) {
	base := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if same {
			at = base
		}
		e.hist(id, "status", "status", "Status geändert", fmt.Sprintf("w%02d", i), u, at)
	}
}

func TestHistoryMore(t *testing.T) {
	e := newHistEnv(t)
	u := e.user("Anna", "Alpha")
	e.grant(u, "procurement.view", "")
	id := e.order("")
	seedMany(e, id, u, 31, false)
	url := "/records/history/purchase_order/" + id
	w := e.get(u, url+"?ls_history=new")
	body := w.Body.String()
	if n := len(histValues(body)); n != 30 {
		t.Errorf("erst %d Zeilen, erwartet 30", n)
	}
	if !strings.Contains(body, "Weitere laden") || !strings.Contains(body, "n=60") {
		t.Errorf("Knopf mit n=60 fehlt: %s", body)
	}
	body = e.get(u, url+"?ls_history=new&n=60").Body.String()
	if n := len(histValues(body)); n != 31 {
		t.Errorf("mit n=60 %d Zeilen, erwartet 31", n)
	}
	if strings.Contains(body, "Weitere laden") {
		t.Errorf("kein Knopf mehr erwartet")
	}
	// n ist begrenzt und fehlerfest
	for _, bad := range []string{"-5", "abc", "0", "99999999999999999999"} {
		if w := e.get(u, url+"?ls_history=new&n="+bad); w.Code != 200 || len(histValues(w.Body.String())) == 0 {
			t.Errorf("n=%s: %d", bad, w.Code)
		}
	}
}

func TestHistoryMoreNoDuplicates(t *testing.T) {
	e := newHistEnv(t)
	u := e.user("Anna", "Alpha")
	e.grant(u, "procurement.view", "")
	id := e.order("")
	seedMany(e, id, u, 40, true) // alle mit demselben Zeitstempel
	url := "/records/history/purchase_order/" + id
	for _, mode := range []string{"new", "old"} {
		first := histValues(e.get(u, url+"?ls_history="+mode).Body.String())
		all := histValues(e.get(u, url+"?ls_history="+mode+"&n=60").Body.String())
		if len(first) != 30 || len(all) != 40 {
			t.Fatalf("%s: %d / %d Zeilen", mode, len(first), len(all))
		}
		seen := map[string]bool{}
		for _, v := range all {
			if seen[v] {
				t.Errorf("%s: %s doppelt", mode, v)
			}
			seen[v] = true
		}
		for i := range first {
			if first[i] != all[i] {
				t.Errorf("%s: Platz %d springt (%s → %s)", mode, i, first[i], all[i])
				break
			}
		}
	}
}

func TestHistoryCapped(t *testing.T) {
	e := newHistEnv(t)
	u := e.user("Anna", "Alpha")
	e.grant(u, "procurement.view", "")
	id := e.order("")
	old := historyCap
	historyCap = 5
	t.Cleanup(func() { historyCap = old })
	seedMany(e, id, u, 5, false)
	url := "/records/history/purchase_order/" + id + "?ls_history=new"
	if body := e.get(u, url).Body.String(); strings.Contains(body, "Nur die neuesten") {
		t.Errorf("genau an der Grenze kein Hinweis: %s", body)
	}
	e.hist(id, "status", "status", "Status geändert", "w06", u, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	e.hist(id, "status", "status", "Status geändert", "w07", u, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC))
	body := e.get(u, url).Body.String()
	if !strings.Contains(body, "Nur die neuesten 5 Einträge berücksichtigt") {
		t.Errorf("Hinweis fehlt: %s", body)
	}
	if got := strings.Join(histValues(body), " "); got != "w07 w06 w05 w04 w03" {
		t.Errorf("nur die neuesten 5: %q", got)
	}
	if rows, capped := e.h.recordHistoryAll(e.ctx, "purchase_order", id); !capped || len(rows) != 5 {
		t.Errorf("recordHistoryAll: %d Zeilen, capped=%v", len(rows), capped)
	}
}

func TestHistoryFragmentPerms(t *testing.T) {
	e := newHistEnv(t)
	none := e.user("Nora", "Nichts")
	a, b := e.dept("A"), e.dept("B")
	inA := e.order(a)
	someID := "0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b"

	// Ohne Recht: nie Inhalt. Modul -> Datensatz (unbekannte Kennung genügt, das Recht wird zuerst geprüft)
	for _, m := range []string{"infrastructure", "storage", "business_partner", "setup", "user"} {
		if w := e.get(none, "/records/history/"+m+"/"+someID); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", m, w.Code)
		}
	}
	if w := e.get(none, "/records/history/purchase_order/"+inA); w.Code != http.StatusForbidden {
		t.Errorf("Bestellung ohne Recht: %d", w.Code)
	}
	// Projekt, KVP, Störung, Ticket, Aufgabe, Wartung, Ersatzteil: ihre Seiten prüfen kein Einzelrecht
	// (jeder Angemeldete, Abteilung über die Vorgangssperre der Route) – die Historie zeigt dasselbe,
	// aber nie ohne Anmeldung.
	for _, m := range []string{"project", "kvp", "fault", "ticket", "task", "maintenance_task", "part"} {
		if w := e.get(&users.User{Role: "viewer"}, "/records/history/"+m+"/"+someID); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Anmeldung: %d", m, w.Code)
		}
		if w := e.get(none, "/records/history/"+m+"/"+someID); w.Code != http.StatusNotFound {
			t.Errorf("%s angemeldet, Datensatz fehlt: %d", m, w.Code)
		}
	}
	// Recht gilt: Seitenprüfung genügt, dann erst die Existenz (404)
	for m, perm := range map[string]string{"infrastructure": "infrastructure.view", "storage": "inventory.view", "business_partner": "directory.view"} {
		u := e.user("Vera", "View"+m[:3])
		e.grant(u, perm, "")
		if w := e.get(u, "/records/history/"+m+"/"+someID); w.Code != http.StatusNotFound {
			t.Errorf("%s mit %s: %d", m, perm, w.Code)
		}
	}
	// Abteilungsebene: Recht nur in B → Bestellung in A bleibt gesperrt, in B und überall nicht
	inB := e.order(b)
	uB := e.user("Bea", "Nurb")
	e.grant(uB, "procurement.view", b)
	if w := e.get(uB, "/records/history/purchase_order/"+inA); w.Code != http.StatusForbidden {
		t.Errorf("fremde Abteilung: %d", w.Code)
	}
	if w := e.get(uB, "/records/history/purchase_order/"+inB); w.Code != 200 {
		t.Errorf("eigene Abteilung: %d", w.Code)
	}
	uAll := e.user("Alex", "Ueberall")
	e.grant(uAll, "procurement.view", "")
	if w := e.get(uAll, "/records/history/purchase_order/"+inA); w.Code != 200 {
		t.Errorf("Recht überall: %d", w.Code)
	}
	// unbekanntes Modul
	if w := e.get(uAll, "/records/history/gibtsnicht/"+inA); w.Code != http.StatusNotFound {
		t.Errorf("unbekanntes Modul: %d", w.Code)
	}
}

func TestHistoryAllPagesUseSlot(t *testing.T) {
	pages := []string{"fault", "ticket", "kvp", "inventory", "partner", "purchase", "infra", "maintenance", "project", "setup", "storage", "task", "user"}
	slot := regexp.MustCompile(`record-history-slot" \(dict "Module" "([a-z_]+)"`)
	var modules []string
	for _, p := range pages {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", p+"_detail.gohtml"))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, `template "record-history" .History`) {
			t.Errorf("%s_detail: bettet die Historie noch fest ein", p)
		}
		m := slot.FindStringSubmatch(s)
		if m == nil {
			t.Errorf("%s_detail: record-history-slot fehlt", p)
			continue
		}
		modules = append(modules, m[1])
		if _, ok := historyModules[m[1]]; !ok {
			t.Errorf("%s_detail: Modul %q fehlt in historyModules", p, m[1])
		}
	}
	sort.Strings(modules)
	if len(modules) != 13 {
		t.Errorf("%d Seiten, erwartet 13: %v", len(modules), modules)
	}
}

// Wer nur „abschließen“ oder „buchen“ darf, sieht die Seite und darum auch ihre Historie.
func TestHistoryLikeThePage(t *testing.T) {
	e := newHistEnv(t)
	maker := e.user("Mia", "Macher")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	fault := e.row("faults", `INSERT INTO faults (title, created_by) VALUES ('H-Störung', $1::uuid) RETURNING id::text`, maker.ID)
	ticket := e.row("tickets", `INSERT INTO tickets (title, created_by) VALUES ('H-Ticket', $1::uuid) RETURNING id::text`, maker.ID)
	part := e.row("spare_parts", `INSERT INTO spare_parts (part_number, name, created_by) VALUES ($2, 'H-Teil', $1::uuid) RETURNING id::text`, maker.ID, "H"+e.sfx)
	e.histT("fault", fault, "status", "status", "Status geändert", "w01", maker, at)
	e.histT("ticket", ticket, "status", "status", "Status geändert", "w01", maker, at)
	e.histT("spare_part", part, "update", "Kanban", "Kanban aktiviert", "w01", maker, at)

	for _, c := range []struct{ module, id, perm string }{
		{"fault", fault, "faults.resolve"},
		{"ticket", ticket, "tickets.resolve"},
		{"part", part, "inventory.book"},
	} {
		u := e.user("Rolf", "Resolve")
		e.grant(u, c.perm, "")
		w := e.get(u, "/records/history/"+c.module+"/"+c.id)
		if w.Code != 200 || len(histValues(w.Body.String())) != 1 {
			t.Errorf("%s mit nur %s: %d %s", c.module, c.perm, w.Code, w.Body.String())
		}
	}
	// ein Benutzer ganz ohne Rechte, aber angemeldet, sieht sie ebenfalls (wie die Seite)
	if w := e.get(e.user("Nina", "Nichts"), "/records/history/fault/"+fault); w.Code != 200 {
		t.Errorf("angemeldet ohne Einzelrecht: %d", w.Code)
	}
}

// Ohne Recht: leiser Hinweis im Platz statt der globalen HTMX-Fehlermeldung.
func TestHistorySlotQuietDenied(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&sb, "record-history-slot", map[string]any{"Module": "storage", "ID": "x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `data-quiet-denied="Keine Berechtigung für die Historie"`) {
		t.Errorf("Platzhalter ohne Hinweis: %s", sb.String())
	}
	b, _ := os.ReadFile(filepath.Join("..", "..", "web", "templates", "base.gohtml"))
	if !strings.Contains(string(b), "data-quiet-denied") {
		t.Errorf("base.gohtml behandelt data-quiet-denied nicht")
	}
}

// Ein unbekannter Schlüssel darf nie „offen“ heißen: der Start bricht ab.
func TestHistoryModuleOfUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("kein Abbruch bei unbekanntem Modul")
		}
	}()
	historyModuleOf("gibtsnicht", historyLogin)
}

func TestHistoryLabelStable(t *testing.T) {
	for _, c := range []struct{ action, field, want string }{
		{"status", "status", "Status geändert"},
		{"create", "", "Angelegt"},
		{"update", "due_date", "Geändert · Fälligkeit"},
		{"update", "Kanban", "Geändert · Kanban"},
		{"seltsam", "", "seltsam"},
	} {
		if got := historyLabel(c.action, c.field); got != c.want {
			t.Errorf("%s/%s: %q, erwartet %q", c.action, c.field, got, c.want)
		}
	}
}
