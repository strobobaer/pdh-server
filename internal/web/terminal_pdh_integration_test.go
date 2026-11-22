//go:build integration

// „PDH öffnen“ am Terminal (terminal_pdh.go): Übergabe nur mit Gerätesitzung und Kartenanmeldung, Einmal-Ticket mit
// kurzer Frist, kurze gerätegebundene Sitzung, Widerruf bei Kartenabmeldung und Rückkehr auf eine /terminal-Seite.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalPDH ./internal/web/
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
	"pdh/pkg/middleware"
)

type pdhEnv struct {
	e             *atEnv
	h             *Handler
	mux           http.Handler
	term, person  string
	plant         string
	dev, otherDev string
	devCookie     *http.Cookie
	otherCookie   *http.Cookie
}

func newPDHEnv(t *testing.T) *pdhEnv {
	e := newATEnv(t)
	x := &pdhEnv{e: e}
	x.plant = e.infra("PDHAnlage", "", "")
	x.term = e.user("pdhterm", "terminal", true)
	x.person = e.user("pdhperson", "viewer", false)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, x.term, x.plant)
	x.dev = e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi', $2) RETURNING id::text`, x.term, hashDeviceSecret("pdht_a"+e.sfx))
	x.otherDev = e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi2', $2) RETURNING id::text`, x.term, hashDeviceSecret("pdht_b"+e.sfx))
	t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM terminal_pdh_sessions WHERE terminal_user_id = $1::uuid`, x.term)
		e.pool.Exec(bg, `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, x.term)
		e.pool.Exec(bg, `DELETE FROM terminal_devices WHERE user_id = $1::uuid`, x.term)
	})
	e.warm()
	x.h = &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-geheim", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	r := chi.NewRouter()
	r.Use(x.h.authMiddleware)
	x.h.terminalRoutes(r)
	r.Get("/maintenance/tasks/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("user=" + getUser(r).ID)) })
	x.mux = r
	tu := &users.User{ID: x.term, Role: users.Role("terminal"), IsSystemUser: true}
	mk := func(dev string) *http.Cookie {
		s, err := x.h.users.IssueToken(tu, time.Hour, map[string]interface{}{"device": dev})
		e.must(err)
		return &http.Cookie{Name: "pdh_token", Value: s}
	}
	x.devCookie, x.otherCookie = mk(x.dev), mk(x.otherDev)
	return x
}

func (x *pdhEnv) do(method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, req)
	return rec
}

func (x *pdhEnv) card(person string) {
	x.e.exec(`INSERT INTO terminal_card_sessions (terminal_user_id, person_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (terminal_user_id) DO UPDATE SET person_id = EXCLUDED.person_id, started_at = now(), last_seen_at = now()`, x.term, person)
}

var pdhTicketRe = regexp.MustCompile(`name="ticket" value="([^"]+)"`)

// ticket: Schritt 1 mit Gerätesitzung und Karte; liefert das Einmal-Ticket.
func (x *pdhEnv) ticket(t *testing.T, next string) string {
	t.Helper()
	rec := x.do("POST", "/terminal/pdh", url.Values{"next": {next}}, x.devCookie)
	m := pdhTicketRe.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("ticket: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Location"), m[1]) {
		t.Error("ticket in der Adresse")
	}
	return m[1]
}

func respCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// open: Schritt 2; bei Erfolg die beiden Cookies der PDH-Sitzung.
func (x *pdhEnv) open(ticket string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, []*http.Cookie) {
	rec := x.do("POST", "/terminal/pdh/open", url.Values{"ticket": {ticket}}, cookies...)
	if rec.Code != http.StatusSeeOther {
		return rec, nil
	}
	return rec, []*http.Cookie{respCookie(rec, "pdh_token"), respCookie(rec, "pdh_return_token")}
}

func (x *pdhEnv) session(t *testing.T) []*http.Cookie {
	t.Helper()
	x.card(x.person)
	rec, cs := x.open(x.ticket(t, "/maintenance/tasks/abc"), x.devCookie)
	if cs == nil {
		t.Fatalf("öffnen: %d %s", rec.Code, rec.Body.String())
	}
	return cs
}

