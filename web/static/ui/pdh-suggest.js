// Lernende Autovervollstaendigung fuer Felder mit data-suggest="<art>" (suggest.go):
// Vorschlaege fuer die aktuelle Zeile; Tab/Klick uebernimmt, Pfeile waehlen, Esc schliesst.
(function(){
  let box = null, field = null, list = [], sel = 0, timer = null, seq = 0;
  function close(){ if(box){ box.remove(); box = null; } list = []; }
  function lineInfo(el){
    const pos = el.selectionStart ?? el.value.length, v = el.value;
    const start = el.tagName === 'TEXTAREA' ? v.lastIndexOf('\n', pos - 1) + 1 : 0;
    return {start, pos, line: v.slice(start, pos)};
  }
  function apply(i){
    const it = list[i]; if(!it || !field) return;
    const {start, pos, line} = lineInfo(field);
    const v = field.value;
    // Einrueckung/Aufzaehlungszeichen der Zeile behalten
    const lead = (line.match(/^[\s\-•*·]*/) || [''])[0];
    const repl = it.ghost ? line + it.ghost : lead + it.text;
    field.value = v.slice(0, start) + repl + v.slice(pos);
    const caret = start + repl.length;
    field.setSelectionRange(caret, caret);
    field.dispatchEvent(new Event('input', {bubbles: true}));
    const learned = it.ghost ? (line + it.ghost).replace(/^[\s\-•*·]+/, '') : it.text;
    fetch('/suggest/accept', {method: 'POST', credentials: 'same-origin', body: new URLSearchParams({k: field.dataset.suggest, text: learned})}).catch(()=>{});
    close();
  }
  function render(){
    if(!list.length){ close(); return; }
    if(!box){ box = document.createElement('div'); box.className = 'pdh-sg'; box.setAttribute('role', 'listbox'); document.body.appendChild(box); }
    const r = field.getBoundingClientRect();
    box.style.left = Math.max(8, Math.min(r.left, innerWidth - 300)) + 'px';
    box.style.minWidth = Math.min(r.width, 420) + 'px';
    const below = innerHeight - r.bottom > 180;
    box.style.top = below ? (r.bottom + 2) + 'px' : 'auto';
    box.style.bottom = below ? 'auto' : (innerHeight - r.top + 2) + 'px';
    box.innerHTML = list.map((it, i) => '<div role="option" data-i="' + i + '" class="' + (i === sel ? 'on' : '') + '">' +
      (it.ghost ? escapeHtml(it.line) + '<span class="g">' + escapeHtml(it.ghost) + '</span>' : escapeHtml(it.text)) + '</div>').join('') +
      '<div class="hint">Tab übernehmen · ↑↓ wählen · Esc schließen</div>';
  }
  async function query(el){
    const {line} = lineInfo(el);
    if(line.trim().length < 2){ close(); return; }
    const my = ++seq;
    try{
      const res = await fetch('/suggest?k=' + encodeURIComponent(el.dataset.suggest) + '&q=' + encodeURIComponent(line), {credentials: 'same-origin'});
      const j = await res.json();
      if(my !== seq || document.activeElement !== el) return;
      field = el; sel = 0; list = [];
      if(j.ghost) list.push({ghost: j.ghost, line});
      (j.items || []).forEach(t => { if(!(j.ghost && t === line + j.ghost)) list.push({text: t}); });
      render();
    }catch(e){ close(); }
  }
  document.addEventListener('input', e => {
    const el = e.target;
    if(!el.matches || !el.matches('[data-suggest]') || el === field && e.isTrusted === false) return;
    clearTimeout(timer); timer = setTimeout(() => query(el), 140);
  });
  document.addEventListener('keydown', e => {
    if(!box || e.target !== field) return;
    if(e.key === 'Tab' && !e.shiftKey){ e.preventDefault(); apply(sel); }
    else if(e.key === 'ArrowDown'){ e.preventDefault(); sel = (sel + 1) % list.length; render(); }
    else if(e.key === 'ArrowUp'){ e.preventDefault(); sel = (sel - 1 + list.length) % list.length; render(); }
    else if(e.key === 'Enter' && sel > 0){ e.preventDefault(); apply(sel); }
    else if(e.key === 'Escape'){ close(); }
  }, true);
  document.addEventListener('mousedown', e => {
    const d = e.target.closest && e.target.closest('.pdh-sg [data-i]');
    if(d){ e.preventDefault(); apply(+d.dataset.i); }
    else if(box && !e.target.closest('.pdh-sg')) close();
  });
  document.addEventListener('focusout', e => { if(e.target === field) setTimeout(close, 150); });
  window.addEventListener('scroll', () => { if(box) render(); }, true);
})();
