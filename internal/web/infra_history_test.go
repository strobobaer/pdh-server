package web

import (
	"bytes"
	"context"
	"html/template"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestParseInfraHistoryFilterDefaults(t *testing.T) {
	f := parseInfraHistoryFilter(httptest.NewRequest("GET", "/infrastructure/x/history", nil))
	if !f.IncludeChildren {
		t.Error("ohne Formular sollen Unteranlagen einbezogen werden")
	}
	if len(f.Modules) != len(infraHistoryModules) {
		t.Errorf("ohne Formular sollen alle Module aktiv sein, got %v", f.Modules)
	}
}

func TestParseInfraHistoryFilterForm(t *testing.T) {
	r := httptest.NewRequest("GET", "/infrastructure/x/history?filtered=1&module=fault&module=part&module=bogus&q=+Lager+&from=2026-01-01&to=kaputt&state=closed", nil)
	f := parseInfraHistoryFilter(r)
	if f.IncludeChildren {
		t.Error("abgewählte Unteranlagen-Checkbox muss respektiert werden")
	}
	if strings.Join(f.Modules, ",") != "fault,part" {
		t.Errorf("Module: %v", f.Modules)
	}
	if f.Query != "Lager" || f.State != "closed" {
		t.Errorf("Query/State: %q %q", f.Query, f.State)
	}
	if f.From == nil || f.To != nil {
		t.Errorf("Datum: from=%v to=%v", f.From, f.To)
	}
}

func TestLikePatternEscapes(t *testing.T) {
	if got := likePattern(`50%_a\b`); got != `%50\%\_a\\b%` {
		t.Errorf("likePattern: %s", got)
	}
}

func TestInfraHistoryTemplateRenders(t *testing.T) {
	tmpl, err := template.New("base.gohtml").Funcs(TemplateFuncs()).ParseFiles(filepath.Join("..", "..", "web", "templates", "base.gohtml"))
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	if _, err := tmpl.ParseGlob(filepath.Join("..", "..", "web", "templates", "widgets", "*.gohtml")); err != nil {
		t.Fatalf("widgets: %v", err)
	}
	res := &InfraHistoryResult{
		InfraID: "root",
		Filter:  infraHistoryFilter{Modules: []string{"fault", "comment", "part"}},
		Modules: []infraHistoryModuleCount{{infraHistoryModules[0], 1, true}},
		Pinned:  []InfraHistoryEntry{{Module: "comment", ID: "c1", Body: "Achtung <b>", Pinned: true, CanEdit: true}},
		Entries: []InfraHistoryEntry{
			{Module: "fault", ModuleLabel: "Störungen", Icon: "ti-alert-triangle", Title: "Motor heiß", StatusLabel: "Gelöst", DetailURL: "/faults/1", FromChild: true, InfraID: "child", InfraName: "Pumpe 2"},
			{Module: "part", ModuleLabel: "Ersatzteile", Title: "4711 · Lager", QtyLabel: "2 Stück", ParentTitle: "Störung: Motor heiß"},
			{Module: "comment", ID: "c2", Body: "Hinweis", CanEdit: false},
		},
		Total: 3,
		Parts: []InfraPartUsage{{PartID: "p1", PartNumber: "4711", Name: "Lager", Unit: "Stück", QtyLabel: "2", Records: 1, CostLabel: "12,00 €"}},
	}
	detail, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := detail.ParseFiles(filepath.Join("..", "..", "web", "templates", "infra_detail.gohtml")); err != nil {
		t.Fatalf("infra_detail: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "infra-history-results", res); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Motor heiß", "Pumpe 2", "2 Stück", "12,00 €", "/infrastructure/root/comments/c1/pin", "Achtung &lt;b&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("Ausgabe enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/comments/c2/delete") {
		t.Error("fremder Kommentar darf keine Löschen-Aktion haben")
	}
}

// ── Sortieren der Anlagen-Historie (Plan 2026-11-19-sortierung, Aufgabe 4) ──

// infra legt eine Anlage an (optional unter einer Oberanlage, optional in einer Abteilung).
func (e *histEnv) infra(name, parent, dept string) string {
	e.t.Helper()
	id := e.id(`INSERT INTO infrastructure (name, type, parent_id, department_id)
		VALUES ($1, 'device', NULLIF($2,'')::uuid, NULLIF($3,'')::uuid) RETURNING id::text`, name+e.sfx, parent, dept)
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1::uuid`, id)
	})
	return id
}

func (e *histEnv) fault(title, desc, infra string, by *users.User) string {
	e.t.Helper()
	return e.row("faults", `INSERT INTO faults (title, description, infrastructure_id, created_by)
		VALUES ($1, $2, $3::uuid, $4::uuid) RETURNING id::text`, title, desc, infra, by.ID)
}

func (e *histEnv) faultAction(fault, text string, by *users.User, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO fault_actions (fault_id, description, created_by, created_at) VALUES ($1::uuid, $2, $3::uuid, $4)`, fault, text, by.ID, at)
}

func (e *histEnv) comment(infra, text string, by *users.User, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO infrastructure_comments (infrastructure_id, text, created_by, created_at) VALUES ($1::uuid, $2, $3::uuid, $4)`, infra, text, by.ID, at)
}

// getInfra ruft das Historien-Fragment über den Router auf.
func (e *histEnv) getInfra(u *users.User, infra, query string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := chi.NewRouter()
	r.Get("/infrastructure/{id}/history", e.h.InfraHistoryWeb)
	req := httptest.NewRequest("GET", "/infrastructure/"+infra+"/history?"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		e.t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
	}
	return w
}

var infraMarkRe = regexp.MustCompile(`\bm-[a-z0-9]+`)

// infraMarks: die Marken m-… der Eintragskörper in der Reihenfolge ihres Erscheinens.
// Direkte Wiederholungen (Text und Bearbeiten-Feld eines eigenen Kommentars) zählen einmal.
func infraMarks(body string) []string {
	var out []string
	for _, m := range infraMarkRe.FindAllString(body, -1) {
		if len(out) == 0 || out[len(out)-1] != m {
			out = append(out, m)
		}
	}
	return out
}

func (e *histEnv) infraViewer(first, last string) *users.User {
	u := e.user(first, last)
	e.grant(u, "infrastructure.view", "")
	return u
}

func TestInfraHistorySortOld(t *testing.T) {
	e := newHistEnv(t)
	u := e.infraViewer("Anna", "Alpha")
	inf := e.infra("Alt", "", "")
	e.exec(`INSERT INTO infrastructure_comments (infrastructure_id, text, created_by, created_at)
		SELECT $1::uuid, 'm-' || lpad(i::text, 3, '0'), $2::uuid, TIMESTAMPTZ '2026-01-01 00:00:00+00' + i * interval '1 minute'
		FROM generate_series(1, 205) i`, inf, u.ID)

	body := e.getInfra(u, inf, "filtered=1&module=comment&ls_timeline=old").Body.String()
	old := infraMarks(body)
	if len(old) != 200 || old[0] != "m-001" || old[199] != "m-200" {
		t.Errorf("old: %d Einträge, erster %v", len(old), old[:min(2, len(old))])
	}
	if !strings.Contains(body, "älteste 200") {
		t.Errorf("old: Hinweis auf die ältesten fehlt")
	}
	nw := infraMarks(e.getInfra(u, inf, "filtered=1&module=comment&ls_timeline=new").Body.String())
	if len(nw) != 200 || nw[0] != "m-205" || nw[199] != "m-006" {
		t.Errorf("new: %d Einträge, erster %v", len(nw), nw[:min(2, len(nw))])
	}
}

func TestInfraHistoryFrequent(t *testing.T) {
	e := newHistEnv(t)
	u := e.infraViewer("Anna", "Alpha")
	inf := e.infra("Presse", "", "")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	f1 := e.fault("Pumpe tropft", "", inf, u)
	f2 := e.fault("Ventil klemmt", "", inf, u) // anderer Titel: die Maßnahme gruppiert trotzdem
	e.faultAction(f1, "Dichtung getauscht", u, at)
	e.faultAction(f2, "dichtung  GETAUSCHT!", u, at.AddDate(0, 0, 2)) // gleiche Maßnahme, andere Schreibweise
	e.faultAction(f1, "Filter gereinigt", u, at.AddDate(0, 0, 1))     // andere Maßnahme am selben Vorgang: bleibt getrennt
	e.faultAction(f1, "Lager geölt", u, at.AddDate(0, 0, 1))

	body := e.getInfra(u, inf, "filtered=1&module=action&ls_timeline=freq").Body.String()
	if !strings.Contains(body, "2× ") || strings.Count(body, "<details") != 1 || strings.Contains(body, "1× ") {
		t.Errorf("freq: eine Gruppe 2× erwartet: %s", body)
	}
	sums := regexp.MustCompile(`(?s)<summary.*?</summary>`).FindAllString(body, -1)
	if len(sums) != 1 || !strings.Contains(sums[0], "2× ") || !strings.Contains(sums[0], "dichtung") || strings.Contains(sums[0], "Filter") ||
		!strings.Contains(sums[0], "zuletzt 03.03.2026") {
		t.Errorf("Gruppenkopf: %q", sums)
	}
	if !strings.Contains(body, "Dichtung getauscht") || !strings.Contains(body, "dichtung  GETAUSCHT!") {
		t.Errorf("beide Einzeleinträge müssen in der Gruppe stehen")
	}
	// die Gruppe steht vor der Einzelzeile
	if a, b := strings.Index(body, "<details"), strings.Index(body, "Filter gereinigt"); !(a >= 0 && a < b) {
		t.Errorf("Gruppe nicht zuerst: %d %d", a, b)
	}
	// „Gleiches zusammen“: Gruppen nach Schlüssel aufsteigend (dichtung vor filter)
	same := e.getInfra(u, inf, "filtered=1&module=action&ls_timeline=same").Body.String()
	if a, b := strings.Index(same, "<details"), strings.Index(same, "Filter gereinigt"); !(a >= 0 && a < b) {
		t.Errorf("same: Reihenfolge %d %d", a, b)
	}
	// ungruppiert gibt es keine Gruppen
	if nw := e.getInfra(u, inf, "filtered=1&module=action&ls_timeline=new").Body.String(); strings.Contains(nw, "<details") {
		t.Errorf("new darf nicht gruppieren")
	}
}

func TestInfraHistoryByKind(t *testing.T) {
	e := newHistEnv(t)
	u := e.infraViewer("Anna", "Alpha")
	inf := e.infra("Kind", "", "")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	f := e.fault("Störung A", "m-fault", inf, u)
	e.faultAction(f, "m-action", u, at.AddDate(0, 0, 1))
	e.comment(inf, "m-comment", u, at.AddDate(0, 0, 2))
	// aufsteigend nach Art: Kommentare, Maßnahmen, Störungen
	got := strings.Join(infraMarks(e.getInfra(u, inf, "filtered=1&module=fault&module=action&module=comment&ls_timeline=kind").Body.String()), " ")
	if got != "m-comment m-action m-fault" {
		t.Errorf("kind: %q", got)
	}
}

func TestInfraHistoryByPerson(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.infraViewer("Anna", "Alpha"), e.user("Bert", "Beta")
	inf := e.infra("Person", "", "")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	e.comment(inf, "m-a1", bert, at)
	e.comment(inf, "m-a2", anna, at.AddDate(0, 0, 1))
	e.comment(inf, "m-a3", bert, at.AddDate(0, 0, 2))
	e.comment(inf, "m-a4", anna, at.AddDate(0, 0, 3))
	// Anna zuerst, je neueste zuerst; danach Bert
	got := strings.Join(infraMarks(e.getInfra(anna, inf, "filtered=1&module=comment&ls_timeline=person").Body.String()), " ")
	if got != "m-a4 m-a2 m-a3 m-a1" {
		t.Errorf("person: %q", got)
	}
}

func TestInfraHistorySortKeepsFilter(t *testing.T) {
	e := newHistEnv(t)
	u := e.infraViewer("Anna", "Alpha")
	inf := e.infra("Filter", "", "")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	e.comment(inf, "m-alt Dichtung", u, at.AddDate(0, -2, 0)) // vor dem Zeitraum
	e.comment(inf, "m-neu Dichtung", u, at)
	e.comment(inf, "m-neu Filter", u, at)
	body := e.getInfra(u, inf, "filtered=1&q=Dichtung&from=2026-02-01&module=comment&ls_timeline=freq").Body.String()
	if got := strings.Join(infraMarks(body), " "); got != "m-neu" {
		t.Errorf("Filter wirkt nicht mit Sortierung: %q", got)
	}
	for _, want := range []string{"q=Dichtung", "from=2026-02-01", "module=comment", "filtered=1", "ls_timeline=old", `hx-target="#ih-res"`, `name="ls_timeline"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Leiste verliert %q: %s", want, body)
		}
	}
	// die Wahl bleibt beim nächsten Aufruf ohne Parameter erhalten (gemerkt)
	if again := e.getInfra(u, inf, "filtered=1&module=comment").Body.String(); !strings.Contains(again, `sortbar-b on" role="button" aria-pressed="true" href="/infrastructure/`+inf+`/history?`) {
		t.Errorf("gemerkte Wahl fehlt: %s", again)
	}
}

