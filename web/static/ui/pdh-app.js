const API = '';
const TOKEN = localStorage.getItem('pdh_token') || '';

const THEME_MODES = ['system','light','dark'];
function resolveTheme(mode) {
  if(mode === 'light' || mode === 'dark') return mode;
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function themeMeta(mode) {
  if(mode === 'light') return {label:'Hell', icon:'ti ti-sun'};
  if(mode === 'dark') return {label:'Dunkel', icon:'ti ti-moon'};
  return {label:'System', icon:'ti ti-device-desktop'};
}
function applyThemeMode(mode) {
  const selected = THEME_MODES.includes(mode) ? mode : 'system';
  localStorage.setItem('pdh_theme', selected);
  document.documentElement.dataset.themeMode = selected;
  document.documentElement.dataset.theme = resolveTheme(selected);
  const meta = themeMeta(selected);
  const icon = document.getElementById('theme-toggle-icon');
  const label = document.getElementById('theme-toggle-label');
  const btn = document.getElementById('theme-toggle');
  if(icon) icon.className = meta.icon;
  if(label) label.textContent = meta.label;
  if(btn) btn.title = 'Hell/Dunkel: ' + meta.label + ' – klicken zum Wechseln';
}
// Darstellung (Farbschema, Schrift, Groesse, Zeilenhoehe): sofort sichtbar, mit
// „Speichern“ am Konto abgelegt. Leer/0 = Firmenstandard.
const lookDraft = Object.assign({}, window.PDH_LOOK_SAVED);
const PDH_FONTS = JSON.parse(PDH_BOOT.fonts);
function lookEffective(k) { return lookDraft[k] || window.PDH_LOOK_DEFAULT[k]; }
function lookApply() {
  const root = document.documentElement;
  const pal = window.PDH_LOOK_ALLOW && window.PDH_PALETTES.indexOf(lookDraft.palette) >= 0 ? lookDraft.palette : window.PDH_LOOK_DEFAULT.palette;
  root.dataset.palette = pal;
  const font = PDH_FONTS[window.PDH_LOOK_ALLOW && lookDraft.font ? lookDraft.font : window.PDH_LOOK_DEFAULT.font];
  if(font) {
    root.style.setProperty('--font', font[0]);
    let link = document.getElementById('pdh-font');
    if(font[1]) {
      if(!link) { link = document.createElement('link'); link.rel = 'stylesheet'; link.id = 'pdh-font'; document.head.appendChild(link); }
      if(link.href !== font[1]) link.href = font[1];
    }
  }
  const scale = lookEffective('scale');
  root.style.setProperty('--ui-scale', (scale / 100).toFixed(2));
  const pct = document.getElementById('look-pct'); if(pct) pct.textContent = scale + ' %';
  const row = lookEffective('row');
  root.style.setProperty('--row-scale', (row / 100).toFixed(2));
  const rp = document.getElementById('look-row'); if(rp) rp.textContent = row + ' %';
  if(window.pdhTopbarFitSoon) pdhTopbarFitSoon();
  root.dataset.style = window.PDH_LOOK_ALLOW && lookDraft.style ? lookDraft.style : window.PDH_LOOK_DEFAULT.style;
  document.querySelectorAll('[data-style-pick]').forEach(b => { const on = b.dataset.stylePick === root.dataset.style; b.classList.toggle('on', on); b.setAttribute('aria-pressed', on); });
  const sel = document.getElementById('look-font'); if(sel) sel.value = lookDraft.font || '';
  document.querySelectorAll('[data-palette-pick]').forEach(b => b.classList.toggle('on', b.dataset.palettePick === (lookDraft.palette || '')));
  const dirty = ['palette','font','style','scale','row'].some(k => (lookDraft[k] || '') !== (window.PDH_LOOK_SAVED[k] || ''));
  const save = document.getElementById('look-save'); if(save) save.disabled = !dirty;
  const msg = document.getElementById('look-msg'); if(msg && dirty) msg.textContent = 'Vorschau – zum Behalten speichern';
}
function lookSet(k, v) { lookDraft[k] = v; lookApply(); }
function lookStep(d) { lookDraft.scale = Math.max(70, Math.min(160, lookEffective('scale') + d)); lookApply(); }
function lookRowStep(d) { lookDraft.row = Math.max(70, Math.min(150, lookEffective('row') + d)); lookApply(); }
function lookReset() { lookDraft.palette = ''; lookDraft.font = ''; lookDraft.style = ''; lookDraft.scale = 0; lookDraft.row = 0; lookApply(); }
async function lookSave() {
  const msg = document.getElementById('look-msg');
  const body = new URLSearchParams({palette: lookDraft.palette || '', font: lookDraft.font || '', style: lookDraft.style || '', scale: String(lookDraft.scale || 0), row: String(lookDraft.row || 0)});
  try {
    const res = await fetch('/account/appearance', {method: 'POST', body, credentials: 'same-origin'});
    const json = await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Fehler');
    window.PDH_LOOK_SAVED = Object.assign({}, lookDraft);
    lookApply();
    if(msg) msg.textContent = 'Gespeichert – gilt jetzt auf allen Geräten';
  } catch(e) { if(msg) msg.textContent = 'Nicht gespeichert: ' + e.message; }
}
// ── Uhr in der Kopfleiste: Serverzeit, tickt genau zum Sekundenwechsel ──
(function(){
  const el = document.getElementById('pdh-clock');
  if(!el) return;
  // Abstand Geraeteuhr ↔ Serveruhr; so zeigen alle Terminals dieselbe Zeit
  const offset = (parseInt(el.dataset.serverMs, 10) || Date.now()) - Date.now();
  const fDay = new Intl.DateTimeFormat(PDH_LOCALE, {weekday: 'long'});
  const fDate = new Intl.DateTimeFormat(PDH_LOCALE, {day: '2-digit', month: '2-digit', year: 'numeric'});
  const fTime = new Intl.DateTimeFormat(PDH_LOCALE, {hour: '2-digit', minute: '2-digit', second: '2-digit'});
  const day = el.querySelector('.clk-day'), date = el.querySelector('.clk-date'), time = el.querySelector('.clk-time');
  function kw(d) { // ISO-Kalenderwoche
    const t = new Date(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()));
    const n = t.getUTCDay() || 7; t.setUTCDate(t.getUTCDate() + 4 - n);
    return Math.ceil(((t - new Date(Date.UTC(t.getUTCFullYear(), 0, 1))) / 86400000 + 1) / 7);
  }
  function tick() {
    const now = new Date(Date.now() + offset);
    day.textContent = fDay.format(now) + ',';
    date.textContent = fDate.format(now);
    time.textContent = fTime.format(now);
    el.title = fDay.format(now) + ', ' + fDate.format(now) + ' · KW ' + kw(now) + ' – Zeiterfassung öffnen';
    setTimeout(tick, 1000 - ((Date.now() + offset) % 1000) + 5);
  }
  tick();
})();
// aktueller Zoom-Faktor (fuer pixelgenaue Positionierung von Menues)
function pdhZoom() { return parseFloat(getComputedStyle(document.body).zoom) || 1; }
document.addEventListener('DOMContentLoaded', lookApply);
// Kopfleiste: passt nicht alles hinein, Knöpfe nach data-tb-hide ausblenden
// (1 zuerst, Sprache zuletzt). Uhr und Konto-Knopf bleiben immer; der
// Seitentitel darf vorher bis auf einen Rest schrumpfen.
function pdhTopbarFit() {
  const tb = document.querySelector('.topbar'); if(!tb) return;
  const lang = tb.querySelector('.lang-switch');
  if(lang) lang.dataset.tbHide = '6';
  const off = [...tb.querySelectorAll('[data-tb-hide]')];
  off.forEach(e => e.classList.remove('tb-auto-off'));
  const html = document.documentElement;
  [...html.classList].filter(c => c.startsWith('tb-off-')).forEach(c => html.classList.remove(c));
  tb.querySelectorAll('.tb-sep').forEach(e => e.classList.remove('tb-auto-off'));
  const cur = tb.querySelector('.topbar-cur');
  const tight = () => tb.scrollWidth > tb.clientWidth + 1 || (cur && cur.scrollWidth > cur.clientWidth + 1 && cur.clientWidth < 60);
  if(!tight()) return;
  tb.querySelectorAll('.tb-sep').forEach(e => e.classList.add('tb-auto-off'));
  off.sort((a, b) => a.dataset.tbHide - b.dataset.tbHide);
  for(const e of off) {
    if(!tight()) break;
    if(getComputedStyle(e).display === 'none') continue;
    e.classList.add('tb-auto-off');
    html.classList.add('tb-off-' + e.dataset.tbHide);
  }
}
(function(){
  let t = 0;
  const run = () => { cancelAnimationFrame(t); t = requestAnimationFrame(pdhTopbarFit); };
  document.addEventListener('DOMContentLoaded', () => {
    run();
    const tb = document.querySelector('.topbar');
    if(tb && window.ResizeObserver) new ResizeObserver(run).observe(tb);
    if(document.fonts) document.fonts.ready.then(run);
  });
  window.addEventListener('resize', run);
  window.pdhTopbarFitSoon = run;
})();
function cycleThemeMode() {
  const current = localStorage.getItem('pdh_theme') || 'system';
  const index = THEME_MODES.indexOf(current);
  applyThemeMode(THEME_MODES[(index + 1) % THEME_MODES.length]);
}
document.addEventListener('DOMContentLoaded', () => applyThemeMode(localStorage.getItem('pdh_theme') || 'system'));
if(window.matchMedia) {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if((localStorage.getItem('pdh_theme') || 'system') === 'system') applyThemeMode('system');
  });
}

