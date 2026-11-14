# Rüstzeiten als Vorgang (Zusatzfunktion)

Stand: 14.11.2026 · Zweig `feature/stillstaende-ruesten`

## 1. Ziel und Zweck

Rüstzeiten sind **Stillstände einer Anlage, die zur Produktion gehören** und
keine Defekte sind: **Produktumstellung**, **produktionsbedingter Tausch von
Hilfsmitteln**, **Reinigung**. Sie werden **gesondert gekennzeichnet**, damit
sie später in **Verfügbarkeitsanalysen** getrennt von Störungen eingehen.

**Umfang dieses Pakets (Vorgaben des Anwenders, 14.11.2026):**
- Zusatzfunktion. Zuerst nur die **Eingabe**: im **PDH wie Tickets** und im
  **Easy-Modus** mit eigenen Rüsttasten.
- Die Rüstzeit wird ein **Vorgang** wie Ticket, Störung, Aufgabe, Wartung.
  Die vorhandenen Vorgänge werden **so weit angepasst, dass die Rüstzeit
  integriert werden kann**.
- Die **Höchstdauer** wird **je Anlage im Stamm** festgelegt. Wird sie
  überschritten, erzeugt das PDH eine **Störmeldung an die Instandhaltung**.

**Zurückgestellt (eigene spätere Vorhaben):** Übernahme aus dem
Fremdsystem, Auswertung der Verfügbarkeit, die Analyse aus MQTT-Zuständen,
Produktionsplan, Schicht- und Pausenzeiten mit Toleranzen und Karenzzeiten
(Unregelmäßigkeit → Begründung durch den Produktionsverantwortlichen oder
Weitergabe an die Instandhaltung). Die Rüstzeit trägt deshalb schon eine
feste **Gruppe** am Grund (siehe 6.1), damit diese Vorhaben ohne Umbau
aufsetzen.

## 2. Entscheidungen des Anwenders (14.11.2026)

1. **Mehrere Tasten je Anlage**, eine je Rüstart, jede **eigens benannt**.
2. **Ein Tipp startet sofort**; „… läuft seit 10:42“ und **„Fertig“**; Notiz
   freiwillig.
3. Im PDH Eingabe **wie Tickets**; Rüstzeit als **Vorgang**, vorhandene
   Vorgänge dafür anpassen.
4. **Höchstdauer je Anlage im Stamm**; Überschreitung → **Störmeldung an die
   Instandhaltung**.
5. Das Wie überlässt der Anwender („so, wie ein Analyst es bauen würde“).
   Festlegungen ohne Vermerk sind Entscheidungen des Entwurfs.

## 3. Ziele, Nicht-Ziele, Fehlschläge

**Ziele**
- Rüstzeit als Vorgang: eigene Seite mit den Reitern wie bei Tickets,
  Detailseite, Verlauf, Feldsätze, Kategorien, Verknüpfungen, Anhänge, Rechte
  mit Abteilungsebene, Erscheinen in der Historie der Anlage.
- Erfassung an der Anlage in einem Tipp, ohne Anmeldung.
- Überwachung der Höchstdauer je Anlage mit automatischer Störmeldung.

**Nicht-Ziele (dieses Paket):** Fremdsystem, Auswertung, Analyse;
Zuständige, Priorität, Fälligkeit, Zuweisung über die Broker, „Mir
zugewiesen“, Leitstand, Kalender, Abschluss-Assistent, Ersatzteile für
Rüstzeiten.

**Fehlschläge (auch wenn alles technisch läuft)**
- Eine Rüstzeit innerhalb der Höchstdauer löst eine Benachrichtigung, ein
  Ticket oder eine Störmeldung aus.
- Eine Überschreitung erzeugt keine oder mehrere Störmeldungen.
- An einer Anlage laufen zwei Rüstzeiten gleichzeitig.
- Das Erfassen an der Anlage braucht mehr als einen Tipp.
- Die Anpassung des gemeinsamen Arbeitsbereichs ändert etwas an Tickets,
  Störungen, Aufgaben oder Wartung.
- Ohne eingerichtete Rüsttasten ändert sich am Easy-Modus oder an der
  Anlagenseite etwas.

## 4. Rahmen

- Go, html/template, htmx, Postgres; Regeln aus `AGENTS.md` (Handbuch,
  Änderungsprotokoll, beide Design-Stile, Rechte mit Abteilungsebene und
  Bereichsadministrator).
