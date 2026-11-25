  // Einklappbare Navigationsgruppen (Akkordeon): offen ist nur die Gruppe mit
  // der geöffneten Seite; wer eine andere aufklappt, schließt die übrigen.
  (function(){
    try{ localStorage.removeItem('pdh_nav_fold'); }catch(e){}
    document.querySelectorAll('.nav-fold').forEach(function(b){
      const g = document.querySelector('[data-fold-group="' + b.dataset.fold + '"]');
      const folded = !(g && g.querySelector('.nav-item.active'));
      b.setAttribute('aria-expanded', folded ? 'false' : 'true');
      if(g) g.classList.toggle('folded', folded);
    });
  })();
  // Navigation anpassen: Gruppen umbenennen/anlegen/loeschen, Eintraege ziehen (nav.go)
  let pdhNavSortables = [];
  function pdhNavEdit(on){
    const html = document.documentElement;
    if(on && html.classList.contains('sidebar-left-collapsed')) toggleSidebar('left');
    if(on && typeof pdhNav === 'function') pdhNav(true); // schmale Ansicht: Navigation einblenden
    html.classList.toggle('nav-editing', on);
    document.querySelectorAll('.nav-edit-tools,.nav-edit-hint').forEach(e => e.style.display = on ? '' : 'none');
    if(!on){ pdhNavSortables.forEach(s => s.destroy()); pdhNavSortables = []; return; }
    document.querySelectorAll('#pdh-nav .nav-fold').forEach(pdhNavHeaderEdit);
    const start = () => document.querySelectorAll('#pdh-nav .nav-group').forEach(g => pdhNavSortables.push(Sortable.create(g, {group: 'pdh-nav', animation: 150, ghostClass: 'nav-ghost', draggable: '.nav-item'})));
    if(window.Sortable) start();
    else { const sc = document.createElement('script'); sc.src = PDH_BOOT.sortableURL; sc.onload = start; document.head.appendChild(sc); }
    document.querySelectorAll('#pdh-nav .nav-item').forEach(a => a.addEventListener('click', pdhNavNoClick));
  }
  // Dashboard anpassen (Benutzermenue): auf dem Dashboard direkt, sonst dorthin wechseln
  function pdhDashboardEdit(){
    if(typeof window.dwEdit === 'function' && document.getElementById('dw-root')){ window.dwEdit(true); document.getElementById('dw-root').scrollIntoView({block: 'start'}); }
    else location.href = '/?dwedit=1';
  }
  function pdhNavNoClick(e){ if(document.documentElement.classList.contains('nav-editing')) e.preventDefault(); }
  function pdhNavHeaderEdit(btn){
    if(btn.querySelector('input')) return;
    const span = btn.querySelector('.nav-gl');
    const inp = document.createElement('input');
    inp.className = 'nav-gl-input'; inp.maxLength = 40; inp.value = span.textContent;
    inp.addEventListener('click', e => e.stopPropagation());
    inp.addEventListener('keydown', e => { if(e.key === ' ' || e.key === 'Enter') e.stopPropagation(); });
    span.style.display = 'none'; btn.insertBefore(inp, span);
    const del = document.createElement('span');
    del.className = 'nav-gdel'; del.title = pdhT("Gruppe löschen (Einträge wandern nach oben)"); del.innerHTML = '<i class="ti ti-trash"></i>';
    del.addEventListener('click', e => {
      e.stopPropagation();
      const g = document.querySelector('[data-fold-group="' + btn.dataset.fold + '"]');
      const top = document.querySelector('[data-fold-group="top"]');
      if(g && top) g.querySelectorAll('.nav-item').forEach(a => top.appendChild(a));
      if(g) g.remove(); btn.remove();
    });
    btn.appendChild(del);
    // Gruppe nach oben/unten verschieben (Kopf und Einträge zusammen)
    [['up', 'ti-arrow-up', pdhT("Gruppe nach oben")], ['down', 'ti-arrow-down', pdhT("Gruppe nach unten")]].forEach(function(m){
      const b = document.createElement('span');
      b.className = 'nav-gmove'; b.title = m[2]; b.setAttribute('role', 'button'); b.setAttribute('aria-label', m[2]); b.tabIndex = 0;
      b.innerHTML = '<i class="ti ' + m[1] + '"></i>';
      const go = e => { e.stopPropagation(); e.preventDefault(); pdhNavMoveGroup(btn, m[0]); };
      b.addEventListener('click', go);
      b.addEventListener('keydown', e => { if(e.key === 'Enter' || e.key === ' ') go(e); });
      btn.insertBefore(b, del);
    });
    btn.onclick = null;
  }
  // pdhNavMoveGroup: Gruppe (Kopf + Einträge) mit der vorigen bzw. nächsten tauschen;
  // die oberste Gruppe ohne Namen bleibt immer oben
  function pdhNavMoveGroup(btn, dir){
    const g = document.querySelector('#pdh-nav [data-fold-group="' + btn.dataset.fold + '"]'); if(!g) return;
    const heads = [...document.querySelectorAll('#pdh-nav .nav-fold')];
    const i = heads.indexOf(btn), other = heads[dir === 'up' ? i - 1 : i + 1];
    if(!other) return;
    const og = document.querySelector('#pdh-nav [data-fold-group="' + other.dataset.fold + '"]');
    if(dir === 'up') { other.before(btn); btn.after(g); }
    else { (og || other).after(btn); btn.after(g); }
    btn.querySelector('.nav-gmove').focus();
  }
  function pdhNavAddGroup(){
    const key = 'g' + Date.now().toString(36);
    const btn = document.createElement('button');
    btn.type = 'button'; btn.className = 'nav-section nav-fold'; btn.dataset.fold = key; btn.setAttribute('aria-expanded', 'true');
    btn.innerHTML = '<span class="nav-gl"></span><i class="ti ti-chevron-down"></i>';
    btn.querySelector('.nav-gl').textContent = pdhT("Neue Gruppe");
    const g = document.createElement('div');
    g.className = 'nav-group'; g.dataset.foldGroup = key;
    const bar = document.querySelector('#pdh-nav .nav-edit-bar');
    bar.parentNode.insertBefore(btn, bar); bar.parentNode.insertBefore(g, bar);
    pdhNavHeaderEdit(btn);
    pdhNavSortables.push(Sortable.create(g, {group: 'pdh-nav', animation: 150, ghostClass: 'nav-ghost', draggable: '.nav-item'}));
    btn.querySelector('input').select();
  }
  async function pdhNavPost(body){
    const res = await fetch('/account/nav-layout', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const j = await res.json().catch(() => ({}));
    if(!res.ok || !j.success) throw new Error(j.error || ('HTTP ' + res.status));
  }
  async function pdhNavSave(){
    const groups = [...document.querySelectorAll('#pdh-nav .nav-group')].map(g => {
      const key = g.dataset.foldGroup;
      const btn = document.querySelector('#pdh-nav .nav-fold[data-fold="' + key + '"]');
      const inp = btn && btn.querySelector('input');
      return {key, label: key === 'top' ? '' : (inp ? inp.value.trim() : (g.dataset.label || '')), items: [...g.querySelectorAll('.nav-item')].map(a => a.dataset.nav)};
    });
    try{ await pdhNavPost({groups}); location.reload(); }
    catch(e){ alert(pdhT("Navigation konnte nicht gespeichert werden:") + ' ' + e.message); }
  }
  async function pdhNavReset(){
    if(!confirm(pdhT("Eigene Anordnung verwerfen und die Vorgabe wiederherstellen?"))) return;
    try{ await pdhNavPost({reset: true}); location.reload(); }
    catch(e){ alert(e.message); }
  }
  function pdhFold(b){
    const opening = b.getAttribute('aria-expanded') !== 'true';
    // Akkordeon: beim Aufklappen alle anderen Gruppen schließen
    document.querySelectorAll('#pdh-nav .nav-fold').forEach(function(o){
      const open = o === b && opening;
      o.setAttribute('aria-expanded', open ? 'true' : 'false');
      const g = document.querySelector('[data-fold-group="' + o.dataset.fold + '"]');
      if(g) g.classList.toggle('folded', !open);
    });
  }
