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

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/users"
	"pdh/internal/modules/it"
	"pdh/pkg/response"
)

// itPermEnv: zwei Abteilungen (A mit Unterabteilung A2, B) mit je einer Anlage und einem IT-Asset, ein
// Asset ohne Anlage, dazu Personen mit Recht nur in A, nur it.edit in A, überall, ohne Recht, nur in A2,
// als Beteiligte am Asset in B.
type itPermEnv struct {
	h                                  *Handler
	pool                               *pgxpool.Pool
	sfx                                string
	deptA, deptB                       string
	infraA, infraA2, infraB            string
	assetA, assetA2, assetB, assetN    string
	nameA, nameA2, nameB, nameN        string
	viewA, editA, viewAll, nobody      string
	adminA, viewSub, involved, editAll string
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
	e := &itPermEnv{h: h, pool: pool, sfx: sfx}
	e.deptA = id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITP A "+sfx)
	deptA2 := id(`INSERT INTO departments (name, parent_id) VALUES ($1, $2::uuid) RETURNING id::text`, "ITP A2 "+sfx, e.deptA)
	e.deptB = id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITP B "+sfx)
	infra := func(name, dept string) string {
		return id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, name+" "+sfx, dept)
	}
	e.infraA, e.infraA2, e.infraB = infra("ITP-Anlage A", e.deptA), infra("ITP-Anlage A2", deptA2), infra("ITP-Anlage B", e.deptB)
	asset := func(name, infra string) (string, string) {
		name += " " + sfx
		return id(`INSERT INTO it_assets (name, type, status, created_by, infrastructure_id) VALUES ($1, 'server', 'active', $2::uuid, NULLIF($3, '')::uuid) RETURNING id::text`,
			name, pdhSystemUserID, infra), name
	}
	e.assetA, e.nameA = asset("ITP-Asset-A", e.infraA)
	e.assetA2, e.nameA2 = asset("ITP-Asset-A2", e.infraA2)
	e.assetB, e.nameB = asset("ITP-Asset-B", e.infraB)
	e.assetN, e.nameN = asset("ITP-Asset-N", "")
	user := func(name string) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', 'Ina', 'Test', 'viewer', '', '') RETURNING id::text`, name+sfx)
	}
	e.viewA, e.editA, e.viewAll, e.nobody, e.adminA = user("itpva"), user("itpea"), user("itpall"), user("itpnone"), user("itpadm")
	e.viewSub, e.involved, e.editAll = user("itpsub"), user("itpinv"), user("itpeall")
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
	grant(e.viewA, "it.view", e.deptA)
	deny(e.viewA, "it.edit")
	grant(e.viewA, "directory.edit", "") // für die Partner-Zuweisung
	grant(e.viewA, "tickets.view", "")
	grant(e.editA, "it.edit", e.deptA)
	deny(e.editA, "it.view")
	grant(e.viewAll, "it.view", "")
	grant(e.viewAll, "directory.view", "")
	deny(e.nobody, "it.view")
	deny(e.nobody, "it.edit")
	grant(e.adminA, "it.admin", e.deptA) // schließt it.view und it.edit in A ein (ohne Einzelverbot)
	grant(e.viewSub, "it.view", deptA2)
	grant(e.editAll, "it.edit", "")
	// Beteiligte: ohne jedes IT-Recht, dem Asset in B zugewiesen
	deny(e.involved, "it.view")
	deny(e.involved, "it.edit")
	_, err := pool.Exec(ctx, `UPDATE it_assets SET assigned_to = $2::uuid WHERE id = $1::uuid`, e.assetB, e.involved)
	must(err)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE title LIKE $1`, "ITP-%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE id IN ($1::uuid, $2::uuid, $3::uuid, $4::uuid) OR name LIKE $5`, e.assetA, e.assetA2, e.assetB, e.assetN, "ITP-Neu%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM business_partners WHERE name LIKE $1`, "ITP-Partner%"+sfx)
		for _, u := range []string{e.viewA, e.editA, e.viewAll, e.nobody, e.adminA, e.viewSub, e.involved, e.editAll} {
			_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, u)
		}
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, e.infraA, e.infraA2, e.infraB)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, deptA2, e.deptA, e.deptB)
	})
	must(h.rbac.Warm(ctx))
	return e
}

