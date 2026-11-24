// Anlagenterminal (terminal_page.go): Uhr, Rückkehr zum Schoner, Aktualisierung der Störungsinfo.
(function () {
  'use strict';
  var body = document.body;
  var mode = body.dataset.mode;
  // Folienschau als Bildschirmschoner (normales Terminal): derselbe Player wie beim globalen Terminal, aber
  // Berührung außerhalb eines Vorgangs und jede Karte wecken das Terminal; kein Zwischenspeicher.
  var saver = mode === 'schoner' && body.dataset.saverSlides === '1' && !!document.getElementById('g-stage');

  // Anmeldung ohne Kartenleser – reine Funktionen (keine DOM-/Netzzugriffe), unabhängig lesbar.
  // readerState: 'ok' | 'none' | 'error'; settings: {manual_login, password_button, idle}.
  // Rückgabe: 'card' (Leser ok: nur Karte) | 'card_pin' | 'password' | 'card_only' | 'none'.
  function loginView(readerState, settings) {
    if (readerState === 'none' || readerState === 'error') {
      var m = settings && settings.manual_login;
      return (m === 'card_pin' || m === 'password' || m === 'card_only') ? m : 'none';
    }
    return 'card';
  }
  // Knopf „Mit Benutzer und Passwort“: nur wenn eingeschaltet und die Anmeldung nicht ganz aus ist.
  function showPasswordButton(readerState, settings) {
    return !!(settings && settings.password_button && settings.manual_login !== 'none');
  }

  // Ziffernfeld der Anmeldung: eingeklappt (nur Kartenfeld) bis das Kartenfeld angetippt wird. Zustand true = ausgeklappt.
  // Ereignisse: 'open' (Kartenfeld angetippt), 'ok' (OK gedrückt), 'cancel' (Schließen), 'login' (angemeldet),
  // 'idle' (Rückkehr zum Schoner). Alles außer 'open' klappt zu; Unbekanntes lässt den Zustand.
  function keypadState(open, event) {
    if (event === 'open') return true;
    if (event === 'ok' || event === 'cancel' || event === 'login' || event === 'idle') return false;
    return !!open;
  }

  // Kreisdiagramm der Rüstgründe (Statistikfolie und Seite „Rüstzeiten – Auswertung“; derselbe Text steht in
  // setup_report.gohtml). Geometrie rein; Zeichnen nur über DOM-Methoden und textContent, nie HTML aus Daten.
  // slices: [{label, value, percent, color}]. Rückgabe je Stück mit value > 0: {d: Pfad, x, y: Ort der Prozentangabe, slice}.
  function pieGeometry(slices, cx, cy, r) {
    var out = [], total = 0;
    var list = Array.isArray(slices) ? slices : [];
    list.forEach(function (s) { var v = Number(s && s.value); if (v > 0) total += v; });
    if (!(total > 0)) return out;
    var rnd = function (n) { return Math.round(n * 10) / 10; };
    var pt = function (a, rr) { return rnd(cx + rr * Math.cos(a)) + ' ' + rnd(cy + rr * Math.sin(a)); };
    var a0 = -Math.PI / 2;
    list.forEach(function (s) {
      var v = Number(s && s.value);
      if (!(v > 0)) return;
      var a1 = a0 + 2 * Math.PI * v / total, mid = (a0 + a1) / 2, d;
      if (v >= total) {
        d = 'M ' + (cx - r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 1 1 ' + (cx + r) + ' ' + cy + ' A ' + r + ' ' + r + ' 0 1 1 ' + (cx - r) + ' ' + cy + ' Z';
      } else {
        d = 'M ' + cx + ' ' + cy + ' L ' + pt(a0, r) + ' A ' + r + ' ' + r + ' 0 ' + (a1 - a0 > Math.PI ? 1 : 0) + ' 1 ' + pt(a1, r) + ' Z';
      }
      out.push({ d: d, x: rnd(cx + r * 0.62 * Math.cos(mid)), y: rnd(cy + r * 0.62 * Math.sin(mid)), slice: s });
      a0 = a1;
    });
    return out;
  }
  // Text für den barrierefreien Titel: „Grund A 60 %, Grund B 40 %“.
  function pieText(pie) {
    var sl = pie && Array.isArray(pie.slices) ? pie.slices : [];
    return sl.map(function (s) { return String(s.label) + ' ' + Number(s.percent) + ' %'; }).join(', ');
  }
  // Farbe nur als Hexwert, sonst Grau.
  function pieColor(c) {
    return typeof c === 'string' && /^#[0-9a-fA-F]{6}$/.test(c) ? c : '#94a3b8';
  }
  var pieSeq = 0;
  // Ein Kreisdiagramm samt Legende als DOM-Baum (div.pie-box).
  function drawPie(pie) {
    var NS = 'http://www.w3.org/2000/svg', n = ++pieSeq;
    var box = document.createElement('div');
    box.className = 'pie-box';
    var geo = pieGeometry(pie && pie.slices, 150, 150, 140);
    if (!geo.length) {
      var none = document.createElement('div');
      none.className = 'pie-none';
      none.textContent = 'Keine abgeschlossenen Rüstzeiten';
      box.appendChild(none);
      return box;
    }
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 300 300');
    svg.setAttribute('class', 'pie-svg');
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-labelledby', 'pie-t' + n + ' pie-d' + n);
    var t = document.createElementNS(NS, 'title');
    t.setAttribute('id', 'pie-t' + n);
    t.textContent = 'Anteil der Rüstgründe – ' + String((pie && pie.title) || '');
    var ds = document.createElementNS(NS, 'desc');
    ds.setAttribute('id', 'pie-d' + n);
    ds.textContent = pieText(pie);
    svg.appendChild(t);
    svg.appendChild(ds);
    geo.forEach(function (g) {
      var p = document.createElementNS(NS, 'path');
      p.setAttribute('d', g.d);
      p.setAttribute('fill', pieColor(g.slice.color));
      p.setAttribute('class', 'pie-slice');
      svg.appendChild(p);
    });
    geo.forEach(function (g) {
      if (!(Number(g.slice.percent) >= 4)) return; // kleine Stücke nur in der Legende
      var tx = document.createElementNS(NS, 'text');
      tx.setAttribute('x', g.x);
      tx.setAttribute('y', g.y + 7);
      tx.setAttribute('class', 'pie-pct');
      tx.textContent = Number(g.slice.percent) + ' %';
      svg.appendChild(tx);
    });
    box.appendChild(svg);
    var ul = document.createElement('ul');
    ul.className = 'pie-legend';
    geo.forEach(function (g) {
      var li = document.createElement('li');
      var sw = document.createElement('span');
      sw.className = 'sw';
      sw.style.backgroundColor = pieColor(g.slice.color);
      li.appendChild(sw);
      li.appendChild(document.createTextNode(String(g.slice.label) + ' – ' + Number(g.slice.percent) + ' %'));
      ul.appendChild(li);
    });
    box.appendChild(ul);
    return box;
  }
  // Auswahl „Alle Anlagen“ / je Anlage und das gewählte Kreisdiagramm (div.pie-set); ohne Diagramme leer.
  function drawPieSet(pies) {
    var set = document.createElement('div');
    set.className = 'pie-set';
    var list = Array.isArray(pies) ? pies : [];
    if (!list.length) return set;
    var sel = document.createElement('select');
    sel.className = 'pie-sel';
    sel.setAttribute('aria-label', 'Anlage für das Kreisdiagramm');
    list.forEach(function (p, i) {
      var o = document.createElement('option');
      o.value = String(i);
      o.textContent = String(p.title || '');
      sel.appendChild(o);
    });
    var holder = document.createElement('div');
    holder.className = 'pie-holder';
    var show = function () {
      while (holder.firstChild) holder.removeChild(holder.firstChild);
      holder.appendChild(drawPie(list[Number(sel.value)] || list[0]));
    };
    sel.addEventListener('change', show);
    set.appendChild(sel);
    set.appendChild(holder);
    show();
    return set;
  }

  // Folienschau des Infobildschirms (Modus global) – reine Funktionen.
  // Nächste Folie (reihum); ohne Folien 0.
  function nextSlideIndex(slides, i) {
    var n = Array.isArray(slides) ? slides.length : 0;
    if (n < 1) return 0;
    if (typeof i !== 'number' || i < 0 || i >= n - 1) return 0;
    return i + 1;
  }
  // Anzeigedauer einer Folie in Millisekunden: 5 bis 600 s, sonst 15 s.
  function slideDurationMs(slide) {
    var s = slide && typeof slide.duration === 'number' ? slide.duration : 15;
    if (!(s >= 5 && s <= 600)) s = 15;
    return Math.round(s * 1000);
  }
  // Adresse einer Website-Folie für den Rahmen: nur http(s), nie der eigene Ursprung (das PDH im Rahmen wäre nicht
  // abgeschottet). Sonst ''.
  function websiteFrameUrl(url, ownOrigin) {
    if (typeof url !== 'string' || url.length > 2048 || !/^https?:\/\//i.test(url)) return '';
    var u;
    try { u = new URL(url); } catch (e) { return ''; }
    if (u.username || u.password || (u.protocol !== 'http:' && u.protocol !== 'https:')) return '';
    if (ownOrigin && u.origin === ownOrigin) return '';
    return u.href;
  }
  // Erste zeigbare Folie ab Index i (reihum). Website- und Planungsfolien (plan_*) entfallen ohne Verbindung
  // (offline); -1 = keine.
  function showableIndex(slides, i, offline) {
    var n = Array.isArray(slides) ? slides.length : 0;
    if (n < 1) return -1;
    var start = typeof i === 'number' && i >= 0 && i < n ? i : 0;
    for (var k = 0; k < n; k++) {
      var j = (start + k) % n;
      if (!(offline && slides[j] && (slides[j].kind === 'website' || /^(plan_.*|tasks|projects_year)$/.test(String(slides[j].kind))))) return j;
    }
    return -1;
  }
  // Wischgeste nach links: mindestens 60 px waagerecht, weniger als 40 px senkrecht.
  function isSwipeLeft(start, end) {
    if (!start || !end) return false;
    var dx = start.x - end.x, dy = Math.abs(start.y - end.y);
    return dx >= 60 && dy < 40;
  }
  // Folienarten, die ältere Terminals kennen. Neuere Arten (Statistik, Grafana, Website, Planung, Aufgaben, Projekte) bleiben aus dem
  // Zwischenspeicher: ein Terminal mit älterem Stand lehnt unbekannte Arten ab und würde alles verwerfen.
  var CACHEABLE_KINDS = ['image', 'text', 'vorgaenge'];

  // Nutzdaten für POST /api/slides/cache: {version, saved_at, slides, images:{id: base64}}.
  // Je Bild höchstens 5 MB (größere entfallen), gesamt höchstens 50 MB (sonst gar keine Bilder).
  // images: {id: base64-Text}; savedAt optional (Date), sonst jetzt.
  function cachePayload(json, images, savedAt) {
    var out = {};
    var total = 0;
    var src = images && typeof images === 'object' ? images : {};
    Object.keys(src).forEach(function (id) {
      var b = src[id];
      if (typeof b !== 'string' || !b) return;
      var pad = b.charAt(b.length - 1) === '=' ? (b.charAt(b.length - 2) === '=' ? 2 : 1) : 0;
      var bytes = Math.floor(b.length * 3 / 4) - pad;
      if (bytes > 5 * 1024 * 1024) return;
      out[id] = b;
      total += bytes;
    });
    if (total > 50 * 1024 * 1024) out = {};
    json = json || {};
    return {
      version: typeof json.version === 'string' ? json.version : '',
      saved_at: (savedAt instanceof Date ? savedAt : new Date()).toISOString(),
      slides: Array.isArray(json.slides) ? json.slides.filter(function (s) {
        return s && CACHEABLE_KINDS.indexOf(s.kind) !== -1;
      }) : [],
      images: out
    };
  }

  // Kartendienst des lokalen Terminals (Ursprung der Ereignisse und der Rückmeldung).
  var CARD_ORIGIN = 'http://127.0.0.1:8080';

  // Funktionen und Farben (Ereignis settings: functions, theme): ändert sich ein Schalter oder das Farbschema,
  // lädt die Seite neu, damit Kacheln und Farben stimmen. Das erste Ereignis ist der Ausgangsstand.
  var fnSig = null;
  function fnChanged(d) {
    if (!d || !d.functions || typeof d.functions !== 'object') return false;
    var s = Object.keys(d.functions).sort().map(function (k) {
      return k + '=' + (d.functions[k] && d.functions[k].enabled === true);
    }).join(',') + '|' + (d.theme && d.theme.scheme);
    if (fnSig === null) { fnSig = s; return false; }
    return s !== fnSig;
  }

  // Bildschirmtastatur auf Anforderung (nur auf dem Gerät, Gerätesitzung): focusin auf ein Texteingabefeld zeigt
  // sie, focusout (150 ms verzögert, wenn nicht gleich das nächste Feld den Fokus bekommt), Absenden und pagehide
  // verbergen sie. Felder mit inputmode="none" haben ihr eigenes Ziffernfeld. Fehler werden ignoriert
  // (Windows und ältere Geräte antworten 503 oder gar nicht).
  var KB_TYPES = { text: 1, password: 1, number: 1, search: 1, email: 1, tel: 1, url: 1 };
  function wantsKeyboard(el) {
    if (!el || el.disabled || el.readOnly) return false;
    var tag = String(el.tagName || '').toLowerCase();
    if (el.getAttribute && el.getAttribute('inputmode') === 'none') return false;
    if (tag === 'textarea') return true;
    if (tag !== 'input') return false;
    var t = String((el.getAttribute && el.getAttribute('type')) || 'text').toLowerCase();
    return KB_TYPES[t] === 1;
  }
  // Tastatur nur nach echter Berührung: Zeigen nur, wenn kurz zuvor ein Zeiger/Touch auf dem Feld (oder seinem
  // Etikett) begann. Programmatischer Fokus, autofocus und vom Browser wiederhergestellter Fokus zeigen sie nie.
  var KB_POINTER_MS = 800;
  function kbShowDecision(pointerAt, now) {
    if (typeof pointerAt !== 'number' || typeof now !== 'number' || pointerAt <= 0) return false;
    var d = now - pointerAt;
    return d >= 0 && d <= KB_POINTER_MS;
  }
  if (body.classList.contains('has-gear')) {
    var kbHide = null;
    var kbPointerAt = 0, kbPointerEl = null;
    var kbPointer = function (ev) { kbPointerAt = Date.now(); kbPointerEl = ev.target || null; };
    document.addEventListener('pointerdown', kbPointer, true);
    document.addEventListener('touchstart', kbPointer, true);
    // Knopf, der ein Feld absichtlich öffnet und fokussiert (z. B. „Benutzer/Passwort“)
    var kbUserGesture = function () { kbPointerAt = Date.now(); kbPointerEl = null; };
    var kbFromPointer = function (el) {
      var t = kbPointerEl;
      if (!t) return true; // ausdrückliche Geste (Knopf), kein Zielfeld
      if (t === el || (t.contains && t.contains(el)) || (el.contains && el.contains(t))) return true;
      var lab = t.closest ? t.closest('label') : null;
      return !!(lab && lab.control === el);
    };
    var kbCall = function (action) {
      try { fetch('http://127.0.0.1:8080/api/keyboard/' + action, { method: 'POST', keepalive: action === 'hide' }).catch(function () { /* ignorieren */ }); } catch (e) { /* kein fetch */ }
    };
    document.addEventListener('focusin', function (ev) {
      if (!wantsKeyboard(ev.target)) return;
      if (kbHide) { clearTimeout(kbHide); kbHide = null; }
      if (kbShowDecision(kbPointerAt, Date.now()) && kbFromPointer(ev.target)) kbCall('show');
    });
    document.addEventListener('focusout', function (ev) {
      if (!wantsKeyboard(ev.target)) return;
      if (kbHide) clearTimeout(kbHide);
      kbHide = setTimeout(function () {
        kbHide = null;
        if (!wantsKeyboard(document.activeElement)) kbCall('hide');
      }, 150);
    });
    window.pdhKbGesture = kbUserGesture;
    document.addEventListener('submit', function () { kbCall('hide'); }, true);
    window.addEventListener('pagehide', function () { kbCall('hide'); });
  }

  // Ausfall des PDH-Servers – reine Funktionen. Im globalen Modus zeigt der Kartendienst des Terminals
  // (CARD_ORIGIN + '/start') die zwischengespeicherte Folienschau, solange PDH nicht erreichbar ist.
  var OUTAGE_LIMIT = 3;
  // Zähler aufeinanderfolgender Netzfehler: failed=true zählt hoch, failed=false setzt zurück.
  // Rückgabe {fails, leave}; leave=true ab OUTAGE_LIMIT Fehlern in Folge.
  function outageStep(fails, failed) {
    var n = failed ? (typeof fails === 'number' && fails > 0 ? fails : 0) + 1 : 0;
    return { fails: n, leave: n >= OUTAGE_LIMIT };
  }
  // Ziel beim Verlassen der Vorgang-Ansicht: PDH erreichbar → Folienschau (/terminal),
  // Netzfehler → Startseite des Terminals (zeigt offline die zwischengespeicherte Folienschau).
  function backTarget(networkFailed, cardOrigin) {
    return networkFailed ? String(cardOrigin) + '/start' : '/terminal';
  }

  // Rückmeldung an das Terminal – reine Funktionen.
  // Nutzdaten für POST /api/card/result: {ok, name, message, notify, logout}.
  function resultPayload(d, notify, logout) {
    d = d || {};
    return {
      ok: d.ok === true,
      name: typeof d.name === 'string' ? d.name : '',
      message: typeof d.message === 'string' ? d.message : '',
      notify: Array.isArray(notify) ? notify : [],
      logout: !!logout
    };
  }
  // Ergebnis der Abmeldung für das Terminal: ok nur, wenn PDH die Abmeldung angenommen hat (HTTP 2xx);
  // sonst steht eine Meldung dabei. Ohne Antwort (null) gilt sie als fehlgeschlagen.
  function logoutResult(r, who) {
    var ok = !!(r && r.ok);
    return { ok: ok, name: typeof who === 'string' ? who : '', message: ok ? '' : 'Abmeldung fehlgeschlagen' };
  }
  // Benachrichtigungsarten aus dem JSON-Text (data-card-notify); fehlt oder ungültig → [].
  function parseNotify(text) {
    var v = null;
    try { v = JSON.parse(text); } catch (e) { return []; }
    if (!Array.isArray(v)) return [];
    return v.filter(function (x) { return typeof x === 'string'; });
  }
  // Karte, die der Schoner hinterlegt hat: liefert die UID, wenn der Eintrag höchstens 10 s alt ist; löscht ihn immer.
  function takePendingCard(store, now) {
    var raw = null;
    try {
      raw = store.getItem('pdh.pendingCard');
      store.removeItem('pdh.pendingCard');
    } catch (e) { return ''; }
    if (!raw) return '';
    var v = null;
    try { v = JSON.parse(raw); } catch (e) { return ''; }
    if (!v || typeof v.uid !== 'string' || !v.uid || typeof v.t !== 'number') return '';
    var age = now - v.t;
    return (age <= 10000 && age >= -2000) ? v.uid : '';
  }
  // Dasselbe Kartenereignis (uid + at) nur einmal verarbeiten: der Pi spielt beim Verbinden ein bis zu 5 s
  // altes Ereignis erneut ab (jede Seitennavigation). true = schon gesehen.
  function seenCardEvent(d) {
    var evKey = d.uid + '|' + String(d.at);
    var seen = null;
    try { seen = sessionStorage.getItem('pdhLastCardEvent'); } catch (e) { /* kein Speicher */ }
    if (seen === evKey) return true;
    try { sessionStorage.setItem('pdhLastCardEvent', evKey); } catch (e) { /* kein Speicher */ }
    return false;
  }

  // Uhr (nur Anzeige)
  var clocks = document.querySelectorAll('[data-clock]');
  function tick() {
    var t = new Date().toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });
    for (var i = 0; i < clocks.length; i++) clocks[i].textContent = t;
  }
  if (clocks.length) { tick(); setInterval(tick, 5000); }

  // Vorgang-Ansicht verlassen (Zurück oder Ruhezeit): erst prüfen, ob PDH antwortet; bei Netzfehler zur
  // Startseite des Terminals (dort läuft offline die zwischengespeicherte Folienschau).
  function leaveVorgang() {
    var go = function (failed) { location.replace(backTarget(failed, CARD_ORIGIN)); };
    try {
      fetch('/terminal/slides.json', { cache: 'no-store', credentials: 'same-origin' })
        .then(function () { go(false); }, function () { go(true); });
    } catch (e) { go(true); }
  }
  if (mode === 'vorgang') {
    var backLink = document.querySelector('.v-back');
    if (backLink) backLink.addEventListener('click', function (ev) { ev.preventDefault(); leaveVorgang(); });
  }

  // Inaktivität: keine Berührung/Taste für data-idle Sekunden → zurück zum Schoner.
  // Auf dem Schoner selbst (data-idle="0") gibt es keinen Timer.
  var idle = parseInt(body.dataset.idle, 10) || 0;
  if (mode !== 'schoner' && idle > 0) {
    var timer = null;
    var reset = function () {
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { document.dispatchEvent(new CustomEvent('pdh-idle')); if (mode === 'vorgang') leaveVorgang(); else location.replace('/terminal'); }, idle * 1000);
    };
    document.addEventListener('pointerdown', reset, true);
    document.addEventListener('keydown', reset, true);
    reset();
  }

  // Störungsinfo: alle 30 s neu laden (nur Zahlen und Listen, kein Seitenwechsel)
  if (mode === 'info') {
    var fill = function (kind, items, total) {
      var list = document.querySelector('[data-list="' + kind + '"]');
      var empty = document.querySelector('[data-empty="' + kind + '"]');
      var more = document.querySelector('[data-more="' + kind + '"]');
      var cnt = document.querySelector('[data-count="' + kind + '"]');
      if (!list || !cnt || !more) return; // Kachel ohne Berechtigung: nichts zu füllen
      cnt.textContent = total;
      cnt.classList.toggle('has', total > 0);
      list.textContent = '';
      items.forEach(function (it) {
        var li = document.createElement('li');
        var a = document.createElement('span'); a.className = 'ti-title'; a.textContent = it.title;
        var b = document.createElement('span'); b.className = 'ti-st'; b.textContent = it.statusLabel;
        var c = document.createElement('span'); c.className = 'ti-meta';
        c.textContent = (it.path ? it.path + ' · ' : '') + it.age;
        li.appendChild(a); li.appendChild(b); li.appendChild(c);
        list.appendChild(li);
      });
      list.hidden = items.length === 0;
      if (empty) empty.hidden = items.length > 0;
      else if (items.length === 0) {
        // Leer-Hinweis nachträglich anlegen
        var e = document.createElement('div');
        e.className = 't-empty'; e.setAttribute('data-empty', kind);
        e.textContent = kind === 'faults' ? 'Keine offenen Störungen' : 'Keine offenen Tickets';
        list.parentNode.insertBefore(e, list.nextSibling);
      }
      var rest = Math.max(0, total - items.length);
      more.hidden = rest === 0;
      more.textContent = rest ? '… und ' + rest + ' weitere' : '';
    };
    var refresh = function () {
      fetch('/terminal/info.json', { cache: 'no-store', credentials: 'same-origin' })
        .then(function (r) {
          // Anlage nicht mehr zugeordnet/ausgeblendet (403/404 ...): zurück zum Schoner statt veralteter Liste
          if (r.status === 403 || r.status === 404 || r.redirected) { location.replace('/terminal'); return null; }
          if (!r.ok) return null; // vorübergehender Fehler: alter Stand bleibt
          return r.json();
        })
        .then(function (d) {
          if (!d) return;
          if (d.canFaults !== false) fill('faults', d.faults || [], d.faultsTotal || 0);
          if (d.canTickets !== false) fill('tickets', d.tickets || [], d.ticketsTotal || 0);
        })
        .catch(function () { /* offline: alter Stand bleibt */ });
    };
    setInterval(refresh, 30000);

    // Vorgangsleiste: alle 30 s nur die Leiste neu holen (kein Seitenwechsel, Ruhezeit bleibt unberührt); nicht
    // während ein Fenster oder der Abschluss-Assistent offen ist.
    if (body.dataset.vbar === '1') {
      setInterval(function () {
        var bar = document.getElementById('vbar'), cw = document.getElementById('cw');
        if (!bar || document.querySelector('[data-bar-dialog]') || (cw && cw.classList.contains('open'))) return;
        fetch('/terminal/info?fragment=bar', { cache: 'no-store', credentials: 'same-origin' })
          .then(function (r) { return r.ok ? r.text() : null; })
          .then(function (html) {
            var cur = document.getElementById('vbar');
            if (!html || !cur || document.querySelector('[data-bar-dialog]')) return;
            var doc = new DOMParser().parseFromString(html, 'text/html');
            var next = doc.getElementById('vbar');
            if (next) { next = document.importNode(next, true); var st = cur.scrollTop; cur.replaceWith(next); next.scrollTop = st; }
          })
          .catch(function () { /* offline: alter Stand bleibt */ });
      }, 30000);
    }
  }

  // Gerätesitzung ohne Anmeldeteil (Schoner, Seite „keine Anlage“): auf Einstellungen hören. Wird das Terminal
  // zum globalen, lädt die Seite neu und wird zur Folienschau. (Info und Neu tun das im Anmeldeteil.)
  if (body.dataset.settingsEvents === '1' && mode !== 'global' && !saver && (body.dataset.cardEvents !== '1' || mode === 'schoner')) {
    try {
      var sev = new EventSource('/terminal/events');
      sev.addEventListener('settings', function (ev) {
        var d = null;
        try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
        if (d && (d.global_screen === true || (mode === 'schoner' && d.saver_slides === true) || fnChanged(d))) location.reload();
      });
      sev.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
    } catch (e) { /* kein EventSource */ }
  }

  // Bildschirmschoner: eine Karte weckt ihn. Die UID wandert (mit Zeit) in den Sitzungsspeicher, die
  // Info-Seite meldet sie an wie eine frisch gelesene.
  if (body.dataset.cardEvents === '1' && mode === 'schoner') {
    try {
      var ses = new EventSource(CARD_ORIGIN + '/api/card/events');
      ses.addEventListener('card', function (ev) {
        var d = null;
        try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
        if (!d || typeof d.uid !== 'string' || !d.uid) return;
        if (seenCardEvent(d)) return;
        try { sessionStorage.setItem('pdh.pendingCard', JSON.stringify({ uid: d.uid, t: Date.now() })); } catch (e) { /* kein Speicher */ }
        ses.close();
        location.replace('/terminal/info');
      });
      ses.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
    } catch (e) { /* kein Kartendienst */ }
  }

  // Rüsten (Neu, Rüsten): wer rüstet und was sichtbar ist, hängt an der Kartenanmeldung – bei Wechsel neu laden.
  // (Info-Seite mit Vorgangsleiste ebenfalls: Fertigmelden und Ergänzen hängen an der Anmeldung)
  if (mode === 'neu' || mode === 'rusten' || mode === 'stillstand' || mode === 'melden' || mode === 'hmi' || mode === 'wartung' || mode === 'vorgaenge' || mode === 'zeit' || (mode === 'info' && body.dataset.vbar === '1')) {
    document.addEventListener('pdh-card-login', function (e) { if (!!e.detail !== (body.dataset.cardIn === '1')) location.reload(); });
  }

  // Zeiterfassung (terminal_zeit.go): Stand (wer ist da, wer arbeitet woran) alle 30 s neu holen, solange kein Fenster offen ist.
  if (mode === 'zeit') {
    setInterval(function () {
      if (document.querySelector('.t-dialog')) return;
      fetch('/terminal/zeit?fragment=status', { cache: 'no-store', credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.text() : null; })
        .then(function (html) {
          var cur = document.getElementById('z-status');
          if (!html || !cur || document.querySelector('.t-dialog')) return;
          var next = new DOMParser().parseFromString(html, 'text/html').getElementById('z-status');
          if (next) cur.replaceWith(document.importNode(next, true));
        })
        .catch(function () { /* offline: alter Stand bleibt */ });
    }, 30000);
  }

  // Vorgänge (terminal_vorgaenge.go): Eine vorgehaltene Karte öffnet den Stand der Person – von der Info-Seite aus
  // einmal je Anmeldung (Marker im Sitzungsspeicher, damit Zurück nicht gleich wieder dorthin führt).
  // Mit Vorgangsleiste entfällt der Sprung: die Leiste zeigt die Vorgänge der Person selbst.
  if (mode === 'info' && body.dataset.vorgaenge === '1' && body.dataset.vbar !== '1') {
    document.addEventListener('pdh-card-login', function (e) {
      var seen = false;
      try { seen = sessionStorage.getItem('pdh.vorgShown') === '1'; } catch (err) { /* kein Speicher */ }
      if (!e.detail) { try { sessionStorage.removeItem('pdh.vorgShown'); } catch (err) { /* ok */ } return; }
      if (seen) return;
      try { sessionStorage.setItem('pdh.vorgShown', '1'); } catch (err) { /* ok */ }
      location.href = '/terminal/vorgaenge';
    });
  }

  // Vorgänge: Beenden einer Störung/eines Tickets öffnet den Abschluss-Assistenten (Token nur für diesen Vorgang);
  // bei einer Aufgabe antwortet der Server mit der Liste.
  if (mode === 'vorgaenge' || (mode === 'info' && body.dataset.vbar === '1')) {
    var vNote = document.getElementById('v-note');
    var vShow = function (t) { vNote = document.getElementById('v-note') || vNote; if (!vNote) { alert(t); return; } vNote.textContent = t; vNote.hidden = false; };
    document.addEventListener('submit', function (ev) {
      var f = ev.target;
      if (!f || !f.hasAttribute || !f.hasAttribute('data-fin')) return;
      var sub = ev.submitter;
      if (sub && sub.getAttribute('formaction')) return; // „Warten“ im selben Formular: normal absenden
      ev.preventDefault();
      var btns = f.querySelectorAll('button');
      btns.forEach(function (b) { b.disabled = true; });
      fetch(f.action, {
        method: 'POST', credentials: 'same-origin', cache: 'no-store',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Accept': 'application/json' },
        body: new URLSearchParams(new FormData(f)).toString()
      }).then(function (r) {
        var ct = r.headers.get('Content-Type') || '';
        if (ct.indexOf('application/json') >= 0) return r.json().then(function (d) { return { ok: r.ok, d: d }; });
        if (r.redirected) { location.href = r.url; return null; }
        return r.text().then(function (t) { return { ok: false, text: t }; });
      }).then(function (x) {
        if (!x) return;
        if (!x.ok || !x.d || !x.d.token) {
          btns.forEach(function (b) { b.disabled = false; });
          vShow((x.text || 'Das hat nicht geklappt – bitte nochmal versuchen.').slice(0, 200));
          return;
        }
        if (typeof window.pdhComplete !== 'function') { location.reload(); return; }
        var back = f.getAttribute('data-back') || '/terminal/vorgaenge';
        window.pdhComplete(x.d.type, x.d.id, function () { location.href = back; }, { token: x.d.token, who: x.d.who, pre: x.d.pre || [] });
      }).catch(function () {
        btns.forEach(function (b) { b.disabled = false; });
        vShow('Keine Verbindung – bitte nochmal versuchen.');
      });
    });
  }

  // Sensoren (ESPPDH) nach Kartenanmeldung: /terminal/sensors.json alle 10 s; ohne Anmeldung
  // oder ohne Recht liefert der Server {"show": false} – dann bleibt der Bereich leer und verborgen.
  var sens = document.querySelector('[data-sensors]');
  if (sens && mode === 'info') {
    var sensBody = sens.querySelector('[data-sensors-body]');
    var hsPrev = {};
    var mk = function (tag, cls, text) {
      var e = document.createElement(tag);
      if (cls) e.className = cls;
      if (text !== undefined) e.textContent = text;
      return e;
    };
    var hsNode = function (h) {
      var s = mk('span', 'hs ' + (h.Running ? 'hs-run' : (h.Stopped ? 'hs-stop' : 'hs-none')));
      s.appendChild(mk('span', 'hs-label', h.Label));
      s.appendChild(mk('code', 'hs-count', h.Count));
      s.appendChild(mk('span', 'hs-state', h.Running ? 'läuft' : (h.Stopped ? (h.Since ? 'steht seit ' + h.Since : 'kein Lebenszeichen') : 'noch kein Lebenszeichen')));
      if (hsPrev[h.ID] !== undefined && hsPrev[h.ID] !== h.Count) s.classList.add('hs-pulse');
      hsPrev[h.ID] = h.Count;
      return s;
    };
    var hideSensors = function () { sensBody.textContent = ''; sens.hidden = true; };
    var drawSensors = function (d) {
      if (!d || !d.show) { hideSensors(); return; }
      sensBody.textContent = '';
      sens.hidden = false;
      if (!d.available) sensBody.appendChild(mk('div', 't-sens', 'Werte gerade nicht verfügbar'));
      (d.handshakes || []).forEach(function (h) {
        var c = mk('div', 't-sens');
        c.appendChild(hsNode(h));
        sensBody.appendChild(c);
      });
      (d.devices || []).forEach(function (v) {
        var c = mk('div', 't-sens');
        var head = mk('div', 't-sens-head');
        head.appendChild(mk('span', null, v.Name));
        head.appendChild(hsNode(v.Handshake));
        c.appendChild(head);
        (v.Groups || []).forEach(function (g) {
          c.appendChild(mk('div', 't-sens-g', g.Name));
          (g.Rows || []).forEach(function (r) {
            var row = mk('div', 't-sens-row' + (r.Stale ? ' stale' : ''));
            row.appendChild(mk('span', null, r.Name));
            row.appendChild(mk('span', 'v', r.Value + (r.Unit ? ' ' + r.Unit : '')));
            c.appendChild(row);
          });
        });
        sensBody.appendChild(c);
      });
      if (!(d.devices || []).length && !(d.handshakes || []).length) sensBody.appendChild(mk('div', 't-sens', 'Keine Sensoren an dieser Anlage'));
    };
    var pullSensors = function () {
      fetch('/terminal/sensors.json', { cache: 'no-store', credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(drawSensors)
        .catch(function () { /* offline: alter Stand bleibt */ });
    };
    document.addEventListener('pdh-card-login', function (e) {
      if (!e.detail) hideSensors(); // abgemeldet: sofort weg, nicht erst beim nächsten Abruf
      pullSensors();
    });
    pullSensors();
    setInterval(pullSensors, 10000);
  }

  // Kartenleser: der Kartendienst auf dem Pi (nur Gerätesitzung, data-card-events) meldet
  // gelesene Karten per Server-Sent Events; die Seite reicht sie an PDH weiter.
  if (body.dataset.cardEvents === '1' && mode !== 'schoner') {
    var bar = document.querySelector('[data-card-bar]');
    var nameEl = document.querySelector('[data-card-name]');
    var msgEl = document.querySelector('[data-card-msg]');
    var logoutBtn = document.querySelector('[data-card-logout]');
    if (bar && nameEl && msgEl && logoutBtn) {
      var loggedIn = false, lastPing = 0, msgTimer = null, gen = 0;
      var onLoginChange = function () {}; // wird vom Anmeldeteil unten gesetzt
      var onReader = function () {};      // Leserstatus des Kartendienstes
      var notify = parseNotify(body.dataset.cardNotify); // wirksame Benachrichtigungsarten (alter PDH: [])
      var reportOk = true;
      // Ergebnis an das lokale Terminal melden (Windows-Mitteilung im Hintergrund). Jeder Fehler und jedes 404
      // (älteres Terminal ohne diese Schnittstelle) bleibt stumm; nach dem ersten Fehlschlag ruht es bis zum Neuladen.
      var report = function (d, logout) {
        if (!reportOk) return;
        try {
          fetch(CARD_ORIGIN + '/api/card/result', {
            mode: 'cors', method: 'POST', cache: 'no-store',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(resultPayload(d, notify, logout))
          }).then(function (r) { if (!r.ok) reportOk = false; })
            .catch(function () { reportOk = false; });
        } catch (e) { reportOk = false; }
      };
      var post = function (url, data) {
        return fetch(url, {
          method: 'POST', credentials: 'same-origin', cache: 'no-store',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(data || {})
        });
      };
      var showBar = function (name) {
        loggedIn = !!name;
        nameEl.textContent = name || '';
        bar.hidden = !loggedIn;
        onLoginChange();
        // Sensoren (unten) sofort neu laden bzw. ausblenden
        document.dispatchEvent(new CustomEvent('pdh-card-login', { detail: loggedIn }));
      };
      var showMsg = function (text) {
        msgEl.textContent = text;
        msgEl.hidden = false;
        if (msgTimer) clearTimeout(msgTimer);
        msgTimer = setTimeout(function () { msgEl.hidden = true; }, 4000);
      };
      var ping = function () {
        lastPing = Date.now();
        var g = gen; // Antworten aus älterer Generation (vor Abmelden/neuer Karte) verwerfen
        post('/terminal/card/ping').then(function (r) { return r.ok ? r.json() : null; })
          .then(function (d) { if (d && g === gen) showBar(d.active ? d.name : ''); })
          .catch(function () { /* offline: Stand bleibt */ });
      };
      var onCard = function (uid) {
        var g = ++gen;
        post('/terminal/card', { uid: uid })
          .then(function (r) { return r.json().catch(function () { return null; }); })
          .then(function (d) {
            if (!d) return;
            if (g !== gen) return;
            report(d, false);
            if (d.ok) { msgEl.hidden = true; showBar(d.name); lastPing = Date.now(); }
            else if (d.message) showMsg(d.message);
          })
          .catch(function () { /* offline: ignorieren */ });
      };
      logoutBtn.addEventListener('click', function () {
        gen++;
        var who = nameEl.textContent;
        showBar('');
        post('/terminal/card/abmelden').then(function (r) {
          report(logoutResult(r, who), true);
        }).catch(function () {});
      });
      // Berührung bei laufender Kartensitzung: höchstens alle 20 s nachfragen, ob sie noch läuft
      document.addEventListener('pointerdown', function (ev) {
        if (loggedIn && Date.now() - lastPing > 20000 && !logoutBtn.contains(ev.target)) ping();
      }, true);
      ping();

      // ---- Anmeldung ohne Kartenleser ----
      var panel = document.getElementById('login-panel');
      var keypad = document.getElementById('login-keypad');
      var pwBtn = document.getElementById('login-password-btn');
      var dialog = document.getElementById('login-dialog');
      if (panel && keypad && pwBtn && dialog) {
        var loginMsg = panel.querySelector('[data-login-msg]');
        var fieldsEl = panel.querySelector('[data-login-fields]');
        var cardIn = document.getElementById('login-card');
        var pinIn = document.getElementById('login-pin');
        var userIn = document.getElementById('login-user');
        var passIn = document.getElementById('login-pass');
        var dForm = dialog.querySelector('form');
        var settings = {
          manual_login: body.dataset.manualLogin || 'card_pin',
          password_button: body.dataset.passwordButton !== '0',
          idle: idle
        };
        // Ältere Geräte senden kein „reader“: dann bleibt es beim Standard „ok“ (= nach 3 s ohne Ereignis).
        var reader = 'ok';
        var activeIn = cardIn, dialogOpen = false;
        var padEl = panel.querySelector('[data-login-pad]');
        var padOpen = false;
        var padEvent = function (ev) { padOpen = keypadState(padOpen, ev); };

        var render = function () {
          var view = loginView(reader, settings);
          if (loggedIn || view === 'card') dialogOpen = false;
          if (loggedIn) { padEvent('login'); clearInputs(); } // Karte hat angemeldet: halb getippte Eingaben verwerfen
          // Der Dialog Benutzer/Passwort öffnet nur auf ausdrücklichen Klick auf den Knopf, nie von selbst.
          var open = !loggedIn && dialogOpen;
          var noReader = !loggedIn && view === 'none';
          var fields = !loggedIn && (view === 'card_pin' || view === 'card_only');
          var btn = !loggedIn && (showPasswordButton(reader, settings) || view === 'password');
          loginMsg.textContent = noReader ? 'Kein Kartenleser – Anmeldung nicht möglich' : '';
          loginMsg.hidden = !noReader;
          fieldsEl.hidden = !fields;
          if (!fields) padEvent('cancel');
          padEl.hidden = !padOpen;
          cardIn.setAttribute('aria-expanded', padOpen ? 'true' : 'false');
          pinIn.hidden = view !== 'card_pin';
          if (pinIn.hidden && activeIn === pinIn) activeIn = cardIn;
          pwBtn.hidden = !btn;
          dialog.hidden = !open;
          panel.hidden = loginMsg.hidden && fieldsEl.hidden && pwBtn.hidden;
        };
        onLoginChange = render;
        var clearInputs = function (keepFocus) {
          cardIn.value = ''; pinIn.value = ''; userIn.value = ''; passIn.value = '';
          if (keepFocus) return; // nach Fehlschlag: Fokus im Feld lassen, nur der Inhalt wird geleert
          var ae = document.activeElement;
          if (ae && (ae === pinIn || ae === userIn || ae === passIn)) ae.blur(); // activeIn passt zum Fokus
          activeIn = cardIn;
        };
        var submit = function (data) {
          var good = false, g = ++gen; // Antworten älterer Versuche/Abmelden verwerfen
          post('/terminal/login', data)
            .then(function (r) {
              var status = r.status;
              return r.json().catch(function () { return null; }).then(function (d) { return { s: status, d: d }; });
            })
            .then(function (x) {
              if (g !== gen) return;
              var d = x.d;
              if (d && d.ok) { good = true; msgEl.hidden = true; dialogOpen = false; padEvent('login'); showBar(d.name); lastPing = Date.now(); }
              else if (x.s === 429) showMsg((d && d.message) || 'Bitte kurz warten');
              else showMsg((d && d.message) || 'Anmeldung fehlgeschlagen');
            })
            .catch(function () { if (g === gen) showMsg('Anmeldung fehlgeschlagen'); }) // Netzfehler
            .then(function () { clearInputs(!good); if (!good) padEvent('open'); render(); }); // Eingaben nach jedem Versuch leeren; blurren nur nach Erfolg
        };

        // Ziffernfeld: 0–9, A–F, Löschen, OK (Tasten mindestens 64 px, siehe .t-keypad)
        var mkKey = function (label, val, aria, primary) {
          var b = document.createElement('button');
          b.type = 'button'; b.className = 't-btn' + (primary ? ' primary' : '');
          b.textContent = label; b.setAttribute('data-key', val);
          if (aria) b.setAttribute('aria-label', aria);
          return b;
        };
        '0123456789ABCDEF'.split('').forEach(function (k) { keypad.appendChild(mkKey(k, k)); });
        keypad.appendChild(mkKey('⌫', 'del', 'Löschen'));
        keypad.appendChild(mkKey('OK', 'ok', 'OK', true));
        cardIn.addEventListener('focus', function () { activeIn = cardIn; });
        // Nur ein Antippen des Kartenfeldes klappt das Ziffernfeld aus (Fokus allein nicht).
        cardIn.addEventListener('click', function () { activeIn = cardIn; padEvent('open'); render(); });
        panel.querySelector('[data-login-collapse]').addEventListener('click', function () { padEvent('cancel'); render(); });
        document.addEventListener('pdh-idle', function () { padEvent('idle'); render(); });
        pinIn.addEventListener('focus', function () { activeIn = pinIn; });
        keypad.addEventListener('pointerdown', function (ev) { ev.preventDefault(); }); // Fokus im Feld lassen
        keypad.addEventListener('click', function (ev) {
          var b = ev.target.closest ? ev.target.closest('button[data-key]') : null;
          if (!b) return;
          var k = b.getAttribute('data-key'), f = activeIn;
          if (k === 'del') { f.value = f.value.slice(0, -1); return; }
          if (k === 'ok') {
            var view = loginView(reader, settings), card = cardIn.value.trim();
            if (!card) { cardIn.focus(); return; }
            if (view === 'card_pin') {
              if (!pinIn.value) { pinIn.focus(); return; }
              padEvent('ok'); render();
              submit({ kind: 'card_pin', card: card, pin: pinIn.value });
            } else if (view === 'card_only') {
              padEvent('ok'); render();
              submit({ kind: 'card_only', card: card });
            }
            return;
          }
          if (f === pinIn && !/^[0-9]$/.test(k)) return; // PIN nur Ziffern
          if (f.value.length < (f === pinIn ? 8 : 32)) f.value += k;
        });

        // Dialog Benutzer/Passwort
        pwBtn.addEventListener('click', function () { dialogOpen = true; render(); if (window.pdhKbGesture) window.pdhKbGesture(); userIn.focus(); });
        dialog.querySelector('[data-login-cancel]').addEventListener('click', function () {
          dialogOpen = false; clearInputs(); render();
        });
        dForm.addEventListener('submit', function (ev) {
          ev.preventDefault();
          var u = userIn.value.trim(), p = passIn.value;
          if (!u || !p) { showMsg('Anmeldung fehlgeschlagen'); clearInputs(true); return; }
          submit({ kind: 'password', user: u, password: p });
        });

        onReader = function (state) {
          reader = (state === 'none' || state === 'error') ? state : 'ok';
          render();
        };

        // Einstellungen des Terminals: Anfangswerte stehen in der Seite, Änderungen kommen sofort per Strom.
        try {
          var sset = new EventSource('/terminal/events');
          sset.addEventListener('settings', function (ev) {
            var d = null;
            try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
            if (!d) return;
            if (d.global_screen === true) { location.reload(); return; } // jetzt ein globales Terminal: Seite wird zur Folienschau
            if (fnChanged(d)) { location.reload(); return; } // Funktion oder Farbschema geändert: Kacheln und Farben neu
            if (typeof d.manual_login === 'string') settings.manual_login = d.manual_login;
            if (typeof d.password_button === 'boolean') settings.password_button = d.password_button;
            // neue Rückkehrzeit gilt ab dem nächsten Zurücksetzen des Timers
            if (typeof d.idle === 'number' && d.idle > 0) { idle = d.idle; settings.idle = d.idle; }
            if (Array.isArray(d.card_notify)) notify = parseNotify(JSON.stringify(d.card_notify));
            render();
          });
          sset.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
        } catch (e) { /* kein EventSource */ }
        render();
      }

      try {
        var es = new EventSource(CARD_ORIGIN + '/api/card/events');
        es.addEventListener('reader', function (ev) {
          var d = null;
          try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
          if (d && typeof d.state === 'string') onReader(d.state);
        });
        es.addEventListener('card', function (ev) {
          var d = null;
          try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
          if (d && typeof d.uid === 'string' && d.uid) {
            // Doppelte Wiedergabe sonst: Karte doppelt gemeldet bzw. nach "Abmelden" + Navigation wieder angemeldet.
            if (seenCardEvent(d)) return;
            onCard(d.uid);
          }
        });
        es.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
      } catch (e) { /* kein Kartendienst */ }

      // Karte vom Schoner: wie eine frisch gelesene anmelden (Eintrag wird sofort gelöscht, höchstens 10 s alt).
      var pending = '';
      try { pending = takePendingCard(sessionStorage, Date.now()); } catch (e) { /* kein Speicher */ }
      if (pending) onCard(pending);
    }
  }

  // Zwischenspeicher des lokalen Terminals (Ursprung wie bei /api/card/result). Fehler und 404 (älteres
  // Terminal) bleiben stumm; nach dem ersten Fehlschlag ruht es bis zum Neuladen.
  var cacheOk = true;
  function cacheCall(method, data) {
    if (!cacheOk) return;
    try {
      var opt = { mode: 'cors', method: method, cache: 'no-store' };
      if (data) { opt.headers = { 'Content-Type': 'application/json' }; opt.body = JSON.stringify(data); }
      fetch(CARD_ORIGIN + '/api/slides/cache', opt)
        .then(function (r) { if (!r.ok) cacheOk = false; })
        .catch(function () { cacheOk = false; });
    } catch (e) { cacheOk = false; }
  }

  // Normales Terminal (Gerätesitzung): ein eventuell vorhandener Zwischenspeicher wird gelöscht.
  if (mode !== 'global' && body.dataset.cardEvents === '1') cacheCall('DELETE', null);

  // Infobildschirm: Folienschau.
  if (mode === 'global' || saver) {
    var stage = document.getElementById('g-stage');
    var pdhName = (stage && stage.dataset.name) || 'PDH';
    var reloadTimer = null; // Website-Folie: regelmäßiges Neuladen des Rahmens
    var slides = [], version = '', cur = -1, slideTimer = null, cachedVersion = null, caching = false, again = false;
    document.addEventListener('contextmenu', function (ev) { ev.preventDefault(); });
    document.addEventListener('selectstart', function (ev) { ev.preventDefault(); });
    document.addEventListener('dragstart', function (ev) { ev.preventDefault(); });

    var el = function (tag, cls, text) {
      var e = document.createElement(tag);
      if (cls) e.className = cls;
      if (text !== undefined) e.textContent = text;
      return e;
    };
    var fillList = function (tile, kind, items, total) {
      var ul = el('ul', 't-list');
      items.forEach(function (it) {
        var li = el('li');
        li.setAttribute('data-kind', kind);
        li.setAttribute('data-id', it.id);
        li.appendChild(el('span', 'ti-title', it.title));
        li.appendChild(el('span', 'ti-st', it.statusLabel));
        li.appendChild(el('span', 'ti-meta', (it.path ? it.path + ' · ' : '') + it.age));
        ul.appendChild(li);
      });
      if (items.length) tile.appendChild(ul);
      else tile.appendChild(el('div', 't-empty', kind === 'fault' ? 'Keine offenen Störungen' : 'Keine offenen Tickets'));
      var rest = Math.max(0, total - items.length);
      if (rest) tile.appendChild(el('div', 't-more', '… und ' + rest + ' weitere'));
    };
    var tileFor = function (kind, items, total) {
      var t = el('section', 't-tile ' + (kind === 'fault' ? 'faults' : 'tickets'));
      var h = el('h2', null, kind === 'fault' ? 'Offene Störungen ' : 'Offene Tickets ');
      h.appendChild(el('span', 'n' + (total ? ' has' : ''), String(total)));
      t.appendChild(h);
      fillList(t, kind, items, total);
      return t;
    };
    // Statistikfolie: Kennzahlen und Säulendiagramm (SVG, Beschriftungen nur über textContent).
    var SVGNS = 'http://www.w3.org/2000/svg';
    var svgEl = function (tag, attrs, text) {
      var e = document.createElementNS(SVGNS, tag);
      Object.keys(attrs || {}).forEach(function (k) { e.setAttribute(k, String(attrs[k])); });
      if (text !== undefined) e.textContent = text;
      return e;
    };
    var drawChart = function (st) {
      var labels = st.labels || [], series = st.series || [];
      // Bei Rüstzeiten (Einheit min) zeigt das Diagramm nur die Anzahl; die Minuten stehen bei den Kennzahlen.
      if (st.unit === 'min') series = series.slice(0, 1);
      var W = 1000, H = 420, L = 50, B = 50, T = 20, R = 10;
      var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, preserveAspectRatio: 'xMidYMid meet', 'class': 'g-chart', role: 'img' });
      var max = 1;
      series.forEach(function (se) { (se.values || []).forEach(function (v) { if (v > max) max = v; }); });
      max = Math.ceil(max);
      var plotH = H - T - B, plotW = W - L - R, n = labels.length || 1;
      for (var i = 0; i <= 4; i++) {
        var y = T + plotH - plotH * i / 4;
        svg.appendChild(svgEl('line', { x1: L, x2: W - R, y1: y, y2: y, 'class': 'g-grid' }));
        svg.appendChild(svgEl('text', { x: L - 8, y: y + 6, 'text-anchor': 'end', 'class': 'g-axis' }, String(Math.round(max * i / 4 * 10) / 10)));
      }
      var slot = plotW / n, bw = Math.min(60, slot / (series.length + 1));
      labels.forEach(function (lb, li) {
        var cx = L + slot * li + slot / 2;
        svg.appendChild(svgEl('text', { x: cx, y: H - 16, 'text-anchor': 'middle', 'class': 'g-axis' }, lb));
        series.forEach(function (se, si) {
          var v = (se.values || [])[li] || 0, bh = plotH * v / max;
          var x = cx - bw * series.length / 2 + bw * si;
          svg.appendChild(svgEl('rect', { x: x, y: T + plotH - bh, width: Math.max(bw - 4, 2), height: bh, 'class': 'g-bar s' + si }));
        });
      });
      return svg;
    };
    var drawStats = function (s) {
      var st = s.stats || { numbers: [], labels: [], series: [] };
      var box = el('div', 'g-stats');
      if (s.title) box.appendChild(el('h1', null, s.title));
      var nums = el('div', 'g-nums');
      (st.numbers || []).forEach(function (n) {
        var c = el('div', 'g-num');
        c.appendChild(el('b', null, n.value));
        c.appendChild(el('span', null, n.label));
        nums.appendChild(c);
      });
      box.appendChild(nums);
      var wrap = el('div', 'g-chartbox');
      wrap.appendChild(drawChart(st));
      var leg = el('div', 'g-legend');
      var legSeries = st.unit === 'min' ? (st.series || []).slice(0, 1) : (st.series || []);
      legSeries.forEach(function (se, i) { leg.appendChild(el('span', 'k s' + i, se.name)); });
      wrap.appendChild(leg);
      if (Array.isArray(st.pies) && st.pies.length) {
        // Rüstzeiten: Säulen und Kreisdiagramm nebeneinander
        var row = el('div', 'g-statrow');
        row.appendChild(wrap);
        row.appendChild(drawPieSet(st.pies));
        box.appendChild(row);
      } else {
        box.appendChild(wrap);
      }
      return box;
    };
    // Planungsfolie (Schichtplan, Abwesenheiten, Dienste, Wartung): je Tag eine Spalte mit Gruppen und Einträgen.
    // Alles über textContent; die Farbe einer Gruppe nur, wenn sie ein Hexwert ist.
    var PLAN_TITLES = { plan_shifts: 'Schichtplan', plan_absences: 'Abwesenheiten', plan_duties: 'Dienste und Bereitschaft', plan_maint: 'Wartungsplanung' };
    var drawPlan = function (s) {
      var pl = s.plan || { days: [] };
      var box = el('div', 'g-plan');
      box.appendChild(el('h1', null, (s.title || PLAN_TITLES[s.kind] || 'Planung') + (pl.label ? ' – ' + pl.label : '')));
      var days = Array.isArray(pl.days) ? pl.days : [];
      var any = days.some(function (d) { return d && Array.isArray(d.groups) && d.groups.length > 0; });
      if (!any) {
        box.appendChild(el('div', 'g-plan-empty', 'Keine Einträge'));
        return box;
      }
      var grid = el('div', 'g-plan-days' + (days.length === 1 ? ' one' : ''));
      days.forEach(function (d) {
        var col = el('section', 'g-plan-day' + (d.today ? ' today' : ''));
        col.appendChild(el('h2', null, String(d.label || '')));
        var groups = Array.isArray(d.groups) ? d.groups : [];
        if (!groups.length) col.appendChild(el('div', 'g-plan-none', '–'));
        groups.forEach(function (g) {
          var gr = el('div', 'g-plan-group');
          var gh = el('h3', null, String(g.name || ''));
          if (typeof g.color === 'string' && /^#[0-9a-fA-F]{6}$/.test(g.color)) gh.style.borderLeftColor = g.color;
          gr.appendChild(gh);
          (Array.isArray(g.items) ? g.items : []).forEach(function (it) { gr.appendChild(el('div', 'g-plan-item', String(it))); });
          if (g.more > 0) gr.appendChild(el('div', 'g-plan-item more', '+ ' + g.more + ' weitere'));
          col.appendChild(gr);
        });
        grid.appendChild(col);
      });
      box.appendChild(grid);
      return box;
    };
    // Aufgabenfolie: überfällige offene Aufgaben oben, darunter je Woche und Tag. Nur textContent; die Farbe einer
    // Aufgabe nur, wenn sie ein Hexwert ist.
    var drawTasks = function (s) {
      var tk = s.tasks || {};
      var box = el('div', 'g-tk');
      box.appendChild(el('h1', null, (s.title || 'Aufgaben') + (tk.label ? ' – ' + tk.label : '')));
      var overdue = Array.isArray(tk.overdue) ? tk.overdue : [];
      var weeks = Array.isArray(tk.weeks) ? tk.weeks : [];
      var any = overdue.length > 0 || weeks.some(function (w) { return w && Array.isArray(w.days) && w.days.length > 0; });
      if (!any) {
        box.appendChild(el('div', 'g-plan-empty', 'Keine offenen Aufgaben'));
        return box;
      }
      var item = function (it, late) {
        var row = el('div', 'g-tk-item' + (late ? ' late' : ''));
        if (typeof it.color === 'string' && /^#[0-9a-fA-F]{6}$/.test(it.color)) row.style.borderLeftColor = it.color;
        var head = el('div', 'g-tk-title');
        if (late) head.appendChild(el('span', 'g-tk-badge', 'überfällig'));
        head.appendChild(document.createTextNode(String(it.title || '')));
        row.appendChild(head);
        var meta = [];
        if (it.plant) meta.push(String(it.plant));
        if (Array.isArray(it.assignees) && it.assignees.length) meta.push(it.assignees.map(String).join(', '));
        if (late && it.due) meta.push('fällig ' + String(it.due));
        if (it.status_label) meta.push(String(it.status_label));
        if (meta.length) row.appendChild(el('div', 'g-tk-meta', meta.join(' · ')));
        return row;
      };
      var cols = el('div', 'g-tk-cols');
      if (overdue.length) {
        var ob = el('section', 'g-tk-block late');
        ob.appendChild(el('h2', null, 'Überfällig'));
        overdue.forEach(function (it) { ob.appendChild(item(it, true)); });
        if (tk.overdue_more > 0) ob.appendChild(el('div', 'g-tk-meta', '+ ' + tk.overdue_more + ' weitere'));
        cols.appendChild(ob);
      }
      weeks.forEach(function (w) {
        var wb = el('section', 'g-tk-block');
        wb.appendChild(el('h2', null, String(w.label || '')));
        (Array.isArray(w.days) ? w.days : []).forEach(function (d) {
          wb.appendChild(el('h3', d.today ? 'today' : null, String(d.label || '')));
          (Array.isArray(d.items) ? d.items : []).forEach(function (it) { wb.appendChild(item(it, false)); });
        });
        cols.appendChild(wb);
      });
      if (tk.more > 0) cols.appendChild(el('div', 'g-tk-meta', '+ ' + tk.more + ' weitere'));
      box.appendChild(cols);
      return box;
    };
    // Projektfolie: Jahresband mit Monatsspalten, je Projekt ein Balken von Start bis Ende, Linie für heute. Die Seiten
    // (höchstens 14 Balken) wechseln innerhalb der Anzeigedauer der Folie; die Seite bleibt beim Neuzeichnen erhalten.
    // Alles über textContent; Zahlen werden auf 0 bis 100 begrenzt, Farben nur als Hexwert.
    var yearState = { id: null, page: 0 };
    var pct = function (v) { v = Number(v); return isFinite(v) ? Math.max(0, Math.min(100, v)) : 0; };
    var drawYear = function (s) {
      var yr = s.year || {};
      var box = el('div', 'g-yr');
      box.appendChild(el('h1', null, (s.title || 'Projekte') + (yr.label ? ' – ' + yr.label : '')));
      var pages = Array.isArray(yr.pages) ? yr.pages : [];
      if (!pages.length) {
        box.appendChild(el('div', 'g-plan-empty', 'Keine Projekte'));
        return box;
      }
      if (yearState.id !== s.id) { yearState.id = s.id; yearState.page = 0; }
      var body = el('div', 'g-yr-body');
      var info = el('div', 'g-yr-info');
      box.appendChild(body);
      box.appendChild(info);
      var months = Array.isArray(yr.months) ? yr.months : [];
      var paint = function () {
        body.textContent = '';
        var page = pages[yearState.page % pages.length] || { bars: [] };
        var bars = Array.isArray(page.bars) ? page.bars.slice(0, 14) : [];
        body.style.gridTemplateRows = 'auto repeat(' + Math.max(bars.length, 1) + ', minmax(0, 1fr))';
        var head = el('div', 'g-yr-months');
        head.style.gridColumn = '2';
        head.style.gridRow = '1';
        months.forEach(function (m) {
          var c = el('span', 'g-yr-month', String(m.label || ''));
          c.style.left = pct(m.l) + '%';
          c.style.width = pct(m.w) + '%';
          head.appendChild(c);
        });
        body.appendChild(head);
        var plot = el('div', 'g-yr-plot');
        plot.style.gridColumn = '2';
        plot.style.gridRow = '2 / -1';
        months.forEach(function (m) {
          var ln = el('i', 'g-yr-line');
          ln.style.left = pct(m.l) + '%';
          plot.appendChild(ln);
        });
        if (typeof yr.today === 'number') {
          var td = el('i', 'g-yr-today');
          td.style.left = pct(yr.today) + '%';
          plot.appendChild(td);
        }
        body.appendChild(plot);
        bars.forEach(function (b, i) {
          var nm = el('div', 'g-yr-name');
          nm.style.gridColumn = '1';
          nm.style.gridRow = String(i + 2);
          nm.appendChild(el('b', null, String(b.name || '')));
          nm.appendChild(el('small', null, String(b.status_label || '')));
          body.appendChild(nm);
          var tr = el('div', 'g-yr-track');
          tr.style.gridColumn = '2';
          tr.style.gridRow = String(i + 2);
          var bar = el('div', 'g-yr-bar' + (b.clip_l ? ' cl' : '') + (b.clip_r ? ' cr' : ''), String(b.start || '') + ' – ' + String(b.end || ''));
          bar.style.left = pct(b.l) + '%';
          bar.style.width = pct(b.w) + '%';
          if (typeof b.color === 'string' && /^#[0-9a-fA-F]{6}$/.test(b.color)) bar.style.background = b.color;
          tr.appendChild(bar);
          body.appendChild(tr);
        });
        info.textContent = '';
        (Array.isArray(yr.legend) ? yr.legend : []).forEach(function (l) {
          var k = el('span', 'k', String(l.label || ''));
          if (typeof l.color === 'string' && /^#[0-9a-fA-F]{6}$/.test(l.color)) k.style.setProperty('--k', l.color);
          info.appendChild(k);
        });
        if (pages.length > 1) info.appendChild(el('span', 'pg', 'Seite ' + ((yearState.page % pages.length) + 1) + ' von ' + pages.length));
      };
      paint();
      if (pages.length > 1) {
        var sec = typeof yr.page_seconds === 'number' && yr.page_seconds >= 1 ? yr.page_seconds : 5;
        reloadTimer = setInterval(function () { yearState.page = (yearState.page + 1) % pages.length; paint(); }, sec * 1000);
      }
      return box;
    };
    // Eine Folie anzeigen (nur textContent, nie HTML aus den Daten).
    var draw = function (s) {
      if (reloadTimer) { clearInterval(reloadTimer); reloadTimer = null; }
      stage.textContent = ''; // entfernt auch einen Website-Rahmen: er lädt nur, solange seine Folie läuft
      if (!s) {
        var e = el('div', 'g-empty', 'Keine Folien eingerichtet');
        e.appendChild(el('small', null, pdhName));
        stage.appendChild(e);
        return;
      }
      if (s.kind === 'image') {
        if (s.image_url) { var img = el('img', 'g-img'); img.alt = ''; img.src = s.image_url; stage.appendChild(img); }
        if (s.title) stage.appendChild(el('div', 'g-cap', s.title));
      } else if (s.kind === 'grafana') {
        if (typeof s.url === 'string' && /^(https:\/\/|\/grafana\/|http:\/\/(localhost|127\.0\.0\.1)([:\/]|$))/.test(s.url)) {
          var fr = el('iframe', 'g-frame');
          fr.title = s.title || 'Dashboard';
          fr.setAttribute('referrerpolicy', 'no-referrer');
          fr.src = s.url;
          stage.appendChild(fr);
        }
        if (s.title) stage.appendChild(el('div', 'g-cap', s.title));
      } else if (s.kind === 'website') {
        var wu = websiteFrameUrl(s.url, location.origin);
        if (wu) {
          var wf = el('iframe', 'g-frame g-web');
          wf.title = s.title || 'Website';
          wf.setAttribute('sandbox', 'allow-scripts allow-same-origin');
          wf.setAttribute('referrerpolicy', 'no-referrer');
          wf.setAttribute('allow', '');
          wf.src = wu;
          stage.appendChild(wf);
          stage.appendChild(el('div', 'g-shield')); // fängt Berührungen: die Website ist nicht bedienbar, Tippen blättert weiter
          if (typeof s.reload === 'number' && s.reload > 0) reloadTimer = setInterval(function () { wf.src = wu; }, s.reload * 60000);
        }
        if (s.title) stage.appendChild(el('div', 'g-cap', s.title));
      } else if (s.kind === 'stats_faults' || s.kind === 'stats_tickets' || s.kind === 'stats_setup' || s.kind === 'stats_downtime') {
        stage.appendChild(drawStats(s));
      } else if (typeof s.kind === 'string' && /^plan_/.test(s.kind)) {
        stage.appendChild(drawPlan(s));
      } else if (s.kind === 'tasks') {
        stage.appendChild(drawTasks(s));
      } else if (s.kind === 'projects_year') {
        stage.appendChild(drawYear(s));
      } else if (s.kind === 'vorgaenge') {
        var g = el('div', 'g-vorg');
        var f = s.faults || [], t = s.tickets || [];
        var hasF = f.length > 0 || (s.faults_total || 0) > 0, hasT = t.length > 0 || (s.tickets_total || 0) > 0;
        // Eine Art ohne jeden Eintrag nur zeigen, wenn auch die andere leer ist (dann beide, mit Leer-Hinweis)
        if (hasF || !hasT) g.appendChild(tileFor('fault', f, s.faults_total || 0));
        if (hasT || !hasF) g.appendChild(tileFor('ticket', t, s.tickets_total || 0));
        if (g.children.length === 1) g.className += ' one';
        stage.appendChild(g);
      } else {
        var box = el('div', 'g-text');
        if (s.title) box.appendChild(el('h1', null, s.title));
        if (s.body) box.appendChild(el('p', null, s.body));
        stage.appendChild(box);
      }
    };
    // Keine Verbindung zu PDH (oder zum Netz): Website-Folien werden übersprungen statt als Fehler gezeigt.
    var isOffline = function () { return navigator.onLine === false || loadFails > 0 || streamFails > 0; };
    var skipWebsiteIfOffline = function () {
      if (cur >= 0 && slides[cur] && (slides[cur].kind === 'website' || /^(plan_.*|tasks|projects_year)$/.test(String(slides[cur].kind))) && isOffline()) show(nextSlideIndex(slides, cur));
    };
    var show = function (i) {
      if (slideTimer) { clearTimeout(slideTimer); slideTimer = null; }
      if (slides.length) {
        var ok = showableIndex(slides, i, isOffline());
        if (ok < 0) { // nur Website-Folien und keine Verbindung: ruhiger Bildschirm, später erneut versuchen
          cur = -1;
          draw({ kind: 'text', title: pdhName });
          slideTimer = setTimeout(function () { show(0); }, 10000);
          return;
        }
        i = ok;
      }
      cur = slides.length ? i : -1;
      yearState.id = null; // Projektfolie beginnt beim Erscheinen mit der ersten Seite
      draw(slides.length ? slides[cur] : null);
      if (slides.length) {
        slideTimer = setTimeout(function () { show(nextSlideIndex(slides, cur)); }, slideDurationMs(slides[cur]));
        if (slides[cur].kind === 'vorgaenge' || /^(stats_|plan_)/.test(slides[cur].kind) || slides[cur].kind === 'tasks' || slides[cur].kind === 'projects_year') load(true); // Liste beim Erscheinen neu holen
      }
    };

    // Zwischenspeicher füllen: Bilder einzeln holen, base64, dann an das lokale Terminal senden.
    var toB64 = function (blob) {
      return new Promise(function (res, rej) {
        var fr = new FileReader();
        fr.onload = function () { var s = String(fr.result); res(s.slice(s.indexOf(',') + 1)); };
        fr.onerror = function () { rej(fr.error); };
        fr.readAsDataURL(blob);
      });
    };
    var saveCache = function (json) {
      if (saver || !cacheOk || json.version === cachedVersion) return; // nur globale Terminals legen den Zwischenspeicher an
      if (caching) { again = true; return; }
      caching = true;
      var imgs = {}, total = 0;
      var chain = Promise.resolve();
      json.slides.forEach(function (s) {
        if (s.kind !== 'image' || !s.image_url) return;
        chain = chain.then(function () {
          if (total > 50 * 1024 * 1024) return null;
          return fetch(s.image_url, { credentials: 'same-origin' })
            .then(function (r) { return r.ok ? r.blob() : null; })
            .then(function (b) { if (!b || b.size > 5 * 1024 * 1024) return null; total += b.size; return toB64(b); })
            .then(function (b64) { if (b64) imgs[s.id] = b64; })
            .catch(function () { /* Bild fehlt im Zwischenspeicher */ });
        });
      });
      chain.then(function () {
        cachedVersion = json.version;
        cacheCall('POST', cachePayload(json, imgs));
      }).catch(function () { /* stumm */ }).then(function () {
        caching = false;
        if (again) { again = false; load(true); }
      });
    };

    // slides.json laden. keep: die laufende Folie bleibt stehen (Timer unberührt), nur ihr Inhalt wird erneuert.
    var loadFails = 0, streamFails = 0, left = false;
    // Netzfehler zählen (slides.json und Ereignisstrom getrennt); ab OUTAGE_LIMIT in Folge zur Startseite des
    // Terminals: sie zeigt offline die zwischengespeicherte Folienschau und kehrt nach PDH zurück, sobald es antwortet.
    var leaveOnOutage = function (st) {
      if (!saver && st.leave && !left) { left = true; location.replace(CARD_ORIGIN + '/start'); }
    };
    var load = function (keep) {
      fetch('/terminal/slides.json', { cache: 'no-store', credentials: 'same-origin' })
        .then(function (r) {
          loadFails = outageStep(loadFails, false).fails; // PDH hat geantwortet
          // Kein globales Terminal mehr (403) oder Sitzung weg: Seite neu laden, sie wird zum normalen Terminal
          if (r.status === 403 || r.redirected) { location.reload(); return null; }
          if (!r.ok) return null; // vorübergehender Fehler: alter Stand bleibt
          return r.json();
        })
        .then(function (d) {
          if (!d || !Array.isArray(d.slides)) return;
          // Die Seite trägt ihre frame-src-Regel im Kopf; kommt für die Folien eine andere, neu laden.
          if (typeof d.frame_csp === 'string' && stage && stage.dataset.frameCsp && d.frame_csp !== stage.dataset.frameCsp) { location.reload(); return; }
          var same = d.version === version;
          var curId = cur >= 0 && slides[cur] ? slides[cur].id : null;
          slides = d.slides; version = d.version;
          if (!same || cur < 0 || !keep) {
            var idx = -1;
            if (keep && curId) for (var k = 0; k < slides.length; k++) if (slides[k].id === curId) { idx = k; break; }
            if (keep && idx >= 0) { cur = idx; draw(slides[cur]); } // gleiche Folie weiter, nur neu zeichnen
            else show(0);
          } else if (cur >= 0 && slides[cur] && (/^(stats|plan)_/.test(slides[cur].kind) || slides[cur].kind === 'tasks' || slides[cur].kind === 'projects_year')) {
            draw(slides[cur]); // Zahlen der laufenden Statistik-, Planungs-, Aufgaben- bzw. Projektfolie erneuern; die Reihenfolge bleibt
          }
          saveCache(d);
        })
        .catch(function () { // offline: alter Stand bleibt, nach mehreren Fehlern in Folge Folienschau des Terminals
          var st = outageStep(loadFails, true); loadFails = st.fails; leaveOnOutage(st);
          skipWebsiteIfOffline();
        });
    };

    // Eingabe: Tippen auf freie Fläche oder Wischen nach links blättert weiter, ein Vorgang öffnet seine Ansicht.
    var down = null;
    stage.addEventListener('pointerdown', function (ev) { down = { x: ev.clientX, y: ev.clientY }; });
    stage.addEventListener('pointercancel', function () { down = null; });
    stage.addEventListener('pointerup', function (ev) {
      if (saver) { // Schoner: nur ein Vorgang öffnet seine Ansicht, jede andere Berührung weckt das Terminal
        var sli = ev.target && ev.target.closest ? ev.target.closest('[data-kind][data-id]') : null;
        var sk = sli ? sli.getAttribute('data-kind') : '', sid = sli ? sli.getAttribute('data-id') : '';
        if ((sk === 'fault' || sk === 'ticket') && /^[0-9a-fA-F-]{36}$/.test(sid)) location.href = '/terminal/vorgang/' + sk + '/' + sid;
        else location.replace('/terminal/info');
        return;
      }
      var st = down; down = null;
      if (!st || !slides.length) return;
      var end = { x: ev.clientX, y: ev.clientY };
      if (isSwipeLeft(st, end)) { show(nextSlideIndex(slides, cur)); return; }
      if (Math.abs(end.x - st.x) > 20 || Math.abs(end.y - st.y) > 20) return; // Ziehen ohne Wischen: nichts
      var li = ev.target && ev.target.closest ? ev.target.closest('[data-kind][data-id]') : null;
      if (li) {
        var kind = li.getAttribute('data-kind'), id = li.getAttribute('data-id');
        if ((kind === 'fault' || kind === 'ticket') && /^[0-9a-fA-F-]{36}$/.test(id)) location.href = '/terminal/vorgang/' + kind + '/' + id;
        return;
      }
      show(nextSlideIndex(slides, cur));
    });

    // Änderungen: Ereignis „slides“ lädt neu, „settings“ mit global_screen aus macht die Seite zum normalen Terminal.
    try {
      var gev = new EventSource('/terminal/events');
      gev.addEventListener('slides', function () { load(true); });
      gev.addEventListener('settings', function (ev) {
        var d = null;
        try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
        if (d && ((saver ? (d.global_screen === true || d.saver_slides === false) : d.global_screen === false) || fnChanged(d))) location.reload();
      });
      gev.onopen = function () { streamFails = outageStep(streamFails, false).fails; };
      gev.onerror = function () { // der EventSource verbindet selbst neu; viele Fehler in Folge = PDH nicht erreichbar
        var st = outageStep(streamFails, true); streamFails = st.fails; leaveOnOutage(st);
        skipWebsiteIfOffline();
      };
    } catch (e) { /* kein EventSource */ }
    window.addEventListener('offline', skipWebsiteIfOffline);
    setInterval(function () { load(true); }, 60000); // Vorgänge und Version auch ohne Ereignis aktuell halten
    load(false);
  }
})();
