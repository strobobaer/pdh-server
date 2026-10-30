package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
	"pdh/internal/ha"
)

// Wartungssperre (maint_lock.go): Zustandsquelle, Uhr, Recht und
// Einstellungen sind Attrappen – kein Socket, kein Agent, keine Datenbank.

// maintFake: veränderbarer Auftrag samt Uhr für einen Test-Handler.
type maintFake struct {
	mu      sync.Mutex
	view    ha.UpdateView
	err     error
	calls   int
	clock   time.Time
	setting map[string]string
}

func (f *maintFake) set(v ha.UpdateView, err error) {
	f.mu.Lock()
	f.view, f.err = v, err
	f.mu.Unlock()
}

// advance stellt die Uhr vor (Zwischenspeicher 1 s, Frist 30 s).
func (f *maintFake) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func lockedView() ha.UpdateView {
	return ha.UpdateView{
		Active: true, Kind: "rolling", Phase: "locked", Locked: true,
		Steps: []string{"Abmelden", "Rollentausch", "Datenbank aktualisieren", "Prüfen", "Freigabe"},
		Step:  1, Percent: 20, Text: "Rollentausch läuft", RemainingS: 1500,
		FromVersion: "0.77.7", ToVersion: "0.77.8",
	}
}

// maintTestHandler: Handler mit Attrappen; Benutzer „admin“ hat das Recht,
// „u1“ nicht. next zählt die Aufrufe.
func maintTestHandler(t *testing.T, v ha.UpdateView) (*Handler, *maintFake) {
	t.Helper()
	f := &maintFake{view: v, clock: time.Now(), setting: map[string]string{}}
	h := &Handler{jwtSecret: boardTestSecret}
	h.streams, h.closeStreams = context.WithCancel(context.Background())
	h.mlock.fetch = func(context.Context) (ha.UpdateView, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		return f.view, f.err
	}
	h.mlock.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.clock
	}
	h.mlock.admin = func(u *users.User) bool { return u != nil && u.ID == "admin" }
	h.mlock.getSetting = func(_ context.Context, k string) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.setting[k]
	}
	h.mlock.setSetting = func(_ context.Context, k, val string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.setting[k] = val
		return nil
	}
	h.userByIDFn = func(_ context.Context, id string) (*users.User, error) {
		if id == "admin" {
			return &users.User{ID: id, FirstName: "Max", LastName: "Muster", Role: users.RoleAdmin, Active: true}, nil
		}
		return &users.User{ID: id, FirstName: "Erika", Role: users.RoleTechnician, Active: true}, nil
	}
	return h, f
}

// maintChain: Sperre vor der Anmeldung vor einem Handler, der „ok“ schreibt.
func maintChain(h *Handler) (http.Handler, *int) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("ok"))
	})
	return h.MaintLockMiddleware(h.authMiddleware(next)), &calls
}

// maintPlain: nur die Sperre vor einem Handler, der „ok“ schreibt.
func maintPlain(h *Handler) (http.Handler, *int) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("ok"))
	})
	return h.MaintLockMiddleware(next), &calls
}

func maintToken(t *testing.T, sub string, iat time.Time) string {
	return boardTestToken(t, jwt.MapClaims{"sub": sub, "iat": iat.Unix(), "exp": time.Now().Add(time.Hour).Unix()})
}

type maintReq struct {
	method, path, body, tok string
	hdr                     map[string]string
}

func maintDo(hd http.Handler, q maintReq) *httptest.ResponseRecorder {
	var req *http.Request
	if q.body != "" {
		req = httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(q.method, q.path, nil)
	}
	if q.tok != "" {
		req.AddCookie(&http.Cookie{Name: "pdh_token", Value: q.tok})
	}
	for k, v := range q.hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hd.ServeHTTP(rec, req)
	return rec
}

var htmlAccept = map[string]string{"Accept": "text/html,application/xhtml+xml"}