// itPermDo: ein Aufruf als diese Person (Rolle viewer, Rechte aus Einzelberechtigungen); params = weitere Pfad-Parameter.
func itPermDo(fn http.HandlerFunc, method, path string, params map[string]string, uid string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, "user", &users.User{ID: uid, FirstName: "Ina", LastName: "Test", Role: "viewer"})
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

func itPermID(id string) map[string]string { return map[string]string{"id": id} }

func (e *itPermEnv) req(uid string) *http.Request {
	return httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), "user", &users.User{ID: uid, Role: "viewer"}))
}

func TestITPermListeKennzahlenUndUnterabteilung(t *testing.T) {
	e := newITPermEnv(t)
	// Recht für A: A, die Unterabteilung A2 und Assets ohne Abteilung, nicht B
	for name, uid := range map[string]string{"it.view nur A": e.viewA, "it.edit nur A": e.editA, "it.admin nur A": e.adminA} {
		w := itPermDo(e.h.ITPage, "GET", "/it", nil, uid, nil)
		if w.Code != 200 {
			t.Fatalf("%s: Liste %d %s", name, w.Code, w.Body.String())
		}
		b := w.Body.String()
		if !strings.Contains(b, e.nameA) || !strings.Contains(b, e.nameA2) || !strings.Contains(b, e.nameN) || strings.Contains(b, e.nameB) {
			t.Errorf("%s: Liste zeigt falsche Assets (A=%v A2=%v ohne=%v B=%v)", name, strings.Contains(b, e.nameA), strings.Contains(b, e.nameA2), strings.Contains(b, e.nameN), strings.Contains(b, e.nameB))
		}
	}
	// Recht nur für die Unterabteilung: A2 und ohne Abteilung, nicht A (Vererbung gilt nur abwärts)
	b := itPermDo(e.h.ITPage, "GET", "/it", nil, e.viewSub, nil).Body.String()
	if !strings.Contains(b, e.nameA2) || !strings.Contains(b, e.nameN) || strings.Contains(b, e.nameA+"<") || strings.Contains(b, e.nameB) {
		t.Errorf("Unterabteilung: A2=%v ohne=%v", strings.Contains(b, e.nameA2), strings.Contains(b, e.nameN))
	}
	if w := itPermDo(e.h.ITDetail, "GET", "/it/"+e.assetA, itPermID(e.assetA), e.viewSub, nil); w.Code != http.StatusForbidden {
		t.Errorf("Recht nur in A2: Detail von A: %d, erwartet 403", w.Code)
	}
	if w := itPermDo(e.h.ITDetail, "GET", "/it/"+e.assetA2, itPermID(e.assetA2), e.viewA, nil); w.Code != 200 {
		t.Errorf("Recht in A: Detail in Unterabteilung A2: %d, erwartet 200", w.Code)
	}
	// Typfilter filtert genauso
	if strings.Contains(itPermDo(e.h.ITPage, "GET", "/it?type=server", nil, e.viewA, nil).Body.String(), e.nameB) {
		t.Error("Typfilter zeigt B")
	}
	// globales Recht: alles (Regression)
	b = itPermDo(e.h.ITPage, "GET", "/it", nil, e.viewAll, nil).Body.String()
	for _, n := range []string{e.nameA, e.nameA2, e.nameB, e.nameN} {
		if !strings.Contains(b, n) {
			t.Errorf("globales it.view: %q fehlt", n)
		}
	}
	// ohne Recht und ohne Beteiligung: 403
	if w := itPermDo(e.h.ITPage, "GET", "/it", nil, e.nobody, nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: Liste %d, erwartet 403", w.Code)
	}
	// Kennzahlen: nur sichtbare Assets, unabhängig von der Zahl fremder Assets in der Datenbank
	stats := func(uid string) map[string]int {
		all, err := e.h.it.List(context.Background(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		return itStatsOf(all, e.h.itVisibleIDs(e.req(uid)))
	}
	if a, all := stats(e.viewA), stats(e.viewAll); a["total"] >= all["total"] {
		t.Errorf("Kennzahlen: A %d muss kleiner sein als alle %d", a["total"], all["total"])
	}
	vis := e.h.itVisibleIDs(e.req(e.viewA))
	if vis[e.assetB] || !vis[e.assetA] || !vis[e.assetA2] || !vis[e.assetN] {
		t.Errorf("sichtbare IDs: %v", vis)
	}
	if e.h.itVisibleIDs(e.req(e.viewAll)) != nil {
		t.Error("globales Recht: keine Einschränkung erwartet (nil)")
	}
}

func TestITPermBeteiligteSehenIhrAsset(t *testing.T) {
	e := newITPermEnv(t)
	// ohne IT-Recht, aber dem Asset in B zugewiesen: Seite offen, Liste nur dieses Asset
	w := itPermDo(e.h.ITPage, "GET", "/it", nil, e.involved, nil)
	if w.Code != 200 {
		t.Fatalf("Beteiligte: Liste %d", w.Code)
	}
	b := w.Body.String()
	if !strings.Contains(b, e.nameB) || strings.Contains(b, e.nameA) || strings.Contains(b, e.nameA2) || strings.Contains(b, e.nameN) {
		t.Errorf("Beteiligte sehen genau ihr Asset (B=%v, andere=%v)", strings.Contains(b, e.nameB), strings.Contains(b, e.nameA))
	}
	for asset, want := range map[string]int{e.assetB: 200, e.assetA: 403, e.assetN: 403} {
		if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, itPermID(asset), e.involved, nil); w.Code != want {
			t.Errorf("Beteiligte: Detail %s: %d, erwartet %d", asset, w.Code, want)
		}
		if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+asset, itPermID(asset), e.involved, nil); w.Code != want {
			t.Errorf("Beteiligte: Asset-Info %s: %d, erwartet %d", asset, w.Code, want)
		}
	}
	// mit Recht nur für A bleibt das zugewiesene Asset in B sichtbar
	if _, err := e.pool.Exec(context.Background(), `UPDATE it_assets SET assigned_to = $2::uuid WHERE id = $1::uuid`, e.assetB, e.viewA); err != nil {
		t.Fatal(err)
	}
	if w := itPermDo(e.h.ITDetail, "GET", "/it/"+e.assetB, itPermID(e.assetB), e.viewA, nil); w.Code != 200 {
		t.Errorf("Recht nur A, aber zugewiesen: Detail B %d, erwartet 200", w.Code)
	}
	// ohne Recht und ohne Beteiligung: auch die Asset-Info ist gesperrt
	if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+e.assetN, itPermID(e.assetN), e.nobody, nil); w.Code != http.StatusForbidden {
		t.Errorf("nobody: Asset-Info %d", w.Code)
	}
}

