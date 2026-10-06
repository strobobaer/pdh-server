package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Ohne Karte gibt es keinen Zugang und keine Cookies.
func TestGlobalDashboardOpenNeedsCard(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.GlobalDashboardOpen(rec, httptest.NewRequest(http.MethodPost, "/global/open", strings.NewReader(`{"rfid_uid":"  ","next":"/faults/x"}`)))
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("ohne Karte: %d, Cookies %v", rec.Code, rec.Result().Cookies())
	}
}

// Abmelden beendet auch den Kurzzugang aus dem Leitstand.
func TestLogoutEndsBoardVisit(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: boardVisitCookie, Value: "1"})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == boardVisitCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared || rec.Header().Get("Location") != "/global/" {
		t.Fatalf("Kurzzugang nicht beendet: %v → %s", rec.Result().Cookies(), rec.Header().Get("Location"))
	}
}

// Ohne Anmeldung fragt der Leitstand beim Öffnen die Karte ab.
func TestGlobalDashboardAsksCardToOpen(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	for _, loggedIn := range []bool{false, true} {
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "global_dashboard.gohtml", GlobalDashboardPageData{LoggedIn: loggedIn}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		want := "const LOGGED_IN = false;"
		if loggedIn {
			want = "const LOGGED_IN = true;"
		}
		for _, s := range []string{want, `id="open-rfid"`, "/global/open", "openPage(t.url"} {
			if !strings.Contains(out, s) {
				t.Errorf("LoggedIn=%v: %q fehlt", loggedIn, s)
			}
		}
		if strings.Contains(out, "window.location = t.url") {
			t.Error("Zeitstrahl öffnet noch ohne Kartenabfrage")
		}
	}
}