func TestLockRefusesWrites(t *testing.T) {
	h, _ := maintTestHandler(t, lockedView())
	hd, calls := maintChain(h)
	admin := maintToken(t, "admin", time.Now())
	cases := []struct {
		name string
		q    maintReq
		kind string // html, hx, json
	}{
		{"HTML-POST", maintReq{method: "POST", path: "/tickets", body: "title=x", hdr: htmlAccept}, "html"},
		{"HTML-POST Administrator", maintReq{method: "POST", path: "/tickets", body: "title=x", tok: admin, hdr: htmlAccept}, "html"},
		{"HX-POST", maintReq{method: "POST", path: "/tickets/1/comment", body: "text=x", hdr: map[string]string{"HX-Request": "true", "Accept": "*/*"}}, "hx"},
		{"HX-POST Administrator", maintReq{method: "POST", path: "/tickets/1/comment", body: "text=x", tok: admin, hdr: map[string]string{"HX-Request": "true"}}, "hx"},
		{"API-POST mit Bearer", maintReq{method: "POST", path: "/api/v1/tickets", body: "{}", hdr: map[string]string{"Authorization": "Bearer abc", "Accept": "application/json"}}, "json"},
		{"API-POST Browser", maintReq{method: "POST", path: "/api/v1/tickets", body: "{}", hdr: htmlAccept}, "json"},
		{"Webhook-POST", maintReq{method: "POST", path: "/n8n/webhook/abc", body: "{}", hdr: map[string]string{"Accept": "*/*"}}, "json"},
		{"Webhook-POST Browser-Accept", maintReq{method: "POST", path: "/n8n/webhook/abc", body: "{}", hdr: htmlAccept}, "json"},
		{"fetch-GET", maintReq{method: "GET", path: "/api/presence", hdr: map[string]string{"Accept": "application/json"}}, "json"},
		{"SSE-GET", maintReq{method: "GET", path: "/chat/stream", hdr: map[string]string{"Accept": "text/event-stream"}}, "json"},
	}
	for _, c := range cases {
		rec := maintDo(hd, c.q)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: Status %d, erwartet 503", c.name, rec.Code)
		}
		if rec.Header().Get("Retry-After") != "30" {
			t.Errorf("%s: Retry-After %q", c.name, rec.Header().Get("Retry-After"))
		}
		switch c.kind {
		case "html":
			if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "Automatische Wartung läuft") {
				t.Errorf("%s: kein Wartungsbildschirm: %q", c.name, rec.Body.String())
			}
		case "hx":
			if rec.Header().Get("HX-Redirect") != "/wartung" {
				t.Errorf("%s: HX-Redirect %q", c.name, rec.Header().Get("HX-Redirect"))
			}
		case "json":
			var body struct {
				Error      string `json:"error"`
				RetryAfter int    `json:"retry_after"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != "Wartung" || body.RetryAfter != 30 {
				t.Errorf("%s: JSON %q (%v)", c.name, rec.Body.String(), err)
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Errorf("%s: Content-Type %q", c.name, rec.Header().Get("Content-Type"))
			}
		}
	}
	if *calls != 0 {
		t.Fatalf("Handler %d-mal aufgerufen – während der Sperre darf nichts durchkommen", *calls)
	}
}

func TestLockAllowsStatusAndStatic(t *testing.T) {
	h, f := maintTestHandler(t, lockedView())
	hd, calls := maintPlain(h)
	for _, p := range []string{"/static/app.css", "/vendor/tabler-icons/tabler-icons.min.css", "/favicon.ico", "/site.webmanifest", "/health"} {
		before := *calls
		if rec := maintDo(hd, maintReq{method: "GET", path: p}); rec.Code != http.StatusOK || *calls != before+1 {
			t.Errorf("%s: %d, Handler nicht erreicht", p, rec.Code)
		}
	}
	// POST auf /health ist keine Ausnahme
	if rec := maintDo(hd, maintReq{method: "POST", path: "/health", body: "x=1"}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /health: %d", rec.Code)
	}
	// Fortschritt ohne Anmeldung
	rec := maintDo(hd, maintReq{method: "GET", path: "/wartung/status"})
	var v ha.UpdateView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || !v.Locked || v.ToVersion != "0.77.8" || v.Step != 1 {
		t.Fatalf("/wartung/status: %d %q", rec.Code, rec.Body.String())
	}
	// Anmeldeseite für Administratoren
	if rec := maintDo(hd, maintReq{method: "GET", path: "/wartung/anmelden", hdr: htmlAccept}); !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Errorf("/wartung/anmelden ohne Anmeldeformular: %q", rec.Body.String())
	}
	// Außerhalb der Sperre: alles durch, /wartung → Startseite, Status weiter abrufbar
	f.set(ha.UpdateView{}, nil)
	f.advance(2 * time.Second)
	before := *calls
	if rec := maintDo(hd, maintReq{method: "POST", path: "/tickets", body: "a=1", hdr: htmlAccept}); rec.Code != http.StatusOK || *calls != before+1 {
		t.Errorf("ohne Sperre: %d", rec.Code)
	}
	if rec := maintDo(hd, maintReq{method: "GET", path: "/wartung", hdr: htmlAccept}); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("GET /wartung ohne Sperre: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := maintDo(hd, maintReq{method: "GET", path: "/wartung/status"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"locked":false`) {
		t.Errorf("/wartung/status ohne Sperre: %d %q", rec.Code, rec.Body.String())
	}
}

