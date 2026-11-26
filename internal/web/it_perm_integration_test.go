//go:build integration

// IT-Assets: Rechte mit Abteilungsbindung gelten im Bestand für den Datensatz (it_perm.go).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run ITPerm ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/users"
	"pdh/internal/modules/it"
	"pdh/pkg/response"
)

// itPermEnv: zwei Abteilungen mit je einer Anlage und einem IT-Asset, ein Asset ohne Anlage, dazu
// Personen mit Recht nur in A, nur it.edit in A, überall und ohne Recht.
type itPermEnv struct {
	h                             *Handler
	pool                          *pgxpool.Pool
	infraA, infraB                string
	assetA, assetB, assetN        string
	nameA, nameB, nameN           string
	viewA, editA, viewAll, nobody string
	adminA                        string
}

func newITPermEnv(t *testing.T) *itPermEnv {
	t.Helper()
	h, pool, src := itNetPageEnv(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.TrimPrefix(src, "unifiweb")
	h.hasPermHook = nil
	h.it = it.NewService(it.NewRepository(pool))
	h.users = users.NewService(users.NewRepository(pool), "test-secret", 1)
	h.jwtSecret = "test-secret"
	e := &itPermEnv{h: h, pool: pool}
	deptA := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITP A "+sfx)
	deptB := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITP B "+sfx)
	e.infraA = id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "ITP-Anlage A "+sfx, deptA)
	e.infraB = id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "ITP-Anlage B "+sfx, deptB)
	asset := func(name, infra string) (string, string) {
		name += " " + sfx
		return id(`INSERT INTO it_assets (name, type, status, created_by, infrastructure_id) VALUES ($1, 'server', 'active', $2::uuid, NULLIF($3, '')::uuid) RETURNING id::text`,
			name, pdhSystemUserID, infra), name
	}
	e.assetA, e.nameA = asset("ITP-Asset-A", e.infraA)
	e.assetB, e.nameB = asset("ITP-Asset-B", e.infraB)
	e.assetN, e.nameN = asset("ITP-Asset-N", "")
	user := func(name string) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', 'Ina', 'Test', 'viewer', '', '') RETURNING id::text`, name+sfx)
	}
	e.viewA, e.editA, e.viewAll, e.nobody, e.adminA = user("itpva"), user("itpea"), user("itpall"), user("itpnone"), user("itpadm")
	grant := func(uid, perm, dept string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
			SELECT $1::uuid, p.id, true, NULLIF($3, '')::uuid FROM permissions p WHERE p.key = $2`, uid, perm, dept)
		must(err)
	}
	deny := func(uid, perm string) { // die Rolle „viewer“ soll das Recht hier nicht mitbringen
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted)
			SELECT $1::uuid, p.id, false FROM permissions p WHERE p.key = $2`, uid, perm)
		must(err)
	}
	grant(e.viewA, "it.view", deptA)
	deny(e.viewA, "it.edit")
	grant(e.editA, "it.edit", deptA)
	deny(e.editA, "it.view")
	grant(e.viewAll, "it.view", "")
	deny(e.nobody, "it.view")
	deny(e.nobody, "it.edit")
	grant(e.adminA, "it.admin", deptA) // schließt it.view und it.edit in A ein (ohne Einzelverbot)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE id IN ($1::uuid, $2::uuid, $3::uuid) OR name LIKE $4`, e.assetA, e.assetB, e.assetN, "ITP-Neu%"+sfx)
		for _, u := range []string{e.viewA, e.editA, e.viewAll, e.nobody, e.adminA} {
			_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, u)
		}
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, e.infraA, e.infraB)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, deptA, deptB)
	})
	must(h.rbac.Warm(ctx))
	return e
}

// itPermDo: ein Aufruf als diese Person (Rolle viewer, Rechte aus Einzelberechtigungen).
func itPermDo(fn http.HandlerFunc, method, path, id, uid string, form url.Values) *httptest.ResponseRecorder {
	return itNetDo(fn, method, path, id, uid, form)
}

