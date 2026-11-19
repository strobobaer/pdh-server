package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

// kvpEnv: Labor-Umgebung für die KVP-Seiten mit Ansichtsrecht (kvp.view, Migration 183).
type kvpEnv struct {
	*projEnv
	roleNone   string // keine Rechte
	roleView   string // kvp.view überall
	roleManage string // kvp.manage, aber nicht kvp.view
}

func newKVPEnv(t *testing.T) *kvpEnv {
	t.Helper()
	p := newProjEnv(t)
	e := &kvpEnv{projEnv: p}
	e.roleNone = p.role("vf_kn", nil)
	e.roleView = p.role("vf_kv", map[string]string{"kvp.view": ""})
	e.roleManage = p.role("vf_km", map[string]string{"kvp.manage": ""})
	return e
}

// idea: Vorschlag mit eindeutigem Titel (nur der Titel zählt in den Seiten).
func (e *kvpEnv) idea(label, infra string) string {
	id := e.kvp(infra)
	e.exec(`UPDATE kvp_ideas SET title = $2, problem = 'Ist', proposal = 'Soll' WHERE id = $1::uuid`, id, label+e.sfx)
	return id
}

func (e *kvpEnv) status(id string) string {
	e.t.Helper()
	return e.id(`SELECT status::text FROM kvp_ideas WHERE id = $1::uuid`, id)
}

func (e *kvpEnv) page(u *users.User, target string) *httptest.ResponseRecorder {
	return e.do(e.h.KVPPage, "GET", target, u, nil, nil)
}

func (e *kvpEnv) comments(u *users.User, method, id string, form url.Values) *httptest.ResponseRecorder {
	fn := e.h.RecordCommentsWeb
	if method == "POST" {
		fn = e.h.RecordCommentsAddWeb
	}
	return e.do(fn, method, "/records/comments/kvp/"+id, u, map[string]string{"module": "kvp", "id": id}, form)
}

