// Anlagenterminal (terminal_page.go): Uhr, Rückkehr zum Schoner, Aktualisierung der Störungsinfo.
(function () {
  'use strict';
  var body = document.body;
  var mode = body.dataset.mode;

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

  // Kartenleser: der Kartendienst auf dem Pi (nur Gerätesitzung, data-card-events) meldet
  // gelesene Karten per Server-Sent Events; die Seite reicht sie an PDH weiter.
  if (body.dataset.cardEvents === '1' && mode !== 'schoner') {
    var bar = document.querySelector('[data-card-bar]');
    var nameEl = document.querySelector('[data-card-name]');
    var msgEl = document.querySelector('[data-card-msg]');
    var logoutBtn = document.querySelector('[data-card-logout]');
    if (bar && nameEl && msgEl && logoutBtn) {
      var loggedIn = false, lastPing = 0, msgTimer = null, gen = 0;
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
      try {
        var es = new EventSource('http://127.0.0.1:8080/api/card/events');
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