- **Kein zentrales Verzeichnis der Vorgangsarten:** Jede Art hat ihre
  Tabelle; rund 35 Stellen zählen die Arten einzeln auf (Erkundung vom
  14.11.2026). Bestes Vorbild für eine Art ohne Zuständige ist **KVP**.
- Die Arbeitsbereiche von Ticket, Störung, Aufgabe, Wartung entstehen aus
  `workKinds` (`internal/web/work_board.go`) mit Reitern Vorgänge, Board,
  Regeln, Archiv. Sie setzen Priorität (`PrioExpr`), Fälligkeit (`DueKey`),
  Zuweisung über die Broker (`BrokerColumn`) und einen Status-Ablauf voraus.
- Vorbild für Katalog, Zuordnung zu Anlagen mit Vererbung und Übersetzung:
  die Meldetexte (`easy_report_texts`, `infra_easy_texts`,
  `internal/web/easy_texts.go`).
- Benachrichtigungen entstehen über Auslöser auf den Vorgangstabellen
  (`record_change_queue`, Migrationen 072/106). Die Rüstzeit bekommt diese
  Auslöser **nicht**.

## 5. Wege, die betrachtet wurden

| Weg | Inhalt | Bewertung |
|---|---|---|
| A – eigenständige Rüstzeit-Seiten | Eigene Liste, Detailseite, Katalog; nur lose an Vorgänge angebunden. | Weniger Eingriff, aber nicht „wie Tickets“ und nicht in den Vorgängen. Vom Anwender verworfen. |
| **B – Rüstzeit als Vorgangsart, gemeinsamer Arbeitsbereich angepasst** (gewählt) | Eigene Tabelle; Eintrag in die gemeinsamen Verzeichnisse der Vorgänge; der Arbeitsbereich (`workKinds`) bekommt optionale Priorität, Fälligkeit und Zuweisung. | Vorgabe des Anwenders. Einmaliger Aufwand im gemeinsamen Teil; danach passen auch künftige Arten ohne Zuständige. |
| C – Rüstzeit als Ticket mit Kennzeichen | Kein neuer Typ, nur ein Merkmal am Ticket. | Ein Ticket ist ein Arbeitsauftrag mit Zuständigen und Benachrichtigung; Zeitspanne und Auswertung passen nicht. Verworfen. |

## 6. Aufbau

### 6.1 Gründe

Jeder Grund gehört zu einer **Gruppe** (fest im Code, bestimmt später die
Wirkung auf die Verfügbarkeit). In diesem Paket nur die Gruppe **`setup`
„Rüsten“**. Die Migration legt drei Gründe an: **Produktumstellung**,
**Hilfsmittel-Tausch**, **Reinigung**. Gründe lassen sich umbenennen,
ergänzen und deaktivieren; löschen nur, solange keine Rüstzeit daran hängt.

### 6.2 Rüsttasten

Eine **Rüsttaste** hat **Name** (deutsch, frei, bis 60 Zeichen), **Grund**
(Pflicht), **Reihenfolge** (für alle Anlagen gleich) und aktiv/inaktiv. Sie
wird **Anlagen zugeordnet** mit derselben Vererbung wie die Meldetexte (an
Halle oder Linie zugeordnet → gilt darunter). Katalog im Infrastruktur-Stamm
neben den Meldetexten, Zuordnung im Reiter der Anlage neben „Meldetexte“. Im
Easy-Modus übersetzt in der Sprache des Handys.

Begründung (Entwurf): Der Grund steht fest an der Taste, damit eine spätere
Auswertung sauber nach Grund trennt, egal wie die Tasten heißen.

### 6.3 Höchstdauer je Anlage (Stamm)

- Neues Feld an der Anlage: **„Höchstdauer Rüsten (Minuten)“**, im Stamm der
  Anlage bearbeitbar. **Vererbung** wie bei den Rüsttasten: Ist es an der
  Anlage leer, gilt der Wert der nächsten übergeordneten Anlage, an der er
  steht. Die Seite der Anlage zeigt den wirksamen Wert und woher er kommt
  („über Linie 2“).
- Ist an der Anlage und darüber **nichts** eingetragen, wird nicht überwacht
  (Entscheidung des Entwurfs: kein erfundener Vorgabewert). Die Rüstzeiten-
  Liste hebt laufende Rüstzeiten über 24 Stunden trotzdem hervor.
