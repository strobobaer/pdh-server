package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"pdh/internal/core/users"
	"pdh/internal/modules/tasks"

	"github.com/go-chi/chi/v5"
)

// Ansichtsrecht für Projekte in den übrigen Stellen: Aufgaben-Formular, Partner,
// Benutzerseite, Anlagen-Historie, Copilot und Kalender. Je Stelle ein Fall ohne
// Recht (nur beteiligte Projekte) und einer mit Recht.

// reqFor: Anfrage mit angemeldeter Person (und optionaler Adresse).
func (e *projEnv) reqFor(u *users.User, target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	return r.WithContext(context.WithValue(r.Context(), "user", u))
}

// placesFx: ein sichtbares (beteiligtes) und ein fremdes Projekt, dazu Personen mit und ohne Recht.
type placesFx struct {
	*projEnv
	none, view          *users.User
	mine, other         string
	mineName, otherName string
}

func newPlacesFx(t *testing.T) *placesFx {
	t.Helper()
	e := newProjEnv(t)
	p := &placesFx{projEnv: e}
	p.none = e.person("vf_pl_none", e.roleNone)
	p.view = e.person("vf_pl_view", e.roleView)
	p.mineName, p.otherName = "PLMINE"+e.sfx, "PLOTHER"+e.sfx
	p.mine = e.named("PLMINE", e.infraA, "")
	p.other = e.named("PLOTHER", e.infraA, "")
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, p.mine, p.none.ID)
	return p
}

func TestTaskFormProjectOptionsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	ids := func(o []ProjectOption) map[string]bool {
		m := map[string]bool{}
		for _, x := range o {
			m[x.ID] = true
		}
		return m
	}
	got := ids(p.h.projectOptions(p.reqFor(p.none, "/"), ""))
	if !got[p.mine] || got[p.other] {
		t.Errorf("ohne Recht: beteiligt=%v fremd=%v (erwartet true/false)", got[p.mine], got[p.other])
	}
	// Das aktuell gewählte Projekt der Aufgabe bleibt in der Auswahl.
	if !ids(p.h.projectOptions(p.reqFor(p.none, "/"), p.other))[p.other] {
		t.Errorf("das gewählte Projekt der Aufgabe fehlt in der Auswahl")
	}
	got = ids(p.h.projectOptions(p.reqFor(p.view, "/"), ""))
	if !got[p.mine] || !got[p.other] {
		t.Errorf("mit Recht fehlen Projekte: %v", got)
	}
}

func TestPartnerRecordsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	partner := p.id(`INSERT INTO business_partners (name) VALUES ($1) RETURNING id::text`, "PLPartner"+p.sfx)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM record_external_parties WHERE partner_id = $1::uuid`, partner)
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM business_partners WHERE id = $1::uuid`, partner)
	})
	for _, id := range []string{p.mine, p.other} {
		p.exec(`INSERT INTO record_external_parties (ref_type, ref_id, role, kind, name, partner_id) VALUES ('project', $1::uuid, 'assigned', 'company', 'X', $2::uuid)`, id, partner)
	}
	titles := func(u *users.User) string {
		var s []string
		for _, rec := range p.h.partnerRecords(p.reqFor(u, "/"), partner) {
			s = append(s, rec.Title)
		}
		return strings.Join(s, "|")
	}
	if got := titles(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("Liste ohne Recht: %q", got)
	}
	if got := titles(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("Liste mit Recht: %q", got)
	}
	opts := func(u *users.User) string {
		var s []string
		for _, g := range p.h.partnerRecordOptions(p.reqFor(u, "/")) {
			for _, o := range g.Options {
				s = append(s, o.Label)
			}
		}
		return strings.Join(s, "|")
	}
	if got := opts(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("Auswahl ohne Recht: %q", got)
	}
	if got := opts(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("Auswahl mit Recht: %q", got)
	}
}

func TestUserDetailProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	// Die beobachtete Person ist an beiden Projekten zuständig.
	target := p.person("vf_pl_target", p.roleNone)
	p.exec(`UPDATE projects SET responsible_to = $2::uuid WHERE id = ANY($1::uuid[])`, []string{p.mine, p.other}, target.ID)
	titles := func(viewer *users.User, of string) string {
		var s []string
		for _, x := range p.h.userResponsibilities(p.reqFor(viewer, "/"), of) {
			s = append(s, x.Title)
		}
		return strings.Join(s, "|")
	}
	// Fremde Sicht ohne Recht: weder das eine noch das andere (nicht beteiligt).
	stranger := p.person("vf_pl_stranger", p.roleNone)
	if got := titles(stranger, target.ID); strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("fremde Sicht ohne Recht zeigt Projekte: %q", got)
	}
	// Beteiligt ist sichtbar (p.none ist an "mine" zugewiesen), das andere nicht.
	if got := titles(p.none, target.ID); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("beteiligte Sicht: %q", got)
	}
	// Die eigenen Zuständigkeiten bleiben sichtbar.
	if got := titles(target, target.ID); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("eigene Zuständigkeiten: %q", got)
	}
	// Mit Recht alles.
	if got := titles(p.view, target.ID); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("mit Recht: %q", got)
	}
}

func TestInfraHistoryProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, ref_type, ref_id, text, created_by) VALUES ($1::uuid, 'project', $2::uuid, $3, $4::uuid)`,
		p.infraA, p.other, "PLKOMM"+p.sfx, p.creator)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, ref_type, ref_id, text, pinned, created_by) VALUES ($1::uuid, 'project', $2::uuid, $3, true, $4::uuid)`,
		p.infraA, p.other, "PLPIN"+p.sfx, p.creator)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, text, created_by) VALUES ($1::uuid, $2, $3::uuid)`,
		p.infraA, "PLFREI"+p.sfx, p.creator)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM infrastructure_comments WHERE infrastructure_id = $1::uuid`, p.infraA)
	})
	f := infraHistoryFilter{IncludeChildren: true}
	for _, m := range infraHistoryModules {
		f.Modules = append(f.Modules, m.Key)
	}
	dump := func(u *users.User) (string, *InfraHistoryResult) {
		res, err := p.h.loadInfraHistory(context.Background(), p.reqFor(u, "/"), p.infraA, f, 200)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range res.Entries {
			s = append(s, e.Module+":"+e.Title+":"+e.Body+":"+e.ParentTitle)
		}
		for _, e := range res.Pinned {
			s = append(s, "pin:"+e.Body)
		}
		return strings.Join(s, "|"), res
	}
	got, res := dump(p.none)
	for _, want := range []string{p.mineName, "PLFREI" + p.sfx} {
		if !strings.Contains(got, want) {
			t.Errorf("ohne Recht fehlt %s: %s", want, got)
		}
	}
	for _, bad := range []string{p.otherName, "PLKOMM" + p.sfx, "PLPIN" + p.sfx} {
		if strings.Contains(got, bad) {
			t.Errorf("ohne Recht sichtbar: %s", bad)
		}
	}
	// Zähler: nur sichtbare Einträge (1 Projekt, 1 Kommentar).
	counts := map[string]int{}
	for _, m := range res.Modules {
		counts[m.Key] = m.Count
	}
	if counts["project"] != 1 || counts["comment"] != 1 {
		t.Errorf("Zähler ohne Recht: Projekte %d (1), Kommentare %d (1)", counts["project"], counts["comment"])
	}
	got, res = dump(p.view)
	for _, want := range []string{p.mineName, p.otherName, "PLKOMM" + p.sfx, "PLPIN" + p.sfx} {
		if !strings.Contains(got, want) {
			t.Errorf("mit Recht fehlt %s: %s", want, got)
		}
	}
	for _, m := range res.Modules {
		if m.Key == "project" && m.Count != 2 {
			t.Errorf("Zähler mit Recht: Projekte %d (2)", m.Count)
		}
	}
	// Auswahl der Bezüge für einen neuen Kommentar
	labels := func(u *users.User) string {
		var s []string
		for _, o := range p.h.infraCommentRefOptions(p.reqFor(u, "/"), p.infraA) {
			s = append(s, o.Label)
		}
		return strings.Join(s, "|")
	}
	if g := labels(p.none); !strings.Contains(g, p.mineName) || strings.Contains(g, p.otherName) {
		t.Errorf("Bezüge ohne Recht: %s", g)
	}
	if g := labels(p.view); !strings.Contains(g, p.otherName) {
		t.Errorf("Bezüge mit Recht: %s", g)
	}
}

func TestCopilotProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	mk := func(u *users.User) *copilotData {
		d := &copilotData{h: p.h, userID: u.ID, can: func(string) bool { return true }}
		d.limitProjects(p.reqFor(u, "/"))
		return d
	}
	ctx := context.Background()
	d := mk(p.none)
	if s, err := d.search(ctx, "PLOTHER"+p.sfx, []string{"project"}, 10); err != nil || strings.Contains(s, p.other) {
		t.Errorf("Suche ohne Recht zeigt die ID des fremden Projekts, err=%v: %s", err, s)
	}
	if s, err := d.search(ctx, "PLOTHER"+p.sfx, []string{"project"}, 10); err != nil || strings.Contains(s, p.otherName) {
		t.Errorf("Suche ohne Recht zeigt fremdes Projekt, err=%v: %s", err, s)
	}
	if s, err := d.search(ctx, "PLMINE"+p.sfx, []string{"project"}, 10); err != nil || !strings.Contains(s, p.mineName) {
		t.Errorf("Suche ohne Recht: eigenes Projekt fehlt, err=%v: %s", err, s)
	}
	if s, err := d.listRecords(ctx, "project", "all", "", 0, false, false, 50); err != nil || strings.Contains(s, p.otherName) || !strings.Contains(s, p.mineName) {
		t.Errorf("Liste ohne Recht, err=%v: %s", err, s)
	}
	if _, err := d.getRecord(ctx, "project", p.other); err == nil {
		t.Errorf("getRecord eines fremden Projekts ohne Recht muss scheitern")
	}
	if s, err := d.getRecord(ctx, "project", p.mine); err != nil || !strings.Contains(s, p.mineName) {
		t.Errorf("getRecord des eigenen Projekts, err=%v: %s", err, s)
	}
	// ohne limitProjects: sicherer Ausgangszustand, keine Projekte
	raw := &copilotData{h: p.h, userID: p.none.ID, can: func(string) bool { return true }}
	if s, err := raw.listRecords(ctx, "project", "all", "", 0, false, false, 50); err != nil || strings.Contains(s, p.mineName) {
		t.Errorf("ohne limitProjects dürfen keine Projekte erscheinen, err=%v: %s", err, s)
	}
	d = mk(p.view)
	if s, err := d.listRecords(ctx, "project", "all", "", 0, false, false, 500); err != nil || !strings.Contains(s, p.otherName) || !strings.Contains(s, p.mineName) {
		t.Errorf("Liste mit Recht, err=%v: %s", err, s)
	}
	if _, err := d.getRecord(ctx, "project", p.other); err != nil {
		t.Errorf("getRecord mit Recht: %v", err)
	}
}

