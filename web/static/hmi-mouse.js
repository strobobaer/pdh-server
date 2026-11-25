/* PDH – Maus-Emulation für die HMI-Ansicht (noVNC) mit dem Finger.
 *
 * Gemeinsam genutzt vom Anlagenterminal (terminal.gohtml, Zweig hmiview) und vom Web-Viewer (hmi_viewer.gohtml,
 * Modus „Tastatur & Maus“). Geladen von /vendor/hmi-mouse.js (hmi.go), Ausgabe ist window.PdhHmiMouse
 * (unter Node module.exports, für die Tests in internal/web/hmi_mouse_test.go).
 *
 * Aufbau
 *   Reine Funktionen   nextKind (Zustandsautomat der Klick-Art), clientToRemote / remoteToClient (Koordinaten),
 *                      padMove (Beschleunigung), scrollDelta, isTap
 *   createEngine       Fingerbewegungen -> Maus-Ereignisse (ohne DOM, nur Rückruf emit)
 *   attach             Leiste, Zeiger, Pointer-Ereignisse und Weitergabe an noVNC
 *
 * noVNC 1.6.0 (web/static/vendor/novnc/rfb.min.js) bleibt unverändert. Seine öffentliche Schnittstelle (RFB) hat
 * keine Methode für Mausereignisse (nur sendKey, sendCtrlAltDel, focus u. a.); der Mauszeiger geht dort über die
 * private Methode _sendMouse, aufgerufen aus _handleMouse (Ereignisse mousedown, mouseup, mousemove auf dem Canvas)
 * und _handleWheel (wheel). Diese Handler lesen nur clientX, clientY, buttons, deltaX, deltaY, deltaMode und prüfen
 * nicht isTrusted. Deshalb wird hier der dokumentierte DOM-Weg benutzt: eigene MouseEvent und WheelEvent an das
 * Canvas. Die Umrechnung von Seiten- in Bildschirmkoordinaten samt Skalierung (scaleViewport) macht noVNC selbst
 * (Display.absX/absY), wir rechnen nur zwischen Seitenkoordinaten und dem Bild des HMI (clientToRemote).
 * Touch- und Stiftereignisse im Bereich werden abgefangen (Capture-Phase, noVNC sieht sie nicht); die Maus bleibt
 * unverändert.
 */
