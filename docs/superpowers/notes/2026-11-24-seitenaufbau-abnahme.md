# Abnahme PDH 0.82.57 „Schneller Seitenaufbau“ am Testverbund

Ziel: http://192.0.2.15:8090 (aktiv: pdh-server 192.0.2.17), Edge über playwright-core, Konto `abnahme-abgleich`.
Skripte, Bilder und Rohdaten: `.superpowers/sdd/2026-11-24-seitenaufbau/abnahme/` (`pruef.js`, `ergebnis.json`, `messung.json`).
Das Umschalten von Stil und Hell/Dunkel geschah nur im Browser (`data-style`/`data-theme`), es wurden keine Einstellungen des Kontos gespeichert, also nichts zurückzusetzen.

## 1. Messung (messung.js, Median von 3 Läufen, Sekunden)

| Seite | Modus | CPU | DCL | load | Symbole |
|---|---|---|---|---|---|
| / | erster Aufruf | 1x | 0,24 | 0,28 | 0,44 |
| / | zwischengespeichert | 1x | 0,10 | 0,10 | 0,18 |
| /energy/meters | erster Aufruf | 1x | 0,24 | 0,27 | 0,31 |
| /energy/meters | zwischengespeichert | 1x | 0,06 | 0,07 | 0,13 |
| /tickets | erster Aufruf | 1x | 0,21 | 0,24 | 0,29 |
| /tickets | zwischengespeichert | 1x | 0,06 | 0,07 | 0,13 |
| /maintenance | erster Aufruf | 1x | 0,22 | 0,23 | 0,31 |
| /maintenance | zwischengespeichert | 1x | 0,09 | 0,10 | 0,15 |
| /infrastructure | erster Aufruf | 1x | 0,21 | 0,25 | 0,33 |
| /infrastructure | zwischengespeichert | 1x | 0,08 | 0,09 | 0,16 |
| / | erster Aufruf | 4x | 1,12 | 1,18 | 1,59 |
| / | zwischengespeichert | 4x | 0,43 | 0,45 | 0,69 |
| /energy/meters | erster Aufruf | 4x | 1,51 | 1,60 | 1,83 |
| /energy/meters | zwischengespeichert | 4x | 0,40 | 0,45 | 0,54 |
| /tickets | erster Aufruf | 4x | 1,31 | 1,47 | 1,67 |
| /tickets | zwischengespeichert | 4x | 0,41 | 0,45 | 0,53 |
| /maintenance | erster Aufruf | 4x | 1,19 | 1,21 | 1,32 |
| /maintenance | zwischengespeichert | 4x | 0,37 | 0,40 | 0,49 |
| /infrastructure | erster Aufruf | 4x | 1,18 | 1,26 | 1,43 |
| /infrastructure | zwischengespeichert | 4x | 0,38 | 0,44 | 0,54 |

Spalte „SVGs“ des Skripts: überall 0 (Symbole sind eingebettet, keine Einzel-SVG-Anfragen).

Bewertung gegen die Ziele (erster Aufruf, load und Symbole <= 0,5 s bei 1x, <= 1,5 s bei 4x):
- 1x: erfüllt auf allen 5 Seiten (load 0,23–0,28 s, Symbole 0,29–0,44 s). Vorher 6,7–7,0 s.
- 4x: load erfüllt bis auf `/energy/meters` (1,60 s); Symbole nicht erfüllt auf `/` (1,59 s), `/energy/meters` (1,83 s) und `/tickets` (1,67 s). Erfüllt: `/maintenance` (1,32 s), `/infrastructure` (1,43 s). Vorher 29–33 s, also rund 20-fach besser, aber das 1,5-s-Ziel mit 4x-Drosselung wird auf 3 von 5 Seiten bei „Symbole“ knapp verfehlt (um 0,1–0,33 s).
- Zwischengespeichert: 0,06–0,10 s (vorher 0,06–0,10 s), 4x 0,40–0,45 s (vorher 0,35–0,4 s): praktisch unverändert, die 4x-Werte liegen leicht (0,03–0,05 s) darüber.
- Fehlgeschlagene `/vendor/`-Anfragen (eigenes Netzwerkprotokoll der 120 Ansichten und Sonderprüfungen): 0 von 605 Antworten (alle 200); kein `requestfailed`, keine 4xx.

