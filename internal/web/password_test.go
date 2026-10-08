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
		{LoginData{Mode: "reset", Token: "abc_-123", Rules: []string{"mindestens 10 Zeichen"}}, []string{`action="/login/reset"`, `name="token" value="abc_-123"`, `name="password2"`, "<li>mindestens 10 Zeichen</li>"}, []string{`action="/login/rfid"`}},
		{LoginData{Mode: "change", Next: "/tickets", Notice: "Bitte vergib jetzt ein eigenes Passwort", Rules: []string{"eine Ziffer"}},
			[]string{`action="/login/change"`, `name="next" value="/tickets"`, `name="password2"`, "Bitte vergib jetzt ein eigenes Passwort", "<li>eine Ziffer</li>", `value="/login/change"`},
			[]string{`action="/login/rfid"`, `name="token"`, `name="current"`}},
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

func TestPasswordPolicyValidate(t *testing.T) {
	def := users.DefaultPolicy()
	for pw, ok := range map[string]bool{"": false, "kurz": false, "1234567": false, "12345678": true, "äöüßäöüß": true, strings.Repeat("x", 72): true, strings.Repeat("x", 73): false} {
		if err := def.Validate(pw); (err == nil) != ok {
			t.Errorf("Standard: Validate(%q) = %v, erwartet ok=%v", pw, err, ok)
		}
	}
	strict := users.PasswordPolicy{MinLength: 10, RequireUpper: true, RequireLower: true, RequireDigit: true, RequireSpecial: true}
	for pw, ok := range map[string]bool{"Sommer2026!": true, "sommer2026!": false, "SOMMER2026!": false, "Sommersonne!": false, "Sommer20261": false, "Som2026!": false, "Ärger-2026x": true} {
		if err := strict.Validate(pw); (err == nil) != ok {
			t.Errorf("streng: Validate(%q) = %v, erwartet ok=%v", pw, err, ok)
		}
	}
	if err := strict.Validate("sommer"); err == nil || !strings.Contains(err.Error(), "Großbuchstabe") || !strings.Contains(err.Error(), "mindestens 10 Zeichen") {
		t.Errorf("Fehlermeldung nennt nicht alles Fehlende: %v", err)
	}
	// Grenzen: unter 8 Zeichen und ueber 72 gibt es nicht
	n := users.PasswordPolicy{MinLength: 3, MaxAgeDays: -5, History: 99}.Normalize()
	if n.MinLength != 8 || n.MaxAgeDays != 0 || n.History != users.MaxHistory {
		t.Errorf("Normalize: %+v", n)
	}
	if !def.ChangeOnFirstLogin || !def.ChangeAfterAdminReset {
		t.Error("Standard: Wechselzwang bei Erstanmeldung und nach Admin-Reset muss an sein")
	}
	if r := strict.Rules(); len(r) != 5 || r[0] != "mindestens 10 Zeichen" {
		t.Errorf("Regeln: %v", r)
	}
	if !(users.PasswordState{Expired: true}).Required() || (users.PasswordState{}).Required() {
		t.Error("PasswordState.Required")
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
	for _, p := range []string{"/login", "/login/forgot", "/login/reset", "/login/rfid", "/login/change"} {
		reached = false
		h.authMiddleware(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
		if !reached {
			t.Errorf("%s ist nicht ohne Anmeldung erreichbar", p)
		}
	}
}

func TestPasswordPolicyPages(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := users.PasswordPolicy{MinLength: 12, RequireDigit: true, MaxAgeDays: 90, History: 5, ChangeOnFirstLogin: true}
	out := renderPage(t, tmpl, "server_config", ServerConfigData{Policy: &p, PasswordPending: 3})
	for _, want := range []string{`data-tab="passwords"`, `action="/admin/server-config/password-policy"`, `name="min_length" min="8" max="72" value="12"`,
		`name="require_digit" checked`, `name="change_on_first_login" checked`, `name="max_age_days" min="0" max="3650" value="90"`, "3 Benutzer müssen ihr Passwort"} {
		if !strings.Contains(out, want) {
			t.Errorf("Server-Einstellungen: %q fehlt", want)
		}
	}
	if strings.Contains(out, `name="change_after_admin_reset" checked`) {
		t.Error("Admin-Reset-Zwang ist aus, aber angehakt")
	}
	ud := renderPage(t, tmpl, "user_detail", UserDetailData{User: UserView{ID: "u1"}, CanEditCore: true, CanEditMaster: true,
		AdminPasswordHint: "Richtlinie: mindestens 12 Zeichen. Die Person muss es bei der nächsten Anmeldung ändern.",
		Password:          users.PasswordState{MustChange: true}})
	for _, want := range []string{"Die Person muss es bei der nächsten Anmeldung ändern.", "Wechsel bei nächster Anmeldung", `hx-post="/users/u1/password-change"`} {
		if !strings.Contains(ud, want) {
			t.Errorf("Benutzerstamm: %q fehlt", want)
		}
	}
}
