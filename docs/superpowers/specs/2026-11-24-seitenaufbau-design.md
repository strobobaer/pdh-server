# Schneller Seitenaufbau (Teil 1 der einheitlichen Oberfläche 0.90.0) – Entwurf

Stand: 24.11.2026 · Wunsch des Anwenders: „es muss schneller seiten aufbauen“
(„unbedingt“) · Entscheidungen: „1 ja mitliefern, 2 ja 0.90.0“, „Weg 1, passt so“ ·
**vom Anwender bestätigt** („Passt ja“); die Prüfung über Cloudflare auf
10.10.0.202 übernimmt der Anwender nach dem Einspielen („ja das mach ich dann“)

## Worum es geht

Jede PDH-Seite lädt die Symbol-Bibliothek Tabler Icons 3.30.0 als eine
CSS-Datei (`/vendor/tabler-icons/tabler-icons.min.css`, 1,4 MB, darin die
Symbolschrift mit 5 918 Zeichen als eingebettete WOFF2-Datei). Außerdem kommen
htmx, Sortable, das Gantt-Diagramm, Chart.js, die Excel- und Word-Vorschau und
die Schriften aus dem Internet.

Gemessen am Testverbund (Edge, angemeldet, 24.11.2026):

| Fall | volle Leistung | 4× gedrosselt (Tablet, Terminal) |
|---|---|---|
| Seite im Zwischenspeicher | 0,06–0,1 s | 0,35–0,4 s |
| erster Aufruf mit Symbolen | 6,7–7,0 s | 29–33 s |
| erster Aufruf ohne Symbole | 0,14–0,2 s | 0,46–0,53 s |

Ein Versuch (lokale Testseite, 60 Symbole, ohne Zwischenspeicher) zeigt: Die
Schrift als eigene Datei statt eingebettet ändert nichts (4,8 s bzw. 21 s in
beiden Fällen). Die Zeit geht ins Entpacken und Prüfen der Schrift mit ihren
5 918 Zeichen. „Erster Aufruf“ trifft jedes Gerät nach jedem Update (die
Adresse enthält die Version), nach geleertem Zwischenspeicher und Geräte mit
wenig Speicher – besonders das Raspberry-Pi-Terminal.

## Ziel

- Jede Seite baut beim ersten Aufruf so schnell auf wie heute ohne Symbole:
  am Testverbund höchstens **0,5 s** bei voller Leistung und **1,5 s** bei 4×
  gedrosselter Leistung (gemessen mit demselben Skript wie oben, Seiten `/`,
  `/energy/meters`, `/tickets`, `/maintenance`, `/infrastructure`).
- Seiten aus dem Zwischenspeicher werden nicht langsamer.
- Keine PDH-Seite lädt mehr etwas aus dem Internet (Teil von offener Punkt 13).
- Alle 5 918 Symbole bleiben überall wählbar und sichtbar, auch die, die
  Anwender frei wählen.

## Nicht-Ziele

- Die Oberfläche selbst ändern (Knöpfe, Anordnung, Navigation) – das ist Teil 2
  bis 5 des Pakets 0.90.0.
- Eine Content-Security-Policy einführen (die vielen eingebetteten Skripte
  bräuchten erst einen Umbau; nach diesem Teil ist sie nur möglich, nicht dran).
- Andere Bibliotheksversionen: es bleiben htmx 1.9.12, Sortable 1.15.2,
  frappe-gantt 0.6.1, Chart.js 4.4.0, xlsx 0.18.5, mammoth 1.8.0, Tabler 3.30.0.

## Was es nicht werden darf (Anti-Ziele)

- Symbole, die erst Sekunden nach der Seite erscheinen oder springen.
- Ein Symbol, das in einer Liste, im Druck oder in einer Ansicht fehlt oder als
  leeres Kästchen erscheint.
- Mehr Netzverkehr als heute bei Seiten aus dem Zwischenspeicher.

## Wege (besprochen am 24.11.2026)

1. **Symbole als kleine Vektorgrafiken (SVG) statt Schrift – gewählt.** Jedes
   Symbol ist eine SVG-Grafik, die als Maske in der Textfarbe gezeichnet wird.
   Keine Schrift muss mehr entpackt werden; jedes der 5 918 Symbole bleibt
   wählbar; die Vorlagen (`<i class="ti ti-…">`) bleiben, wie sie sind.
2. Kleine Schrift mit den vom PDH genutzten Symbolen plus eine vom PDH erzeugte
   Zusatzschrift für frei gewählte – genauso schnell, aber Schriften selbst zu
   erzeugen ist aufwendig und fehleranfällig. Verworfen.
3. Seite ohne Symbole zeichnen, Schrift im Hintergrund nachladen (Vorschlag des
   Anwenders) – Seite sofort da, aber Symbole erst nach 5 s (Terminal 20–30 s),
   Gerät so lange ausgelastet. Verworfen.

