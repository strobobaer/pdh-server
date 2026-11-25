package web

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Maus-Emulation in der HMI-Ansicht (web/static/hmi-mouse.js): reine Funktionen und Fingergesten unter Node.
func TestHMIMouseLogic(t *testing.T) {
	file, err := filepath.Abs("../../web/static/hmi-mouse.js")
	if err != nil {
		t.Fatal(err)
	}
	js := `
const M = require(` + jsString(filepath.ToSlash(file)) + `);
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };

// ── Zustandsautomat der Klick-Art
let s = { kind: 'left', sticky: false };
eq(M.nextKind(s, { type: 'select', kind: 'right' }), { kind: 'right', sticky: false }, 'Antippen wählt Rechts');
eq(M.nextKind({ kind: 'right', sticky: false }, { type: 'used' }), { kind: 'left', sticky: false }, 'nach dem Tipp zurück auf Links');
eq(M.nextKind(s, { type: 'hold', kind: 'double' }), { kind: 'double', sticky: true }, 'langes Drücken hält fest');
eq(M.nextKind({ kind: 'double', sticky: true }, { type: 'used' }), { kind: 'double', sticky: true }, 'festgehaltene Art bleibt nach dem Tipp');
eq(M.nextKind({ kind: 'double', sticky: true }, { type: 'hold', kind: 'double' }), { kind: 'left', sticky: false }, 'nochmal langes Drücken löst');
eq(M.nextKind({ kind: 'double', sticky: true }, { type: 'select', kind: 'drag' }), { kind: 'drag', sticky: false }, 'Antippen ersetzt die feste Art');
eq(M.nextKind({ kind: 'double', sticky: true }, { type: 'hold', kind: 'drag' }), { kind: 'drag', sticky: true }, 'andere Art festhalten');
eq(M.nextKind(s, { type: 'hold', kind: 'left' }), { kind: 'left', sticky: false }, 'Links festhalten gibt es nicht');
eq(M.nextKind(s, { type: 'select', kind: 'unbekannt' }), { kind: 'left', sticky: false }, 'unbekannte Art ignoriert');
eq(M.nextKind(undefined, { type: 'used' }), { kind: 'left', sticky: false }, 'ohne Zustand');
eq(M.nextKind({ kind: 'x', sticky: true }, null), { kind: 'left', sticky: false }, 'ungültiger Zustand');
eq(M.KINDS, ['left', 'right', 'double', 'drag', 'scroll'], 'Arten');

// ── Koordinaten (Skalierung scaleViewport)
const rect = { left: 100, top: 50, width: 640, height: 360 };      // Anzeige halb so groß wie das Bild
eq(M.clientToRemote(100, 50, rect, 1280, 720), { x: 0, y: 0 }, 'links oben');
eq(M.clientToRemote(420, 230, rect, 1280, 720), { x: 640, y: 360 }, 'Mitte, doppelt skaliert');
eq(M.clientToRemote(739, 409, rect, 1280, 720), { x: 1278, y: 718 }, 'rechts unten innen');
eq(M.clientToRemote(5000, 5000, rect, 1280, 720), { x: 1279, y: 719 }, 'außerhalb wird begrenzt');
eq(M.clientToRemote(-5, -5, rect, 1280, 720), { x: 0, y: 0 }, 'links oben außerhalb');
eq(M.clientToRemote(300, 300, { left: 0, top: 0, width: 1280, height: 720 }, 1280, 720), { x: 300, y: 300 }, 'Originalgröße 1:1');
eq(M.clientToRemote(10, 10, { left: 0, top: 0, width: 0, height: 0 }, 1280, 720), { x: 0, y: 0 }, 'leeres Rechteck');
eq(M.clientToRemote(10, 10, rect, 0, 0), { x: 0, y: 0 }, 'ohne Bildgröße');
// Hin und zurück ergibt denselben Bildpunkt, bei jeder Skalierung
for (const r of [rect, { left: 0, top: 0, width: 1280, height: 720 }, { left: 7, top: 3, width: 1920, height: 1080 }, { left: 0, top: 0, width: 321, height: 181 }]) {
  for (const [rx, ry] of [[0, 0], [1, 1], [640, 360], [1279, 719], [123, 456]]) {
    const c = M.remoteToClient(rx, ry, r, 1280, 720);
    eq(M.clientToRemote(c.x, c.y, r, 1280, 720), { x: rx, y: ry }, 'Hin und zurück ' + rx + ',' + ry + ' bei ' + r.width);
  }
}
eq(M.clampToRect(-5, 9999, rect), { x: 100, y: 409 }, 'Zeiger bleibt im Bild');

// ── Beschleunigung
eq(M.padGain(0) < 1 && M.padGain(0.1) < 1, true, 'langsam fein');
eq(M.padGain(0.2) <= M.padGain(0.5) && M.padGain(0.5) <= M.padGain(1) && M.padGain(1) <= M.padGain(5), true, 'wächst mit der Geschwindigkeit');
eq(M.padGain(1000), 3, 'höchstens dreifach');
eq(M.padGain(-1), 0.8, 'ungültig');
eq(M.padMove(0, 0, 16), { x: 0, y: 0 }, 'kein Weg');
const slow = M.padMove(3, 4, 100), fast = M.padMove(30, 40, 16);
eq(Math.hypot(slow.x, slow.y) < 5, true, 'langsam kürzer als der Finger');
eq(Math.hypot(fast.x, fast.y) > 50, true, 'schnell länger als der Finger');
eq(fast.x / fast.y, 0.75, 'Richtung bleibt');

// ── Scrollen
eq(M.scrollDelta(0, -10).y > 0, true, 'Finger nach oben dreht das Rad nach unten');
eq(M.scrollDelta(10, 0).x < 0, true, 'Finger nach rechts dreht das Rad nach links');
eq(M.isTap(false, 100), true, 'kurzer Tipp');
eq(M.isTap(true, 100), false, 'bewegt ist kein Tipp');
eq(M.isTap(false, 2000), false, 'zu lang ist kein Tipp');

// ── Fingergesten
function rig(mode, kind) {
  const r = { mode, kind, evs: [], used: 0 };
  r.eng = M.createEngine({ mode: () => r.mode, kind: () => r.kind, used: () => { r.used++; }, emit: (e) => r.evs.push(e) });
  return r;
}
const sig = (evs) => evs.map(e => e.type + (e.button !== undefined ? ':' + e.button : '') + (e.buttons !== undefined ? '/' + e.buttons : '')).join(' ');

// direkt: Links-Tipp
let r = rig('direct', 'left');
r.eng.down(1, 200, 100, 0); r.eng.up(1, 202, 101, 120);
eq(sig(r.evs), 'move/0 down:0/1 up:0/0', 'Links-Tipp');
eq([r.evs[1].x, r.evs[1].y], [200, 100], 'Links-Tipp an der Berührstelle');
eq(r.used, 1, 'Tipp verbraucht die Art');
// Rechts
r = rig('direct', 'right');
r.eng.down(1, 50, 60, 0); r.eng.up(1, 50, 60, 100);
eq(sig(r.evs), 'move/0 down:2/2 up:2/0', 'Rechts-Tipp');
// Doppel
r = rig('direct', 'double');
r.eng.down(1, 50, 60, 0); r.eng.up(1, 50, 60, 100);
eq(sig(r.evs), 'move/0 down:0/1 up:0/0 down:0/1 up:0/0', 'Doppel-Tipp');
// zu lang oder bewegt: kein Klick, Art bleibt
r = rig('direct', 'left');
r.eng.down(1, 50, 60, 0); r.eng.up(1, 50, 60, 900);
eq(r.evs.length + '/' + r.used, '0/0', 'langes Drücken ist kein Tipp');
r = rig('direct', 'left');
r.eng.down(1, 50, 60, 0); r.eng.move(1, 90, 60, 50); r.eng.up(1, 90, 60, 100);
eq(r.evs.length + '/' + r.used, '0/0', 'Wischen ist kein Tipp');
// Ziehen: Taste halten, mit dem Finger ziehen
r = rig('direct', 'drag');
r.eng.down(1, 10, 10, 0); r.eng.move(1, 40, 10, 30); r.eng.move(1, 80, 30, 60); r.eng.up(1, 80, 30, 90);
eq(sig(r.evs), 'move/0 down:0/1 move/1 move/1 up:0/0', 'Ziehen');
eq([r.evs[3].x, r.evs[3].y], [80, 30], 'Ziehen folgt dem Finger');
eq(r.used, 1, 'Ziehen verbraucht die Art');
// Ziehen abgebrochen: Taste wird losgelassen
r = rig('direct', 'drag');
r.eng.down(1, 10, 10, 0); r.eng.cancel(1);
eq(sig(r.evs), 'move/0 down:0/1 up:0/0', 'Abbruch lässt die Taste los');
// Scrollen mit einem Finger in der Art Scrollen
r = rig('direct', 'scroll');
r.eng.down(1, 100, 200, 0); r.eng.move(1, 100, 150, 40); r.eng.move(1, 100, 100, 80); r.eng.up(1, 100, 100, 100);
eq(r.evs.map(e => e.type).join(' '), 'wheel wheel', 'Scrollen: nur Rad');
eq(r.evs[0].dy > 0, true, 'Finger nach oben: Rad nach unten');
eq(r.used, 1, 'Scrollen verbraucht die Art');
r = rig('direct', 'scroll');
r.eng.down(1, 100, 200, 0); r.eng.up(1, 100, 200, 100);
eq(r.evs.length + '/' + r.used, '0/0', 'Tippen in der Art Scrollen tut nichts');
// zwei Finger: Scrollen in jeder Art, Art bleibt
r = rig('direct', 'right');
r.eng.down(1, 100, 200, 0); r.eng.down(2, 140, 200, 5);
r.eng.move(1, 100, 180, 30); r.eng.move(2, 140, 180, 30); r.eng.move(1, 100, 150, 60); r.eng.move(2, 140, 150, 60);
r.eng.up(2, 140, 150, 80); r.eng.up(1, 100, 150, 90);
eq(r.evs.every(e => e.type === 'wheel') && r.evs.length > 0, true, 'zwei Finger scrollen nur');
eq(r.evs[0].x, 120, 'Rad in der Mitte der Finger');
eq(r.used, 0, 'Zwei-Finger-Scrollen verbraucht die Art nicht');
// zweiter Finger beendet einen laufenden Zug und lässt die Taste los
r = rig('direct', 'drag');
r.eng.down(1, 10, 10, 0); r.eng.down(2, 50, 10, 5);
eq(sig(r.evs), 'move/0 down:0/1 up:0/0', 'zweiter Finger löst die Taste');
// der übrig bleibende Finger klickt nicht
r.evs.length = 0; r.eng.up(2, 50, 10, 50); r.eng.up(1, 10, 10, 60);
eq(r.evs.length, 0, 'übriger Finger nach dem Scrollen tut nichts');

// Touchpad: relative Bewegung, Tipp klickt am Zeiger
r = rig('pad', 'left');
r.eng.cursor = { x: 300, y: 200 };
r.eng.down(1, 50, 50, 0); r.eng.move(1, 70, 50, 20); r.eng.move(1, 90, 50, 40); r.eng.up(1, 90, 50, 60);
eq(r.evs.every(e => e.type === 'move' && e.buttons === 0), true, 'Touchpad bewegt nur');
eq(r.evs[r.evs.length - 1].x > 300 && r.evs[r.evs.length - 1].y === 200, true, 'Zeiger wandert nach rechts');
eq(r.used, 0, 'Wischen verbraucht die Art nicht');
r = rig('pad', 'right');
r.eng.cursor = { x: 300, y: 200 };
r.eng.down(1, 50, 50, 0); r.eng.up(1, 51, 50, 100);
eq(sig(r.evs), 'move/0 down:2/2 up:2/0', 'Touchpad-Tipp klickt (Rechts)');
eq([r.evs[1].x, r.evs[1].y], [300, 200], 'Klick am Zeiger, nicht am Finger');
// Touchpad-Ziehen: Taste an der Zeigerstelle halten, Finger bewegt den Zeiger
r = rig('pad', 'drag');
r.eng.cursor = { x: 300, y: 200 };
r.eng.down(1, 50, 50, 0); r.eng.move(1, 80, 50, 30); r.eng.up(1, 80, 50, 60);
eq(sig(r.evs), 'move/0 down:0/1 move/1 up:0/0', 'Touchpad-Ziehen');
eq([r.evs[1].x, r.evs[1].y], [300, 200], 'Taste an der Zeigerstelle');
eq(r.evs[2].x > 300, true, 'Zug bewegt den Zeiger');
// Touchpad: zwei Finger scrollen am Zeiger
r = rig('pad', 'left');
r.eng.cursor = { x: 300, y: 200 };
r.eng.down(1, 50, 50, 0); r.eng.down(2, 90, 50, 5); r.eng.move(1, 50, 20, 30); r.eng.move(2, 90, 20, 30);
eq(r.evs.length > 0 && r.evs.every(e => e.type === 'wheel' && e.x === 300 && e.y === 200), true, 'Touchpad: Rad am Zeiger');
// Zeiger bleibt im Bild (clip)
r = { evs: [] };
r.eng = M.createEngine({ mode: () => 'pad', kind: () => 'left', used() {}, emit: (e) => r.evs.push(e), clip: (x, y) => ({ x: Math.min(Math.max(x, 0), 400), y: Math.min(Math.max(y, 0), 300) }) });
r.eng.cursor = { x: 390, y: 10 };
r.eng.down(1, 0, 0, 0); r.eng.move(1, 100, -50, 16); r.eng.move(1, 300, -300, 32); r.eng.up(1, 300, -300, 48);
eq(r.evs.every(e => e.x <= 400 && e.y >= 0), true, 'Zeiger verlässt das Bild nicht');
eq(r.eng.active(), false, 'alle Finger oben');
if (out.length) console.log(out.join('\n'));
`
	runNode(t, js)
}

