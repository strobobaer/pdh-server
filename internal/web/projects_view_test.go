package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/modules/projects"
	"pdh/internal/modules/tasks"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// projEnv: Labor-Umgebung für die Seiten, die Schnittstelle und die Datensatz-Bausteine
// der Projekte (Ansichtsrecht projects.view, Migration 183).
type projEnv struct {
	*viewFx
	roleNone string // darf Projekte bearbeiten, aber nicht ansehen
	roleView string // projects.view überall
	roleInA  string // projects.view nur in Abteilung A
	roleEdit string // bearbeiten + ansehen
}

func newProjEnv(t *testing.T) *projEnv {
	t.Helper()
	f := newViewFx(t)
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	f.h.tmpl = loadTestTemplates(t)
	f.h.projects = projects.NewService(projects.NewRepository(f.pool))
	f.h.tasks = tasks.NewService(tasks.NewRepository(f.pool))
	f.h.users = users.NewService(users.NewRepository(f.pool), "test-secret", 1)
	f.h.jwtSecret = "test-secret"
	cwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	e := &projEnv{viewFx: f}
	e.roleNone = f.role("vf_pn", map[string]string{"projects.edit": ""})
	e.roleView = f.role("vf_pv", map[string]string{"projects.view": ""})
	e.roleInA = f.role("vf_pa", map[string]string{"projects.view": f.deptA})
	e.roleEdit = f.role("vf_pe", map[string]string{"projects.edit": "", "projects.view": ""})
	return e
}

// named: Projekt mit eindeutigem Namen (nur der Name zählt in den Seiten).
func (e *projEnv) named(label, infra, group string) string {
	id := e.project(infra, group)
	e.exec(`UPDATE projects SET name = $2 WHERE id = $1::uuid`, id, label+e.sfx)
	return id
}

func (e *projEnv) person(name, role string) *users.User {
	return &users.User{ID: e.user(name, role), Role: users.Role(role), FirstName: "T", LastName: "U"}
}

// do ruft einen Seiten-Handler mit Benutzer und Pfadparametern auf.
func (e *projEnv) do(fn http.HandlerFunc, method, target string, u *users.User, params map[string]string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, "user", u)
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

func (e *projEnv) token(u *users.User) string {
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": u.ID, "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("test-secret"))
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func TestProjectsNavNeedsView(t *testing.T) {
	e := newProjEnv(t)
	admin := e.role("vf_padm", map[string]string{"projects.admin": ""})
	inA := e.roleInA
	for _, c := range []struct {
		name, role string
		want       bool
	}{
		{"nur bearbeiten", e.roleNone, false}, // „bearbeiten“ schließt „ansehen“ nicht ein
		{"ansehen überall", e.roleView, true},
		{"ansehen nur in einer Abteilung", inA, true},
		{"Bereichsadministrator", admin, true},
	} {
		u := e.person("vf_n_"+strings.ReplaceAll(c.name, " ", ""), c.role)
		b := &BaseData{Lang: "de", navCan: func(p string) bool { return e.rb.HasPermissionForUser(u.ID, string(u.Role), p) }}
		got := strings.Contains(navKeys(buildNav(b, nil)), "projects")
		if got != c.want {
			t.Errorf("%s: Eintrag Projekte sichtbar=%v, erwartet %v", c.name, got, c.want)
		}
	}
}

