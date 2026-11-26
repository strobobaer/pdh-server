// Neuer Rahmen (0.90.0 Teil 2) – nur von frame.gohtml geladen. Bereiche, Menüband,
// Befehlszeile Strg+K und Statusleiste; Werte aus window.PDH_BOOT, Texte über pdhT
// (Datenblock pdh-boot in widgets/frame_shared.gohtml). Aktionen des Menübands kommen
// als Namen aus ribbon.go (data-ribbon-action), nie als JavaScript-Text.
(function () {
  'use strict';
  var T = function (k) { return typeof window.pdhT === 'function' ? window.pdhT(k) : k; };
  var $ = function (s, el) { return (el || document).querySelector(s); };
  var $$ = function (s, el) { return [].slice.call((el || document).querySelectorAll(s)); };
  window.pdhFrame = window.pdhFrame || {};

  // ── Bereichsleiste ─────────────────────────────────────────────
  function railToggle(open) {
    var rail = $('.rail'), btn = $('[data-rail-more]');
    if (!rail || !btn) return;
    rail.classList.toggle('open', open);
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
  }
  try { if (localStorage.getItem('pdh_rail_narrow') === '1') document.documentElement.classList.add('rail-narrow'); } catch (e) {}

  // Reihenfolge der sichtbaren Bereiche (data-order). Favoriten schickt der Server
  // nicht mit: „Favorit“ sendet nur den einen Eintrag, ausgeblendete bleiben erhalten.
  function order() {
    var r = $('.rail');
    return ((r && r.dataset.order) || '').split(' ').filter(Boolean);
  }
  function save(url, body, reload, okMsg) {
    return fetch(url, { method: 'POST', credentials: 'same-origin', pdhQuiet: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { if (!r.ok || !j.success) throw new Error(j.error || ('HTTP ' + r.status)); }); })
      .then(function () {
        if (okMsg && typeof window.setGlobalStatus === 'function') window.setGlobalStatus('ok', okMsg);
        if (reload) location.reload();
      })
      .catch(function (e) {
        if (typeof window.setGlobalStatus === 'function') window.setGlobalStatus('error', T('Nicht gespeichert'), null, String(e && e.message || e));
      });
  }

  // Bereiche ordnen: kleiner Dialog mit Nach oben / Nach unten
  function openOrder() {
    var rail = $('.rail');
    if (!rail || $('.frame-dialog')) return;
    var items = $$('[data-area]', rail).map(function (a) { return { key: a.dataset.area, label: a.getAttribute('title') || a.dataset.area }; });
    var box = document.createElement('div');
    box.className = 'frame-dialog';
    box.setAttribute('role', 'dialog');
    box.setAttribute('aria-modal', 'true');
    box.setAttribute('aria-label', T('Bereiche ordnen'));
    var close = function () { box.remove(); };
    var draw = function () {
      box.innerHTML = '';
      var h = document.createElement('h2'); h.textContent = T('Bereiche ordnen'); box.appendChild(h);
      var ul = document.createElement('ol'); ul.className = 'frame-order';
      items.forEach(function (it, i) {
        var li = document.createElement('li');
        var s = document.createElement('span'); s.textContent = it.label; li.appendChild(s);
        [['up', 'ti-arrow-up', T('Nach oben'), -1], ['down', 'ti-arrow-down', T('Nach unten'), 1]].forEach(function (m) {
          var b = document.createElement('button');
          b.type = 'button'; b.className = 'btn btn-small'; b.title = m[2]; b.setAttribute('aria-label', m[2] + ': ' + it.label);
          b.innerHTML = '<i class="ti ' + m[1] + '"></i>';
          var j = i + m[3];
          b.disabled = j < 0 || j >= items.length;
          b.onclick = function () { var x = items[i]; items[i] = items[j]; items[j] = x; draw(); };
          li.appendChild(b);
        });
        ul.appendChild(li);
      });
      box.appendChild(ul);
      var bar = document.createElement('div'); bar.className = 'frame-dialog-bar';
      var firm = document.createElement('button'); firm.type = 'button'; firm.className = 'btn'; firm.classList.add('fd-firm'); firm.textContent = T('Wie Firmenvorgabe');
      firm.onclick = function () { save('/account/ui-areas', { reset_order: true }, true); };
      var cancel = document.createElement('button'); cancel.type = 'button'; cancel.className = 'btn'; cancel.textContent = T('Abbrechen'); cancel.onclick = close;
      var ok = document.createElement('button'); ok.type = 'button'; ok.className = 'btn btn-primary'; ok.textContent = T('Speichern');
      ok.onclick = function () { save('/account/ui-areas', { order: items.map(function (it) { return it.key; }) }, true); };
      bar.appendChild(firm); bar.appendChild(cancel); bar.appendChild(ok); box.appendChild(bar);
    };
    draw();
    box.addEventListener('keydown', function (e) { if (e.key === 'Escape') close(); });
    document.body.appendChild(box);
    var first = $('button:not([disabled])', box); if (first) first.focus();
  }

  // ── Menüband ───────────────────────────────────────────────────
  var ACTIONS = {
    'create': function (arg) { if (typeof window.pdhCreate === 'function') { if (arg) window.pdhCreate(arg); else window.pdhCreate(); } },
    // mit Wert: diesen Reiter der rechten Leiste öffnen (Copilot, Chat, Hilfe), sonst Leiste ein/aus
    'toggle-right': function (arg) {
      if (arg && typeof window.activateContextTab === 'function') window.activateContextTab(arg);
      else if (typeof window.toggleSidebar === 'function') window.toggleSidebar('right');
    },
    'search': function () { palOpen(''); },
    'share-chat': function () { if (typeof window.pdhShareToChat === 'function') window.pdhShareToChat(); },
    'override-login': function () { var m = document.getElementById('override-login-modal'); if (m) m.style.display = 'flex'; },
    'undo': function () { if (window.pdhEdit) window.pdhEdit.undo(); },
    'save-edit': function () { if (window.pdhEdit) window.pdhEdit.save(); },
    'cancel-edit': function () { if (window.pdhEdit) window.pdhEdit.cancel(); },
    'dash-reset': function () { if (typeof window.dwReset === 'function') window.dwReset(); },
    'addw': function () { if (typeof window.dwOpenCatalog === 'function') window.dwOpenCatalog(); },
    // Zeit ohne Vorgang braucht einen kurzen Text (Zielart „Sonstiges“, zeit_ziele.go)
    'time-start': function () {
      var txt = window.prompt(T('Woran arbeitest du?'));
      if (txt && txt.trim()) timePost('/time/start-web', { target_kind: 'other', description: txt.trim() }, T('Zeit läuft.'));
    },
    'time-stop': function (arg) { if (arg) timePost('/time/' + encodeURIComponent(arg) + '/stop-web', null, T('Zeit gestoppt und gebucht.')); },
    'time-manual': function () { var d = document.getElementById('frame-time-manual'); if (d && typeof d.showModal === 'function') d.showModal(); },
    'dash-standard': function () { confirmPost('/core/settings/defaults', { what: 'dashboard', action: 'set' }, T('Die jetzige Anordnung wird die Vorgabe der Firma. Wer seine Startseite schon angepasst hat, behält sie, bis er „Auf Vorgabe zurücksetzen“ drückt.'), T('Als Standard für alle gesetzt.')); },
    'leit-standard': function (arg) { if (arg) confirmPost('/leitstaende/' + encodeURIComponent(arg) + '/default', {}, T('Dieser Leitstand wird der Standard-Leitstand für alle.'), T('Als Standard für alle gesetzt.')); },
    'toggle-narrow': function () {
      var on = !document.documentElement.classList.contains('rail-narrow');
      document.documentElement.classList.toggle('rail-narrow', on);
      try { localStorage.setItem('pdh_rail_narrow', on ? '1' : ''); } catch (e) {}
    },
    'fullscreen': function () {
      if (document.fullscreenElement) document.exitFullscreen();
      else if (document.documentElement.requestFullscreen) document.documentElement.requestFullscreen();
    },
    'fav': function (arg) { save('/account/ui-areas', { fav: arg }, true); }, // anheften oder lösen
    'areas-order': function () { openOrder(); },
    'dashboard-edit': function () { if (typeof window.pdhDashboardEdit === 'function') window.pdhDashboardEdit(); },
    // nur die Reihenfolge – eigene Favoriten gehen nicht an alle
    'areas-firm': function () { save('/admin/gestaltung/areas', { order: order() }, false, T('Als Firmenvorgabe gespeichert')); },
    'style': function (arg) { if (typeof window.lookSet === 'function') { window.lookSet('style', arg); window.lookSave(); } },
    'mode': function (arg) { if (typeof window.applyThemeMode === 'function') window.applyThemeMode(arg); },
    'palette': function () { if (typeof window.toggleMenu === 'function') window.toggleMenu('user-menu'); },
    'density': function (arg) { if (typeof window.lookSet === 'function') { window.lookSet('row', parseInt(arg, 10)); window.lookSave(); } }
  };
  window.pdhFrame.run = function (action, arg) { var f = ACTIONS[action]; if (f) f(arg); };

  // Formular-POST (Zeiterfassung, Firmenvorgaben): danach neu laden, Meldung über die Statusleiste
  function formPost(url, fields) {
    var body = new URLSearchParams();
    Object.keys(fields || {}).forEach(function (k) { body.append(k, fields[k]); });
    return fetch(url, { method: 'POST', credentials: 'same-origin', pdhQuiet: true, body: body })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); });
  }
  function noteAfterReload(msg) { try { sessionStorage.setItem('pdh_frame_note', msg); } catch (e) {} }
  function failed(e) { if (typeof window.setGlobalStatus === 'function') window.setGlobalStatus('error', T('Nicht gespeichert'), null, String(e && e.message || e)); }
  function timePost(url, fields, okMsg) {
    formPost(url, fields).then(function () { noteAfterReload(okMsg); location.reload(); }).catch(failed);
  }
  function confirmPost(url, fields, question, okMsg) {
    if (!window.confirm(question)) return;
    formPost(url, fields).then(function () { noteAfterReload(okMsg); location.reload(); }).catch(failed);
  }
  window.pdhFrame.timePost = timePost;
  try {
    var note = sessionStorage.getItem('pdh_frame_note');
    if (note) { sessionStorage.removeItem('pdh_frame_note'); window.addEventListener('load', function () { if (typeof window.setGlobalStatus === 'function') window.setGlobalStatus('ok', note); }); }
  } catch (e) {}

  // Zeit manuell erfassen (frame-time-manual) und laufende Dauer an „Zeit stoppen“ (Entwurf 11.9).
  // Die Dauer zählt mit der Serverzeit der Uhr, nicht mit der Uhr des Geräts.
  document.addEventListener('submit', function (e) {
    var f = e.target;
    if (!f.matches || !f.matches('[data-time-manual]')) return;
    e.preventDefault();
    var fd = {};
    new FormData(f).forEach(function (v, k) { fd[k] = v; });
    timePost('/time/manual-web', fd, T('Gebucht.'));
  });
  document.addEventListener('click', function (e) {
    if (!e.target.closest('[data-time-close]')) return;
    var d = document.getElementById('frame-time-manual');
    if (d && d.open) d.close();
  });
  (function () {
    var c = document.getElementById('pdh-clock');
    var off = c ? (parseInt(c.dataset.serverMs, 10) || Date.now()) - Date.now() : 0;
    var two = function (n) { return (n < 10 ? '0' : '') + n; };
    function upd() {
      document.querySelectorAll('[data-since]').forEach(function (b) {
        var s = Date.parse(b.dataset.since);
        if (isNaN(s)) return;
        var sec = Math.max(0, Math.floor((Date.now() + off - s) / 1000));
        var sp = b.querySelector('span');
        if (sp) sp.textContent = T('Zeit stoppen') + ' ' + Math.floor(sec / 3600) + ':' + two(Math.floor(sec / 60) % 60) + ':' + two(sec % 60);
      });
    }
    upd();
    setInterval(upd, 1000);
  })();

  // Aufklappmenüs im Menüband (Neu, Darstellung, Anpassen)
  function menuClose(except) {
    $$('.ribbon-dd').forEach(function (dd) {
      if (dd === except) return;
      var m = $('.ribbon-menu', dd), b = $('[data-ribbon-menu]', dd);
      if (m) m.hidden = true;
      if (b) b.setAttribute('aria-expanded', 'false');
    });
  }
  function menuToggle(btn) {
    var dd = btn.closest('.ribbon-dd'), m = $('.ribbon-menu', dd), open = m.hidden;
    menuClose(dd);
    m.hidden = !open;
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) { // fest positioniert: das Band scrollt waagerecht und würde das Menü abschneiden
      var r = btn.getBoundingClientRect(), w = m.offsetWidth;
      m.style.top = Math.round(r.bottom + 2) + 'px';
      m.style.left = Math.round(Math.max(8, Math.min(r.left, window.innerWidth - w - 8))) + 'px';
    }
    if (open) { var first = $('.ribbon-item', m); if (first) first.focus(); }
  }

  // ── Befehlszeile Strg+K ────────────────────────────────────────
  // palSeq macht ausstehende Antworten ungültig; palLoading: neue Treffer kommen noch
  // (dann gibt es nichts zu wählen – Enter öffnet nie einen Treffer der vorigen Suche).
  var pal = null, palTimer = 0, palItems = [], palSel = 0, palSeq = 0, palLoading = false, palReturn = null;
  function palClose() {
    clearTimeout(palTimer); palSeq++; palItems = []; palSel = 0; palLoading = false;
    // Fokus nur zurückgeben, wenn er noch in der Befehlszeile steht (nicht, wenn jemand
    // daneben in ein Feld der Seite geklickt hat)
    var a = document.activeElement, back = !a || a === document.body || (pal && pal.contains(a));
    if (pal) { pal.remove(); pal = null; }
    if (back && palReturn && document.contains(palReturn) && typeof palReturn.focus === 'function') palReturn.focus();
    palReturn = null;
  }
  function palRender() {
    if (!pal) return;
    var ul = $('.fp-list', pal), inp = $('.fp-input', pal);
    ul.innerHTML = '';
    if (palLoading) { inp.removeAttribute('aria-activedescendant'); return; }
    if (!palItems.length) {
      var e = document.createElement('li'); e.className = 'fp-empty'; e.textContent = T('Keine Treffer'); ul.appendChild(e);
      inp.removeAttribute('aria-activedescendant');
      return;
    }
    palItems.forEach(function (it, i) {
      var li = document.createElement('li');
      li.id = 'fp-o' + i; li.setAttribute('role', 'option');
      li.className = 'fp-item fp-' + it.kind + (i === palSel ? ' on' : '');
      li.setAttribute('aria-selected', i === palSel ? 'true' : 'false');
      var l = document.createElement('span'); l.className = 'fp-label'; l.textContent = it.label; li.appendChild(l);
      if (it.hint) { var h = document.createElement('span'); h.className = 'fp-hint'; h.textContent = it.hint; li.appendChild(h); }
      li.addEventListener('mousedown', function (ev) { ev.preventDefault(); palGo(i); });
      ul.appendChild(li);
    });
    inp.setAttribute('aria-activedescendant', 'fp-o' + palSel);
  }
  function palQuery(q) {
    var seq = ++palSeq;
    fetch('/api/palette?q=' + encodeURIComponent(q), { credentials: 'same-origin', pdhQuiet: true })
      .then(function (r) { return r.ok ? r.json() : { items: [] }; })
      .then(function (j) { if (seq !== palSeq) return; palLoading = false; palItems = (j && Array.isArray(j.items)) ? j.items : []; palSel = 0; palRender(); })
      .catch(function () { if (seq !== palSeq) return; palLoading = false; palItems = []; palRender(); });
  }
  function palGo(i) {
    var it = palItems[i];
    if (!it) return;
    palClose();
    if (it.href.indexOf('#create:') === 0) {
      if (typeof window.pdhCreate === 'function') window.pdhCreate(it.href.slice(8));
      return;
    }
    var go = function () { location.href = it.href; };
    if (window.pdhEdit) window.pdhEdit.guard(go); else go(); // offene Bearbeitung: erst nachfragen
  }
  function palKey(e) {
    // Esc nicht weiterreichen: ein offener Assistent dahinter (Fertigstellen) bliebe sonst nicht offen
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); palClose(); return; }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!palItems.length) return;
      palSel = (palSel + (e.key === 'ArrowDown' ? 1 : palItems.length - 1)) % palItems.length;
      palRender();
      return;
    }
    if (e.key === 'Enter') { e.preventDefault(); palGo(palSel); }
  }
  function palOpen(initial) {
    if (pal) { $('.fp-input', pal).focus(); return; }
    palReturn = document.activeElement;
    pal = document.createElement('div');
    pal.className = 'frame-palette';
    pal.setAttribute('role', 'dialog');
    pal.setAttribute('aria-modal', 'true');
    pal.setAttribute('aria-label', T('Was möchtest du tun?'));
    pal.innerHTML = '<input type="search" class="fp-input" autocomplete="off" role="combobox" aria-expanded="true" aria-controls="fp-list"><ul id="fp-list" class="fp-list" role="listbox"></ul>';
    document.body.appendChild(pal);
    var inp = $('.fp-input', pal);
    inp.placeholder = T('Was möchtest du tun?');
    inp.setAttribute('aria-label', T('Was möchtest du tun?'));
    inp.addEventListener('input', function () {
      palItems = []; palSel = 0; palSeq++; palLoading = true; palRender();
      clearTimeout(palTimer); palTimer = setTimeout(function () { palQuery(inp.value); }, 200);
    });
    inp.addEventListener('keydown', palKey);
    inp.focus();
    inp.value = initial || '';
    palLoading = true;
    palQuery(inp.value);
  }
  window.pdhFrame.palette = function (q) { palOpen(typeof q === 'string' ? q : ''); };

  // ── Statusleiste ───────────────────────────────────────────────
  // Meldungen von setGlobalStatus merken (letzte 20, nur diese Sitzung); Tippen auf die
  // Leiste zeigt sie. Update-Stand kommt aus pdh-maint-band.js (Ereignis pdh:maint).
  var LOG_KEY = 'pdh_frame_log';
  function logRead() {
    try { var l = JSON.parse(sessionStorage.getItem(LOG_KEY) || '[]'); return Array.isArray(l) ? l : []; } catch (e) { return []; }
  }
  // logWorthy: nur Meldungen mit Aussage – nicht „Bereit“/„Fertig“ nach jedem Abruf und
  // nicht „Ausgewaehlt: …“ bei jedem Listenklick (sonst verdrängen sie die Fehler).
  function logWorthy(state, msg, err) {
    if (state === 'busy') return false;
    if (state === 'error' || state === 'warn') return true;
    var m = String(msg || ''), e = String(err || '');
    var noErr = !e || e === 'Keine Fehler' || e === T('Keine Fehler');
    if (m === 'Bereit' || m === 'Fertig' || m === T('Bereit') || m === T('Fertig')) return !noErr;
    if (m.indexOf('Ausgewaehlt:') === 0) return false;
    return !!m || !noErr;
  }
  function logAdd(state, msg, err) {
    try {
      if (!logWorthy(state, msg, err)) return;
      var l = logRead();
      l.unshift({ t: new Date().toLocaleTimeString(), s: state || '', m: String(msg || ''), e: String(err || '') });
      sessionStorage.setItem(LOG_KEY, JSON.stringify(l.slice(0, 20)));
    } catch (e) {}
  }
  if (typeof window.setGlobalStatus === 'function') {
    var origStatus = window.setGlobalStatus;
    window.setGlobalStatus = function (state, message, progress, errorText) {
      logAdd(state, message, errorText);
      var r = origStatus.apply(this, arguments);
      statusTitles();
      return r;
    };
  }
  // statusTitles: gekürzte Texte der Statusleiste ganz im Tooltip
  function statusTitles() {
    ['#footer-status-text', '#footer-status-error'].forEach(function (s) { var el = $(s); if (el) el.title = el.textContent; });
  }
  statusTitles();
  function logToggle(open) {
    var box = $('#frame-log'), foot = $('#global-status-footer');
    if (!box || !foot) return;
    if (open) {
      var ol = $('ol', box);
      ol.innerHTML = '';
      var l = logRead();
      if (!l.length) { var e = document.createElement('li'); e.textContent = T('Keine Meldungen'); ol.appendChild(e); }
      l.forEach(function (x) {
        var li = document.createElement('li');
        li.className = 'fl-' + x.s;
        li.textContent = x.t + ' · ' + x.m + (x.e ? ' – ' + x.e : '');
        ol.appendChild(li);
      });
    }
    box.hidden = !open;
    foot.setAttribute('aria-expanded', open ? 'true' : 'false');
  }
  var mirrorText = ($('#frame-warn') || {}).textContent || '';
  document.addEventListener('pdh:maint', function (e) {
    var v = e.detail || {}, warn = $('#frame-warn');
    if (!warn) return;
    var parts = [];
    if (v.locked || v.active && !v.countdown_until) parts.push(T('Update läuft – PDH ist gleich kurz weg'));
    else if (v.countdown_until) {
      var min = Math.max(0, Math.ceil((Date.parse(v.countdown_until) - Date.now()) / 60000));
      parts.push(T('Automatische Wartung in %s min').replace('%s', String(min)) + (v.to_version ? ' (' + v.to_version + ')' : ''));
    }
    if (mirrorText) parts.push(mirrorText);
    warn.textContent = parts.join(' · ');
    warn.title = warn.textContent; // gekürzt am schmalen Bildschirm – ganz im Tooltip
    warn.hidden = !parts.length;
  });

  function ribbonSelect(key) {
    $$('[data-ribbon-tab]').forEach(function (t) { t.setAttribute('aria-selected', t.dataset.ribbonTab === key ? 'true' : 'false'); });
    $$('[data-ribbon-panel]').forEach(function (p) { p.hidden = p.dataset.ribbonPanel !== key; });
  }

  document.addEventListener('click', function (e) {
    var t = e.target;
    if (t.closest('#global-status-footer')) { logToggle($('#frame-log').hidden); return; }
    if (!t.closest('#frame-log')) logToggle(false);
    if (t.closest('[data-palette-open]')) { palOpen(); return; }
    if (pal && !t.closest('.frame-palette')) palClose();
    if (t.closest('[data-rail-more]')) { railToggle(!$('.rail').classList.contains('open')); return; }
    if (!t.closest('.rail')) railToggle(false);
    var mb = t.closest('[data-ribbon-menu]');
    if (mb) { menuToggle(mb); return; }
    if (!t.closest('.ribbon-menu')) menuClose(null);
    var tab = t.closest('[data-ribbon-tab]');
    if (tab) { ribbonSelect(tab.dataset.ribbonTab); return; }
    var more = t.closest('[data-ribbon-more]');
    if (more) {
      var rb = $('.ribbon'), open = !rb.classList.contains('open');
      rb.classList.toggle('open', open);
      more.setAttribute('aria-expanded', open ? 'true' : 'false');
      return;
    }
    var act = t.closest('[data-ribbon-action]');
    if (act && !act.disabled) { menuClose(null); window.pdhFrame.run(act.dataset.ribbonAction, act.dataset.ribbonArg || ''); }
  });
  document.addEventListener('keydown', function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && (e.key === 'k' || e.key === 'K')) { e.preventDefault(); palOpen(); return; }
    if (e.key === 'Escape') { railToggle(false); logToggle(false); menuClose(null); }
    if ((e.key === 'Enter' || e.key === ' ') && e.target.id === 'global-status-footer') { e.preventDefault(); logToggle($('#frame-log').hidden); }
    // Suchzeile: Tab geht vorbei; Enter, Leertaste oder ein Buchstabe öffnet die Befehlszeile
    var altGr = e.getModifierState && e.getModifierState('AltGraph'); // @, € usw. auf deutscher Tastatur
    if (e.target.matches && e.target.matches('[data-palette-open]') && (altGr || (!e.ctrlKey && !e.metaKey && !e.altKey)) &&
        (e.key === 'Enter' || e.key === ' ' || e.key.length === 1)) {
      e.preventDefault();
      palOpen(e.key.length === 1 && e.key !== ' ' ? e.key : '');
    }
  });
})();