func jsString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `\'`) + "'"
}

// Die Leiste gehört nur in den Steuermodus: Das Skript ist eingebunden, und die Seiten rufen attach nur dort auf.
func TestHMIMouseWiring(t *testing.T) {
	term := readStatic(t, "../../web/templates/terminal.gohtml")
	i := strings.Index(term, `{{else if eq .Mode "hmiview"}}`)
	j := strings.Index(term[i:], `{{else if eq .Mode "schichtplan"}}`)
	if i < 0 || j < 0 {
		t.Fatal("Zweig hmiview nicht gefunden")
	}
	hv := term[i : i+j]
	const guarded = `if (MODE === 'control' && window.PdhHmiMouse) mouse = PdhHmiMouse.attach(screen, rfb);`
	for _, want := range []string{`/vendor/hmi-mouse.js?v={{appVersion}}`, guarded} {
		if !strings.Contains(hv, want) {
			t.Errorf("terminal.gohtml hmiview: %q fehlt", want)
		}
	}
	if strings.Count(hv, "PdhHmiMouse.attach(") != 1 {
		t.Error("terminal.gohtml hmiview: attach nur einmal und nur im Steuermodus")
	}
	viewer := readStatic(t, "../../web/templates/hmi_viewer.gohtml")
	for _, want := range []string{`/vendor/hmi-mouse.js?v={{appVersion}}`, guarded} {
		if !strings.Contains(viewer, want) {
			t.Errorf("hmi_viewer.gohtml: %q fehlt", want)
		}
	}
	if strings.Count(viewer, "PdhHmiMouse.attach(") != 1 {
		t.Error("hmi_viewer.gohtml: attach nur einmal und nur im Steuermodus")
	}
	// die gleiche Datei für beide Seiten, eingebettet und ausgeliefert wie noVNC
	if !strings.Contains(readStatic(t, "../../internal/web/hmi.go"), `r.Get("/vendor/hmi-mouse.js", h.HMIMouseScript)`) {
		t.Error("Route /vendor/hmi-mouse.js fehlt")
	}
}

// Die HMI-Seite am Terminal bindet das Skript nur im Steuermodus ein; „Nur ansehen“ bleibt wie bisher.
func TestTerminalHMIViewMouseScriptOnlyInControl(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(mode string) string {
		rec := httptest.NewRecorder()
		h.renderTerminal(rec, terminalPage{Mode: "hmiview", Idle: 45, HMI: terminalHMIPage{
			Sel: terminalHMIItem{ID: "22222222-2222-2222-2222-222222222222", InfraID: "33333333-3333-3333-3333-333333333333", Name: "Panel", Control: true}, Mode: mode}})
		return rec.Body.String()
	}
	ctl, view := render("control"), render("view")
	if !strings.Contains(ctl, `<script src="/vendor/hmi-mouse.js?v=`) || !strings.Contains(ctl, "PdhHmiMouse.attach(screen, rfb)") {
		t.Error("Steuermodus: Skript oder attach fehlt")
	}
	if strings.Contains(view, `/vendor/hmi-mouse.js`) {
		t.Error("Nur ansehen: das Skript darf nicht eingebunden sein")
	}
	if !strings.Contains(view, `rfb.viewOnly = MODE !== 'control'`) {
		t.Error("Nur ansehen bleibt schreibgeschützt")
	}
}