func TestITPermDetailAssetInfoUndEinzelpruefung(t *testing.T) {
	e := newITPermEnv(t)
	for name, uid := range map[string]string{"view A": e.viewA, "edit A": e.editA, "admin A": e.adminA} {
		for asset, want := range map[string]int{e.assetA: 200, e.assetN: 200, e.assetB: http.StatusForbidden} {
			if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, itPermID(asset), uid, nil); w.Code != want {
				t.Errorf("%s: Detail %s: %d, erwartet %d", name, asset, w.Code, want)
			}
			if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+asset, itPermID(asset), uid, nil); w.Code != want {
				t.Errorf("%s: Asset-Info %s: %d, erwartet %d", name, asset, w.Code, want)
			}
		}
	}
	for _, asset := range []string{e.assetA, e.assetB, e.assetN} { // globales Recht: überall (Regression)
		if w := itPermDo(e.h.ITDetail, "GET", "/it/"+asset, itPermID(asset), e.viewAll, nil); w.Code != 200 {
			t.Errorf("global: Detail %s: %d", asset, w.Code)
		}
		if w := itPermDo(e.h.AssetInfoPage, "GET", "/a/"+asset, itPermID(asset), e.viewAll, nil); w.Code != 200 {
			t.Errorf("global: Asset-Info %s: %d", asset, w.Code)
		}
	}
	if w := itPermDo(e.h.ITDetail, "GET", "/it/"+e.assetA, itPermID(e.assetA), e.nobody, nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: Detail %d", w.Code)
	}
	// App-Scan, Etiketten, Neu anlegen nutzen canViewITRec
	if !e.h.canViewITRec(e.req(e.viewA), e.assetA) || e.h.canViewITRec(e.req(e.viewA), e.assetB) || !e.h.canViewITRec(e.req(e.viewA), e.assetN) {
		t.Error("canViewITRec: nur A und ohne Abteilung")
	}
	if !e.h.canViewITRec(e.req(e.viewAll), e.assetB) {
		t.Error("canViewITRec: globales Recht sieht B")
	}
}

