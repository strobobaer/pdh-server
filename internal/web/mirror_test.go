package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pdh/internal/core/users"
	"pdh/pkg/database"
)

// mirrorTestHandler: Spiegel-Middleware vor einem Handler, der „ok“ schreibt.
func mirrorTestHandler(on bool) (http.Handler, *int) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: on})
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("ok"))
	})
	return h.MirrorMiddleware(next), &calls
}

func mirrorServe(hd http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hd.ServeHTTP(rec, req)
	return rec
}

func TestReadOnlyBlocksWrites(t *testing.T) {
	hd, calls := mirrorTestHandler(true)
	rec := mirrorServe(hd, http.MethodPost, "/tickets/x/comment", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) {
		t.Fatalf("POST: %d %q", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatal("POST darf den Handler nicht erreichen")
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if rec := mirrorServe(hd, m, "/", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s /: %d", m, rec.Code)
		}
	}
	if *calls != 3 {
		t.Fatalf("GET/HEAD/OPTIONS müssen durch: %d", *calls)
	}
	// Schnittstelle: JSON wie response.Error
	rec = mirrorServe(hd, http.MethodDelete, "/api/v1/tickets/abc", nil)
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error != mirrorReadOnlyMsg || body.Success {
		t.Fatalf("API: %d %q", rec.Code, rec.Body.String())
	}
}

func TestReadOnlyAllowsAuth(t *testing.T) {
	hd, calls := mirrorTestHandler(true)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/login"},
		{http.MethodPost, "/login/rfid"},
		{http.MethodGet, "/logout"},
		{http.MethodPost, "/logout"},
		{http.MethodPost, "/lang"},
		{http.MethodPost, "/admin/server-config/ha/command"},
		{http.MethodPost, "/api/v1/users/login"},
	}
	for _, c := range cases {
		if rec := mirrorServe(hd, c.method, c.path, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if *calls != len(cases) {
		t.Fatalf("durchgereicht: %d", *calls)
	}
	for _, p := range []string{"/loginx", "/login/forgot", "/login/reset", "/login/change", "/login/x", "/admin/server-config", "/admin/server-config/hax", "/language", "/api/v1/users/login/x", "/api/v1/users"} {
		if rec := mirrorServe(hd, http.MethodPost, p, nil); rec.Code != http.StatusConflict {
			t.Fatalf("POST %s muss gesperrt sein: %d", p, rec.Code)
		}
	}
}

func TestReadOnlyBlocksHTMX(t *testing.T) {
	hd, _ := mirrorTestHandler(true)
	rec := mirrorServe(hd, http.MethodPost, "/tickets/x/comment", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("X-PDH-Spiegel") != "1" || rec.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("Kopfzeilen: %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) || strings.Contains(rec.Body.String(), "<") {
		t.Fatalf("Text: %q", rec.Body.String())
	}
}

func TestReadOnlyOffPassesAll(t *testing.T) {
	hd, calls := mirrorTestHandler(false)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodGet} {
		if rec := mirrorServe(hd, m, "/tickets/x/comment", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
	if *calls != 4 {
		t.Fatalf("calls %d", *calls)
	}
}

// mirrorStaleHeaders: Kopfzeilen des Handlers, die zur Sperrmeldung nicht passen.
var mirrorStaleHeaders = map[string]string{
	"Content-Disposition": `attachment; filename="x.pdf"`,
	"Content-Encoding":    "gzip",
	"ETag":                `"abc"`,
	"Last-Modified":       "Mon, 02 Jan 2006 15:04:05 GMT",
	"Cache-Control":       "max-age=3600",
}

// writeIn25006: Handler, der wie ein Schreibzugriff am Standby scheitert
// (Tracer markiert die Anfrage) und dann 500 meldet.
func writeIn25006(fail bool) http.Handler {
	tr := database.NewReadOnlyTracer()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			tr.TraceQueryEnd(r.Context(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "25006"}})
		}
		w.Header().Set("HX-Redirect", "/irgendwo")
		for k, v := range mirrorStaleHeaders {
			w.Header().Set(k, v)
		}
		http.Error(w, "interner Fehler", http.StatusInternalServerError)
	})
}