func TestInfraHistoryScopeCounts(t *testing.T) {
	e := newHistEnv(t)
	maker := e.user("Mia", "Macher")
	a, b := e.dept("A"), e.dept("B")
	root := e.infra("Wurzel", "", "")
	inA := e.infra("Halle A", root, a)
	inB := e.infra("Halle B", root, b)
	for i := 0; i < 2; i++ {
		e.fault("Pumpe tropft", "", inA, maker)
	}
	for i := 0; i < 3; i++ {
		e.fault("Pumpe tropft", "", inB, maker)
	}
	role := "scoped" + e.sfx
	e.exec(`INSERT INTO roles (key, label, department_id) VALUES ($1, 'Nur A', $2::uuid)`, role, a)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
	scoped := e.user("Sven", "Scoped")
	scoped.Role = users.Role(role)
	e.grant(scoped, "infrastructure.view", "")
	all := e.infraViewer("Alex", "Ueberall")

	q := "filtered=1&children=1&module=fault&ls_timeline=freq"
	body := e.getInfra(scoped, root, q).Body.String()
	if !strings.Contains(body, "2× Pumpe tropft") || strings.Contains(body, "5× ") {
		t.Errorf("eingeschränkt: Gruppe darf nur 2 zählen: %s", body)
	}
	if !strings.Contains(body, "· 2 Einträge") {
		t.Errorf("eingeschränkt: Summe 2 erwartet: %s", body)
	}
	if body := e.getInfra(all, root, q).Body.String(); !strings.Contains(body, "5× Pumpe tropft") {
		t.Errorf("uneingeschränkt: 5× erwartet: %s", body)
	}
}

