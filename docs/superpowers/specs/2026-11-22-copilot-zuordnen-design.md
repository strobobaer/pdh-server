# Copilot beim Anlegen und Zuordnen von Importen und Exporten – Entwurf

Stand 22.11.2026, zur Bestätigung durch den Anwender. Version: nächste freie
Korrekturversion (nach „Herkunft und Kachelfarbe“).

## Anlass

Wunsch des Anwenders (22.11.2026): „beim Anlegen und vor allem beim Zuordnen
von Imports und Exports jeglicher Art eine volle Copilot-Unterstützung im
Code Style“. Entschieden: **C** – Vorschläge zum Abhaken direkt am Formular
**und** ein Assistent im Copilot-Reiter, der Änderungen wie ein
Programmier-Assistent als Änderungsliste vorschlägt. Copilot im Betrieb:
**Anthropic**.

## Heute

- Wertzuordnung (`import_mappings`): Quellwert (MQTT-Topic, Excel-/CSV-Spalte,
  MS-SQL-Spalte, REST-Pfad, OPC-UA-Knoten, Modbus-Register) → Anlage,
  Bezeichnung, Notiz, wahlweise Heartbeat. Von Hand, einzeln.
- Export-Zuordnung (`export_mappings`): Import-Zuordnung → Feldname je
  Export-Verbindung. Von Hand, einzeln.
- Copilot (Anthropic) kann mit Werkzeugen lesen (Vorgänge, Anlagen,
  Ersatzteile, aktuelle Importwerte), immer mit den Rechten der Person
  (`copilot_data.go`). Er legt nichts an.

## Ziele

1. **Vorschläge am Formular (A), ohne Sprachmodell:** In jeder
   Zuordnungsseite einer Verbindung eine Liste „Vorgeschlagene Zuordnungen“
   für noch nicht zugeordnete Quellwerte: Anlage (aus Namensähnlichkeit von
   Topic/Spalte/Knoten mit Anlagenname und -pfad), Bezeichnung (lesbar aus dem
   Quellnamen), Einheit/Art aus Name und Wert (z. B. `strom` → A, `temp` →
   °C, 0/1 → Zustand). Je Zeile Haken, Felder änderbar, „Ausgewählte
   übernehmen“. Funktioniert ohne Internet und ohne Copilot.
2. **Assistent im Copilot-Reiter (B):** Auf jeder Verbindungs- und
   Zuordnungsseite versteht der Copilot Aufträge wie „ordne alle Topics von
   Halle 2 den Pressen zu“, „exportiere alle Stromwerte von Presse 3 nach
   Efficio“ oder „warum kommt bei dieser Verbindung nichts an?“. Er liest
   Verbindung, Quellwerte, Anlagenbaum und vorhandene Zuordnungen und
   antwortet mit einer **Änderungsliste wie ein Diff**:
   `+ neu`, `~ geändert`, `= unverändert`, `– wird nicht angefasst`, je Zeile
   mit kurzer Begründung. Der Anwender kann Zeilen abwählen und drückt
   **Übernehmen**; erst dann legt das PDH an. Rückfragen stellt der Copilot
   im Chat, wenn etwas mehrdeutig ist.
3. **Beim Anlegen einer Verbindung:** Der Copilot schlägt die Einstellungen
   aus einem Satz vor („MQTT-Broker 10.10.0.50, Benutzer maschinen“) und
   füllt das Formular – gespeichert wird erst mit dem normalen Speichern.
   Kennwörter fragt er nie ab und schreibt sie nie in den Chat.
4. **Alle Arten:** MQTT, Excel/CSV, MS SQL, **MySQL, SQLite** (gibt es schon),
   REST, OPC UA, Modbus, **InfluxDB (neu)**; Export-Zuordnungen;
   Feldzuordnung beim Abgleich mit Fremdsystemen.
5. **Vorlagen aus dem Internet** (Wunsch 22.11.2026): Der Copilot sucht auf
   Wunsch im Internet nach Vorlagen – z. B. Modbus-Registertabelle eines
   Zählers (Hersteller und Typ), MQTT-Themen eines Geräts (Shelly, Tasmota),
   OPC-UA-Knoten einer Steuerung, Tabellenaufbau einer InfluxDB – und macht
   daraus einen Vorschlag mit **Quellenangabe** (Link). Übernommen wird wie
   immer erst nach Prüfung und „Übernehmen“. Nur mit Anthropic (Websuche).