- Gilt für alle Rüsttasten der Anlage gleich (Vorgabe „je Anlage“).

### 6.4 Rüstzeit (Vorgang)

Eigene Tabelle. Felder:

| Feld | Inhalt |
|---|---|
| Nummer | fortlaufend, Anzeige `RZ-0001` (wie KVP) |
| Anlage | Pflicht (`infrastructure_id`) |
| Beginn | Pflicht |
| Ende | leer, solange sie läuft |
| Grund | Pflicht (daraus die Gruppe) |
| Rüsttaste | mit welcher gestartet; Name wird mitgespeichert |
| Herkunft | `easy` (an der Anlage), `manual` (im PDH); später `foreign`, `analysis` |
| Status | `running` „Läuft“, `overrun` „Überschritten“, `done` „Beendet“ |
| Höchstdauer | wirksamer Wert beim Start (fest am Vorgang, damit eine spätere Änderung im Stamm laufende nicht verändert) |
| Störmeldung | Verweis auf die erzeugte Störmeldung bei Überschreitung |
| Notiz | freiwillig, bis 1000 Zeichen |
| angelegt / geändert von, Zeitpunkte | wie bei den übrigen Vorgängen; „Service“ im Easy-Modus |
| archiviert am/von | wie bei den übrigen Vorgängen |

**Titel** zur Anzeige in Listen, Verlauf und Verknüpfungen:
„RZ-0001 · Formatwechsel · Abfüllanlage 3“.

Regeln:
- **Höchstens eine laufende Rüstzeit je Anlage** (Status `running` oder
  `overrun`; die Datenbank erzwingt es). Eine andere Rüsttaste beendet die
  laufende in diesem Moment und beginnt die neue im selben Moment.
- Ende nach Beginn; Beginn nicht in der Zukunft (5 Minuten Spielraum).
- Gilt nur für **diese Anlage** (Entscheidung des Entwurfs).

### 6.5 Überschreitung → Störmeldung

- Ein Hintergrunddienst (nur am aktiven Server, ruht in der Wartungssperre,
  etwa jede Minute) prüft laufende Rüstzeiten mit Höchstdauer. Ist sie
  überschritten:
  - Status wird **`overrun`** „Überschritten“; die Rüstzeit **läuft weiter**,
    bis jemand „Fertig“ drückt oder sie im PDH beendet.
  - Es entsteht **genau eine Störmeldung** an derselben Anlage – über den
    normalen Dienst für Störungen, also mit den üblichen Benachrichtigungen
    an die Instandhaltung:
    - Titel „Rüstzeit überschritten: Formatwechsel“,
    - Beschreibung: Beginn, Höchstdauer, wer gestartet hat, Verweis auf die
      Rüstzeit,
    - Schweregrad **hoch**, **„Anlage steht“** gesetzt (Push-Alarm an die
      Bereitschaft wie bei anderen Stillständen),
    - Ersteller „PDH-System“, Verlauf „aus Rüstzeit RZ-0001 (Höchstdauer
      60 min überschritten)“.
  - Rüstzeit und Störmeldung sind gegenseitig verknüpft und im Verlauf
    vermerkt.
- „Fertig“ nach der Überschreitung beendet die Rüstzeit (`done`); die
  Störmeldung bleibt und wird von der Instandhaltung wie jede andere
  bearbeitet.
- Entscheidung des Entwurfs: Die Zeit nach der Höchstdauer bleibt Teil der
  Rüstzeit; dass sie zu lang war, zeigt der Status und die verknüpfte
  Störmeldung. Die spätere Analyse kann den Teil über der Höchstdauer gesondert
  werten.

### 6.6 An der Anlage (Easy-Modus und Anlagenseite)

- Hat eine Anlage mindestens eine aktive Rüsttaste, zeigt der Easy-Modus
  unter „Störung +“ und „Ticket +“ einen Abschnitt **„Rüsten“** mit je einer
  Taste. Ohne Rüsttaste ändert sich nichts.

  ```text
   Abfüllanlage 3
   ┌──────────────┐ ┌──────────────┐
   │  Störung +   │ │   Ticket +   │
   └──────────────┘ └──────────────┘
   Rüsten
   ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
   │ Formatwechsel│ │ Düsen tausch.│ │  Reinigung   │
   └──────────────┘ └──────────────┘ └──────────────┘
  ```