// Rolle mit Abteilungssperre: Reiterzähler und Überschrift zählen dieselben Projekte wie die
// Liste (vorher zählten die Reiter bei Recht überall alle Projekte der Datenbank).
func TestProjectTabsMatchListWithDepartmentLock(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVSA", e.infraA, "")
	pb := e.named("PVSB", e.infraB, "")
	e.exec(`UPDATE projects SET status = 'active' WHERE id = ANY($1::uuid[])`, []string{pa, pb})
	locked := e.role("vf_plock", map[string]string{"projects.view": ""})
	e.exec(`UPDATE roles SET department_id = $2::uuid WHERE key = $1`, locked, e.deptA)
	u := e.person("vf_s1", locked)
	cardRe := regexp.MustCompile(`class="mt-plan [^"]*" href="/projects/`)
	num := func(body, pattern string) int {
		t.Helper()
		m := regexp.MustCompile(pattern).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%q nicht gefunden", pattern)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	for target, tab := range map[string]string{"/projects": `Alle <b>(\d+)</b>`, "/projects?status=active": `Aktiv <b>(\d+)</b>`} {
		body := e.do(e.h.ProjectsPage, "GET", target, u, nil, nil).Body.String()
		if !strings.Contains(body, "PVSA"+e.sfx) || strings.Contains(body, "PVSB"+e.sfx) {
			t.Fatalf("%s: Projekt in A erwartet, in B (gesperrte Abteilung) nicht", target)
		}
		cards := len(cardRe.FindAllString(body, -1))
		if n := num(body, tab); n != cards {
			t.Errorf("%s: Reiter zählt %d, Liste zeigt %d", target, n, cards)
		}
		if n := num(body, `<p>(\d+) Projekte`); n != cards {
			t.Errorf("%s: Überschrift zählt %d, Liste zeigt %d", target, n, cards)
		}
	}
}