func TestITPermListeUndSucheNachAbteilung(t *testing.T) {
	e := newITPermEnv(t)
	// Abteilungsrecht A: eigene und Assets ohne Abteilung, nicht die aus B
	for name, uid := range map[string]string{"it.view nur A": e.viewA, "it.edit nur A": e.editA, "it.admin nur A": e.adminA} {
		w := itPermDo(e.h.ITPage, "GET", "/it", "", uid, nil)
		if w.Code != 200 {
			t.Fatalf("%s: Liste %d %s", name, w.Code, w.Body.String())
		}
		b := w.Body.String()
		if !strings.Contains(b, e.nameA) || !strings.Contains(b, e.nameN) || strings.Contains(b, e.nameB) {
			t.Errorf("%s: Liste zeigt falsche Assets (A=%v ohne=%v B=%v)", name, strings.Contains(b, e.nameA), strings.Contains(b, e.nameN), strings.Contains(b, e.nameB))
		}
	}
	// Suche nach Typ filtert genauso
	w := itPermDo(e.h.ITPage, "GET", "/it?type=server", "", e.viewA, nil)
	if strings.Contains(w.Body.String(), e.nameB) {
		t.Error("Typfilter zeigt B")
	}
	// globales Recht: alles (Regression)
	b := itPermDo(e.h.ITPage, "GET", "/it", "", e.viewAll, nil).Body.String()
	if !strings.Contains(b, e.nameA) || !strings.Contains(b, e.nameB) || !strings.Contains(b, e.nameN) {
		t.Error("globales it.view soll alle Assets sehen")
	}
	// ohne Recht: 403
	if w := itPermDo(e.h.ITPage, "GET", "/it", "", e.nobody, nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: Liste %d, erwartet 403", w.Code)
	}
	// Kennzahlen zählen nur sichtbare Assets: A-Person sieht weniger als die Person mit globalem Recht
	statsOf := func(uid string) map[string]int {
		req := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), "user", &users.User{ID: uid, Role: "viewer"}))
		all, err := e.h.it.List(req.Context(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		return itStatsOf(all, e.h.itVisibleIDs(req))
	}
	if a, all := statsOf(e.viewA), statsOf(e.viewAll); a["total"] != all["total"]-1 {
		t.Errorf("Kennzahlen: A %d, alle %d (B fehlt genau einmal)", a["total"], all["total"])
	}
	if !strings.Contains(itPermDo(e.h.ITPage, "GET", "/it", "", e.viewA, nil).Body.String(), "ITP-Asset-A") {
		t.Error("Seite zeigt A")
	}
}

func TestITPermDetailAssetInfoUndScan(t *testing.T) {
	e := newITPermEnv(t)
	for name, uid := range map[string]string{"view A": e.viewA, "edit A": e.editA, "admin A": e.adminA} {
		for asset, want := range map[string]int{e.assetA: 200, e.assetN: 200, e.assetB: http.StatusForbidden} {
			if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, asset, uid, nil); w.Code != want {
				t.Errorf("%s: Detail %s: %d, erwartet %d", name, asset, w.Code, want)
			}
			if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+asset, asset, uid, nil); w.Code != want {
				t.Errorf("%s: Asset-Info %s: %d, erwartet %d", name, asset, w.Code, want)
			}
		}
	}
	// globales Recht: überall (Regression)
	for _, asset := range []string{e.assetA, e.assetB, e.assetN} {
		if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, asset, e.viewAll, nil); w.Code != 200 {
			t.Errorf("global: Detail %s: %d", asset, w.Code)
		}
		if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+asset, asset, e.viewAll, nil); w.Code != 200 {
			t.Errorf("global: Asset-Info %s: %d", asset, w.Code)
		}
	}
	// ohne Recht nirgends
	for _, asset := range []string{e.assetA, e.assetN} {
		if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, asset, e.nobody, nil); w.Code != http.StatusForbidden {
			t.Errorf("ohne Recht: Detail %s: %d", asset, w.Code)
		}
	}
	// die gemeinsame Prüfung (App-Scan, Etiketten, Verknüpfungen, Neu anlegen) nutzt canViewITRec
	req := func(uid string) *http.Request {
		return httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), "user", &users.User{ID: uid, Role: "viewer"}))
	}
	if !e.h.canViewITRec(req(e.viewA), e.assetA) || e.h.canViewITRec(req(e.viewA), e.assetB) || !e.h.canViewITRec(req(e.viewA), e.assetN) {
		t.Error("canViewITRec: nur A und ohne Abteilung")
	}
	if !e.h.canViewITRec(req(e.viewAll), e.assetB) {
		t.Error("canViewITRec: globales Recht sieht B")
	}
	// Etiketten: „alle IT-Assets“ und einzelne fremde
	q := url.Values{"it_all": {"1"}}
	items, err := e.h.assetLabelItems(req(e.viewA), q)
	if err != nil {
		t.Fatal(err)
	}
	for _, li := range items {
		if strings.Contains(li.LocationLeaf, "ITP-Asset-B") {
			t.Error("Etiketten zeigen B")
		}
	}
	if _, err := e.h.assetLabelItems(req(e.viewA), url.Values{"it": {e.assetB}}); err == nil {
		t.Error("Etikett für ein Asset aus B soll abgelehnt werden")
	}
}

