package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

// terminalTestHandler: Handler ohne Datenbank, nur mit Vorlagen.
func terminalTestHandler(t *testing.T) (*Handler, chi.Router) {
	t.Helper()
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	h := &Handler{tmpl: loadTestTemplates(t), jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.terminalRoutes(r)
	return h, r
}

func terminalGet(r http.Handler, target string, u *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", u))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTerminalRequiresSystemUser(t *testing.T) {
	_, r := terminalTestHandler(t)
	normal := &users.User{ID: "11111111-1111-1111-1111-111111111111", Role: users.Role("admin"), FirstName: "A", LastName: "B"}
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu", "/terminal/info.json"} {
		rec := terminalGet(r, p, normal)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur für Terminal-Benutzer") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestTerminalWithoutInfra(t *testing.T) {
	_, r := terminalTestHandler(t)
	sys := &users.User{ID: "22222222-2222-2222-2222-222222222222", Role: users.Role("viewer"), IsSystemUser: true}
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		rec := terminalGet(r, p, sys)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Diesem Terminal ist keine Anlage zugeordnet") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	// JSON: klare Antwort statt 500
	rec := terminalGet(r, "/terminal/info.json", sys)
	if rec.Code != http.StatusNotFound {
		t.Errorf("info.json ohne Anlage: %d", rec.Code)
	}
}

func TestTerminalTemplateIdleAndEscaping(t *testing.T) {
	h, _ := terminalTestHandler(t)
	d := terminalPage{Mode: "info", Idle: 45, CanFaults: true, CanTickets: true, EasyMode: true, InfraID: "33333333-3333-3333-3333-333333333333", Name: `<b>Presse</b>`, Path: `A › "B"`,
		Info: terminalInfo{Faults: []terminalItem{{ID: "f1", Title: `<script>alert(1)</script>`, Path: "Unter", StatusLabel: "Offen", Age: "vor 3 h"}}, FaultsTotal: 60}}
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, d)
	body := rec.Body.String()
	if !strings.Contains(body, `data-idle="45"`) {
		t.Error("data-idle fehlt")
	}
	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<b>Presse</b>") {
		t.Error("nicht maskiert")
	}
	if !strings.Contains(body, "59 weitere") {
		t.Errorf("Hinweis auf weitere Einträge fehlt")
	}
	// Schoner: kein Inaktivitäts-Timer
	d.Mode, d.Idle = "schoner", 0
	rec = httptest.NewRecorder()
	h.renderTerminal(rec, d)
	if !strings.Contains(rec.Body.String(), `data-idle="0"`) {
		t.Error("Schoner darf keinen Timer haben")
	}
}

func TestTerminalAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "gerade eben", 12 * time.Minute: "vor 12 min", 3 * time.Hour: "vor 3 h",
		24 * time.Hour: "vor 1 Tag", 48 * time.Hour: "vor 2 Tagen", -time.Minute: "gerade eben",
	} {
		if got := terminalAge(now, now.Add(-d)); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestTerminalRelPath(t *testing.T) {
	nodes := map[string]terminalNode{
		"a": {Name: "Halle", Parent: ""}, "b": {Name: "Linie", Parent: "a"}, "c": {Name: "Presse", Parent: "b"},
	}
	if got := terminalRelPath(nodes, "c", "b"); got != "Presse" {
		t.Errorf("rel: %q", got)
	}
	if got := terminalRelPath(nodes, "b", "b"); got != "" {
		t.Errorf("Wurzel: %q", got)
	}
	if got := terminalRelPath(nodes, "c", "a"); got != "Linie › Presse" {
		t.Errorf("tief: %q", got)
	}
}

func TestTerminalTemplateCardEvents(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(d terminalPage) string {
		rec := httptest.NewRecorder()
		h.renderTerminal(rec, d)
		return rec.Body.String()
	}
	base := terminalPage{Mode: "info", Idle: 45, CanFaults: true, CanTickets: true, InfraID: "33333333-3333-3333-3333-333333333333", Name: "Presse"}
	if strings.Contains(render(base), "data-card-events") {
		t.Error("ohne Geräte-Token darf data-card-events nicht gesetzt sein")
	}
	base.CardEvents = true
	if !strings.Contains(render(base), `data-card-events="1"`) {
		t.Error("data-card-events fehlt im Modus info")
	}
	base.Mode = "neu"
	if !strings.Contains(render(base), `data-card-events="1"`) {
		t.Error("data-card-events fehlt im Modus neu")
	}
	// Schoner: abonniert den Kartenstrom ebenfalls (Karte weckt ihn), aber nur in der Gerätesitzung
	base.Mode, base.Idle = "schoner", 0
	base.CardNotify = `["overlay"]`
	sc := render(base)
	if !strings.Contains(sc, `data-card-events="1"`) {
		t.Error("Schoner trägt data-card-events nicht")
	}
	if !strings.Contains(sc, `data-card-notify="[&#34;overlay&#34;]"`) {
		t.Errorf("Schoner trägt data-card-notify nicht: %s", sc)
	}
	if strings.Contains(sc, "data-card-bar") || strings.Contains(sc, "login-panel") {
		t.Error("Schoner darf keine Anmelde-Oberfläche tragen")
	}
	base.CardEvents = false
	if strings.Contains(render(base), "data-card-events") {
		t.Error("Schoner ohne Geräte-Token darf nichts abonnieren")
	}
}

func TestTerminalNotifyAttribute(t *testing.T) {
	h, _ := terminalTestHandler(t)
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, terminalPage{Mode: "info", Idle: 45, CardEvents: true, CanFaults: true, CanTickets: true,
		InfraID: "33333333-3333-3333-3333-333333333333", Name: "Presse", ManualLogin: "card_pin", PasswordButton: true,
		CardNotify: `["overlay","banner"]`})
	if !strings.Contains(rec.Body.String(), `data-card-notify="[&#34;overlay&#34;,&#34;banner&#34;]"`) {
		t.Error("data-card-notify mit der wirksamen Liste fehlt")
	}
}

// Rückmeldung an das Terminal: Aufruf an /api/card/result, jeder Fehler (alter Stand: 404) bleibt stumm.
func TestTerminalScriptResultReport(t *testing.T) {
	_, r := terminalTestHandler(t)
	b := terminalGet(r, "/terminal/terminal.js", &users.User{ID: "x", IsSystemUser: true}).Body.String()
	for _, s := range []string{"/api/card/result", "mode: 'cors'", "pdh.pendingCard", "/terminal/info", "resultPayload", "takePendingCard"} {
		if !strings.Contains(b, s) {
			t.Errorf("Skript enthält %q nicht", s)
		}
	}
	i := strings.Index(b, "CARD_ORIGIN + '/api/card/result'")
	if i < 0 {
		t.Fatal("Aufruf an CARD_ORIGIN fehlt")
	}
	// Der Aufruf endet mit einem stillen catch (kein console.*), sonst gäbe es Fehler am alten Terminal.
	j := strings.Index(b[i:], ".catch(function")
	if j < 0 || j > 700 {
		t.Error("Aufruf von /api/card/result hat kein catch")
	}
	if strings.Contains(b, "console.") {
		t.Error("Skript schreibt in die Konsole")
	}
}

