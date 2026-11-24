package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

// Seite im Modus global: Bühne, kein Kartenstrom, keine Anmeldung, kein Bericht.
func TestTerminalGlobalTemplate(t *testing.T) {
	h, _ := terminalTestHandler(t)
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, terminalPage{Mode: "global", Name: "PDH"})
	b := rec.Body.String()
	if !strings.Contains(b, `data-mode="global"`) || !strings.Contains(b, `id="g-stage"`) {
		t.Error("Bühne fehlt")
	}
	for _, bad := range []string{"data-card-events", "data-card-notify", "login-panel", "login-dialog", "data-card-bar", "data-manual-login", `href="/terminal/neu"`, "/terminal/info"} {
		if strings.Contains(b, bad) {
			t.Errorf("globale Seite enthält %q", bad)
		}
	}
	if strings.Contains(b, "<a ") {
		t.Error("globale Seite enthält einen Link")
	}
}

// Vorgang-Ansicht: ein Link („Zurück“), keine Formulare, Texte maskiert; 404-Seite verrät nichts.
func TestTerminalVorgangTemplate(t *testing.T) {
	h, _ := terminalTestHandler(t)
	rec := httptest.NewRecorder()
	h.renderTerminalVorgang(rec, http.StatusOK, terminalVorgang{Idle: 60, KindLabel: "Störung", Title: `<script>alert(1)</script>`, StatusLabel: "Offen",
		Infra: "Halle › Presse", Description: "Zeile 1\nZeile 2", Created: "01.01.2026 10:00", Updated: "02.01.2026 11:00",
		History: []terminalVorgangHist{{At: "02.01.2026 11:00", Text: "Ersatzteil <b>bestellt</b>"}}})
	b := rec.Body.String()
	if rec.Code != 200 || strings.Contains(b, "<script>alert") || strings.Contains(b, "<b>bestellt") {
		t.Errorf("Texte nicht maskiert: %d", rec.Code)
	}
	if strings.Count(b, "<a ") != 1 || strings.Count(b, "href=") != 1 || !strings.Contains(b, `href="/terminal"`) || !strings.Contains(b, "Zurück") {
		t.Error("genau ein Link „Zurück“ erwartet")
	}
	for _, bad := range []string{"<form", "<button", "<input", "<img", "/uploads", "/attachments"} {
		if strings.Contains(b, bad) {
			t.Errorf("Ansicht enthält %q", bad)
		}
	}
	for _, want := range []string{`data-mode="vorgang"`, `data-idle="60"`, "Halle › Presse", "Zeile 1", "Verlauf"} {
		if !strings.Contains(b, want) {
			t.Errorf("Ansicht enthält %q nicht", want)
		}
	}
	rec = httptest.NewRecorder()
	h.renderTerminalVorgang(rec, http.StatusNotFound, terminalVorgang{NotFound: true, Idle: 60})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "nicht (mehr) verfügbar") || strings.Count(rec.Body.String(), "<a ") != 1 {
		t.Errorf("404-Seite: %d", rec.Code)
	}
}

// Folienwege sind nur für Gerätesitzungen: normaler Terminal-Benutzer und normale Person bekommen 403.
func TestTerminalGlobalRoutesNeedDeviceSession(t *testing.T) {
	_, r := terminalTestHandler(t)
	sys := &users.User{ID: "22222222-2222-2222-2222-222222222222", Role: users.Role("viewer"), IsSystemUser: true}
	normal := &users.User{ID: "11111111-1111-1111-1111-111111111111", Role: users.Role("admin")}
	id := "33333333-3333-3333-3333-333333333333"
	for _, u := range []*users.User{sys, normal} {
		for _, p := range []string{"/terminal/slides.json", "/terminal/slides/" + id + "/image", "/terminal/vorgang/fault/" + id, "/terminal/vorgang/ticket/" + id} {
			if rec := terminalGet(r, p, u); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %d", p, rec.Code)
			}
		}
	}
}

// Skript: Zwischenspeicher am lokalen Terminal, stumme Fehler, nichts verlinkt.
func TestTerminalScriptGlobal(t *testing.T) {
	_, r := terminalTestHandler(t)
	b := terminalGet(r, "/terminal/terminal.js", &users.User{ID: "x", IsSystemUser: true}).Body.String()
	for _, s := range []string{"CARD_ORIGIN + '/api/slides/cache'", "mode: 'cors'", "cacheCall('DELETE'", "cacheCall('POST'", "/terminal/slides.json", "/terminal/events", "addEventListener('slides'", "global_screen", "/terminal/vorgang/", "cachePayload", "nextSlideIndex", "isSwipeLeft", "slideDurationMs", "outageStep(", "location.replace(CARD_ORIGIN + '/start')", "leaveVorgang", "backTarget(failed, CARD_ORIGIN)"} {
		if !strings.Contains(b, s) {
			t.Errorf("Skript enthält %q nicht", s)
		}
	}
	i := strings.Index(b, "CARD_ORIGIN + '/api/slides/cache'")
	if j := strings.Index(b[i:], ".catch(function"); j < 0 || j > 600 {
		t.Error("Aufruf von /api/slides/cache hat kein catch")
	}
	if strings.Contains(b, "innerHTML") || strings.Contains(b, "console.") {
		t.Error("innerHTML oder console im Skript")
	}
	// Der Bericht an /api/card/result und der Kartenstrom hängen am Attribut data-card-events, das die globale Seite nicht trägt.
	if !strings.Contains(b, "body.dataset.cardEvents === '1'") {
		t.Error("Kartenstrom nicht am Attribut data-card-events")
	}
}

