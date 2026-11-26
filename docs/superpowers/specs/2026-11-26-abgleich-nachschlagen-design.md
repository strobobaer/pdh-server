# Entwurf: Abgleich mit Nachschlagetabellen – Störung oder Ticket aus mehreren Tabellen

Stand 26.11.2026 · Wunsch des Anwenders: „Ich habe in einer SQL-Datenbank 4 Tabellen,
die zur Störungsfeststellung sind: eine Tabelle Fault gibt die Fehlermeldung an, eine
gibt die Anlage an verknüpft mit einer Zahl, eine den Anlagenteil verknüpft mit einer
Zahl, eine den Stillstandsgrund wiederum verknüpft mit einer Zahl, eine die Kommentare
verknüpft mit der Störungsnummer. Wie kann ich diese Daten verwenden, um eine Störung
oder ein Ticket auszulösen?“ – Ergänzung: „Störung, wenn Tabellenwert true oder false
ist. Art der Störung (elektrisch, mechanisch, Rüstzeiten, Reinigung …) wird auch per
Tabelle eingelesen, verknüpft als Zahl. Meldender wird als Text in Fault angegeben,
der soll dann als Meldender eingetragen werden.“

Entschieden (26.11.2026): Ziel **je nach Wert**; Anlagen über **Liste mit Vorschlag**;
Stillstandsgrund als **Stillstandsgrund des PDH**; **Kommentare einlesen**. Mit
„Vorschlag“ markierte Punkte hat Claude festgelegt.

## 1. Heute (Abgleich, `internal/modules/extsync`)

- Ein Profil liest **eine** Tabelle (Schlüssel, Änderungsspalte – seit 0.82.70 auch
  `datetimeoffset`) und legt Tickets **oder** Störungen an.
- Zuordenbar: Titel, Beschreibung, Priorität/Schweregrad, Status, Fällig am, Lösung,
  Ursache; je Feld Richtung, fester Wert, Übersetzung fester Werte.
- Die Kommentartabelle wird nur **beschrieben** (Kommentare aus dem PDH hinaus).
- Es fehlen: Zahlen in Texte auflösen, Anlage, Kategorie, Stillstandsgrund,
  Meldender, Ziel je Zeile, Kommentare herein. Sichten (Views) nimmt der Abgleich
  nicht an.

## 2. Beispiel des Anwenders

| Fremdtabelle | Inhalt | Verbindung |
|---|---|---|
| `Fault` (Quelle) | Fehlermeldung, Meldender (Text), Ja/Nein-Spalte „Störung“, Zahlen für Anlage, Anlagenteil, Stillstandsgrund, Art | Schlüssel = Störungsnummer |
| Anlagen | Nummer → Name | `Fault.<Anlage>` |
| Anlagenteile | Nummer → Name | `Fault.<Anlagenteil>` |
| Stillstandsgründe | Nummer → Text | `Fault.<Grund>` |
| Arten | Nummer → Text (elektrisch, mechanisch, Rüstzeiten, Reinigung …) | `Fault.<Art>` |
| Kommentare | Text, Verfasser, Zeit | `Kommentar.<Störungsnummer>` = Schlüssel von `Fault` |

Die echten Spaltennamen liefert der Anwender (Vorlage „Spalten einer Tabelle“).

## 3. Was gebaut wird

### 3.1 Nachschlagetabellen

- Im Schritt **1 Quelle** neu: **„Nachschlagen“** – je Zahlenspalte der Quelle eine
  Nachschlagetabelle: Tabelle, Spalte mit der Nummer, Spalte mit dem Text (Auswahl aus
  der Datenbank, wie bei der Quelle). Höchstens 8 je Profil (*Vorschlag*).
- Beim Einlesen löst das PDH die Zahl auf (eine Abfrage je Nachschlagetabelle und
  Seite, nicht je Zeile). Der Text steht danach wie eine eigene Spalte zur Verfügung,
  z. B. `Anlage.Name` – für Titel, Beschreibung und die Zuordnungslisten.
- Unbekannte Nummer: Feld bleibt leer, die Zeile kommt trotzdem; Hinweis im Lauf.
- Nur lesend; geschrieben wird weiter nur in die Quelle (und Kommentare).

### 3.2 Ziel je Zeile: Störung oder Ticket

- Im Schritt **2 Ziel** neu: **„Je nach Spalte“** – Ja/Nein-Spalte wählen und
  festlegen, welcher Wert eine **Störung** ist; der andere wird ein **Ticket**
  (Ja/Nein, 1/0, true/false; leer → Ticket, *Vorschlag*).
- Ein Profil, zwei Feldsätze: Zuordnungen für Störung und Ticket getrennt, wo die
  Felder abweichen (Schweregrad ↔ Priorität, Status-Werte); gemeinsame Felder einmal.
- **Wechselt** der Wert später (aus Ticket wird Störung): das PDH legt nichts doppelt
  an, sondern meldet es am vorhandenen Vorgang („im Fremdsystem jetzt Störung“) und
  verknüpft auf Wunsch eine neue Störung mit dem Ticket (*Vorschlag, Frage A*).

