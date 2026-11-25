// Live-Übersetzung (live_translate.go): übersetzt alle Texte der Seite in die
// Zielsprache des Benutzers – auch nachgeladene (htmx, Chat). Eingaben,
// Code und Elemente mit translate="no" bleiben unberührt.
(function(){
  const lang = document.body.dataset.liveTranslate;
  if(!lang) return;
  const SKIP = 'script,style,noscript,code,pre,kbd,samp,textarea,input,svg,math,[translate="no"],.notranslate,[contenteditable=""],[contenteditable="true"]';
  const ATTRS = ['placeholder', 'title', 'aria-label'];
  const store = 'pdh.lt.' + lang;
  const done = new Map(); // Ausgangstext → Übersetzung (diese Sitzung)
  try { Object.entries(JSON.parse(sessionStorage.getItem(store) || '{}')).forEach(([k, v]) => done.set(k, v)); } catch(e) {}
  const failed = new Set();
  const queue = new Map(); // Ausgangstext → [Ziele]
  let timer = null, busy = false, applying = false, stopped = false;

  const worth = s => s.length > 1 && s.length <= 2000 && /\p{L}{2}/u.test(s) && !/^(https?:\/\/|\/|[\w.+-]+@[\w-]+\.)\S*$/.test(s);
  const skipEl = el => !el || !!el.closest(SKIP);

  function want(src, target){
    if(done.has(src)) { apply(target, done.get(src)); return; }
    if(failed.has(src)) return;
    if(!queue.has(src)) queue.set(src, []);
    queue.get(src).push(target);
    if(!timer) timer = setTimeout(flush, 120);
  }
  // target: {node} für Textknoten, {el, attr} für Attribute
  function apply(t, text){
    applying = true;
    if(t.node){
      const v = t.node.nodeValue, m = v.match(/^(\s*)[\s\S]*?(\s*)$/);
      t.node.__pdhLt = m[1] + text + m[2];
      if(v !== t.node.__pdhLt) t.node.nodeValue = t.node.__pdhLt;
    } else {
      t.el.__pdhLt = t.el.__pdhLt || {};
      t.el.__pdhLt[t.attr] = text;
      if(t.el.getAttribute(t.attr) !== text) t.el.setAttribute(t.attr, text);
    }
    applying = false;
  }
  function scanText(node){
    if(node.__pdhLt === node.nodeValue) return; // schon übersetzt
    const s = node.nodeValue.trim();
    if(worth(s) && !skipEl(node.parentElement)) want(s, {node});
  }
  function scanAttrs(el){
    if(skipEl(el) && !(el.matches && el.matches('input,textarea'))) return;
    if(el.closest && el.closest('[translate="no"],.notranslate')) return;
    ATTRS.forEach(a => {
      const v = el.getAttribute && el.getAttribute(a);
      if(!v || (el.__pdhLt && el.__pdhLt[a] === v)) return;
      const s = v.trim();
      if(worth(s)) want(s, {el, attr: a});
    });
  }
  function scan(root){
    if(stopped || !root) return;
    if(root.nodeType === 3) { scanText(root); return; }
    if(root.nodeType !== 1 && root.nodeType !== 9) return;
    const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for(let n = w.nextNode(); n; n = w.nextNode()) scanText(n);
    if(root.nodeType === 1) scanAttrs(root);
    root.querySelectorAll('[placeholder],[title],[aria-label]').forEach(scanAttrs);
  }
  async function flush(){
    timer = null;
    if(busy || !queue.size) return;
    busy = true;
    const batch = [...queue.keys()].slice(0, 80);
    const status = typeof setGlobalStatus === 'function';
    if(status) setGlobalStatus('busy', 'Übersetze …');
    try {
      const res = await fetch('/translate/live', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({texts: batch})});
      const json = await res.json().catch(() => ({}));
      if(res.status === 401 || res.status === 403) stopped = true;
      const t = json.t || {};
      batch.forEach(src => {
        const targets = queue.get(src) || [];
        queue.delete(src);
        if(t[src] !== undefined) { done.set(src, t[src]); targets.forEach(x => apply(x, t[src])); }
        else failed.add(src);
      });
      try { sessionStorage.setItem(store, JSON.stringify(Object.fromEntries([...done].slice(-4000)))); } catch(e) {}
      if(status) json.error ? setGlobalStatus('warn', 'Übersetzung: ' + json.error) : setGlobalStatus('ok', 'Bereit');
    } catch(e) {
      batch.forEach(src => { queue.delete(src); failed.add(src); });
      if(status) setGlobalStatus('warn', 'Übersetzung nicht erreichbar');
    }
    busy = false;
    if(queue.size && !stopped) timer = setTimeout(flush, 50);
  }
  new MutationObserver(list => {
    if(applying || stopped) return;
    list.forEach(m => {
      if(m.type === 'childList') m.addedNodes.forEach(scan);
      else if(m.type === 'characterData') scanText(m.target);
      else if(m.type === 'attributes') scanAttrs(m.target);
    });
  }).observe(document.body, {childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ATTRS});
  scan(document.body);
})();
