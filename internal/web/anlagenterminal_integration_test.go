//go:build integration

// Anlagenterminal gegen eine echte PostgreSQL (Migration 156).
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
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

func TestAnlagenterminalHMIFields(t *testing.T) {
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
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	must(database.RunMigrations(ctx, pool, "../../migrations"))

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Anna', 'Muster', 'admin', '', '') RETURNING id::text`, "at"+sfx)
	admin := &users.User{ID: uid, Role: users.Role("admin"), FirstName: "Anna", LastName: "Muster"}
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse "+sfx)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, uid)
	}()
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))

	h := &Handler{db: pool, rbac: rb, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.hmiRoutes(r)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), "user", admin))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	list := func() hmiView {
		t.Helper()
		rec := do("GET", "/infrastructure/"+infra+"/hmis", "")
		var out struct {
			HMIs []hmiView `json:"hmis"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &out))
		if len(out.HMIs) != 1 {
			t.Fatalf("Liste: %s", rec.Body.String())
		}
		return out.HMIs[0]
	}

	// neues HMI ohne Terminal-Felder (alter Client) → Standardwerte
	rec := do("POST", "/infrastructure/"+infra+"/hmis", `{"name":"Panel","host":"10.0.0.5","allow_control":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &created))
	v := list()
	if v.TerminalView != "everyone" || v.TerminalControl != "card" || v.TerminalControlEff != "card" {
		t.Errorf("Standard: %+v", v)
	}

	// Bedienen: jeder bei allow_control=false → gespeichert, wirksam never
	upd := "/infrastructure/" + infra + "/hmis/" + created.ID
	rec = do("POST", upd, `{"name":"Panel","host":"10.0.0.5","allow_control":false,"terminal_view":"card","terminal_control":"everyone"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("speichern: %d %s", rec.Code, rec.Body.String())
	}
	v = list()
	if v.TerminalView != "card" || v.TerminalControl != "everyone" || v.TerminalControlEff != "never" {
		t.Errorf("gespeichert: %+v", v)
	}

	// ungültige Werte → 422, Zeile unverändert
	for _, body := range []string{
		`{"name":"Neu","host":"10.0.0.9","allow_control":true,"terminal_view":"alle"}`,
		`{"name":"Neu","host":"10.0.0.9","allow_control":true,"terminal_control":"Card"}`,
	} {
		rec = do("POST", upd, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("ungültig: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do("POST", "/infrastructure/"+infra+"/hmis", `{"name":"Zwei","host":"10.0.0.9","terminal_view":"x"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Ansehen am Terminal ungültig") {
		t.Errorf("ungültig anlegen: %d %s", rec.Code, rec.Body.String())
	}
	v = list()
	if v.Name != "Panel" || v.TerminalView != "card" || v.TerminalControl != "everyone" || v.AllowControl {
		t.Errorf("Zeile verändert: %+v", v)
	}
}

func TestAnlagenterminalIdleSettings(t *testing.T) {
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
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	must(database.RunMigrations(ctx, pool, "../../migrations"))

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	mk := func(name string, system bool) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, is_system_user)
			VALUES ($1::text, $1::text || '@x', 'x', 'T', 'U', 'admin', '', '', $2) RETURNING id::text`, name+sfx, system)
	}
	adminID, term, normal := mk("ia", false), mk("it", true), mk("in", false)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, KeyTerminalIdle)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::uuid[])`, []string{adminID, term, normal})
	}()
	_, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, KeyTerminalIdle)
	admin := &users.User{ID: adminID, Role: users.Role("admin"), FirstName: "T", LastName: "U"}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	us := users.NewService(users.NewRepository(pool), "test-secret", 1)

	h := &Handler{db: pool, rbac: rb, users: us, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	r.Post("/core/settings/terminal", h.TerminalSettingsWeb)
	post := func(target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), "user", admin))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	saveUser := func(uid, val string) string {
		rec := post("/users/"+uid+"/master", "terminal_idle_seconds="+val)
		return rec.Header().Get("Location")
	}

	// ohne Werte → 120
	if got := h.terminalIdleSeconds(ctx, term); got != 120 {
		t.Errorf("Standard: %d", got)
	}
	// global 300
	if rec := post("/core/settings/terminal", "idle_seconds=300"); rec.Code != http.StatusSeeOther {
		t.Fatalf("global: %d", rec.Code)
	}
	if got := h.terminalIdleSeconds(ctx, term); got != 300 {
		t.Errorf("global: %d", got)
	}
	// ungültiger globaler Wert wird abgelehnt, 300 bleibt
	if loc := post("/core/settings/terminal", "idle_seconds=5").Header().Get("Location"); !strings.Contains(loc, "notice=") || !strings.Contains(loc, "30+und+3600") {
		t.Errorf("global ungültig: %s", loc)
	}
	if got := h.globalTerminalIdle(ctx); got != 300 {
		t.Errorf("global nach Fehler: %d", got)
	}
	// Terminal 45
	if loc := saveUser(term, "45"); !strings.Contains(loc, "msg=") {
		t.Fatalf("Terminal 45: %s", loc)
	}
	if got := h.terminalIdleSeconds(ctx, term); got != 45 {
		t.Errorf("Terminal: %d", got)
	}
	// ungültig 5 → Fehlermeldung, Wert bleibt 45
	if loc := saveUser(term, "5"); !strings.Contains(loc, "err=") || !strings.Contains(loc, "30+und+3600") {
		t.Errorf("ungültig: %s", loc)
	}
	for _, bad := range []string{"0", "-5", "abc", "3601"} {
		if loc := saveUser(term, bad); !strings.Contains(loc, "err=") {
			t.Errorf("%q nicht abgelehnt: %s", bad, loc)
		}
	}
	if got := h.terminalIdleSeconds(ctx, term); got != 45 {
		t.Errorf("Wert nach Fehler: %d", got)
	}
	// Nicht-System-Benutzer: Feld ignoriert (auch ungültig), keine Zeile
	if loc := saveUser(normal, "99"); !strings.Contains(loc, "msg=") {
		t.Errorf("Nicht-System: %s", loc)
	}
	_ = saveUser(normal, "5")
	var n int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM terminal_settings WHERE user_id = $1::uuid`, normal).Scan(&n))
	if n != 0 {
		t.Errorf("Nicht-System-Benutzer hat %d Zeilen", n)
	}
	// leer = zurück auf global
	_ = saveUser(term, "")
	if got := h.terminalIdleSeconds(ctx, term); got != 300 {
		t.Errorf("leer: %d", got)
	}
}

func TestAnlagenterminalInfo(t *testing.T) {
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
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	must(database.RunMigrations(ctx, pool, "../../migrations"))

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	term := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, is_system_user)
		VALUES ($1::text, $1::text || '@x', 'x', 'T', 'U', 'viewer', '', '', true) RETURNING id::text`, "tm"+sfx)
	infraA := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Halle A "+sfx)
	infraB := id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'plant', $2::uuid) RETURNING id::text`, "Presse B "+sfx, infraA)
	infraH := id(`INSERT INTO infrastructure (name, type, parent_id, hidden_at) VALUES ($1, 'plant', $2::uuid, NOW()) RETURNING id::text`, "Versteckt "+sfx, infraA)
	infraC := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Fremd C "+sfx)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM faults WHERE created_by = $1::uuid`, term)
		_, _ = pool.Exec(ctx, `DELETE FROM tickets WHERE created_by = $1::uuid`, term)
		_, _ = pool.Exec(ctx, `UPDATE users SET terminal_infrastructure_id = NULL WHERE id = $1::uuid`, term)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = ANY($1::uuid[])`, []string{infraB, infraH, infraA, infraC})
		_, _ = pool.Exec(ctx, `DELETE FROM terminal_settings WHERE user_id = $1::uuid`, term)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, term)
	}()
	fault := func(title, infra, status string, archived bool) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO faults (title, status, infrastructure_id, created_by, archived_at)
			VALUES ($1, $2::fault_status, $3::uuid, $4::uuid, CASE WHEN $5 THEN NOW() END)`, title, status, infra, term, archived)
		must(err)
	}
	ticket := func(title, infra, status string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO tickets (title, status, infrastructure_id, created_by) VALUES ($1, $2::ticket_status, $3::uuid, $4::uuid)`, title, status, infra, term)
		must(err)
	}
	fault("A offen", infraA, "detected", false)
	fault("B in Arbeit", infraB, "in_progress", false)
	fault("B geloest", infraB, "resolved", false)
	fault("A geschlossen", infraA, "closed", false)
	fault("A archiviert", infraA, "closed", true) // Trigger 108 löscht archived_at bei offenem Status
	fault("C fremd", infraC, "detected", false)
	fault("H versteckt", infraH, "detected", false)
	ticket("T-A offen", infraA, "open")
	ticket("T-B in Arbeit", infraB, "in_progress")
	ticket("T-B wartet", infraB, "pending")
	ticket("T-A geloest", infraA, "resolved")
	ticket("T-A zu", infraA, "closed")
	ticket("T-C fremd", infraC, "open")

	h := &Handler{db: pool, jwtSecret: "test-geheim"}

	info, err := h.loadTerminalInfo(ctx, infraA)
	must(err)
	titles := func(items []terminalItem) string {
		var s []string
		for _, it := range items {
			s = append(s, it.Title+"@"+it.Path)
		}
		return strings.Join(s, "|")
	}
	ft := titles(info.Faults)
	if info.FaultsTotal != 2 || len(info.Faults) != 2 || !strings.Contains(ft, "A offen@") || !strings.Contains(ft, "B in Arbeit@Presse B "+sfx) {
		t.Errorf("Störungen: total %d %s", info.FaultsTotal, ft)
	}
	for _, bad := range []string{"geloest", "fremd", "versteckt", "archiviert", "geschlossen"} {
		if strings.Contains(ft, bad) {
			t.Errorf("Störung %q darf nicht erscheinen: %s", bad, ft)
		}
	}
	tt := titles(info.Tickets)
	if info.TicketsTotal != 3 || len(info.Tickets) != 3 || strings.Contains(tt, "geloest") || strings.Contains(tt, "fremd") || strings.Contains(tt, "zu@") {
		t.Errorf("Tickets: total %d %s", info.TicketsTotal, tt)
	}
	if info.Faults[0].StatusLabel == "" || info.Faults[0].Age != "gerade eben" {
		t.Errorf("Status/Alter: %+v", info.Faults[0])
	}

	// sehr viele offene Störungen: höchstens 50, Gesamtzahl bleibt
	for i := 0; i < 58; i++ {
		fault("Masse", infraB, "detected", false)
	}
	info, err = h.loadTerminalInfo(ctx, infraA)
	must(err)
	if len(info.Faults) != terminalListLimit || info.FaultsTotal != 60 {
		t.Errorf("Limit: %d Einträge, total %d", len(info.Faults), info.FaultsTotal)
	}
	if info.Faults[0].Title != "Masse" {
		t.Errorf("neueste zuerst verletzt: %s", info.Faults[0].Title)
	}

	// Seiten über die Routen
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	h.tmpl = loadTestTemplates(t)
	r := chi.NewRouter()
	h.terminalRoutes(r)
	sys := &users.User{ID: term, Role: users.Role("viewer"), IsSystemUser: true}
	get := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", p, nil)
		req = req.WithContext(context.WithValue(req.Context(), "user", sys))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	noInfra := "Diesem Terminal ist keine Anlage zugeordnet"
	if rec := get("/terminal/info"); rec.Code != 200 || !strings.Contains(rec.Body.String(), noInfra) {
		t.Errorf("ohne Anlage: %d", rec.Code)
	}
	_, err = pool.Exec(ctx, `UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, infraA)
	must(err)
	rec := get("/terminal/info")
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, noInfra) || !strings.Contains(body, "und 10 weitere") || !strings.Contains(body, `data-idle="120"`) {
		t.Errorf("info: %d, Hinweis auf weitere bzw. data-idle=120 fehlt", rec.Code)
	}
	_, err = pool.Exec(ctx, `INSERT INTO terminal_settings (user_id, idle_seconds) VALUES ($1::uuid, 45)`, term)
	must(err)
	if body := get("/terminal/neu").Body.String(); !strings.Contains(body, `data-idle="45"`) || !strings.Contains(body, `href="/e/`+infraA+`"`) {
		t.Errorf("neu: data-idle=45 bzw. Melde-Link fehlt")
	}
	if body := get("/terminal").Body.String(); !strings.Contains(body, `data-idle="0"`) || !strings.Contains(body, "Zum Melden berühren") {
		t.Errorf("Schoner")
	}
	jr := get("/terminal/info.json")
	var js terminalInfo
	must(json.Unmarshal(jr.Body.Bytes(), &js))
	if jr.Code != 200 || js.FaultsTotal != 60 || len(js.Faults) != 50 {
		t.Errorf("info.json: %d %d/%d", jr.Code, js.FaultsTotal, len(js.Faults))
	}

	// ausgeblendete Anlage → wie ohne Anlage, kein 500
	_, err = pool.Exec(ctx, `UPDATE infrastructure SET hidden_at = NOW() WHERE id = $1::uuid`, infraA)
	must(err)
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		if rec := get(p); rec.Code != 200 || !strings.Contains(rec.Body.String(), noInfra) {
			t.Errorf("ausgeblendet %s: %d", p, rec.Code)
		}
	}
	if rec := get("/terminal/info.json"); rec.Code != http.StatusNotFound {
		t.Errorf("info.json ausgeblendet: %d", rec.Code)
	}
	if _, err := h.loadTerminalInfo(ctx, infraA); err == nil {
		t.Error("loadTerminalInfo bei ausgeblendeter Anlage ohne Fehler")
	}
}