### 3.3 Anlage und Anlagenteil

- Neues Zielfeld **„Anlage“**. Quelle ist die Anlagennummer (bzw. ihr Text aus 3.1).
- **Zuordnungsliste** (entschieden): Fremd-Nummer → PDH-Anlage, einmal einrichten.
  Das PDH schlägt nach dem Namen vor (gleicher oder ähnlicher Name, Pfad sichtbar),
  der Anwender bestätigt oder wählt. Neue, noch nicht zugeordnete Nummern erscheinen
  als „nicht zugeordnet“ in einer Liste am Profil; die Meldung kommt trotzdem, ohne
  Anlage (*Vorschlag*).
- **Anlagenteil**: zweite Liste Fremd-Nummer → Unteranlage; ist sie zugeordnet,
  hängt die Meldung an der Unteranlage, sonst an der Anlage (*Vorschlag*).
- Rechte: Die Liste pflegt, wer den Abgleich bearbeiten darf (`sync.edit`); als Ziel
  kommen nur Anlagen in Frage, die diese Person bearbeiten darf.

### 3.4 Art der Störung → Kategorie

- Neues Zielfeld **„Kategorie“** (vorhandene Kategorien des PDH, `categories`).
- Zuordnungsliste Fremd-Nummer (Art) → Kategorie, mit Vorschlag nach Namen; fehlt eine
  Kategorie, kann sie dort gleich angelegt werden (Recht der Kategorien-Pflege).
- „Rüstzeiten“ und „Reinigung“ werden so zunächst Kategorien einer Störung bzw.
  eines Tickets. Ob eine Art statt einer Störung eine **Rüstzeit** im PDH anlegen
  soll, ist *Frage B*.

### 3.5 Stillstandsgrund

- Neues Zielfeld **„Stillstandsgrund“** (entschieden: Katalog des PDH,
  `downtime_reasons`), Zuordnungsliste Fremd-Nummer → Grund mit Vorschlag nach Namen.
- Nur bei Störungen; beim Ticket steht der Grund als Text in der Beschreibung.

### 3.6 Meldender

- Neues Zielfeld **„Gemeldet von“** aus der Textspalte in `Fault`.
- Passt der Text zu genau einem PDH-Benutzer (Vor- und Nachname, Benutzername oder
  Personalnummer), wird er als Meldender eingetragen; sonst steht der Text als
  **externer Meldender** am Vorgang („Max Muster (aus cybernetics)“). Dafür
  bekommen Störungen und Tickets das Feld `reporter_name` (Migration) –
  *Vorschlag*. Benachrichtigungen an den Meldenden nur bei erkanntem Benutzer.

### 3.7 Kommentare einlesen

- Die Kommentartabelle (Tabelle, Spalte Störungsnummer, Text, Verfasser, Zeit) wird
  zusätzlich **gelesen**: neue Kommentare erscheinen an der Störung bzw. dem Ticket
  mit Verfasser und Zeit aus dem Fremdsystem; jeder Kommentar genau einmal (Schlüssel
  des Kommentars bzw. Prüfwert aus Text und Zeit).
- Zurückschreiben bleibt wie heute (Schalter „Schreiben erlaubt“). Kommentare, die
  das PDH selbst hinausgeschrieben hat, kommen nicht doppelt herein.

### 3.8 Vorlage

- „**Störmeldung mit Anlage, Anlagenteil, Grund, Art und Kommentaren**“: Beim Anlegen
  des Profils wählbar; sie legt die Nachschlagetabellen, Zielfelder und Listen an,
  der Anwender wählt nur noch Tabellen und Spalten aus.

## 4. Test und Betrieb

- Der vorhandene **Trockenlauf** zeigt je Zeile: Störung/Ticket, aufgelöste Texte,
  Anlage (oder „nicht zugeordnet“), Kategorie, Grund, Meldender, Kommentare.
- Labor: Tabellen nach dem Muster des Anwenders im Labor-SQL-Server
  (`deploy/lab/mssql/seed.sql`), Integrationstests für jeden Schritt.
- Keine neuen Rechte (Abgleich: `sync.view`/`sync.edit`); Migration für die Listen,
  Nachschlagetabellen und `reporter_name`.

## 5. Schnitt (Vorschlag)

1. Nachschlagen + Ziel je Zeile + Meldender (3.1, 3.2, 3.6).
2. Anlage/Anlagenteil, Kategorie, Stillstandsgrund mit Zuordnungslisten (3.3–3.5).
3. Kommentare einlesen und Vorlage (3.7, 3.8).

## 6. Fragen an den Anwender

- **A.** Wenn sich der Ja/Nein-Wert später ändert: nur am vorhandenen Vorgang melden
  (Vorschlag) oder den Vorgang umwandeln?
- **B.** Soll eine Art wie „Rüstzeiten“ statt einer Störung eine **Rüstzeit** im PDH
  anlegen, oder reicht die Kategorie?
- **C.** Meldender ohne passenden Benutzer: als externer Name speichern (Vorschlag)
  oder einen Benutzer „aus cybernetics“ verwenden?
- **D.** Reihenfolge der drei Teile so?