func TestLockAdminSeesScreenWithButton(t *testing.T) {
	h, _ := maintTestHandler(t, lockedView())
	hd, calls := maintChain(h)
	rec := maintDo(hd, maintReq{method: "GET", path: "/tickets", tok: maintToken(t, "admin", time.Now()), hdr: htmlAccept})
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, `action="/wartung/abbrechen"`) {
		t.Fatalf("Administrator: %d, kein Abbruch-Knopf: %q", rec.Code, body)
	}
	for _, want := range []string{"0.77.7", "0.77.8", "Rollentausch läuft", "data-style", "/wartung/status"} {
		if !strings.Contains(body, want) {
			t.Errorf("Bildschirm ohne %q", want)
		}
	}
	if cookieCleared(rec, "pdh_token") {
		t.Error("Administrator wurde abgemeldet")
	}
	// Benutzer ohne Recht und Gäste: kein Knopf, aber Administrator-Anmeldung
	for name, tok := range map[string]string{"Benutzer": maintToken(t, "u1", time.Now()), "Gast": ""} {
		rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: tok, hdr: htmlAccept})
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), `action="/wartung/abbrechen"`) ||
			!strings.Contains(rec.Body.String(), `href="/wartung/anmelden"`) {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	if *calls != 0 {
		t.Fatal("Handler erreicht")
	}
}

func TestLockLogsOutNonAdmins(t *testing.T) {
	h, f := maintTestHandler(t, ha.UpdateView{Active: true, Phase: "countdown"})
	hd, calls := maintChain(h)
	old := time.Now().Add(-time.Minute)
	user, admin := maintToken(t, "u1", old), maintToken(t, "admin", old)
	// vor der Sperre: beide angemeldet
	for _, tok := range []string{user, admin} {
		if rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: tok, hdr: htmlAccept}); rec.Code != http.StatusOK {
			t.Fatalf("vor der Sperre: %d", rec.Code)
		}
	}
	stream := h.streamsCtx()
	// Sperre an: Epoche gesetzt und gespeichert, Dauerverbindungen beendet
	f.set(lockedView(), nil)
	f.advance(2 * time.Second)
	rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: user, hdr: htmlAccept})
	if rec.Code != http.StatusServiceUnavailable || !cookieCleared(rec, "pdh_token") {
		t.Fatalf("Benutzer während der Sperre: %d, Cookie gelöscht=%v", rec.Code, cookieCleared(rec, "pdh_token"))
	}
	nb := h.sessionNotBefore.Load()
	if nb < old.Unix()+1 {
		t.Fatalf("session.not_before nicht gesetzt: %d", nb)
	}
	if got := f.setting["session.not_before"]; got == "" || got != h.maintNotBeforeString() {
		t.Errorf("session.not_before nicht gespeichert: %q", got)
	}
	select {
	case <-stream.Done():
	default:
		t.Error("offene Dauerverbindungen nicht beendet")
	}
	if h.streamsCtx().Err() != nil {
		t.Error("neue Dauerverbindungen nach der Sperre unmöglich")
	}
	// zweites Sehen der Sperre: Epoche bleibt
	f.advance(2 * time.Second)
	maintDo(hd, maintReq{method: "GET", path: "/wartung/status"})
	if h.sessionNotBefore.Load() != nb {
		t.Error("Epoche bei jedem Abruf neu gesetzt")
	}
	// Sperre vorbei: alte Sitzung des Benutzers ungültig, Administrator bleibt
	f.set(ha.UpdateView{Result: "ok"}, nil)
	f.advance(2 * time.Second)
	before := *calls
	rec = maintDo(hd, maintReq{method: "GET", path: "/", tok: user, hdr: htmlAccept})
	if rec.Code != http.StatusFound || !cookieCleared(rec, "pdh_token") || *calls != before {
		t.Fatalf("alte Sitzung nach der Sperre: %d", rec.Code)
	}
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: admin, hdr: htmlAccept}); rec.Code != http.StatusOK || cookieCleared(rec, "pdh_token") {
		t.Fatalf("Administrator nach der Sperre: %d", rec.Code)
	}
	// neue Anmeldung nach der Sperre gilt
	fresh := maintToken(t, "u1", time.Unix(nb, 0))
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: fresh, hdr: htmlAccept}); rec.Code != http.StatusOK {
		t.Fatalf("neue Sitzung: %d", rec.Code)
	}
}