- **Ein Tipp startet** sofort (Herkunft `easy`, Ersteller „Service“).
- Läuft eine, steht oben **„Formatwechsel läuft seit 10:42“** mit **„Fertig“**
  und einem freiwilligen Notizfeld; bei Überschreitung zusätzlich
  „Höchstdauer überschritten – Instandhaltung ist informiert“.
- Missbrauchsschutz (Entscheidung des Entwurfs): eigene Grenze je Anlage,
  nicht je Adresse, unabhängig von den Meldungen – höchstens 30 Rüst-Vorgänge
  in 10 Minuten; doppeltes Antippen derselben Taste innerhalb 10 Sekunden
  startet nicht neu.
- **Angemeldete** landen beim QR-Code auf der Seite der Anlage; dort derselbe
  Abschnitt für jeden, der die Anlage sehen darf (Entscheidung des Entwurfs);
  Ersteller ist die Person. Ist der Easy-Modus abgeschaltet, gibt es Rüsten an
  der Anlage nur dort.

### 6.7 Im PDH wie Tickets – Integration in die Vorgänge

**Arbeitsbereich „Rüstzeiten“** (Navigation in der Gruppe der Vorgänge) aus
derselben Beschreibung wie Tickets (`workKinds`), mit den Reitern:
- **Vorgänge:** laufende und überschrittene oben, Filter nach Anlage (mit
  Unteranlagen), Grund, Status, Zeitraum; Spalten Nummer, Anlage, Grund,
  Taste, Beginn, Ende, Dauer, Status; Summe der Dauer.
- **Board:** Spalten „Läuft“, „Überschritten“ (Status-Ablauf der Rüstzeit).
- **Regeln:** kurze Regeln zum Rüsten (Fertig drücken, Grund richtig wählen,
  Überschreitung heißt Instandhaltung prüft).
- **Archiv:** beendete Rüstzeiten.
- **Anlegen** wie bei Tickets: Anlage, Grund, Beginn, Ende oder Dauer, Notiz;
  ohne Ende laufend.
- **Detailseite** wie bei Tickets: Kopf mit Status, Bearbeiten (Beginn, Ende,
  Grund, Notiz), „Beenden“, und die gemeinsamen Bausteine **Feldsätze,
  Kategorien, Verknüpfungen, Verlauf, Anhänge/Fotos**.

**Anpassung des gemeinsamen Arbeitsbereichs (`workKinds`)** – das ist die
Anpassung der vorhandenen Vorgänge:
- Priorität, Fälligkeit und Zuweisung über die Broker werden **optional**.
  Eine Art ohne sie zeigt keine Prioritätsspalte, keinen Fälligkeitsfilter,
  keine Broker-Spalte und keine Zuweisungsaktionen.
- Der Abschluss-Text der Art (`DoLabel`) führt bei der Rüstzeit zu „Beenden“
  statt zum Abschluss-Assistenten.
- Tickets, Störungen, Aufgaben und Wartung verhalten sich **unverändert**
  (Prüfung: bestehende Tests und Seiten gleich).

**Eintrag in die gemeinsamen Verzeichnisse der Vorgänge** (nach Vorbild KVP):
Liste der Vorgangsarten (Titel, Pfad, Symbol), Feldsätze/Kategorien/
Verknüpfungen/Verlauf, Abteilung über die Anlage, Sichtbarkeit nach
Abteilung (beteiligt = Ersteller), Bereichsadministrator, Rechte-Tor für
Anlegen/Bearbeiten/Löschen, Ablageordner im Dokumentenspeicher, Verweise im
Chat, Symbole.

**An der Anlage:**
- Historie der Anlage (Reiter Verlauf) mit eigenem Filter „Rüstzeiten“
  (Beginn → Zeitpunkt, Ende → abgeschlossen).
- „Offene Vorgänge an der Anlage“ zeigt die laufende Rüstzeit.
- Reiter **„Rüsttasten“** (Zuordnung) und Stammfeld **Höchstdauer** (6.3).
- Die Anlagenüberwachung zeigt ein Kennzeichen „rüstet“, solange eine
  Rüstzeit läuft (Entscheidung des Entwurfs).

**Dashboard:** Schnellaktion „Rüstzeit erfassen“.

