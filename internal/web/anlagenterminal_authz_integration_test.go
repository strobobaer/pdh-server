//go:build integration

// Anlagenterminal: Rechte, Easy-Mode-Hinweis, HMI-Speichern (Abteilung, Felder behalten).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Anlagenterminal ./internal/web/
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

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

// atEnv: Pool und Hilfsfunktionen (mit Aufräumen) für die Rechte-Tests.
type atEnv struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	sfx  string
	rb   *rbac.Service
}

func newATEnv(t *testing.T) *atEnv {
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
	e := &atEnv{t: t, ctx: ctx, pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.must(database.RunMigrations(ctx, pool, "../../migrations"))
	e.rb = rbac.NewService(rbac.NewRepository(pool))
	return e
}

func (e *atEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *atEnv) id(q string, args ...any) string {
	e.t.Helper()
	var s string
	e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&s))
	return s
}

func (e *atEnv) exec(q string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, q, args...)
	e.must(err)
}

func (e *atEnv) user(name, role string, system bool) string {
	e.t.Helper()
	uid := e.id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, is_system_user)
		VALUES ($1::text, $1::text || '@x', 'x', 'T', 'U', $2, '', '', $3) RETURNING id::text`, name+e.sfx, role, system)
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `UPDATE users SET terminal_infrastructure_id = NULL, manager_id = NULL WHERE id = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM terminal_settings WHERE user_id = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM faults WHERE created_by = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM tickets WHERE created_by = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	return uid
}

func (e *atEnv) dept(name string) string {
	e.t.Helper()
	d := e.id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, name+e.sfx)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, d) })
	return d
}

func (e *atEnv) infra(name, dept, parent string) string {
	e.t.Helper()
	i := e.id(`INSERT INTO infrastructure (name, type, department_id, parent_id) VALUES ($1, 'plant', NULLIF($2,'')::uuid, NULLIF($3,'')::uuid) RETURNING id::text`, name+e.sfx, dept, parent)
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM infrastructure_hmis WHERE infrastructure_id = $1::uuid`, i)
		_, _ = e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, i)
	})
	return i
}

// grant: Einzelberechtigung (dept "" = überall); danach warm() aufrufen.
func (e *atEnv) grant(uid, perm, dept string, granted bool) {
	e.t.Helper()
	e.exec(`INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
		SELECT $1::uuid, id, $3, NULLIF($4,'')::uuid FROM permissions WHERE key = $2`, uid, perm, granted, dept)
}

func (e *atEnv) warm() { e.t.Helper(); e.must(e.rb.Warm(e.ctx)) }

func (e *atEnv) setting(key, value string) {
	e.t.Helper()
	e.exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW()) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, key) })
}

func withUser(req *http.Request, u *users.User) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), "user", u))
}

// I1: Felder fehlen im Request → gespeicherte Werte bleiben.
func TestAnlagenterminalHMISaveKeepsTerminalFields(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("hk", "admin", false)
	infra := e.infra("Keep ", "", "")
	e.warm()
	admin := &users.User{ID: uid, Role: users.Role("admin")}
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.hmiRoutes(r)
	do := func(target, body string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest("POST", target, strings.NewReader(body)), admin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/infrastructure/"+infra+"/hmis", `{"name":"P","host":"10.0.0.5","allow_control":true,"terminal_view":"never","terminal_control":"everyone"}`)
	if rec.Code != 200 {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	e.must(json.Unmarshal(rec.Body.Bytes(), &c))
	read := func() (string, string) {
		var v, ctl string
		e.must(e.pool.QueryRow(e.ctx, `SELECT terminal_view, terminal_control FROM infrastructure_hmis WHERE id = $1::uuid`, c.ID).Scan(&v, &ctl))
		return v, ctl
	}
	if v, ctl := read(); v != "never" || ctl != "everyone" {
		t.Fatalf("angelegt: %s/%s", v, ctl)
	}
	upd := "/infrastructure/" + infra + "/hmis/" + c.ID
	// alter Client: ohne beide Felder
	if rec := do(upd, `{"name":"P2","host":"10.0.0.6","allow_control":true}`); rec.Code != 200 {
		t.Fatalf("update ohne Felder: %d %s", rec.Code, rec.Body.String())
	}
	if v, ctl := read(); v != "never" || ctl != "everyone" {
		t.Errorf("Update ohne Felder überschreibt: %s/%s", v, ctl)
	}
	// nur ein Feld → das andere bleibt
	if rec := do(upd, `{"name":"P2","host":"10.0.0.6","allow_control":true,"terminal_view":"card"}`); rec.Code != 200 {
		t.Fatalf("update mit einem Feld: %d", rec.Code)
	}
	if v, ctl := read(); v != "card" || ctl != "everyone" {
		t.Errorf("ein Feld: %s/%s", v, ctl)
	}
}

// M3: HMI speichern verlangt hmi.manage für die Abteilung der Anlage.
func TestAnlagenterminalHMISaveDepartmentScope(t *testing.T) {
	e := newATEnv(t)
	deptA, deptB := e.dept("DA"), e.dept("DB")
	infraA, infraB := e.infra("HA ", deptA, ""), e.infra("HB ", deptB, "")
	mgr := e.user("hm", "viewer", false)
	e.grant(mgr, "hmi.manage", deptA, true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.hmiRoutes(r)
	do := func(infra string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest("POST", "/infrastructure/"+infra+"/hmis", strings.NewReader(`{"name":"P","host":"10.0.0.5"}`)),
			&users.User{ID: mgr, Role: users.Role("viewer")})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(infraA); rec.Code != 200 {
		t.Errorf("eigene Abteilung: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(infraB); rec.Code != http.StatusForbidden {
		t.Errorf("fremde Abteilung: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM infrastructure_hmis WHERE infrastructure_id = $1::uuid`, infraB).Scan(&n))
	if n != 0 {
		t.Errorf("HMI in fremder Abteilung angelegt: %d", n)
	}
}