## Entscheidungen des Anwenders

- Weg 1.
- htmx, Sortable, das Gantt-Diagramm und die Schriften liefert das PDH selbst
  mit („1 ja mitliefern“); dazu – ergänzt und bestätigt („passt so“) – Chart.js,
  xlsx und mammoth sowie alle in der Darstellung wählbaren Schriften (DM Sans,
  Source Sans 3, Roboto, Inter, Atkinson Hyperlegible).
- Das große Oberflächen-Paket heißt am Ende **0.90.0**. Dieser Teil kommt
  vorher als eigene Korrekturversion (nächste über `main`, heute 0.82.56).

## Wie es gebaut wird

### Symbole

- **Quelle:** die SVG-Daten von Tabler Icons **3.30.0** (dieselbe Version wie
  die heutige Schrift, Lizenz MIT), umriss- und gefüllte Symbole, als eine oder
  wenige Dateien im Repository unter `web/static/vendor/tabler-icons/` (z. B.
  die Knoten- oder Sprite-Datei des Pakets `@tabler/icons`). Welche Form, ist
  Sache des Baus; Ziel: wenige Dateien im Repository, keine 6 000 Einzeldateien.
- **Namen:** genau die 5 918 Klassen von heute (`ti-…`, aus der heutigen CSS-Datei
  abgeleitet). Fehlt für einen Namen die SVG-Quelle, zeigt ein Test ihn an.
- **Darstellung:** `.ti` bleibt die Klasse jedes Symbols, `::before` zeichnet
  ein Quadrat von `1em` mit der SVG als Maske in `currentColor`
  (`mask-image`, mit `-webkit-`-Vorsatz). Größe, Farbe, Ausrichtung,
  Drehen/Animationen wie heute über `font-size`, `color` und Klassen; die
  vorhandenen Regeln wie `.hs .ti{font-size:15px}` gelten weiter.
- **Zwei Wege zum Browser:**
  - **Eingebettet in die Stildatei** (keine Anfrage je Symbol): die vom PDH
    selbst genutzten Symbole (heute rund 450: alle `ti-…` in Vorlagen, Go-Code,
    `web/static/*.js` und die Vorgaben der Symbol-Schlüssel) **und alle
    Symbole, die gerade irgendwo gewählt sind** – `/admin/icons`, Kategorien,
    Grafana-Statistiken, Anlagen-Watch-Boards, IT-Kategorien. Die Stildatei
    besteht damit aus einem festen Teil je Version und einem kleinen Teil für
    die gewählten Symbole; ihre Adresse enthält Version und einen Kennwert der
    gewählten Symbole, ändert sich also, sobald jemand ein anderes Symbol wählt.
  - **Einzeln nachgeladen** wird ein Symbol nur dort, wo die ganze Auswahl zu
    sehen ist: in den Symbol-Auswahlen (`/admin/icons` und die Auswahl an
    Kategorien usw.). Dafür liefert der Server je Name eine kleine SVG-Adresse
    aus der Quelle. Die Auswahl darf dafür viele Symbole laden, soll aber nicht
    hängen (z. B. erst beim Sichtbarwerden).
- **Gegen falsch vorgehaltene Dateien (Cloudflare):** In 0.44.0 erschienen
  Symbole hinter Cloudflare als leere Kästchen, solange die Schrift eine eigene
  Datei war (Behebung damals: Schrift eingebettet, Adresse mit der Version).
  Deshalb: Bei allen mitgelieferten Dateien (Stildatei, SVG, Bibliotheken,
  Schriften) steht die Version **im Pfad**, nicht nur hinter `?`; lange
  zwischengespeichert (`immutable`) wird nur eine Antwort mit Erfolg für die
  laufende Version, mit richtigem `Content-Type` (`image/svg+xml`,
  `font/woff2`, `text/css`, `text/javascript`); eine fremde Version, ein
  unbekannter Name und Fehler bekommen `no-store`. Die Seiten selbst brauchen
  keine Einzel-SVG – fällt eine Einzeladresse aus, betrifft das nur die
  Auswahllisten.
- **Druck:** Symbole erscheinen im Druck (`print-color-adjust: exact`).
- **Erzwungene Farben** (Windows-Kontrastmodus): Symbole bleiben sichtbar.
- **Aufräumen:** `ensureIconFont` und die Regeln `html.icons-fallback …` in
  `base.gohtml` entfallen oder werden auf die neue Darstellung umgestellt; die
  Namensliste in `icons.go` (`loadIconNames`) liest künftig die neue Quelle.
- **Seiten mit eigenem Kopf** (`easy`, `global_dashboard`, `push_alert`,
  `terminal`, `terminal_device_error`) bekommen dieselbe Stildatei.

### Mitgelieferte Bibliotheken und Schriften

