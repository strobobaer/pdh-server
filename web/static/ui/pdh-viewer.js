// PDH-Viewer: verknuepfte Datensaetze (data-frame) bzw. freigegebene
// externe Portale (data-frame-external) im Overlay statt Seitenwechsel.
// Strg/Cmd/Mittelklick oeffnen weiterhin normal im neuen Tab.
function openPdhFrame(href, title, external){
  closePdhFrame();
  const wrap = document.createElement('div');
  wrap.className = 'pdh-frame';
  wrap.id = 'pdh-frame';
  wrap.addEventListener('click', function(e){ if(e.target === wrap) closePdhFrame(); });
  const panel = document.createElement('div');
  panel.className = 'pdh-frame-panel';
  const head = document.createElement('div');
  head.className = 'pdh-frame-head';
  const t = document.createElement('div');
  t.className = 'pdh-frame-title';
  t.textContent = title || href;
  const btn = function(icon, label, fn){
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'btn btn-small'; b.title = label;
    b.innerHTML = '<i class="ti '+icon+'"></i>';
    b.addEventListener('click', fn);
    return b;
  };
  const frame = document.createElement('iframe');
  frame.name = external ? 'pdh-external' : 'pdh-embed';
  if(external){
    frame.setAttribute('sandbox', 'allow-scripts allow-forms allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-downloads');
    frame.setAttribute('referrerpolicy', 'no-referrer');
  }
  frame.src = href;
  head.appendChild(t);
  head.appendChild(btn('ti-arrows-maximize', 'Breite umschalten', function(){ panel.classList.toggle('wide'); }));
  head.appendChild(btn('ti-external-link', 'In neuem Tab öffnen', function(){
    let url = href;
    try { if(!external && frame.contentWindow) url = frame.contentWindow.location.href; } catch(e) {}
    window.open(url, '_blank', 'noopener');
  }));
  head.appendChild(btn('ti-x', 'Schließen', closePdhFrame));
  panel.appendChild(head);
  if(external){
    const hint = document.createElement('div');
    hint.className = 'pdh-frame-hint';
    hint.innerHTML = '<i class="ti ti-info-circle"></i> Externes Portal. Bleibt die Ansicht leer, verbietet der Anbieter die Einbettung – dann über <i class="ti ti-external-link"></i> in neuem Tab öffnen.';
    panel.appendChild(hint);
  }
  panel.appendChild(frame);
  wrap.appendChild(panel);
  document.body.appendChild(wrap);
}
function closePdhFrame(){
  const f = document.getElementById('pdh-frame');
  if(f) f.remove();
}
document.addEventListener('keydown', function(e){ if(e.key === 'Escape') closePdhFrame(); });

// Barcode von einem Foto lesen (BarcodeDetector; Chrome/Android, Edge). Ohne
// Unterstützung (z. B. Safari/iPhone) oder ohne Treffer: Nummer abtippen lassen.
async function pdhReadBarcode(file){
  if(file && 'BarcodeDetector' in window){
    try{
      const bitmap = await createImageBitmap(file);
      const formats = await BarcodeDetector.getSupportedFormats();
      const detector = new BarcodeDetector({formats: formats && formats.length ? formats : undefined});
      const codes = await detector.detect(bitmap);
      if(codes && codes.length) return codes[0].rawValue || '';
      const typed = prompt('Kein Barcode erkannt. Nummer unter dem Strichcode eintippen:');
      return typed ? typed.trim() : '';
    }catch(e){ /* weiter mit Eingabe */ }
  }
  const typed = prompt('Dieser Browser kann keine Barcodes lesen. Nummer unter dem Strichcode eintippen:');
  return typed ? typed.trim() : '';
}
async function scanBarcodeIntoInput(fileInput, targetInputId){
  const target = document.getElementById(targetInputId);
  if(!target || !fileInput.files || !fileInput.files.length) return;
  try{
    const code = await pdhReadBarcode(fileInput.files[0]);
    if(code){ target.value = code; target.dispatchEvent(new Event('input', {bubbles: true})); target.focus(); }
  }finally{ fileInput.value = ''; }
}
// Ersatzteil per Barcode öffnen (Ersatzteil-Liste → „Barcode“)
async function pdhBarcodeLookup(fileInput){
  if(!fileInput.files || !fileInput.files.length) return;
  try{
    const code = await pdhReadBarcode(fileInput.files[0]);
    if(code) location.href = '/inventory/lookup?code=' + encodeURIComponent(code);
  }finally{ fileInput.value = ''; }
}