// I2 + I3: Terminal-Seiten prüfen die Rechte des Terminal-Benutzers; Easy-Mode aus → Neu deaktiviert.
func TestAnlagenterminalPermissionsAndEasyMode(t *testing.T) {
	e := newATEnv(t)
	deptA, deptB := e.dept("PA"), e.dept("PB")
	infraA := e.infra("Anlage A ", deptA, "")
	termFaults := e.user("tf", "viewer", true) // nur Störungen in A
	termNone := e.user("tn", "viewer", true)   // Rechte nur in Abteilung B
	termBoth := e.user("tb", "viewer", true)   // beides in A
	for _, u := range []string{termFaults, termNone, termBoth} {
		e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, u, infraA)
	}
	e.grant(termFaults, "faults.view", deptA, true)
	e.grant(termFaults, "tickets.view", "", false)
	e.grant(termNone, "faults.view", deptB, true)
	e.grant(termNone, "tickets.view", deptB, true)
	e.grant(termBoth, "faults.view", deptA, true)
	e.grant(termBoth, "tickets.view", deptA, true)
	adminU := e.user("ad", "admin", false)
	e.warm()
	e.exec(`INSERT INTO faults (title, status, infrastructure_id, created_by) VALUES ('F-GEHEIM', 'detected'::fault_status, $1::uuid, $2::uuid)`, infraA, termBoth)
	e.exec(`INSERT INTO tickets (title, status, infrastructure_id, created_by) VALUES ('T-GEHEIM', 'open'::ticket_status, $1::uuid, $2::uuid)`, infraA, termBoth)

	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	h.terminalRoutes(r)
	get := func(path string, uid string, override string) *httptest.ResponseRecorder {
		u := &users.User{ID: uid, Role: users.Role("viewer"), IsSystemUser: true}
		if override != "" { // Override-Sitzung: angemeldet ist ein Admin, das Terminal steht im Rückkehr-Token
			u = &users.User{ID: uid, Role: users.Role("admin")}
		}
		req := withUser(httptest.NewRequest("GET", path, nil), u)
		if override != "" {
			tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": override}).SignedString([]byte("test-geheim"))
			e.must(err)
			req.AddCookie(&http.Cookie{Name: "pdh_return_token", Value: tok})
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	noPerm := "Keine Berechtigung"

	// beides erlaubt
	rec := get("/terminal/info", termBoth, "")
	if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "F-GEHEIM") || !strings.Contains(b, "T-GEHEIM") || strings.Contains(b, noPerm) {
		t.Errorf("beides: %d", rec.Code)
	}
	// nur Störungen: Tickets ausgeblendet, Hinweis
	rec = get("/terminal/info", termFaults, "")
	if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "F-GEHEIM") || strings.Contains(b, "T-GEHEIM") || !strings.Contains(b, noPerm+" für Tickets") {
		t.Errorf("nur Störungen: %d Tickets sichtbar bzw. Hinweis fehlt", rec.Code)
	}
	var js terminalInfo
	rec = get("/terminal/info.json", termFaults, "")
	e.must(json.Unmarshal(rec.Body.Bytes(), &js))
	if rec.Code != 200 || len(js.Faults) != 1 || len(js.Tickets) != 0 || js.TicketsTotal != 0 || !js.CanFaults || js.CanTickets {
		t.Errorf("info.json nur Störungen: %d %s", rec.Code, rec.Body.String())
	}
	// Rechte nur in anderer Abteilung → klare Meldung, info.json 403
	for _, p := range []string{"/terminal/info", "/terminal/neu"} {
		rec = get(p, termNone, "")
		if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, noPerm) || strings.Contains(b, "F-GEHEIM") || strings.Contains(b, "T-GEHEIM") {
			t.Errorf("%s ohne Recht: %d", p, rec.Code)
		}
	}
	if rec = get("/terminal/info.json", termNone, ""); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "GEHEIM") {
		t.Errorf("info.json ohne Recht: %d", rec.Code)
	}
	// Override-Sitzung: der Admin darf alles, das Terminal aber nicht
	if rec = get("/terminal/info.json", adminU, termNone); rec.Code != http.StatusForbidden {
		t.Errorf("Override-Sitzung info.json: %d", rec.Code)
	}
	if b := get("/terminal/info", adminU, termNone).Body.String(); strings.Contains(b, "GEHEIM") || !strings.Contains(b, noPerm) {
		t.Errorf("Override-Sitzung info: Terminal-Rechte nicht beachtet")
	}
	// Bildschirmschoner braucht kein Recht
	if rec = get("/terminal", termNone, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), noPerm) {
		t.Errorf("Schoner: %d", rec.Code)
	}

	// I3: Easy-Mode an → Links; aus → deaktiviert mit Hinweis
	if b := get("/terminal/neu", termBoth, "").Body.String(); !strings.Contains(b, `href="/e/`+infraA+`"`) {
		t.Error("Easy-Mode an: Melde-Link fehlt")
	}
	e.setting(KeyEasyMode, "0")
	b := get("/terminal/neu", termBoth, "").Body.String()
	if strings.Contains(b, `href="/e/`) || !strings.Contains(b, "Melden am Terminal ist ausgeschaltet") || !strings.Contains(b, "Easy-Mode") {
		t.Errorf("Easy-Mode aus: Link noch da bzw. Hinweis fehlt")
	}
}

