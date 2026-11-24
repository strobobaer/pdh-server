// Messung des Seitenaufbaus (Paket "Schneller Seitenaufbau", Teil 1).
//
// Misst je Seite und Modus DOMContentLoaded, load und "alle Symbole sichtbar".
//   erster Aufruf  = neuer Browser-Kontext, Zwischenspeicher ganz leer (nur die
//                    Anmeldung wird aus einer gespeicherten Sitzung uebernommen)
//   zwischengespeichert = zweiter Aufruf derselben Seite im selben Kontext
// Jeweils bei voller (1x) und gedrosselter (4x) CPU-Leistung.
//
// "Alle Symbole sichtbar" (messbar definiert): die Symbol-Stildatei
// (/vendor/<ver>/icons-<kennung>.css) ist geladen und angewendet, alle
// Einzel-SVGs unter /vendor/<ver>/icons/ ausserdem fertig geladen, und das ist
// zwei Bildwiederholungen lang unveraendert geblieben. Gemessen wird die Zeit
// seit Navigationsbeginn (performance.now()). Eingebettete Symbole brauchen
// keine eigene Anfrage und zaehlen mit, sobald die Stildatei angewendet ist.
//
// Aufruf siehe README.md. Kein Kennwort im Skript: nur per Datei oder Umgebung.
'use strict';

const fs = require('fs');

function usage(msg) {
  if (msg) console.error('Fehler: ' + msg);
  console.error(
    'Aufruf: node messung.js --base http://HOST:8090 --user KONTO\n' +
    '          (--password-file DATEI | Umgebung PDH_PASSWORD)\n' +
    '          [--playwright PFAD/zu/playwright-core] [--channel msedge]\n' +
    '          [--pages /,/energy/meters,/tickets,/maintenance,/infrastructure]\n' +
    '          [--cpu 1,4] [--runs 3] [--json DATEI]');
  process.exit(2);
}

function parseArgs(argv) {
  const o = {
    base: process.env.PDH_BASE || '', user: process.env.PDH_USER || '',
    passwordFile: process.env.PDH_PASSWORD_FILE || '', playwright: 'playwright-core',
    channel: 'msedge', pages: ['/', '/energy/meters', '/tickets', '/maintenance', '/infrastructure'],
    cpu: [1, 4], runs: 3, json: '',
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i], v = argv[i + 1];
    const need = () => { if (v === undefined) usage('Wert fehlt nach ' + a); i++; return v; };
    switch (a) {
      case '--base': o.base = need(); break;
      case '--user': o.user = need(); break;
      case '--password-file': o.passwordFile = need(); break;
      case '--playwright': o.playwright = need(); break;
      case '--channel': o.channel = need(); break;
      case '--pages': o.pages = need().split(',').filter(Boolean); break;
      case '--cpu': o.cpu = need().split(',').map(Number).filter(n => n >= 1); break;
      case '--runs': o.runs = Math.max(1, parseInt(need(), 10) || 1); break;
      case '--json': o.json = need(); break;
      case '-h': case '--help': usage(); break;
      default: usage('unbekannte Angabe ' + a);
    }
  }
  return o;
}

function readPassword(o) {
  if (process.env.PDH_PASSWORD) return process.env.PDH_PASSWORD;
  if (o.passwordFile) {
    try { return fs.readFileSync(o.passwordFile, 'utf8').trim(); }
    catch (e) { usage('Kennwortdatei nicht lesbar: ' + o.passwordFile); }
  }
  return '';
}

// Laeuft im Browser: wartet, bis die Symbole sichtbar sind, und liefert die Zeiten.
const IN_PAGE = () => new Promise(resolve => {
  const iconSheet = () => {
    for (const s of document.styleSheets) {
      if (s.href && /\/vendor\/[^/]+\/icons-[^/]+\.css/.test(s.href)) return s;
    }
    return null;
  };
  const sheetReady = () => {
    const s = iconSheet();
    if (!s) return false;
    try { return s.cssRules.length > 0; } catch (e) { return false; }
  };
  const pending = () => performance.getEntriesByType('resource')
    .filter(r => /\/vendor\/[^/]+\/icons\/[^/]+\.svg/.test(r.name))
    .filter(r => !(r.responseEnd > 0)).length;
  const count = () => performance.getEntriesByType('resource')
    .filter(r => /\/vendor\/[^/]+\/icons\/[^/]+\.svg/.test(r.name)).length;
  let stable = 0, last = -1;
  const t0 = performance.now();
  const tick = () => {
    const c = count();
    if (sheetReady() && pending() === 0 && c === last) stable++; else stable = 0;
    last = c;
    if (stable >= 2) {
      const e = performance.getEntriesByType('navigation')[0];
      resolve({
        dcl: Math.round(e.domContentLoadedEventEnd), load: Math.round(e.loadEventEnd),
        icons: Math.round(performance.now()), svgs: c, sheet: true,
      });
    } else if (performance.now() - t0 > 60000) {
      const e = performance.getEntriesByType('navigation')[0];
      resolve({ dcl: Math.round(e.domContentLoadedEventEnd), load: Math.round(e.loadEventEnd), icons: null, svgs: c, sheet: sheetReady() });
    } else requestAnimationFrame(tick);
  };
  // erst nach dem load-Ereignis beginnen
  if (document.readyState === 'complete') requestAnimationFrame(tick);
  else window.addEventListener('load', () => requestAnimationFrame(tick));
});

