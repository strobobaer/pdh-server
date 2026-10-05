package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

func renderLogin(t *testing.T, d LoginData) string {
	t.Helper()
	root := filepath.Join("..", "..", "web", "templates")
	tmpl, err := template.New("login.gohtml").Funcs(TemplateFuncs()).ParseFiles(filepath.Join(root, "login.gohtml"), filepath.Join(root, "widgets", "lang_switch.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestLoginPasswordModes(t *testing.T) {
	cases := []struct {
		d          LoginData
		want, miss []string
	}{
		{LoginData{}, []string{`action="/login"`, `href="/login/forgot"`, `action="/login/rfid"`}, []string{`action="/login/reset"`}},
		{LoginData{Mode: "forgot"}, []string{`action="/login/forgot"`, `href="/login"`, `value="/login/forgot"`}, []string{`action="/login/rfid"`, `name="password"`}},
		{LoginData{Mode: "forgot", Notice: "Link verschickt"}, []string{"Link verschickt"}, []string{`action="/login/forgot"`}},
		{LoginData{Mode: "reset", Token: "abc_-123"}, []string{`action="/login/reset"`, `name="token" value="abc_-123"`, `name="password2"`}, []string{`action="/login/rfid"`}},
	}
	for _, c := range cases {
		out := renderLogin(t, c.d)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("Modus %q: %q fehlt", c.d.Mode, w)
			}
		}
		for _, m := range c.miss {
			if strings.Contains(out, m) {
				t.Errorf("Modus %q: %q sollte fehlen", c.d.Mode, m)
			}
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for pw, ok := range map[string]bool{"": false, "kurz": false, "1234567": false, "12345678": true, "äöüßäöüß": true, strings.Repeat("x", 72): true, strings.Repeat("x", 73): false} {
		if err := users.ValidatePassword(pw); (err == nil) != ok {
			t.Errorf("ValidatePassword(%q) = %v, erwartet ok=%v", pw, err, ok)
		}
	}
}

func TestResetTokenHashAndBaseURL(t *testing.T) {
	if a, b := hashResetToken("x"), hashResetToken("x"); a != b || len(a) != 64 || hashResetToken("y") == a {
		t.Errorf("hashResetToken nicht stabil/eindeutig: %s", a)
	}
	// Der Link darf nie aus dem Host der Anfrage gebaut werden.
	h := &Handler{}
	h.mailCfg.Host, h.mailCfg.From = "smtp.example", "pdh@example"
	if h.passwordResetAvailable() {
		t.Error("ohne PDH_PUBLIC_URL darf kein Ruecksetz-Link verschickt werden")
	}
	h.mailCfg.PublicURL = "https://pdh.firma.de/ "
	if !h.passwordResetAvailable() || h.passwordResetBaseURL() != "https://pdh.firma.de" {
		t.Errorf("Basis-URL: %q", h.passwordResetBaseURL())
	}
}

func TestLoginSubpathsArePublic(t *testing.T) {
	h := &Handler{}
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	for _, p := range []string{"/login", "/login/forgot", "/login/reset", "/login/rfid"} {
		reached = false
		h.authMiddleware(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
		if !reached {
			t.Errorf("%s ist nicht ohne Anmeldung erreichbar", p)
		}
	}
}
