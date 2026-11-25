function attachmentToken(){
  return document.cookie.split(';').find(c=>c.trim().startsWith('pdh_token='))?.split('=')[1] || localStorage.getItem('pdh_token') || '';
}
function escapeAttr(value){
  return String(value || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
function attachmentCard(a, refType, refId){
  const name = escapeAttr(a.filename || 'Datei');
  const url = escapeAttr(a.url || ('/uploads/' + (a.filepath || '')));
  const isImage = !!a.is_image;
  const recordBadge = a.record_image ? '<span class="attachment-badge">Datensatzbild</span>' : '';
  // aus dem Internet uebernommene Dokumente: Art, Stand und Quelle
  const docLabel = ({datasheet: 'Datenblatt', manual: 'Benutzerhandbuch'})[a.doc_kind] || '';
  const docBadge = docLabel ? `<span class="attachment-badge doc">${docLabel}</span>` : '';
  const preview = isImage ? `<img src="${url}" alt="${name}">${recordBadge}` : `<i class="ti ${a.doc_kind === 'manual' ? 'ti-book' : 'ti-file-description'}"></i>${docBadge}`;
  const checked = a.source_checked_at ? new Date(a.source_checked_at).toLocaleDateString(PDH_LOCALE) : '';
  const srcLine = a.source_url ? `<div class="attachment-src" title="${escapeAttr(a.source_url)}">Stand ${checked} · <a href="${escapeAttr(a.source_url)}" target="_blank" rel="noopener noreferrer">Quelle</a></div>` : '';
  const refreshButton = a.source_url && refType === 'inventory'
    ? `<button type="button" class="btn" onclick="pdhRefreshDoc('${refId}','${a.id}',this)" title="Beim Anbieter nach einer neueren Fassung schauen"><i class="ti ti-refresh"></i> Aktualisieren</button>`
    : '';
  const imageButton = isImage && !a.record_image
    ? `<button type="button" class="btn" onclick="setRecordImage('${refType}','${refId}','${a.id}')"><i class="ti ti-photo-check"></i> Aktivieren</button>`
    : '';
  return `<div class="attachment-card">
    <a class="attachment-preview" href="${url}" target="_blank" rel="noopener">${preview}</a>
    <div class="attachment-meta">
      <div class="attachment-name" title="${name}">${name}</div>
      ${srcLine}
      <div class="attachment-actions">
        ${imageButton}${refreshButton}
        <button type="button" class="btn btn-danger" onclick="deleteAttachment('${a.id}','${refType}','${refId}')"><i class="ti ti-trash"></i> Entfernen</button>
      </div>
    </div>
  </div>`;
}
async function loadAttachmentGallery(refType, refId) {
  const container = document.getElementById('attachments-'+refId);
  if(!container) return;
  container.classList.add('attachment-gallery');
  container.style.display = 'grid';
  container.style.flexWrap = '';
  container.style.gap = '10px';
  container.innerHTML = '<div style="font-size:12px;color:var(--muted)">Lade Galerie...</div>';
  try {
    const res = await fetch('/api/v1/attachments/'+refType+'/'+refId, {headers:{Authorization:'Bearer '+attachmentToken()}});
    const json = await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Galerie konnte nicht geladen werden');
    const files = json.data || [];
    container.innerHTML = files.length ? files.map(a => attachmentCard(a, refType, refId)).join('') : '<div style="font-size:12px;color:var(--muted)">Keine Bilder oder Dokumente vorhanden</div>';
  } catch(err) {
    container.innerHTML = '<div style="font-size:12px;color:var(--red)">Fehler: '+escapeAttr(err.message || err)+'</div>';
  }
}
async function uploadFiles(input, refType, refId) {
  if(!input.files.length) return;
  const fd = new FormData();
  for(const f of input.files) fd.append('files', f);
  try {
    const res = await fetch('/api/v1/attachments/'+refType+'/'+refId, {
      method:'POST',
      headers:{Authorization:'Bearer '+attachmentToken()},
      body:fd
    });
    const json = await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Upload fehlgeschlagen');
    input.value = '';
    await loadAttachmentGallery(refType, refId);
  } catch(err) {
    showGlobalError('Upload fehlgeschlagen', err.message || String(err));
  }
}
async function deleteAttachment(id, refType, refId) {
  try {
    const res = await fetch('/api/v1/attachments/'+id, {method:'DELETE',headers:{Authorization:'Bearer '+attachmentToken()}});
    const json = await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Loeschen fehlgeschlagen');
    await loadAttachmentGallery(refType, refId);
  } catch(err) {
    showGlobalError('Anhang konnte nicht entfernt werden', err.message || String(err));
  }
}
async function setRecordImage(refType, refId, id) {
  try {
    const res = await fetch('/api/v1/attachments/'+refType+'/'+refId+'/'+id+'/record-image', {method:'POST',headers:{Authorization:'Bearer '+attachmentToken()}});
    const json = await res.json();
    if(!res.ok || !json.success) throw new Error(json.error || 'Datensatzbild konnte nicht gesetzt werden');
    await loadAttachmentGallery(refType, refId);
    setGlobalStatus('ok', 'Datensatzbild gesetzt', 100, 'Keine Fehler');
  } catch(err) {
    showGlobalError('Datensatzbild fehlgeschlagen', err.message || String(err));
  }
}
// Seitenleisten in der Breite ziehen: Griff am Innenrand, Breite bleibt im
// Browser gespeichert; Doppelklick = Standard, Pfeiltasten = 16 px.
(function(){
  const root = document.documentElement, app = document.querySelector('.app');
  if(!app) return;
  const LIM = {left: [160, 480, 200], right: [220, 760, 260]}; // min, max, Standard
  const MAIN_MIN = 420; // Inhalt in der Mitte bleibt bedienbar
  function cur(side){ return parseInt(getComputedStyle(root).getPropertyValue('--' + side + '-open-w'), 10) || LIM[side][2]; }
  function set(side, w, save){
    const other = side === 'left' ? 'right' : 'left';
    const otherW = root.classList.contains('sidebar-' + other + '-collapsed') ? 54 : (parseInt(getComputedStyle(root).getPropertyValue('--' + other + '-sidebar-w'), 10) || 0);
    const max = Math.min(LIM[side][1], app.clientWidth - otherW - MAIN_MIN);
    w = Math.round(Math.max(LIM[side][0], Math.min(max, w)));
    root.style.setProperty('--' + side + '-open-w', w + 'px');
    if(save){ try{ localStorage.setItem('pdh_sidebar_' + side + '_w', String(w)); }catch(e){} window.dispatchEvent(new Event('resize')); }
    return w;
  }
  document.querySelectorAll('.pdh-resizer').forEach(el => {
    const side = el.classList.contains('left') ? 'left' : 'right';
    el.addEventListener('pointerdown', e => {
      if(e.button !== 0) return;
      e.preventDefault();
      el.setPointerCapture(e.pointerId);
      el.classList.add('on'); root.classList.add('pdh-resizing');
      const r = app.getBoundingClientRect();
      const move = ev => set(side, side === 'left' ? ev.clientX - r.left : r.right - ev.clientX, false);
      const up = () => {
        el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up);
        el.classList.remove('on'); root.classList.remove('pdh-resizing');
        set(side, cur(side), true);
      };
      el.addEventListener('pointermove', move); el.addEventListener('pointerup', up); el.addEventListener('pointercancel', up);
    });
    el.addEventListener('dblclick', () => {
      root.style.removeProperty('--' + side + '-open-w');
      try{ localStorage.removeItem('pdh_sidebar_' + side + '_w'); }catch(e){}
      window.dispatchEvent(new Event('resize'));
    });
    el.addEventListener('keydown', e => {
      if(e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      e.preventDefault();
      const grow = (e.key === 'ArrowRight') === (side === 'left');
      set(side, cur(side) + (grow ? 16 : -16), true);
    });
  });
})();
// Dokument (oder 'all') erneut von der Quelle laden – ersetzt nur bei neuer Fassung
async function pdhRefreshDoc(partId, attId, btn) {
  if(btn) btn.disabled = true;
  setGlobalStatus('busy', 'Prüfe auf neue Fassung…', 40, '');
  try {
    const res = await fetch('/inventory/' + partId + '/documents/' + attId + '/refresh', {method: 'POST', credentials: 'same-origin'});
    const json = await res.json().catch(() => ({}));
    if(!res.ok || !json.success) throw new Error(json.error || ('HTTP ' + res.status));
    await loadAttachmentGallery('inventory', partId);
    setGlobalStatus(json.failed ? 'error' : 'ok', json.message, 100, json.failed ? 'Teilweise fehlgeschlagen' : 'Keine Fehler');
  } catch(err) {
    showGlobalError('Aktualisieren fehlgeschlagen', err.message || String(err));
  }
  if(btn) btn.disabled = false;
}
function initAttachmentGalleries(){
  document.querySelectorAll('[data-attach-ref]').forEach(el => {
    const [refType, refId] = String(el.dataset.attachRef || '').split(':');
    if(refType && refId) loadAttachmentGallery(refType, refId);
  });
}
if(document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initAttachmentGalleries);
else initAttachmentGalleries();
// Druckkopf: Datum beim Drucken eintragen
window.addEventListener("beforeprint", function(){ document.querySelectorAll("[data-print-date]").forEach(function(el){ el.textContent = new Date().toLocaleString(PDH_LOCALE); }); });