// Widget „Scannen“: Live-Scan mit der Rückkamera (BarcodeDetector), Treffer → /scan?code=…
// Ohne Kamera-Zugriff oder BarcodeDetector (z. B. iPhone, http): Foto aufnehmen und daraus lesen.
async function pdhScanLive(photoInput){
  const fallback = () => { if(photoInput) photoInput.click(); };
  if(!('BarcodeDetector' in window) || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia){ fallback(); return; }
  let stream;
  try{ stream = await navigator.mediaDevices.getUserMedia({video: {facingMode: 'environment'}, audio: false}); }
  catch(e){ fallback(); return; }
  const ov = document.createElement('div');
  ov.className = 'pdh-scan-ov';
  ov.innerHTML = '<video playsinline muted></video><div class="pdh-scan-frame"></div>' +
    '<div class="pdh-scan-hint">QR-Code oder Barcode in den Rahmen halten</div><button type="button" class="btn">Abbrechen</button>';
  document.body.appendChild(ov);
  let done = false;
  const stop = () => { done = true; stream.getTracks().forEach(t => t.stop()); ov.remove(); };
  ov.querySelector('button').onclick = stop;
  const video = ov.querySelector('video');
  video.srcObject = stream;
  await video.play().catch(() => {});
  const formats = await BarcodeDetector.getSupportedFormats().catch(() => []);
  const detector = new BarcodeDetector(formats && formats.length ? {formats: formats} : undefined);
  const tick = async () => {
    if(done) return;
    try{
      const codes = await detector.detect(video);
      if(codes && codes.length && codes[0].rawValue){
        const code = codes[0].rawValue;
        stop();
        if(navigator.vibrate) navigator.vibrate(80);
        location.href = '/scan?code=' + encodeURIComponent(code);
        return;
      }
    }catch(e){ /* Bild noch nicht bereit */ }
    setTimeout(tick, 200);
  };
  tick();
}
async function pdhScanPhoto(fileInput){
  if(!fileInput.files || !fileInput.files.length) return;
  try{
    const code = await pdhReadBarcode(fileInput.files[0]);
    if(code) location.href = '/scan?code=' + encodeURIComponent(code);
  }finally{ fileInput.value = ''; }
}

// Reiter der Stammdaten-Detailseiten (.rec-tab[data-tab] / .rec-pane[data-pane]);
// der aktive Reiter steht in ?tab=, damit Links und Ruecksprung ihn behalten.
function recTab(tab){
  const tabs = document.querySelectorAll('.rec-tab[data-tab]');
  if(!tabs.length) return;
  if(!document.querySelector('.rec-pane[data-pane="'+tab+'"]')) tab = tabs[0].dataset.tab;
  tabs.forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  document.querySelectorAll('.rec-pane').forEach(p => p.classList.toggle('active', p.dataset.pane === tab));
  const u = new URL(location.href);
  u.searchParams.set('tab', tab); u.searchParams.delete('msg'); u.searchParams.delete('err');
  history.replaceState(null, '', u);
}
document.addEventListener('click', function(e){
  const t = e.target.closest('.rec-tab[data-tab]');
  if(t){ e.preventDefault(); recTab(t.dataset.tab); }
});
document.addEventListener('DOMContentLoaded', function(){
  if(document.querySelector('.rec-tab[data-tab]')) recTab(new URL(location.href).searchParams.get('tab') || '');
});

// ── Kontexthilfe: Handbuchkapitel zur aktuellen Seite ────
// Kapitel in /help tragen data-pages="<BaseData.Page …>"; ohne Treffer
// wird das allgemeine Kapitel "Arbeiten mit Datensaetzen" gezeigt.
let pdhHelpLoaded = false;
async function pdhLoadContextHelp(){
  const box = document.getElementById('ctx-help');
  if(!box || pdhHelpLoaded) return;
  pdhHelpLoaded = true;
  try {
    const res = await fetch('/help?fragment=1', {credentials: 'same-origin'});
    if(!res.ok) throw new Error('HTTP ' + res.status);
    const doc = new DOMParser().parseFromString(await res.text(), 'text/html');
    const page = box.dataset.page;
    const sections = [...doc.querySelectorAll('section[data-pages]')];
    const sec = sections.find(s => (s.dataset.pages || '').split(/\s+/).includes(page)) || doc.getElementById('records') || sections[0];
    if(!sec) throw new Error('kein Kapitel');
    // Styles und SVG-Definitionen der Illustrationen mitnehmen
    const extras = [...doc.querySelectorAll('style'), ...doc.querySelectorAll('body > svg')].map(e => e.outerHTML).join('');
    box.innerHTML = extras + sec.innerHTML;
    if(typeof initAttachmentGalleries === 'function') initAttachmentGalleries();
    const link = document.getElementById('ctx-help-chapter');
    if(link) link.href = '/help#' + sec.id;
    // Kapitel-interne Links (#…) ins Handbuch umleiten
    box.querySelectorAll('a[href^="#"]').forEach(a => a.href = '/help' + a.getAttribute('href'));
  } catch(e){ pdhHelpLoaded = false; box.textContent = 'Hilfe konnte nicht geladen werden (' + e.message + ').'; }
}