func (e *kvpEnv) commentCount(id string) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM kvp_comments WHERE idea_id = $1::uuid`, id).Scan(&n))
	return n
}

func TestKVPPageWithoutView(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_kp1", e.roleNone)
	other := e.idea("KVNFREMD", e.infraA)
	mine := e.idea("KVNEIGEN", e.infraB)
	e.exec(`UPDATE kvp_ideas SET submitter_id = $2::uuid WHERE id = $1::uuid`, mine, u.ID)
	e.exec(`UPDATE kvp_ideas SET status = 'plan' WHERE id = $1::uuid`, other)

	for _, target := range []string{"/kvp", "/kvp?tab=ideas", "/kvp?tab=board", "/kvp?tab=ideas&status=all", "/kvp?tab=ideas&status=archive",
		"/kvp?tab=ideas&q=KVNFREMD", "/kvp?tab=ideas&status=plan"} {
		w := e.page(u, target)
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("%s: %d", target, w.Code)
		}
		if strings.Contains(body, "KVNFREMD"+e.sfx) {
			t.Errorf("%s: fremder Vorschlag sichtbar", target)
		}
		for _, bad := range []string{"Einsparung realisiert", "ti-trophy", "Nutzen-Aufwand-Matrix", "PDCA-Board", "Prämien"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: %q ohne Recht sichtbar", target, bad)
			}
		}
		if !strings.Contains(body, "pdhKVP()") {
			t.Errorf("%s: Knopf „Idee einreichen“ fehlt", target)
		}
	}
	if body := e.page(u, "/kvp?tab=ideas").Body.String(); !strings.Contains(body, "KVNEIGEN") || !strings.Contains(body, "Meine Vorschläge") {
		t.Errorf("eigener Vorschlag oder Überschrift fehlt")
	}
}

func TestKVPPageWithView(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_kp2", e.roleView)
	a := e.idea("KVWA", e.infraA)
	b := e.idea("KVWB", e.infraB)
	e.exec(`UPDATE kvp_ideas SET status = 'plan' WHERE id = ANY($1::uuid[])`, []string{a, b})
	body := e.page(u, "/kvp?tab=ideas").Body.String()
	for _, want := range []string{"KVWA" + e.sfx, "KVWB" + e.sfx, "Einsparung realisiert", "PDCA-Board", "ti-trophy"} {
		if !strings.Contains(body, want) {
			t.Errorf("mit Recht fehlt %q", want)
		}
	}
	if strings.Contains(body, "Meine Vorschläge") {
		t.Errorf("Überschrift „Meine Vorschläge“ trotz Recht")
	}
}

// Recht nur in Abteilung A: Liste und Tafel zeigen nur A und Beteiligte, keine Kennzahlen aller.
func TestKVPPageViewInDepartment(t *testing.T) {
	e := newKVPEnv(t)
	role := e.role("vf_kd", map[string]string{"kvp.view": e.deptA})
	u := e.person("vf_kp3", role)
	a := e.idea("KVDA", e.infraA)
	e.idea("KVDB", e.infraB)
	e.exec(`UPDATE kvp_ideas SET status = 'plan' WHERE id = $1::uuid`, a)
	for _, target := range []string{"/kvp?tab=ideas", "/kvp?tab=board"} {
		body := e.page(u, target).Body.String()
		if !strings.Contains(body, "KVDA"+e.sfx) || strings.Contains(body, "KVDB"+e.sfx) {
			t.Errorf("%s: Abteilung A erwartet, B nicht", target)
		}
		if strings.Contains(body, "Einsparung realisiert") {
			t.Errorf("%s: Kennzahlen aller Vorschläge trotz Recht nur in einer Abteilung", target)
		}
	}
}

func TestKVPSubmitterWithoutView(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_ks", e.roleNone)
	form := url.Values{"title": {"KVSUB" + e.sfx}, "problem": {"Ist"}, "proposal": {"Soll"}}
	w := e.do(e.h.KVPCreateWeb, "POST", "/kvp", u, nil, form)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/kvp/") {
		t.Fatalf("einreichen: %d %s", w.Code, loc)
	}
	id := e.id(`SELECT id::text FROM kvp_ideas WHERE title = $1`, "KVSUB"+e.sfx)
	e.kvpIDs = append(e.kvpIDs, id)
	if w := e.do(e.h.KVPDetailPage, "GET", "/kvp/"+id, u, map[string]string{"id": id}, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "KVSUB"+e.sfx) {
		t.Errorf("Detail: %d", w.Code)
	}
	if w := e.do(e.h.KVPPrintPage, "GET", "/kvp/"+id+"/print", u, map[string]string{"id": id}, nil); w.Code != 200 {
		t.Errorf("Druck: %d", w.Code)
	}
	if w := e.comments(u, "GET", id, nil); w.Code != 200 {
		t.Errorf("Kommentare lesen: %d", w.Code)
	}
	if w := e.comments(u, "POST", id, url.Values{"text": {"Rückfrage"}}); w.Code != 200 || e.commentCount(id) != 1 {
		t.Errorf("Kommentar schreiben: %d", w.Code)
	}
	if w := e.do(e.h.KVPCommentLegacy, "POST", "/kvp/"+id+"/comment", u, map[string]string{"id": id}, url.Values{"text": {"alt"}}); w.Code != 200 || e.commentCount(id) != 2 {
		t.Errorf("alter Weg: %d", w.Code)
	}
	// Inhalt ändern darf die einreichende Person, solange noch nicht bewertet
	if w := e.do(e.h.KVPSaveWeb, "POST", "/kvp/"+id, u, map[string]string{"id": id},
		url.Values{"title": {"KVSUB" + e.sfx}, "problem": {"Ist2"}, "proposal": {"Soll"}}); w.Code != http.StatusSeeOther {
		t.Errorf("eigenen Vorschlag ändern: %d", w.Code)
	}
}

func TestKVPDetailNeedsView(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_kd1", e.roleNone)
	id := e.idea("KVGEHEIM", e.infraA)
	e.exec(`INSERT INTO kvp_comments (idea_id, user_id, text) VALUES ($1::uuid, $2::uuid, 'geheimer Kommentar')`, id, e.creator)
	p := map[string]string{"id": id}
	w := e.do(e.h.KVPDetailPage, "GET", "/kvp/"+id, u, p, nil)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "KVGEHEIM") {
		t.Errorf("Detail: %d", w.Code)
	}
	if w := e.do(e.h.KVPPrintPage, "GET", "/kvp/"+id+"/print", u, p, nil); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "KVGEHEIM") {
		t.Errorf("Druck: %d", w.Code)
	}
	if w := e.comments(u, "GET", id, nil); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "geheimer") {
		t.Errorf("Kommentare lesen: %d", w.Code)
	}
	if w := e.comments(u, "POST", id, url.Values{"text": {"x"}}); w.Code != http.StatusForbidden || e.commentCount(id) != 1 {
		t.Errorf("Kommentare schreiben: %d", w.Code)
	}
	if w := e.do(e.h.KVPCommentLegacy, "POST", "/kvp/"+id+"/comment", u, p, url.Values{"text": {"x"}}); w.Code != http.StatusForbidden || e.commentCount(id) != 1 {
		t.Errorf("alter Weg: %d", w.Code)
	}
	// mit Recht: alles da
	v := e.person("vf_kd2", e.roleView)
	if w := e.do(e.h.KVPDetailPage, "GET", "/kvp/"+id, v, p, nil); w.Code != 200 {
		t.Errorf("Detail mit Recht: %d", w.Code)
	}
	if w := e.comments(v, "GET", id, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "geheimer") {
		t.Errorf("Kommentare mit Recht: %d", w.Code)
	}
}

func TestKVPManageWithoutView(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_km1", e.roleManage)
	id := e.idea("KVSTEUER", e.infraA)
	e.exec(`UPDATE kvp_ideas SET status = 'review' WHERE id = $1::uuid`, id)
	p := map[string]string{"id": id}
	calls := []struct {
		name string
		fn   http.HandlerFunc
		form url.Values
	}{
		{"Detail", e.h.KVPDetailPage, nil},
		{"Druck", e.h.KVPPrintPage, nil},
		{"Speichern", e.h.KVPSaveWeb, url.Values{"title": {"X"}, "problem": {"a"}, "proposal": {"b"}}},
		{"Bewerten", e.h.KVPAssessWeb, url.Values{"benefit_score": {"5"}, "effort_score": {"1"}}},
		{"Plan", e.h.KVPPlanWeb, url.Values{"root_cause": {"x"}}},
		{"Prüfen", e.h.KVPCheckWeb, url.Values{"check_result": {"ok"}}},
		{"Standard", e.h.KVPActWeb, url.Values{"standardization": {"x"}}},
		{"Status", e.h.KVPAdvanceWeb, url.Values{"to": {"parked"}, "note": {"weg"}}},
		{"Maßnahme anlegen", e.h.KVPActionAddWeb, url.Values{"description": {"x"}}},
		{"Löschen", e.h.KVPDeleteWeb, url.Values{}},
	}
	for _, c := range calls {
		method := "POST"
		if c.form == nil {
			method = "GET"
		}
		if w := e.do(c.fn, method, "/kvp/"+id, u, p, c.form); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Ansichtsrecht: %d, erwartet 403", c.name, w.Code)
		}
	}
	act := e.id(`INSERT INTO kvp_actions (idea_id, description) VALUES ($1::uuid, 'M') RETURNING id::text`, id)
	pa := map[string]string{"id": id, "aid": act}
	for name, fn := range map[string]http.HandlerFunc{"Maßnahme abhaken": e.h.KVPActionToggleWeb, "Maßnahme löschen": e.h.KVPActionDeleteWeb} {
		if w := e.do(fn, "POST", "/kvp/"+id+"/actions/"+act, u, pa, url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Ansichtsrecht: %d, erwartet 403", name, w.Code)
		}
	}
	if e.status(id) != "review" {
		t.Errorf("Status verändert: %s", e.status(id))
	}
	if n := e.id(`SELECT COUNT(*)::text FROM kvp_ideas WHERE id = $1::uuid`, id); n != "1" {
		t.Errorf("Vorschlag gelöscht")
	}
	// mit Ansichtsrecht dazu: Steuern geht wieder
	both := e.role("vf_kb", map[string]string{"kvp.manage": "", "kvp.view": ""})
	v := e.person("vf_km2", both)
	if w := e.do(e.h.KVPAdvanceWeb, "POST", "/kvp/"+id+"/status", v, p, url.Values{"to": {"parked"}, "note": {"später"}}); w.Code == http.StatusForbidden {
		t.Errorf("Steuern mit Ansichtsrecht: %d", w.Code)
	}
}

// Beteiligte Prüfer ohne Recht bearbeiten weiter, was sie bearbeiten durften.
func TestKVPReviewerWithoutViewKeepsWork(t *testing.T) {
	e := newKVPEnv(t)
	u := e.person("vf_kr", e.roleNone)
	id := e.idea("KVPRUEF", e.infraA)
	e.exec(`UPDATE kvp_ideas SET status = 'plan', reviewer_id = $2::uuid WHERE id = $1::uuid`, id, u.ID)
	if w := e.do(e.h.KVPDetailPage, "GET", "/kvp/"+id, u, map[string]string{"id": id}, nil); w.Code != 200 {
		t.Errorf("Prüfer: Detail %d", w.Code)
	}
	w := e.do(e.h.KVPActionAddWeb, "POST", "/kvp/"+id+"/actions", u, map[string]string{"id": id}, url.Values{"description": {"x"}})
	if w.Code == http.StatusForbidden {
		t.Errorf("Prüfer: Maßnahme anlegen %d", w.Code)
	}
}

func TestStatKVPWidgetNeedsView(t *testing.T) {
	e := newKVPEnv(t)
	d, ok := widgetDef("stat_kvp")
	if !ok || d.Perm != permKVPView {
		t.Fatalf("stat_kvp braucht %s, hat %q", permKVPView, d.Perm)
	}
	none := e.person("vf_kw1", e.roleNone)
	view := e.person("vf_kw2", e.roleView)
	if e.h.canFn(e.req(none.ID, e.roleNone))(d.Perm) {
		t.Errorf("Baustein ohne Recht verfügbar")
	}
	if !e.h.canFn(e.req(view.ID, e.roleView))(d.Perm) {
		t.Errorf("Baustein mit Recht nicht verfügbar")
	}
	// die Schnellaktion „Idee einreichen“ bleibt für alle
	qa, _ := widgetDef("quick_actions")
	if qa.Perm != "" {
		t.Errorf("Schnellaktionen brauchen ein Recht: %q", qa.Perm)
	}
}

// Anhänge eines Vorschlags über /api/v1/attachments/kvp/{id}: kvp steht nicht in scopeTables,
// daher prüft APIDepartmentScope das Ansehen eigens (Liste, Hochladen, Datensatzbild).
func TestKVPAttachmentsNeedView(t *testing.T) {
	e := newKVPEnv(t)
	id := e.idea("KVANH", e.infraA)
	reached := 0
	api := e.h.APIDepartmentScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	call := func(u *users.User, method, path string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(""))
		r.Header.Set("Authorization", "Bearer "+e.token(u))
		w := httptest.NewRecorder()
		before := reached
		api.ServeHTTP(w, r)
		if reached > before {
			return http.StatusOK
		}
		return w.Code
	}
	none := e.person("vf_ka1", e.roleNone)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/attachments/kvp/" + id},
		{"POST", "/api/v1/attachments/kvp/" + id},
		{"POST", "/api/v1/attachments/kvp/" + id + "/" + id + "/record-image"},
	} {
		if got := call(none, c.method, c.path); got != http.StatusForbidden {
			t.Errorf("%s %s ohne Recht: %d, erwartet 403", c.method, c.path, got)
		}
	}
	if got := call(e.person("vf_ka2", e.roleView), "GET", "/api/v1/attachments/kvp/"+id); got != http.StatusOK {
		t.Errorf("mit Recht: %d", got)
	}
	// Beteiligte sehen die Anhänge ihres Vorschlags
	e.exec(`UPDATE kvp_ideas SET submitter_id = $2::uuid WHERE id = $1::uuid`, id, none.ID)
	if got := call(none, "GET", "/api/v1/attachments/kvp/"+id); got != http.StatusOK {
		t.Errorf("beteiligt: %d", got)
	}
	// andere Arten bleiben unberührt
	if got := call(e.person("vf_ka3", e.roleNone), "GET", "/api/v1/attachments/ticket/"+id); got != http.StatusOK {
		t.Errorf("Anhänge anderer Arten: %d", got)
	}
}
