// Bearbeitungsschritte im neuen Rahmen (0.90.0, Entwurf 11.12) – nur von frame.gohtml geladen.
// Eine Seite startet eine Bearbeitung (begin), meldet jeden Schritt (step) mit der Funktion,
// die ihn zurücknimmt. „Rückgängig“ im Band Start und Strg+Z nehmen den letzten Schritt
// zurück, bis gespeichert ist. Ungespeicherte Schritte zeigt die Statusleiste. Wer die Seite
// verlassen will, wird gefragt; ohne Bestätigung werden die Eingaben verworfen. Beim
// Schließen des Fensters wird weder gefragt noch gespeichert.
(function () {
  'use strict';
  var T = function (k) { return typeof window.pdhT === 'function' ? window.pdhT(k) : k; };
  var cur = null, steps = [], saving = null;

  function status(kind, msg) { if (typeof window.setGlobalStatus === 'function') window.setGlobalStatus(kind, msg); }
  function paint() {
    var n = steps.length;
    document.documentElement.classList.toggle('pdh-editing', !!cur);
    [].forEach.call(document.querySelectorAll('[data-ribbon-action="undo"]'), function (b) {
      b.disabled = !n;
      var s = b.querySelector('span');
      if (s) s.textContent = n ? T('Rückgängig') + ' (' + n + ')' : T('Rückgängig');
    });
    var u = document.getElementById('frame-unsaved');
    if (u) { u.hidden = !n; u.textContent = '● ' + n + ' ' + T('nicht gespeichert'); }
  }
  function undoAll() { while (steps.length) { var s = steps.pop(); try { s.undo(); } catch (e) {} } }

  var api = {
    begin: function (opts) { cur = opts || {}; steps = []; paint(); },
    active: function () { return !!cur; },
    dirty: function () { return steps.length > 0; },
    step: function (label, undo) {
      if (!cur) return;
      steps.push({ label: label, undo: undo });
      paint();
      status('ok', label + ' – ' + T('noch nicht gespeichert.'));
    },
    undo: function () {
      var s = steps.pop();
      if (!s) return;
      try { s.undo(); } catch (e) {}
      paint();
      status('ok', T('Rückgängig') + ': ' + s.label);
    },
    // genau ein Speichervorgang zur Zeit (Doppelklick, Nachfrage und Band zugleich)
    save: function () {
      if (!cur) return Promise.resolve(true);
      if (saving) return saving;
      var c = cur;
      saving = Promise.resolve(c.save ? c.save() : true).then(function (ok) {
        saving = null;
        if (ok === false) return false;
        cur = null; steps = []; paint();
        status('ok', T('Gespeichert') + ': ' + (c.name || '') + '.');
        return true;
      }, function () { saving = null; return false; });
      return saving;
    },
    cancel: function () {
      var c = cur;
      undoAll();
      cur = null; paint();
      if (c && c.discard) c.discard();
    },
    guard: function (go) {
      if (!steps.length) { go(); return; }
      ask(go);
    }
  };

  // „Änderungen speichern?“ – Esc, Klick daneben oder „Verwerfen“ verwirft
  function ask(go) {
    if (document.querySelector('.fe-ask')) return;
    var n = steps.length;
    var back = document.createElement('div'); back.className = 'fe-back';
    var box = document.createElement('div');
    box.className = 'frame-dialog fe-ask';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-modal', 'true');
    box.setAttribute('aria-label', T('Änderungen speichern?'));
    var h = document.createElement('h2'); h.textContent = T('Änderungen speichern?'); box.appendChild(h);
    var p = document.createElement('p');
    p.textContent = (n === 1 ? T('1 Änderung ist noch nicht gespeichert.') : n + ' ' + T('Änderungen sind noch nicht gespeichert.')) + ' ' + T('Ohne Bestätigung werden die Eingaben verworfen.');
    box.appendChild(p);
    var bar = document.createElement('div'); bar.className = 'frame-dialog-bar';
    var no = document.createElement('button'); no.type = 'button'; no.className = 'btn'; no.textContent = T('Verwerfen');
    var yes = document.createElement('button'); yes.type = 'button'; yes.className = 'btn btn-primary'; yes.textContent = T('Speichern');
    bar.appendChild(no); bar.appendChild(yes); box.appendChild(bar);
    var done = false;
    var close = function () { box.remove(); back.remove(); document.removeEventListener('keydown', esc, true); };
    var discard = function () {
      if (done || saving) return; // während des Speicherns nicht verwerfen
      done = true; close(); api.cancel();
      status('ok', T('Nicht bestätigt – Eingaben verworfen.'));
      go();
    };
    var esc = function (e) { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); discard(); } };
    no.onclick = discard;
    back.onclick = discard;
    yes.onclick = function () {
      no.disabled = yes.disabled = true;
      api.save().then(function (ok) {
        no.disabled = yes.disabled = false; // bei einem Fehler bleibt das Fenster mit allen Schritten offen
        if (ok && !done) { done = true; close(); go(); }
      });
    };
    document.addEventListener('keydown', esc, true);
    document.body.appendChild(back); document.body.appendChild(box);
    yes.focus();
  }

  // Wächter an allen Wegen weg von der Seite: Links (Bänder, Bereiche, Favoriten, Abmelden,
  // Inhalt) und Formulare außerhalb von [data-edit-keep]
  document.addEventListener('click', function (e) {
    if (!steps.length || e.defaultPrevented || e.button || e.ctrlKey || e.metaKey || e.shiftKey) return;
    var a = e.target.closest && e.target.closest('a[href]');
    if (!a || a.closest('[data-edit-keep]') || a.target === '_blank') return;
    var raw = a.getAttribute('href') || '';
    if (raw.charAt(0) === '#' || raw.indexOf('javascript:') === 0) return;
    e.preventDefault(); e.stopPropagation();
    var href = a.href;
    api.guard(function () { location.href = href; });
  }, true);
  document.addEventListener('submit', function (e) {
    if (!steps.length) return;
    var f = e.target;
    if (!f || (f.closest && f.closest('[data-edit-keep]')) || (f.getAttribute('method') || '').toLowerCase() === 'dialog') return;
    e.preventDefault(); e.stopPropagation();
    var sub = e.submitter; // gedrückter Knopf mit Name, Wert und formaction
    api.guard(function () {
      if (typeof f.requestSubmit === 'function') f.requestSubmit(sub && sub.form === f ? sub : undefined);
      else HTMLFormElement.prototype.submit.call(f);
    });
  }, true);
  // Eingaben während einer Bearbeitung sind Schritte (Widget-Einstellungen, Filter, Farbe):
  // Rückgängig stellt den vorigen Wert wieder her
  document.addEventListener('focusin', function (e) {
    var el = e.target;
    if (cur && el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) && !el.closest('[data-edit-keep]')) el._pdhOld = el.type === 'checkbox' ? el.checked : el.value;
  }, true);
  document.addEventListener('change', function (e) {
    var el = e.target;
    if (!cur || !el || !('_pdhOld' in el) || el.closest('[data-edit-keep]')) return;
    var old = el._pdhOld;
    el._pdhOld = el.type === 'checkbox' ? el.checked : el.value;
    api.step(T('Eingabe geändert'), function () {
      if (el.type === 'checkbox') el.checked = old; else el.value = old;
      el._pdhOld = old;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
  }, true);
  document.addEventListener('keydown', function (e) {
    if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey || (e.key !== 'z' && e.key !== 'Z')) return;
    var t = e.target;
    if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    if (steps.length) { e.preventDefault(); api.undo(); }
  });

  window.pdhEdit = api;
  paint();
})();
