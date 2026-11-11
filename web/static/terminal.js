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
})();
