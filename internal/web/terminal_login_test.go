package web

import (
	"net/http"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

func TestLoginKindAllowed(t *testing.T) {
	cases := []struct {
		name string
		s    terminalLoginSettings
		kind string
		want bool
	}{
		{"password mit Knopf bei card_pin", terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}, loginPassword, true},
		{"password ohne Knopf bei card_pin", terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: false}, loginPassword, false},
		{"password mit Knopf bei card_only", terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: true}, loginPassword, true},
		{"password ohne Knopf bei password", terminalLoginSettings{ManualLogin: loginPassword, PasswordButton: false}, loginPassword, true},
		{"password mit Knopf bei password", terminalLoginSettings{ManualLogin: loginPassword, PasswordButton: true}, loginPassword, true},
		{"password bei none mit Knopf", terminalLoginSettings{ManualLogin: loginNone, PasswordButton: true}, loginPassword, false},
		{"password bei none ohne Knopf", terminalLoginSettings{ManualLogin: loginNone, PasswordButton: false}, loginPassword, false},
		{"card_pin bei card_pin", terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: false}, loginCardPIN, true},
		{"card_pin bei card_only", terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: true}, loginCardPIN, false},
		{"card_pin bei password", terminalLoginSettings{ManualLogin: loginPassword, PasswordButton: true}, loginCardPIN, false},
		{"card_pin bei none", terminalLoginSettings{ManualLogin: loginNone, PasswordButton: true}, loginCardPIN, false},
		{"card_only bei card_only", terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: false}, loginCardOnly, true},
		{"card_only bei card_pin", terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}, loginCardOnly, false},
		{"card_only bei none", terminalLoginSettings{ManualLogin: loginNone, PasswordButton: true}, loginCardOnly, false},
		{"unbekannte Art", terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}, "foo", false},
		{"leere Art", terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: true}, "", false},
	}
	for _, c := range cases {
		if got := loginKindAllowed(c.s, c.kind); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTerminalLoginRequiresDeviceSession(t *testing.T) {
	r := cardTestRouter()
	body := `{"kind":"password","user":"a","password":"b"}`
	normal := &users.User{ID: "11111111-1111-1111-1111-111111111111", Role: users.Role("admin")}
	if rec := cardPost(r, "/terminal/login", "application/json", body, normal); rec.Code != http.StatusForbidden {
		t.Errorf("normaler Benutzer: %d", rec.Code)
	}
	// Terminal-Benutzer, aber ohne Geräte-Cookie (Override-/Passwortsitzung).
	term := &users.User{ID: cardTestTerm, IsSystemUser: true}
	if rec := cardPost(r, "/terminal/login", "application/json", body, term); rec.Code != http.StatusForbidden {
		t.Errorf("ohne Gerätesitzung: %d", rec.Code)
	}
}

func TestTerminalLoginBadRequests(t *testing.T) {
	r := cardTestRouter()
	term := &users.User{ID: cardTestTerm, IsSystemUser: true}
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]struct{ ct, body string }{
		"falscher Content-Type": {"text/plain", `{"kind":"card_only","card":"abc"}`},
		"ohne Content-Type":     {"", `{"kind":"card_only","card":"abc"}`},
		"kein JSON":             {"application/json", `kind=card_only`},
		"unbekannte Art":        {"application/json", `{"kind":"foo"}`},
		"Art fehlt":             {"application/json", `{"card":"abc"}`},
		"Karte leer":            {"application/json", `{"kind":"card_only","card":"   "}`},
		"Karte 101 Zeichen":     {"application/json", `{"kind":"card_only","card":"` + long(101) + `"}`},
		"PIN leer":              {"application/json", `{"kind":"card_pin","card":"abc","pin":""}`},
		"PIN 9 Zeichen":         {"application/json", `{"kind":"card_pin","card":"abc","pin":"123456789"}`},
		"Benutzer leer":         {"application/json", `{"kind":"password","user":"","password":"x"}`},
		"Benutzer 201":          {"application/json", `{"kind":"password","user":"` + long(201) + `","password":"x"}`},
		"Passwort leer":         {"application/json", `{"kind":"password","user":"a","password":""}`},
		"Passwort 201":          {"application/json", `{"kind":"password","user":"a","password":"` + long(201) + `"}`},
		"über 1 KiB":            {"application/json", `{"kind":"card_only","card":"a","x":"` + long(1100) + `"}`},
	}
	for name, c := range cases {
		rec := cardPostC(r, "/terminal/login", c.ct, c.body, term, cardDeviceCookie(t, "test-geheim", cardTestTerm))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
}