// Verknüpfen per Kennung nur mit Projekten, die man sehen darf: Aufgabe (Schnittstelle, die auch
// die Formulare nutzen), Partner-Zuordnung und Bezug eines Anlagen-Kommentars.
func TestProjectLinkNeedsView(t *testing.T) {
	p := newPlacesFx(t)
	ctx := context.Background()
	title := "PLTASK" + p.sfx
	t.Cleanup(func() { _, _ = p.pool.Exec(context.Background(), `DELETE FROM tasks WHERE title LIKE $1`, title+"%") })
	th := tasks.NewHandler(p.h.tasks)
	th.ProjectAllowed = p.h.MayLinkProject
	api := chi.NewRouter()
	api.Mount("/api/v1/tasks", th.Routes("test-secret"))
	call := func(u *users.User, path, body string) (int, string) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+p.token(u))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	taskProject := func(id string) string {
		return p.id(`SELECT COALESCE(project_id::text, '') FROM tasks WHERE id = $1::uuid`, id)
	}
	// anlegen: fremdes Projekt abgelehnt, beteiligtes erlaubt
	if c, b := call(p.none, "/api/v1/tasks/", `{"title":"`+title+`1","project_id":"`+p.other+`"}`); c != http.StatusForbidden || !strings.Contains(b, "nicht ansehen") {
		t.Errorf("Aufgabe am fremden Projekt anlegen: %d %s", c, b)
	}
	if n := p.id(`SELECT COUNT(*)::text FROM tasks WHERE title = $1`, title+"1"); n != "0" {
		t.Errorf("Aufgabe trotzdem angelegt")
	}
	c, b := call(p.none, "/api/v1/tasks/", `{"title":"`+title+`2","project_id":"`+p.mine+`"}`)
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(b), &created); c != http.StatusCreated || err != nil || created.Data.ID == "" {
		t.Fatalf("Aufgabe am beteiligten Projekt anlegen: %d %s", c, b)
	}
	tid := created.Data.ID
	// ändern: auf das fremde Projekt umhängen abgelehnt
	if c, b := call(p.none, "/api/v1/tasks/"+tid+"/edit", `{"project_id":"`+p.other+`"}`); c != http.StatusForbidden || taskProject(tid) != p.mine {
		t.Errorf("auf fremdes Projekt umhängen: %d %s, Projekt %s", c, b, taskProject(tid))
	}
	// schon verknüpftes Projekt bleibt beim Speichern erlaubt (unverändert mitgeschickt)
	p.exec(`UPDATE tasks SET project_id = $2::uuid WHERE id = $1::uuid`, tid, p.other)
	if c, b := call(p.none, "/api/v1/tasks/"+tid+"/edit", `{"title":"`+title+`2b","project_id":"`+p.other+`"}`); c != http.StatusOK {
		t.Errorf("Speichern mit unverändertem Projekt: %d %s", c, b)
	}
	// mit Recht: erlaubt
	if c, b := call(p.view, "/api/v1/tasks/", `{"title":"`+title+`3","project_id":"`+p.other+`"}`); c != http.StatusCreated {
		t.Errorf("mit Recht anlegen: %d %s", c, b)
	}

	// Partner einem Vorgang zuordnen
	partner := p.id(`INSERT INTO business_partners (name) VALUES ($1) RETURNING id::text`, "PLAssign"+p.sfx)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM record_external_parties WHERE partner_id = $1::uuid`, partner)
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = ANY($1::uuid[])`, []string{p.mine, p.other})
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM business_partners WHERE id = $1::uuid`, partner)
	})
	parties := func(id string) string {
		return p.id(`SELECT COUNT(*)::text FROM record_external_parties WHERE partner_id = $1::uuid AND ref_id = $2::uuid`, partner, id)
	}
	if err := p.h.assignPartner(ctx, p.reqFor(p.none, "/"), partner, "record", "assigned", "project:"+p.other); err == nil || !strings.Contains(err.Error(), "nicht ansehen") || parties(p.other) != "0" {
		t.Errorf("Partner am fremden Projekt: %v", err)
	}
	if err := p.h.assignPartner(ctx, p.reqFor(p.none, "/"), partner, "record", "assigned", "project:"+p.mine); err != nil || parties(p.mine) != "1" {
		t.Errorf("Partner am beteiligten Projekt: %v", err)
	}
	if err := p.h.assignPartner(ctx, p.reqFor(p.view, "/"), partner, "record", "assigned", "project:"+p.other); err != nil {
		t.Errorf("Partner mit Recht: %v", err)
	}

	// Bezug eines Anlagen-Kommentars
	if _, _, err := p.h.parseInfraCommentRef(p.reqFor(p.none, "/"), p.infraA, "project:"+p.other); err == nil || !strings.Contains(err.Error(), "nicht ansehen") {
		t.Errorf("Kommentar-Bezug auf fremdes Projekt: %v", err)
	}
	if _, _, err := p.h.parseInfraCommentRef(p.reqFor(p.none, "/"), p.infraA, "project:"+p.mine); err != nil {
		t.Errorf("Kommentar-Bezug auf beteiligtes Projekt: %v", err)
	}
	if _, _, err := p.h.parseInfraCommentRef(p.reqFor(p.view, "/"), p.infraA, "project:"+p.other); err != nil {
		t.Errorf("Kommentar-Bezug mit Recht: %v", err)
	}
}

// Ein Projekt-Verweis im Chat bekommt seinen Titel nur, wenn der Absender das Projekt sehen darf.
func TestChatLinkTitleNeedsView(t *testing.T) {
	p := newPlacesFx(t)
	ctx := context.Background()
	conv := p.id(`INSERT INTO chat_conversations (kind, name, created_by) VALUES ('group', $1, $2::uuid) RETURNING id::text`, "PLChat"+p.sfx, p.none.ID)
	msg := p.id(`INSERT INTO chat_messages (conversation_id, user_id, body) VALUES ($1::uuid, $2::uuid, 'x') RETURNING id::text`, conv, p.none.ID)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = p.pool.Exec(c, `DELETE FROM chat_message_links WHERE message_id = $1::uuid`, msg)
		_, _ = p.pool.Exec(c, `DELETE FROM chat_messages WHERE conversation_id = $1::uuid`, conv)
		_, _ = p.pool.Exec(c, `DELETE FROM chat_conversations WHERE id = $1::uuid`, conv)
	})
	body := "siehe /projects/" + p.other + " und /projects/" + p.mine
	titles := func(sender string) map[string]string {
		p.h.chatResolveLinks(ctx, msg, body, sender)
		out := map[string]string{}
		rows, err := p.pool.Query(ctx, `SELECT ref_id::text, title FROM chat_message_links WHERE message_id = $1::uuid`, msg)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, title string
			if rows.Scan(&id, &title) == nil {
				out[id] = title
			}
		}
		return out
	}
	got := titles(p.none.ID)
	if v, ok := got[p.other]; !ok || v != "" {
		t.Errorf("fremdes Projekt ohne Recht: Verweis da=%v, Titel %q (erwartet ohne Titel)", ok, v)
	}
	if got[p.mine] != p.mineName {
		t.Errorf("beteiligtes Projekt: Titel %q", got[p.mine])
	}
	if got := titles(p.view.ID); got[p.other] != p.otherName {
		t.Errorf("mit Recht: Titel %q", got[p.other])
	}
}

// Erst filtern, dann begrenzen: Liegen unsichtbare Projekte vorn, bleibt das sichtbare trotzdem.
func TestCalendarProjectLimitAfterFilter(t *testing.T) {
	p := newPlacesFx(t)
	old := calendarProjectLimit
	calendarProjectLimit = 1
	t.Cleanup(func() { calendarProjectLimit = old })
	// das fremde Projekt beginnt vor dem eigenen (sortiert nach Beginn)
	p.exec(`UPDATE projects SET start_date = CURRENT_DATE - 300, end_date = CURRENT_DATE + 30, status = 'active' WHERE id = $1::uuid`, p.other)
	p.exec(`UPDATE projects SET start_date = CURRENT_DATE - 1, end_date = CURRENT_DATE + 30, status = 'active' WHERE id = $1::uuid`, p.mine)
	d := p.h.buildCalendar(p.reqFor(p.none, "/calendar?set=1&show=project&days=14"), nil)
	if len(d.ProjectRows) != 1 || d.ProjectRows[0].Name != p.mineName {
		var s []string
		for _, r := range d.ProjectRows {
			s = append(s, r.Name)
		}
		t.Errorf("ohne Recht, Grenze 1: %q, erwartet nur %s", s, p.mineName)
	}
}

// Die Partnerliste zählt nur Vorgänge, die die Person sehen darf.
func TestPartnerListCountsVisibleRecords(t *testing.T) {
	p := newPlacesFx(t)
	partner := p.id(`INSERT INTO business_partners (name) VALUES ($1) RETURNING id::text`, "PLZaehl"+p.sfx)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM record_external_parties WHERE partner_id = $1::uuid`, partner)
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM business_partners WHERE id = $1::uuid`, partner)
	})
	for _, id := range []string{p.mine, p.other} {
		p.exec(`INSERT INTO record_external_parties (ref_type, ref_id, role, kind, name, partner_id) VALUES ('project', $1::uuid, 'assigned', 'company', 'X', $2::uuid)`, id, partner)
	}
	listCount := func(u *users.User) string {
		t.Helper()
		body := p.do(p.h.DirectoryPage, "GET", "/directory?q="+url.QueryEscape("PLZaehl"+p.sfx), u, nil, nil).Body.String()
		m := regexp.MustCompile(`title="Vorgänge"><i class="ti ti-briefcase"></i>(\d+)`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("Zahl der Vorgänge nicht gefunden")
		}
		return m[1]
	}
	none := p.person("vf_pz1", p.role("vf_pzn", map[string]string{"directory.view": ""}))
	p.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, p.mine, none.ID)
	view := p.person("vf_pz2", p.role("vf_pzv", map[string]string{"directory.view": "", "projects.view": ""}))
	if got := listCount(none); got != "1" {
		t.Errorf("ohne Recht: %s Vorgänge, erwartet 1 (nur das beteiligte Projekt)", got)
	}
	if got := listCount(view); got != "2" {
		t.Errorf("mit Recht: %s Vorgänge, erwartet 2", got)
	}
}

func TestCalendarProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	rows := func(u *users.User) string {
		d := p.h.buildCalendar(p.reqFor(u, "/calendar?set=1&show=project&days=14"), nil)
		var s []string
		for _, r := range d.ProjectRows {
			s = append(s, r.Name)
		}
		return strings.Join(s, "|")
	}
	if got := rows(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("ohne Recht: %q", got)
	}
	if got := rows(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("mit Recht: %q", got)
	}
}