// Die reinen Hilfsfunktionen laufen mit node (wie die Zeitstrahl-Logik); ohne node wird übersprungen.
func TestTerminalScriptPureFunctions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	src, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n")) // Arbeitskopie kann CRLF haben
	var fns []string
	for _, name := range []string{"resultPayload", "logoutResult", "takePendingCard", "parseNotify"} {
		m := regexp.MustCompile(`(?s)\n  function ` + name + `\(.*?\n  \}\n`).Find(src)
		if m == nil {
			t.Fatalf("Funktion %s nicht gefunden", name)
		}
		fns = append(fns, string(m))
	}
	harness := strings.Join(fns, "\n") + `
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
eq(resultPayload({ok: true, name: 'Anna', message: ''}, ['overlay'], false), {ok: true, name: 'Anna', message: '', notify: ['overlay'], logout: false}, 'Anmeldung');
eq(resultPayload({ok: false, message: 'Karte nicht registriert'}, ['toast'], false), {ok: false, name: '', message: 'Karte nicht registriert', notify: ['toast'], logout: false}, 'Fehler');
eq(resultPayload({ok: true, name: 'Anna'}, [], true), {ok: true, name: 'Anna', message: '', notify: [], logout: true}, 'Abmeldung');
eq(resultPayload(null, null, false), {ok: false, name: '', message: '', notify: [], logout: false}, 'leer');
eq(logoutResult({ok: true}, 'Anna'), {ok: true, name: 'Anna', message: ''}, 'Abmeldung angenommen');
eq(logoutResult({ok: false, status: 500}, 'Anna'), {ok: false, name: 'Anna', message: 'Abmeldung fehlgeschlagen'}, 'Abmeldung mit HTTP-Fehler');
eq(logoutResult(null, 'Anna'), {ok: false, name: 'Anna', message: 'Abmeldung fehlgeschlagen'}, 'Abmeldung ohne Antwort');
eq(logoutResult({ok: true}, undefined), {ok: true, name: '', message: ''}, 'Abmeldung ohne Namen');
eq(parseNotify('["overlay","banner"]'), ['overlay', 'banner'], 'Liste');
eq(parseNotify(undefined), [], 'fehlt');
eq(parseNotify('kaputt'), [], 'ungültig');
eq(parseNotify('{"a":1}'), [], 'kein Feld');
eq(parseNotify('[1,"front"]'), ['front'], 'nur Texte');
const mk = (v) => { const m = {}; if (v !== undefined) m['pdh.pendingCard'] = v; return { m, getItem: k => (k in m ? m[k] : null), removeItem: k => { delete m[k]; } }; };
let s = mk(JSON.stringify({uid: 'AB12', t: 100000}));
eq(takePendingCard(s, 105000), 'AB12', 'frisch');
eq(s.m, {}, 'frisch gelöscht');
s = mk(JSON.stringify({uid: 'AB12', t: 100000}));
eq(takePendingCard(s, 110000), 'AB12', 'genau 10 s');
s = mk(JSON.stringify({uid: 'AB12', t: 100000}));
eq(takePendingCard(s, 110001), '', 'zu alt');
eq(s.m, {}, 'zu alt gelöscht');
s = mk('kaputt');
eq(takePendingCard(s, 1), '', 'ungültig');
eq(s.m, {}, 'ungültig gelöscht');
s = mk(JSON.stringify({uid: '', t: 1}));
eq(takePendingCard(s, 2), '', 'leere UID');
s = mk(JSON.stringify({uid: 'X'}));
eq(takePendingCard(s, 2), '', 'ohne Zeit');
eq(takePendingCard(mk(), 2), '', 'kein Eintrag');
eq(takePendingCard({getItem() { throw new Error('x'); }, removeItem() { throw new Error('x'); }}, 2), '', 'Speicher wirft');
if (out.length) { console.log(out.join('\n')); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "t.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("Hilfsfunktionen: %s", b)
	}
}

func TestTerminalDeviceSession(t *testing.T) {
	h := &Handler{jwtSecret: "test-geheim"}
	mk := func(claims jwt.MapClaims) *http.Request {
		claims["sub"], claims["iat"], claims["exp"] = "u1", time.Now().Unix(), time.Now().Add(time.Hour).Unix()
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-geheim"))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/terminal/info", nil)
		req.AddCookie(&http.Cookie{Name: "pdh_token", Value: s})
		return req
	}
	if !h.terminalDeviceSession(mk(jwt.MapClaims{"device": "dev-1"})) {
		t.Error("Geräte-Token nicht erkannt")
	}
	if h.terminalDeviceSession(mk(jwt.MapClaims{})) {
		t.Error("normale Sitzung gilt als Gerät")
	}
	if h.terminalDeviceSession(httptest.NewRequest("GET", "/terminal/info", nil)) {
		t.Error("ohne Cookie gilt als Gerät")
	}
}

func TestTerminalScriptCardReader(t *testing.T) {
	_, r := terminalTestHandler(t)
	rec := terminalGet(r, "/terminal/terminal.js", &users.User{ID: "x", IsSystemUser: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	b := rec.Body.String()
	for _, s := range []string{"127.0.0.1:8080", "/api/card/events", "/terminal/card/ping", "/terminal/card/abmelden", "/terminal/card"} {
		if !strings.Contains(b, s) {
			t.Errorf("Skript enthält %q nicht", s)
		}
	}
	if strings.Contains(b, "innerHTML") {
		t.Error("innerHTML im Skript")
	}
}

func TestTerminalTemplateManualLogin(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(d terminalPage) string {
		rec := httptest.NewRecorder()
		h.renderTerminal(rec, d)
		return rec.Body.String()
	}
	base := terminalPage{Mode: "info", Idle: 45, CardEvents: true, CanFaults: true, CanTickets: true,
		InfraID: "33333333-3333-3333-3333-333333333333", Name: "Presse", ManualLogin: "card_pin", PasswordButton: true}
	body := render(base)
	for _, want := range []string{`id="login-panel"`, `id="login-keypad"`, `id="login-password-btn"`, `id="login-dialog"`,
		"Mit Benutzer und Passwort", `data-manual-login="card_pin"`, `data-password-button="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s fehlt", want)
		}
	}
	base.ManualLogin, base.PasswordButton = "none", false
	body = render(base)
	if !strings.Contains(body, `data-manual-login="none"`) || !strings.Contains(body, `data-password-button="0"`) {
		t.Error("Werte des Terminals fehlen")
	}
	// ohne Gerätesitzung und im Schoner: kein Anmeldeteil
	base.CardEvents = false
	if strings.Contains(render(base), "login-panel") {
		t.Error("ohne Gerätesitzung kein Anmeldeteil")
	}
	base.CardEvents, base.Mode, base.Idle = true, "schoner", 0
	body = render(base)
	if strings.Contains(body, "login-panel") || strings.Contains(body, "data-manual-login") {
		t.Error("Schoner darf keinen Anmeldeteil haben")
	}
}