func TestTerminalPDHHandoffNeedsDeviceAndCard(t *testing.T) {
	x := newPDHEnv(t)
	count := func() (n int) {
		x.e.must(x.e.pool.QueryRow(x.e.ctx, `SELECT count(*) FROM terminal_pdh_sessions WHERE terminal_user_id = $1::uuid`, x.term).Scan(&n))
		return
	}
	// ohne jede Sitzung
	if rec := x.do("POST", "/terminal/pdh", url.Values{}); rec.Code == http.StatusOK {
		t.Errorf("ohne Sitzung: %d", rec.Code)
	}
	// ohne Kartenanmeldung (Gerätesitzung vorhanden)
	if rec := x.do("POST", "/terminal/pdh", url.Values{}, x.devCookie); rec.Code != http.StatusForbidden || count() != 0 {
		t.Errorf("ohne Kartenanmeldung: %d, %d Tickets", rec.Code, count())
	}
	if rec := x.do("POST", "/terminal/pdh/open", url.Values{"ticket": {"x"}}, x.devCookie); rec.Code != http.StatusForbidden {
		t.Errorf("open ohne Karte: %d", rec.Code)
	}
	// mit Karte, aber ohne Gerätesitzung: Sitzung einer Person (kein Geräte-Claim)
	x.card(x.person)
	pt, _ := x.h.users.IssueToken(&users.User{ID: x.person, Role: users.Role("viewer")}, time.Hour, nil)
	if rec := x.do("POST", "/terminal/pdh", url.Values{}, &http.Cookie{Name: "pdh_token", Value: pt}); rec.Code == http.StatusOK || count() != 0 {
		t.Errorf("ohne Gerätesitzung: %d, %d Tickets", rec.Code, count())
	}
	// Gerätesitzung plus Karte: geht; cross-site wird abgewiesen
	req := httptest.NewRequest("POST", "/terminal/pdh", nil)
	req.AddCookie(x.devCookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", rec.Code)
	}
	x.ticket(t, "/")
}

func TestTerminalPDHSessionIsShortAndBound(t *testing.T) {
	x := newPDHEnv(t)
	cs := x.session(t)
	tok, ret := cs[0], cs[1]
	if ret == nil || ret.Value != x.devCookie.Value {
		t.Fatal("Rückkehr-Cookie fehlt oder ist nicht die Gerätesitzung")
	}
	if tok.MaxAge <= 0 || tok.MaxAge > 900 || !tok.HttpOnly {
		t.Errorf("Cookie: MaxAge %d, HttpOnly %v", tok.MaxAge, tok.HttpOnly)
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tok.Value, claims, func(*jwt.Token) (interface{}, error) { return []byte("test-geheim"), nil }); err != nil {
		t.Fatal(err)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat > 900 {
		t.Errorf("Sitzung länger als 15 min: %v s", exp-iat)
	}
	if claims["sub"] != x.person || claims["tsess"] == nil {
		t.Errorf("Claims: %v", claims)
	}
	// mit beiden Cookies: eigene Sitzung der Person
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, tok, ret); rec.Code != http.StatusOK || rec.Body.String() != "user="+x.person {
		t.Fatalf("Sitzung gilt nicht: %d %s", rec.Code, rec.Body.String())
	}
	// anderer Client: nur das Token ohne Gerätesitzung
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, tok); rec.Code == http.StatusOK {
		t.Errorf("Token allein gilt: %d", rec.Code)
	}
	// anderes Gerät desselben Terminal-Benutzers
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, tok, &http.Cookie{Name: "pdh_return_token", Value: x.otherCookie.Value}); rec.Code == http.StatusOK {
		t.Errorf("Token mit fremdem Gerät gilt: %d", rec.Code)
	}
	// dieselbe Prüfung für die Schnittstelle (middleware.Auth)
	api := middleware.Auth("test-geheim")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(cs ...*http.Cookie) int {
		req := httptest.NewRequest("GET", "/api/v1/x", nil)
		for _, c := range cs {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(tok, ret); c != http.StatusNoContent {
		t.Errorf("API mit Gerät: %d", c)
	}
	if c := call(tok); c != http.StatusUnauthorized {
		t.Errorf("API ohne Gerät: %d", c)
	}
	if c := call(tok, &http.Cookie{Name: "pdh_return_token", Value: x.otherCookie.Value}); c != http.StatusUnauthorized {
		t.Errorf("API mit fremdem Gerät: %d", c)
	}
	// Ablauf der Sitzung (serverseitig, unabhängig vom Cookie)
	x.e.exec(`UPDATE terminal_pdh_sessions SET expires_at = now() - interval '1 second' WHERE terminal_user_id = $1::uuid`, x.term)
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, tok, ret); rec.Code == http.StatusOK {
		t.Errorf("abgelaufene Sitzung gilt: %d", rec.Code)
	} else if c := respCookie(rec, "pdh_token"); c == nil || c.Value != x.devCookie.Value || rec.Header().Get("Location") != "/terminal" {
		t.Errorf("Gerätesitzung nicht wiederhergestellt: %v %q", c, rec.Header().Get("Location"))
	}
	if c := call(tok, ret); c != http.StatusUnauthorized {
		t.Errorf("API mit abgelaufener Sitzung: %d", c)
	}
}