// Hinweis „Nur die neuesten … berücksichtigt“ bleibt auch für Benutzer mit Abteilungsbeschränkung,
// obwohl der Zähler nach dem Filter nur noch ihre sichtbaren Einträge kennt.
func TestInfraHistoryCapNoticeWithScope(t *testing.T) {
	e := newHistEnv(t)
	maker := e.user("Mia", "Macher")
	a, b := e.dept("A"), e.dept("B")
	root := e.infra("Wurzel", "", "")
	inA := e.infra("Halle A", root, a)
	inB := e.infra("Halle B", root, b)
	// erst B, dann A: unter den neuesten 3 sind beide Abteilungen, der eingeschränkte Benutzer sieht nur A
	for i := 0; i < 3; i++ {
		e.fault("Pumpe tropft", "", inB, maker)
	}
	for i := 0; i < 2; i++ {
		e.fault("Pumpe tropft", "", inA, maker)
	}
	role := "scopedcap" + e.sfx
	e.exec(`INSERT INTO roles (key, label, department_id) VALUES ($1, 'Nur A', $2::uuid)`, role, a)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
	scoped := e.user("Sven", "Scoped")
	scoped.Role = users.Role(role)
	e.grant(scoped, "infrastructure.view", "")

	old := infraHistoryGroupLimit
	infraHistoryGroupLimit = 3
	t.Cleanup(func() { infraHistoryGroupLimit = old })
	body := e.getInfra(scoped, root, "filtered=1&children=1&module=fault&ls_timeline=freq").Body.String()
	if !strings.Contains(body, "Nur die neuesten 3 Einträge berücksichtigt") {
		t.Errorf("Hinweis fehlt für eingeschränkten Benutzer: %s", body)
	}
	// Grenze nicht erreicht: kein Hinweis
	infraHistoryGroupLimit = 100
	if body := e.getInfra(scoped, root, "filtered=1&children=1&module=fault&ls_timeline=freq").Body.String(); strings.Contains(body, "Nur die neuesten") {
		t.Errorf("Hinweis ohne erreichte Grenze: %s", body)
	}
}