func TestReadOnlyTracerConverts500(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})

	// GET-Seite, die nebenbei schreibt
	rec := mirrorServe(h.MirrorMiddleware(writeIn25006(true)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) || strings.Contains(rec.Body.String(), "interner Fehler") {
		t.Fatalf("mit Marke: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatal("htmx-Kopfzeilen des Handlers müssen verworfen werden")
	}
	for k := range mirrorStaleHeaders {
		if k != "Cache-Control" && rec.Header().Get(k) != "" {
			t.Errorf("Kopfzeile %s des Handlers muss verworfen werden", k)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: %q", rec.Header().Get("Cache-Control"))
	}
	// erlaubter Schreibpfad (Anmeldung) schreibt trotzdem
	rec = mirrorServe(h.MirrorMiddleware(writeIn25006(true)), http.MethodPost, "/login", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusConflict || rec.Header().Get("X-PDH-Spiegel") != "1" {
		t.Fatalf("htmx mit Marke: %d %v", rec.Code, rec.Header())
	}
	// ohne Marke bleibt 500
	rec = mirrorServe(h.MirrorMiddleware(writeIn25006(false)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "interner Fehler") {
		t.Fatalf("ohne Marke: %d %q", rec.Code, rec.Body.String())
	}
	// Spiegel aus: keine Marke im Kontext, keine Umwandlung
	off := &Handler{}
	rec = mirrorServe(off.MirrorMiddleware(writeIn25006(true)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Spiegel aus: %d", rec.Code)
	}
}

// Ereignisströme (SSE) und lange Downloads laufen ungepuffert durch.
func TestReadOnlyStreamsPassThrough(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	flushed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: 1\n\n"))
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("Flusher fehlt")
			return
		}
		f.Flush()
		flushed = true
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("Hijacker fehlt")
		}
	})
	rec := httptest.NewRecorder()
	h.MirrorMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat/stream", nil))
	if !flushed || !rec.Flushed || rec.Body.String() != "data: 1\n\n" {
		t.Fatalf("SSE: flushed=%v %q", rec.Flushed, rec.Body.String())
	}
}

func TestPresenceSkippedInMirror(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	uid := "presence-mirror-test"
	presence.mu.Lock()
	delete(presence.saved, uid)
	presence.mu.Unlock()
	h.presenceTouch(uid)
	presence.mu.Lock()
	_, saved := presence.saved[uid]
	delete(presence.seen, uid)
	presence.mu.Unlock()
	if saved {
		t.Fatal("im Spiegel darf presenceTouch nichts speichern")
	}
}

// ── Anmeldung nach Regel C, eingeschränkter Modus ───────────

func TestMirrorLoginAllowed(t *testing.T) {
	seen := mirrorPeer{Known: true, ActiveSeen: true}
	gone := mirrorPeer{Known: true}
	unknown := mirrorPeer{}
	cases := []struct {
		name  string
		admin bool
		p     mirrorPeer
		want  bool
	}{
		{"admin/gesehen", true, seen, true},
		{"admin/nicht gesehen", true, gone, true},
		{"admin/unbekannt", true, unknown, true},
		{"benutzer/gesehen", false, seen, false},
		{"benutzer/nicht gesehen", false, gone, true},
		{"benutzer/unbekannt", false, unknown, false},
	}
	for _, c := range cases {
		if got := mirrorLoginAllowed(c.admin, c.p); got != c.want {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

// mirrorStatus: pdh-ha-Status mit aktivem Partner (gesehen oder nicht).
func mirrorStatus(seen bool) haStatus {
	return haStatus{Configured: true, Peers: []haNode{
		{Kind: "witness", Role: "aktiv", Seen: true, Paths: []haPath{{Addr: "10.10.0.99:7946"}}},
		{Kind: "node", Role: "aktiv", Seen: seen, Paths: []haPath{{Addr: "10.10.0.17:7946"}}},
	}}
}

// mirrorGateTest: authMiddleware mit Sitzung (Benutzer ohne Datenbank) vor einem Handler, der „ok“ schreibt.
func mirrorGateTest(t *testing.T, info MirrorInfo, admin bool, st haStatus) (http.Handler, *int, string) {
	t.Helper()
	h := &Handler{jwtSecret: boardTestSecret}
	h.SetMirror(info)
	h.userByIDFn = func(_ context.Context, id string) (*users.User, error) {
		return &users.User{ID: id, Role: users.RoleTechnician, Active: true}, nil
	}
	h.mirrorAdminFn = func(*http.Request) bool { return admin }
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return st, nil }
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("ok"))
	})
	tok := boardTestToken(t, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	return h.authMiddleware(next), &calls, tok
}

func mirrorSessionGet(hd http.Handler, tok, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "pdh_token", Value: tok})
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hd.ServeHTTP(rec, req)
	return rec
}

