# Herkunft von Tickets und Störungen, Farbe der Startseiten-Kacheln – Entwurf

Stand 22.11.2026, zur Bestätigung durch den Anwender. Version: nächste freie
Korrekturversion.

## Anlass

Wunsch des Anwenders (22.11.2026): „ergänze bei jedem Import von wo dieser
kommt – bei Tickets und Störungen sollen diese in der Dashboard-Kachel
sichtbar sein – evtl. eine Farbauswahl des Kachelhintergrundes, alles helle
Farben“. Teil desselben Wunsches ist die Copilot-Unterstützung beim Anlegen
und Zuordnen von Importen und Exporten; die kommt als eigenes, großes Paket
mit eigenem Entwurf danach.

## Heute

- Ein Herkunftsfeld gibt es nicht. Die Herkunft lässt sich nur ablesen bei
  **Abgleich** (`sync_links` → Profilname, nur auf der Detailseite sichtbar),
  **App** (`tickets.app_request_id`) und **Alarm-Tickets** (Systembenutzer,
  Energiedaten und IT-Netz). Terminal und Easy-Modus schreiben nur eine
  Textzeile in die Beschreibung.
- Kacheln: „Offene Tickets“ und „Aktive Störungen“ zeigen nur Zahlen,
  „Aktuelle Störungen“ eine Liste mit Anlage. Kacheln haben keine Farbe zur
  Auswahl.

## Ziele

1. **Herkunft an jedem Ticket und jeder Störung**, festgehalten beim Anlegen:
   - Web (von Hand) – wird nicht hervorgehoben,
   - Easy-Modus, Terminal (mit Name des Terminals), App,
   - Abgleich mit Fremdsystem (Name des Profils, z. B. „SAP PM“),
   - Alarm (Energiedaten, IT-Netz), System (z. B. Firewall-Freigabe).
   Vorhandene Datensätze bekommen sie nachträglich, soweit erkennbar
   (Abgleich, App, Alarm); der Rest bleibt „unbekannt“ und wird nicht
   angezeigt.
2. **Sichtbar in den Kacheln:**
   - „Offene Tickets“ und „Aktive Störungen“: unter der Zahl eine Zeile
     „davon importiert: 3 aus SAP PM · 2 aus der App · 1 vom Terminal“
     (nur Wege, die vorkommen; „von Hand“ zählt nicht).
   - „Aktuelle Störungen“ und „Mir zugewiesen“: je Zeile ein kleines Schild
     mit der Herkunft (z. B. „SAP PM“, „Terminal Presse 3“).
   - Auch in den Listen und auf der Detailseite von Tickets und Störungen
     (dort heute nur beim Abgleich).
3. **Hintergrundfarbe je Kachel:** beim Bearbeiten der Startseite an jeder
   Kachel eine Farbauswahl mit hellen Tönen: Standard, Hellblau, Mint,
   Hellgrün, Hellgelb, Pfirsich, Rosé, Flieder, Hellgrau. Gilt für jede
   Kachel (nicht nur Tickets und Störungen), je Benutzer.

## Nicht-Ziele

- Keine neue Rechte-Ebene: Die Herkunft sieht, wer den Datensatz sieht.
  Den Namen eines Abgleich-Profils sieht weiterhin nur, wer `sync.view` hat;
  sonst steht dort nur „Fremdsystem“.
- Keine Filter nach Herkunft in dieser Stufe (später möglich).

## Ansätze

- **A – eigene Spalte `origin` an Tickets und Störungen** (Vorschlag): eine
  Migration, jeder Anlage-Weg setzt sie; Kacheln zählen per `GROUP BY`.
  Schnell, eindeutig, auch für spätere Filter.
- B – Herkunft bei jeder Anzeige ableiten (Joins auf `sync_links`,
  `app_requests`, Textzeile): keine Migration, aber Terminal und Easy-Modus
  nur über die Textzeile – unzuverlässig.

## Farben in hell und dunkel (mein Vorschlag)

- Im hellen Modus die gewählte helle Farbe als Hintergrund, Text bleibt
  dunkel.
- Im **dunklen Modus** derselbe Farbton gedämpft (dunkle Tönung), sonst
  wären helle Flächen zwischen dunklen Kacheln grell und die helle Schrift
  unlesbar. Fiori und Fluent wie bisher über die Farbvariablen.

## Rechte

- Kein neues Recht: Kachelfarbe ist eine persönliche Einstellung der eigenen
  Startseite; die Herkunft folgt den Sichtrechten des Datensatzes.

## Dem Bauenden überlassen

- Genaue Farbwerte (Kontrast mindestens 4,5:1 zur Schrift).
- Schreibweise der Schilder, Kürzen langer Namen.
