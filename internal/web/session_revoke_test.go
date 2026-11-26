package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"pdh/pkg/middleware"
)

// Abmelden zieht genau das Token dieser Sitzung am Server zurück (0.82.66): ein vorher
// kopiertes Token gilt danach nicht mehr, andere Geräte bleiben angemeldet, das
// Rückkehr-Token des Terminals bleibt gültig.

func testSessionToken(t *testing.T, secret, sub, nonce string, extra jwt.MapClaims) string {
	t.Helper()
	now := time.Now()
	c := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "n": nonce}
	for k, v := range extra {
		c[k] = v
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func reqWithToken(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "pdh_token", Value: raw})
	return req
}

func TestLogoutRevokesToken(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	pc := testSessionToken(t, h.jwtSecret, "u1", "pc", nil)
	phone := testSessionToken(t, h.jwtSecret, "u1", "handy", nil)
	if sub, _ := h.sessionClaims(reqWithToken(pc)); sub != "u1" {
		t.Fatalf("Sitzung vor dem Abmelden ungültig: %q", sub)
	}
	ret := testSessionToken(t, h.jwtSecret, "term1", "geraet", jwt.MapClaims{"device": "dev1"})
	req := reqWithToken(pc)
	req.AddCookie(&http.Cookie{Name: "pdh_return_token", Value: ret})
	h.Logout(httptest.NewRecorder(), req)
	if sub, _ := h.sessionClaims(reqWithToken(pc)); sub != "" {
		t.Error("kopiertes Token gilt nach dem Abmelden weiter")
	}
	if sub, _ := h.sessionClaims(reqWithToken(phone)); sub != "u1" {
		t.Error("Abmelden am PC hat das Handy abgemeldet")
	}
	if h.sessionTokenRevoked(req.Context(), ret) {
		t.Error("Rückkehr-Token des Terminals zurückgezogen")
	}
	// ohne gültige Sitzung (kaputtes Cookie) passiert nichts
	h.Logout(httptest.NewRecorder(), reqWithToken("kaputt"))
}

// Die Schnittstelle nimmt dasselbe Token an (Bearer oder Cookie): nach dem Abmelden gilt es
// auch dort nicht mehr, und Rechte-Sperre und Änderungshinweise sehen keinen Benutzer mehr.
func TestLogoutRevokesTokenForAPI(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	h.terminalRoutes(chi.NewRouter()) // meldet die Prüfung bei middleware.Auth an – wie im Betrieb (Routes)
	api := middleware.Auth(h.jwtSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	tok := testSessionToken(t, h.jwtSecret, "u1", "pc", nil)
	other := testSessionToken(t, h.jwtSecret, "u1", "handy", nil)
	call := func(raw string, bearer bool) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tickets/", nil)
		if bearer {
			req.Header.Set("Authorization", "Bearer "+raw)
		} else {
			req.AddCookie(&http.Cookie{Name: "pdh_token", Value: raw})
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec.Code
	}
	apiReq := func(raw string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tickets/", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		return req
	}
	if call(tok, true) != http.StatusNoContent || call(tok, false) != http.StatusNoContent {
		t.Fatal("Token vor dem Abmelden an der Schnittstelle abgewiesen")
	}
	if h.requestActor(reqWithToken(tok)) != "u1" || h.requestActor(apiReq(tok)) != "u1" {
		t.Fatal("Benutzer vor dem Abmelden nicht erkannt")
	}
	h.Logout(httptest.NewRecorder(), reqWithToken(tok))
	if c := call(tok, true); c != http.StatusUnauthorized {
		t.Errorf("kopiertes Token als Bearer nach dem Abmelden: %d", c)
	}
	if c := call(tok, false); c != http.StatusUnauthorized {
		t.Errorf("kopiertes Token als Cookie an der Schnittstelle nach dem Abmelden: %d", c)
	}
	if uid, present := h.requestCredentials(reqWithToken(tok)); uid != "" || !present {
		t.Errorf("Web: Benutzer nach dem Abmelden %q, Anmeldedaten erkannt=%v", uid, present)
	}
	if uid, present := h.requestCredentials(apiReq(tok)); uid != "" || !present {
		t.Errorf("Schnittstelle: Benutzer nach dem Abmelden %q, Anmeldedaten erkannt=%v", uid, present)
	}
	if call(other, true) != http.StatusNoContent || h.requestActor(apiReq(other)) != "u1" {
		t.Error("Abmelden am PC hat das Handy an der Schnittstelle abgemeldet")
	}
}

// Rückkehr aus einer Override-Sitzung: das Token der Person wird zurückgezogen, das Token,
// zu dem zurückgekehrt wird, nie.
func TestOverrideLogoutRevokesPersonToken(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	sys := testSessionToken(t, h.jwtSecret, "term1", "system", nil)
	person := testSessionToken(t, h.jwtSecret, "u1", "override", jwt.MapClaims{"override": true})
	back := func(cur string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/override-logout", nil)
		req.AddCookie(&http.Cookie{Name: "pdh_token", Value: cur})
		req.AddCookie(&http.Cookie{Name: "pdh_return_token", Value: sys})
		rec := httptest.NewRecorder()
		h.OverrideLogoutWeb(rec, req)
		return rec
	}
	rec := back(person)
	restored := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pdh_token" {
			restored = c.Value
		}
	}
	if restored != sys {
		t.Fatalf("Rückkehr setzt nicht das Systemnutzer-Token: %q", restored)
	}
	if sub, _ := h.sessionClaims(reqWithToken(person)); sub != "" {
		t.Error("Token der Person gilt nach der Rückkehr weiter")
	}
	if sub, _ := h.sessionClaims(reqWithToken(sys)); sub != "term1" {
		t.Error("Systemnutzer-Token nach der Rückkehr ungültig")
	}
	// Rückkehr-Cookie liegt noch im Browser, die Sitzung ist schon die des Systemnutzers: nichts zurückziehen
	back(sys)
	if sub, _ := h.sessionClaims(reqWithToken(sys)); sub != "term1" {
		t.Error("Systemnutzer-Token durch erneute Rückkehr zurückgezogen")
	}
	// dasselbe Token in anderer Schreibweise der Signatur ist immer noch das Rückkehr-Token
	for _, tw := range signatureTwins(t, sys) {
		back(tw)
	}
	if sub, _ := h.sessionClaims(reqWithToken(sys)); sub != "term1" {
		t.Error("Systemnutzer-Token über eine andere Schreibweise zurückgezogen")
	}
}

