package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

const cardTestTerm = "22222222-2222-2222-2222-222222222222"

func cardPost(r http.Handler, path, ct, body string, u *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
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
		rec := cardPost(r, "/terminal/card", c.ct, c.body, term)
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
	rec := cardPost(r, "/terminal/card", "application/json", `{"uid":"abc"}`, term)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "Zu viele unbekannte Karten. Bitte kurz warten.") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type: %q", rec.Header().Get("Content-Type"))
	}
	cardFailLimit = newFailLimiter(10, time.Minute)
}
