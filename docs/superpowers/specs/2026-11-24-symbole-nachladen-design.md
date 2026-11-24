# Symbole nachladen (Teil 1b Seitenaufbau) – kurzer Entwurf

Stand 24.11.2026 · Branch `fix/symbole-nachladen` ab `main` 0.82.57 (`038c2dd7`) · vom Anwender bestätigt („Ja“ auf den Vorschlag „Stildatei teilen – eingebettete Symbole sofort, Verweise auf die übrigen nachgeladen“).

## Anlass

Abnahme von 0.82.57 am Testverbund (`docs/superpowers/notes/2026-11-24-seitenaufbau-abnahme.md`): erster Aufruf bei 1× CPU 0,29–0,44 s (Ziel 0,5 s erfüllt), bei 4× CPU „Symbole sichtbar“ 1,32–1,83 s – Ziel 1,5 s auf `/`, `/energy/meters`, `/tickets` knapp verfehlt. Die Stildatei `/vendor/<ver>/icons-<key>.css` ist 590 KB roh (64 KB gzip) und blockiert das Zeichnen; davon sind die Verweisregeln der 5 474 nicht eingebetteten Symbole etwa die Hälfte. Der Anwender wollte von Anfang an: „erstmal ohne und im laufenden Betrieb nachladen“.

## Ziel

- Die Seite zeichnet nur mit Grundregeln und eingebetteten Symbolen (PDH-genutzte + gewählte); alle anderen Symbole bleiben nutzbar, kommen aber aus einer zweiten Stildatei, die **nach dem Laden der Seite** nachgeladen wird.
- Ziel 4× CPU ≤ 1,5 s auf allen fünf gemessenen Seiten, 1× unverändert ≤ 0,5 s.

## Nicht-Ziele

Keine Änderung an Einbettung, Kennung, Zwischenspeicher-Regeln (immutable nur laufende Version, fremde Version 302 `no-store`), Symbolauswahl-Logik oder Aussehen.

## Bauweise (Ansatz)

- Zwei Dateien statt einer, beide unter der Version im Pfad und mit derselben Kennung:
  - `/vendor/<ver>/icons-<key>.css` – Grundregeln (`iconCSSBase`) + Regeln der eingebetteten Symbole. Weiter im `<head>` als normales Stylesheet.
  - `/vendor/<ver>/icons-rest-<key>.css` – nur die Verweisregeln `.ti-x{--ti:url(/vendor/<ver>/icons/x.svg)}` aller übrigen Symbole. Gleiche Regeln wie die Hauptdatei: laufende Version + aktuelle Kennung → `immutable`; fremde Kennung → aktuelle Datei `no-store`; fremde Version → 302 `no-store` auf die aktuelle Adresse; gzip und ETag.
- Einbinden über **eine** Vorlagenfunktion für alle Seitenköpfe (base, easy, global_dashboard, push_alert, terminal, terminal_device_error), z. B. `{{iconCSSLinks}}`: das Haupt-`<link>` plus ein kleines Skript, das die Rest-Datei nach dem `load`-Ereignis anhängt (sofort, wenn die Seite schon geladen ist), und ein `<noscript>`-`<link>` als Rückfall. So verzögert die Rest-Datei weder das Zeichnen noch das `load`-Ereignis.
- Bis die Rest-Datei da ist, ist ein seltenes, nicht eingebettetes Symbol unsichtbar (Rückfall-Maske), danach sichtbar – kein Quadrat.
- Druck: `print-color-adjust` bleibt; die Rest-Datei gilt für alle Medien.

## Abgelehnt

- `media="print" onload="this.media='all'"`: lädt früh mit, verzögert aber das `load`-Ereignis.
- Verweisregeln ganz weglassen: verletzt „alle 5 918 Symbole bleiben überall nutzbar“.

## Tests

- Hauptdatei enthält keine `url(/vendor/…/icons/…svg)`-Regel mehr, Rest-Datei genau eine je nicht eingebettetem Namen und keine eingebettete; zusammen genau eine Regel je Name (Ersatz für `TestIconCSSHasRuleForEveryName`).
- Rest-Route: 200 `text/css` `immutable` für laufende Version und Kennung; fremde Kennung `no-store` aktuelle Datei; fremde Version 302 `no-store`; gzip/ETag.
- Alle sechs Seitenköpfe enthalten Haupt-Link, Nachlade-Skript und `<noscript>`-Rest-Link.
- Wächtertests bleiben grün (`TestNoExternalAssets`, `TestNoIconFontUse`, `TestIconsUsedListComplete`).
- Nach dem Einspielen: Messung mit `deploy/lab/seitenaufbau/messung.js` am Testverbund.

## Version

Nächste Korrekturversion über `main` (heute 0.82.58), CHANGELOG `### Geändert` ein Satz; Handbuch unverändert (Wirkung für Anwender: Seiten noch schneller).