// signatureTwins: dasselbe Token mit anderem letzten Zeichen der Signatur – gleiche
// Signatur-Bytes, andere Schreibweise (Base64 ohne strenge Prüfung nimmt beides an).
func signatureTwins(t *testing.T, tok string) []string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, tok[len(tok)-1])
	if last < 0 {
		t.Fatalf("unerwartetes Zeichen am Ende des Tokens: %q", tok[len(tok)-1:])
	}
	var twins []string
	for i := last &^ 3; i < last&^3+4; i++ {
		if i != last {
			twins = append(twins, tok[:len(tok)-1]+string(alphabet[i]))
		}
	}
	return twins
}

// Eine Anfrage, die die Datenbank kurz vor dem Abmelden gefragt hat, hebt den Rückzug nicht
// wieder auf; der Zwischenspeicher wirft Veraltetes weg, nie einen geltenden Rückzug.
func TestSessionRevocationsCache(t *testing.T) {
	var s sessionRevocations
	now := time.Now()
	fill := func(prefix string, e sessionRevEntry) {
		for i := 0; len(s.m) < sessionRevokeCacheMax; i++ {
			s.m[prefix+strconv.Itoa(i)] = e
		}
	}
	s.set("a", true, now.Add(time.Hour), now)
	if !s.set("a", false, time.Time{}, now) {
		t.Fatal("verspätetes „nicht zurückgezogen“ kommt nach dem Abmelden noch durch")
	}
	if e, ok := s.get("a"); !ok || !e.revoked {
		t.Fatal("„nicht zurückgezogen“ hat den Rückzug überschrieben")
	}
	// voll: abgelaufene Rückzüge und veraltete Prüfergebnisse gehen, der Rest bleibt
	s.set("alt", true, now.Add(-time.Minute), now) // Token inzwischen abgelaufen
	s.m["frage"] = sessionRevEntry{checked: now.Add(-2 * sessionRevokeRecheck)}
	fill("x", sessionRevEntry{checked: now})
	s.set("neu", false, time.Time{}, now)
	if _, ok := s.get("alt"); ok {
		t.Error("abgelaufener Rückzug nicht verworfen")
	}
	if _, ok := s.get("frage"); ok {
		t.Error("veraltetes Prüfergebnis nicht verworfen")
	}
	if _, ok := s.get("neu"); !ok {
		t.Error("neuer Eintrag fehlt")
	}
	// voll mit frischen Prüfergebnissen: sie gehen alle, die Größe bleibt begrenzt
	fill("y", sessionRevEntry{checked: now})
	s.pruned = time.Time{}
	s.set("b", true, now.Add(time.Hour), now)
	if len(s.m) != 2 {
		t.Errorf("nach dem Aufräumen %d Einträge, erwartet nur die beiden Rückzüge", len(s.m))
	}
	// voll mit geltenden Rückzügen: keiner geht, Prüfergebnisse werden nicht mehr gemerkt
	fill("z", sessionRevEntry{revoked: true, until: now.Add(time.Hour)})
	s.pruned = time.Time{}
	if s.set("frage2", false, time.Time{}, now) {
		t.Error("Prüfergebnis bei vollem Speicher als zurückgezogen gemeldet")
	}
	if _, ok := s.get("frage2"); ok || len(s.m) != sessionRevokeCacheMax {
		t.Errorf("voller Speicher wächst durch Prüfergebnisse: %d", len(s.m))
	}
	s.set("c", true, now.Add(time.Hour), now)
	for _, k := range []string{"a", "b", "c", "z0"} {
		if e, ok := s.get(k); !ok || !e.revoked {
			t.Errorf("geltender Rückzug %q verworfen", k)
		}
	}
}

// Das letzte Zeichen der Signatur lässt sich ändern, ohne dass das Token ungültig wird
// (Base64 ohne strenge Prüfung). Auch so ein verändertes Token gilt nach dem Abmelden nicht.
func TestLogoutRevokesReencodedToken(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	tok := testSessionToken(t, h.jwtSecret, "u1", "pc", nil)
	twins := signatureTwins(t, tok)
	accepted := 0
	for _, tw := range twins {
		if sub, _ := h.sessionClaims(reqWithToken(tw)); sub == "u1" {
			accepted++
		}
	}
	if accepted == 0 {
		t.Log("die Bibliothek lehnt andere Schreibweisen der Signatur ab – nichts zu umgehen")
	}
	h.Logout(httptest.NewRecorder(), reqWithToken(tok))
	for _, tw := range append(twins, tok) {
		if sub, _ := h.sessionClaims(reqWithToken(tw)); sub != "" {
			t.Errorf("Token mit anderer Schreibweise der Signatur gilt nach dem Abmelden weiter: …%s", tw[len(tw)-4:])
		}
		if !h.sessionTokenRevoked(context.Background(), tw) {
			t.Errorf("Rückzug erkennt die andere Schreibweise nicht: …%s", tw[len(tw)-4:])
		}
	}
}