function applySidebarState() {
  // Auf dem Smartphone ist die Navigation ein ausfahrbares Menue – dort immer mit Beschriftung
  const left = localStorage.getItem('pdh_sidebar_left') !== 'expanded' && !window.matchMedia('(max-width:760px)').matches;
  document.documentElement.classList.toggle('sidebar-left-collapsed', left);
}
// Rechte Seitenleiste oeffnen/schliessen (Zustand wird bewusst nicht gespeichert).
function setRightSidebar(open) {
  document.documentElement.classList.toggle('sidebar-right-collapsed', !open);
  if(open && localStorage.getItem('pdh_context_tab') === 'chat' && window.pdhSideChat) window.pdhSideChat.shown();
}
// ── Smartphone/Tablet: Navigation (☰) und Kontextleiste als Overlay ──
const PDH_PHONE = window.matchMedia('(max-width:760px)');
const PDH_NARROW = window.matchMedia('(max-width:1180px)');
function pdhNav(open) {
  const h = document.documentElement;
  h.classList.toggle('nav-open', open === undefined ? !h.classList.contains('nav-open') : open);
}
function pdhCloseOverlays() {
  pdhNav(false);
  if(PDH_NARROW.matches) setRightSidebar(false);
}
document.addEventListener('keydown', e => {
  const h = document.documentElement;
  if(e.key === 'Escape' && (h.classList.contains('nav-open') || (PDH_NARROW.matches && !h.classList.contains('sidebar-right-collapsed')))) pdhCloseOverlays();
});
PDH_PHONE.addEventListener('change', () => { pdhNav(false); applySidebarState(); });
// Lange druecken = Rechtsklick (iPhone/iPad kennen kein contextmenu-Ereignis;
// Android liefert es selbst – dann wird hier nichts doppelt ausgeloest).
(function(){
  if(!('ontouchstart' in window)) return;
  let timer = null, start = null, nativeFired = false, suppressClick = false;
  document.addEventListener('contextmenu', e => { if(e.isTrusted) nativeFired = true; }, true);
  document.addEventListener('touchstart', e => {
    if(e.touches.length !== 1) { clearTimeout(timer); start = null; return; }
    const t = e.touches[0];
    start = {x: t.clientX, y: t.clientY, target: e.target};
    nativeFired = false;
    clearTimeout(timer);
    timer = setTimeout(() => {
      if(nativeFired || !start) return;
      const ev = new MouseEvent('contextmenu', {bubbles: true, cancelable: true, clientX: start.x, clientY: start.y, button: 2});
      if(!start.target.dispatchEvent(ev)) { suppressClick = true; if(navigator.vibrate) navigator.vibrate(15); }
      start = null;
    }, 550);
  }, {passive: true});
  document.addEventListener('touchmove', e => {
    if(!start) return;
    const t = e.touches[0];
    if(Math.hypot(t.clientX - start.x, t.clientY - start.y) > 10) { clearTimeout(timer); start = null; }
  }, {passive: true});
  document.addEventListener('touchend', () => { clearTimeout(timer); start = null; }, {passive: true});
  document.addEventListener('click', e => { if(suppressClick) { suppressClick = false; e.preventDefault(); e.stopPropagation(); } }, true);
})();
function toggleSidebar(side) {
  if(side === 'right') {
    setRightSidebar(document.documentElement.classList.contains('sidebar-right-collapsed'));
    return;
  }
  const collapsed = localStorage.getItem('pdh_sidebar_left') !== 'expanded';
  localStorage.setItem('pdh_sidebar_left', collapsed ? 'expanded' : 'collapsed');
  applySidebarState();
}
// ── Menü-Umschalter (globaler Fallback; einzelne Seiten dürfen das lokal
// überschreiben, z.B. für andere ID-Präfixe) ──────────────────────────
function toggleMenu(id){
  var m = document.getElementById(id);
  if(m) m.style.display = (m.style.display === 'none' || !m.style.display) ? 'block' : 'none';
}
function closeMenu(id){
  var m = document.getElementById(id);
  if(m) m.style.display = 'none';
}
document.addEventListener('click', function(e){
  if(!e.target.closest('[onclick*="toggleMenu"]') && !e.target.closest('.look-menu')){
    document.querySelectorAll('[id$="-menu"]').forEach(function(m){ m.style.display = 'none'; });
  }
});

// ── Auto-Logout nach Inaktivität, mit Systemnutzer-/Override-Ausnahme ──
(function(){
  var isSystemUser = PDH_BOOT.isSystemUser;
  // Kurzzugang aus dem Leitstand verhaelt sich wie eine Override-Sitzung
  var isOverride    = PDH_BOOT.isOverride;
  var idleMinutes     = PDH_BOOT.idleMinutes;
  var overrideMinutes = PDH_BOOT.overrideMinutes;

  var logoutUrl = null, minutes = 0;
  if(isOverride){
    // Inaktivität beendet auch eine Override-Sitzung und führt zum Leitstand.
    logoutUrl = '/logout';
    minutes = overrideMinutes;
  } else if(!isSystemUser){
    logoutUrl = '/logout';
    minutes = idleMinutes;
  }
  // reiner Systemnutzer (kein Override aktiv): kein Timer, bleibt wie
  // gewünscht dauerhaft eingeloggt.

  // Administratoren: solange code-server, n8n oder pgAdmin in einem anderen
  // Tab offen sind, nicht abmelden (admin_tool_activity.go).
  var waitForAdminTools = (PDH_BOOT.isAdmin ? !isOverride : false);

  if(logoutUrl && minutes > 0){
    var idleTimer;
    function idleExpired(){
      if(!waitForAdminTools){ window.location = logoutUrl; return; }
      fetch('/api/admin-tools/active', {cache: 'no-store', credentials: 'same-origin'})
        .then(function(r){ return r.ok ? r.json() : {}; })
        .then(function(d){ if(d && d.active) resetIdleTimer(); else window.location = logoutUrl; })
        .catch(function(){ window.location = logoutUrl; });
    }
    function resetIdleTimer(){
      clearTimeout(idleTimer);
      idleTimer = setTimeout(idleExpired, minutes * 60 * 1000);
    }
    ['mousemove','keydown','click','touchstart','scroll'].forEach(function(evt){
      document.addEventListener(evt, resetIdleTimer, {passive:true});
    });
    resetIdleTimer();
  }

  var params = new URLSearchParams(location.search);
  var overrideErr = params.get('override_error');
  if(overrideErr){
    var messages = {
      'nur_am_systemnutzer': 'Override-Anmeldung ist nur an einem Systemnutzer-Terminal möglich.',
      'zugangsdaten': 'E-Mail/Benutzername oder Passwort falsch.',
      'unbekannte_karte': 'Diese RFID-Karte ist keinem Konto zugeordnet.',
      'keine_berechtigung': 'Dieses Konto hat keine Berechtigung für die Override-Anmeldung.',
      'passwort_aendern': 'Für dieses Konto muss zuerst das Passwort geändert werden – bitte einmal normal anmelden.',
      'token': 'Anmeldung fehlgeschlagen, bitte erneut versuchen.'
    };
    alert(messages[overrideErr] || 'Anmeldung fehlgeschlagen.');
    history.replaceState({}, '', location.pathname);
  }
})();

