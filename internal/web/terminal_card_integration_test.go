//go:build integration

// Kartensitzung des Anlagenterminals: Speicher und Rechteprüfung gegen die Kartenperson.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalCard ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func newCardHandler(e *atEnv) *Handler {
	us := users.NewService(users.NewRepository(e.pool), "test-secret", 1)
	return &Handler{db: e.pool, rbac: e.rb, users: us}
}

func cardRows(e *atEnv, terminal string) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, terminal).Scan(&n))
	return n
}

func TestTerminalCardSessionLifecycle(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	term := e.user("kterm", "user", true)
	p1 := e.user("kp1", "user", false)
	p2 := e.user("kp2", "user", false)
	t.Cleanup(func() { e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })

	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("ohne Sitzung: %v %v", p, err)
	}
	e.must(h.cardSessionStart(e.ctx, term, p1))
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p == nil || p.ID != p1 {
		t.Fatalf("Person: %v %v", p, err)
	}

	// Zweiter Start mit anderer Person ersetzt.
	e.must(h.cardSessionStart(e.ctx, term, p2))
	if p, _ := h.cardSessionPerson(e.ctx, term); p == nil || p.ID != p2 {
		t.Fatalf("ersetzt: %v", p)
	}
	if cardRows(e, term) != 1 {
		t.Fatal("genau eine Zeile erwartet")
	}

	// Touch verlängert eine gültige Sitzung.
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '100 seconds' WHERE terminal_user_id = $1::uuid`, term)
	if ok, err := h.cardSessionTouch(e.ctx, term); err != nil || !ok {
		t.Fatalf("touch: %v %v", ok, err)
	}
	var age float64
	e.must(e.pool.QueryRow(e.ctx, `SELECT extract(epoch FROM now()-last_seen_at) FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term).Scan(&age))
	if age > 10 {
		t.Fatalf("last_seen_at nicht verlängert: %v s", age)
	}

	// Abgelaufen: nil, Zeile gelöscht, Touch false.
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '121 seconds' WHERE terminal_user_id = $1::uuid`, term)
	if ok, _ := h.cardSessionTouch(e.ctx, term); ok {
		t.Fatal("Touch darf abgelaufene Sitzung nicht verlängern")
	}
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("abgelaufen: %v %v", p, err)
	}
	if cardRows(e, term) != 0 {
		t.Fatal("abgelaufene Zeile nicht gelöscht")
	}

	// Inaktive Person: nil.
	e.must(h.cardSessionStart(e.ctx, term, p1))
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, p1)
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("inaktiv: %v %v", p, err)
	}

	// End löscht.
	e.must(h.cardSessionStart(e.ctx, term, p2))
	e.must(h.cardSessionEnd(e.ctx, term))
	if cardRows(e, term) != 0 {
		t.Fatal("End hat nicht gelöscht")
	}
	if p, _ := h.cardSessionPerson(e.ctx, term); p != nil {
		t.Fatal("nach End keine Person")
	}
}

func TestTerminalCardPersonCan(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	term := e.user("kcterm", "user", true)
	person := e.user("kcperson", "user", false)
	dx, dy := e.dept("kcx"), e.dept("kcy")
	ax := e.infra("kca-x", dx, "")
	ay := e.infra("kca-y", dy, "")
	e.grant(person, "faults.view", dx, true)
	e.warm()

	pu, err := h.userByID(e.ctx, person)
	e.must(err)
	tu, err := h.userByID(e.ctx, term)
	e.must(err)
	if !h.cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ax) {
		t.Fatal("Anlage in X: erwartet true")
	}
	if h.cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ay) {
		t.Fatal("Anlage in Y: erwartet false")
	}
	if h.cardPersonCan(e.ctx, tu, "faults.view", "infrastructure", ax) {
		t.Fatal("Terminal-Benutzer selbst: erwartet false")
	}
	if (&Handler{}).cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ax) {
		t.Fatal("ohne rbac: erwartet false")
	}
}

// cardHTTP: Router mit Karten- und Schoner-Routen; Nutzer im Kontext wie die Middleware.
func cardHTTP(e *atEnv, h *Handler, termID, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	h.terminalRoutes(r)
	method := "POST"
	if path == "/terminal" {
		method = "GET"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	u, err := h.userByID(e.ctx, termID)
	e.must(err)
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	req.AddCookie(cardDeviceCookie(e.t, h.jwtSecret, termID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTerminalCardHandler(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	h.jwtSecret = "test-geheim"
	h.tmpl = loadTestTemplates(t)
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	cardFailLimit = newFailLimiter(10, time.Minute)
	term := e.user("khterm", "user", true)
	p1 := e.user("khp1", "user", false)
	p2 := e.user("khp2", "user", false)
	inactive := e.user("khinact", "user", false)
	sys := e.user("khsys", "user", true)
	t.Cleanup(func() { e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })
	uidOf := func(id, uid string) { e.exec(`UPDATE users SET rfid_uid = $2 WHERE id = $1::uuid`, id, uid+e.sfx) }
	uidOf(p1, "card-p1") // Kleinschreibung hinterlegt, Terminal sendet Großbuchstaben
	uidOf(p2, "CARD-P2")
	uidOf(inactive, "CARD-INACT")
	uidOf(sys, "CARD-SYS")
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, inactive)
	t.Cleanup(func() { cardFailLimit = newFailLimiter(10, time.Minute) })

	post := func(uid string) (*httptest.ResponseRecorder, map[string]any) {
		rec := cardHTTP(e, h, term, "/terminal/card", `{"uid":" `+uid+` "}`)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	sessionPerson := func() string {
		p, err := h.cardSessionPerson(e.ctx, term)
		e.must(err)
		if p == nil {
			return ""
		}
		return p.ID
	}

	rec, m := post("CARD-P1" + strings.ToUpper(e.sfx))
	if rec.Code != 200 || m["ok"] != true || m["name"] != "T U" || sessionPerson() != p1 {
		t.Fatalf("bekannte Karte: %d %v", rec.Code, m)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	rec, m = post("CARD-P2" + e.sfx)
	if m["ok"] != true || sessionPerson() != p2 {
		t.Fatalf("zweite Karte ersetzt nicht: %v", m)
	}

	// Ping verlängert; Abmelden beendet.
	rec = cardHTTP(e, h, term, "/terminal/card/ping", ``)
	if !strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("ping: %q", rec.Body.String())
	}

	// Unbekannt, inaktiv, Systembenutzer: identische Antwort.
	for _, uid := range []string{"CARD-NOPE" + e.sfx, "CARD-INACT" + e.sfx, "CARD-SYS" + e.sfx} {
		rec, m = post(uid)
		if rec.Code != 200 || m["ok"] != false || m["message"] != "Karte nicht registriert" {
			t.Fatalf("%s: %d %v", uid, rec.Code, m)
		}
	}
	if sessionPerson() != p2 {
		t.Fatal("fehlgeschlagene Karte darf bestehende Sitzung nicht ändern")
	}

	// Schoner beendet die Sitzung.
	cardHTTP(e, h, term, "/terminal", "")
	rec = cardHTTP(e, h, term, "/terminal/card/ping", ``)
	if !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Fatalf("ping nach Schoner: %q", rec.Body.String())
	}

	// Abmelden: 204.
	post("CARD-P1" + strings.ToUpper(e.sfx))
	rec = cardHTTP(e, h, term, "/terminal/card/abmelden", ``)
	if rec.Code != http.StatusNoContent || sessionPerson() != "" {
		t.Fatalf("abmelden: %d", rec.Code)
	}

	// 3 Fehlversuche waren schon; bis 10 auffüllen, dann 429.
	for i := 0; i < 7; i++ {
		post("CARD-NOPE" + e.sfx)
	}
	rec, m = post("CARD-NOPE" + e.sfx)
	if rec.Code != http.StatusTooManyRequests || m["message"] != "Zu viele unbekannte Karten. Bitte kurz warten." {
		t.Fatalf("429 erwartet: %d %v", rec.Code, m)
	}
}

// UID-Vergleich ohne Groß-/Kleinschreibung und Leerraum; bei Mehrdeutigkeit: nicht gefunden.
func TestTerminalCardUIDCaseInsensitive(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	a := e.user("kci1", "user", false)
	b := e.user("kci2", "user", false)
	c := e.user("kci3", "user", false)
	set := func(id, uid string) { e.exec(`UPDATE users SET rfid_uid = $2 WHERE id = $1::uuid`, id, uid) }
	set(a, "deadbeef"+e.sfx)
	for _, q := range []string{"DEADBEEF" + e.sfx, " deadbeef" + e.sfx + " ", "deadbeef" + e.sfx} {
		p, err := h.users.GetByRFID(e.ctx, q)
		if err != nil || p == nil || p.ID != a {
			t.Fatalf("%q: %v %v", q, p, err)
		}
	}
	// Zwei aktive Konten nur in Schreibweise verschieden: mehrdeutig -> nicht gefunden.
	set(b, "ab12"+e.sfx)
	set(c, "AB12"+e.sfx)
	for _, q := range []string{"ab12" + e.sfx, "AB12" + e.sfx} {
		if p, err := h.users.GetByRFID(e.ctx, q); err == nil || !isNoRows(err) {
			t.Fatalf("%q mehrdeutig: %v %v", q, p, err)
		}
	}
	// Ist eines der beiden inaktiv, ist die Karte wieder eindeutig.
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, c)
	if p, err := h.users.GetByRFID(e.ctx, "Ab12"+e.sfx); err != nil || p == nil || p.ID != b {
		t.Fatalf("eindeutig: %v %v", p, err)
	}
}

// Das Ablauf-Löschen ist bedingt: eine frische Sitzung bleibt, eine abgelaufene verschwindet.
func TestTerminalCardExpireDeleteIsConditional(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	term := e.user("kexp", "user", true)
	p1 := e.user("kexp1", "user", false)
	t.Cleanup(func() { e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })
	e.must(h.cardSessionStart(e.ctx, term, p1))
	e.must(h.cardSessionExpire(e.ctx, term))
	if cardRows(e, term) != 1 {
		t.Fatal("frische Sitzung gelöscht")
	}
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '121 seconds' WHERE terminal_user_id = $1::uuid`, term)
	e.must(h.cardSessionExpire(e.ctx, term))
	if cardRows(e, term) != 0 {
		t.Fatal("abgelaufene Sitzung nicht gelöscht")
	}
}
