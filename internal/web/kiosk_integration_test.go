//go:build integration

// Kiosk-Terminals gegen eine echte PostgreSQL (Migration 155, Speichern,
// Zuordnung, Befehl „Am Terminal zeigen“, Zugriffsprotokoll).
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
	must(database.RunMigrations(ctx, pool, "../../migrations"))

	// Recht hmi.kiosk für admin, manager, technician
	var n int
	must(pool.QueryRow(ctx, `SELECT COUNT(DISTINCT r.key) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id WHERE p.key = 'hmi.kiosk' AND r.key IN ('admin', 'manager', 'technician')`).Scan(&n))
	if n != 3 {
		t.Errorf("hmi.kiosk bei %d von 3 Rollen", n)
	}

	h := &Handler{db: pool, jwtSecret: "test-geheim", hasPermHook: func(*http.Request, string) bool { return true }}
	r := chi.NewRouter()
	h.kioskRoutes(r)
	h.hmiRoutes(r)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var uid string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Maria', 'Muster', 'technician', '', '') RETURNING id::text`, "k"+sfx).Scan(&uid))
	user := &users.User{ID: uid, Role: users.RoleTechnician, FirstName: "Maria", LastName: "Muster"}
	do := func(method, target, ctype, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req = req.WithContext(context.WithValue(req.Context(), "user", user))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Attrappe des Terminals
	f := &kioskFake{body: `{"status":"opened"}`}
	srv := startKioskFake(t, f)

	// Terminal anlegen (Formular)
	name := "Terminal " + sfx
	form := url.Values{"name": {name}, "base_url": {srv.URL + "/"}, "api_key": {testKioskKey}, "pubkey_sha256": {withColons(kioskPin(srv))}, "active": {"on"}}
	rec := do("POST", "/core/settings/kiosk-terminals", "application/x-www-form-urlencoded", form.Encode())
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
	// gleicher Name → Fehlermeldung statt 500
	rec = do("POST", "/core/settings/kiosk-terminals", "application/x-www-form-urlencoded", form.Encode())
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("doppelter Name angenommen: %s", rec.Header().Get("Location"))
	}
	// ändern ohne Schlüssel: Schlüssel bleibt
	form.Set("terminal_id", tid)
	form.Del("api_key")
	rec = do("POST", "/core/settings/kiosk-terminals", "application/x-www-form-urlencoded", form.Encode())
	var keyEnc2 string
	must(pool.QueryRow(ctx, `SELECT api_key_enc FROM kiosk_terminals WHERE id = $1::uuid`, tid).Scan(&keyEnc2))
	if keyEnc2 != keyEnc {
		t.Error("Schlüssel beim Ändern ohne Eingabe verändert")
	}
	// Verbindung prüfen
	f.body = `{"version":"0.1.7","terminal":"Halle","busy":false}`
	rec = do("POST", "/core/settings/kiosk-terminals/"+tid+"/check", "application/x-www-form-urlencoded", "")
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "0.1.7") || !strings.Contains(loc, "frei") {
		t.Errorf("prüfen: %s", loc)
	}

	// Anlage mit HMI, Terminal zuordnen
	var line, plant, hmiID string
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'line') RETURNING id::text`, "Linie "+sfx).Scan(&line))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ('Presse 2', 'plant', $1::uuid) RETURNING id::text`, line).Scan(&plant))
	defer pool.Exec(ctx, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, plant, line)
	pwEnc, _ := encryptSecret(h.hmiKey(), "vnc-geheim")
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure_hmis (infrastructure_id, name, host, port, password_enc, allow_control)
		VALUES ($1::uuid, 'Panel', '10.9.8.7', 5901, $2, false) RETURNING id::text`, plant, pwEnc).Scan(&hmiID))
	req := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"kiosk_terminal_id": {tid}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	must(h.saveInfraKioskTerminal(ctx, req, plant))

	// HMI-Liste: Knopf „zeigen“, nicht „bedienen“ (am HMI nicht erlaubt)
	rec = do("GET", "/infrastructure/"+plant+"/hmis", "", "")
	var list struct {
		HMIs     []hmiView `json:"hmis"`
		Terminal string    `json:"kiosk_terminal"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &list))
	if len(list.HMIs) != 1 || !list.HMIs[0].KioskView || list.HMIs[0].KioskControl || list.Terminal != name {
		t.Fatalf("HMI-Liste: %s", rec.Body.String())
	}

	// bedienen abgewiesen (am HMI nicht erlaubt), ohne Befehl ans Terminal
	calls := f.calls.Load()
	rec = do("POST", "/infrastructure/"+plant+"/hmis/"+hmiID+"/kiosk", "application/json", `{"mode":"control"}`)
	if rec.Code != http.StatusForbidden || f.calls.Load() != calls {
		t.Fatalf("bedienen: %d %s", rec.Code, rec.Body.String())
	}

	// zeigen → geöffnet
	f.code, f.body = 200, `{"status":"opened"}`
	rec = do("POST", "/infrastructure/"+plant+"/hmis/"+hmiID+"/kiosk", "application/json", `{"mode":"view"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "geöffnet") {
		t.Fatalf("zeigen: %d %s", rec.Code, rec.Body.String())
	}
	var sent kioskOpenRequest
	must(json.Unmarshal(f.lastBody, &sent))
	if sent.Host != "10.9.8.7" || sent.Port != 5901 || sent.Password != "vnc-geheim" || sent.Mode != "view" ||
		sent.Label != "Linie "+sfx+" › Presse 2 / Panel" || sent.User != "Maria Muster" {
		t.Errorf("Befehl: %+v", sent)
	}

	// belegt → 409 mit Meldung
	f.code, f.body = 409, `{"status":"busy","hmi":"Presse 1","since":"2026-11-09T14:05:00+01:00"}`
	rec = do("POST", "/infrastructure/"+plant+"/hmis/"+hmiID+"/kiosk", "application/json", `{"mode":"view"}`)
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
	rec = do("GET", "/infrastructure/"+plant+"/hmis/"+hmiID+"/sessions", "", "")
	if !strings.Contains(rec.Body.String(), `"terminal":"`+name+`"`) {
		t.Errorf("Zugriffe ohne Terminal: %s", rec.Body.String())
	}

	// inaktives Terminal: kein Befehl
	_, err = pool.Exec(ctx, `UPDATE kiosk_terminals SET active = false WHERE id = $1::uuid`, tid)
	must(err)
	calls = f.calls.Load()
	rec = do("POST", "/infrastructure/"+plant+"/hmis/"+hmiID+"/kiosk", "application/json", `{"mode":"view"}`)
	if rec.Code != http.StatusConflict || f.calls.Load() != calls {
		t.Errorf("inaktiv: %d %s", rec.Code, rec.Body.String())
	}

	// löschen: Anlage verliert die Zuordnung, Protokoll behält den Namen
	rec = do("POST", "/core/settings/kiosk-terminals/"+tid+"/delete", "application/x-www-form-urlencoded", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("löschen: %d", rec.Code)
	}
	var still string
	must(pool.QueryRow(ctx, `SELECT COALESCE(kiosk_terminal_id::text, '') FROM infrastructure WHERE id = $1::uuid`, plant).Scan(&still))
	if still != "" {
		t.Error("Zuordnung nach dem Löschen nicht entfernt")
	}
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM infrastructure_hmi_sessions WHERE hmi_id = $1::uuid AND kiosk_terminal_name = $2 AND kiosk_terminal_id IS NULL`, hmiID, name).Scan(&n))
	if n != 2 {
		t.Errorf("Protokoll nach dem Löschen: %d Zeilen mit Namen", n)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, uid)
}