(function () {
  'use strict';

  var KINDS = ['left', 'right', 'double', 'drag', 'scroll'];
  var KIND_LABEL = { left: 'Links', right: 'Rechts', double: 'Doppel', drag: 'Ziehen', scroll: 'Scrollen' };
  var TAP_SLOP = 10;       // Bildschirmpunkte: mehr Bewegung ist kein Tipp
  var TAP_MS = 450;        // längere Berührung ist kein Tipp
  var HOLD_MS = 550;       // langes Drücken auf einen Knopf der Leiste hält die Art fest
  var SCROLL_GAIN = 1.4;   // Mausrad-Pixel je Fingerpixel

  // ── Zustandsautomat der Klick-Art ─────────────────────────────────────────────
  // state = {kind, sticky}. Ereignisse: {type: 'select', kind} (Antippen eines Knopfs: gilt für den nächsten Tipp),
  // {type: 'hold', kind} (langes Drücken: bleibt bestehen; nochmal langes Drücken auf dieselbe Art löst sie wieder),
  // {type: 'used'} (ein Tipp oder eine Geste wurde ausgeführt: zurück auf „Links“, außer festgehalten).
  function nextKind(state, ev) {
    var cur = state && KINDS.indexOf(state.kind) >= 0 ? { kind: state.kind, sticky: !!state.sticky } : { kind: 'left', sticky: false };
    if (!ev) return cur;
    if (ev.type === 'select' && KINDS.indexOf(ev.kind) >= 0) return { kind: ev.kind, sticky: false };
    if (ev.type === 'hold' && KINDS.indexOf(ev.kind) >= 0) {
      if (ev.kind === 'left' || (cur.sticky && cur.kind === ev.kind)) return { kind: 'left', sticky: false };
      return { kind: ev.kind, sticky: true };
    }
    if (ev.type === 'used') return cur.sticky ? cur : { kind: 'left', sticky: false };
    return cur;
  }

  // ── Koordinaten ───────────────────────────────────────────────────────────────
  // rect: Rechteck des Canvas auf der Seite (getBoundingClientRect), fbw/fbh: Größe des HMI-Bildes in Bildpunkten.
  // Bei scaleViewport ist das Rechteck kleiner oder größer als das Bild; die Umrechnung gilt für jede Skalierung.
  function clamp(v, lo, hi) { return v < lo ? lo : (v > hi ? hi : v); }
  function clientToRemote(cx, cy, rect, fbw, fbh) {
    if (!rect || !(rect.width > 0) || !(rect.height > 0) || !(fbw > 0) || !(fbh > 0)) return { x: 0, y: 0 };
    return {
      x: clamp(Math.floor((cx - rect.left) * fbw / rect.width), 0, fbw - 1),
      y: clamp(Math.floor((cy - rect.top) * fbh / rect.height), 0, fbh - 1)
    };
  }
  // Mitte des Bildpunkts rx/ry auf der Seite.
  function remoteToClient(rx, ry, rect, fbw, fbh) {
    if (!rect || !(fbw > 0) || !(fbh > 0)) return { x: 0, y: 0 };
    return { x: rect.left + (rx + 0.5) * rect.width / fbw, y: rect.top + (ry + 0.5) * rect.height / fbh };
  }
  // Seitenkoordinaten in das Rechteck zwingen (der Zeiger verlässt das Bild nicht).
  function clampToRect(x, y, rect) {
    return { x: clamp(x, rect.left, rect.left + rect.width - 1), y: clamp(y, rect.top, rect.top + rect.height - 1) };
  }

  // ── Touchpad: Beschleunigung ─────────────────────────────────────────────────
  // Verstärkung nach Geschwindigkeit (Punkte je Millisekunde): langsam leicht abgebremst für feine Bewegung,
  // schnell bis dreifach.
  function padGain(speed) {
    if (!(speed > 0)) return 0.8;
    if (speed < 0.2) return 0.8;
    return Math.min(3, 0.8 + (speed - 0.2) * 1.5);
  }
  function padMove(dx, dy, dt) {
    var dist = Math.sqrt(dx * dx + dy * dy);
    if (dist === 0) return { x: 0, y: 0 };
    var g = padGain(dist / Math.max(1, dt || 16));
    return { x: dx * g, y: dy * g };
  }

  // Fingerbewegung -> Mausrad (natürliche Richtung: Finger nach oben schiebt den Inhalt nach oben, das Rad dreht nach unten).
  function scrollDelta(dx, dy) {
    return { x: -dx * SCROLL_GAIN, y: -dy * SCROLL_GAIN };
  }

  function isTap(moved, dt) { return !moved && dt <= TAP_MS; }

  // ── Fingergesten -> Mausereignisse ──────────────────────────────────────────
  // cfg: mode() 'direct' | 'pad', kind() aktuelle Klick-Art, used() Rückruf nach ausgeführter Geste,
  // emit(ev) Ausgabe: {type:'move', x, y, buttons} | {type:'down'|'up', x, y, button, buttons} | {type:'wheel', x, y, dx, dy}
  // (Koordinaten wie die Eingabe: Seitenkoordinaten), clip(x, y) -> {x, y} hält den Zeiger im Bild (optional).
  // Zeiten (t) in Millisekunden vom Aufrufer. Im Modus 'pad' wirkt der Finger relativ auf engine.cursor.
  function createEngine(cfg) {
    var ptrs = {};       // id -> {x, y, t}
    var order = [];      // ids in der Reihenfolge des Aufsetzens
    var g = null;        // Geste eines Fingers
    var sc = null;       // Zwei-Finger-Scrollen
    var ignoreRest = false;
    var eng = { cursor: { x: 0, y: 0 } };
    var clip = cfg.clip || function (x, y) { return { x: x, y: y }; };

    function center() {
      var a = ptrs[order[0]], b = ptrs[order[1]];
      return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
    }
    function releaseHeld() {
      if (g && g.held) {
        cfg.emit({ type: 'up', x: g.at.x, y: g.at.y, button: 0, buttons: 0 });
        g.held = false;
      }
    }
    function remove(id) {
      delete ptrs[id];
      order = order.filter(function (k) { return k !== id; });
    }

    eng.down = function (id, x, y, t) {
      if (ptrs[id]) return;
      ptrs[id] = { x: x, y: y, t: t };
      order.push(id);
      if (order.length === 2) { // zweiter Finger: Scrollen, ein offener Tipp oder Zug endet
        releaseHeld();
        g = null;
        var c = center();
        sc = { x: c.x, y: c.y };
        return;
      }
      if (order.length > 2 || ignoreRest) return;
      var kind = cfg.kind();
      var at = cfg.mode() === 'pad' ? { x: eng.cursor.x, y: eng.cursor.y } : clip(x, y);
      g = { id: id, kind: kind, x0: x, y0: y, t0: t, lx: x, ly: y, lt: t, moved: false, held: false, at: at, ws: 0 };
      if (kind === 'drag') {
        if (cfg.mode() !== 'pad') eng.cursor = { x: at.x, y: at.y };
        cfg.emit({ type: 'move', x: at.x, y: at.y, buttons: 0 });
        cfg.emit({ type: 'down', x: at.x, y: at.y, button: 0, buttons: 1 });
        g.held = true;
      }
    };

    eng.move = function (id, x, y, t) {
      var p = ptrs[id];
      if (!p) return;
      var dx = x - p.x, dy = y - p.y, dt = t - p.t;
      p.x = x; p.y = y; p.t = t;
      if (sc) {
        if (order.length < 2) return;
        var c = center(), sd = scrollDelta(c.x - sc.x, c.y - sc.y);
        sc.x = c.x; sc.y = c.y;
        if (sd.x !== 0 || sd.y !== 0) {
          var wp = cfg.mode() === 'pad' ? eng.cursor : clip(c.x, c.y); // Touchpad: am Zeiger, sonst zwischen den Fingern
          cfg.emit({ type: 'wheel', x: wp.x, y: wp.y, dx: sd.x, dy: sd.y });
        }
        return;
      }
      if (!g || g.id !== id) return;
      var wasMoved = g.moved;
      if (!g.moved) {
        var ddx = x - g.x0, ddy = y - g.y0;
        if (g.kind === 'drag' || Math.sqrt(ddx * ddx + ddy * ddy) > TAP_SLOP) g.moved = true;
      }
      if (!g.moved) return;
      var pad = cfg.mode() === 'pad';
      if (g.kind === 'scroll') {
        var s = scrollDelta(dx, dy);
        var pos = pad ? eng.cursor : clip(x, y);
        cfg.emit({ type: 'wheel', x: pos.x, y: pos.y, dx: s.x, dy: s.y });
        return;
      }
      if (pad) {
        if (!wasMoved && g.kind !== 'drag') { dx = 0; dy = 0; } // die Wegstrecke bis zur Schwelle zählt nicht
        var m = padMove(dx, dy, dt);
        var np = clip(eng.cursor.x + m.x, eng.cursor.y + m.y);
        eng.cursor = { x: np.x, y: np.y };
        g.at = { x: np.x, y: np.y };
        cfg.emit({ type: 'move', x: np.x, y: np.y, buttons: g.held ? 1 : 0 });
      } else if (g.held) { // Ziehen mit dem Finger direkt auf dem Bild
        var cp = clip(x, y);
        eng.cursor = { x: cp.x, y: cp.y };
        g.at = { x: cp.x, y: cp.y };
        cfg.emit({ type: 'move', x: cp.x, y: cp.y, buttons: 1 });
      }
    };

    eng.up = function (id, x, y, t) {
      var p = ptrs[id];
      if (!p) return;
      remove(id);
      if (sc) { // Zwei-Finger-Scrollen verbraucht die Klick-Art nicht; der übrige Finger tut nichts mehr
        if (order.length < 2) { sc = null; ignoreRest = order.length > 0; }
        return;
      }
      if (!order.length) ignoreRest = false;
      if (!g || g.id !== id) return;
      var gg = g;
      g = null;
      if (gg.kind === 'drag') {
        cfg.emit({ type: 'up', x: gg.at.x, y: gg.at.y, button: 0, buttons: 0 });
        gg.held = false;
        cfg.used();
        return;
      }
      if (gg.kind === 'scroll') {
        if (gg.moved) cfg.used();
        return;
      }
      if (!isTap(gg.moved, t - gg.t0)) return;
      var at = gg.at;
      eng.cursor = { x: at.x, y: at.y };
      cfg.emit({ type: 'move', x: at.x, y: at.y, buttons: 0 });
      var clicks = gg.kind === 'double' ? 2 : 1, btn = gg.kind === 'right' ? 2 : 0, mask = gg.kind === 'right' ? 2 : 1;
      for (var i = 0; i < clicks; i++) {
        cfg.emit({ type: 'down', x: at.x, y: at.y, button: btn, buttons: mask });
        cfg.emit({ type: 'up', x: at.x, y: at.y, button: btn, buttons: 0 });
      }
      cfg.used();
    };

    eng.cancel = function (id) {
      if (!ptrs[id]) return;
      remove(id);
      if (sc) {
        if (order.length < 2) { sc = null; ignoreRest = order.length > 0; }
        return;
      }
      if (g && g.id === id) { releaseHeld(); g = null; }
      if (!order.length) ignoreRest = false;
    };

    eng.active = function () { return order.length > 0; };
    return eng;
  }

  // ── DOM: Leiste, Zeiger, Weitergabe an noVNC ──────────────────────────────────
  var STYLE_ID = 'hm-style';
  var CSS = [
    '.hm-bar{display:flex;flex-wrap:wrap;align-items:center;gap:6px;margin:0 0 8px;font:inherit;flex:none}',
    '.hm-btn{min-height:clamp(44px,6vh,64px);min-width:44px;padding:0 14px;border-radius:10px;border:2px solid var(--border,#475569);background:var(--bg2,#1e293b);color:var(--text,#e2e8f0);font:inherit;font-size:clamp(15px,1.6vw,26px);cursor:pointer;touch-action:manipulation;user-select:none;-webkit-user-select:none}',
    '.hm-btn:focus-visible{outline:3px solid var(--accent,#3b82f6);outline-offset:2px}',
    '.hm-btn[aria-pressed="true"]{border-color:var(--accent,#3b82f6);background:var(--accent,#3b82f6);color:#fff;font-weight:700}',
    '.hm-btn.hm-sticky{outline:3px solid var(--amber,#f59e0b);outline-offset:1px}',
    '.hm-sep{width:1px;align-self:stretch;background:var(--border,#475569);margin:0 4px}',
    '.hm-state{font-size:clamp(13px,1.3vw,22px);color:var(--muted,#94a3b8);margin-left:4px}',
    '.hm-cursor{position:fixed;left:0;top:0;width:26px;height:26px;pointer-events:none;z-index:20;filter:drop-shadow(0 1px 2px rgba(0,0,0,.6));display:none}',
    '.hm-cursor.on{display:block}'
  ].join('\n');

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    var st = document.createElement('style');
    st.id = STYLE_ID;
    st.textContent = CSS;
    document.head.appendChild(st);
  }

  function makeCursor() {
    var NS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 26 26');
    svg.setAttribute('class', 'hm-cursor');
    svg.setAttribute('aria-hidden', 'true');
    var p = document.createElementNS(NS, 'path');
    p.setAttribute('d', 'M2 2 L2 21 L7 16.5 L10.5 24 L14 22.5 L10.6 15.2 L17.5 15 Z');
    p.setAttribute('fill', '#ffffff');
    p.setAttribute('stroke', '#000000');
    p.setAttribute('stroke-width', '1.6');
    p.setAttribute('stroke-linejoin', 'round');
    svg.appendChild(p);
    return svg;
  }

  // attach(screen, rfb, opts): Leiste und Eingabeschicht für die HMI-Ansicht im Steuermodus. screen ist das Element,
  // in dem noVNC das Bild aufbaut; die Leiste wird davor eingefügt. rfb wird nur für focus() benutzt. Rückgabe:
  // {destroy(), state(), setMode(m)}. opts.mode: 'direct' (Standard) | 'pad'.
  function attach(screen, rfb, opts) {
    opts = opts || {};
    ensureStyle();
    var canvas = screen.querySelector('canvas');
    if (!canvas) return { destroy: function () {}, state: function () { return null; }, setMode: function () {} };
    var kindState = { kind: 'left', sticky: false };
    var padOn = opts.mode === 'pad';
    var remote = null; // letzte Zeigerstellung im Bild des HMI {x, y} (unabhängig von der Skalierung)
    var buttons = {};
    var holdTimer = null, holdFired = false;

    var bar = document.createElement('div');
    bar.className = 'hm-bar';
    bar.setAttribute('role', 'toolbar');
    bar.setAttribute('aria-label', 'Maus-Emulation');
    var stateEl = document.createElement('span');
    stateEl.className = 'hm-state';
    stateEl.setAttribute('aria-live', 'polite');
    var cursor = makeCursor();
    var prevTouchAction = screen.style.touchAction;

    function render() {
      KINDS.forEach(function (k) {
        var b = buttons[k];
        b.setAttribute('aria-pressed', kindState.kind === k ? 'true' : 'false');
        b.className = 'hm-btn' + (kindState.kind === k && kindState.sticky ? ' hm-sticky' : '');
      });
      buttons.pad.setAttribute('aria-pressed', padOn ? 'true' : 'false');
      stateEl.textContent = (kindState.sticky ? KIND_LABEL[kindState.kind] + ' (fest)' : (kindState.kind === 'left' ? '' : 'nächster Tipp: ' + KIND_LABEL[kindState.kind]));
      cursor.setAttribute('class', 'hm-cursor' + (padOn ? ' on' : ''));
      place();
    }
    function dims() {
      var r = canvas.getBoundingClientRect();
      var w = canvas.width > 0 ? canvas.width : Math.max(1, Math.round(r.width));
      var h = canvas.height > 0 ? canvas.height : Math.max(1, Math.round(r.height));
      return { rect: r, w: w, h: h };
    }
    // Zeigerstellung auf dem Bildschirm aus der gespeicherten Stellung im HMI-Bild
    function pos() {
      var d = dims();
      if (!remote) remote = { x: Math.floor(d.w / 2), y: Math.floor(d.h / 2) };
      return remoteToClient(remote.x, remote.y, d.rect, d.w, d.h);
    }
    function place() {
      if (!padOn) return;
      var c = pos();
      cursor.style.transform = 'translate(' + Math.round(c.x - 2) + 'px,' + Math.round(c.y - 2) + 'px)'; // Spitze des Pfeils bei (2, 2)
    }
    function clip(x, y) {
      var d = dims();
      return d.rect.width > 0 ? clampToRect(x, y, d.rect) : { x: x, y: y };
    }

    // an noVNC: eigene DOM-Ereignisse am Canvas (siehe Kopfkommentar)
    function fire(type, x, y, button, mask) {
      var ev = new MouseEvent(type, { bubbles: true, cancelable: true, view: window, clientX: x, clientY: y, screenX: x, screenY: y, button: button, buttons: mask });
      // noVNC fängt die Maus bei mousedown auf Fenster-Ebene ein; das Loslassen muss dort ankommen, sonst bleibt die
      // Einfangfläche (noVNC_mouse_capture_elem) über der Seite liegen.
      if (type === 'mouseup' && document.captureElement) window.dispatchEvent(ev);
      else canvas.dispatchEvent(ev);
    }
    function send(o) {
      if (o.type === 'move') fire('mousemove', o.x, o.y, 0, o.buttons);
      else if (o.type === 'down') fire('mousedown', o.x, o.y, o.button, o.buttons);
      else if (o.type === 'up') fire('mouseup', o.x, o.y, o.button, o.buttons);
      else if (o.type === 'wheel') {
        canvas.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, view: window, clientX: o.x, clientY: o.y, deltaX: o.dx, deltaY: o.dy, deltaMode: 0 }));
      }
      if (o.type !== 'wheel') {
        var d = dims();
        remote = clientToRemote(o.x, o.y, d.rect, d.w, d.h);
        place();
      }
    }

    var engine = createEngine({
      mode: function () { return padOn ? 'pad' : 'direct'; },
      kind: function () { return kindState.kind; },
      used: function () { kindState = nextKind(kindState, { type: 'used' }); render(); },
      emit: send,
      clip: clip
    });

    function syncCursor() { var c = pos(); engine.cursor = { x: c.x, y: c.y }; }

    // Pointer-Ereignisse von Finger und Stift (die Maus bleibt unberührt)
    function isTouch(ev) { return ev.pointerType === 'touch' || ev.pointerType === 'pen'; }
    function swallow(ev) { ev.preventDefault(); ev.stopPropagation(); }
    function onDown(ev) {
      if (!isTouch(ev)) return;
      swallow(ev);
      try { screen.setPointerCapture(ev.pointerId); } catch (e) { /* ältere Browser */ }
      if (rfb && typeof rfb.focus === 'function') { try { rfb.focus(); } catch (e) { /* ignorieren */ } }
      syncCursor();
      engine.down(ev.pointerId, ev.clientX, ev.clientY, ev.timeStamp);
    }
    function onMove(ev) { if (!isTouch(ev)) return; swallow(ev); engine.move(ev.pointerId, ev.clientX, ev.clientY, ev.timeStamp); }
    function onUp(ev) { if (!isTouch(ev)) return; swallow(ev); engine.up(ev.pointerId, ev.clientX, ev.clientY, ev.timeStamp); }
    function onCancel(ev) { if (!isTouch(ev)) return; swallow(ev); engine.cancel(ev.pointerId); }
    function onTouch(ev) { swallow(ev); } // noVNC sieht keine Touch-Ereignisse; auch keine nachgemachten Mausklicks
    var opt = { capture: true, passive: false };
    var handlers = [['pointerdown', onDown], ['pointermove', onMove], ['pointerup', onUp], ['pointercancel', onCancel],
      ['touchstart', onTouch], ['touchmove', onTouch], ['touchend', onTouch], ['touchcancel', onTouch]];
    handlers.forEach(function (h) { screen.addEventListener(h[0], h[1], opt); });
    screen.style.touchAction = 'none';
    window.addEventListener('resize', place);
    var ro = typeof ResizeObserver === 'function' ? new ResizeObserver(place) : null; // Anpassen/Originalgröße ändert das Canvas ohne resize
    if (ro) ro.observe(canvas);

    function addButton(key, label, title) {
      var b = document.createElement('button');
      b.type = 'button';
      b.textContent = label;
      b.title = title;
      b.className = 'hm-btn';
      b.setAttribute('aria-pressed', 'false');
      buttons[key] = b;
      bar.appendChild(b);
      return b;
    }
    KINDS.forEach(function (k) {
      var b = addButton(k, KIND_LABEL[k], KIND_LABEL[k] + ': gilt für den nächsten Tipp; lang drücken hält die Art fest');
      var stop = function () { if (holdTimer) { clearTimeout(holdTimer); holdTimer = null; } };
      b.addEventListener('pointerdown', function () {
        holdFired = false;
        stop();
        holdTimer = setTimeout(function () {
          holdTimer = null;
          holdFired = true;
          kindState = nextKind(kindState, { type: 'hold', kind: k });
          if (navigator.vibrate) { try { navigator.vibrate(30); } catch (e) { /* ignorieren */ } }
          render();
        }, HOLD_MS);
      });
      b.addEventListener('pointerup', stop);
      b.addEventListener('pointerleave', stop);
      b.addEventListener('pointercancel', stop);
      b.addEventListener('contextmenu', function (ev) { ev.preventDefault(); });
      b.addEventListener('click', function () {
        if (holdFired) { holdFired = false; return; } // das lange Drücken hat schon gewirkt
        kindState = nextKind(kindState, { type: 'select', kind: k });
        render();
      });
    });
    var sep = document.createElement('span');
    sep.className = 'hm-sep';
    bar.appendChild(sep);
    var padBtn = addButton('pad', 'Touchpad', 'Touchpad-Modus: Finger bewegt den Zeiger wie auf einem Laptop');
    padBtn.addEventListener('click', function () { padOn = !padOn; if (padOn) syncCursor(); render(); });
    bar.appendChild(stateEl);
    screen.parentNode.insertBefore(bar, screen);
    screen.appendChild(cursor);
    render();

    return {
      state: function () { return { kind: kindState.kind, sticky: kindState.sticky, pad: padOn }; },
      setMode: function (m) { padOn = m === 'pad'; render(); },
      destroy: function () {
        handlers.forEach(function (h) { screen.removeEventListener(h[0], h[1], opt); });
        window.removeEventListener('resize', place);
        if (ro) ro.disconnect();
        if (holdTimer) clearTimeout(holdTimer);
        screen.style.touchAction = prevTouchAction;
        if (bar.parentNode) bar.parentNode.removeChild(bar);
        if (cursor.parentNode) cursor.parentNode.removeChild(cursor);
      }
    };
  }

  var api = {
    KINDS: KINDS, TAP_SLOP: TAP_SLOP, TAP_MS: TAP_MS, HOLD_MS: HOLD_MS,
    nextKind: nextKind, clientToRemote: clientToRemote, remoteToClient: remoteToClient, clampToRect: clampToRect,
    padGain: padGain, padMove: padMove, scrollDelta: scrollDelta, isTap: isTap, createEngine: createEngine, attach: attach
  };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else window.PdhHmiMouse = api;
})();