func cookieCleared(rec *httptest.ResponseRecorder, name string) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestMirrorGateExistingSession(t *testing.T) {
	hd, calls, tok := mirrorGateTest(t, MirrorInfo{On: true}, false, mirrorStatus(true))
	rec := mirrorSessionGet(hd, tok, "/", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?spiegel=1" {
		t.Fatalf("Nicht-Admin bei erreichbarem aktivem Server: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range []string{"pdh_token", "pdh_user_id", "pdh_return_token"} {
		if !cookieCleared(rec, c) {
			t.Errorf("Cookie %s nicht gelöscht", c)
		}
	}
	if *calls != 0 {
		t.Fatal("Handler darf nicht erreicht werden")
	}
	// htmx (Hintergrundabfrage): ganze Seite zur Anmeldung
	rec = mirrorSessionGet(hd, tok, "/api/presence", map[string]string{"HX-Request": "true"})
	if rec.Header().Get("HX-Redirect") != "/login?spiegel=1" || !cookieCleared(rec, "pdh_token") {
		t.Fatalf("htmx: %d %v", rec.Code, rec.Header())
	}
	// aktiver Server weg → Nicht-Admin darf lesen
	hd, calls, tok = mirrorGateTest(t, MirrorInfo{On: true}, false, mirrorStatus(false))
	if rec := mirrorSessionGet(hd, tok, "/", nil); rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("aktiver Server nicht gesehen: %d", rec.Code)
	}
	// Admin immer
	hd, calls, tok = mirrorGateTest(t, MirrorInfo{On: true}, true, mirrorStatus(true))
	if rec := mirrorSessionGet(hd, tok, "/", nil); rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("Admin: %d", rec.Code)
	}
	// Spiegel aus: keine Schranke, kein Statusabruf
	h := &Handler{jwtSecret: boardTestSecret}
	h.userByIDFn = func(_ context.Context, id string) (*users.User, error) {
		return &users.User{ID: id, Role: users.RoleTechnician, Active: true}, nil
	}
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { t.Error("Statusabruf ohne Spiegel"); return haStatus{}, nil }
	h.mirrorAdminFn = func(*http.Request) bool { return false }
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	if mirrorSessionGet(h.authMiddleware(next), tok, "/", nil); !reached {
		t.Fatal("Spiegel aus: Handler nicht erreicht")
	}
}

