// Chat in der rechten Seitenleiste: Ungelesen-Zaehler, Chatliste,
// Unterhaltung mit Antworten und automatisches Oeffnen bei neuer Nachricht
// (nicht in eingebetteten Ansichten und nicht auf der Chat-Seite selbst).
(function(){
  if(document.documentElement.classList.contains('pdh-embedded') || !window.EventSource) return;
  const $ = id => document.getElementById(id);
  const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const S = {me: null, users: {}, convs: {}, teams: {}, current: null, msgs: [], loaded: false, isAdmin: false, actId: null};
  const COLORS = ['#4f6ef7','#10b981','#f59e0b','#ef4444','#7c3aed','#0ea5e9','#ec4899','#14b8a6'];
  const color = id => { let h = 0; for(const c of String(id)) h = (h*31 + c.charCodeAt(0)) >>> 0; return COLORS[h % COLORS.length]; };
  const userName = id => (S.users[id] && S.users[id].name) || 'Unbekannt';
  const other = c => (c.members || []).find(id => id !== S.me);
  function convName(c){
    if(c.kind === 'direct'){ const o = other(c); return o ? userName(o) : 'Notizen'; }
    if(c.kind === 'channel') return '# ' + c.name + (S.teams[c.team_id] ? ' · ' + S.teams[c.team_id] : '');
    return c.name || (c.members || []).filter(id => id !== S.me).map(id => userName(id).split(' ')[0]).join(', ') || 'Gruppe';
  }
  function avatar(c){
    const id = c.kind === 'direct' ? (other(c) || S.me) : c.id;
    let inner = '#';
    if(c.kind === 'direct') inner = esc((S.users[id] && S.users[id].initials) || '?');
    // Profilbild bzw. Logo bei „Service“ (avatar.go)
    if(c.kind === 'direct' && S.users[id] && S.users[id].avatar) return '<span class="sc-av img"><img src="' + esc(S.users[id].avatar) + '" alt=""></span>';
    if(c.kind === 'group') inner = '<i class="ti ti-users"></i>';
    return '<span class="sc-av" style="background:' + color(id) + '">' + inner + '</span>';
  }
  function time(ts){
    const d = new Date(ts), t = new Date();
    return d.toDateString() === t.toDateString()
      ? d.toLocaleTimeString(PDH_LOCALE, {hour: '2-digit', minute: '2-digit'})
      : d.toLocaleDateString(PDH_LOCALE, {day: '2-digit', month: '2-digit'});
  }
  function renderBody(m){
    let s = esc(m.body);
    s = s.replace(/(https?:\/\/[^\s<]+[^\s<.,;:!?)])/g, function(u){
      if(u.startsWith(location.origin + '/')) return '<a href="' + u + '" data-frame>' + u + '</a>';
      return '<a href="' + u + '" target="_blank" rel="noopener noreferrer">' + u + '</a>';
    });
    (m.mentions || []).forEach(function(uid){
      const n = esc('@' + userName(uid));
      s = s.split(n).join('<b class="sc-mention">' + n + '</b>');
    });
    return s;
  }
  async function api(method, url, body){
    const opt = {method: method, credentials: 'same-origin'};
    if(body) opt.body = body;
    const res = await fetch(url, opt);
    const d = res.status === 204 ? null : await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error((d && d.error) || ('HTTP ' + res.status));
    return d;
  }

  // ── Zaehler ──
  function updateBadges(){
    let total = 0, mention = false;
    Object.values(S.convs).forEach(function(c){ if(!c.muted) total += c.unread; if(c.mentions) mention = true; });
    const txt = total > 99 ? '99+' : String(total);
    ['chat-nav-badge', 'ctx-chat-badge', 'top-chat-badge'].forEach(function(id){
      const b = $(id); if(!b) return;
      b.textContent = txt; b.style.display = total ? '' : 'none';
      b.style.background = mention ? 'var(--red)' : 'var(--accent)';
    });
  }

  // ── Laden ──
  async function bootstrap(){
    const d = await api('GET', '/chat/api/bootstrap');
    S.me = d.me; S.isAdmin = !!d.is_admin; S.users = {}; d.users.forEach(function(u){ S.users[u.id] = u; });
    S.teams = {}; (d.teams || []).forEach(function(t){ S.teams[t.id] = t.name; });
    S.convs = {}; d.conversations.forEach(function(c){ S.convs[c.id] = c; });
    S.loaded = true;
    updateBadges();
    if(!S.current) renderList();
  }

  function renderList(){
    S.current = null;
    $('sc-title').textContent = 'Chat';
    $('sc-back').style.display = 'none';
    $('sc-full').href = '/chat';
    $('sc-compose').style.display = 'none';
    const list = Object.values(S.convs).sort(function(a, b){ return new Date(b.last_message_at) - new Date(a.last_message_at); });
    $('sc-body').innerHTML = list.map(function(c){
      const p = c.preview;
      const prev = p ? ((p.user_id === S.me ? 'Du: ' : '') + (p.body || '📎 Datei')) : '';
      let badge = '';
      if(c.mentions) badge = '<span class="sc-badge" style="background:var(--red)">@</span>';
      else if(c.unread) badge = '<span class="sc-badge">' + c.unread + '</span>';
      return '<div class="sc-item' + (c.unread ? ' unread' : '') + '" data-conv="' + c.id + '">' + avatar(c) +
        '<div class="sc-t"><div class="sc-n">' + esc(convName(c)) + '</div><div class="sc-p">' + esc(prev) + '</div></div>' +
        '<div class="sc-meta">' + (p ? time(p.created_at) : '') + badge + '</div></div>';
    }).join('') || '<div class="sc-empty">Noch keine Unterhaltungen.<br><a href="/chat" style="color:var(--accent)">Chat öffnen</a></div>';
  }

  async function openConv(id){
    const c = S.convs[id]; if(!c) return;
    S.current = id;
    $('sc-title').textContent = convName(c);
    $('sc-back').style.display = '';
    $('sc-full').href = '/chat#c=' + id;
    $('sc-compose').style.display = '';
    $('sc-body').innerHTML = '<div class="sc-empty">Wird geladen…</div>';
    try {
      const d = await api('GET', '/chat/api/conversations/' + id + '/messages');
      if(S.current !== id) return;
      S.msgs = d.messages.slice(-60);
      renderMsgs();
      markRead();
      if(S.share){
        // Teilen: Titel und Link vorbelegen, Absenden bleibt beim Benutzer
        $('sc-input').value = (S.share.title ? S.share.title + '\n' : '') + S.share.url;
        S.share = null; $('sc-share').style.display = 'none';
        $('sc-input').focus();
      }
    } catch(e){ $('sc-body').innerHTML = '<div class="sc-empty">' + esc(e.message) + '</div>'; }
  }

  function renderMsgs(){
    const box = $('sc-body');
    box.innerHTML = S.msgs.map(function(m){
      if(m.kind === 'system') return '<div class="sc-sys">' + esc(m.body) + '</div>';
      const mine = m.user_id === S.me;
      const links = (m.links || []).map(function(l){
        return '<a class="sc-card" href="' + l.url + '" data-frame data-frame-title="' + esc(l.label + ': ' + l.title) + '"><i class="ti ' + l.icon + '"></i> ' + esc(l.title) + '</a>';
      }).join('');
      const files = (m.files || []).map(function(f){
        return '<a class="sc-card" href="' + f.url + '" target="_blank" rel="noopener"><i class="ti ' + (f.is_image ? 'ti-photo' : 'ti-file') + '"></i> ' + esc(f.name) + '</a>';
      }).join('');
      let bubble = '';
      if(m.deleted) bubble = '<div class="sc-bubble" style="font-style:italic;opacity:.6">Nachricht gelöscht</div>';
      else if(m.body) bubble = '<div class="sc-bubble">' + renderBody(m) + '</div>';
      const au = S.users[m.user_id];
      const who = mine ? '' : '<div class="sc-who">' + (au && au.avatar ? '<img class="sc-who-av" src="' + esc(au.avatar) + '" alt=""> ' : '') + esc(userName(m.user_id)) + (m.parent_id ? ' <i class="ti ti-arrow-back-up"></i>' : '') + '</div>';
      const canDel = !m.deleted && (mine || S.isAdmin);
      const del = canDel ? '<button type="button" class="sc-del" data-del="' + esc(m.id) + '" title="Nachricht löschen" aria-label="Nachricht löschen"><i class="ti ti-trash"></i></button>' : '';
      return '<div class="sc-msg' + (mine ? ' mine' : '') + (canDel && S.actId === m.id ? ' act' : '') + '" data-mid="' + esc(m.id) + '">' + del + who + bubble + links + files + '<div class="sc-time">' + time(m.created_at) + '</div></div>';
    }).join('') || '<div class="sc-empty">Noch keine Nachrichten</div>';
    box.scrollTop = box.scrollHeight;
  }

  async function delMsg(id){
    if(!confirm('Nachricht löschen?')) return;
    try {
      const m = await api('POST', '/chat/api/messages/' + encodeURIComponent(id) + '/delete');
      S.actId = null;
      const i = S.msgs.findIndex(function(x){ return x.id === id; });
      if(i >= 0 && m) S.msgs[i] = m; else if(i >= 0) S.msgs[i].deleted = true;
      renderMsgs();
    } catch(e){ alert(e.message); }
  }

  let readTimer = null;
  function markRead(){
    const c = S.convs[S.current];
    if(!c || document.hidden || document.documentElement.classList.contains('sidebar-right-collapsed')) return;
    clearTimeout(readTimer);
    readTimer = setTimeout(function(){
      c.unread = 0; c.mentions = 0; updateBadges();
      fetch('/chat/api/conversations/' + c.id + '/read', {method: 'POST', credentials: 'same-origin'}).catch(function(){});
    }, 500);
  }

  async function send(){
    const ta = $('sc-input'); const text = ta.value.trim();
    if(!text || !S.current) return;
    const fd = new FormData(); fd.append('body', text);
    ta.disabled = true;
    try {
      const m = await api('POST', '/chat/api/conversations/' + S.current + '/messages', fd);
      ta.value = '';
      if(!S.msgs.some(function(x){ return x.id === m.id; })) S.msgs.push(m);
      renderMsgs();
    } catch(e){ alert('Senden fehlgeschlagen: ' + e.message); }
    finally { ta.disabled = false; ta.focus(); }
  }

  // Laufende Eingabe irgendwo auf der Seite? Dann die Leiste nicht ungefragt umschalten.
  function userIsTyping(){
    const a = document.activeElement;
    if(!a || a.value === '' || a.value === undefined) return false;
    if(a.tagName === 'TEXTAREA') return true;
    return a.tagName === 'INPUT' && ['checkbox', 'radio', 'button', 'submit', 'hidden'].indexOf(a.type) < 0;
  }

  function onMessage(m){
    const c = S.convs[m.conversation_id];
    if(!c){ bootstrap().catch(function(){}); return; }
    c.last_message_at = m.created_at;
    c.preview = {user_id: m.user_id, body: m.body, kind: m.kind, created_at: m.created_at};
    const open = !document.documentElement.classList.contains('sidebar-right-collapsed') && localStorage.getItem('pdh_context_tab') === 'chat';
    if(S.current === m.conversation_id && !S.msgs.some(function(x){ return x.id === m.id; })){
      S.msgs.push(m); renderMsgs();
    }
    if(m.user_id === S.me || m.kind !== 'text'){ if(!S.current) renderList(); return; }
    if(S.current === m.conversation_id && open && !document.hidden) markRead();
    else { c.unread++; if((m.mentions || []).indexOf(S.me) >= 0) c.mentions++; }
    updateBadges();
    // Automatisch oeffnen (nicht bei stummgeschalteten Chats / laufender Eingabe;
    // auf Smartphone/Tablet nur Zaehler, damit das Overlay nicht die Arbeit verdeckt)
    if(!c.muted && !userIsTyping() && !(typeof PDH_NARROW !== 'undefined' && PDH_NARROW.matches)){
      activateContextTab('chat', {keepOpen: true});
      if(S.current !== m.conversation_id && !$('sc-input').value) openConv(m.conversation_id);
    } else if(!S.current) renderList();
  }

  // ── Ereignisse ──
  document.addEventListener('DOMContentLoaded', function(){
    if(!$('sc-body')) return;
    $('sc-back').onclick = renderList;
    $('sc-body').addEventListener('click', function(e){
      const it = e.target.closest('[data-conv]'); if(it){ openConv(it.dataset.conv); return; }
      const db = e.target.closest('[data-del]');
      if(db){ delMsg(db.dataset.del); return; }
      if(e.target.closest('a, button, img')) return;
      // Touch: Antippen einer Nachricht zeigt/versteckt den Papierkorb, Antippen woanders schließt ihn
      const el = e.target.closest('.sc-msg[data-mid]');
      const prev = $('sc-body').querySelector('.sc-msg.act');
      if(prev) prev.classList.remove('act');
      if(el && el.querySelector('.sc-del') && S.actId !== el.dataset.mid){ S.actId = el.dataset.mid; el.classList.add('act'); } else S.actId = null;
    });
    $('sc-send').onclick = send;
    $('sc-input').addEventListener('keydown', function(e){ if(e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); send(); } });
    bootstrap().catch(function(){});
    const es = new EventSource('/chat/stream');
    es.addEventListener('chat', function(e){
      const ev = JSON.parse(e.data);
      if(!S.loaded) return;
      if(ev.type === 'message') onMessage(ev.data);
      else if(ev.type === 'message_updated' && S.current === ev.data.conversation_id){
        const i = S.msgs.findIndex(function(x){ return x.id === ev.data.id; });
        if(i >= 0){ S.msgs[i] = ev.data; renderMsgs(); }
      } else if(ev.type === 'read' && ev.data.user_id === S.me && S.convs[ev.data.conversation_id]){
        S.convs[ev.data.conversation_id].unread = 0; S.convs[ev.data.conversation_id].mentions = 0;
        updateBadges(); if(!S.current) renderList();
      } else if(ev.type === 'refresh') bootstrap().catch(function(){});
    });
    window.addEventListener('pagehide', function(){ es.close(); });
  });
  // wird von activateContextTab aufgerufen, wenn der Chat-Reiter sichtbar wird
  window.pdhSideChat = {
    shown: function(){ if(S.current) markRead(); else if(S.loaded) renderList(); },
    // bestimmte Unterhaltung in der Seitenleiste oeffnen (z. B. "Chat" auf der Benutzerseite)
    open: async function(convId){
      if(!S.convs[convId]) await bootstrap();
      activateContextTab('chat', {keepOpen: true});
      openConv(convId);
      setTimeout(function(){ const i = $('sc-input'); if(i) i.focus(); }, 300);
    },
    // "Im Chat teilen": Seitenleiste oeffnen, Unterhaltung auswaehlen lassen
    share: function(url, title){
      S.share = {url: url, title: title};
      const box = $('sc-share');
      box.innerHTML = '<i class="ti ti-share"></i><span style="flex:1">Unterhaltung wählen, um <b></b> zu teilen</span><a href="#" title="Abbrechen"><i class="ti ti-x"></i></a>';
      box.querySelector('b').textContent = title || url;
      box.querySelector('a').onclick = function(e){ e.preventDefault(); S.share = null; box.style.display = 'none'; };
      box.style.display = 'flex';
      activateContextTab('chat', {keepOpen: true});
      if(S.current) renderList();
    }
  };
  document.addEventListener('visibilitychange', function(){ if(!document.hidden) markRead(); });
})();
