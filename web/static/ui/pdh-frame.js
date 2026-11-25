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

  function prefs() {
    var r = $('.rail');
    var list = function (v) { return (v || '').split(' ').filter(Boolean); };
    return { order: list(r && r.dataset.order), favorites: list(r && r.dataset.favs) };
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
      var cancel = document.createElement('button'); cancel.type = 'button'; cancel.className = 'btn'; cancel.textContent = T('Abbrechen'); cancel.onclick = close;
      var ok = document.createElement('button'); ok.type = 'button'; ok.className = 'btn btn-primary'; ok.textContent = T('Speichern');
      ok.onclick = function () { var p = prefs(); p.order = items.map(function (it) { return it.key; }); save('/account/ui-areas', p, true); };
      bar.appendChild(cancel); bar.appendChild(ok); box.appendChild(bar);
    };
    draw();
    box.addEventListener('keydown', function (e) { if (e.key === 'Escape') close(); });
    document.body.appendChild(box);
    var first = $('button:not([disabled])', box); if (first) first.focus();
  }

  // ── Menüband ───────────────────────────────────────────────────
  var ACTIONS = {
    'create': function () { if (typeof window.pdhCreate === 'function') window.pdhCreate(); },
    'toggle-right': function () { if (typeof window.toggleSidebar === 'function') window.toggleSidebar('right'); },
    'toggle-narrow': function () {
      var on = !document.documentElement.classList.contains('rail-narrow');
      document.documentElement.classList.toggle('rail-narrow', on);
      try { localStorage.setItem('pdh_rail_narrow', on ? '1' : ''); } catch (e) {}
    },
    'fullscreen': function () {
      if (document.fullscreenElement) document.exitFullscreen();
      else if (document.documentElement.requestFullscreen) document.documentElement.requestFullscreen();
    },
    'fav': function (arg) {
      var p = prefs(), i = p.favorites.indexOf(arg);
      if (i >= 0) p.favorites.splice(i, 1); else p.favorites.push(arg);
      save('/account/ui-areas', p, true);
    },
    'areas-order': function () { openOrder(); },
    'dashboard-edit': function () { if (typeof window.pdhDashboardEdit === 'function') window.pdhDashboardEdit(); },
    'areas-firm': function () { save('/admin/gestaltung/areas', prefs(), false, T('Als Firmenvorgabe gespeichert')); },
    'style': function (arg) { if (typeof window.lookSet === 'function') { window.lookSet('style', arg); window.lookSave(); } },
    'mode': function (arg) { if (typeof window.applyThemeMode === 'function') window.applyThemeMode(arg); },
    'palette': function () { if (typeof window.toggleMenu === 'function') window.toggleMenu('user-menu'); },
    'density': function (arg) { if (typeof window.lookSet === 'function') { window.lookSet('row', parseInt(arg, 10)); window.lookSave(); } }
  };
  window.pdhFrame.run = function (action, arg) { var f = ACTIONS[action]; if (f) f(arg); };

  function ribbonSelect(key) {
    $$('[data-ribbon-tab]').forEach(function (t) { t.setAttribute('aria-selected', t.dataset.ribbonTab === key ? 'true' : 'false'); });
    $$('[data-ribbon-panel]').forEach(function (p) { p.hidden = p.dataset.ribbonPanel !== key; });
  }

  document.addEventListener('click', function (e) {
    var t = e.target;
    if (t.closest('[data-rail-more]')) { railToggle(!$('.rail').classList.contains('open')); return; }
    if (!t.closest('.rail')) railToggle(false);
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
    if (act) window.pdhFrame.run(act.dataset.ribbonAction, act.dataset.ribbonArg || '');
  });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') railToggle(false); });
})();
