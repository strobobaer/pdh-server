//go:build integration

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

// M7: Speichern von Rückkehrzeit, Anmeldeart und globaler Rückkehrzeit löst ein
// „settings“-Ereignis an die offenen Terminals aus.
func TestTerminalSettingsPublishEvents(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("pubmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	term := e.user("pubterm", "viewer", true)
	other := e.user("pubother", "viewer", true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	r.Post("/core/settings/terminal", h.TerminalSettingsWeb)
	post := func(target, body string) {
		t.Helper()
		req := httptest.NewRequest("POST", target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = withUser(req, &users.User{ID: mgr, Role: users.Role("admin")})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Header().Get("Location"))
		}
	}
	chTerm, unsub := h.eventHub().subscribe(term)
	defer unsub()
	chOther, unsub2 := h.eventHub().subscribe(other)
	defer unsub2()
	expect := func(label string, ch <-chan string) {
		t.Helper()
		if ev, ok := recvEvent(t, ch); !ok || ev != "settings" {
			t.Fatalf("%s: %q %v", label, ev, ok)
		}
	}

	post("/users/"+term+"/master", "terminal_idle_seconds=90")
	expect("Rückkehrzeit", chTerm)
	noEvent(t, chOther)

	post("/users/"+term+"/master", "terminal_manual_login=password&terminal_password_button=on")
	expect("Anmeldeart", chTerm)
	noEvent(t, chOther)

	post("/core/settings/terminal", "idle_seconds=200")
	expect("global (Terminal)", chTerm)
	expect("global (anderes Terminal)", chOther)
	t.Cleanup(func() { e.exec(`DELETE FROM app_settings WHERE key = $1`, KeyTerminalIdle) })
}