function median(a) {
  const b = a.filter(x => x !== null && x !== undefined).sort((x, y) => x - y);
  return b.length ? b[Math.floor(b.length / 2)] : null;
}

async function main() {
  const o = parseArgs(process.argv.slice(2));
  if (!o.base) usage('--base fehlt');
  if (!/^https?:\/\//.test(o.base)) usage('--base muss mit http:// oder https:// beginnen');
  if (!o.user) usage('--user fehlt');
  const password = readPassword(o);
  if (!password) usage('Kennwort fehlt (--password-file oder Umgebung PDH_PASSWORD)');
  const base = o.base.replace(/\/+$/, '');

  let pw;
  try { pw = require(o.playwright); }
  catch (e) { usage('playwright-core nicht gefunden (' + o.playwright + '); Pfad mit --playwright angeben'); }

  const browser = await pw.chromium.launch({ channel: o.channel, headless: true });
  const rows = [];
  try {
    // Anmelden einmal; die Sitzung wird in jeden frischen Kontext uebernommen,
    // damit der Zwischenspeicher dort wirklich leer ist.
    const lc = await browser.newContext({ viewport: { width: 1366, height: 900 } });
    const lp = await lc.newPage();
    await lp.goto(base + '/login', { waitUntil: 'load', timeout: 60000 });
    await lp.fill('input[name="email"]', o.user);
    await lp.fill('input[name="password"]', password);
    await Promise.all([
      lp.waitForNavigation({ waitUntil: 'load', timeout: 60000 }),
      lp.click('form[action="/login"] button[type="submit"]'),
    ]);
    if (/\/login/.test(lp.url())) throw new Error('Anmeldung fehlgeschlagen (Konto gesperrt oder Kennwort falsch?)');
    const state = await lc.storageState();
    await lc.close();

    const measure = async (page, cdp, url) => {
      await page.goto(url, { waitUntil: 'load', timeout: 120000 });
      return page.evaluate(IN_PAGE);
    };

    for (const rate of o.cpu) {
      for (const p of o.pages) {
        for (const mode of ['erster Aufruf', 'zwischengespeichert']) {
          const runs = [];
          for (let i = 0; i < o.runs; i++) {
            const ctx = await browser.newContext({ storageState: state, viewport: { width: 1366, height: 900 } });
            const page = await ctx.newPage();
            const cdp = await ctx.newCDPSession(page);
            await cdp.send('Emulation.setCPUThrottlingRate', { rate });
            try {
              if (mode === 'zwischengespeichert') await measure(page, cdp, base + p); // Zwischenspeicher fuellen
              runs.push(await measure(page, cdp, base + p));
            } catch (e) {
              runs.push({ fehler: String(e.message).slice(0, 80) });
            }
            await ctx.close();
          }
          const ok = runs.filter(r => !r.fehler);
          rows.push({
            seite: p, modus: mode, cpu: rate, laeufe: ok.length,
            dcl: median(ok.map(r => r.dcl)), load: median(ok.map(r => r.load)),
            symbole: median(ok.map(r => r.icons)), svgs: median(ok.map(r => r.svgs)),
            fehler: runs.length - ok.length ? runs.find(r => r.fehler).fehler : '',
          });
        }
      }
    }
  } finally {
    await browser.close();
  }

  const f = v => (v === null || v === undefined ? '   -  ' : (v / 1000).toFixed(2).padStart(6));
  console.log('Seite'.padEnd(18) + 'Modus'.padEnd(21) + 'CPU  ' + ' DCL(s)' + ' load(s)' + ' Symb(s)' + '  SVGs  Läufe');
  for (const r of rows) {
    console.log(r.seite.padEnd(18) + r.modus.padEnd(21) + (r.cpu + 'x').padEnd(5) + f(r.dcl) + ' ' + f(r.load) + '  ' + f(r.symbole) +
      '  ' + String(r.svgs ?? '-').padStart(4) + '  ' + r.laeufe + '/' + o.runs + (r.fehler ? '  FEHLER: ' + r.fehler : ''));
  }
  console.log('\nZiel (Entwurf): erster Aufruf, load und Symbole <= 0,5 s bei 1x CPU, <= 1,5 s bei 4x CPU.');
  if (o.json) fs.writeFileSync(o.json, JSON.stringify(rows, null, 2));
}

main().catch(e => { console.error('ABBRUCH: ' + e.message); process.exit(1); });