func TestProjectsListFiltered(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVLA", e.infraA, "")
	pb := e.named("PVLB", e.infraB, "")
	pn := e.named("PVLN", "", "")
	_, _, _ = pa, pb, pn

	// ohne Recht: nur beteiligte, Überschrift „Meine Projekte“
	none := e.person("vf_l1", e.roleNone)
	mine := e.named("PVLM", e.infraB, "")
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, mine, none.ID)
	body := e.do(e.h.ProjectsPage, "GET", "/projects", none, nil, nil).Body.String()
	if !strings.Contains(body, "Meine Projekte") {
		t.Errorf("Überschrift „Meine Projekte“ fehlt")
	}
	if !strings.Contains(body, "PVLM"+e.sfx) {
		t.Errorf("beteiligtes Projekt fehlt")
	}
	for _, bad := range []string{"PVLA", "PVLB", "PVLN"} {
		if strings.Contains(body, bad+e.sfx) {
			t.Errorf("%s ohne Recht und ohne Beteiligung sichtbar", bad)
		}
	}

	// Recht nur in A: A und das Projekt ohne Abteilung, nicht B
	inA := e.person("vf_l2", e.roleInA)
	body = e.do(e.h.ProjectsPage, "GET", "/projects", inA, nil, nil).Body.String()
	if strings.Contains(body, "Meine Projekte") {
		t.Errorf("mit Recht keine Überschrift „Meine Projekte“")
	}
	for _, want := range []string{"PVLA", "PVLN"} {
		if !strings.Contains(body, want+e.sfx) {
			t.Errorf("%s mit Recht in A fehlt", want)
		}
	}
	for _, bad := range []string{"PVLB", "PVLM"} {
		if strings.Contains(body, bad+e.sfx) {
			t.Errorf("%s mit Recht nur in A sichtbar", bad)
		}
	}

	// Recht überall: alle
	all := e.person("vf_l3", e.roleView)
	body = e.do(e.h.ProjectsPage, "GET", "/projects", all, nil, nil).Body.String()
	for _, want := range []string{"PVLA", "PVLB", "PVLN", "PVLM"} {
		if !strings.Contains(body, want+e.sfx) {
			t.Errorf("%s mit Recht überall fehlt", want)
		}
	}
}

