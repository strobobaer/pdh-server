//go:build integration

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

// M12: Anfangswerte der Terminalseite (Anmeldeart, Knopf) kommen aus terminalLoginSettings.
func TestTerminalPageInitialLoginSettings(t *testing.T) {
	e := newATEnv(t)
	term := e.user("pgterm", "viewer", true)
	dept := e.dept("pgdept")
	infra := e.infra("Presse pg", dept, "")
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, infra)
	t.Cleanup(func() { e.exec(`UPDATE users SET terminal_infrastructure_id = NULL WHERE id = $1::uuid`, term) })
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim"}

	gate := func() terminalPage {
		claims := jwt.MapClaims{"sub": term, "device": "dev-x", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.jwtSecret))
		e.must(err)
		req := httptest.NewRequest("GET", "/terminal/info", nil)
		req.AddCookie(&http.Cookie{Name: "pdh_token", Value: s})
		req = withUser(req, &users.User{ID: term, IsSystemUser: true, Role: users.Role("viewer")})
		p, ok := h.terminalGate(httptest.NewRecorder(), req, "info")
		if !ok {
			t.Fatal("terminalGate abgelehnt")
		}
		return p
	}
	if p := gate(); p.ManualLogin != loginCardPIN || !p.PasswordButton {
		t.Fatalf("Standard: %q %v", p.ManualLogin, p.PasswordButton)
	} else if p.CardNotify != `["overlay"]` {
		t.Fatalf("Standard card_notify: %q", p.CardNotify)
	}
	e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: false, CardNotify: []string{"banner", "toast"}}))
	if p := gate(); p.ManualLogin != loginCardOnly || p.PasswordButton || p.CardNotify != `["toast","banner"]` {
		t.Fatalf("aus terminal_settings: %q %v %q", p.ManualLogin, p.PasswordButton, p.CardNotify)
	}
}
