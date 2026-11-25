// Hinweis-Band vor der automatischen Wartung: fragt alle 15 s /wartung/status (ohne Fortschrittsbalken),
// zählt bei laufendem Countdown sekündlich herunter und springt bei Sperre auf den Wartungsbildschirm
(function(){
  const band = document.getElementById('maint-band');
  if(!band || document.documentElement.classList.contains('pdh-embedded')) return;
  const txt = document.getElementById('maint-band-text');
  const act = band.querySelector('.mb-act');
  const tpl = pdhT("Automatische Wartung in %s min – bitte Eingaben speichern");
  const askTpl = pdhT("Wartungsfenster: Update auf %v durchführen? Noch %s min");
  const L = {
    go: pdhT("Durchführen"), goQ: pdhT("Update jetzt durchführen?"),
    no: pdhT("Absagen"), noQ: pdhT("Update absagen?"),
    stop: pdhT("Abbrechen"), stopQ: pdhT("Update abbrechen und zurücksetzen?"),
    err: pdhT("Aktion nicht möglich")
  };
  let until = 0, skew = 0, tick = null, timer = null, mode = '', ver = '', note = '', acts = '';
  const fmt = s => Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
  function hide(){ until = 0; mode = ''; acts = ''; act.textContent = ''; band.classList.remove('soon'); if(tick){ clearInterval(tick); tick = null; }
    if(note){ band.hidden = false; txt.textContent = note; return; } // Rückmeldung einer Aktion bleibt sichtbar
    band.hidden = true; }
  function draw(){
    const left = Math.max(0, Math.round((until - (Date.now() + skew)) / 1000));
    if(note){ txt.textContent = note; return; }
    if(mode === 'ask'){ txt.textContent = askTpl.replace('%v', ver).replace('%s', fmt(left)); band.classList.remove('soon'); return; }
    txt.textContent = tpl.replace('%s', fmt(left));
    band.classList.toggle('soon', left <= 30);
  }
  // Knöpfe nur neu aufbauen, wenn sich ihre Zusammensetzung ändert (sonst geht ein Klick verloren)
  function buttons(list){
    const key = list.map(b => b.url + '#' + b.label).join('|');
    if(key === acts) return;
    acts = key; act.textContent = '';
    list.forEach(b => {
      const el = document.createElement('button');
      el.type = 'button'; el.className = 'btn btn-small' + (b.primary ? ' btn-primary' : ''); el.textContent = b.label;
      el.addEventListener('click', () => post(b, el));
      act.appendChild(el);
    });
  }
  async function post(b, el){
    if(!confirm(b.q)) return;
    el.disabled = true;
    try {
      const r = await fetch(b.url, {method: 'POST', credentials: 'same-origin', headers: {Accept: 'application/json'}});
      let j = null;
      try { j = await r.json(); } catch(e) { /* z. B. 403 als Klartext */ }
      note = j ? (j.ok ? (j.msg || '') : (j.error || L.err)) : (r.statusText || (L.err + ' (' + r.status + ')'));
    } catch(e) { note = L.err; }
    el.disabled = false; draw();
    setTimeout(() => { note = ''; poll(); }, 6000);
    poll();
  }
  function schedule(ms){ clearTimeout(timer); timer = setTimeout(poll, ms); }
  async function poll(){
    if(document.hidden){ schedule(15000); return; }
    try {
      const r = await fetch('/wartung/status', {credentials: 'same-origin', cache: 'no-store', pdhQuiet: true});
      const v = await r.json();
      if(v.locked){ location.href='/wartung'; return; }
      const d = Date.parse(r.headers.get('Date') || '');
      skew = isNaN(d) ? 0 : d - Date.now();
      const u = v.countdown_until ? Date.parse(v.countdown_until) : 0;
      const ask = (v.phase === 'nachfrage' && v.ask_until) ? Date.parse(v.ask_until) : 0;
      if(ask && ask > Date.now() + skew){
        until = ask; mode = 'ask'; ver = v.to_version || ''; band.hidden = false;
        const l = [];
        if(v.can_confirm) l.push({url: '/wartung/durchfuehren', label: L.go, q: L.goQ, primary: true});
        if(v.can_abort) l.push({url: '/wartung/abbrechen', label: L.no, q: L.noQ});
        buttons(l); draw();
        if(!tick) tick = setInterval(draw, 1000);
      } else if(u && u > Date.now() + skew){
        until = u; mode = 'count'; band.hidden = false;
        buttons(v.can_abort ? [{url: '/wartung/abbrechen', label: L.stop, q: L.stopQ}] : []); draw();
        if(!tick) tick = setInterval(draw, 1000);
      } else if(!(until && mode === 'count' && Date.now() + skew - until < 60000)) hide(); // abgelaufen: Sperre folgt in Kürze
      else buttons([]);
    } catch(e) { /* Server startet vielleicht neu – weiter fragen */ }
    schedule(until ? 2000 : 15000);
  }
  document.addEventListener('visibilitychange', () => { if(!document.hidden) poll(); });
  schedule(3000);
})();
