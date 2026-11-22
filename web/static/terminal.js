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
  // Wischgeste nach links: mindestens 60 px waagerecht, weniger als 40 px senkrecht.
  function isSwipeLeft(start, end) {
    if (!start || !end) return false;
    var dx = start.x - end.x, dy = Math.abs(start.y - end.y);
    return dx >= 60 && dy < 40;
  }
  // Folienarten, die ältere Terminals kennen. Neuere Arten (Statistik, Grafana) bleiben aus dem
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
      timer = setTimeout(function () { if (mode === 'vorgang') leaveVorgang(); else location.replace('/terminal'); }, idle * 1000);
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
  }

  // Gerätesitzung ohne Anmeldeteil (Schoner, Seite „keine Anlage“): auf Einstellungen hören. Wird das Terminal
  // zum globalen, lädt die Seite neu und wird zur Folienschau. (Info und Neu tun das im Anmeldeteil.)
  if (body.dataset.settingsEvents === '1' && mode !== 'global' && !saver && (body.dataset.cardEvents !== '1' || mode === 'schoner')) {
    try {
      var sev = new EventSource('/terminal/events');
      sev.addEventListener('settings', function (ev) {
        var d = null;
        try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
        if (d && (d.global_screen === true || (mode === 'schoner' && d.saver_slides === true))) location.reload();
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
  if (mode === 'neu' || mode === 'rusten' || mode === 'stillstand' || mode === 'melden' || mode === 'hmi' || mode === 'wartung') {
    document.addEventListener('pdh-card-login', function (e) { if (!!e.detail !== (body.dataset.cardIn === '1')) location.reload(); });
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
        var activeIn = cardIn, dialogOpen = false, dismissed = false;

        var render = function () {
          var view = loginView(reader, settings);
          if (loggedIn || view === 'card') { dialogOpen = false; dismissed = false; }
          if (loggedIn) clearInputs(); // Karte hat angemeldet: halb getippte Eingaben verwerfen
          var open = !loggedIn && (dialogOpen || (view === 'password' && !dismissed));
          var noReader = !loggedIn && view === 'none';
          var fields = !loggedIn && (view === 'card_pin' || view === 'card_only');
          var btn = !loggedIn && (showPasswordButton(reader, settings) || view === 'password');
          loginMsg.textContent = noReader ? 'Kein Kartenleser – Anmeldung nicht möglich' : '';
          loginMsg.hidden = !noReader;
          fieldsEl.hidden = !fields;
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
              if (d && d.ok) { good = true; msgEl.hidden = true; dialogOpen = false; showBar(d.name); lastPing = Date.now(); }
              else if (x.s === 429) showMsg((d && d.message) || 'Bitte kurz warten');
              else showMsg((d && d.message) || 'Anmeldung fehlgeschlagen');
            })
            .catch(function () { if (g === gen) showMsg('Anmeldung fehlgeschlagen'); }) // Netzfehler
            .then(function () { clearInputs(!good); render(); }); // Eingaben nach jedem Versuch leeren; blurren nur nach Erfolg
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
              submit({ kind: 'card_pin', card: card, pin: pinIn.value });
            } else if (view === 'card_only') {
              submit({ kind: 'card_only', card: card });
            }
            return;
          }
          if (f === pinIn && !/^[0-9]$/.test(k)) return; // PIN nur Ziffern
          if (f.value.length < (f === pinIn ? 8 : 32)) f.value += k;
        });

        // Dialog Benutzer/Passwort
        pwBtn.addEventListener('click', function () { dialogOpen = true; dismissed = false; render(); userIn.focus(); });
        dialog.querySelector('[data-login-cancel]').addEventListener('click', function () {
          dialogOpen = false; dismissed = true; clearInputs(); render();
        });
        dForm.addEventListener('submit', function (ev) {
          ev.preventDefault();
          var u = userIn.value.trim(), p = passIn.value;
          if (!u || !p) { showMsg('Anmeldung fehlgeschlagen'); clearInputs(true); return; }
          submit({ kind: 'password', user: u, password: p });
        });

        onReader = function (state) {
          reader = (state === 'none' || state === 'error') ? state : 'ok';
          dismissed = false;
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
            if (typeof d.manual_login === 'string') settings.manual_login = d.manual_login;
            if (typeof d.password_button === 'boolean') settings.password_button = d.password_button;
            // neue Rückkehrzeit gilt ab dem nächsten Zurücksetzen des Timers
            if (typeof d.idle === 'number' && d.idle > 0) { idle = d.idle; settings.idle = d.idle; }
            if (Array.isArray(d.card_notify)) notify = parseNotify(JSON.stringify(d.card_notify));
            dismissed = false;
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
      box.appendChild(wrap);
      return box;
    };
    // Eine Folie anzeigen (nur textContent, nie HTML aus den Daten).
    var draw = function (s) {
      stage.textContent = '';
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
      } else if (s.kind === 'stats_faults' || s.kind === 'stats_tickets' || s.kind === 'stats_setup' || s.kind === 'stats_downtime') {
        stage.appendChild(drawStats(s));
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
    var show = function (i) {
      if (slideTimer) { clearTimeout(slideTimer); slideTimer = null; }
      cur = slides.length ? i : -1;
      draw(slides.length ? slides[cur] : null);
      if (slides.length) {
        slideTimer = setTimeout(function () { show(nextSlideIndex(slides, cur)); }, slideDurationMs(slides[cur]));
        if (slides[cur].kind === 'vorgaenge' || /^stats_/.test(slides[cur].kind)) load(true); // Liste beim Erscheinen neu holen
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
          } else if (cur >= 0 && slides[cur] && /^stats_/.test(slides[cur].kind)) {
            draw(slides[cur]); // Zahlen der laufenden Statistikfolie erneuern; die Reihenfolge bleibt
          }
          saveCache(d);
        })
        .catch(function () { // offline: alter Stand bleibt, nach mehreren Fehlern in Folge Folienschau des Terminals
          var st = outageStep(loadFails, true); loadFails = st.fails; leaveOnOutage(st);
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
        if (d && (saver ? (d.global_screen === true || d.saver_slides === false) : d.global_screen === false)) location.reload();
      });
      gev.onopen = function () { streamFails = outageStep(streamFails, false).fails; };
      gev.onerror = function () { // der EventSource verbindet selbst neu; viele Fehler in Folge = PDH nicht erreichbar
        var st = outageStep(streamFails, true); streamFails = st.fails; leaveOnOutage(st);
      };
    } catch (e) { /* kein EventSource */ }
    setInterval(function () { load(true); }, 60000); // Vorgänge und Version auch ohne Ereignis aktuell halten
    load(false);
  }
})();