func TestMirrorGateBoardComplete(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	tok := boardTestToken(t, jwt.MapClaims{"sub": "u1", "scope": boardCompleteScope, "ref": "fault:" + id, "exp": time.Now().Add(time.Minute).Unix()})
	for _, admin := range []bool{false, true} {
		hd, calls, _ := mirrorGateTest(t, MirrorInfo{On: true}, admin, mirrorStatus(true))
		req := httptest.NewRequest(http.MethodPost, "/complete/fault/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, req)
		if admin {
			if rec.Code != http.StatusOK || *calls != 1 {
				t.Fatalf("Admin: %d", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusConflict || *calls != 0 {
			t.Fatalf("Nicht-Admin per Leitstand-Token: %d calls=%d", rec.Code, *calls)
		}
	}
}

func TestMirrorRestrictedRedirect(t *testing.T) {
	info := MirrorInfo{On: true, Restricted: true}
	hd, calls, tok := mirrorGateTest(t, info, true, mirrorStatus(false))
	rec := mirrorSessionGet(hd, tok, "/tickets", map[string]string{"Accept": "text/html"})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/server-config?tab=dr" {
		t.Fatalf("Admin /tickets: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/admin/server-config", "/admin/server-config?tab=dr", "/admin/server-config/ha/join-script", "/logout"} {
		if rec := mirrorSessionGet(hd, tok, p, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s muss durch: %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if *calls != 4 {
		t.Fatalf("durchgereicht: %d", *calls)
	}
	// htmx-Hintergrundabfragen: nichts tauschen, keine Umleitungsschleife
	rec = mirrorSessionGet(hd, tok, "/api/presence", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusNoContent || rec.Header().Get("Location") != "" {
		t.Fatalf("htmx: %d %v", rec.Code, rec.Header())
	}
	// Nicht-Admin: abgemeldet, auch wenn der aktive Server weg ist
	hd, calls, tok = mirrorGateTest(t, info, false, mirrorStatus(false))
	rec = mirrorSessionGet(hd, tok, "/admin/server-config", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?spiegel=1" || !cookieCleared(rec, "pdh_token") || *calls != 0 {
		t.Fatalf("Nicht-Admin eingeschränkt: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMirrorRestrictedAllowed(t *testing.T) {
	for _, p := range []string{"/admin/server-config", "/admin/server-config/ha/command", "/login", "/login/rfid", "/logout", "/lang", "/static/x.js", "/help", "/help/ha"} {
		if !mirrorRestrictedAllowed(p) {
			t.Errorf("%s muss erlaubt sein", p)
		}
	}
	for _, p := range []string{"/", "/tickets", "/admin/server-configx", "/admin/users", "/loginx", "/language", "/staticx", "/helpx"} {
		if mirrorRestrictedAllowed(p) {
			t.Errorf("%s darf nicht erlaubt sein", p)
		}
	}
}

func TestMirrorPeerFromStatus(t *testing.T) {
	h := &Handler{}
	fetches := 0
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) {
		fetches++
		return haStatus{Configured: true, Peers: []haNode{{Role: "aktiv", Seen: true, Paths: []haPath{{Addr: "10.10.0.17:7946"}}}}}, nil
	}
	p := h.mirrorPeerState(context.Background())
	if !p.Known || !p.ActiveSeen || p.ActiveAddr != "10.10.0.17" {
		t.Fatalf("Peer: %+v", p)
	}
	h.mirrorPeerState(context.Background())
	if fetches != 1 {
		t.Fatalf("Zwischenspeicher: %d Abrufe", fetches)
	}
	// Zeuge zählt nicht als aktiver Server
	h = &Handler{}
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) {
		return haStatus{Configured: true, Peers: []haNode{{Kind: "witness", Role: "aktiv", Seen: true}}}, nil
	}
	if p := h.mirrorPeerState(context.Background()); !p.Known || p.ActiveSeen {
		t.Fatalf("Zeuge: %+v", p)
	}
	// Fehler oder nicht eingerichtet → unbekannt
	for _, f := range []func(context.Context) (haStatus, error){
		func(context.Context) (haStatus, error) { return haStatus{}, errors.New("socket fehlt") },
		func(context.Context) (haStatus, error) { return haStatus{Configured: false}, nil },
	} {
		h = &Handler{}
		h.mirrorPeerFetch = f
		if p := h.mirrorPeerState(context.Background()); p.Known {
			t.Fatalf("muss unbekannt sein: %+v", p)
		}
	}
	// ohne Socket (Standardabruf) → unbekannt
	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
	h = &Handler{}
	if p := h.mirrorPeerState(context.Background()); p.Known {
		t.Fatalf("ohne Socket: %+v", p)
	}
}

func TestMirrorActiveURL(t *testing.T) {
	p := mirrorPeer{Known: true, ActiveSeen: true, ActiveAddr: "10.10.0.17"}
	r := httptest.NewRequest(http.MethodGet, "http://10.10.0.18:8080/x", nil)
	if u := mirrorActiveLink(r, p); u != "http://10.10.0.17:8080/" {
		t.Fatalf("mit Port: %q", u)
	}
	r = httptest.NewRequest(http.MethodGet, "http://pdh2.firma.de/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if u := mirrorActiveLink(r, p); u != "https://10.10.0.17/" {
		t.Fatalf("ohne Port, https: %q", u)
	}
	if u := mirrorActiveLink(r, mirrorPeer{Known: true}); u != "" {
		t.Fatalf("ohne Adresse: %q", u)
	}
	if u := mirrorActiveLink(r, mirrorPeer{ActiveAddr: "fd00::17"}); u != "https://[fd00::17]/" {
		t.Fatalf("IPv6: %q", u)
	}
}

func TestMirrorLoginHint(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return mirrorStatus(true), nil }
	d := h.mirrorLoginHint(httptest.NewRequest(http.MethodGet, "http://10.10.0.18:8080/login?spiegel=1", nil), LoginData{})
	if !d.Mirror || d.MirrorURL != "http://10.10.0.17:8080/" {
		t.Fatalf("Hinweis: %+v", d)
	}
	if d := h.mirrorLoginHint(httptest.NewRequest(http.MethodGet, "/login", nil), LoginData{}); d.Mirror {
		t.Fatal("ohne spiegel=1 kein Hinweis")
	}
	off := &Handler{}
	if d := off.mirrorLoginHint(httptest.NewRequest(http.MethodGet, "/login?spiegel=1", nil), LoginData{}); d.Mirror {
		t.Fatal("Spiegel aus: kein Hinweis")
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "login.gohtml"))
	if err != nil || !strings.Contains(string(b), "Bitte den aktiven Server verwenden") || !strings.Contains(string(b), ".MirrorURL") {
		t.Fatal("login.gohtml: Hinweis fehlt")
	}
}

// ── Band „Spiegel – nur lesen“ ─

func TestMirrorLagText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                               "0 s",
		59 * time.Second:                "59 s",
		61 * time.Second:                "1 min",
		3900 * time.Second:              "1 h 5 min",
		-3 * time.Second:                "0 s",
		7200 * time.Second:              "2 h 0 min",
		59*time.Minute + 59*time.Second: "59 min",
	} {
		if got := mirrorLagText(d); got != want {
			t.Errorf("%v: %q, erwartet %q", d, got, want)
		}
	}
}

func mirrorBannerPage(t *testing.T, m *MirrorBanner) string {
	t.Helper()
	bd := BaseData{Mirror: m}
	bd.Nav = buildNav(&bd, nil)
	out := renderPage(t, loadTestTemplates(t), "org_units", OrgUnitsData{BaseData: bd})
	// nur das Band – Skripte der Seite enthalten ähnliche Texte
	i := strings.Index(out, `<div class="mirror-band`)
	if i < 0 {
		return ""
	}
	j := strings.Index(out[i:], `<div class="main">`)
	if j < 0 {
		t.Fatal("Band nicht vor dem Inhalt")
	}
	return out[i : i+j]
}

func TestMirrorBannerRendered(t *testing.T) {
	out := mirrorBannerPage(t, &MirrorBanner{Known: true, ActiveSeen: true, Lag: "3 s", ActiveURL: "http://10.10.0.17:8080/"})
	for _, want := range []string{"Spiegel – nur lesen", "Datenstand vor 3 s", `href="http://10.10.0.17:8080/"`, ">10.10.0.17:8080<", "mirror-band warn"} {
		if !strings.Contains(out, want) {
			t.Errorf("aktiv gesehen: %q fehlt", want)
		}
	}
	if strings.Contains(out, "nicht erreichbar") || strings.Contains(out, "?tab=dr") {
		t.Error("aktiv gesehen: kein Ausfall-Hinweis")
	}
	// Rückstand unbekannt
	if out := mirrorBannerPage(t, &MirrorBanner{Known: true, ActiveSeen: true}); !strings.Contains(out, "Datenstand unbekannt") {
		t.Error("ohne Rückstand: „Datenstand unbekannt“ fehlt")
	}
	// aktiver Server weg: rot, Admin mit Knopf zum Ausfallschutz
	out = mirrorBannerPage(t, &MirrorBanner{Known: true, DataAt: "28.10. 06:49", Admin: true})
	for _, want := range []string{"Aktiver Server nicht erreichbar – Daten vom 28.10. 06:49", "mirror-band err", `href="/admin/server-config?tab=dr"`} {
		if !strings.Contains(out, want) {
			t.Errorf("weg/Admin: %q fehlt", want)
		}
	}
	if out := mirrorBannerPage(t, &MirrorBanner{Known: true, DataAt: "28.10. 06:49"}); strings.Contains(out, "?tab=dr") {
		t.Error("Nicht-Admin: kein Knopf zum Ausfallschutz")
	}
	if out := mirrorBannerPage(t, &MirrorBanner{Known: true}); !strings.Contains(out, "Aktiver Server nicht erreichbar") || !strings.Contains(out, "Datenstand unbekannt") {
		t.Error("weg ohne Zeitpunkt")
	}
	// Status unbekannt (pdh-ha nicht lesbar): orange statt rot, Admin mit Knopf
	out = mirrorBannerPage(t, &MirrorBanner{Lag: "4 s", Admin: true})
	for _, want := range []string{"Status des aktiven Servers unbekannt", "mirror-band warn", "Datenstand vor 4 s", `href="/admin/server-config?tab=dr"`} {
		if !strings.Contains(out, want) {
			t.Errorf("unbekannt: %q fehlt", want)
		}
	}
	if strings.Contains(out, "nicht erreichbar") || strings.Contains(out, "mirror-band err") {
		t.Error("unbekannt: nicht als Ausfall zeigen")
	}
	if out := mirrorBannerPage(t, &MirrorBanner{}); strings.Contains(out, "?tab=dr") {
		t.Error("unbekannt, Nicht-Admin: kein Knopf")
	}
	// auf dem Stand des aktiven Servers: „aktuell“ statt wachsendem Rückstand
	out = mirrorBannerPage(t, &MirrorBanner{Known: true, ActiveSeen: true, Current: true, Lag: "5 min"})
	if !strings.Contains(out, "Datenstand aktuell") || strings.Contains(out, "Datenstand vor") {
		t.Errorf("aktuell: %s", out)
	}
	// eingeschränkt
	if out := mirrorBannerPage(t, &MirrorBanner{Restricted: true, Admin: true}); !strings.Contains(out, "Spiegel wartet auf das Update des aktiven Servers") {
		t.Error("eingeschränkt: Hinweis fehlt")
	}
	// außerhalb des Spiegels kein Band
	if out := mirrorBannerPage(t, nil); strings.Contains(out, `class="mirror-band`) {
		t.Error("ohne Spiegel: Band angezeigt")
	}
}

func TestMirrorBannerData(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://10.10.0.18:8080/", nil)
	if (&Handler{}).mirrorBanner(r) != nil {
		t.Fatal("Spiegel aus: Band")
	}
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	h.mirrorAdminFn = func(*http.Request) bool { return true }
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return mirrorStatus(true), nil }
	at := time.Date(2026, 10, 28, 6, 49, 0, 0, time.Local)
	lagCalls := 0
	h.mirrorLagFetch = func(context.Context) (time.Duration, time.Time, error) {
		lagCalls++
		return 3 * time.Second, at, nil
	}
	curCalls := 0
	h.mirrorCurFetch = func(context.Context) (bool, error) { curCalls++; return true, nil }
	b := h.mirrorBanner(r)
	if b == nil || !b.Known || !b.ActiveSeen || !b.Admin || b.Restricted || !b.Current || b.Lag != "3 s" || b.DataAt != "28.10. 06:49" || b.ActiveURL != "http://10.10.0.17:8080/" {
		t.Fatalf("Band: %+v", b)
	}
	h.mirrorBanner(r)
	if lagCalls != 1 || curCalls != 1 {
		t.Fatalf("Rückstand zwischengespeichert: %d/%d Abfragen", lagCalls, curCalls)
	}
	// Abfragefehler → kein Rückstand
	h = &Handler{}
	h.SetMirror(MirrorInfo{On: true, Restricted: true})
	h.mirrorAdminFn = func(*http.Request) bool { return false }
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return mirrorStatus(false), nil }
	h.mirrorLagFetch = func(context.Context) (time.Duration, time.Time, error) { return 0, time.Time{}, errors.New("db weg") }
	h.mirrorCurFetch = func(context.Context) (bool, error) { return true, errors.New("db weg") }
	if b := h.mirrorBanner(r); b == nil || !b.Known || b.ActiveSeen || b.Admin || !b.Restricted || b.Current || b.Lag != "" || b.DataAt != "" {
		t.Fatalf("Fehler: %+v", b)
	}
	// noch nichts eingespielt (NULL) → kein Rückstand
	h.mirrorLagC = mirrorCache[mirrorLag]{}
	h.mirrorLagFetch = func(context.Context) (time.Duration, time.Time, error) { return 0, time.Time{}, nil }
	h.mirrorCurFetch = func(context.Context) (bool, error) { return false, nil }
	if b := h.mirrorBanner(r); b.Lag != "" || b.DataAt != "" || b.Current {
		t.Fatalf("NULL: %+v", b)
	}
	// ohne Datenbank kein Absturz
	h = &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	h.mirrorAdminFn = func(*http.Request) bool { return false }
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return haStatus{}, errors.New("x") }
	if b := h.mirrorBanner(r); b == nil || b.Lag != "" || b.Current || b.Known {
		t.Fatalf("ohne DB: %+v", b)
	}
}