func TestTerminalPDHTicketOnceAndExpiry(t *testing.T) {
	x := newPDHEnv(t)
	x.card(x.person)
	tk := x.ticket(t, "/maintenance/tasks/abc")
	rec, cs := x.open(tk, x.devCookie)
	if cs == nil || rec.Header().Get("Location") != "/maintenance/tasks/abc" {
		t.Fatalf("öffnen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var hash string
	x.e.must(x.e.pool.QueryRow(x.e.ctx, `SELECT ticket_sha256 FROM terminal_pdh_sessions WHERE terminal_user_id = $1::uuid AND redeemed_at IS NOT NULL`, x.term).Scan(&hash))
	if hash == tk || len(hash) != 64 {
		t.Errorf("Ticket im Klartext gespeichert: %q", hash)
	}
	// Wiederverwendung
	if rec, cs := x.open(tk, x.devCookie); cs != nil || rec.Code != http.StatusForbidden {
		t.Errorf("Ticket zweimal eingelöst: %d", rec.Code)
	}
	// Ablauf (60 s)
	tk2 := x.ticket(t, "/")
	var ttl float64
	x.e.must(x.e.pool.QueryRow(x.e.ctx, `SELECT extract(epoch FROM ticket_expires_at - created_at) FROM terminal_pdh_sessions WHERE ticket_sha256 = $1`, hashPDHTicket(tk2)).Scan(&ttl))
	if ttl > 60.5 {
		t.Errorf("Ticket %v s gültig", ttl)
	}
	x.e.exec(`UPDATE terminal_pdh_sessions SET ticket_expires_at = now() - interval '1 second' WHERE ticket_sha256 = $1`, hashPDHTicket(tk2))
	if rec, cs := x.open(tk2, x.devCookie); cs != nil || rec.Code != http.StatusForbidden {
		t.Errorf("abgelaufenes Ticket: %d", rec.Code)
	}
	// ohne Gerätesitzung oder ohne Karte lässt sich auch ein frisches Ticket nicht einlösen
	tk3 := x.ticket(t, "/")
	pt, _ := x.h.users.IssueToken(&users.User{ID: x.person, Role: users.Role("viewer")}, time.Hour, nil)
	if rec, cs := x.open(tk3, &http.Cookie{Name: "pdh_token", Value: pt}); cs != nil || rec.Code == http.StatusSeeOther {
		t.Errorf("Ticket ohne Gerätesitzung eingelöst: %d", rec.Code)
	}
	if rec, cs := x.open(tk3, x.otherCookie); cs != nil || rec.Code != http.StatusForbidden {
		t.Errorf("Ticket von anderem Gerät eingelöst: %d", rec.Code)
	}
	x.e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, x.term)
	if rec, cs := x.open(tk3, x.devCookie); cs != nil || rec.Code != http.StatusForbidden {
		t.Errorf("Ticket ohne Karte eingelöst: %d", rec.Code)
	}
	// andere Person an der Karte: Ticket gilt nicht mehr
	other := x.e.user("pdhother", "viewer", false)
	x.card(x.person)
	tk4 := x.ticket(t, "/")
	x.card(other)
	if rec, cs := x.open(tk4, x.devCookie); cs != nil || rec.Code != http.StatusForbidden {
		t.Errorf("Ticket einer anderen Person eingelöst: %d", rec.Code)
	}
	// Ziel nur innerhalb PDH
	x.card(x.person)
	for next, want := range map[string]string{"/terminal/info": "/", "//evil.example": "/", "https://evil.example": "/", "/maintenance": "/maintenance"} {
		if rec, cs := x.open(x.ticket(t, next), x.devCookie); cs == nil || rec.Header().Get("Location") != want {
			t.Errorf("next %q: %d %q, erwartet %q", next, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

func TestTerminalPDHRevokedOnReturnAndCardLogout(t *testing.T) {
	x := newPDHEnv(t)
	revoked := func() (n int) {
		x.e.must(x.e.pool.QueryRow(x.e.ctx, `SELECT count(*) FROM terminal_pdh_sessions WHERE terminal_user_id = $1::uuid AND revoked_at IS NOT NULL AND redeemed_at IS NOT NULL`, x.term).Scan(&n))
		return
	}
	// Rückkehr auf eine /terminal-Seite (auch Ruhezeit und Zurück): Sitzung serverseitig beendet, Gerätesitzung zurück
	cs := x.session(t)
	rec := x.do("GET", "/terminal/info", nil, cs...)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/terminal/info" {
		t.Fatalf("Rückkehr: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := respCookie(rec, "pdh_token"); c == nil || c.Value != x.devCookie.Value {
		t.Error("Gerätesitzung nicht wiederhergestellt")
	}
	if c := respCookie(rec, "pdh_return_token"); c == nil || c.MaxAge >= 0 {
		t.Error("Rückkehr-Cookie nicht gelöscht")
	}
	if revoked() != 1 {
		t.Errorf("Sitzung nicht widerrufen: %d", revoked())
	}
	// das alte Token gilt auch dann nicht mehr, wenn jemand es mit dem Gerät wieder vorlegt
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, cs...); rec.Code == http.StatusOK {
		t.Errorf("widerrufene Sitzung gilt: %d", rec.Code)
	}
	// Kartenabmeldung beendet die Sitzung, ohne dass die Seite etwas tun muss
	cs = x.session(t)
	if rec := x.do("POST", "/terminal/card/abmelden", url.Values{}, x.devCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("abmelden: %d", rec.Code)
	}
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, cs...); rec.Code == http.StatusOK {
		t.Errorf("Sitzung nach Kartenabmeldung gilt: %d", rec.Code)
	}
	// andere Person hält ihre Karte vor
	cs = x.session(t)
	other := x.e.user("pdhother2", "viewer", false)
	e := x.e
	e.must(x.h.cardSessionStart(e.ctx, x.term, other))
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, cs...); rec.Code == http.StatusOK {
		t.Errorf("Sitzung nach Kartenwechsel gilt: %d", rec.Code)
	}
	// Abmelden in PDH und Rückkehr zum Terminal (Ruhezeit-Zeitgeber der PDH-Seiten)
	x.card(x.person)
	cs = x.session(t)
	x.h.pdhHandoffRevokeCookie(func() *http.Request {
		r := httptest.NewRequest("POST", "/x", nil)
		r.AddCookie(cs[0])
		return r
	}())
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, cs...); rec.Code == http.StatusOK {
		t.Errorf("Sitzung nach Abmelden gilt: %d", rec.Code)
	}
	// normale PDH-Sitzung bleibt unberührt: Token ohne tsess und ohne Rückkehr-Cookie
	pt, _ := x.h.users.IssueToken(&users.User{ID: x.person, Role: users.Role("viewer")}, time.Hour, nil)
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, &http.Cookie{Name: "pdh_token", Value: pt}); rec.Code != http.StatusOK || rec.Body.String() != "user="+x.person {
		t.Errorf("normale Sitzung: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTerminalPDHHardening(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	// Idle-Ablauf der Kartensitzung beendet auch die PDH-Sitzung
	cs := x.session(t)
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '10 minutes' WHERE terminal_user_id = $1::uuid`, x.term)
	if p, err := x.h.cardSessionPerson(e.ctx, x.term); err != nil || p != nil {
		t.Fatalf("Kartensitzung nicht abgelaufen: %v %v", p, err)
	}
	if rec := x.do("GET", "/maintenance/tasks/abc", nil, cs...); rec.Code == http.StatusOK {
		t.Errorf("Sitzung nach Karten-Ruhezeit gilt: %d", rec.Code)
	}
	// apiActor prüft tsess
	cs = x.session(t)
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.AddCookie(cs[0])
	if uid, _ := x.h.apiActor(req); uid != "" {
		t.Errorf("apiActor ohne Gerät: %q", uid)
	}
	req.AddCookie(cs[1])
	if uid, _ := x.h.apiActor(req); uid != x.person {
		t.Errorf("apiActor mit Gerät: %q", uid)
	}
	x.e.exec(`UPDATE terminal_pdh_sessions SET revoked_at = now() WHERE terminal_user_id = $1::uuid`, x.term)
	if uid, _ := x.h.apiActor(req); uid != "" {
		t.Errorf("apiActor nach Widerruf: %q", uid)
	}
	// Rolle Terminal: hmi.view ja, hmi.control nein
	var view, ctl int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FILTER (WHERE p.key = 'hmi.view'), count(*) FILTER (WHERE p.key = 'hmi.control')
		FROM role_permissions rp JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.id = rp.permission_id WHERE r.key = 'terminal'`).Scan(&view, &ctl))
	if view != 1 || ctl != 0 {
		t.Errorf("Rolle Terminal: hmi.view %d, hmi.control %d", view, ctl)
	}
}

func TestLoginNextControlChars(t *testing.T) {
	for _, n := range []string{"/\t/evil.example", "/\n/evil", "/\r/x", "/a\x00b", "/\x1f/x", "/a\x7f"} {
		if got := loginNext(n); got != "/" {
			t.Errorf("loginNext(%q) = %q", n, got)
		}
	}
	if got := loginNext("/maintenance?x=1"); got != "/maintenance?x=1" {
		t.Errorf("normaler Pfad: %q", got)
	}
}
