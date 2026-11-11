//go:build integration

// Kopplungscode für Terminal-Benutzer gegen eine echte PostgreSQL (Migration 157).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalPairCode ./internal/web/
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestTerminalPairCode(t *testing.T) {
	e := newATEnv(t)
	admin := e.user("pa", "admin", false)
	e.grant(admin, "system.manage_users", "", true)
	plain := e.user("pp", "viewer", false)
	e.grant(plain, "system.manage_users", "", false)
	term := e.user("pt", "viewer", true)
	normal := e.user("pn", "viewer", false)
	e.warm()

	tmpl := loadTestTemplates(t) // vor dem Wechsel des Arbeitsverzeichnisses
	// Die Seite lädt ihre Vorlage relativ zum Arbeitsverzeichnis (Repo-Wurzel).
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: tmpl}
	r := chi.NewRouter()
	r.Post("/users/{id}/terminal-code", h.TerminalPairCodeWeb)
	post := func(target, actor, role string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest("POST", "/users/"+target+"/terminal-code", nil), &users.User{ID: actor, Role: users.Role(role)})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	open := func() int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_pairing_codes WHERE user_id = $1::uuid AND used_at IS NULL AND expires_at > NOW()`, term).Scan(&n))
		return n
	}
	pat := regexp.MustCompile(`\b[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}\b`)

	rec := post(term, admin, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("Code erzeugen: %d %.300s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	code := pat.FindString(body)
	if code == "" || !strings.Contains(body, "10 Minuten") {
		t.Errorf("Seite ohne Code-Muster oder Gültigkeit: %.500s", body)
	}
	if n := open(); n != 1 {
		t.Errorf("offene Codes nach erstem Aufruf: %d", n)
	}
	var stored string
	e.must(e.pool.QueryRow(e.ctx, `SELECT code_sha256 FROM terminal_pairing_codes WHERE user_id = $1::uuid`, term).Scan(&stored))
	if stored != hashPairingCode(normalizePairingCode(code)) {
		t.Error("gespeicherter Hash passt nicht zum angezeigten Code")
	}

	rec = post(term, admin, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("zweiter Aufruf: %d", rec.Code)
	}
	if n := open(); n != 1 {
		t.Errorf("offene Codes nach zweitem Aufruf: %d", n)
	}
	if code2 := pat.FindString(rec.Body.String()); code2 == code {
		t.Error("zweiter Code ist gleich dem ersten")
	}

	rec = post(normal, admin, "admin")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Nur für Terminal-Benutzer") {
		t.Errorf("Nicht-System-Benutzer: %d %.300s", rec.Code, rec.Body.String())
	}
	if pat.MatchString(rec.Body.String()) {
		t.Error("Fehlerseite enthält einen Code")
	}

	if rec = post(term, plain, "viewer"); rec.Code != http.StatusForbidden {
		t.Errorf("ohne system.manage_users: %d", rec.Code)
	}
	if n := open(); n != 1 {
		t.Errorf("offene Codes nach verbotenem Aufruf: %d", n)
	}
}

// Geräteanmeldung und Sperrprüfung in der Sitzung (POST /terminal/geraet).
func TestTerminalDeviceLogin(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	terminalDevices = newDeviceCache(30 * time.Second)
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	e := newATEnv(t)
	term := e.user("dl", "viewer", true)
	admin := e.user("dla", "admin", false)
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-secret", tmpl: loadTestTemplates(t)}
	routes := h.Routes()

	secret, err := newDeviceSecret()
	e.must(err)
	devID := e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi', $2) RETURNING id::text`, term, hashDeviceSecret(secret))
	t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_devices WHERE id = $1::uuid`, devID) })

	login := func(ip, sec string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/terminal/geraet", strings.NewReader(url.Values{"secret": {sec}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = ip + ":5000"
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}
	cookies := func(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
		m := map[string]*http.Cookie{}
		for _, c := range rec.Result().Cookies() {
			m[c.Name] = c
		}
		return m
	}
	get := func(p string, cs ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", p, nil)
		for _, c := range cs {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}

	rec := login("198.51.100.20", secret)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/terminal" {
		t.Fatalf("Anmeldung: %d %s %.200s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	cs := cookies(rec)
	tok, uid := cs["pdh_token"], cs["pdh_user_id"]
	if tok == nil || uid == nil || tok.MaxAge != deviceCookieMaxAge || uid.MaxAge != deviceCookieMaxAge || uid.Value != term {
		t.Fatalf("Cookies: %+v %+v", tok, uid)
	}
	if rt := cs["pdh_return_token"]; rt == nil || rt.MaxAge >= 0 {
		t.Errorf("pdh_return_token nicht gelöscht: %+v", rt)
	}
	var seen bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT last_seen_at IS NOT NULL FROM terminal_devices WHERE id = $1::uuid`, devID).Scan(&seen))
	if !seen {
		t.Error("last_seen_at nicht gesetzt")
	}
	if rec := get("/terminal", tok, uid); rec.Code != 200 {
		t.Fatalf("GET /terminal mit Gerätesitzung: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Wartungssperre: Gerätesitzung bleibt, normale Sitzung nicht
	normalTok, err := h.users.IssueToken(&users.User{ID: admin, Role: users.Role("viewer")}, time.Hour, nil)
	e.must(err)
	h.sessionNotBefore.Store(time.Now().Add(time.Second).Unix())
	if rec := get("/terminal", tok, uid); rec.Code != 200 {
		t.Errorf("Wartungssperre, Gerätesitzung: %d", rec.Code)
	}
	if rec := get("/terminal", &http.Cookie{Name: "pdh_token", Value: normalTok}); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/global/" {
		t.Errorf("Wartungssperre, normale Sitzung: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	h.sessionNotBefore.Store(0)

	// Sperren: nach Zwischenspeicher-Verfall bzw. forget keine Sitzung mehr
	e.exec(`UPDATE terminal_devices SET revoked_at = NOW() WHERE id = $1::uuid`, devID)
	terminalDevices.forget(devID)
	if rec := get("/terminal", tok, uid); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/global/" {
		t.Errorf("gesperrt: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := login("198.51.100.21", secret); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Gerät nicht gekoppelt oder gesperrt. Bitte in der Admin-Seite des Terminals neu koppeln.") {
		t.Errorf("gesperrtes Geheimnis: %d %.300s", rec.Code, rec.Body.String())
	}
	e.exec(`UPDATE terminal_devices SET revoked_at = NULL WHERE id = $1::uuid`, devID)
	terminalDevices.forget(devID)
	if rec := get("/terminal", tok, uid); rec.Code != 200 {
		t.Errorf("entsperrt: %d", rec.Code)
	}

	// Benutzer deaktiviert / kein Terminal-Benutzer mehr → Sitzung und Anmeldung abgewiesen
	e.exec(`UPDATE users SET is_system_user = false WHERE id = $1::uuid`, term)
	terminalDevices.forget(devID)
	if rec := get("/terminal", tok, uid); rec.Code != http.StatusFound {
		t.Errorf("kein System-Benutzer, Sitzung: %d", rec.Code)
	}
	if rec := login("198.51.100.22", secret); rec.Code != http.StatusForbidden {
		t.Errorf("kein System-Benutzer, Anmeldung: %d", rec.Code)
	}
	e.exec(`UPDATE users SET is_system_user = true WHERE id = $1::uuid`, term)

	// falsches Geheimnis: 403 mit Text, nach 5 Fehlversuchen 429
	for i := 0; i < 5; i++ {
		if rec := login("198.51.100.30", "pdht_falsch"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Gerät nicht gekoppelt oder gesperrt") {
			t.Fatalf("falsches Geheimnis #%d: %d %.200s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := login("198.51.100.30", secret); rec.Code != http.StatusTooManyRequests {
		t.Errorf("Sperre nach Fehlversuchen: %d", rec.Code)
	}

	// Override-Rückkehr mit Geräte-Token: 400-Tage-Cookie bleibt, sonst 1 Tag
	devTok, err := h.users.IssueToken(&users.User{ID: term, Role: users.Role("viewer")}, time.Hour, map[string]interface{}{"device": devID})
	e.must(err)
	back := func(token string) map[string]*http.Cookie {
		req := httptest.NewRequest("POST", "/override-logout", nil)
		req.AddCookie(&http.Cookie{Name: "pdh_return_token", Value: token})
		rec := httptest.NewRecorder()
		h.OverrideLogoutWeb(rec, req)
		return cookies(rec)
	}
	if c := back(devTok); c["pdh_token"] == nil || c["pdh_token"].MaxAge != deviceCookieMaxAge || c["pdh_user_id"].MaxAge != deviceCookieMaxAge {
		t.Errorf("Override-Rückkehr Gerät: %+v", c["pdh_token"])
	}
	if c := back(normalTok); c["pdh_token"] == nil || c["pdh_token"].MaxAge != 86400 {
		t.Errorf("Override-Rückkehr normal: %+v", c["pdh_token"])
	}
}