// ── nachgezogene Korrekturen ─

func TestMirrorPeerStateGrace(t *testing.T) {
	h := &Handler{}
	fail := false
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) {
		if fail {
			return haStatus{}, errors.New("socket hängt")
		}
		return mirrorStatus(false), nil
	}
	if p := h.mirrorPeerState(context.Background()); !p.Known || p.ActiveSeen {
		t.Fatalf("Erfolg: %+v", p)
	}
	age := func(d time.Duration) {
		h.mirrorPeerC.mu.Lock()
		h.mirrorPeerC.at = h.mirrorPeerC.at.Add(-d)
		h.mirrorPeerC.okAt = h.mirrorPeerC.okAt.Add(-d)
		h.mirrorPeerC.mu.Unlock()
	}
	fail = true
	age(6 * time.Second) // Lebensdauer um, Nachfrist läuft
	if p := h.mirrorPeerState(context.Background()); !p.Known || p.ActiveSeen {
		t.Fatalf("Fehler in der Nachfrist: alter Wert erwartet, %+v", p)
	}
	age(31 * time.Second)
	if p := h.mirrorPeerState(context.Background()); p.Known {
		t.Fatalf("Fehler nach 30 s: unbekannt erwartet, %+v", p)
	}
}

func TestMirrorPeerStateSingleFlight(t *testing.T) {
	h := &Handler{}
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	slow := false
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) {
		if slow {
			entered <- struct{}{}
			<-block
			return mirrorStatus(true), nil
		}
		return mirrorStatus(false), nil
	}
	h.mirrorPeerState(context.Background())
	h.mirrorPeerC.mu.Lock()
	h.mirrorPeerC.at = h.mirrorPeerC.at.Add(-time.Minute)
	h.mirrorPeerC.mu.Unlock()
	slow = true
	done := make(chan mirrorPeer)
	go func() { done <- h.mirrorPeerState(context.Background()) }()
	<-entered
	// zweiter Aufruf wartet nicht auf den hängenden Abruf
	got := make(chan mirrorPeer)
	go func() { got <- h.mirrorPeerState(context.Background()) }()
	select {
	case p := <-got:
		if !p.Known || p.ActiveSeen {
			t.Fatalf("veralteter Wert erwartet: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("zweiter Aufruf blockiert")
	}
	close(block)
	if p := <-done; !p.ActiveSeen {
		t.Fatalf("Abrufer: %+v", p)
	}
}

func TestMirrorPeerPrefersAlivePath(t *testing.T) {
	st := haStatus{Configured: true, Peers: []haNode{{Role: "aktiv", Seen: true, Paths: []haPath{
		{Addr: "10.10.0.17:7946"}, {Addr: "192.168.5.17:7946", Alive: true},
	}}}}
	if p := mirrorPeerFrom(st); p.ActiveAddr != "192.168.5.17" {
		t.Fatalf("lebender Pfad: %+v", p)
	}
	st.Peers[0].Paths[1].Alive = false
	if p := mirrorPeerFrom(st); p.ActiveAddr != "10.10.0.17" {
		t.Fatalf("Rückfall erster Pfad: %+v", p)
	}
}

func TestMirrorLoginRefused(t *testing.T) {
	user := &users.User{ID: "u1", Role: users.RoleTechnician, Active: true}
	cases := []struct {
		name   string
		info   MirrorInfo
		admin  bool
		seen   bool
		refuse bool
	}{
		{"spiegel aus", MirrorInfo{}, false, true, false},
		{"benutzer, aktiv erreichbar", MirrorInfo{On: true}, false, true, true},
		{"benutzer, aktiv weg", MirrorInfo{On: true}, false, false, false},
		{"admin", MirrorInfo{On: true}, true, true, false},
		{"eingeschränkt, benutzer", MirrorInfo{On: true, Restricted: true}, false, false, true},
	}
	for _, c := range cases {
		h := &Handler{}
		h.SetMirror(c.info)
		var seenUser *users.User
		h.mirrorAdminFn = func(r *http.Request) bool {
			if u, ok := r.Context().Value("user").(*users.User); ok {
				seenUser = u
			}
			return c.admin
		}
		h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return mirrorStatus(c.seen), nil }
		rec := httptest.NewRecorder()
		got := h.mirrorLoginRefused(rec, httptest.NewRequest(http.MethodPost, "/login", nil), user, "passwort", "max")
		if got != c.refuse {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.refuse)
			continue
		}
		if c.info.On && seenUser != user {
			t.Errorf("%s: Benutzer nicht im Kontext", c.name)
		}
		if got && (rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?spiegel=1" || len(rec.Result().Cookies()) != 0) {
			t.Errorf("%s: %d %q Cookies=%v", c.name, rec.Code, rec.Header().Get("Location"), rec.Result().Cookies())
		}
		if !got && (rec.Code != http.StatusOK || rec.Body.Len() != 0) {
			t.Errorf("%s: unerwartete Antwort %d", c.name, rec.Code)
		}
	}
}