// Der Administrator bleibt über einen Neustart (bzw. den Tausch auf B mit
// replizierter Datenbank) angemeldet; der neue Prozess startet gesperrt.
func TestLockAdminKeepsSessionAfterRestart(t *testing.T) {
	h, f := maintTestHandler(t, lockedView())
	epoch := time.Now().Add(-30 * time.Second).Unix()
	f.setting["session.not_before"] = strconv.FormatInt(epoch, 10) // aus der (replizierten) Datenbank
	h.LoadMaintLock(context.Background())
	if h.sessionNotBefore.Load() < epoch {
		t.Fatalf("Epoche nicht geladen: %d < %d", h.sessionNotBefore.Load(), epoch)
	}
	// Abfrage danach gestört → letzter Stand (gesperrt) gilt weiter
	f.set(ha.UpdateView{}, errors.New("pdh-ha nicht erreichbar"))
	f.advance(2 * time.Second)
	hd, calls := maintChain(h)
	old := time.Unix(epoch-60, 0)
	admin, user := maintToken(t, "admin", old), maintToken(t, "u1", old)
	rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: admin, hdr: htmlAccept})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `action="/wartung/abbrechen"`) || cookieCleared(rec, "pdh_token") {
		t.Fatalf("nach dem Neustart gesperrt, Administrator mit Knopf: %d", rec.Code)
	}
	if *calls != 0 {
		t.Fatal("Start nicht gesperrt")
	}
	// Sperre vorbei: Administrator weiter angemeldet, Benutzer nicht
	// (nach einer Störung fragt die Sperre erst nach 5 s erneut)
	f.set(ha.UpdateView{Result: "ok"}, nil)
	f.advance(6 * time.Second)
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: admin, hdr: htmlAccept}); rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("Administrator nach dem Neustart: %d", rec.Code)
	}
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", tok: user, hdr: htmlAccept}); rec.Code != http.StatusFound || !cookieCleared(rec, "pdh_token") {
		t.Fatalf("Benutzer nach dem Neustart: %d", rec.Code)
	}
}

func TestLockLoginOnlyAdmins(t *testing.T) {
	h, _ := maintTestHandler(t, lockedView())
	h.loginFn = func(_ context.Context, email, pw string) (string, *users.User, error) {
		u, _ := h.userByIDFn(context.Background(), email)
		if pw != "geheim" {
			return "", nil, errors.New("falsch")
		}
		return maintToken(t, email, time.Now()), u, nil
	}
	hd, _ := maintChain(h)
	post := func(body string) *httptest.ResponseRecorder {
		return maintDo(hd, maintReq{method: "POST", path: "/wartung/anmelden", body: body, hdr: htmlAccept})
	}
	hasToken := func(rec *httptest.ResponseRecorder) bool {
		for _, c := range rec.Result().Cookies() {
			if c.Name == "pdh_token" && c.Value != "" {
				return true
			}
		}
		return false
	}
	rec := post("email=u1&password=geheim")
	if hasToken(rec) || !strings.Contains(rec.Body.String(), "Anmeldung während der Wartung nur für Administratoren") {
		t.Fatalf("Benutzer ohne Recht: %d %q", rec.Code, rec.Body.String())
	}
	rec = post("email=admin&password=falsch")
	if hasToken(rec) || !strings.Contains(rec.Body.String(), "Ungültige Anmeldedaten") {
		t.Fatalf("falsches Passwort: %d %q", rec.Code, rec.Body.String())
	}
	rec = post("email=admin&password=geheim")
	if !hasToken(rec) || rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/wartung" {
		t.Fatalf("Administrator: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLockFailOpenAfter30s(t *testing.T) {
	h, f := maintTestHandler(t, lockedView())
	hd, calls := maintPlain(h)
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", hdr: htmlAccept}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gesperrt: %d", rec.Code)
	}
	f.set(ha.UpdateView{}, errors.New("pdh-ha nicht erreichbar"))
	for _, d := range []time.Duration{2 * time.Second, 10 * time.Second, 15 * time.Second} {
		f.advance(d)
		if rec := maintDo(hd, maintReq{method: "GET", path: "/", hdr: htmlAccept}); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("nach %v Störung schon offen: %d", d, rec.Code)
		}
	}
	f.advance(5 * time.Second) // 32 s ohne Antwort
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", hdr: htmlAccept}); rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("nach 30 s ohne Antwort nicht offen: %d", rec.Code)
	}
	// Antwort wieder da → Sperre gilt wieder
	f.set(lockedView(), nil)
	f.advance(6 * time.Second)
	if rec := maintDo(hd, maintReq{method: "GET", path: "/", hdr: htmlAccept}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Antwort wieder da: %d", rec.Code)
	}
	// Zwischenspeicher: höchstens eine Abfrage je Sekunde
	n := f.calls
	for i := 0; i < 5; i++ {
		maintDo(hd, maintReq{method: "GET", path: "/wartung/status"})
	}
	if f.calls != n {
		t.Errorf("Zwischenspeicher wirkt nicht: %d Abfragen", f.calls-n)
	}
}

