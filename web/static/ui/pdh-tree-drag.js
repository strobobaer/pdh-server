// Bäume per Ziehen ordnen – Infrastruktur und Lager (Entwurf
// docs/superpowers/specs/2026-11-26-baum-ziehen-design.md, Teil A).
//
// Markup: eine Wurzel [data-tree-move="/infrastructure/"] (Adresse; das Skript hängt
// <id>/move an), darin Knoten [data-tree-node="<id>"] mit data-tree-parent (Elternknoten,
// leer = oberste Ebene) und data-tree-name; die Zeile trägt [data-tree-row], der Griff zum
// Ziehen [data-tree-grip]; ein Knopf „Verschieben nach …“ ruft pdhTreeMoveTo(knopf) auf.
// Optional eine Leiste [data-tree-top] als Ziel „ganz oben“.
//
// Ablauf: ziehen (Maus, Finger, Stift) oder „Verschieben nach …“ → Nachfrage → POST →
// Seite neu laden; danach steht über dem Baum die Meldung mit „Rückgängig“. Der Server
// prüft Recht, Schleifen und Stand selbst (tree_move.go).
(function () {
  'use strict';
  var KEY = 'pdh.tree.last';

  function nodeOf(el) { return el && el.closest ? el.closest('[data-tree-node]') : null; }
  function rootOf(el) { return el && el.closest ? el.closest('[data-tree-move]') : null; }
  function idOf(n) { return n ? n.getAttribute('data-tree-node') : ''; }
  function nameOf(n) { return n ? n.getAttribute('data-tree-name') || '' : ''; }

  function pathOf(n) {
    var parts = [];
    for (var x = n; x; x = nodeOf(x.parentElement)) parts.unshift(nameOf(x));
    return parts.join(' › ');
  }
  function placeOf(target) { return target ? '„' + pathOf(target) + '“' : pdhT('ganz oben'); }

  // gültiges Ziel? nicht der Knoten selbst, keiner seiner Unterknoten, nicht der jetzige Ort
  function allowed(node, target) {
    if (target === node || (target && node.contains(target))) return false;
    var parent = node.getAttribute('data-tree-parent') || '';
    return (target ? idOf(target) : '') !== parent;
  }

  function nodeByID(root, id) {
    if (!id || !root) return null;
    var all = root.querySelectorAll('[data-tree-node]');
    for (var i = 0; i < all.length; i++) if (idOf(all[i]) === id) return all[i];
    return null;
  }

  // ── Nachfrage ────────────────────────────────────────────────────────────
  function dialog() {
    var d = document.getElementById('confirm-tree');
    if (d) return d;
    d = document.createElement('dialog');
    d.id = 'confirm-tree';
    d.className = 'tree-dlg';
    d.innerHTML = '<form method="dialog"><h3 class="tree-dlg-title"></h3><p class="tree-dlg-from"></p><p class="tree-dlg-to"></p>' +
      '<div class="tree-dlg-pick" hidden><input type="search" class="form-input tree-dlg-search"><div class="tree-dlg-list" role="listbox"></div></div>' +
      '<div class="tree-dlg-actions"><button class="btn" value="cancel"></button><button class="btn btn-primary" value="ok"></button></div></form>';
    document.body.appendChild(d);
    d.querySelector('[value=cancel]').textContent = pdhT('Abbrechen');
    d.querySelector('[value=ok]').textContent = pdhT('Verschieben');
    d.querySelector('.tree-dlg-search').placeholder = pdhT('Ziel suchen …');
    d.addEventListener('click', function (e) { if (e.target === d) d.close('cancel'); }); // daneben = abbrechen
    return d;
  }

  function ask(node, target) {
    return new Promise(function (resolve) {
      var d = dialog();
      d.querySelector('.tree-dlg-pick').hidden = true;
      d.querySelector('.tree-dlg-title').textContent = '„' + nameOf(node) + '“ ' + (target ? pdhT('unter') + ' „' + nameOf(target) + '“' : pdhT('ganz nach oben')) + ' ' + pdhT('verschieben?');
      var parent = nodeByID(rootOf(node), node.getAttribute('data-tree-parent') || '');
      d.querySelector('.tree-dlg-from').textContent = pdhT('Bisher') + ': ' + placeOf(parent);
      d.querySelector('.tree-dlg-to').textContent = pdhT('Neu') + ': ' + placeOf(target);
      d.querySelector('[value=ok]').disabled = false;
      d.onclose = function () { resolve(d.returnValue === 'ok'); };
      d.returnValue = '';
      d.showModal();
    });
  }

  // „Verschieben nach …“ ohne Ziehen: Liste aller erlaubten Ziele mit Suche
  function pick(node) {
    return new Promise(function (resolve) {
      var root = rootOf(node), d = dialog(), list = d.querySelector('.tree-dlg-list'), search = d.querySelector('.tree-dlg-search');
      var chosen;
      d.querySelector('.tree-dlg-title').textContent = pdhT('Verschieben nach …') + ' – „' + nameOf(node) + '“';
      d.querySelector('.tree-dlg-from').textContent = pdhT('Bisher') + ': ' + placeOf(nodeByID(root, node.getAttribute('data-tree-parent') || ''));
      d.querySelector('.tree-dlg-to').textContent = '';
      d.querySelector('.tree-dlg-pick').hidden = false;
      var ok = d.querySelector('[value=ok]');
      ok.disabled = true;
      var targets = [];
      if (root.querySelector('[data-tree-top]') && allowed(node, null)) targets.push(null);
      root.querySelectorAll('[data-tree-node]').forEach(function (t) { if (allowed(node, t)) targets.push(t); });
      function draw() {
        var q = search.value.trim().toLowerCase();
        list.textContent = '';
        targets.forEach(function (t) {
          var label = t ? pathOf(t) : pdhT('Ganz oben');
          if (q && label.toLowerCase().indexOf(q) < 0) return;
          var b = document.createElement('button');
          b.type = 'button';
          b.className = 'tree-dlg-item' + (t === chosen ? ' is-chosen' : '');
          b.setAttribute('role', 'option');
          b.textContent = label;
          b.onclick = function () { chosen = t; ok.disabled = false; d.querySelector('.tree-dlg-to').textContent = pdhT('Neu') + ': ' + placeOf(t); draw(); };
          list.appendChild(b);
        });
      }
      search.value = '';
      search.oninput = draw;
      chosen = undefined;
      draw();
      d.onclose = function () { resolve(d.returnValue === 'ok' && chosen !== undefined ? { target: chosen } : null); };
      d.returnValue = '';
      d.showModal();
      search.focus();
    });
  }

  // ── Speichern ────────────────────────────────────────────────────────────
  function send(root, id, parent, from, undo) {
    var body = new URLSearchParams({ parent: parent, from: from });
    return fetch(root.getAttribute('data-tree-move') + encodeURIComponent(id) + '/move', {
      method: 'POST', body: body, credentials: 'same-origin', headers: { 'X-Requested-With': 'fetch' }
    }).then(function (res) {
      if (!res.ok) return res.text().then(function (t) { throw new Error(t.trim() || pdhT('Nicht verschoben')); });
      return res.json();
    }).then(function (j) {
      try {
        sessionStorage.setItem(KEY, JSON.stringify({ base: root.getAttribute('data-tree-move'), id: id, back: j.from || '', now: parent, msg: j.message || '', undo: !!undo }));
      } catch (e) { /* ohne Speicher: keine Meldung nach dem Neuladen */ }
      location.reload();
    }).catch(function (err) { window.alert(err.message || String(err)); });
  }

  function moveNode(node, target) {
    var root = rootOf(node);
    if (!root || !allowed(node, target)) return;
    ask(node, target).then(function (ok) {
      if (ok) send(root, idOf(node), target ? idOf(target) : '', node.getAttribute('data-tree-parent') || '', false);
    });
  }

  window.pdhTreeMoveTo = function (btn) {
    var node = nodeOf(btn);
    if (!node && btn && btn.getAttribute('data-tree-id')) {
      var roots = document.querySelectorAll('[data-tree-move]');
      for (var i = 0; i < roots.length && !node; i++) node = nodeByID(roots[i], btn.getAttribute('data-tree-id'));
    }
    if (!node) return;
    pick(node).then(function (r) {
      if (r) send(rootOf(node), idOf(node), r.target ? idOf(r.target) : '', node.getAttribute('data-tree-parent') || '', false);
    });
  };

  // ── Ziehen (Zeiger-Ereignisse: Maus, Finger, Stift) ──────────────────────
  var drag = null;

  function clearMark() {
    document.querySelectorAll('.tree-drop-ok, .tree-drop-no').forEach(function (el) { el.classList.remove('tree-drop-ok', 'tree-drop-no'); });
  }

  function targetAt(x, y) {
    drag.ghost.style.display = 'none';
    var el = document.elementFromPoint(x, y);
    drag.ghost.style.display = '';
    if (!el || rootOf(el) !== drag.root) return { el: null };
    var top = el.closest('[data-tree-top]');
    if (top) return { el: top, node: null };
    var n = nodeOf(el);
    return n ? { el: n.querySelector('[data-tree-row]') || n, node: n } : { el: null };
  }

  function onMove(e) {
    if (!drag || e.pointerId !== drag.pointer) return;
    if (!drag.active) {
      if (Math.abs(e.clientX - drag.x) + Math.abs(e.clientY - drag.y) < 6) return;
      drag.active = true;
      drag.node.classList.add('tree-dragging');
      drag.root.classList.add('tree-drag-on');
      document.body.appendChild(drag.ghost);
    }
    e.preventDefault();
    drag.ghost.style.left = (e.clientX + 12) + 'px';
    drag.ghost.style.top = (e.clientY + 12) + 'px';
    clearMark();
    var t = targetAt(e.clientX, e.clientY);
    drag.target = undefined;
    if (t.el) {
      var ok = allowed(drag.node, t.node);
      t.el.classList.add(ok ? 'tree-drop-ok' : 'tree-drop-no');
      if (ok) drag.target = t.node;
    }
    // am Rand mitblättern
    if (e.clientY < 60) window.scrollBy(0, -12); else if (e.clientY > window.innerHeight - 60) window.scrollBy(0, 12);
  }

  function end(e, cancel) {
    if (!drag || (e && e.pointerId !== drag.pointer)) return;
    var d = drag;
    drag = null;
    clearMark();
    d.node.classList.remove('tree-dragging');
    d.root.classList.remove('tree-drag-on');
    if (d.ghost.parentNode) d.ghost.parentNode.removeChild(d.ghost);
    try { d.grip.releasePointerCapture(d.pointer); } catch (x) { /* schon frei */ }
    if (!cancel && d.active && d.target !== undefined) moveNode(d.node, d.target);
  }

  document.addEventListener('pointerdown', function (e) {
    var grip = e.target.closest && e.target.closest('[data-tree-grip]');
    if (!grip || e.button > 0 || drag) return; // ein zweiter Finger übernimmt keinen laufenden Zug
    var node = nodeOf(grip), root = rootOf(grip);
    if (!node || !root) return;
    e.preventDefault();
    var ghost = document.createElement('div');
    ghost.className = 'tree-ghost';
    ghost.textContent = nameOf(node);
    drag = { node: node, root: root, grip: grip, pointer: e.pointerId, x: e.clientX, y: e.clientY, active: false, ghost: ghost };
    try { grip.setPointerCapture(e.pointerId); } catch (x) { /* ohne Erfassung geht es auch */ }
  });
  document.addEventListener('pointermove', onMove, { passive: false });
  document.addEventListener('pointerup', function (e) { end(e, false); });
  document.addEventListener('pointercancel', function (e) { end(e, true); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && drag) end(null, true); });

  // ── Meldung nach dem Neuladen, mit „Rückgängig“ ──────────────────────────
  document.addEventListener('DOMContentLoaded', function () {
    var last;
    try { last = JSON.parse(sessionStorage.getItem(KEY) || 'null'); sessionStorage.removeItem(KEY); } catch (e) { last = null; }
    if (!last) return;
    var root = null;
    document.querySelectorAll('[data-tree-move]').forEach(function (r) { if (r.getAttribute('data-tree-move') === last.base) root = r; });
    if (!root) return;
    var box = document.createElement('div');
    box.className = 'tree-note';
    box.setAttribute('role', 'status');
    var text = document.createElement('span');
    text.textContent = last.msg;
    box.appendChild(text);
    if (!last.undo) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn btn-small';
      b.textContent = pdhT('Rückgängig');
      b.onclick = function () { send(root, last.id, last.back, last.now, true); };
      box.appendChild(b);
    }
    root.insertBefore(box, root.firstChild);
    if (nodeByID(root, last.id) && typeof window.scrollToNode === 'function') window.scrollToNode(last.id);
  });
})();
