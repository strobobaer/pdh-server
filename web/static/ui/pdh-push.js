// Push: Geraet anmelden (Mein Konto) und Abo aktuell halten
window.pdhPush = (function(){
  const supported = 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  const b64ToBytes = s => { const p = '='.repeat((4 - s.length % 4) % 4); const raw = atob((s + p).replace(/-/g, '+').replace(/_/g, '/')); return Uint8Array.from(raw, c => c.charCodeAt(0)); };
  async function reg(){ return navigator.serviceWorker.register('/sw.js', {scope: '/'}); }
  async function key(){ const r = await fetch('/push/key', {credentials: 'same-origin'}); return r.json(); }
  async function save(sub){
    const r = await fetch('/push/subscribe', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(sub.toJSON())});
    if(!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'nicht gespeichert');
  }
  async function enable(){
    if(!supported) throw new Error('Dieser Browser kann keine Push-Nachrichten. iPhone: PDH erst „Zum Home-Bildschirm“ hinzufügen und von dort öffnen.');
    const perm = await Notification.requestPermission();
    if(perm !== 'granted') throw new Error('Benachrichtigungen wurden nicht erlaubt – in den Einstellungen des Browsers/Handys freigeben.');
    const k = await key();
    if(!k.enabled || !k.key) throw new Error('Push ist auf dem Server ausgeschaltet.');
    const r = await reg(); await navigator.serviceWorker.ready;
    let sub = await r.pushManager.getSubscription();
    if(!sub) sub = await r.pushManager.subscribe({userVisibleOnly: true, applicationServerKey: b64ToBytes(k.key)});
    await save(sub);
  }
  async function disable(){
    if(!supported) return;
    const r = await navigator.serviceWorker.getRegistration('/');
    const sub = r && await r.pushManager.getSubscription();
    if(sub){
      await fetch('/push/unsubscribe', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({endpoint: sub.endpoint})});
      await sub.unsubscribe();
    }
  }
  async function status(){
    if(!supported) return {supported: false};
    const r = await navigator.serviceWorker.getRegistration('/');
    const sub = r && await r.pushManager.getSubscription();
    return {supported: true, permission: Notification.permission, here: !!sub};
  }
  async function test(){
    const r = await fetch('/push/test', {method: 'POST', credentials: 'same-origin'});
    const j = await r.json().catch(() => ({}));
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    return j;
  }
  // bei jedem Seitenaufruf: erlaubtes Abo auffrischen (gehoert der angemeldeten Person)
  if(supported && Notification.permission === 'granted'){
    navigator.serviceWorker.getRegistration('/').then(async r => {
      if(!r) return;
      try{
        let sub = await r.pushManager.getSubscription();
        if(!sub){ const k = await key(); if(k.key) sub = await r.pushManager.subscribe({userVisibleOnly: true, applicationServerKey: b64ToBytes(k.key)}); }
        if(sub) await save(sub);
      }catch(e){}
    });
  }
  return {supported, enable, disable, status, test};
})();

// „Anlage steht“: Vollbild ueber der App, bis jemand annimmt
(function(){
  const box = document.getElementById('pdh-alarm');
  let shown = null, snoozed = {};
  const $ = id => document.getElementById(id);
  function show(a, more){
    shown = a;
    $('pdh-alarm-kind').textContent = a.kind;
    $('pdh-alarm-title').textContent = a.title;
    $('pdh-alarm-body').textContent = a.body || '';
    $('pdh-alarm-more').textContent = more > 1 ? 'Noch ' + (more - 1) + ' weitere(r) offene(r) Alarm(e)' : '';
    $('pdh-alarm-open').href = a.url;
    $('pdh-alarm-msg').textContent = '';
    $('pdh-alarm-accept').disabled = false;
    box.hidden = false;
    if(navigator.vibrate) navigator.vibrate([700, 300, 700]);
  }
  function hide(){ shown = null; box.hidden = true; }
  async function check(){
    if(location.pathname.startsWith('/push/alert/')) return; // dort ist der Alarm schon bildschirmfuellend
    try{
      const r = await fetch('/push/alerts/open', {credentials: 'same-origin', cache: 'no-store', pdhQuiet: true});
      if(!r.ok) return;
      const list = ((await r.json()).alerts || []).filter(a => !(snoozed[a.id] > Date.now()));
      if(!list.length) hide();
      else if(!shown || !list.some(a => a.id === shown.id)) show(list[0], list.length);
    }catch(e){}
  }
  $('pdh-alarm-accept').addEventListener('click', async () => {
    if(!shown) return;
    $('pdh-alarm-accept').disabled = true; $('pdh-alarm-msg').textContent = 'Wird angenommen …';
    try{
      const r = await fetch(shown.accept, {method: 'POST', credentials: 'same-origin'});
      const j = await r.json().catch(() => ({}));
      if(r.ok && j.url){ hide(); location.href = j.url; return; }
      $('pdh-alarm-msg').textContent = j.error || ('HTTP ' + r.status);
      setTimeout(check, 1500);
    }catch(e){ $('pdh-alarm-msg').textContent = e.message; $('pdh-alarm-accept').disabled = false; }
  });
  $('pdh-alarm-snooze').addEventListener('click', () => { if(shown){ snoozed[shown.id] = Date.now() + 120000; hide(); setTimeout(check, 121000); } });
  if(navigator.serviceWorker) navigator.serviceWorker.addEventListener('message', e => { if(e.data && e.data.pdhPush) check(); });
  check();
  setInterval(() => { if(!document.hidden) check(); }, 30000);
  document.addEventListener('visibilitychange', () => { if(!document.hidden) check(); });
})();
