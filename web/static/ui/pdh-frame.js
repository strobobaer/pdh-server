// Neuer Rahmen (0.90.0 Teil 2) – nur von frame.gohtml geladen. Bereiche, Menüband,
// Befehlszeile Strg+K und Statusleiste kommen hier hinzu; Werte liest das Skript aus
// window.PDH_BOOT, Texte über pdhT (Datenblock pdh-boot in base.gohtml).
(function () {
  'use strict';
  window.pdhFrame = window.pdhFrame || {};

  // Handy: „Mehr“ klappt alle Bereiche auf; Esc oder ein Tipp daneben schließt.
  function railToggle(open) {
    var rail = document.querySelector('.rail');
    var btn = document.querySelector('[data-rail-more]');
    if (!rail || !btn) return;
    rail.classList.toggle('open', open);
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
  }
  document.addEventListener('click', function (e) {
    if (e.target.closest('[data-rail-more]')) {
      railToggle(!document.querySelector('.rail').classList.contains('open'));
    } else if (!e.target.closest('.rail')) {
      railToggle(false);
    }
  });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') railToggle(false); });
})();
