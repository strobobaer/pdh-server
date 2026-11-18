package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

const cardTestTerm = "22222222-2222-2222-2222-222222222222"

// cardDeviceCookie: Sitzungs-Cookie mit Geräte-Claim (wie bei einem Anlagenterminal).
func cardDeviceCookie(t testing.TB, secret, sub string) *http.Cookie {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "device": "dev-1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "pdh_token", Value: s}
}

// cardPost: ohne Geräte-Cookie (normale Sitzung).
func cardPost(r http.Handler, path, ct, body string, u *users.User) *httptest.ResponseRecorder {
	return cardPostC(r, path, ct, body, u, nil)
}

func cardPostC(r http.Handler, path, ct, body string, u *users.User, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c != nil {
		req.AddCookie(c)
	}
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func cardTestRouter() chi.Router {
	h := &Handler{jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.cardRoutes(r)
	return r
}

func TestTerminalCardRequiresTerminalUser(t *testing.T) {
	r := cardTestRouter()
	normal := &users.User{ID: "11111111-1111-1111-1111-111111111111", Role: users.Role("admin")}
	for _, p := range []string{"/terminal/card", "/terminal/card/ping", "/terminal/card/abmelden"} {
		rec := cardPost(r, p, "application/json", `{"uid":"abc"}`, normal)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur für Terminal-Benutzer") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestTerminalCardBadRequests(t *testing.T) {
	r := cardTestRouter()
	term := &users.User{ID: cardTestTerm, IsSystemUser: true}
	cases := map[string]struct{ ct, body string }{
		"leere UID":         {"application/json", `{"uid":""}`},
		"nur Leerzeichen":   {"application/json", `{"uid":"   "}`},
		"101 Zeichen":       {"application/json", `{"uid":"` + strings.Repeat("a", 101) + `"}`},
		"kein JSON":         {"application/json", `uid=abc`},
		"Form statt JSON":   {"application/x-www-form-urlencoded", `{"uid":"abc"}`},
		"ohne Content-Type": {"", `{"uid":"abc"}`},
		"über 1 KiB":        {"application/json", `{"uid":"a","x":"` + strings.Repeat("b", 1100) + `"}`},
	}
	for name, c := range cases {
		rec := cardPostC(r, "/terminal/card", c.ct, c.body, term, cardDeviceCookie(t, "test-geheim", cardTestTerm))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
}

func TestTerminalCardRateLimited(t *testing.T) {
	r := cardTestRouter()
	term := &users.User{ID: cardTestTerm, IsSystemUser: true}
	cardFailLimit = newFailLimiter(10, time.Minute)
	for i := 0; i < 10; i++ {
		cardFailLimit.fail("card:"+cardTestTerm, time.Now())
	}
	rec := cardPostC(r, "/terminal/card", "application/json", `{"uid":"abc"}`, term, cardDeviceCookie(t, "test-geheim", cardTestTerm))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "Zu viele unbekannte Karten. Bitte kurz warten.") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type: %q", rec.Header().Get("Content-Type"))
	}
	cardFailLimit = newFailLimiter(10, time.Minute)
}

// Terminal-Benutzer ohne Geräte-Claim (z. B. Override-/Passwortsitzung): alle drei Routen 403.
func TestTerminalCardRequiresDeviceSession(t *testing.T) {
	r := cardTestRouter()
	term := &users.User{ID: cardTestTerm, IsSystemUser: true}
	for _, p := range []string{"/terminal/card", "/terminal/card/ping", "/terminal/card/abmelden"} {
		rec := cardPost(r, p, "application/json", `{"uid":"abc"}`, term)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur für Terminal-Benutzer") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}