- htmx, Sortable, frappe-gantt (Skript und Stil), Chart.js, xlsx und mammoth
  liegen in denselben Versionen unter `web/static/vendor/` und werden wie die
  übrigen Dateien dort ausgeliefert (komprimiert, lange zwischengespeichert,
  Version in der Adresse). Alle Verweise auf `unpkg.com`, `cdn.jsdelivr.net`
  und `cdnjs.cloudflare.com` entfallen.
- Die Schriften der Darstellung (DM Sans, Source Sans 3, Roboto, Inter,
  Atkinson Hyperlegible) liegen als WOFF2 mit eigener `@font-face`-Datei im PDH;
  `uiFonts` (`appearance.go`) verweist darauf statt auf Google Fonts. Es werden
  nur die heute genutzten Schnitte mitgeliefert.
- `THIRD_PARTY_LICENSES.md` nennt jede mitgelieferte Datei mit Lizenz.

### Prüfungen

- Test: jede `ti-…`-Klasse in Vorlagen, Go-Code und `web/static/*.js` und jede
  Vorgabe der Symbol-Schlüssel steht eingebettet in der Stildatei.
- Test: keine Vorlage und kein Go-Code verweist auf `unpkg.com`,
  `cdn.jsdelivr.net`, `cdnjs.cloudflare.com`, `fonts.googleapis.com` oder
  `fonts.gstatic.com`.
- Test: jede der 5 918 Klassen hat eine Regel, die SVG-Adresse liefert eine
  gültige SVG, ein unbekannter Name 404.
- Test: ein in Kategorien oder `/admin/icons` gewähltes Symbol steht nach dem
  Speichern eingebettet in der Stildatei, deren Adresse sich dabei ändert.
- Test: Zwischenspeicher-Kopfzeilen wie oben (laufende Version `immutable`,
  fremde Version/unbekannt/Fehler `no-store`), richtige `Content-Type`.
- **Messung** (Skript ins Repository unter `deploy/lab/`, Node mit
  playwright-core und Edge wie bisher): je Seite ein neuer Browser-Kontext,
  also **ganzer Zwischenspeicher leer** („erster Aufruf“), angemeldet; gemessen
  werden `DOMContentLoaded`, `load` und „alle Symbole sichtbar“
  (`document.fonts.ready` entfällt, stattdessen: alle `.ti`-Masken geladen);
  bei voller und 4× gedrosselter Leistung; dazu derselbe Lauf mit gefülltem
  Zwischenspeicher. Vor dem Einspielen die alten Werte am Testverbund
  nachmessen, danach die neuen. Ziel: `load` und „alle Symbole sichtbar“
  höchstens 0,5 s bzw. 1,5 s.
- Browser (Testverbund): Netzprotokoll ohne fremde Adressen; Startseite, eine
  Liste, eine Detailseite, `/admin/icons`, das Terminal und eine Druckansicht in
  Fiori und Fluent, hell und dunkel, 320–1366 px; Symbole sehen aus wie vorher
  (Bildschirmfotos vorher/nachher).
- Über Cloudflare (10.10.0.202 mit Tunnel, durch den Anwender nach dem
  Einspielen): eine Seite und `/admin/icons` über die Tunnel-Adresse öffnen,
  Symbole da; Seite neu laden, Symbole weiterhin da.

## Von mir festgelegt (nach der Prüfung des Entwurfs, zur Durchsicht)

- Auch alle gerade gewählten Symbole stehen eingebettet in der Stildatei; einzeln
  nachgeladen wird nur in den Symbol-Auswahlen (Lehre aus 0.44.0).
- Version im Pfad bei allen mitgelieferten Dateien; lange Zwischenspeicherung
  nur für Erfolg in der laufenden Version.
- Messung genau festgelegt (ganzer Zwischenspeicher leer, `load` und „alle
  Symbole sichtbar“), Skript ins Repository.

## Dem Bau überlassen

- Form der SVG-Quelle im Repository und wie der Server daraus SVG und Stildatei
  erzeugt (beim Start, zwischengespeichert).
- Wie die Liste der vom PDH genutzten Symbole entsteht (Test sammelt sie und
  vergleicht mit einer eingecheckten Liste, oder der Server sammelt sie beim
  Start) – Hauptsache: ein neues `ti-…` im Code ohne Eintrag fällt im Test auf.
- Feinheiten der Ausrichtung, damit Symbole neben Text wie vorher sitzen.

## Umfang und Version

Nächste Korrekturversion über `main` (heute 0.82.56), keine Migration, kein
neues Recht (es ändert sich nur die Auslieferung). Handbuch: kurzer Satz im
Kapitel „Versionen & Updates“ bzw. dort, wo Symbole und Darstellung beschrieben
sind; Änderungsprotokoll `### Geändert`. Offener Punkt 13 (Internet-Abhängigkeit)
wird damit für die Oberfläche erledigt.