func TestProjectDetailNeedsView(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVDA", e.infraA, "")
	pb := e.named("PVDB", e.infraB, "")

	none := e.person("vf_d1", e.roleNone)
	w := e.do(e.h.ProjectDetail, "GET", "/projects/"+pa, none, map[string]string{"id": pa}, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Keine Berechtigung") {
		t.Errorf("ohne Recht und unbeteiligt: %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PVDA") {
		t.Errorf("Name im Fehlertext")
	}
	// beteiligt: 200
	e.exec(`UPDATE projects SET responsible_to = $2::uuid WHERE id = $1::uuid`, pa, none.ID)
	w = e.do(e.h.ProjectDetail, "GET", "/projects/"+pa, none, map[string]string{"id": pa}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "PVDA"+e.sfx) {
		t.Errorf("beteiligt: %d", w.Code)
	}
	// Recht nur in A: A ja, B nein
	inA := e.person("vf_d2", e.roleInA)
	if w := e.do(e.h.ProjectDetail, "GET", "/projects/"+pa, inA, map[string]string{"id": pa}, nil); w.Code != http.StatusOK {
		t.Errorf("Recht in A, Projekt in A: %d", w.Code)
	}
	if w := e.do(e.h.ProjectDetail, "GET", "/projects/"+pb, inA, map[string]string{"id": pb}, nil); w.Code != http.StatusForbidden {
		t.Errorf("Recht in A, Projekt in B: %d", w.Code)
	}
	// Recht überall
	all := e.person("vf_d3", e.roleView)
	if w := e.do(e.h.ProjectDetail, "GET", "/projects/"+pb, all, map[string]string{"id": pb}, nil); w.Code != http.StatusOK {
		t.Errorf("Recht überall: %d", w.Code)
	}
}

func TestProjectAPIViewRights(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVAA", e.infraA, "")
	pb := e.named("PVAB", e.infraB, "")
	list := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, id := range []string{pa, pb} {
			out = append(out, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	api := e.h.APIDepartmentScope(list)
	call := func(u *users.User, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+e.token(u))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	ids := func(w *httptest.ResponseRecorder) string {
		var got struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("Antwort: %v %s", err, w.Body.String())
		}
		var s []string
		for _, d := range got.Data {
			s = append(s, d.ID)
		}
		return strings.Join(s, ",")
	}

	// Benutzer ohne Abteilungssperre und ohne Ansichtsrecht
	none := e.person("vf_a1", e.roleNone)
	if w := call(none, "/api/v1/projects/"+pa); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"kein zugriff"`) {
		t.Errorf("einzeln ohne Recht: %d %s", w.Code, w.Body.String())
	}
	if got := ids(call(none, "/api/v1/projects")); got != "" {
		t.Errorf("Liste ohne Recht: %q", got)
	}
	// beteiligt: einzeln ja, Liste nur dieses Projekt
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, pb, none.ID)
	if w := call(none, "/api/v1/projects/"+pb); w.Code != http.StatusOK {
		t.Errorf("einzeln beteiligt: %d", w.Code)
	}
	if got := ids(call(none, "/api/v1/projects")); got != pb {
		t.Errorf("Liste beteiligt: %q, erwartet %q", got, pb)
	}
	// Recht in A
	inA := e.person("vf_a2", e.roleInA)
	if w := call(inA, "/api/v1/projects/"+pa); w.Code != http.StatusOK {
		t.Errorf("einzeln in A: %d", w.Code)
	}
	if w := call(inA, "/api/v1/projects/"+pb); w.Code != http.StatusForbidden {
		t.Errorf("einzeln in B mit Recht nur in A: %d", w.Code)
	}
	if got := ids(call(inA, "/api/v1/projects")); got != pa {
		t.Errorf("Liste Recht in A: %q, erwartet %q", got, pa)
	}
	// Recht überall: unverändert alles
	all := e.person("vf_a3", e.roleView)
	if got := ids(call(all, "/api/v1/projects")); got != pa+","+pb {
		t.Errorf("Liste Recht überall: %q", got)
	}
	if w := call(all, "/api/v1/projects/"+pb); w.Code != http.StatusOK {
		t.Errorf("einzeln Recht überall: %d", w.Code)
	}
}

// Bearbeiten ohne Ansehen (Review Focus 1): Schnittstelle und Datensatz-Bausteine lehnen ab.
func TestProjectEditNeedsView(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVEA", e.infraA, "")
	var reached int
	gate := e.h.PermissionGateMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	post := func(u *users.User, path string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(""))
		r.Header.Set("Authorization", "Bearer "+e.token(u))
		w := httptest.NewRecorder()
		before := reached
		gate.ServeHTTP(w, r)
		if reached > before {
			return http.StatusOK
		}
		return w.Code
	}
	paths := []string{
		"/api/v1/projects/" + pa + "/edit",
		"/records/project/" + pa + "/people",
		"/records/project/" + pa + "/group",
		"/records/project/" + pa + "/parties",
	}
	none := e.person("vf_e1", e.roleNone)
	for _, p := range paths {
		if got := post(none, p); got != http.StatusForbidden {
			t.Errorf("bearbeiten ohne ansehen, unbeteiligt %s: %d", p, got)
		}
	}
	editView := e.person("vf_e2", e.roleEdit)
	for _, p := range paths {
		if got := post(editView, p); got != http.StatusOK {
			t.Errorf("bearbeiten mit ansehen %s: %d", p, got)
		}
	}
	// beteiligt (ohne Bearbeiten-Recht): wie bisher durchgelassen
	viewer := e.person("vf_e3", e.roleNone)
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, pa, viewer.ID)
	for _, p := range paths {
		if got := post(viewer, p); got != http.StatusOK {
			t.Errorf("beteiligt %s: %d", p, got)
		}
	}
	// nur ansehen, kein Bearbeiten, unbeteiligt: weiterhin abgelehnt
	onlyView := e.person("vf_e4", e.roleView)
	for _, p := range paths {
		if got := post(onlyView, p); got != http.StatusForbidden {
			t.Errorf("nur ansehen %s: %d", p, got)
		}
	}
	// Anlegen betrifft das Ansehen nicht
	if got := post(none, "/api/v1/projects/"); got != http.StatusOK {
		t.Errorf("anlegen: %d", got)
	}
}

func TestRecordsBlocksProjectView(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVRA", e.infraA, "")
	rt := chi.NewRouter()
	rt.Get("/records/fields/{module}/{id}", e.h.RecordFieldsWeb)
	rt.Get("/records/links/{module}/{id}", e.h.RecordLinksWeb)
	rt.Get("/records/meta/{module}/{id}", e.h.RecordMetaWeb)
	rt.Get("/records/history/{module}/{id}", e.h.RecordHistoryWeb)
	rt.Get("/records/{refType}/{id}/parties", e.h.RecordPartiesWeb)
	get := func(u *users.User, path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), "user", u))
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, r)
		return w.Code
	}
	paths := []string{
		"/records/fields/project/" + pa, "/records/links/project/" + pa,
		"/records/meta/project/" + pa, "/records/history/project/" + pa,
		"/records/project/" + pa + "/parties",
	}
	none := e.person("vf_r1", e.roleNone)
	for _, p := range paths {
		if got := get(none, p); got != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", p, got)
		}
	}
	e.exec(`UPDATE projects SET created_by = $2::uuid WHERE id = $1::uuid`, pa, none.ID)
	for _, p := range paths {
		if got := get(none, p); got == http.StatusForbidden {
			t.Errorf("%s beteiligt: 403", p)
		}
	}
	inB := e.person("vf_r2", e.roleInA)
	pb := e.named("PVRB", e.infraB, "")
	if got := get(inB, "/records/history/project/"+pb); got != http.StatusForbidden {
		t.Errorf("Historie, Recht nur in A, Projekt in B: %d", got)
	}
	if got := get(inB, "/records/history/project/"+pa); got != http.StatusOK {
		t.Errorf("Historie, Recht in A, Projekt in A: %d", got)
	}
}

func TestCategoriesHidesWithoutView(t *testing.T) {
	e := newProjEnv(t)
	pa := e.named("PVCP", e.infraA, "")
	ka := e.kvp(e.infraA)
	e.exec(`UPDATE kvp_ideas SET title = $2 WHERE id = $1::uuid`, ka, "PVCK"+e.sfx)
	cat := e.id(`INSERT INTO categories (name, color, icon) VALUES ($1, '#112233', 'ti-hash') RETURNING id::text`, "vfcat"+e.sfx)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM record_categories WHERE category_id = $1::uuid`, cat)
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM categories WHERE id = $1::uuid`, cat)
	})
	e.exec(`INSERT INTO record_categories (module, record_id, category_id) VALUES ('project', $1::uuid, $3::uuid), ('kvp', $2::uuid, $3::uuid)`, pa, ka, cat)

	page := func(u *users.User) string {
		return e.do(e.h.CategoriesPage, "GET", "/categories?c="+cat, u, nil, nil).Body.String()
	}
	none := e.person("vf_c1", e.roleNone)
	body := page(none)
	for _, bad := range []string{"PVCP" + e.sfx, "PVCK" + e.sfx} {
		if strings.Contains(body, bad) {
			t.Errorf("%s ohne Recht und ohne Beteiligung sichtbar", bad)
		}
	}
	// beteiligt: der eigene Vorgang erscheint
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, pa, none.ID)
	e.exec(`UPDATE kvp_ideas SET submitter_id = $2::uuid WHERE id = $1::uuid`, ka, none.ID)
	body = page(none)
	for _, want := range []string{"PVCP" + e.sfx, "PVCK" + e.sfx} {
		if !strings.Contains(body, want) {
			t.Errorf("%s als Beteiligter fehlt", want)
		}
	}
	// Recht für die Abteilung des Datensatzes: sichtbar; anderes Recht (Abteilung B): nicht
	inA := e.person("vf_c2", e.roleInA)
	if body := page(inA); !strings.Contains(body, "PVCP"+e.sfx) {
		t.Errorf("Projekt mit Recht in A fehlt")
	}
	kvpRoleB := e.role("vf_kb", map[string]string{"projects.view": e.deptB, "kvp.view": e.deptB})
	inB := e.person("vf_c3", kvpRoleB)
	body = page(inB)
	for _, bad := range []string{"PVCP" + e.sfx, "PVCK" + e.sfx} {
		if strings.Contains(body, bad) {
			t.Errorf("%s mit Recht nur in B sichtbar", bad)
		}
	}
	kvpRoleA := e.role("vf_ka", map[string]string{"kvp.view": ""})
	if body := page(e.person("vf_c4", kvpRoleA)); !strings.Contains(body, "PVCK"+e.sfx) || strings.Contains(body, "PVCP"+e.sfx) {
		t.Errorf("kvp.view überall: KVP ja, Projekt nein")
	}
}