## 2. Netzwerk-Hosts (alle geöffneten Seiten)

| Host | Seiten |
|---|---|
| 192.0.2.15:8090 | alle (`/`, `/tickets`, `/maintenance`, `/infrastructure`, `/admin/icons`, `/help`, `/login`) |

Keine anderen Hosts (kein unpkg, jsdelivr, cdnjs, Google Fonts); data:-URIs wurden vom Protokoll nicht als Anfrage geführt. `/vendor/`-Antworten mit Status ungleich 200/304: keine (605 mal 200).

## 3. Echte Seiten, 6 Seiten x 20 Ansichten = 120 Ansichten

Fiori/Fluent x hell/dunkel x 320, 390, 768, 1024, 1366 px; geprüft wurden Seitenüberlauf (`scrollWidth > innerWidth + 1`), Konsolenfehler und `.ti`-Elemente ohne `::before`-Maske.

| Seite | Ansichten | Überlauf | Konsolenfehler | Symbole ohne Maske |
|---|---|---|---|---|
| / | 20 | 0 | 0 | 0 |
| /tickets | 20 | 0 | 0 | 0 |
| /maintenance | 20 | 0 | 0 | 0 |
| /infrastructure | 20 | 0 | 0 | 0 |
| /admin/icons | 20 | 0 | 0 | 0 |
| /help | 20 | 0 | 0 | 0 |

Befunde: keine. Bildschirmfotos (hell, 390 und 1366 px, beide Stile) liegen als `<seite>-<stil>-light-<breite>.png` im Ordner; Stichprobe `4b-forced-_tickets-dark.png` zeigt die Symbole sichtbar. Nur sichtbare `.ti`-Elemente wurden gezählt (z. B. 116 auf `/`, 91 auf `/tickets`).

## 4. Einzelprüfungen

a) `/admin/icons`, unbekannter Name `gibts-nicht-xyz`: Vorschau-`<i>` hat Klasse `ti ti-gibts-nicht-xyz`, `::before` hat `mask-image: linear-gradient(transparent, transparent)` – kein gefülltes Quadrat (Bild `4a-unbekanntes-symbol.png`: leeres Feld). Auswahlsuche mit dem Namen: „0 Symbole“, keine Schaltflächen (`4a-suche-leer.png`). In Ordnung.

b) Windows-Kontrastmodus (`forcedColors: active`, bestätigt durch `matchMedia`): `/` und `/tickets`, hell und dunkel: 116 bzw. 91 Symbole, 0 ohne Maske, 0 transparent; `::before`-Hintergrundfarbe `rgb(0, 0, 0)` (Systemfarbe). Bilder `4b-forced-*.png`: Symbole sichtbar. In Ordnung.

c) Druckansicht (`media: print`, `/tickets`): 91 Symbole, 0 ohne Maske, Farbe `rgb(29, 45, 62)`. Bild `4c-print-tickets.png`, zusätzlich PDF `4c-print-tickets.pdf`. In Ordnung.

d) Alte Adressen:
- `GET /vendor/tabler-icons/tabler-icons.min.css?v=0.82.56` -> 302, `Cache-Control: no-store`, Ziel `/vendor/0.82.57/icons-b73c20f8a6.css`. In Ordnung.
- `GET /vendor/0.82.56/htmx/htmx.min.js` -> 302, `no-store`, Ziel `/vendor/0.82.57/htmx/htmx.min.js`. In Ordnung.
- `GET /vendor/0.82.57/htmx/htmx.min.js` -> 200, `public, max-age=31536000, immutable`. In Ordnung.

## 5. Testdaten

Keine angelegt oder geändert (nur lesende Aufrufe, Eingaben nur im Formularfeld der Seite ohne Speichern). Nichts zu entfernen. Kennwort wurde nur programmatisch gelesen.

## Zusammenfassung der Befunde

1. Ziel bei 4x CPU auf „Symbole sichtbar“ knapp verfehlt: `/` 1,59 s, `/energy/meters` 1,83 s (load 1,60 s), `/tickets` 1,67 s (Ziel 1,5 s). Alle übrigen Ziele erfüllt. Hinweis: Messung in einer Umgebung mit Edge headless, 3 Läufe, Median.
2. Sonst keine Befunde.
