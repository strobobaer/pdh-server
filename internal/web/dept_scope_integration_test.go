//go:build integration

// Integrationstest Abteilungsrechte gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run DeptScope ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

func TestDeptScopeIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(q string, args ...interface{}) string {
		t.Helper()
		var v string
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	us := users.NewService(users.NewRepository(pool), "test-secret", 1)
	h := &Handler{db: pool, rbac: rb, users: us, jwtSecret: "test-secret"}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")

	// Vorbelegung aus der Migration
	var globals int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM departments WHERE is_global AND lower(name) IN ('instandhaltung','it','office','gl')`).Scan(&globals))
	if globals != 4 {
		t.Fatalf("übergeordnete Abteilungen: %d statt 4", globals)
	}

	prodA := id(`INSERT INTO departments (name) VALUES ('Produktion A '||$1::text) RETURNING id::text`, sfx)
	linie1 := id(`INSERT INTO departments (name, parent_id) VALUES ('Linie 1 '||$1::text, $2::uuid) RETURNING id::text`, sfx, prodA)
	prodB := id(`INSERT INTO departments (name) VALUES ('Produktion B '||$1::text) RETURNING id::text`, sfx)
	ih := id(`SELECT id::text FROM departments WHERE lower(name) = 'instandhaltung'`)

	roleKey := "proda" + sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, department_id) VALUES ($1, 'Produktion A', $2::uuid)`, roleKey, prodA)
	must(err)
	ihRole := "ih" + sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, department_id) VALUES ($1, 'Instandhalter', $2::uuid)`, ihRole, ih)
	must(err)
	newUser := func(name, role string) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', $2, 'S', $3, '', '') RETURNING id::text`, name+sfx, name, role)
	}
	pUser := newUser("paula", roleKey)
	ihUser := newUser("ivo", ihRole)
	plainRole := "plain" + sfx // Rolle ohne Abteilung
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label) VALUES ($1, 'Ohne Abteilung')`, plainRole)
	must(err)
	admin := newUser("adam", plainRole)

	// Anlagen: Werk (Produktion A) > Linie (Linie 1) > Geraet (erbt); Halle B (Produktion B); Lager ohne Abteilung
	werk := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ('Werk', 'building', $1::uuid) RETURNING id::text`, prodA)
	linie := id(`INSERT INTO infrastructure (name, type, parent_id, department_id) VALUES ('Linie', 'line', $1::uuid, $2::uuid) RETURNING id::text`, werk, linie1)
	geraet := id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ('Gerät', 'device', $1::uuid) RETURNING id::text`, linie)
	halleB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ('Halle B', 'building', $1::uuid) RETURNING id::text`, prodB)
	lager := id(`INSERT INTO infrastructure (name, type) VALUES ('Lager', 'building') RETURNING id::text`)
	if d := id(`SELECT department_id::text FROM infrastructure_department WHERE infrastructure_id = $1::uuid`, geraet); d != linie1 {
		t.Fatalf("Gerät erbt %s statt Linie 1", d)
	}

	ticket := func(title, infra string, assigned interface{}) string {
		return id(`INSERT INTO tickets (title, description, priority, status, created_by, infrastructure_id, assigned_to)
			VALUES ($1, '', 'medium', 'open', $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`, title+sfx, admin, infra, assigned)
	}
	tGeraet := ticket("Gerät", geraet, nil)   // Unterabteilung -> sichtbar
	tHalleB := ticket("Halle B", halleB, nil) // fremd -> unsichtbar
	tLager := ticket("Lager", lager, nil)     // ohne Abteilung -> sichtbar
	tMine := ticket("Meins", halleB, pUser)   // fremd, aber zugewiesen -> sichtbar

	s := h.deptScopeFor(ctx, pUser, roleKey)
	if s == nil || len(s.Departments) != 2 {
		t.Fatalf("Scope Produktion A: %+v", s)
	}
	if h.deptScopeFor(ctx, ihUser, ihRole) != nil {
		t.Fatal("übergeordnete Abteilung darf nicht eingeschränkt sein")
	}
	if h.deptScopeFor(ctx, admin, plainRole) != nil {
		t.Fatal("Rolle ohne Abteilung darf nicht eingeschränkt sein")
	}
	for tid, want := range map[string]bool{tGeraet: true, tHalleB: false, tLager: true, tMine: true} {
		if got := h.recordInScope(ctx, s, "ticket", tid); got != want {
			t.Errorf("Ticket %s: sichtbar=%v, erwartet %v", tid, got, want)
		}
	}
	asP := &users.User{ID: pUser, Role: users.Role(roleKey)}
	req := httptest.NewRequest(http.MethodGet, "/tickets", nil).WithContext(context.WithValue(ctx, "user", asP))
	ids := h.scopeAllowedIDs(req, "ticket")
	if !ids[tGeraet] || ids[tHalleB] || !ids[tLager] || !ids[tMine] {
		t.Fatalf("Listenfilter: %v", ids)
	}

	// Web-Middleware
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mw := h.DepartmentScopeMiddleware(ok)
	for path, want := range map[string]int{
		"/tickets/" + tHalleB:                    http.StatusForbidden,
		"/tickets/" + tHalleB + "/status-web":    http.StatusForbidden,
		"/records/ticket/" + tHalleB + "/people": http.StatusForbidden,
		"/tickets/" + tGeraet:                    http.StatusTeapot,
		"/tickets":                               http.StatusTeapot,
	} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.WithValue(ctx, "user", asP)))
		if rec.Code != want {
			t.Errorf("%s: %d, erwartet %d", path, rec.Code, want)
		}
	}

	// API: Bearer-Token, Einzelabruf gesperrt, Liste gefiltert
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": pUser, "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("test-secret"))
	list := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []map[string]string{{"id": tGeraet}, {"id": tHalleB}, {"id": tLager}}})
	})
	api := h.APIDepartmentScope(list)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	api.ServeHTTP(rec, r)
	var body struct{ Data []map[string]string }
	must(json.Unmarshal(rec.Body.Bytes(), &body))
	if len(body.Data) != 2 || strings.Contains(rec.Body.String(), tHalleB) {
		t.Fatalf("API-Liste nicht gefiltert: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/v1/tickets/"+tHalleB, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	api.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("API-Einzelabruf fremd: %d", rec.Code)
	}

	// Organigramm-Baum
	var found bool
	for _, n := range h.orgDeptTree(ctx) {
		if n.ID == prodA {
			found = len(n.Children) == 1 && n.Children[0].ID == linie1
		}
	}
	if !found {
		t.Fatal("Linie 1 nicht unter Produktion A im Organigramm")
	}
}
