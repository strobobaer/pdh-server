/* PDH Service Worker – Push-Benachrichtigungen (internal/web/push.go).
 * type "alert": Nachricht mit „Annehmen“/„Ansehen“; urgent = „Anlage steht“
 *               (bleibt stehen, vibriert lang, wiederholt sich bis zur Annahme)
 * type "close": jemand hat angenommen oder der Vorgang ist erledigt – wegnehmen
 * type "test":  Probe aus „Mein Konto“
 */
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

function tellPages(msg) {
  return self.clients.matchAll({type: 'window', includeUncontrolled: true})
    .then(list => list.forEach(c => c.postMessage({pdhPush: msg})));
}

self.addEventListener('push', e => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (_) { d = {title: 'PDH', body: e.data ? e.data.text() : ''}; }
  if (d.type === 'close') {
    e.waitUntil(self.registration.getNotifications({tag: d.tag})
      .then(ns => ns.forEach(n => n.close()))
      .then(() => tellPages(d)));
    return;
  }
  const urgent = !!d.urgent;
  const opts = {
    body: d.body || '',
    tag: d.tag || 'pdh',
    renotify: true,                 // bei Wiederholung erneut klingeln/vibrieren
    requireInteraction: d.type === 'alert',
    vibrate: urgent ? [800, 300, 800, 300, 800, 300, 1500] : [200, 100, 200],
    icon: '/icon-192.png',
    badge: '/favicon-32.png',
    timestamp: Date.now(),
    data: {url: d.url || '/', accept: d.accept || '', id: d.id || '', urgent: urgent},
    actions: d.accept ? [{action: 'accept', title: 'Annehmen'}, {action: 'open', title: 'Ansehen'}] : []
  };
  e.waitUntil(self.registration.showNotification(d.title || 'PDH', opts).then(() => tellPages(d)));
});

function openPage(url) {
  return self.clients.matchAll({type: 'window', includeUncontrolled: true}).then(list => {
    for (const c of list) {
      if ('navigate' in c && 'focus' in c) return c.navigate(url).then(w => (w || c).focus()).catch(() => c.focus());
    }
    return self.clients.openWindow(url);
  });
}

self.addEventListener('notificationclick', e => {
  const d = e.notification.data || {};
  e.notification.close();
  if (e.action === 'accept' && d.accept) {
    // direkt aus der Nachricht annehmen; danach den Vorgang bzw. die Alarmseite zeigen
    e.waitUntil(fetch(d.accept, {method: 'POST', credentials: 'include', headers: {'X-PDH-Push': '1'}})
      .then(r => r.json().catch(() => ({})).then(j => openPage(r.ok && j.url ? j.url : d.url)))
      .catch(() => openPage(d.url)));
    return;
  }
  // Antippen (und iPhone ohne Knöpfe): Alarmseite mit „Annehmen“
  e.waitUntil(openPage(d.url || '/'));
});