// M1: Terminal-Karte nur für „Benutzer verwalten“; sonst 403 und Master-Feld ignoriert.
func TestAnlagenterminalGlobalSettingAuthz(t *testing.T) {
	e := newATEnv(t)
	rolesOnly := e.user("mr", "viewer", false)
	e.grant(rolesOnly, "system.manage_roles", "", true)
	e.grant(rolesOnly, "system.manage_users", "", false)
	usersOnly := e.user("mu", "viewer", false)
	e.grant(usersOnly, "system.manage_users", "", true)
	e.grant(usersOnly, "system.manage_roles", "", false)
	both := e.user("mb", "viewer", false)
	e.grant(both, "system.manage_users", "", true)
	e.grant(both, "system.manage_roles", "", true)
	sup := e.user("sv", "viewer", false) // nur Vorgesetzter des Terminals
	term := e.user("st", "viewer", true)
	e.exec(`UPDATE users SET manager_id = $1::uuid WHERE id = $2::uuid`, sup, term)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Post("/core/settings/terminal", h.TerminalSettingsWeb)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	post := func(target, body string, uid string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest("POST", target, strings.NewReader(body)), &users.User{ID: uid, Role: users.Role("viewer")})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, KeyTerminalIdle) })
	if rec := post("/core/settings/terminal", "idle_seconds=200", rolesOnly); rec.Code != http.StatusForbidden {
		t.Errorf("nur Rollen verwalten: %d", rec.Code)
	}
	if rec := post("/core/settings/terminal", "idle_seconds=200", usersOnly); rec.Code != http.StatusSeeOther {
		t.Errorf("Benutzer verwalten: %d", rec.Code)
	}
	if got := h.globalTerminalIdle(e.ctx); got != 200 {
		t.Errorf("global: %d", got)
	}

	// Karte nur sichtbar, wer sie auch speichern darf
	// Die Seite lädt ihre Vorlage relativ zum Arbeitsverzeichnis (Repo-Wurzel).
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	page := func(uid string) string {
		req := withUser(httptest.NewRequest("GET", "/core/settings", nil), &users.User{ID: uid, Role: users.Role("viewer")})
		rec := httptest.NewRecorder()
		h.CoreSettingsPage(rec, req)
		return rec.Body.String()
	}
	if b := page(rolesOnly); strings.Contains(b, "/core/settings/terminal") {
		t.Error("Karte für Nutzer ohne Recht sichtbar")
	}
	if b := page(both); !strings.Contains(b, "/core/settings/terminal") {
		t.Errorf("Karte für Berechtigte fehlt (%d Zeichen: %.300s)", len(b), b)
	}

	// Vorgesetzte(r) ohne „Benutzer verwalten“: Rückkehrzeit am Master wird ignoriert
	rec := post("/users/"+term+"/master", "terminal_idle_seconds=77", sup)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Errorf("Master-Speichern: %s", loc)
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_settings WHERE user_id = $1::uuid`, term).Scan(&n))
	if n != 0 {
		t.Errorf("Rückkehrzeit ohne Recht gespeichert: %d Zeilen", n)
	}
}

// M4: Datenbankfehler werden gemeldet bzw. fallen sicher zurück (nicht „kein Systembenutzer“/„gespeichert“).
func TestAnlagenterminalSettingsDBErrors(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close() // alle Zugriffe schlagen fehl
	h := &Handler{db: pool}
	if sys, err := h.isSystemUser(ctx, "11111111-1111-1111-1111-111111111111"); err == nil || sys {
		t.Errorf("isSystemUser: %v %v", sys, err)
	}
	if err := h.saveTerminalIdle(ctx, "11111111-1111-1111-1111-111111111111", "60"); err == nil {
		t.Error("saveTerminalIdle meldet bei DB-Fehler keinen Fehler")
	}
	if got := h.terminalIdleSeconds(ctx, "11111111-1111-1111-1111-111111111111"); got != terminalIdleDefault {
		t.Errorf("terminalIdleSeconds Rückfall: %d", got)
	}
	if got := h.terminalIdleField(ctx, "11111111-1111-1111-1111-111111111111"); got != "" {
		t.Errorf("terminalIdleField Rückfall: %q", got)
	}
}