// Reiter der rechten Seitenleiste waehlen. Mit id: Leiste oeffnen - ein
// erneuter Klick auf den bereits offenen Reiter klappt sie wieder ein
// (ausser opts.keepOpen). Ohne id: nur den gespeicherten Reiter vorwaehlen.
function activateContextTab(id, opts) {
  const html = document.documentElement;
  const stored = localStorage.getItem('pdh_context_tab') || 'search';
  let selected = id || stored;
  if(!document.querySelector('[data-context-panel="' + selected + '"]')) selected = 'search';
  const isOpen = !html.classList.contains('sidebar-right-collapsed');
  if(id && isOpen && selected === stored && !(opts && opts.keepOpen)) { setRightSidebar(false); return; }
  document.querySelectorAll('[data-context-tab]').forEach(btn => btn.classList.toggle('active', btn.dataset.contextTab === selected));
  document.querySelectorAll('[data-context-panel]').forEach(panel => panel.classList.toggle('active', panel.dataset.contextPanel === selected));
  localStorage.setItem('pdh_context_tab', selected);
  html.classList.toggle('ctx-chat-open', selected === 'chat');
  if(selected === 'copilot' && id && window.pdhCopilotSeen) window.pdhCopilotSeen();
  if(id) setRightSidebar(true);
  if(selected === 'help' && id) pdhLoadContextHelp();
}
document.addEventListener('DOMContentLoaded', () => {
  applySidebarState();
  activateContextTab(null);
});

