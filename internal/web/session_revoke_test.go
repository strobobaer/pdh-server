package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Abmelden zieht genau das Token dieser Sitzung am Server zurück (0.82.66): ein vorher
// kopiertes Token gilt danach nicht mehr, andere Geräte bleiben angemeldet, das
// Rückkehr-Token des Terminals bleibt gültig.

func testSessionToken(t *testing.T, secret, sub, nonce string, extra jwt.MapClaims) string {
	t.Helper()
	now := time.Now()
	c := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "n": nonce}
	for k, v := range extra {
		c[k] = v
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func reqWithToken(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "pdh_token", Value: raw})
	return req
}

func TestLogoutRevokesToken(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	pc := testSessionToken(t, h.jwtSecret, "u1", "pc", nil)
	phone := testSessionToken(t, h.jwtSecret, "u1", "handy", nil)
	if sub, _ := h.sessionClaims(reqWithToken(pc)); sub != "u1" {
		t.Fatalf("Sitzung vor dem Abmelden ungültig: %q", sub)
	}
	ret := testSessionToken(t, h.jwtSecret, "term1", "geraet", jwt.MapClaims{"device": "dev1"})
	req := reqWithToken(pc)
	req.AddCookie(&http.Cookie{Name: "pdh_return_token", Value: ret})
	h.Logout(httptest.NewRecorder(), req)
	if sub, _ := h.sessionClaims(reqWithToken(pc)); sub != "" {
		t.Error("kopiertes Token gilt nach dem Abmelden weiter")
	}
	if sub, _ := h.sessionClaims(reqWithToken(phone)); sub != "u1" {
		t.Error("Abmelden am PC hat das Handy abgemeldet")
	}
	if h.sessionTokenRevoked(req.Context(), ret) {
		t.Error("Rückkehr-Token des Terminals zurückgezogen")
	}
	// ohne gültige Sitzung (kaputtes Cookie) passiert nichts
	h.Logout(httptest.NewRecorder(), reqWithToken("kaputt"))
}