// maintHAEtc: Rechner gehört zum Verbund (node.json vorhanden).
func maintHAEtc(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_HA_ETC", dir)
}

func TestLockAbortCallsHA(t *testing.T) {
	maintHAEtc(t)
	fake := &fakeHA{}
	startFakeHA(t, fake)
	h, _ := maintTestHandler(t, lockedView())
	hd, _ := maintChain(h)
	// ohne Recht: abgewiesen, kein Aufruf
	rec := maintDo(hd, maintReq{method: "POST", path: "/wartung/abbrechen", body: "x=1", tok: maintToken(t, "u1", time.Now()), hdr: htmlAccept})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht: %d", rec.Code)
	}
	// fremde Seite (CSRF): abgewiesen
	rec = maintDo(hd, maintReq{method: "POST", path: "/wartung/abbrechen", body: "x=1", tok: maintToken(t, "admin", time.Now()),
		hdr: map[string]string{"Accept": "text/html", "Sec-Fetch-Site": "cross-site"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("fremde Seite: %d", rec.Code)
	}
	for _, c := range fake.callList() {
		if strings.Contains(c, "abort") {
			t.Fatalf("Abbruch ohne Berechtigung ausgelöst: %v", fake.callList())
		}
	}
	rec = maintDo(hd, maintReq{method: "POST", path: "/wartung/abbrechen", body: "x=1", tok: maintToken(t, "admin", time.Now()),
		hdr: map[string]string{"Accept": "text/html", "Sec-Fetch-Site": "same-origin"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/wartung") {
		t.Fatalf("Administrator: %d %q %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	calls := fake.callList()
	idx := -1
	for i, c := range calls {
		if c == "POST /v1/update/abort" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("pdh-ha nicht aufgerufen: %v", calls)
	}
	fake.mu.Lock()
	body := fake.bodies[idx]
	fake.mu.Unlock()
	if !strings.Contains(body, `"by":"Max Muster"`) {
		t.Errorf("Abbruch ohne Namen: %q", body)
	}
}

// Einzelserver: Abbruch und Zustand über den Update-Agenten.
func TestLockAbortCallsAgent(t *testing.T) {
	t.Setenv("PDH_HA_ETC", filepath.Join(t.TempDir(), "fehlt"))
	var mu sync.Mutex
	var got []string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		if r.URL.Path == "/v1/update/job" {
			w.Write([]byte(`{"ok":true,"active":true,"phase":"locked","locked":true,"step":1,"from_version":"0.77.7","to_version":"0.77.8"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer agent.Close()
	h, _ := maintTestHandler(t, ha.UpdateView{})
	h.mlock.fetch, h.mlock.abort = nil, nil
	h.updateAgentURL, h.updateAgentToken = agent.URL, "tok"
	v, err := h.maintFetch(context.Background())
	if err != nil || !v.Locked || v.ToVersion != "0.77.8" || v.Step != 1 {
		t.Fatalf("Zustand vom Agenten: %+v %v", v, err)
	}
	if err := h.maintAbort(context.Background(), "Max Muster"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[1] != `POST /v1/update/job/abort {"by":"Max Muster"}` {
		t.Fatalf("Agent-Aufrufe: %q", got)
	}
}

// Ohne Verbund und ohne Agent (z. B. Docker-Testsystem) nie gesperrt.
func TestLockNeverWithoutSource(t *testing.T) {
	t.Setenv("PDH_HA_ETC", filepath.Join(t.TempDir(), "fehlt"))
	h := &Handler{}
	if v, err := h.maintFetch(context.Background()); err != nil || v.Locked {
		t.Fatalf("ohne Quelle: %+v %v", v, err)
	}
}