// mirrorLoginHandler: Spiegel an, aktiver Server erreichbar, Anmeldung ohne Datenbank.
func mirrorLoginHandler(admin bool) *Handler {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	h.mirrorAdminFn = func(*http.Request) bool { return admin }
	h.mirrorPeerFetch = func(context.Context) (haStatus, error) { return mirrorStatus(true), nil }
	u := &users.User{ID: "u1", FirstName: "Max", Role: users.RoleTechnician, Active: true}
	h.loginFn = func(_ context.Context, email, pw string) (string, *users.User, error) {
		if email != "max" || pw != "geheim" {
			return "", nil, errors.New("falsch")
		}
		return "tok", u, nil
	}
	h.loginRFIDFn = func(_ context.Context, uid string) (string, *users.User, error) {
		if uid != "AB12" {
			return "", nil, errors.New("unbekannt")
		}
		return "tok", u, nil
	}
	return h
}

func TestMirrorLoginPostRuleC(t *testing.T) {
	cases := []struct {
		name string
		call func(h *Handler, w http.ResponseWriter, r *http.Request)
		body string
	}{
		{"passwort", (*Handler).LoginPost, "email=max&password=geheim&next=%2Ftickets"},
		{"rfid", (*Handler).LoginRFIDWeb, "uid=AB12&next=%2Ftickets"},
	}
	for _, c := range cases {
		for _, admin := range []bool{false, true} {
			h := mirrorLoginHandler(admin)
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			c.call(h, rec, req)
			var tok bool
			for _, ck := range rec.Result().Cookies() {
				if ck.Name == "pdh_token" && ck.Value != "" {
					tok = true
				}
			}
			loc := rec.Header().Get("Location")
			if admin {
				if rec.Code != http.StatusFound || loc != "/tickets" || !tok {
					t.Errorf("%s/Admin: %d %q Cookie=%v", c.name, rec.Code, loc, tok)
				}
				continue
			}
			if rec.Code != http.StatusFound || loc != "/login?spiegel=1" || tok {
				t.Errorf("%s/Benutzer: %d %q Cookie=%v", c.name, rec.Code, loc, tok)
			}
		}
	}
}