**Bewusst nicht eingebunden** (setzen Zuständige, Priorität oder Fälligkeit
voraus): „Mir zugewiesen“, Zuweisung über die Broker, Leitstand, Kalender,
Abschluss-Assistent, Ersatzteile, Push-Annahme, Zeiterfassung, Abgleich.

### 6.8 Rechte

Kategorie „Infrastruktur & Anlagen“.

| Schlüssel | Erlaubt |
|---|---|
| `setup.view` | Rüstzeiten sehen |
| `setup.edit` | anlegen, bearbeiten, beenden; Gründe und Rüsttasten pflegen und zuordnen |
| `setup.admin` | Bereichsadministrator; löscht als Einziger Rüstzeiten, Gründe und Tasten |

Die Höchstdauer an der Anlage pflegt, wer die Anlage bearbeiten darf
(`infrastructure.edit`). Migration: Rollen mit `infrastructure.view` bekommen
`setup.view`; mit `infrastructure.edit` zusätzlich `setup.edit`; mit
Administratorrechten `setup.admin`. Abteilung einer Rüstzeit ist die ihrer
Anlage. Rüsten an der Anlage (6.6) braucht kein Recht.

### 6.9 Verbund

Der Dienst aus 6.5 läuft nur am aktiven Server. Alle Daten liegen in der
PDH-Datenbank und sind nach einem Rollentausch vorhanden; eine Überschreitung
erzeugt auch nach einem Tausch nur eine Störmeldung (Vermerk am Vorgang).

## 7. Fehlerbilder

| Lage | Verhalten |
|---|---|
| Höchstdauer überschritten | Status „Überschritten“, genau eine Störmeldung an die Instandhaltung, Rüstzeit läuft weiter |
| „Fertig“ vergessen, Anlage mit Höchstdauer | wie oben |
| „Fertig“ vergessen, Anlage ohne Höchstdauer | läuft weiter, in der Liste ab 24 h hervorgehoben |
| Zwei Tasten kurz nacheinander | nahtloser Wechsel |
| Gleichzeitige Tipps an derselben Anlage | genau eine Rüstzeit läuft |
| Rollentausch während einer Überschreitung | keine zweite Störmeldung |
| Höchstdauer im Stamm geändert | gilt für neue Rüstzeiten; laufende behalten ihren Wert |
| Rüsttaste umbenannt oder gelöscht | Rüstzeiten behalten Namen und Grund |
| Anlage ohne Rüsttaste | Easy-Modus und Anlagenseite unverändert |

## 8. Prüfung

- Tests: Arbeitsbereich mit und ohne Priorität/Fälligkeit/Broker (Tickets,
  Störungen, Aufgaben, Wartung unverändert), eine laufende je Anlage auch
  gleichzeitig, nahtloser Wechsel, doppeltes Antippen, Missbrauchsgrenze
  getrennt von Meldungen, Vererbung von Rüsttasten und Höchstdauer,
  Überschreitung → genau eine Störmeldung mit „Anlage steht“ und Verknüpfung
  (auch bei zwei Durchläufen und nach Rollentausch), keine Benachrichtigung
  innerhalb der Höchstdauer, Rechte mit Abteilungsebene, Feldsätze/Verlauf/
  Verknüpfungen an der Rüstzeit, Historie der Anlage, Easy-Modus unverändert
  ohne Rüsttasten.
- Oberfläche in Fiori und Fluent, hell und dunkel, 390 und 1366 px.

## 9. Handbuch und Version

- Neues Kapitel „Rüstzeiten“ (So geht's: Rüsttasten anlegen und zuordnen,
  Höchstdauer an der Anlage, an der Anlage rüsten, im PDH erfassen und
  korrigieren, was bei Überschreitung passiert). Ergänzen: Easy-Modus,
  Anlage, Störungen (Störmeldung aus Rüstzeit).
- Änderungsprotokoll: nächste Korrekturversion über `main` (erwartet
  0.80.3), „Neu“ und „Sicherheit“ (neue Rechte).

## 10. Dem Bauenden überlassen

- Tabellen- und Spaltennamen, Migrationsnummer.
- Wie `workKinds` die optionalen Teile ausdrückt, solange die übrigen Arten
  unverändert bleiben.
- Gestaltung nach vorhandenen Mustern (Tickets, KVP, Meldetexte).
- Genauer Takt des Dienstes aus 6.5.
