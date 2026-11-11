//go:build integration

// Kopplungscode für Terminal-Benutzer gegen eine echte PostgreSQL (Migration 157).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalPairCode ./internal/web/
package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

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