// ── Zurueck zur vorherigen Seite ─────────────────────────
// Pro Browser-Tab (sessionStorage) wird der Seitenverlauf gefuehrt; Zurueck-
// Knoepfe (Pfeil links / "Zurück" / data-back) springen zur vorherigen ANDEREN
// Seite. Reiterwechsel, Speichern (Redirect auf dieselbe Seite) und Meldungen
// zaehlen nicht als eigener Schritt. Ohne Vorgaenger gilt das feste href.
(function(){
  if(document.documentElement.classList.contains('pdh-embedded')) return;
  const KEY = 'pdh_nav';
  let stack = [];
  try { stack = JSON.parse(sessionStorage.getItem(KEY) || '[]'); } catch(e) { stack = []; }
  const clean = location.search.replace(/([?&])(msg|err)=[^&]*/g, '$1').replace(/[?&]+$/, '').replace('?&', '?');
  const here = {p: location.pathname, u: location.pathname + clean, t: document.title.replace(/^PDH – /, '')};
  const last = stack[stack.length - 1];
  if(last && last.p === here.p) { last.u = here.u; last.t = here.t; }
  else if(stack.length > 1 && stack[stack.length - 2].p === here.p) { stack.pop(); stack[stack.length - 1].u = here.u; } // Browser-Zurueck
  else { stack.push(here); if(stack.length > 40) stack = stack.slice(-40); }
  const save = () => { try { sessionStorage.setItem(KEY, JSON.stringify(stack)); } catch(e) {} };
  save();
  // aktuellen Reiter (?tab=) im Verlauf mitfuehren, damit "zurueck" dort landet
  window.addEventListener('pagehide', () => {
    const top = stack[stack.length - 1];
    if(top && top.p === location.pathname) { top.u = location.pathname + location.search.replace(/([?&])(msg|err)=[^&]*/g, '$1').replace(/[?&]+$/, '').replace('?&', '?'); save(); }
  });
  function previous(){
    for(let i = stack.length - 2; i >= 0; i--) if(stack[i].p !== here.p) return i;
    return -1;
  }
  const isBack = a => a && a.tagName === 'A' && (a.hasAttribute('data-back') || a.getAttribute('aria-label') === 'Zurück' ||
    (a.classList.contains('btn-icon') && a.querySelector('i.ti-arrow-left')));
  document.addEventListener('click', e => {
    const a = e.target.closest('a');
    if(!isBack(a) || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
    const i = previous();
    if(i < 0) return; // kein Vorgaenger: festes Ziel
    e.preventDefault();
    const target = stack[i].u;
    stack = stack.slice(0, i + 1); save();
    location.href = target;
  });
  document.addEventListener('DOMContentLoaded', () => {
    const i = previous();
    if(i < 0) return;
    document.querySelectorAll('a').forEach(a => { if(isBack(a)) { a.title = 'Zurück zu: ' + stack[i].t; a.setAttribute('aria-label', 'Zurück'); } });
  });
})();

// "Im Chat teilen": aktuelle Seite (ohne embed-Parameter) an den Chat uebergeben
// Oeffnet den Chat in der rechten Seitenleiste im Teilen-Modus (Fallback: Chat-Seite).
function pdhShareToChat(){
  const path = location.pathname + location.search.replace(/[?&]embed=1/, '').replace(/[?&](msg|err)=[^&]*/g, '');
  const title = ((document.querySelector('.main h1') || {}).textContent || document.title.replace(/^PDH – /, '')).trim();
  if(window.pdhSideChat) { window.pdhSideChat.share(location.origin + path, title); return; }
  location.href = '/chat?share=' + encodeURIComponent(path) + '&title=' + encodeURIComponent(title);
}