func TestITPermBearbeitenNurInDerAbteilung(t *testing.T) {
	e := newITPermEnv(t)
	ctx := context.Background()
	// Die Sperre (PermissionGateMiddleware) prüft it.edit je Datensatz; hier genau wie im Betrieb
	// mit Anmelde-Token. Alle Schreibrouten am Asset: edit-web, status-web, net-*.
	h := e.h
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	gate := h.PermissionGateMiddleware(ok)
	tok := func(uid string) *http.Cookie {
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(h.jwtSecret))
		return &http.Cookie{Name: "pdh_token", Value: s}
	}
	post := func(uid, path string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader("")).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(tok(uid))
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	for _, route := range []string{"/edit-web", "/status-web", "/net-monitor", "/net-detach", "/net-reattach", "/net-grace"} {
		if c := post(e.editA, "/it/"+e.assetB+route); c != http.StatusForbidden {
			t.Errorf("it.edit nur A: %s an B: %d, erwartet 403", route, c)
		}
		if c := post(e.editA, "/it/"+e.assetA+route); c != http.StatusTeapot {
			t.Errorf("it.edit nur A: %s an A: %d, erwartet 418 (durchgelassen)", route, c)
		}
		if c := post(e.viewA, "/it/"+e.assetA+route); c != http.StatusForbidden {
			t.Errorf("nur it.view: %s an A: %d, erwartet 403", route, c)
		}
	}
	if c := post(e.editA, "/api/v1/it/"+e.assetB+"/status"); c != http.StatusForbidden {
		t.Errorf("API-Status an B: %d, erwartet 403", c)
	}
	// Anlegen und Verschieben: auch die Ziel-Anlage muss in der Abteilung liegen
	form := func(extra url.Values) url.Values {
		f := url.Values{"name": {"ITP-Neu x"}, "type": {"server"}}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	if w := itPermDo(h.ITCreate, "POST", "/it", "", e.editA, form(url.Values{"infrastructure_id": {e.infraB}})); w.Code != http.StatusForbidden {
		t.Errorf("Anlegen in Anlage von B: %d, erwartet 403", w.Code)
	}
	if w := itPermDo(h.ITEditWeb, "POST", "/it/"+e.assetA+"/edit-web", e.assetA, e.editA, form(url.Values{"name": {"ITP-Asset-A umbenannt"}, "infrastructure_id": {e.infraB}})); w.Code != http.StatusForbidden {
		t.Errorf("Verschieben nach B: %d, erwartet 403", w.Code)
	}
	var infra string
	if err := e.pool.QueryRow(ctx, `SELECT infrastructure_id::text FROM it_assets WHERE id = $1::uuid`, e.assetA).Scan(&infra); err != nil || infra != e.infraA {
		t.Errorf("Asset A bleibt in A: %q %v", infra, err)
	}
	if w := itPermDo(h.ITEditWeb, "POST", "/it/"+e.assetA+"/edit-web", e.assetA, e.editA, form(url.Values{"name": {"ITP-Asset-A neu"}, "infrastructure_id": {e.infraA}})); w.Code != http.StatusFound {
		t.Errorf("Bearbeiten in A: %d, erwartet 302", w.Code)
	}
}

func TestITPermSchnittstelleListeUndKennzahlen(t *testing.T) {
	e := newITPermEnv(t)
	h := e.h
	tok := func(uid string) string {
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(h.jwtSecret))
		return s
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stats") {
			stats, _ := h.it.GetStats(r.Context())
			response.JSON(w, 200, stats)
			return
		}
		list, _ := h.it.List(r.Context(), "", "")
		response.JSON(w, 200, list)
	})
	api := h.APIDepartmentScope(inner)
	get := func(uid, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tok(uid))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	ids := func(w *httptest.ResponseRecorder) map[string]bool {
		var body struct{ Data []struct{ ID string } }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("Antwort: %v %s", err, w.Body.String())
		}
		m := map[string]bool{}
		for _, a := range body.Data {
			m[a.ID] = true
		}
		return m
	}
	w := get(e.viewA, "/api/v1/it")
	if got := ids(w); !got[e.assetA] || !got[e.assetN] || got[e.assetB] {
		t.Errorf("API-Liste it.view nur A: %v", got)
	}
	if got := ids(get(e.viewAll, "/api/v1/it")); !got[e.assetA] || !got[e.assetB] || !got[e.assetN] {
		t.Errorf("API-Liste globales Recht: %v", got)
	}
	if w := get(e.nobody, "/api/v1/it"); w.Code != http.StatusForbidden {
		t.Errorf("API-Liste ohne Recht: %d", w.Code)
	}
	if w := get(e.nobody, "/api/v1/it/stats"); w.Code != http.StatusForbidden {
		t.Errorf("API-Kennzahlen ohne Recht: %d", w.Code)
	}
	statsTotal := func(uid string) int {
		w := get(uid, "/api/v1/it/stats")
		var body struct{ Data map[string]int }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("Kennzahlen %d %s", w.Code, w.Body.String())
		}
		return body.Data["total"]
	}
	if a, all := statsTotal(e.viewA), statsTotal(e.viewAll); a != all-1 {
		t.Errorf("API-Kennzahlen: A %d, alle %d", a, all)
	}
}
