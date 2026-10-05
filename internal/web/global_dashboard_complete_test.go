package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"pdh/pkg/middleware"
)

const boardTestSecret = "test-signing-secret-with-more-than-32-characters"

func boardTestToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(boardTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScopedTokenAllows(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		ref, method, path string
		ok                bool
	}{
		{"fault:" + id, "GET", "/api/v1/faults/" + id + "/pending-parts", true},
		{"fault:" + id, "POST", "/api/v1/faults/" + id + "/pending-parts", true},
		{"fault:" + id, "DELETE", "/api/v1/faults/" + id + "/pending-parts/abc", true},
		{"maintenance:" + id, "GET", "/api/v1/maintenance/tasks/" + id + "/pending-parts", true},
		{"ticket:" + id, "GET", "/api/v1/storage/", true},
		{"ticket:" + id, "GET", "/api/v1/inventory/", true},
		// alles andere nicht
		{"fault:" + id, "GET", "/api/v1/faults/22222222-2222-2222-2222-222222222222/pending-parts", false},
		{"fault:" + id, "GET", "/api/v1/tickets/" + id + "/pending-parts", false},
		{"fault:" + id, "PUT", "/api/v1/faults/" + id, false},
		{"fault:" + id, "GET", "/api/v1/faults/", false},
		{"fault:" + id, "POST", "/api/v1/inventory/", false},
		{"fault:" + id, "DELETE", "/api/v1/faults/" + id + "/pending-parts/a/b", false},
		{"fault:" + id, "GET", "/api/v1/users/", false},
		{"bogus:" + id, "GET", "/api/v1/storage/", true}, // Lesen der Lagerorte ist zweckneutral
		{"bogus:" + id, "GET", "/api/v1/bogus/" + id + "/pending-parts", false},
	}
	for _, c := range cases {
		if got := middleware.ScopedTokenAllows("board-complete", c.ref, c.method, c.path); got != c.ok {
			t.Errorf("%s %s (ref %s) = %v, erwartet %v", c.method, c.path, c.ref, got, c.ok)
		}
	}
	if middleware.ScopedTokenAllows("anderer-zweck", "fault:"+id, "GET", "/api/v1/storage/") {
		t.Error("unbekannter Zweck darf nichts")
	}
}

func TestBoardTokenIsNoSession(t *testing.T) {
	h := &Handler{jwtSecret: boardTestSecret}
	tok := boardTestToken(t, jwt.MapClaims{"sub": "u1", "scope": boardCompleteScope, "ref": "fault:x", "exp": time.Now().Add(time.Minute).Unix()})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "pdh_token", Value: tok})
	if got := h.sessionUserID(r); got != "" {
		t.Errorf("Leitstand-Token als Cookie ergab Sitzung für %q", got)
	}
	normal := boardTestToken(t, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Minute).Unix()})
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "pdh_token", Value: normal})
	if got := h.sessionUserID(r); got != "u1" {
		t.Errorf("normale Sitzung: %q", got)
	}
}

func TestBoardCompleteUserOnlyForItsRecord(t *testing.T) {
	// Pfad- und Zweckpruefung greifen vor dem Laden des Benutzers (users = nil)
	h := &Handler{jwtSecret: boardTestSecret}
	const id = "11111111-1111-1111-1111-111111111111"
	tok := boardTestToken(t, jwt.MapClaims{"sub": "", "scope": boardCompleteScope, "ref": "fault:" + id, "exp": time.Now().Add(time.Minute).Unix()})
	for _, p := range []string{"/", "/faults/" + id, "/complete/ticket/" + id, "/complete/fault/22222222-2222-2222-2222-222222222222"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		if h.boardCompleteUser(r) != nil {
			t.Errorf("%s: Leitstand-Token darf hier nicht gelten", p)
		}
	}
	expired := boardTestToken(t, jwt.MapClaims{"sub": "u1", "scope": boardCompleteScope, "ref": "fault:" + id, "exp": time.Now().Add(-time.Minute).Unix()})
	r := httptest.NewRequest(http.MethodGet, "/complete/fault/"+id, nil)
	r.Header.Set("Authorization", "Bearer "+expired)
	if h.boardCompleteUser(r) != nil {
		t.Error("abgelaufenes Token akzeptiert")
	}
	plain := boardTestToken(t, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Minute).Unix()})
	r = httptest.NewRequest(http.MethodGet, "/complete/fault/"+id, nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	if h.boardCompleteUser(r) != nil {
		t.Error("Token ohne Zweck darf den Leitstand-Weg nicht nutzen")
	}
}

func TestGlobalDashboardStartsGuidedCompletion(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	tmpl := loadTestTemplates(t)
	c, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join(root, "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "global_dashboard.gohtml", GlobalDashboardPageData{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"/global/complete-start", "pdhComplete(item.type_key", `id="cw"`, "Fertigmeldung starten", "Authorization"} {
		if !strings.Contains(out, want) {
			t.Errorf("Leitstand enthält %q nicht", want)
		}
	}
	checkScripts(t, "leitstand", out)
}