// Die Seiten tragen data-settings-events nur, wenn das Feld gesetzt ist; auch die Seite „keine Anlage“ kann es tragen.
func TestTerminalTemplateSettingsEvents(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(d terminalPage) string {
		rec := httptest.NewRecorder()
		h.renderTerminal(rec, d)
		return rec.Body.String()
	}
	for _, d := range []terminalPage{
		{Mode: "schoner", Name: "Presse", SettingsEvents: true},
		{Mode: "info", NoInfra: true, SettingsEvents: true},
	} {
		if !strings.Contains(render(d), `data-settings-events="1"`) {
			t.Errorf("%+v: data-settings-events fehlt", d)
		}
	}
	if strings.Contains(render(terminalPage{Mode: "schoner", Name: "Presse"}), "data-settings-events") {
		t.Error("ohne Gerätesitzung darf data-settings-events nicht stehen")
	}
}

// Das Skript öffnet /terminal/events auch ohne Anmeldeteil (Schoner, Seite „keine Anlage“) und lädt bei global_screen neu.
func TestTerminalScriptSettingsStreamOnSchoner(t *testing.T) {
	b, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(s, "body.dataset.settingsEvents === '1'")
	if i < 0 {
		t.Fatal("Skript prüft data-settings-events nicht")
	}
	blk := s[i:]
	if j := strings.Index(blk, "// Bildschirmschoner"); j > 0 {
		blk = blk[:j]
	}
	for _, want := range []string{"new EventSource('/terminal/events')", "global_screen === true", "location.reload()", "mode === 'schoner'"} {
		if !strings.Contains(blk, want) {
			t.Errorf("Block enthält %q nicht", want)
		}
	}
}
