# Messung des Seitenaufbaus

`messung.js` misst, wie schnell das PDH eine Seite aufbaut: je Seite und Modus
`DOMContentLoaded`, `load` und „alle Symbole sichtbar“, bei voller und bei
4-fach gedrosselter CPU-Leistung. Es dient dem Paket „Schneller Seitenaufbau“
(Entwurf `docs/superpowers/specs/2026-11-24-seitenaufbau-design.md`).

## Voraussetzungen

- Node.js und das Paket `playwright-core` (nicht im Repository; in einem
  eigenen Ordner mit `npm install playwright-core` anlegen).
- Microsoft Edge (`--channel msedge`, Standard) oder ein anderer Kanal mit
  `--channel chrome`.
- Ein PDH-Konto, das sich anmelden darf. Am Testverbund ist das das Prüfkonto
  `abnahme-abgleich`; wie man es für die Prüfung freischaltet und danach wieder
  sperrt, steht in `AGENTS.md` unter „Browserprüfung auf dem Testverbund“.
  Das Kennwort kommt nur aus einer Datei außerhalb des Repositorys
  (`--password-file`) oder aus der Umgebung (`PDH_PASSWORD`), nie aus dem Skript.

## Aufruf

    node deploy/lab/seitenaufbau/messung.js \
      --base http://10.10.0.15:8090 --user abnahme-abgleich \
      --password-file /pfad/zur/kennwortdatei \
      --playwright /pfad/zu/node_modules/playwright-core

Weitere Angaben: `--pages /,/tickets` (Standard: `/`, `/energy/meters`,
`/tickets`, `/maintenance`, `/infrastructure`), `--cpu 1,4`, `--runs 3`
(Läufe je Zeile, es gilt der Median),
`--json datei.json`. Ohne Angaben oder bei einem Fehler endet das Skript mit
einer Meldung und dem Code 2 bzw. 1.

## Was gemessen wird

- **erster Aufruf:** je Lauf ein neuer Browser-Kontext, also ein ganz leerer
  Zwischenspeicher. Nur die Anmeldung (Cookies) wird aus einer einmal
  gespeicherten Sitzung übernommen; die Anmeldeseite füllt den
  Zwischenspeicher so nicht.
- **zwischengespeichert:** die Seite wird im selben Kontext ein zweites Mal
  aufgerufen; gemessen wird der zweite Aufruf.
- **DCL / load:** `domContentLoadedEventEnd` und `loadEventEnd` der Navigation.
- **Symbole (alle Symbole sichtbar):** Zeit seit Navigationsbeginn, zu der
  (1) die Symbol-Stildatei `/vendor/<Version>/icons-<Kennung>.css` geladen und
  angewendet ist, (2) alle Einzel-SVGs unter `/vendor/<Version>/icons/` fertig
  geladen sind und (3) das zwei Bildwiederholungen lang unverändert blieb.
  Eingebettete Symbole brauchen keine eigene Anfrage. „SVGs“ in der Tabelle
  ist die Zahl der nachgeladenen Einzel-SVGs.
- Es wird auf das Laden der Seite gewartet, nicht auf „networkidle“ (die
  Live-Verbindungen bleiben offen). Playwright-Anfragefilter (`page.route`)
  werden nicht benutzt, weil sie den HTTP-Zwischenspeicher abschalten würden.

## Ziel

Erster Aufruf: `load` und „Symbole“ höchstens 0,5 s bei voller Leistung und
höchstens 1,5 s bei 4-facher CPU-Drosselung; aus dem Zwischenspeicher nicht
langsamer als vorher. Ausgangswerte vor dem Paket (Testverbund,
24.11.2026, Edge): zwischengespeichert 0,06–0,1 s (4×: 0,35–0,4 s), erster
Aufruf mit Symbolschrift 6,7–7,0 s (4×: 29–33 s).