function pdhToken(){return document.cookie.split(';').find(c=>c.trim().startsWith('pdh_token='))?.split('=')[1] || localStorage.getItem('pdh_token') || ''}
function escapeHtml(s){return String(s||'').replace(/[&<>"]/g,m=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[m]))}

/* ── Rechtsklick-Kontextmenü (Win11-Stil), projektweit nutzbar ──
   showContextMenu(event, items) öffnet direkt an der Mausposition.
   items: [{label, icon, onClick, danger, disabled, shortcut}] oder
   {separator:true} oder {heading:'...'}.
   bindContextMenu(el, event => items) hängt den Rechtsklick-Handler an
   ein Element, itemsFn wird bei jedem Rechtsklick neu ausgewertet. */
let win11ActiveMenu = null;
function closeContextMenu(){
  if(!win11ActiveMenu) return;
  win11ActiveMenu.remove();
  win11ActiveMenu = null;
  document.removeEventListener('click', closeContextMenu, true);
  document.removeEventListener('contextmenu', win11CloseOnOutsideContext, true);
  document.removeEventListener('keydown', win11CloseOnEscape, true);
  document.removeEventListener('scroll', closeContextMenu, true);
}
function win11CloseOnEscape(e){ if(e.key === 'Escape') closeContextMenu(); }
function win11CloseOnOutsideContext(e){ if(win11ActiveMenu && !win11ActiveMenu.contains(e.target)) closeContextMenu(); }

function showContextMenu(event, items){
  event.preventDefault();
  event.stopPropagation();
  closeContextMenu();
  const menu = document.createElement('div');
  menu.className = 'win11-menu';
  (items || []).forEach(function(item){
    if(item.separator){
      const sep = document.createElement('div');
      sep.className = 'win11-menu-separator';
      menu.appendChild(sep);
      return;
    }
    if(item.heading){
      const heading = document.createElement('div');
      heading.className = 'win11-menu-label';
      heading.textContent = item.heading;
      menu.appendChild(heading);
      return;
    }
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'win11-menu-item' + (item.danger ? ' danger' : '');
    btn.disabled = !!item.disabled;
    if(item.icon){
      const icon = document.createElement('i');
      icon.className = 'ti ' + item.icon;
      btn.appendChild(icon);
    }
    const label = document.createElement('span');
    label.textContent = item.label;
    btn.appendChild(label);
    if(item.shortcut){
      const sc = document.createElement('span');
      sc.className = 'win11-menu-shortcut';
      sc.textContent = item.shortcut;
      btn.appendChild(sc);
    }
    if(!item.disabled){
      btn.addEventListener('click', function(){
        closeContextMenu();
        if(item.onClick) item.onClick();
      });
    }
    menu.appendChild(btn);
  });
  document.body.appendChild(menu);
  win11ActiveMenu = menu;

  const menuRect = menu.getBoundingClientRect();
  let x = event.clientX, y = event.clientY;
  if(x + menuRect.width > window.innerWidth) x = window.innerWidth - menuRect.width - 6;
  if(y + menuRect.height > window.innerHeight) y = window.innerHeight - menuRect.height - 6;
  const z = pdhZoom();
  menu.style.left = Math.max(4, x) / z + 'px';
  menu.style.top = Math.max(4, y) / z + 'px';

  setTimeout(function(){
    document.addEventListener('click', closeContextMenu, true);
    document.addEventListener('contextmenu', win11CloseOnOutsideContext, true);
    document.addEventListener('keydown', win11CloseOnEscape, true);
    document.addEventListener('scroll', closeContextMenu, true);
  }, 0);
}

function bindContextMenu(el, itemsFn){
  if(!el) return;
  el.addEventListener('contextmenu', function(event){
    showContextMenu(event, itemsFn(event));
  });
}
// Symbole aus der Symbol-Tabelle (icons.go, änderbar unter /admin/icons)
const PDH_ICONS = PDH_BOOT.icons;
function infraIcon(type){return '<i class="ti ' + (PDH_ICONS['infra.' + type] || 'ti-point') + '"></i>'}
// Typ nur als Symbol im Baum; der Name steht im Tooltip (title) und für Screenreader im aria-label
const INFRA_TYPE_LABEL = {building:'Gebäude', line:'Linie', plant:'Anlage', device:'Gerät'};
function selectInfra(btn){
  const root=btn.closest('[data-infra-picker]');
  if(!root) return;
  const target=document.getElementById(root.dataset.target);
  const label=document.getElementById(root.dataset.selectedLabel);
  if(target) target.value=btn.dataset.infraId || '';
  if(label) label.textContent=btn.dataset.infraName || 'Keine Infrastruktur ausgewählt';
  root.__infraFocus=btn.dataset.infraId || '';
  root.querySelectorAll('.infra-picker-row.is-focus').forEach(r=>r.classList.remove('is-focus'));
  btn.closest('.infra-picker-row')?.classList.add('is-focus');
}
function clearInfraPicker(root){
  if(!root) return;
  const target=document.getElementById(root.dataset.target);
  const label=document.getElementById(root.dataset.selectedLabel);
  if(target) target.value='';
  if(label) label.textContent='Keine Infrastruktur ausgewählt';
}
async function loadInfraPicker(root){
  if(!root) return;
  const box=root.querySelector('[data-infra-tree]');
  if(box) box.innerHTML='<div class="infra-picker-empty">Lade Baum...</div>';
  try{
    const res=await fetch('/api/v1/infrastructure/tree',{headers:{Authorization:'Bearer '+pdhToken()}});
    const json=await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Infrastruktur konnte nicht geladen werden');
    root.__infraTree=json.data||[];
    root.__infraCCView=null;
    root.__infraMode=infraPickerSavedMode();
    infraPickerSyncModeButtons(root);
    infraPickerReset(root);
  }catch(e){
    if(box) box.innerHTML='<div class="infra-picker-empty" style="color:var(--red)">Infrastruktur konnte nicht geladen werden</div>';
  }
}
// Ansicht des Pickers: "tree" = nach Standort (Baum), "cc" = nach Kostenstelle.
// Die Wahl gilt je Browser für alle Picker.
function infraPickerSavedMode(){try{return localStorage.getItem('pdh.infraPickerMode')==='cc'?'cc':'tree'}catch(e){return 'tree'}}
function infraPickerSyncModeButtons(root){root.querySelectorAll('[data-infra-mode]').forEach(b=>b.setAttribute('aria-pressed', String(b.dataset.infraMode===(root.__infraMode||'tree'))))}
function infraView(root){
  if((root.__infraMode||'tree')!=='cc') return root.__infraTree||[];
  if(!root.__infraCCView) root.__infraCCView=buildInfraCCView(root.__infraTree||[]);
  return root.__infraCCView;
}
// Aufklappen bis zur aktuellen Auswahl, sonst bis zum Terminal-Standort;
// ohne beides bleibt der Baum zugeklappt.
function infraPickerReset(root){
  root.__infraExpanded=new Set();
  const target=document.getElementById(root.dataset.target);
  const focus=(target && target.value) || window.PDH_TERMINAL_INFRA || '';
  const path=focus ? findInfraPath(infraView(root), focus) : null;
  if(path){ path.forEach(id=>root.__infraExpanded.add(id)); root.__infraFocus=focus; }
  infraPickerRender(root);
  if(path) scrollInfraFocus(root);
}
function infraPickerRender(root){
  const q=(root.querySelector('[data-infra-filter]')?.value||'').toLowerCase().trim();
  renderInfraPicker(root, q ? filterInfraTree(infraView(root), q) : infraView(root));
}
function setInfraPickerMode(btn){
  const root=btn.closest('[data-infra-picker]');
  if(!root) return;
  root.__infraMode=btn.dataset.infraMode==='cc'?'cc':'tree';
  try{localStorage.setItem('pdh.infraPickerMode', root.__infraMode)}catch(e){}
  infraPickerSyncModeButtons(root);
  if(root.__infraTree) infraPickerReset(root);
}
// Ansicht „Nach Kostenstelle“: oberste Ebene sind die Kostenstellen. Ohne
// eigene Kostenstelle gilt die des übergeordneten Knotens; darunter bleibt
// der Baum erhalten. Teile mit anderer Kostenstelle stehen bei ihrer eigenen
// Gruppe, mit dem Standort als Hinweis.
function buildInfraCCView(tree){
  const groups=new Map();
  const group=(cc)=>{
    const key=cc.id||'none';
    if(!groups.has(key)) groups.set(key,{id:'cc:'+key, group:true, type:'cc', name:cc.id?(cc.label||'Kostenstelle'):'Ohne Kostenstelle', none:!cc.id, children:[], count:0});
    return groups.get(key);
  };
  const walk=(nodes, inherited, path, parent)=>{
    (nodes||[]).forEach(n=>{
      const cc=n.cost_center_id ? {id:n.cost_center_id, label:[n.cost_center_number, n.cost_center_name].filter(Boolean).join(' ')} : inherited;
      const copy={...n, children:[]};
      group(cc).count++;
      if(parent && cc.id===inherited.id) parent.children.push(copy);
      else { if(path) copy.path=path; group(cc).children.push(copy); }
      walk(n.children, cc, path ? path+' › '+n.name : n.name, copy);
    });
  };
  walk(tree, {id:'', label:''}, '', null);
  return [...groups.values()].sort((a,b)=>(a.none-b.none) || a.name.localeCompare(b.name, 'de', {numeric:true}));
}
function renderInfraPicker(root, nodes){
  const box=root.querySelector('[data-infra-tree]');
  if(!box) return;
  if(!nodes.length){box.innerHTML='<div class="infra-picker-empty">Keine Infrastruktur vorhanden</div>'; return}
  box.innerHTML=renderInfraNodes(root, nodes, 0);
}
function findInfraPath(nodes, id){
  for(const n of (nodes||[])){
    if(n.id===id) return [n.id];
    const sub=findInfraPath(n.children, id);
    if(sub) return [n.id, ...sub];
  }
  return null;
}
function scrollInfraFocus(root){
  const box=root.querySelector('[data-infra-tree]');
  const row=box && box.querySelector('.infra-picker-row.is-focus');
  if(row) box.scrollTop=Math.max(0, row.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop - 40);
}
function renderInfraNodes(root, nodes, depth){
  const expanded=root.__infraExpanded||new Set();
  const filtering=!!(root.querySelector('[data-infra-filter]')?.value||'').trim(); // Suchtreffer immer sichtbar
  return (nodes||[]).map(n=>{
    const children=n.children||[];
    const hasChildren=children.length>0;
    const open=hasChildren && (filtering || expanded.has(n.id));
    const toggle=hasChildren
      ? `<button type="button" class="infra-picker-toggle" aria-label="Ast ein- oder ausklappen" aria-expanded="${open}" onclick="toggleInfraBranch(event,this)" data-infra-toggle="${escapeHtml(n.id)}"><i class="ti ti-chevron-right"></i></button>`
      : `<span class="infra-picker-spacer"></span>`;
    // Kostenstellen-Gruppe: nur auf- und zuklappen, nicht auswählbar
    const label=n.group
      ? `<button type="button" class="infra-picker-group" data-infra-toggle="${escapeHtml(n.id)}" onclick="toggleInfraBranch(event,this)" aria-expanded="${open}"><span class="infra-picker-ic" aria-hidden="true"><i class="ti ${n.none?'ti-circle-dashed':'ti-building-bank'}"></i></span> ${escapeHtml(n.name)}<span class="n">${n.count}</span></button>`
      : `<button type="button" class="infra-picker-select" data-infra-id="${escapeHtml(n.id)}" data-infra-name="${escapeHtml(n.name)}" onclick="selectInfra(this)" aria-label="${escapeHtml((INFRA_TYPE_LABEL[n.type]||n.type)+': '+n.name)}"><span class="infra-picker-ic" title="${escapeHtml(INFRA_TYPE_LABEL[n.type]||n.type)}" aria-hidden="true">${infraIcon(n.type)}</span> ${escapeHtml(n.name)}${n.path?`<span class="infra-picker-sub">in ${escapeHtml(n.path)}</span>`:''}</button>`;
    return `<div class="infra-picker-node" data-infra-node="${escapeHtml(n.id)}" style="--depth:${depth*12}px"><div class="infra-picker-row${root.__infraFocus===n.id?' is-focus':''}">${toggle}${label}</div>${hasChildren?`<div data-infra-children="${escapeHtml(n.id)}" style="display:${open?'block':'none'}">${renderInfraNodes(root, children, depth+1)}</div>`:''}</div>`;
  }).join('');
}
function toggleInfraBranch(event, btn){
  event.stopPropagation();
  const root=btn.closest('[data-infra-picker]');
  const id=btn.dataset.infraToggle;
  root.__infraExpanded=root.__infraExpanded||new Set();
  // nur ein Ast offen: beim Öffnen bleiben nur der Knoten und seine Vorfahren aufgeklappt
  const path=findInfraPath(infraView(root), id)||[id];
  if(root.__infraExpanded.has(id)) root.__infraExpanded=new Set(path.slice(0,-1)); else root.__infraExpanded=new Set(path);
  infraPickerRender(root);
}
// Treffer in Name, Typ oder Kostenstelle; trifft eine Kostenstellen-Gruppe, bleibt sie ganz sichtbar
function filterInfraTree(nodes, q){return (nodes||[]).map(n=>{const text=[n.name, n.type, n.cost_center_number, n.cost_center_name].join(' ').toLowerCase(); if(n.group && text.includes(q)) return n; const children=filterInfraTree(n.children||[],q); return text.includes(q)||children.length?{...n,children}:null}).filter(Boolean)}
function filterInfraPicker(input){const root=input.closest('[data-infra-picker]'); if(root) infraPickerRender(root)}
document.addEventListener('input', event => {
  if(event.target.matches('[data-infra-filter]')) filterInfraPicker(event.target);
});
document.addEventListener('DOMContentLoaded',()=>document.querySelectorAll('[data-infra-picker]').forEach(loadInfraPicker));

let globalStatusTimer = null;
let selectedListItem = null;
// Statusbalken unten: Tätigkeit und Fortschritt in %. Solange gearbeitet wird
// (busy), läuft der Balken gleichmäßig auf 90 % zu; echte Werte (Upload/Download)
// setzt pdhStatusProgress(). Fertig = 100 %, danach leert sich der Balken.
const pdhStatus = {value: 0, message: '', creep: null, active: 0};
function pdhStatusRender(state) {
  const fill = document.getElementById('footer-progress-fill');
  const label = document.getElementById('footer-progress-label');
  const bar = document.getElementById('footer-progress');
  const pct = Math.round(Math.max(0, Math.min(100, pdhStatus.value)));
  if(fill) fill.style.width = pct + '%';
  if(bar) bar.setAttribute('aria-valuenow', String(pct));
  if(label) {
    label.textContent = '';
    if(pdhStatus.message && (state !== 'ok' || pct > 0)) {
      label.appendChild(document.createTextNode(pdhStatus.message));
      if(state === 'busy' || pct > 0) {
        const b = document.createElement('b');
        b.textContent = pct + ' %';
        label.appendChild(b);
      }
    }
  }
}
function pdhStatusStopCreep() {
  if(pdhStatus.creep) window.clearInterval(pdhStatus.creep);
  pdhStatus.creep = null;
}
// pdhStatusProgress: echter Fortschritt (0–100) einer laufenden Aktion.
function pdhStatusProgress(pct) {
  pdhStatus.value = Math.max(pdhStatus.value, Math.min(99, Number(pct) || 0));
  pdhStatusRender('busy');
}
function setGlobalStatus(state, message, progress, errorText) {
  const footer = document.getElementById('global-status-footer');
  if(!footer) return;
  const normalized = ['ok','busy','warn','error'].includes(state) ? state : 'ok';
  footer.className = 'global-footer ' + normalized;
  const text = document.getElementById('footer-status-text');
  const error = document.getElementById('footer-status-error');
  pdhStatus.message = message || (normalized === 'busy' ? 'Arbeite …' : 'Bereit');
  if(text) text.textContent = pdhStatus.message;
  if(error) error.textContent = errorText || (normalized === 'error' ? (message || 'Fehler') : 'Keine Fehler');
  if(globalStatusTimer) window.clearTimeout(globalStatusTimer);
  // Spielerei: Logo dreht sich, solange gearbeitet wird
  document.documentElement.classList.toggle('pdh-busy', normalized === 'busy');
  if(normalized === 'busy') {
    pdhStatus.value = progress === undefined || progress === null ? Math.max(pdhStatus.value < 100 ? pdhStatus.value : 0, 8) : Number(progress) || 0;
    if(!pdhStatus.creep) pdhStatus.creep = window.setInterval(() => {
      if(pdhStatus.value < 90) { pdhStatus.value += Math.max(0.4, (90 - pdhStatus.value) * 0.06); pdhStatusRender('busy'); }
    }, 200);
    pdhStatusRender('busy');
    return;
  }
  pdhStatusStopCreep();
  pdhStatus.value = progress === undefined || progress === null ? 100 : Number(progress) || 0;
  pdhStatusRender(normalized);
  if(normalized === 'ok') {
    globalStatusTimer = window.setTimeout(() => {
      pdhStatus.value = 0;
      pdhStatusRender('ok');
    }, 1500);
  }
}
// pdhStatusWhat: Beschreibung der auslösenden Aktion (data-status, Beschriftung
// des Knopfes bzw. Formulars), z. B. „Speichere: Anlegen“.
function pdhStatusWhat(elt, method) {
  let what = '';
  if(elt && elt.closest) {
    const own = elt.closest('[data-status]');
    if(own) return own.dataset.status;
    let src = elt;
    if(elt.tagName === 'FORM') src = elt.querySelector('[type=submit]') || elt;
    what = src.getAttribute('aria-label') || src.getAttribute('title') || '';
    if(!what && elt.tagName === 'FORM') {
      const head = elt.closest('.card') && elt.closest('.card').querySelector('.card-title');
      what = head ? head.textContent : '';
    }
    if(!what && elt.tagName !== 'FORM' && elt.tagName !== 'DIV' && elt.tagName !== 'BODY') what = elt.textContent || '';
  }
  what = what.replace(/\s+/g, ' ').trim();
  if(what.length > 60) what = what.slice(0, 57) + '…';
  const verb = (method || 'GET').toUpperCase() === 'GET' ? 'Lade' : 'Speichere';
  return what ? verb + ': ' + what + ' …' : (verb === 'Lade' ? 'Lade Daten …' : 'Speichere …');
}
function showGlobalError(message, detail) {
  setGlobalStatus('error', message || 'Fehler', 100, detail || message || 'Fehler');
}
function csrfHeaders() {
  const token = document.cookie.split(';').find(c=>c.trim().startsWith('pdh_token='))?.split('=')[1] || TOKEN;
  return token ? {'Authorization':'Bearer '+token} : {};
}
function markSelectedListItem(item) {
  if(!item) return;
  document.querySelectorAll('.selectable-item.is-selected').forEach(el => {
    if(el !== item) el.classList.remove('is-selected');
  });
  selectedListItem = item;
  item.classList.add('is-selected');
  const title = item.dataset.title || item.querySelector('.li-title')?.textContent || item.textContent || '';
  setGlobalStatus('ok', 'Ausgewaehlt: ' + title.trim().slice(0, 80), 100, 'Keine Fehler');
}
function getSelectedListItem() {
  if(selectedListItem && document.body.contains(selectedListItem)) return selectedListItem;
  selectedListItem = document.querySelector('.selectable-item.is-selected');
  if(selectedListItem) return selectedListItem;
  setGlobalStatus('warn', 'Bitte zuerst einen Listeneintrag auswaehlen', 100, 'Keine Auswahl');
  return null;
}
function resolveTargetElement(selectorOrId, root) {
  if(!selectorOrId) return null;
  const scope = root || document;
  if(selectorOrId.startsWith('#') || selectorOrId.startsWith('.') || selectorOrId.includes('[')) {
    return scope.querySelector(selectorOrId) || document.querySelector(selectorOrId);
  }
  return document.getElementById(selectorOrId) || scope.querySelector(selectorOrId);
}
async function postSelectedAction(item, action) {
  const url = item.dataset[action + 'Url'] || item.dataset.actionUrl;
  if(!url) return false;
  const method = item.dataset[action + 'Method'] || item.dataset.actionMethod || 'POST';
  const headers = csrfHeaders();
  let body = null;
  const bodyValue = item.dataset[action + 'Body'] || item.dataset.actionBody;
  if(bodyValue) {
    headers['Content-Type'] = 'application/x-www-form-urlencoded;charset=UTF-8';
    body = bodyValue;
  }
  setGlobalStatus('busy', 'Sende Aktion...', 45, '');
  const res = await fetch(url, {method, headers, body});
  if(!res.ok) throw new Error('HTTP ' + res.status + ' ' + res.statusText);
  setGlobalStatus('ok', 'Aktion ausgefuehrt', 100, 'Keine Fehler');
  window.setTimeout(() => location.reload(), 350);
  return true;
}
async function runSelectedItemAction(action) {
  const item = getSelectedListItem();
  if(!item) return false;
  try {
    if(action === 'view') {
      const href = item.dataset.viewHref || item.dataset.href || item.getAttribute('data-href') || item.querySelector('a[href]')?.getAttribute('href');
      if(href) location.href = href;
      else setGlobalStatus('warn', 'Keine Detailansicht hinterlegt', 100, 'Keine Detailansicht');
      return false;
    }
    if(action === 'edit') {
      const target = resolveTargetElement(item.dataset.editTarget, item);
      if(target) {
        target.style.display = target.style.display === 'none' || !target.style.display ? 'block' : 'none';
        setGlobalStatus('ok', 'Bearbeiten umgeschaltet', 100, 'Keine Fehler');
      } else if(item.dataset.editUrl) {
        location.href = item.dataset.editUrl;
      } else {
        setGlobalStatus('warn', 'Keine Bearbeiten-Aktion hinterlegt', 100, 'Keine Bearbeiten-Aktion');
      }
      return false;
    }
    if(action === 'save') {
      const form = resolveTargetElement(item.dataset.saveTarget, item) || resolveTargetElement(item.dataset.editTarget, item)?.querySelector('form');
      if(form && form.requestSubmit) form.requestSubmit();
      else if(form) form.submit();
      else setGlobalStatus('warn', 'Kein Formular zum Speichern gefunden', 100, 'Kein Formular');
      return false;
    }
    if(action === 'cancel') {
      const target = resolveTargetElement(item.dataset.cancelTarget || item.dataset.editTarget, item);
      if(target) {
        target.style.display = 'none';
        setGlobalStatus('ok', 'Abgebrochen', 100, 'Keine Fehler');
      } else {
        setGlobalStatus('warn', 'Keine Abbrechen-Aktion hinterlegt', 100, 'Keine Abbrechen-Aktion');
      }
      return false;
    }
    if(action === 'copy') {
      if(await postSelectedAction(item, 'copy')) return false;
      const text = item.dataset.copyText || item.dataset.title || item.textContent;
      if(navigator.clipboard && text) {
        await navigator.clipboard.writeText(text.trim());
        setGlobalStatus('ok', 'In Zwischenablage kopiert', 100, 'Keine Fehler');
      } else {
        setGlobalStatus('warn', 'Keine Kopieren-Aktion hinterlegt', 100, 'Keine Kopieren-Aktion');
      }
      return false;
    }
    if(action === 'refresh') {
      location.reload();
      return false;
    }
    // Erledigen von Vorgaengen: immer ueber den Abschluss-Assistenten
    if(action === 'done' && item.dataset.complete) {
      const [type, id] = item.dataset.complete.split(':');
      pdhComplete(type, id);
      return false;
    }
    if(['analyze','done','activate','deactivate','delete','start','stop'].includes(action)) {
      return await postSelectedAction(item, action);
    }
    setGlobalStatus('warn', 'Aktion nicht hinterlegt: ' + action, 100, 'Unbekannte Aktion');
  } catch(err) {
    showGlobalError('Aktion fehlgeschlagen', err.message || String(err));
  }
  return false;
}
document.addEventListener('click', (event) => {
  const item = event.target.closest('[data-selectable]');
  if(!item) return;
  if(event.target.closest('button,input,select,textarea,label,.widget-icon-toolbar,[data-ignore-select]')) return;
  const link = event.target.closest('a[href]');
  if(link && item.contains(link)) event.preventDefault();
  markSelectedListItem(item);
});
document.addEventListener('dblclick', (event) => {
  const item = event.target.closest('[data-selectable]');
  if(!item || event.target.closest('button,input,select,textarea,label,[data-ignore-select]')) return;
  const link = event.target.closest('a[href]');
  if(link && item.contains(link)) event.preventDefault();
  markSelectedListItem(item);
  runSelectedItemAction('view');
});
document.addEventListener('DOMContentLoaded', () => {
  setGlobalStatus('ok', 'Bereit', 100, 'Keine Fehler');
});
// ── Aufklappbare Listenzeilen ────────────────────────────
// Einheitliche Bedienung aller Listen und Tabellen: Klick (oder Enter) auf eine
// Zeile mit data-rx klappt sie auf, darunter erscheinen die Aktionen. Attribute:
//   data-rx-view="/url"   Ansehen (Schnellansicht im Overlay)
//   data-rx-edit="/url"   Bearbeiten (öffnet den Datensatz); Doppelklick ebenso
//   data-rx-done="art:id" Fertigstellen über den Abschluss-Assistenten
//                         (data-rx-done-label / data-rx-done-icon ändern Text/Symbol)
//   data-rx-title, data-rx-desc  Titel bzw. Kurztext im aufgeklappten Bereich
//   <template class="rx-more"> in der Zeile: weitere, seitenspezifische Knöpfe
(function(){
  const IGNORE = 'a,button,input,select,textarea,label,summary,[data-rx-ignore],.win11-menu';
  function close(row){
    const p = row && row.nextElementSibling;
    if(p && p.classList.contains('rx-panel')) p.remove();
    if(row){ row.classList.remove('rx-open'); row.setAttribute('aria-expanded', 'false'); }
  }
  function btn(tag, cls, icon, label){
    const b = document.createElement(tag);
    b.className = 'btn btn-small ' + cls;
    if(tag === 'button') b.type = 'button';
    const i = document.createElement('i'); i.className = 'ti ' + icon;
    b.append(i, ' ' + label);
    return b;
  }
  function build(row){
    const d = row.dataset, box = document.createElement('div');
    box.className = 'rx-box';
    const title = d.rxTitle || '';
    if(d.rxDesc){ const p = document.createElement('div'); p.className = 'rx-desc'; p.textContent = d.rxDesc; box.appendChild(p); }
    if(d.rxView){
      const b = btn('button', '', 'ti-eye', pdhT("Ansehen"));
      b.addEventListener('click', () => openPdhFrame(d.rxView, title));
      box.appendChild(b);
    }
    if(d.rxEdit){
      const a = btn('a', '', 'ti-edit', pdhT("Bearbeiten"));
      a.href = d.rxEdit;
      box.appendChild(a);
    }
    if(d.rxDone){
      const [kind, id] = d.rxDone.split(':');
      const b = btn('button', 'cw-finish', d.rxDoneIcon || 'ti-circle-check', d.rxDoneLabel || pdhT("Fertigstellen"));
      b.addEventListener('click', () => window.pdhComplete && window.pdhComplete(kind, id));
      box.appendChild(b);
    }
    const more = row.querySelector('template.rx-more');
    if(more){
      const g = document.createElement('span'); g.className = 'grow'; box.appendChild(g);
      box.appendChild(more.content.cloneNode(true));
    }
    if(row.tagName === 'TR'){
      const tr = document.createElement('tr'), td = document.createElement('td');
      tr.className = 'rx-panel';
      td.colSpan = [...row.cells].reduce((n, c) => n + (c.colSpan || 1), 0);
      td.appendChild(box); tr.appendChild(td);
      return tr;
    }
    const div = document.createElement('div');
    div.className = 'rx-panel'; div.appendChild(box);
    return div;
  }
  function toggle(row){
    const wasOpen = row.classList.contains('rx-open');
    document.querySelectorAll('[data-rx].rx-open').forEach(close);
    if(wasOpen) return;
    const panel = build(row);
    row.after(panel);
    row.classList.add('rx-open');
    row.setAttribute('aria-expanded', 'true');
    if(window.htmx) htmx.process(panel);
    const first = panel.querySelector('button,a');
    if(first && row.matches(':focus')) first.focus();
  }
  function prepare(root){
    (root || document).querySelectorAll('[data-rx]:not([tabindex])').forEach(r => {
      r.tabIndex = 0;
      r.setAttribute('aria-expanded', 'false');
      if(!r.title) r.title = pdhT("Klicken zum Aufklappen") + (r.dataset.rxEdit ? pdhT(" · Doppelklick öffnet") : '');
    });
  }
  window.pdhRowsPrepare = prepare;
  document.addEventListener('click', e => {
    const row = e.target.closest('[data-rx]');
    if(!row || e.target.closest(IGNORE)) return;
    toggle(row);
  });
  document.addEventListener('dblclick', e => {
    const row = e.target.closest('[data-rx]');
    if(!row || e.target.closest(IGNORE) || !row.dataset.rxEdit) return;
    location.href = row.dataset.rxEdit;
  });
  document.addEventListener('keydown', e => {
    const row = e.target.closest && e.target.closest('[data-rx]');
    if(!row || e.target !== row || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    toggle(row);
  });
  document.addEventListener('DOMContentLoaded', () => prepare());
  document.addEventListener('htmx:afterSwap', e => prepare(e.target));
})();
// Handshake: kurzer Puls, wenn ein Zähler seit dem letzten Abruf weitergezählt hat (handshake.gohtml)
window.pdhHS = window.pdhHS || {};
function pdhHandshakePulse(root){
  (root || document).querySelectorAll('[data-hs]').forEach(el => {
    const id = el.dataset.hs, c = el.dataset.count, prev = window.pdhHS[id];
    if(prev !== undefined && prev !== c){ el.classList.remove('hs-pulse'); void el.offsetWidth; el.classList.add('hs-pulse'); }
    window.pdhHS[id] = c;
  });
}
document.addEventListener('DOMContentLoaded', () => pdhHandshakePulse());
document.addEventListener('htmx:afterSwap', e => pdhHandshakePulse(e.target));
const pdhNativeFetch = window.fetch ? window.fetch.bind(window) : null;
if(pdhNativeFetch) {
  window.fetch = async function(input, init) {
    if(init && init.pdhQuiet) return pdhNativeFetch(input, init);
    setGlobalStatus('busy', pdhStatusWhat(document.activeElement, init && init.method));
    try {
      const response = await pdhNativeFetch(input, init);
      if(response.ok) setGlobalStatus('ok', 'Fertig', 100, 'Keine Fehler');
      else showGlobalError('HTTP ' + response.status, response.statusText || 'Request fehlgeschlagen');
      return response;
    } catch(err) {
      showGlobalError('Netzwerkfehler', err.message || String(err));
      throw err;
    }
  };
}
// Hintergrund-Abfragen (hx-trigger „every …“, Nachladen beim Öffnen) zeigen keinen Balken
function pdhHtmxQuiet(event) {
  const elt = event.detail && event.detail.elt;
  const trig = elt && elt.getAttribute ? (elt.getAttribute('hx-trigger') || '') : '';
  return /every|load|revealed|intersect/.test(trig) && !(event.detail.requestConfig && event.detail.requestConfig.triggeringEvent && event.detail.requestConfig.triggeringEvent.isTrusted);
}
document.body.addEventListener('htmx:beforeRequest', (event) => {
  if(pdhHtmxQuiet(event)) return;
  const verb = event.detail.requestConfig ? event.detail.requestConfig.verb : 'get';
  setGlobalStatus('busy', pdhStatusWhat(event.detail.elt, verb));
});
document.body.addEventListener('htmx:xhr:progress', (event) => {
  if(event.detail && event.detail.lengthComputable && event.detail.total > 0) pdhStatusProgress(event.detail.loaded / event.detail.total * 95);
});
document.body.addEventListener('htmx:afterRequest', (event) => {
  const xhr = event.detail && event.detail.xhr;
  if(xhr && xhr.status === 423) showGlobalError('Gesperrt', xhr.responseText || 'Datensatz gesperrt');
  else if(xhr && xhr.status === 409 && xhr.getResponseHeader('X-PDH-Spiegel')) showGlobalError('Spiegel – nur lesen', xhr.responseText || 'Änderungen bitte am aktiven Server.');
  else if(xhr && xhr.status === 403 && event.detail.elt && event.detail.elt.hasAttribute && event.detail.elt.hasAttribute('data-quiet-denied')) { /* leise: Status anderer Anfragen nicht anfassen */ }
  else if(xhr && xhr.status >= 400) showGlobalError('HTTP ' + xhr.status, xhr.statusText || 'HTMX-Fehler');
  else if(!pdhHtmxQuiet(event)) setGlobalStatus('ok', 'Fertig', 100, 'Keine Fehler');
});
// normale Formulare und Seitenwechsel: Balken läuft, bis die neue Seite da ist
document.addEventListener('submit', (event) => {
  window.setTimeout(() => {
    const f = event.target;
    if(event.defaultPrevented || !f || f.target === '_blank' || f.hasAttribute('hx-post') || f.hasAttribute('hx-get')) return;
    setGlobalStatus('busy', pdhStatusWhat(event.submitter || f, f.method));
  }, 0);
});
window.addEventListener('pageshow', (event) => { if(event.persisted) setGlobalStatus('ok', 'Bereit', 0, 'Keine Fehler'); });
document.body.addEventListener('htmx:responseError', (event) => {
  const xhr = event.detail && event.detail.xhr;
  // Nachgeladene Teile, die ohne Recht leise abgelehnt werden (z. B. Historie): Hinweis statt Fehlermeldung
  const elt = event.detail && event.detail.elt;
  if(xhr && xhr.status === 403 && elt && elt.hasAttribute && elt.hasAttribute('data-quiet-denied')) { const note = document.createElement('div'); note.style.cssText = 'font-size:12px;color:var(--muted);padding:8px 0'; note.textContent = elt.getAttribute('data-quiet-denied'); elt.replaceChildren(note); return; }
  showGlobalError('HTMX-Fehler', xhr ? ('HTTP ' + xhr.status + ' ' + xhr.statusText) : 'Antwortfehler');
});
document.body.addEventListener('htmx:sendError', () => showGlobalError('HTMX-Sendefehler', 'Anfrage konnte nicht gesendet werden'));
window.addEventListener('error', (event) => showGlobalError('JavaScript-Fehler', event.message || 'Unbekannter Fehler'));
window.addEventListener('unhandledrejection', (event) => showGlobalError('Promise-Fehler', event.reason && event.reason.message ? event.reason.message : String(event.reason || 'Unbekannter Fehler')));

// Copilot in der Seitenleiste: auf einer Stoerung mit deren Kontext, sonst allgemein
async function sendCop() {
  const input = document.getElementById('cop-in');
  const q = input.value.trim();
  if(!q) return;
  const msgs = document.getElementById('cop-msgs');
  const add = (cls, text, style) => { const d = document.createElement('div'); d.className = cls; if(style) d.style.cssText = style; d.textContent = text; msgs.appendChild(d); msgs.scrollTop = msgs.scrollHeight; return d; };
  add('cop-q', 'Du: ' + q);
  input.value = '';
  const wait = add('cop-a', 'Copilot denkt nach …', 'color:var(--muted)');
  try{
    const res = await fetch('/copilot/ask', {method: 'POST', credentials: 'same-origin', body: new URLSearchParams({q, fault: PDH_BOOT.faultID, conn: PDH_BOOT.copilotConn, web: (document.getElementById('cop-web') || {}).checked ? '1' : ''})});
    const raw = await res.text();
    let j = {};
    try{ j = JSON.parse(raw); }catch(e){}
    wait.remove();
    if(!j.reply){
      // keine JSON-Antwort: meist Zeitlimit oder Fehlerseite eines Proxys davor
      let why = j.error;
      if(!why) why = 'HTTP ' + res.status + (res.status === 502 || res.status === 504 || res.status === 524
        ? ' – die Antwort kam nicht durch (Zeitlimit oder Proxy vor PDH). Details im Server-Protokoll unter „copilot“.'
        : (raw ? ' – ' + raw.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 160) : ''));
      add('cop-a', 'Copilot: ' + why, 'color:var(--red)');
    }
    else {
      // Antwort als Text; PDH-Pfade (/faults/…, /inventory/…) werden zu Links
      const a = add('cop-a', 'Copilot: ');
      String(j.reply).split(/(\/(?:faults|tickets|tasks|maintenance\/tasks|projects|infrastructure|inventory)\/[0-9a-fA-F-]{36})/).forEach(function(part, i){
        if(i % 2){ const l = document.createElement('a'); l.href = part; l.textContent = part; l.style.color = 'var(--accent)'; a.appendChild(l); }
        else a.appendChild(document.createTextNode(part));
      });
      if(j.sources && j.sources.length) add('cop-a', 'Aus dem PDH: ' + j.sources.filter((s, i, all) => all.indexOf(s) === i).join(', '), 'color:var(--muted);font-size:11px');
      if(j.links && j.links.length) copRenderLinks(msgs, j.links);
      if(j.proposal) copRenderProposal(msgs, j.proposal);
    }
  }catch(e){ wait.remove(); add('cop-a', 'Copilot nicht erreichbar: ' + e.message, 'color:var(--red)'); }
}

// Quellen aus dem Internet (Websuche des Copilot auf Verbindungsseiten)
function copRenderLinks(msgs, links) {
  const box = document.createElement('div');
  box.className = 'cop-links';
  box.appendChild(document.createTextNode('Quellen aus dem Internet:'));
  links.forEach(function(l){
    if(!l || !/^https?:\/\//i.test(l.url || '')) return;
    const a = document.createElement('a');
    a.href = l.url; a.target = '_blank'; a.rel = 'noopener noreferrer';
    a.textContent = l.title || l.url;
    box.appendChild(a);
  });
  msgs.appendChild(box);
}

// Änderungsliste des Copilot (copilot_mapping.go): Zeilen mit Haken, angelegt
// wird erst mit „Übernehmen“ (POST /copilot/proposals/{id}/apply).
function copRenderProposal(msgs, p) {
  const el = (tag, cls, text) => { const e = document.createElement(tag); if(cls) e.className = cls; if(text) e.textContent = text; return e; };
  const card = el('div', 'cop-diff');
  card.appendChild(el('div', 'cop-diff-head', p.summary || 'Vorschlag'));
  const rows = p.rows || [];
  const usable = r => r.valid && !r.applied && (r.op === 'add' || r.op === 'change');
  const nAdd = rows.filter(r => usable(r) && r.op === 'add').length;
  const nChg = rows.filter(r => usable(r) && r.op === 'change').length;
  card.appendChild(el('div', 'cop-diff-count', '+' + nAdd + ' neu · ~' + nChg + ' geändert'));
  const boxes = [];
  rows.forEach(function(r, i){
    const row = el('div', 'cop-diff-row');
    let sign = '–', cls = '', state = '';
    if(r.op === 'keep'){ sign = '='; state = 'unverändert'; }
    else if(r.op === 'delete'){ sign = '✕'; state = 'löschen – bitte von Hand'; }
    else if(!r.valid && !r.applied){ state = 'wird nicht angefasst'; }
    else if(r.op === 'add'){ sign = '+'; cls = 'add'; }
    else { sign = '~'; cls = 'chg'; }
    if(usable(r)){
      const cb = document.createElement('input');
      cb.type = 'checkbox'; cb.checked = true; cb.dataset.i = i;
      cb.setAttribute('aria-label', 'Zeile ' + (i + 1) + ' übernehmen');
      row.appendChild(cb); boxes.push(cb);
    } else { row.classList.add('off'); row.appendChild(el('span', 'nobox')); }
    row.appendChild(el('span', 'cop-diff-sign' + (cls ? ' ' + cls : ''), sign));
    const body = el('div', 'cop-diff-body');
    const what = p.direction === 'export' ? (r.field || '') : (r.name || '');
    body.appendChild(el('div', '', (r.label || '') + (r.asset ? ' → ' + r.asset : '') + (what ? ' · ' + what : '')));
    if(r.formula) body.appendChild(el('div', 'm', '= ' + r.formula));
    if(r.probe) body.appendChild(el('div', 'm', 'Probe mit den Werten von jetzt: ' + r.probe));
    if(r.valid_from) body.appendChild(el('div', 'm', 'gültig ab ' + r.valid_from));
    if(r.note) body.appendChild(el('div', 'm', r.note));
    if(r.reason) body.appendChild(el('div', 'm', r.reason));
    if(state) body.appendChild(el('div', 'm', state));
    if(r.applied) body.appendChild(el('div', 'm', 'schon übernommen'));
    if(r.problem && r.op !== 'delete') body.appendChild(el('div', 'p', r.problem));
    if(r.url && /^https?:\/\//i.test(r.url)){
      const a = el('a', '', 'Quelle'); a.href = r.url; a.target = '_blank'; a.rel = 'noopener noreferrer';
      body.appendChild(a);
    }
    row.appendChild(body);
    card.appendChild(row);
  });
  const foot = el('div', 'cop-diff-foot');
  const btn = el('button', 'cop-btn', 'Übernehmen');
  btn.type = 'button';
  btn.disabled = !boxes.length;
  const res = el('div', 'cop-diff-res', boxes.length ? 'Noch ist nichts angelegt.' : 'Nichts zu übernehmen.');
  btn.addEventListener('click', async function(){
    const picked = boxes.filter(b => b.checked && !b.disabled).map(b => b.dataset.i);
    if(!picked.length){ res.textContent = 'Bitte mindestens eine Zeile auswählen.'; return; }
    btn.disabled = true;
    res.textContent = 'Wird übernommen …';
    try{
      const r = await fetch('/copilot/proposals/' + encodeURIComponent(p.id) + '/apply', {method: 'POST', credentials: 'same-origin', headers: csrfHeaders(), body: new URLSearchParams({rows: picked.join(',')})});
      let j = {};
      try{ j = await r.json(); }catch(e){}
      res.replaceChildren();
      if(!r.ok || j.error){ res.appendChild(el('div', 'p', j.error || ('HTTP ' + r.status))); btn.disabled = false; return; }
      const parts = [(j.created || 0) + ' angelegt', (j.changed || 0) + ' geändert'];
      if(j.skipped) parts.push(j.skipped + ' schon vorhanden');
      res.appendChild(el('div', '', parts.join(', ') + '.'));
      const failed = {};
      (j.errors || []).forEach(function(e){ failed[e.index] = true; res.appendChild(el('div', 'p', (e.index >= 0 ? 'Zeile ' + (e.index + 1) + ': ' : '') + e.error)); });
      boxes.forEach(function(b){ if(picked.indexOf(b.dataset.i) >= 0 && !failed[b.dataset.i]){ b.checked = false; b.disabled = true; } });
      const again = el('a', '', 'Seite neu laden');
      again.href = '#';
      again.addEventListener('click', function(ev){ ev.preventDefault(); location.reload(); });
      res.appendChild(again);
      btn.disabled = !boxes.some(b => !b.disabled);
    }catch(e){ res.replaceChildren(el('div', 'p', 'Nicht erreichbar: ' + e.message)); btn.disabled = false; }
  });
  foot.appendChild(btn);
  foot.appendChild(res);
  card.appendChild(foot);
  msgs.appendChild(card);
  msgs.scrollTop = Math.max(0, card.offsetTop - msgs.offsetTop - 4); // Anfang der Liste zeigen, nicht das Ende
}

// ── Copilot-Vorschläge zu aktiven Störungen und Tickets (copilot_suggest.go) ──
// Auf der Detailseite (CopilotRef) und im Behebungs-Assistenten lädt die
// Seitenleiste automatisch Dopplungen, bewährte Lösungen und Ersatzteile aus der
// Datensammlung. Solange sie ungelesen sind, blinkt der Copilot-Reiter gelb.
(function(){
  const PAGE_REF = PDH_BOOT.copilotRef;
  const cache = {};
  let shown = '', wizard = false;
  const KIND = {fault: 'Störung', ticket: 'Ticket'};
  function el(tag, cls, text){ const e = document.createElement(tag); if(cls) e.className = cls; if(text != null) e.textContent = text; return e; }
  function link(r){ const a = el('a', '', r.title); a.href = r.url; a.setAttribute('data-frame', ''); a.dataset.frameTitle = r.title; return a; }
  function pill(text, cls){ return el('span', 'cop-pill' + (cls ? ' ' + cls : ''), text); }
  function seenKey(ref, d){ return ref + '#' + d.duplicates.length + '/' + d.cases.map(c => c.id).join(','); }
  function isSeen(ref, d){ try{ return sessionStorage.getItem('pdh_cop_seen_' + ref) === seenKey(ref, d); }catch(e){ return false; } }
  function copilotVisible(){
    return !document.documentElement.classList.contains('sidebar-right-collapsed') && localStorage.getItem('pdh_context_tab') === 'copilot';
  }
  function setAlert(on){
    document.querySelectorAll('[data-context-tab="copilot"], .topbar-ctx, .topbar-cop').forEach(b => b.classList.toggle('cop-alert', on));
    const head = document.querySelector('#cw .cw-head');
    let btn = document.getElementById('cw-cop');
    const d = cache[shown];
    if(wizard && head && d && d.count){
      if(!btn){
        btn = el('button', 'cw-cop'); btn.type = 'button'; btn.id = 'cw-cop';
        btn.addEventListener('click', () => activateContextTab('copilot', {keepOpen: true}));
        head.appendChild(btn);
      }
      btn.replaceChildren(el('i', 'ti ti-bulb'), document.createTextNode(' Copilot: ' + d.count + (d.count === 1 ? ' Vorschlag' : ' Vorschläge') + ' aus euren Daten'));
      btn.style.display = '';
      btn.style.animation = on ? '' : 'none';
    } else if(btn) btn.style.display = 'none';
  }
  function render(ref){
    const box = document.getElementById('cop-suggest');
    const d = cache[ref];
    shown = ref;
    if(!box) return;
    box.replaceChildren();
    if(!d || !d.count && !d.parts.length){ box.hidden = true; setAlert(false); return; }
    box.hidden = false;
    const [kind] = ref.split(':');
    if(d.duplicates.length){
      box.appendChild(el('h4', 'warn', '⚠ Mögliche Dopplung – gibt es schon'));
      d.duplicates.forEach(x => {
        const c = el('div', 'cop-card dup');
        c.append(el('span', 'm', KIND[x.type] + ' · '), link(x), pill(x.percent + ' %'));
        c.appendChild(el('div', 'm', [x.status, x.infra, x.same_asset ? 'gleiche Anlage' : ''].filter(Boolean).join(' · ')));
        box.appendChild(c);
      });
    }
    if(d.cases.length){
      box.appendChild(el('h4', '', '💡 Bewährte Lösungen'));
      d.cases.forEach(x => {
        const c = el('div', 'cop-card');
        c.append(el('span', 'm', KIND[x.type] + ' · '), link(x), pill(x.percent + ' %'));
        if(x.count > 1) c.appendChild(pill(x.count + '× so gelöst', 'g'));
        if(x.same_asset) c.appendChild(pill('gleiche Anlage'));
        else if(x.infra) c.appendChild(el('div', 'm', x.infra));
        if(x.root_cause){ c.appendChild(el('div', 'l', 'Ursache')); c.appendChild(el('div', '', x.root_cause)); }
        if(x.resolution){ c.appendChild(el('div', 'l', 'Lösung')); c.appendChild(el('div', '', x.resolution)); }
        if(x.actions.length){ c.appendChild(el('div', 'l', 'Maßnahmen')); const ul = el('ul'); x.actions.forEach(a => ul.appendChild(el('li', '', a))); c.appendChild(ul); }
        if(x.parts && x.parts.length){ c.appendChild(el('div', 'l', 'Ersatzteile')); c.appendChild(el('div', '', x.parts.join(', '))); }
        if(x.more.length){
          const m = el('div', 'm', 'auch: ');
          x.more.forEach((r, i) => { if(i) m.append(', '); m.appendChild(link(r)); });
          c.appendChild(m);
        }
        box.appendChild(c);
      });
    }
    if(d.parts.length){
      box.appendChild(el('h4', '', '📦 Oft benötigte Ersatzteile'));
      const c = el('div', 'cop-card');
      d.parts.forEach(p => {
        const row = el('div');
        row.append(link({title: p.name, url: p.url}), el('span', 'm', ' ' + p.number + ' · in ' + p.cases + (p.cases === 1 ? ' Fall' : ' Fällen')));
        row.appendChild(pill('Bestand ' + (Math.round(p.stock * 1000) / 1000).toLocaleString('de-DE') + (p.unit ? ' ' + p.unit : ''), p.stock > 0 ? 'g' : 'r'));
        c.appendChild(row);
      });
      box.appendChild(c);
    }
    box.appendChild(el('div', 'm', 'Vorschläge aus euren Daten – ' + (kind === 'ticket' ? 'Tickets' : 'Störungen') + ' und Tickets mit ähnlicher Beschreibung. Für Details unten fragen.'));
    setAlert(d.count > 0 && !(copilotVisible() || isSeen(ref, d)));
  }
  async function load(ref){
    if(!cache[ref]){
      const [type, id] = ref.split(':');
      try{
        const res = await fetch('/copilot/suggest?type=' + encodeURIComponent(type) + '&id=' + encodeURIComponent(id), {credentials: 'same-origin'});
        if(!res.ok) return;
        const d = await res.json();
        if(!d || !d.active) return;
        cache[ref] = d;
      }catch(e){ return; }
    }
    render(ref);
  }
  // gelesen: Copilot-Reiter geöffnet
  window.pdhCopilotSeen = function(){
    const d = cache[shown];
    if(d){ try{ sessionStorage.setItem('pdh_cop_seen_' + shown, seenKey(shown, d)); }catch(e){} }
    setAlert(false);
  };
  // Behebungs-Assistent: pdhCopilotFor(art, id, true) beim Öffnen, pdhCopilotFor(null) beim Schließen
  window.pdhCopilotFor = function(type, id){
    if(!type){
      wizard = false;
      document.documentElement.classList.remove('cop-raise');
      if(PAGE_REF && cache[PAGE_REF]) render(PAGE_REF); else setAlert(false);
      return;
    }
    if(!KIND[type]) return;
    wizard = true;
    document.documentElement.classList.add('cop-raise');
    const ref = type + ':' + id;
    try{ sessionStorage.removeItem('pdh_cop_seen_' + ref); }catch(e){} // in der Behebung erneut hinweisen
    load(ref);
  };
  if(PAGE_REF){
    if(document.readyState === 'loading') document.addEventListener('DOMContentLoaded', () => load(PAGE_REF)); else load(PAGE_REF);
  }
})();
