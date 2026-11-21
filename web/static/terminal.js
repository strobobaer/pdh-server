// Anlagenterminal (terminal_page.go): Uhr, Rückkehr zum Schoner, Aktualisierung der Störungsinfo.
(function () {
  'use strict';
  var body = document.body;
  var mode = body.dataset.mode;

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

  // Uhr (nur Anzeige)
  var clocks = document.querySelectorAll('[data-clock]');
  function tick() {
    var t = new Date().toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });
    for (var i = 0; i < clocks.length; i++) clocks[i].textContent = t;
  }
  if (clocks.length) { tick(); setInterval(tick, 5000); }

  // Inaktivität: keine Berührung/Taste für data-idle Sekunden → zurück zum Schoner.
  // Auf dem Schoner selbst (data-idle="0") gibt es keinen Timer.
  var idle = parseInt(body.dataset.idle, 10) || 0;
  if (mode !== 'schoner' && idle > 0) {
    var timer = null;
    var reset = function () {
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { location.replace('/terminal'); }, idle * 1000);
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
      var s = mk('span', 'hs ' + (h.Running ? 'hs-run' : 'hs-stop'));
      s.appendChild(mk('span', 'hs-label', h.Label));
      s.appendChild(mk('code', 'hs-count', h.Count));
      s.appendChild(mk('span', 'hs-state', h.Running ? 'läuft' : (h.Since ? 'steht seit ' + h.Since : 'kein Lebenszeichen')));
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
            if (d.ok) { msgEl.hidden = true; showBar(d.name); lastPing = Date.now(); }
            else if (d.message) showMsg(d.message);
          })
          .catch(function () { /* offline: ignorieren */ });
      };
      logoutBtn.addEventListener('click', function () {
        gen++;
        showBar('');
        post('/terminal/card/abmelden').catch(function () {});
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
            if (typeof d.manual_login === 'string') settings.manual_login = d.manual_login;
            if (typeof d.password_button === 'boolean') settings.password_button = d.password_button;
            // neue Rückkehrzeit gilt ab dem nächsten Zurücksetzen des Timers
            if (typeof d.idle === 'number' && d.idle > 0) { idle = d.idle; settings.idle = d.idle; }
            dismissed = false;
            render();
          });
          sset.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
        } catch (e) { /* kein EventSource */ }
        render();
      }

      try {
        var es = new EventSource('http://127.0.0.1:8080/api/card/events');
        es.addEventListener('reader', function (ev) {
          var d = null;
          try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
          if (d && typeof d.state === 'string') onReader(d.state);
        });
        es.addEventListener('card', function (ev) {
          var d = null;
          try { d = JSON.parse(ev.data); } catch (e) { /* ungültig */ }
          if (d && typeof d.uid === 'string' && d.uid) {
            // Der Pi spielt beim Verbinden ein bis zu 5 s altes Ereignis erneut ab (jede Seitennavigation).
            // Dasselbe Ereignis (uid + at) nur einmal verarbeiten, sonst wird eine Karte doppelt gemeldet
            // bzw. nach "Abmelden" + Navigation die Person wieder angemeldet.
            var evKey = d.uid + '|' + String(d.at);
            var seen = null;
            try { seen = sessionStorage.getItem('pdhLastCardEvent'); } catch (e) { /* kein Speicher */ }
            if (seen === evKey) return;
            try { sessionStorage.setItem('pdhLastCardEvent', evKey); } catch (e) { /* kein Speicher */ }
            onCard(d.uid);
          }
        });
        es.onerror = function () { /* stumm: der EventSource verbindet selbst neu */ };
      } catch (e) { /* kein Kartendienst */ }
    }
  }
})();