func TestMirrorRestrictedBackground(t *testing.T) {
	hd, calls, tok := mirrorGateTest(t, MirrorInfo{On: true, Restricted: true}, true, mirrorStatus(false))
	html := map[string]string{"Accept": "text/html,application/xhtml+xml"}
	if rec := mirrorSessionGet(hd, tok, "/tickets", html); rec.Code != http.StatusFound {
		t.Fatalf("Seitenaufruf: %d", rec.Code)
	}
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
	}{
		"fetch json":  {"/api/presence", map[string]string{"Accept": "application/json"}},
		"ohne accept": {"/api/presence", nil},
		"fragment":    {"/tickets?fragment=1", html},
	} {
		if rec := mirrorSessionGet(hd, tok, c.path, c.hdr); rec.Code != http.StatusNoContent || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, p := range []string{"/help", "/help/kapitel"} {
		if rec := mirrorSessionGet(hd, tok, p, html); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if *calls != 2 {
		t.Fatalf("durchgereicht: %d", *calls)
	}
}

func TestMirrorRestrictedURL(t *testing.T) {
	if mirrorRestrictedURL != "/admin/server-config?tab=dr" {
		t.Fatalf("mirrorRestrictedURL = %q", mirrorRestrictedURL)
	}
}

func TestMirrorReasonInBand(t *testing.T) {
	out := mirrorBannerPage(t, &MirrorBanner{Restricted: true, Reason: "Programm zu alt für diese Datenbank"})
	if !strings.Contains(out, "Programm zu alt für diese Datenbank") || strings.Contains(out, "wartet auf das Update") {
		t.Errorf("Grund fehlt im Band: %s", out)
	}
	out = mirrorBannerPage(t, &MirrorBanner{Restricted: true})
	if !strings.Contains(out, "Spiegel wartet auf das Update des aktiven Servers") {
		t.Error("ohne Grund: bisheriger Text")
	}
}

// Die Migration 154 legt die Rechte an und vergibt sie an die richtigen Rollen
// (Textprüfung der SQL-Datei, da der Test keine Datenbank braucht).
func TestMaintenanceRightsMigration(t *testing.T) {
	b, err := os.ReadFile("../../migrations/154_verbund_update.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"'system.maintenance_window'", "Wartungsfenster einstellen",
		"'system.maintenance_abort'", "Automatische Wartung abbrechen",
		"'System & Sicherheit'", "o.key = 'system.server_config'",
		"o.key = 'system.manage_roles'", "LIKE '%.admin'", "ON CONFLICT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 154: %q fehlt", want)
		}
	}
	// Beide Vergaben nur aus globalen Quellrechten (Abteilungsrechte geben sie nicht)
	if n := strings.Count(s, "rp.department_id IS NULL"); n != 2 {
		t.Errorf("Migration 154: department_id-IS-NULL-Bedingung %d-mal, erwartet 2", n)
	}
}