// Maßnahme und Kommentar gleichen Textes bilden eine Gruppe ohne „Maßnahme · “; nur reine Maßnahmen-Gruppen tragen es.
func TestInfraHistoryGroupLabelMixed(t *testing.T) {
	e := newHistEnv(t)
	u := e.infraViewer("Anna", "Alpha")
	inf := e.infra("Gemischt", "", "")
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	f := e.fault("Pumpe tropft", "", inf, u)
	e.faultAction(f, "Dichtung getauscht", u, at)
	e.comment(inf, "dichtung getauscht", u, at.AddDate(0, 0, 1)) // neuester Eintrag ist ein Kommentar
	body := e.getInfra(u, inf, "filtered=1&module=action&module=comment&ls_timeline=freq").Body.String()
	sums := regexp.MustCompile(`(?s)<summary.*?</summary>`).FindAllString(body, -1)
	if len(sums) != 1 || !strings.Contains(sums[0], "2× dichtung getauscht") || strings.Contains(sums[0], "Maßnahme ·") {
		t.Errorf("gemischte Gruppe: %q", sums)
	}
	e.faultAction(f, "Dichtung getauscht", u, at.AddDate(0, 0, 2))
	body = e.getInfra(u, inf, "filtered=1&module=action&ls_timeline=freq").Body.String()
	if sums = regexp.MustCompile(`(?s)<summary.*?</summary>`).FindAllString(body, -1); len(sums) != 1 || !strings.Contains(sums[0], "Maßnahme · Dichtung getauscht") {
		t.Errorf("reine Maßnahmen-Gruppe: %q", sums)
	}
}
