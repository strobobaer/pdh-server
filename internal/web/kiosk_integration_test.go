//go:build integration

// Kiosk-Terminals gegen eine echte PostgreSQL (Migration 155, Speichern,
// Zuordnung, Befehl „Am Terminal zeigen“, Zugriffsprotokoll, Rechte auf
// Abteilungsebene).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Kiosk ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestKioskIntegration(t *testing.T) {
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

	// Recht hmi.kiosk für admin, manager, technician, asset_manager
	var n int
	must(pool.QueryRow(ctx, `SELECT COUNT(DISTINCT r.key) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.key = 'hmi.kiosk' AND rp.department_id IS NULL AND r.key IN ('admin', 'manager', 'technician', 'asset_manager')`).Scan(&n))
	if n != 4 {
		t.Errorf("hmi.kiosk bei %d von 4 Rollen", n)
	}

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	deptA := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Halle A "+sfx)
	deptB := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Halle B "+sfx)
	// Rolle mit hmi.kiosk und hmi.manage nur in Halle A
	role := "kioska" + sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'Kiosk A', 40)`, role)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT r.id, p.id, $2::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key IN ('hmi.kiosk', 'hmi.manage', 'hmi.view')`, role, deptA)
	must(err)
	mkUser := func(login, roleKey, first string) *users.User {
		uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', $3, 'Muster', $2, '', '') RETURNING id::text`, login+sfx, roleKey, first)
		return &users.User{ID: uid, Role: users.Role(roleKey), FirstName: first, LastName: "Muster"}
	}
	admin := mkUser("ka", "admin", "Anna")
	tech := mkUser("kt", "technician", "Maria")
	scoped := mkUser("ks", role, "Sven")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, admin.ID, tech.ID, scoped.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE key = $1`, role)
	}()
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))

	h := &Handler{db: pool, rbac: rb, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.kioskRoutes(r)
	h.hmiRoutes(r)
	do := func(u *users.User, method, target, ctype, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req = req.WithContext(context.WithValue(req.Context(), "user", u))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	const formCT = "application/x-www-form-urlencoded"

	// Attrappe des Terminals
	f := &kioskFake{body: `{"status":"opened"}`}
	srv := startKioskFake(t, f)

	// Terminal anlegen (Formular) – nur mit hmi.manage ohne Abteilungsbegrenzung
	name := "Terminal " + sfx
	form := url.Values{"name": {name}, "base_url": {srv.URL + "/"}, "api_key": {testKioskKey}, "pubkey_sha256": {withColons(kioskPin(srv))}, "active": {"on"}}
	if rec := do(scoped, "POST", "/core/settings/kiosk-terminals", formCT, form.Encode()); rec.Code != http.StatusForbidden {
		t.Errorf("anlegen mit hmi.manage nur in Halle A: %d", rec.Code)
	}
	rec := do(admin, "POST", "/core/settings/kiosk-terminals", formCT, form.Encode())
	if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var tid, keyEnc, pin, base string
	must(pool.QueryRow(ctx, `SELECT id::text, api_key_enc, pubkey_sha256, base_url FROM kiosk_terminals WHERE name = $1`, name).Scan(&tid, &keyEnc, &pin, &base))
	defer pool.Exec(ctx, `DELETE FROM kiosk_terminals WHERE id = $1::uuid`, tid)
	if strings.Contains(keyEnc, testKioskKey) || pin != kioskPin(srv) || base != srv.URL {
		t.Fatalf("gespeichert: key_enc=%q pin=%q base=%q", keyEnc, pin, base)
	}
	if k, err := decryptSecret(h.kioskKey(), keyEnc); err != nil || k != testKioskKey {
		t.Fatalf("Schlüssel nicht entschlüsselbar: %v", err)
	}
	// gleicher Name → Fehlercode statt 500
	rec = do(admin, "POST", "/core/settings/kiosk-terminals", formCT, form.Encode())
	if !strings.Contains(rec.Header().Get("Location"), "err=name_taken") {
		t.Errorf("doppelter Name: %s", rec.Header().Get("Location"))
	}
	// ändern ohne Schlüssel, Ziel gleich: Schlüssel bleibt
	form.Set("terminal_id", tid)
	form.Del("api_key")
	do(admin, "POST", "/core/settings/kiosk-terminals", formCT, form.Encode())
	keyOf := func() string {
		var k string
		must(pool.QueryRow(ctx, `SELECT api_key_enc FROM kiosk_terminals WHERE id = $1::uuid`, tid).Scan(&k))
		return k
	}
	if keyOf() != keyEnc {
		t.Error("Schlüssel beim Ändern ohne neues Ziel verändert")
	}
	// I1: neue Adresse ohne neuen Schlüssel → abgewiesen, nichts geändert
	moved := url.Values{}
	for k, v := range form {
		moved[k] = v
	}
	moved.Set("base_url", "https://10.66.66.66:8443")
	rec = do(admin, "POST", "/core/settings/kiosk-terminals", formCT, moved.Encode())
	if !strings.Contains(rec.Header().Get("Location"), "err=key_required") {
		t.Errorf("neue Adresse ohne Schlüssel: %s", rec.Header().Get("Location"))
	}
	must(pool.QueryRow(ctx, `SELECT base_url FROM kiosk_terminals WHERE id = $1::uuid`, tid).Scan(&base))
	if base != srv.URL || keyOf() != keyEnc {
		t.Fatalf("Terminal trotz Abweisung geändert: %s", base)
	}
	// neuer Fingerabdruck ohne Schlüssel → abgewiesen
	repin := url.Values{}
	for k, v := range form {
		repin[k] = v
	}
	repin.Set("pubkey_sha256", strings.Repeat("cd", 32))
	if rec = do(admin, "POST", "/core/settings/kiosk-terminals", formCT, repin.Encode()); !strings.Contains(rec.Header().Get("Location"), "err=key_required") {
		t.Errorf("neuer Fingerabdruck ohne Schlüssel: %s", rec.Header().Get("Location"))
	}
	// SQL-Schutz: Ziel geändert und kein Schlüssel (z. B. gleichzeitige Änderung) → Schlüssel verworfen
	_, err = pool.Exec(ctx, `UPDATE kiosk_terminals SET base_url = 'https://10.77.77.77:8443' WHERE id = $1::uuid`, tid)
	must(err)
	_, err = pool.Exec(ctx, kioskUpdateSQL, tid, name, srv.URL, kioskPin(srv), true, "")
	must(err)
	if keyOf() != "" {
		t.Error("Schlüssel trotz geändertem Ziel behalten (SQL)")
	}
	// Schlüssel neu eintragen
	form.Set("api_key", testKioskKey)
	do(admin, "POST", "/core/settings/kiosk-terminals", formCT, form.Encode())
	if k, _ := decryptSecret(h.kioskKey(), keyOf()); k != testKioskKey {
		t.Fatal("neuer Schlüssel nicht gespeichert")
	}

	// Verbindung prüfen: Ergebnis auf der Seite, in der Adresse nur Codes
	f.body = `{"version":"0.1.7","terminal":"Halle","busy":false}`
	rec = do(admin, "POST", "/core/settings/kiosk-terminals/"+tid+"/check", formCT, "")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "notice=checked") || strings.Contains(loc, "0.1.7") {
		t.Errorf("prüfen: %s", loc)
	}
	if res, ok := kioskLastCheck(tid); !ok || !res.OK || !strings.Contains(res.Message, "0.1.7") || !strings.Contains(res.Message, "frei") {
		t.Errorf("Prüfergebnis: %+v", res)
	}
	if rec := do(scoped, "POST", "/core/settings/kiosk-terminals/"+tid+"/check", formCT, ""); rec.Code != http.StatusForbidden {
		t.Errorf("prüfen mit hmi.manage nur in Halle A: %d", rec.Code)
	}

	// Anlagen: plant (Halle B) mit HMI, plantA (Halle A) mit HMI
	line := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'line') RETURNING id::text`, "Linie "+sfx)
	plant := id(`INSERT INTO infrastructure (name, type, parent_id, department_id) VALUES ('Presse 2', 'plant', $1::uuid, $2::uuid) RETURNING id::text`, line, deptB)
	plantA := id(`INSERT INTO infrastructure (name, type, parent_id, department_id) VALUES ('Presse 9', 'plant', $1::uuid, $2::uuid) RETURNING id::text`, line, deptA)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, plant, plantA, line)
		_, _ = pool.Exec(ctx, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, deptA, deptB)
	}()
	pwEnc, _ := encryptSecret(h.hmiKey(), "vnc-geheim")
	hmiID := id(`INSERT INTO infrastructure_hmis (infrastructure_id, name, host, port, password_enc, allow_control)
		VALUES ($1::uuid, 'Panel', '10.9.8.7', 5901, $2, false) RETURNING id::text`, plant, pwEnc)
	hmiA := id(`INSERT INTO infrastructure_hmis (infrastructure_id, name, host, port, password_enc)
		VALUES ($1::uuid, 'Panel A', '10.9.8.9', 5900, $2) RETURNING id::text`, plantA, pwEnc)
	assign := func(u *users.User, infra string) error {
		req := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"kiosk_terminal_id": {tid}}.Encode()))
		req.Header.Set("Content-Type", formCT)
		req = req.WithContext(context.WithValue(req.Context(), "user", u))
		_ = req.ParseForm()
		return h.saveInfraKioskTerminal(ctx, req, infra)
	}
	// Zuordnung: Abteilungsrecht gilt nur in Halle A
	must(assign(scoped, plant))
	var cur string
	must(pool.QueryRow(ctx, `SELECT COALESCE(kiosk_terminal_id::text, '') FROM infrastructure WHERE id = $1::uuid`, plant).Scan(&cur))
	if cur != "" {
		t.Error("Zuordnung in Halle B mit Recht nur in Halle A gespeichert")
	}
	must(assign(scoped, plantA))
	must(assign(admin, plant))

	// HMI-Liste: Knopf „zeigen“, nicht „bedienen“ (am HMI nicht erlaubt)
	rec = do(tech, "GET", "/infrastructure/"+plant+"/hmis", "", "")
	var list struct {
		HMIs     []hmiView `json:"hmis"`
		Terminal string    `json:"kiosk_terminal"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &list))
	if len(list.HMIs) != 1 || !list.HMIs[0].KioskView || list.HMIs[0].KioskControl || list.Terminal != name {
		t.Fatalf("HMI-Liste: %s", rec.Body.String())
	}
	// Abteilungsrecht: in Halle B kein Knopf
	rec = do(scoped, "GET", "/infrastructure/"+plant+"/hmis", "", "")
	list.HMIs, list.Terminal = nil, ""
	must(json.Unmarshal(rec.Body.Bytes(), &list))
	if len(list.HMIs) == 1 && list.HMIs[0].KioskView {
		t.Errorf("Knopf trotz Recht nur in Halle A: %s", rec.Body.String())
	}

	openURL := func(infra, hmi string) string { return "/infrastructure/" + infra + "/hmis/" + hmi + "/kiosk" }
	calls := f.calls.Load()
	// Abteilungsrecht: Halle B abgewiesen, ohne Befehl
	if rec = do(scoped, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`); rec.Code != http.StatusForbidden {
		t.Errorf("Halle B mit Recht nur in Halle A: %d %s", rec.Code, rec.Body.String())
	}
	// IDOR: HMI einer anderen Anlage unter erlaubter Anlage → nicht gefunden
	if rec = do(scoped, "POST", openURL(plantA, hmiID), "application/json", `{"mode":"view"}`); rec.Code != http.StatusNotFound {
		t.Errorf("HMI einer anderen Anlage: %d %s", rec.Code, rec.Body.String())
	}
	// bedienen abgewiesen (Techniker ohne hmi.control)
	if rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"control"}`); rec.Code != http.StatusForbidden {
		t.Errorf("bedienen: %d %s", rec.Code, rec.Body.String())
	}
	// bedienen abgewiesen (am HMI nicht erlaubt), auch für den Administrator
	if rec = do(admin, "POST", openURL(plant, hmiID), "application/json", `{"mode":"control"}`); rec.Code != http.StatusForbidden {
		t.Errorf("bedienen am HMI nicht erlaubt: %d %s", rec.Code, rec.Body.String())
	}
	if f.calls.Load() != calls {
		t.Fatal("abgewiesener Befehl hat das Terminal erreicht")
	}

	// Abteilungsrecht in Halle A: erlaubt
	f.code, f.body = 200, `{"status":"opened"}`
	if rec = do(scoped, "POST", openURL(plantA, hmiA), "application/json", `{"mode":"view"}`); rec.Code != http.StatusOK {
		t.Errorf("Halle A: %d %s", rec.Code, rec.Body.String())
	}

	// zeigen → geöffnet
	rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "geöffnet") {
		t.Fatalf("zeigen: %d %s", rec.Code, rec.Body.String())
	}
	var sent kioskOpenRequest
	must(json.Unmarshal(f.lastBody, &sent))
	if sent.Host != "10.9.8.7" || sent.Port != 5901 || sent.Password != "vnc-geheim" || sent.Mode != "view" ||
		sent.Label != "Linie "+sfx+" › Presse 2 / Panel" || sent.User != "Maria Muster" {
		t.Errorf("Befehl: %+v", sent)
	}

	// S4: läuft schon ein Befehl an dieses Terminal → abgewiesen ohne Aufruf
	release, ok := kioskAcquire(tid)
	if !ok {
		t.Fatal("Sperre nicht frei")
	}
	calls = f.calls.Load()
	rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`)
	release()
	if rec.Code != http.StatusTooManyRequests || f.calls.Load() != calls || !strings.Contains(rec.Body.String(), "gerade") {
		t.Errorf("gleichzeitig: %d %s", rec.Code, rec.Body.String())
	}

	// belegt → 409 mit Meldung
	f.code, f.body = 409, `{"status":"busy","hmi":"Presse 1","since":"2026-11-09T14:05:00+01:00"}`
	rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Presse 1") {
		t.Fatalf("belegt: %d %s", rec.Code, rec.Body.String())
	}

	// Protokoll: zwei Befehle mit Terminal, einer mit Fehler; nie Passwort/Schlüssel
	rows, err := pool.Query(ctx, `SELECT mode, COALESCE(kiosk_terminal_id::text, ''), kiosk_terminal_name, error, ended_at IS NOT NULL
		FROM infrastructure_hmi_sessions WHERE hmi_id = $1::uuid ORDER BY started_at`, hmiID)
	must(err)
	var got []string
	for rows.Next() {
		var mode, ktid, kname, e string
		var ended bool
		must(rows.Scan(&mode, &ktid, &kname, &e, &ended))
		if ktid != tid || kname != name || !ended || strings.Contains(e, "vnc-geheim") || strings.Contains(e, testKioskKey) {
			t.Errorf("Protokollzeile: %s %s %s %q %v", mode, ktid, kname, e, ended)
		}
		got = append(got, e)
	}
	rows.Close()
	if len(got) != 2 || got[0] != "" || !strings.Contains(got[1], "belegt") {
		t.Errorf("Protokoll: %q", got)
	}
	rec = do(tech, "GET", "/infrastructure/"+plant+"/hmis/"+hmiID+"/sessions", "", "")
	if !strings.Contains(rec.Body.String(), `"terminal":"`+name+`"`) {
		t.Errorf("Zugriffe ohne Terminal: %s", rec.Body.String())
	}

	// S2: archivierte Anlage → kein Befehl
	_, err = pool.Exec(ctx, `UPDATE infrastructure SET active = false WHERE id = $1::uuid`, plant)
	must(err)
	calls = f.calls.Load()
	if rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`); rec.Code != http.StatusNotFound || f.calls.Load() != calls {
		t.Errorf("archivierte Anlage: %d %s", rec.Code, rec.Body.String())
	}
	_, err = pool.Exec(ctx, `UPDATE infrastructure SET active = true WHERE id = $1::uuid`, plant)
	must(err)

	// inaktives Terminal: kein Befehl
	_, err = pool.Exec(ctx, `UPDATE kiosk_terminals SET active = false WHERE id = $1::uuid`, tid)
	must(err)
	if rec = do(tech, "POST", openURL(plant, hmiID), "application/json", `{"mode":"view"}`); rec.Code != http.StatusConflict || f.calls.Load() != calls {
		t.Errorf("inaktiv: %d %s", rec.Code, rec.Body.String())
	}

	// löschen: nur ohne Abteilungsbegrenzung; Anlage verliert die Zuordnung, Protokoll behält den Namen
	if rec = do(scoped, "POST", "/core/settings/kiosk-terminals/"+tid+"/delete", formCT, ""); rec.Code != http.StatusForbidden {
		t.Errorf("löschen mit hmi.manage nur in Halle A: %d", rec.Code)
	}
	if rec = do(admin, "POST", "/core/settings/kiosk-terminals/"+tid+"/delete", formCT, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("löschen: %d", rec.Code)
	}
	must(pool.QueryRow(ctx, `SELECT COALESCE(kiosk_terminal_id::text, '') FROM infrastructure WHERE id = $1::uuid`, plant).Scan(&cur))
	if cur != "" {
		t.Error("Zuordnung nach dem Löschen nicht entfernt")
	}
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM infrastructure_hmi_sessions WHERE hmi_id = $1::uuid AND kiosk_terminal_name = $2 AND kiosk_terminal_id IS NULL`, hmiID, name).Scan(&n))
	if n != 2 {
		t.Errorf("Protokoll nach dem Löschen: %d Zeilen mit Namen", n)
	}
}