6. **Berechnete Werte mit Formelgenerator** (Wunsch 22.11.2026): Werte aus
   mehreren Zuordnungen zusammenführen, z. B.
   `Gesamtleistung Halle 2 = [Presse 1 Leistung] + [Presse 2 Leistung]` oder
   `Wirkungsgrad = [Ausgang] / [Eingang] × 100`.
   - Eine berechnete Zuordnung hat Anlage, Bezeichnung, Einheit und eine
     **Folge von Formeln mit Gültigkeit**: Formel 1 gilt bis Datum 1, ab
     Datum 1 gilt Formel 2 bis Datum 2 usw. Lücken und Überschneidungen gibt
     es nicht – jede neue Fassung beendet die vorige.
   - Formelgenerator am Formular: Werte aus einer Liste anklicken, Rechenzeichen
     und Klammern, sofortige Prüfung (Schreibweise, unbekannte Werte,
     Kreisbezüge) und Probe mit den aktuellen Werten.
   - Der Copilot kann Formeln vorschlagen („Summe aller Pressen in Halle 2“).
   - Gleicher Rechenkern wie die berechneten Messstellen der Energiedaten; die
     bekommen dieselbe Gültigkeit.
   - Berechnete Werte gibt es überall, wo Zuordnungen verwendet werden
     (Anzeige, Copilot, Export).

## Nicht-Ziele

- Kein selbsttätiges Anlegen oder Löschen – jede Änderung braucht
  „Übernehmen“. Löschen schlägt der Copilot nur vor, ausgeführt wird es wie
  bisher von Hand.
- Kein Assistent mit Ollama (Anwender: Anthropic); ohne Copilot bleiben die
  Vorschläge am Formular (A).

## Ansätze für den Assistenten

- **B1 – Werkzeuge + gespeicherter Vorschlag** (Vorschlag): Der Copilot
  bekommt lesende Werkzeuge (Verbindungen, Quellwerte, Anlagenbaum,
  Zuordnungen) und ein Werkzeug „Vorschlag machen“. Das PDH prüft den
  Vorschlag (gibt es Quellwert und Anlage, Rechte, Dopplungen), speichert ihn
  als Entwurf (eigene Tabelle, verfällt nach 24 h) und zeigt ihn als
  Änderungsliste. „Übernehmen“ führt nur geprüfte Zeilen aus – über dieselben
  Wege wie das Anlegen von Hand.
- B2 – Copilot schreibt JSON in den Text, die Seite liest es aus:
  verworfen (fehleranfällig, keine Prüfung auf dem Server).

## Rechte

- Vorschlagen und Fragen: wer den Copilot und die Verbindung sehen darf
  (bestehende Rechte).
- **Übernehmen: dieselben Rechte wie von Hand** (`import.write` bzw.
  Export-/Abgleich-Recht), zusätzlich prüft das PDH je Zeile, ob die Person
  an der Anlage (Abteilung) arbeiten darf.
- Mein Vorschlag: **kein neues Recht** – der Copilot tut nichts, was die
  Person nicht selbst darf. (Falls gewünscht: eigenes Recht
  `copilot.mappings` zum Ein-/Ausschalten je Rolle.)

## Schutz

- Kennwörter, Schlüssel und Verbindungsgeheimnisse gehen nie an den Copilot.
- Quellwerte und Anlagennamen gehen an Anthropic (wie heute beim Copilot);
  der Hinweis steht in der Copilot-Seitenleiste.
- Nicht am Spiegel, nicht in der Wartung (Übernehmen).

## Gültigkeit der Formeln (Vorschlag)

- Gültig ab einem **Datum** (00:00 Uhr Ortszeit); die vorige Fassung gilt bis
  23:59:59 des Vortags. Eine Uhrzeit ist nicht nötig.
- **Rückwirkend:** Wo es einen Verlauf gibt (Energiedaten), rechnet das PDH
  die Werte ab dem neuen Datum mit der neuen Formel neu – auch für Exporte,
  die noch nicht verschickt sind. Schon verschickte Exporte bleiben, wie sie
  waren (Hinweis an der Formel). Ohne Verlauf (übrige Zuordnungen, nur der
  letzte Wert) gilt die neue Formel ab ihrem Datum für den aktuellen Wert.
- Jede Fassung zeigt, wer sie wann angelegt hat; ändern lässt sich nur eine
  Fassung, deren Datum noch in der Zukunft liegt.

## Pakete

1. **Zuordnen (A + B)** für Import- und Export-Zuordnungen aller Arten,
   Vorlagen aus dem Internet.
2. **Berechnete Werte** mit Formelgenerator und Gültigkeit (auch für die
   berechneten Messstellen der Energiedaten).
3. **InfluxDB** als neue Verbindungsart.
4. **Anlegen von Verbindungen** und Feldzuordnung beim Abgleich.

## Dem Bauenden überlassen

- Regeln der Namensähnlichkeit (Umlaute, Trenner, Nummern).
- Grenzen: höchstens 500 Zeilen je Vorschlag, Zeitlimit des Copilot.
