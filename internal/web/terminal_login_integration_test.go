//go:build integration

// POST /terminal/login: Anmeldung am Anlagenterminal ohne Kartenleser.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalLogin ./internal/web/
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestTerminalLoginHandler(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	h.jwtSecret = "test-geheim"
	h.tmpl = loadTestTemplates(t)
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	cardFailLimit = newFailLimiter(10, time.Minute)
	t.Cleanup(func() { cardFailLimit = newFailLimiter(10, time.Minute) })

	term := e.user("lgterm", "user", true)
	t.Cleanup(func() { e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })
	pin := e.user("lgpin", "user", false)
	nopin := e.user("lgnopin", "user", false)
	inactive := e.user("lginact", "user", false)
	sys := e.user("lgsys", "user", true)
	pw := e.user("lgpw", "user", false)
	uidOf := func(id, uid string) { e.exec(`UPDATE users SET rfid_uid = $2 WHERE id = $1::uuid`, id, uid+e.sfx) }
	uidOf(pin, "DEADBEEF")
	uidOf(nopin, "NOPIN-CARD")
	uidOf(inactive, "INACT-CARD")
	uidOf(sys, "SYS-CARD")
	e.must(h.setTerminalPIN(e.ctx, pin, "2580"))
	e.must(h.setTerminalPIN(e.ctx, inactive, "2580"))
	e.must(h.setTerminalPIN(e.ctx, sys, "2580"))
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, inactive)
	hash, err := bcrypt.GenerateFromPassword([]byte("geheim-pw"), bcrypt.MinCost)
	e.must(err)
	e.exec(`UPDATE users SET password_hash = $2 WHERE id = $1::uuid`, pw, string(hash))

	setKind := func(kind string, button bool) {
		e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: kind, PasswordButton: button}))
	}
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		rec := cardHTTP(e, h, term, "/terminal/login", body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	cardBody := func(kind, card, p string) string {
		b, _ := json.Marshal(map[string]string{"kind": kind, "card": card, "pin": p})
		return string(b)
	}
	pwBody := func(user, p string) string {
		b, _ := json.Marshal(map[string]string{"kind": "password", "user": user, "password": p})
		return string(b)
	}
	failed := func(label string, rec *httptest.ResponseRecorder, m map[string]any) {
		t.Helper()
		if rec.Code != 200 || m["ok"] != false || m["message"] != "Anmeldung fehlgeschlagen" {
			t.Fatalf("%s: %d %v", label, rec.Code, m)
		}
	}
	reset := func() {
		cardFailLimit = newFailLimiter(10, time.Minute)
		e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term)
	}

	// card_pin
	setKind(loginCardPIN, true)
	rec, m := post(cardBody("card_pin", "deadbeef"+e.sfx, "2580"))
	if rec.Code != 200 || m["ok"] != true || m["name"] != "T U" || cardRows(e, term) != 1 {
		t.Fatalf("card_pin richtig: %d %v", rec.Code, m)
	}
	reset()
	// Kartennummer mit Leerraum/Kleinbuchstaben (Review Focus 4)
	rec, m = post(cardBody("card_pin", "  deadbeef"+e.sfx+" ", "2580"))
	if m["ok"] != true {
		t.Fatalf("Leerraum: %d %v", rec.Code, m)
	}
	reset()
	for label, body := range map[string]string{
		"falsche PIN":      cardBody("card_pin", "DEADBEEF"+e.sfx, "9999"),
		"unbekannte Karte": cardBody("card_pin", "NOPE"+e.sfx, "2580"),
		"ohne PIN":         cardBody("card_pin", "NOPIN-CARD"+e.sfx, "2580"), // Review Focus 2
		"Systembenutzer":   cardBody("card_pin", "SYS-CARD"+e.sfx, "2580"),
		"inaktiv":          cardBody("card_pin", "INACT-CARD"+e.sfx, "2580"),
	} {
		rec, m = post(body)
		failed(label, rec, m)
	}
	if cardRows(e, term) != 0 {
		t.Fatal("Fehlschlag darf keine Sitzung anlegen")
	}
	reset()

	// card_only
	setKind(loginCardOnly, true)
	rec, m = post(cardBody("card_only", "DEADBEEF"+e.sfx, ""))
	if rec.Code != 200 || m["ok"] != true || cardRows(e, term) != 1 {
		t.Fatalf("card_only: %d %v", rec.Code, m)
	}
	reset()
	rec, m = post(cardBody("card_only", "SYS-CARD"+e.sfx, ""))
	failed("card_only System", rec, m)
	if rec, _ = post(cardBody("card_pin", "DEADBEEF"+e.sfx, "2580")); rec.Code != 403 {
		t.Fatalf("card_pin bei card_only: %d", rec.Code)
	}

	// password
	setKind(loginPassword, false)
	reset()
	rec, m = post(pwBody("lgpw"+e.sfx, "geheim-pw"))
	if rec.Code != 200 || m["ok"] != true || cardRows(e, term) != 1 {
		t.Fatalf("password Benutzername: %d %v", rec.Code, m)
	}
	reset()
	rec, m = post(pwBody("lgpw"+e.sfx+"@x", "geheim-pw"))
	if m["ok"] != true {
		t.Fatalf("password E-Mail: %d %v", rec.Code, m)
	}
	reset()
	rec, m = post(pwBody("lgpw"+e.sfx, "falsch"))
	failed("falsches Passwort", rec, m)
	rec, m = post(pwBody("lgsys"+e.sfx, "x"))
	failed("Systembenutzer Passwort", rec, m)

	// Knopf an bei card_pin erlaubt password, Knopf aus nicht
	setKind(loginCardPIN, true)
	reset()
	if rec, m = post(pwBody("lgpw"+e.sfx, "geheim-pw")); m["ok"] != true {
		t.Fatalf("password per Knopf: %d %v", rec.Code, m)
	}
	setKind(loginCardPIN, false)
	if rec, _ = post(pwBody("lgpw"+e.sfx, "geheim-pw")); rec.Code != 403 {
		t.Fatalf("password ohne Knopf: %d", rec.Code)
	}

	// none: alles 403 (Review Focus 1)
	setKind(loginNone, true)
	reset()
	for _, body := range []string{
		cardBody("card_only", "DEADBEEF"+e.sfx, ""),
		cardBody("card_pin", "DEADBEEF"+e.sfx, "2580"),
		pwBody("lgpw"+e.sfx, "geheim-pw"),
	} {
		if rec, _ = post(body); rec.Code != 403 {
			t.Fatalf("none: %d für %s", rec.Code, body)
		}
	}
	if cardRows(e, term) != 0 {
		t.Fatal("none darf keine Sitzung anlegen")
	}

	// Bremse: gemischt Karte (/terminal/card) und Eingabe; danach 429 auch mit richtigen Daten
	setKind(loginCardPIN, true)
	reset()
	for i := 0; i < 5; i++ {
		cardHTTP(e, h, term, "/terminal/card", `{"uid":"NOPE`+e.sfx+`"}`)
	}
	for i := 0; i < 5; i++ {
		rec, m = post(cardBody("card_pin", "NOPE"+e.sfx, "2580"))
		failed("Fehlschlag", rec, m)
	}
	rec, m = post(cardBody("card_pin", "DEADBEEF"+e.sfx, "2580"))
	if rec.Code != http.StatusTooManyRequests || m["ok"] != false || m["message"] != "Bitte kurz warten" {
		t.Fatalf("429 erwartet: %d %v", rec.Code, m)
	}
	reset()
}