func TestTerminalScriptGlobalPureFunctions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	src, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	var fns []string
	for _, name := range []string{"nextSlideIndex", "slideDurationMs", "isSwipeLeft", "cachePayload", "outageStep", "backTarget", "localOrigin"} {
		m := regexp.MustCompile(`(?s)\n  function ` + name + `\(.*?\n  \}\n`).Find(src)
		if m == nil {
			t.Fatalf("Funktion %s nicht gefunden", name)
		}
		fns = append(fns, string(m))
	}
	kinds := regexp.MustCompile(`var CACHEABLE_KINDS = [^\n]*`).Find(src)
	if kinds == nil {
		t.Fatal("CACHEABLE_KINDS nicht gefunden")
	}
	harness := "var OUTAGE_LIMIT = 3;\n" + string(kinds) + "\n" + strings.Join(fns, "\n") + `
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
const three = [{id: 'a'}, {id: 'b'}, {id: 'c'}];
eq(nextSlideIndex(three, 0), 1, 'weiter');
eq(nextSlideIndex(three, 1), 2, 'weiter 2');
eq(nextSlideIndex(three, 2), 0, 'reihum');
eq(nextSlideIndex(three, -1), 0, 'vor dem Anfang');
eq(nextSlideIndex(three, 7), 0, 'hinter dem Ende');
eq(nextSlideIndex(three, undefined), 0, 'ohne Index');
eq(nextSlideIndex([{id: 'a'}], 0), 0, 'eine Folie');
eq(nextSlideIndex([], 0), 0, 'keine Folie');
eq(nextSlideIndex(null, 0), 0, 'kein Feld');
eq(slideDurationMs({duration: 20}), 20000, 'Dauer');
eq(slideDurationMs({duration: 5}), 5000, 'Dauer unten');
eq(slideDurationMs({duration: 600}), 600000, 'Dauer oben');
eq(slideDurationMs({duration: 4}), 15000, 'zu kurz');
eq(slideDurationMs({duration: 601}), 15000, 'zu lang');
eq(slideDurationMs({}), 15000, 'ohne Dauer');
eq(slideDurationMs(null), 15000, 'ohne Folie');
eq(slideDurationMs({duration: '30'}), 15000, 'Text statt Zahl');
eq(isSwipeLeft({x: 300, y: 100}, {x: 240, y: 100}), true, 'genau 60 px');
eq(isSwipeLeft({x: 300, y: 100}, {x: 241, y: 100}), false, '59 px');
eq(isSwipeLeft({x: 300, y: 100}, {x: 100, y: 139}), true, 'senkrecht 39');
eq(isSwipeLeft({x: 300, y: 100}, {x: 100, y: 140}), false, 'senkrecht 40');
eq(isSwipeLeft({x: 300, y: 100}, {x: 100, y: 61}), true, 'senkrecht 39 nach oben');
eq(isSwipeLeft({x: 100, y: 100}, {x: 300, y: 100}), false, 'nach rechts');
eq(isSwipeLeft(null, {x: 1, y: 1}), false, 'ohne Start');
const d = new Date('2026-11-21T10:00:00Z');
const slides = [{id: 'a', kind: 'image'}];
let p = cachePayload({version: 'v1', idle: 120, slides: slides}, {a: 'QUJD'}, d);
eq(p, {version: 'v1', saved_at: '2026-11-21T10:00:00.000Z', slides: slides, images: {a: 'QUJD'}}, 'Nutzdaten');
eq(Object.keys(p), ['version', 'saved_at', 'slides', 'images'], 'Felder');
// Neue Folienarten (Statistik, Grafana) kommen nicht in den Zwischenspeicher älterer Terminals
const mixed = [{id: 'a', kind: 'image'}, {id: 's', kind: 'stats_faults'}, {id: 't', kind: 'text'}, {id: 'g', kind: 'grafana'}, {id: 'v', kind: 'vorgaenge'}, {id: 'x', kind: 'stats_setup'}, {id: 'y', kind: 'stats_tickets'}, null, {id: 'u'}];
eq(cachePayload({version: 'v', slides: mixed}, {}, d).slides.map((s) => s.id), ['a', 't', 'v'], 'neue Arten nicht zwischenspeichern');
eq(cachePayload({version: 'v', slides: [{id: 's', kind: 'stats_faults'}]}, {}, d).slides, [], 'nur neue Arten: leer');
eq(cachePayload(null, null, d), {version: '', saved_at: '2026-11-21T10:00:00.000Z', slides: [], images: {}}, 'leer');
eq(typeof cachePayload({version: 'v'}, {}).saved_at, 'string', 'saved_at ohne Datum');
const mb = (n) => 'A'.repeat(Math.ceil(n * 4 / 3));
eq(Object.keys(cachePayload({version: 'v'}, {ok: mb(5 * 1048576), big: mb(5 * 1048576 + 4)}, d).images), ['ok'], 'je Bild 5 MB');
const many = {}; for (let i = 0; i < 10; i++) many['i' + i] = mb(5 * 1048576);
eq(Object.keys(cachePayload({version: 'v'}, many, d).images).length, 10, 'genau 50 MB');
many.i10 = mb(1048576);
eq(cachePayload({version: 'v'}, many, d).images, {}, 'über 50 MB: ohne Bilder');
eq(cachePayload({version: 'v'}, {a: 5, b: ''}, d).images, {}, 'keine Texte');
let o = outageStep(0, true); eq(o, {fails: 1, leave: false}, 'erster Fehler');
o = outageStep(o.fails, true); eq(o, {fails: 2, leave: false}, 'zweiter Fehler');
o = outageStep(o.fails, true); eq(o, {fails: 3, leave: true}, 'dritter Fehler');
o = outageStep(o.fails, true); eq(o.leave, true, 'weitere Fehler');
eq(outageStep(2, false), {fails: 0, leave: false}, 'Antwort setzt zurück');
eq(outageStep(undefined, true), {fails: 1, leave: false}, 'ohne Zähler');
eq(outageStep(-5, true), {fails: 1, leave: false}, 'negativer Zähler');
eq(backTarget(true, 'http://127.0.0.1:8080'), 'http://127.0.0.1:8080/start', 'Zurück bei Netzfehler');
eq(backTarget(false, 'http://127.0.0.1:8080'), '/terminal', 'Zurück mit PDH');
// Lokaler Port des Geräts (data-local-port): nur 1024–65534 in Ziffern, sonst 8080
eq(localOrigin(null), 'http://127.0.0.1:8080', 'ohne Attribut');
eq(localOrigin(''), 'http://127.0.0.1:8080', 'leeres Attribut');
eq(localOrigin('18080'), 'http://127.0.0.1:18080', 'Port 18080');
eq(localOrigin('1024'), 'http://127.0.0.1:1024', 'untere Grenze');
eq(localOrigin('65534'), 'http://127.0.0.1:65534', 'obere Grenze');
eq(localOrigin('1023'), 'http://127.0.0.1:8080', 'zu klein');
eq(localOrigin('65535'), 'http://127.0.0.1:8080', 'zu groß');
eq(localOrigin('abc'), 'http://127.0.0.1:8080', 'Buchstaben');
eq(localOrigin(' 18080'), 'http://127.0.0.1:8080', 'Leerzeichen');
eq(localOrigin('18080/x@evil'), 'http://127.0.0.1:8080', 'Anhang');
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

// Wischen auf Touchscreens: ohne touch-action:none bricht Chromium den Zeigerstrom bei waagerechter Bewegung
// mit pointercancel ab, und die Wischgeste käme nie an.
func TestTerminalGlobalStageTouchAction(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/terminal.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\.g-stage\{[^}]*touch-action:none`).Match(b) {
		t.Error(".g-stage braucht touch-action:none")
	}
}

// Vorgang-Ansicht: der Zurück-Knopf bleibt ein Link (Rückfall ohne Skript), das Skript prüft vor dem Verlassen PDH.
func TestTerminalVorgangBackProbe(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/terminal_vorgang.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `class="v-back" href="/terminal"`) {
		t.Error("Zurück-Link fehlt")
	}
	js, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(js), "\r\n", "\n")
	i := strings.Index(s, "function leaveVorgang()")
	if i < 0 {
		t.Fatal("leaveVorgang fehlt")
	}
	blk := s[i:]
	blk = blk[:strings.Index(blk, "// Inaktivität")]
	for _, want := range []string{"fetch('/terminal/slides.json'", "go(true)", "'.v-back'", "preventDefault"} {
		if !strings.Contains(blk, want) {
			t.Errorf("leaveVorgang enthält %q nicht", want)
		}
	}
	if !strings.Contains(s, "if (mode === 'vorgang') leaveVorgang();") {
		t.Error("Ruhezeit in der Vorgang-Ansicht prüft PDH nicht")
	}
}