func TestITPermEtikettenVerknuepfungenZuordnenUndNeuAnlegen(t *testing.T) {
	e := newITPermEnv(t)
	ctx := context.Background()
	// Etiketten: „alle IT-Assets“ nur sichtbare, ein fremdes einzelnes abgelehnt
	items, err := e.h.assetLabelItems(e.req(e.viewA), url.Values{"it_all": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, li := range items {
		names = append(names, li.LocationLeaf)
	}
	all := strings.Join(names, "|")
	if !strings.Contains(all, "ITP-Asset-A "+e.sfx) || strings.Contains(all, "ITP-Asset-B") {
		t.Errorf("Etiketten it_all: %v", names)
	}
	if _, err := e.h.assetLabelItems(e.req(e.viewA), url.Values{"it": {e.assetB}}); err == nil {
		t.Error("Etikett für ein Asset aus B soll abgelehnt werden")
	}
	if _, err := e.h.assetLabelItems(e.req(e.viewA), url.Values{"it": {e.assetA}}); err != nil {
		t.Errorf("Etikett für A: %v", err)
	}
	// Verknüpfungen im Vorgang: das IT-Asset des Tickets nur, wenn sichtbar
	tk := func(name, asset string) string {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO tickets (title, created_by, it_asset_id) VALUES ($1, $2::uuid, $3::uuid) RETURNING id::text`, "ITP-"+name+" "+e.sfx, e.viewA, asset).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tA, tB := tk("TA", e.assetA), tk("TB", e.assetB)
	links := func(ticket string) string {
		return itPermDo(e.h.RecordLinksWeb, "GET", "/records/links/ticket/"+ticket, map[string]string{"module": "ticket", "id": ticket}, e.viewA, nil).Body.String()
	}
	if b := links(tA); !strings.Contains(b, e.nameA) {
		t.Errorf("Verknüpfungen: Asset in A fehlt: %.200s", b)
	}
	if b := links(tB); strings.Contains(b, e.nameB) {
		t.Error("Verknüpfungen zeigen das Asset aus B")
	}
	// Zuordnen am Vorgang (RecordITAssetWeb): nur sichtbare Assets
	assign := func(ticket, asset string) int {
		return itPermDo(e.h.RecordITAssetWeb, "POST", "/records/ticket/"+ticket+"/it-asset", map[string]string{"refType": "ticket", "id": ticket}, e.viewA, url.Values{"it_asset_id": {asset}}).Code
	}
	if c := assign(tA, e.assetB); c != http.StatusForbidden {
		t.Errorf("Zuordnen von B: %d, erwartet 403", c)
	}
	if c := assign(tA, e.assetA); c != 200 {
		t.Errorf("Zuordnen von A: %d, erwartet 200", c)
	}
	// Neu anlegen mit voreingestelltem Asset: Name nur für sichtbare
	page := func(asset string) string {
		return itPermDo(e.h.AssignmentNewPage, "GET", "/assignments/new?it="+asset, nil, e.viewA, nil).Body.String()
	}
	if b := page(e.assetA); !strings.Contains(b, e.nameA) {
		t.Error("Neu anlegen: Asset in A fehlt")
	}
	if b := page(e.assetB); strings.Contains(b, e.nameB) {
		t.Error("Neu anlegen zeigt den Namen des Assets aus B")
	}
}

func TestITPermPartnerSeiteZaehlerUndZuweisung(t *testing.T) {
	e := newITPermEnv(t)
	ctx := context.Background()
	partner := func(name string) string {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO business_partners (name, kind) VALUES ($1, 'supplier') RETURNING id::text`, name+" "+e.sfx).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pid := partner("ITP-Partner")
	if _, err := e.pool.Exec(ctx, `UPDATE it_assets SET supplier_id = $1::uuid WHERE id IN ($2::uuid, $3::uuid)`, pid, e.assetA, e.assetB); err != nil {
		t.Fatal(err)
	}
	// Partnerseite: Liste der Assets nur sichtbare; Zähler der Partnerliste ebenso
	b := itPermDo(e.h.PartnerDetail, "GET", "/directory/"+pid, itPermID(pid), e.viewA, nil).Body.String()
	if !strings.Contains(b, e.nameA) || strings.Contains(b, e.nameB) {
		t.Errorf("Partnerseite: A=%v B=%v", strings.Contains(b, e.nameA), strings.Contains(b, e.nameB))
	}
	count := func(uid string) int {
		var n int
		vis := e.h.itVisibleIDs(e.req(uid))
		ids := make([]string, 0, len(vis))
		for id := range vis {
			ids = append(ids, id)
		}
		if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM it_assets a WHERE a.supplier_id = $1::uuid AND (NOT $2::boolean OR a.id::text = ANY($3::text[]))`, pid, vis != nil, ids).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(e.viewA) != 1 || count(e.viewAll) != 2 {
		t.Errorf("Zähler: A %d (erwartet 1), alle %d (erwartet 2)", count(e.viewA), count(e.viewAll))
	}
	rows := partnerITVisible(e.h.itVisibleIDs(e.req(e.viewA)), []PartnerITRow{{ID: e.assetA}, {ID: e.assetB}})
	if len(rows) != 1 || rows[0].ID != e.assetA {
		t.Errorf("partnerITVisible: %v", rows)
	}
	// Zuweisen und Lösen mit target=it: it.edit für genau dieses Asset (hier nur it.view in A: beides 403)
	form := func(asset string) url.Values {
		return url.Values{"target": {"it"}, "role": {"supplier"}, "target_id": {asset}}
	}
	for _, fn := range []http.HandlerFunc{e.h.PartnerAssignWeb, e.h.PartnerUnassignWeb} {
		if w := itPermDo(fn, "POST", "/directory/"+pid+"/assign", itPermID(pid), e.viewA, form(e.assetB)); w.Code != http.StatusForbidden {
			t.Errorf("Zuweisung an B ohne it.edit: %d, erwartet 403", w.Code)
		}
		if w := itPermDo(fn, "POST", "/directory/"+pid+"/assign", itPermID(pid), e.viewA, form("kein-uuid")); w.Code != http.StatusForbidden {
			t.Errorf("Zuweisung mit ungültiger ID: %d, erwartet 403", w.Code)
		}
	}
	// it.edit nur A: directory.edit dazu, Zuweisung in A erlaubt, in B nicht
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1::uuid, p.id, true FROM permissions p WHERE p.key = 'directory.edit'`, e.editA); err != nil {
		t.Fatal(err)
	}
	if err := e.h.rbac.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	if w := itPermDo(e.h.PartnerAssignWeb, "POST", "/directory/"+pid+"/assign", itPermID(pid), e.editA, form(e.assetB)); w.Code != http.StatusForbidden {
		t.Errorf("it.edit nur A: Zuweisung an B %d", w.Code)
	}
	if w := itPermDo(e.h.PartnerAssignWeb, "POST", "/directory/"+pid+"/assign", itPermID(pid), e.editA, form(e.assetA)); w.Code == http.StatusForbidden {
		t.Errorf("it.edit nur A: Zuweisung an A darf nicht gesperrt sein")
	}
}

func TestITPermBearbeitenNurInDerAbteilung(t *testing.T) {
	e := newITPermEnv(t)
	ctx := context.Background()
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
	form := func(extra url.Values) url.Values {
		f := url.Values{"name": {"ITP-Neu x " + e.sfx}, "type": {"server"}}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	// Anlegen: Ziel-Anlage in der Abteilung, ohne Anlage nur mit it.edit überall
	if w := itPermDo(h.ITCreate, "POST", "/it", nil, e.editA, form(url.Values{"infrastructure_id": {e.infraB}})); w.Code != http.StatusForbidden {
		t.Errorf("Anlegen in Anlage von B: %d, erwartet 403", w.Code)
	}
	if w := itPermDo(h.ITCreate, "POST", "/it", nil, e.editA, form(nil)); w.Code != http.StatusForbidden {
		t.Errorf("Anlegen ohne Anlage mit Abteilungsrecht: %d, erwartet 403", w.Code)
	}
	if w := itPermDo(h.ITCreate, "POST", "/it", nil, e.editA, form(url.Values{"infrastructure_id": {e.infraA}})); w.Code != 200 {
		t.Errorf("Anlegen in A: %d, erwartet 200", w.Code)
	}
	if w := itPermDo(h.ITCreate, "POST", "/it", nil, e.editAll, form(nil)); w.Code != 200 {
		t.Errorf("Anlegen ohne Anlage mit globalem it.edit: %d, erwartet 200", w.Code)
	}
	// Verschieben: Ziel in B gesperrt, Anlage leeren nur global
	edit := func(uid, name, infra string) int {
		return itPermDo(h.ITEditWeb, "POST", "/it/"+e.assetA+"/edit-web", itPermID(e.assetA), uid, form(url.Values{"name": {name}, "infrastructure_id": {infra}})).Code
	}
	if c := edit(e.editA, "ITP-Asset-A x", e.infraB); c != http.StatusForbidden {
		t.Errorf("Verschieben nach B: %d, erwartet 403", c)
	}
	if c := edit(e.editA, "ITP-Asset-A x", ""); c != http.StatusForbidden {
		t.Errorf("Anlage leeren mit Abteilungsrecht: %d, erwartet 403", c)
	}
	var infra string
	if err := e.pool.QueryRow(ctx, `SELECT infrastructure_id::text FROM it_assets WHERE id = $1::uuid`, e.assetA).Scan(&infra); err != nil || infra != e.infraA {
		t.Errorf("Asset A bleibt in A: %q %v", infra, err)
	}
	if c := edit(e.editA, "ITP-Asset-A "+e.sfx, e.infraA); c != http.StatusFound {
		t.Errorf("Bearbeiten in A: %d, erwartet 302", c)
	}
	if c := edit(e.editAll, "ITP-Asset-A "+e.sfx, ""); c != http.StatusFound {
		t.Errorf("Anlage leeren mit globalem it.edit: %d, erwartet 302", c)
	}
}

func TestITPermSchnittstelleListeKennzahlenUndAnlegen(t *testing.T) {
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
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		list, _ := h.it.List(r.Context(), "", "")
		response.JSON(w, 200, list)
	})
	api := h.APIDepartmentScope(inner)
	call := func(uid, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
	if got := ids(call(e.viewA, "GET", "/api/v1/it", "")); !got[e.assetA] || !got[e.assetN] || got[e.assetB] {
		t.Errorf("API-Liste it.view nur A: %v", got)
	}
	if got := ids(call(e.viewAll, "GET", "/api/v1/it", "")); !got[e.assetA] || !got[e.assetB] || !got[e.assetN] {
		t.Errorf("API-Liste globales Recht: %v", got)
	}
	if w := call(e.nobody, "GET", "/api/v1/it", ""); w.Code != http.StatusForbidden {
		t.Errorf("API-Liste ohne Recht: %d", w.Code)
	}
	if w := call(e.nobody, "GET", "/api/v1/it/stats", ""); w.Code != http.StatusForbidden {
		t.Errorf("API-Kennzahlen ohne Recht: %d", w.Code)
	}
	total := func(uid string) int {
		w := call(uid, "GET", "/api/v1/it/stats", "")
		var body struct{ Data map[string]int }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("Kennzahlen %d %s", w.Code, w.Body.String())
		}
		return body.Data["total"]
	}
	if a, all := total(e.viewA), total(e.viewAll); a >= all {
		t.Errorf("API-Kennzahlen: A %d muss kleiner sein als alle %d", a, all)
	}
	// Anlegen: Ziel-Anlage wie im Web
	create := func(uid, infra string) int {
		body := `{"name":"ITP-Neu api","type":"server"}`
		if infra != "" {
			body = `{"name":"ITP-Neu api","type":"server","infrastructure_id":"` + infra + `"}`
		}
		return call(uid, "POST", "/api/v1/it", body).Code
	}
	if c := create(e.editA, e.infraB); c != http.StatusForbidden {
		t.Errorf("API anlegen in B: %d, erwartet 403", c)
	}
	if c := create(e.editA, ""); c != http.StatusForbidden {
		t.Errorf("API anlegen ohne Anlage mit Abteilungsrecht: %d, erwartet 403", c)
	}
	if c := create(e.editA, e.infraA); c != http.StatusTeapot {
		t.Errorf("API anlegen in A: %d, erwartet 418 (durchgelassen)", c)
	}
	if c := create(e.editAll, ""); c != http.StatusTeapot {
		t.Errorf("API anlegen ohne Anlage mit globalem it.edit: %d, erwartet 418", c)
	}
}

func TestITPermChatKartenUndSnmpKarte(t *testing.T) {
	e := newITPermEnv(t)
	ctx := context.Background()
	h := e.h
	// Chat-Karte: Name und Link nur für Leser, die das Asset sehen dürfen
	var conv, msg string
	if err := e.pool.QueryRow(ctx, `INSERT INTO chat_conversations (kind, name) VALUES ('group', $1) RETURNING id::text`, "ITP-Chat "+e.sfx).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM chat_conversations WHERE id = $1::uuid`, conv)
	})
	if err := e.pool.QueryRow(ctx, `INSERT INTO chat_messages (conversation_id, body) VALUES ($1::uuid, 'x') RETURNING id::text`, conv).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{e.assetA, e.assetB} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO chat_message_links (message_id, ref_type, ref_id, title) VALUES ($1::uuid, 'it_asset', $2::uuid, $3)`, msg, a, "Titel "+a); err != nil {
			t.Fatal(err)
		}
	}
	links := func(viewer string) map[string]chatLink {
		m := &chatMessage{ID: msg, Mentions: []string{}, Reactions: []chatReaction{}, Files: []chatFile{}, Links: []chatLink{}}
		h.chatEnrich(ctx, viewer, []*chatMessage{m})
		out := map[string]chatLink{}
		for _, l := range m.Links {
			out[l.RefID] = l
		}
		return out
	}
	if l := links(e.viewA); l[e.assetA].Title == "" || l[e.assetA].URL == "" || l[e.assetB].Title != "" || l[e.assetB].URL != "" || l[e.assetB].Label != "IT-Asset" {
		t.Errorf("Karten für Leser mit Recht nur A: %+v", l)
	}
	if l := links(e.nobody); l[e.assetA].Title != "" || l[e.assetA].URL != "" {
		t.Errorf("Karten ohne Recht zeigen Namen oder Link: %+v", l)
	}
	if l := links(e.viewAll); l[e.assetB].Title == "" {
		t.Errorf("globales Recht sieht den Namen: %+v", l)
	}
	if l := links(""); l[e.assetA].Title != "" {
		t.Errorf("Ereignis an alle zeigt Namen: %+v", l)
	}
	// SNMP-Karte: Assetspalten nur für sichtbare Assets
	h.itNet.of(itNetSourceSNMP).name = "snmpitp" + e.sfx
	var profile string
	if err := e.pool.QueryRow(ctx, `INSERT INTO it_snmp_profiles (name) VALUES ($1) RETURNING id::text`, "itp"+e.sfx).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.pool.Exec(c, `DELETE FROM it_snmp_targets WHERE profile_id = $1::uuid`, profile)
		_, _ = e.pool.Exec(c, `DELETE FROM it_snmp_profiles WHERE id = $1::uuid`, profile)
	})
	for i, a := range []string{e.assetA, e.assetB} {
		var tid string
		if err := e.pool.QueryRow(ctx, `INSERT INTO it_snmp_targets (host, port, profile_id) VALUES ($1, 161, $2::uuid) RETURNING id::text`, "10.99.0."+string(rune('1'+i)), profile).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE it_assets SET net_source = $2, net_external_id = $3 WHERE id = $1::uuid`, a, h.itNetSNMPName(), tid); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(uid string) map[string]string {
		c := h.itNetSNMPCard(e.req(uid))
		out := map[string]string{}
		for _, r := range c.Rows {
			out[r.Host] = r.AssetName
		}
		return out
	}
	if m := rows(e.viewA); m["10.99.0.1"] != e.nameA || m["10.99.0.2"] != "" {
		t.Errorf("SNMP-Karte Recht nur A: %v", m)
	}
	if m := rows(e.viewAll); m["10.99.0.2"] != e.nameB {
		t.Errorf("SNMP-Karte globales Recht: %v", m)
	}
}
