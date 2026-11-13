# Abgleich mit Fremdsystemen (Paket 1) – Entwurf

Stand: 2026-11-13 · Zweig `feature/abgleich-fremdsysteme` · Version 0.80.2
(nächste Korrekturversion über `main`; beim Merge neu prüfen)

## 1. Ziel

Ein externes System mit eigener Datenbank wird in beide Richtungen an das PDH
angebunden:

- Kommt dort eine neue Meldung an, entsteht im PDH ein Ticket oder eine
  Störmeldung.
- Ändert sich der Datensatz im PDH, geht die Änderung in das externe System
  zurück.

Paket 1 baut den Abgleich mit festen Regeln und einem Cockpit, in dem die
Zuordnungen von Hand gepflegt und getestet werden. Die KI-gestützte
Einrichtung (Paket 2) füllt später dasselbe Cockpit aus.

**Erfolg:**

- Eine neue Zeile in der Meldungstabelle eines SQL Servers erscheint binnen
  des eingestellten Takts als Ticket oder Störmeldung im PDH, genau einmal.
- Eine Änderung an einem zugeordneten Feld im PDH steht binnen Sekunden in der
  fremden Zeile.
- Ein Kommentar im PDH steht als neue Zeile in der Nebentabelle des
  Fremdsystems.
- Der Abgleich läuft direkt und über einen ausgehenden VPN-Tunnel.
- In der fremden Datenbank wird nie gelöscht und nie eine Meldung angelegt.

## 2. Entscheidungen des Anwenders (13.11.2026)

- Grundansatz: fester Abgleich nach bestätigten Regeln; KI nur als
  Einrichtungshelfer (Paket 2), nicht im laufenden Betrieb.
- Aus einer fremden Meldung entsteht ein Ticket oder eine Störmeldung, je
  Profil wählbar.
- Zurück geht jede Änderung an Feldern, die für den Export zugeordnet sind.
- Spätere Änderungen im Fremdsystem übernimmt das PDH. Ändern beide Seiten
  dasselbe Feld, gewinnt das PDH; der Fremdwert steht im Verlauf.
- Das PDH ändert nur vorhandene Zeilen und hängt Zeilen in Nebentabellen an.
  **Löschungen auf keinen Fall.** Keine neuen Meldungen im Fremdsystem.
- Takt: hereinkommend einstellbar je Profil, hinausgehend binnen Sekunden.
- Test: Trockenlauf, danach ein echter Schreibversuch, der zurückgenommen wird.
- Datenbanken: zuerst Microsoft SQL Server; der Aufbau muss weitere
  Datenbanken und MQTT aufnehmen können (Paket 3).
- Später, eigene Entwürfe: Produktionsüberwachung (Energie, Produktionszähler,
  Abmessungen), Qualitätsmerker.

## 3. Rahmen

- **Zusatz.** Ohne Profil ändert sich am PDH nichts. Ein Fehler im Abgleich
  hält weder das PDH noch einen Rollentausch auf.
- **Nicht Teil von Paket 1:** KI-Einrichtung, MySQL, PostgreSQL, Oracle, ODBC,
  MQTT, Anlegen von Meldungen im Fremdsystem, Stammdaten,
  Produktionsüberwachung, Qualitätsmerker.

## 4. Aufbau

### 4.1 Begriffe und Daten

- **Verbindung:** vorhandener Eintrag in `import_export_connections`
  (Art `mssql`). Neu: Weg (direkt oder ausgehender VPN-Tunnel), Verschlüsselung
  zur Datenbank, „Schreiben erlaubt“.
- **Profil** (`sync_profiles`): Verbindung, Quelltabelle, Schlüsselspalte(n),
  optionale Änderungsspalte, Ziel (Ticket oder Störmeldung), Abteilung,
  Vorgabewerte, Filter, Zeitfenster, Takt, Obergrenze je Lauf (Vorgabe 200),
  Zustand (`entwurf`, `getestet`, `scharf`, `angehalten`).
- **Zuordnung** (`sync_field_maps`): fremde Spalte ↔ PDH-Feld, Richtung
  (`herein`, `hinaus`, `beides`), Werteübersetzung, fester Wert.
- **Nebentabelle** (im Profil): Tabelle für Kommentare mit Spalte für den
  Fremdschlüssel der Meldung und den zugeordneten Spalten (Text, Verfasser,
  Zeit).
- **Verknüpfung** (`sync_links`): Profil, Schlüssel der fremden Zeile,
  PDH-Datensatz (Typ, ID), zuletzt gesehener Stand der fremden Zeile
  (Änderungswert oder Prüfwert, Feldwerte der zugeordneten Spalten), zuletzt
  zurückgeschriebener Stand. Eindeutig je Profil und fremdem Schlüssel.
- **Warteliste** (`sync_outbox`): dauerhafte Liste hinausgehender Änderungen
  je Verknüpfung, in Reihenfolge, mit Versuchszähler und letztem Fehler.
- **Läufe** (`sync_runs`): je Lauf Zeit, Zahlen (neu, geändert,
  zurückgeschrieben, fehlgeschlagen), Fehlertexte.

Die vorhandene `record_change_queue` wird nach den Benachrichtigungen
gelöscht und taugt nicht als Warteliste. Die Warteliste wird eigenständig
gefüllt (im selben Schritt, in dem die Änderung an Ticket, Störmeldung oder
Kommentar entsteht), nur für verknüpfte Datensätze.

### 4.2 Hereinkommend

Im Takt des Profils, nur am aktiven Server:

1. Zeilen der Quelltabelle lesen, begrenzt durch Filter, Zeitfenster,
   Zeilen-Obergrenze und Zeitgrenze der Abfrage.
2. **Erkennen von Änderungen:** über die Änderungsspalte (Zeitstempel oder
   `rowversion`), sonst über einen Prüfwert der zugeordneten Spalten je Zeile
   gegen den Stand in `sync_links`.
3. **Neue Zeile:** Datensatz über `tickets.Service.Create` bzw.
   `faults.Service.Create` mit dem Systembenutzer anlegen, Verknüpfung
   schreiben, Herkunft im Verlauf vermerken.
4. **Geänderte Zeile:** je Feld mit Richtung `herein` oder `beides` den
   Fremdwert mit dem zuletzt gesehenen Fremdwert vergleichen. Hat sich nur die
   fremde Seite geändert, übernimmt das PDH. Hat sich auch der PDH-Wert seit
   dem letzten Stand geändert, bleibt der PDH-Wert, der Fremdwert steht im
   Verlauf, und die Warteliste bekommt den PDH-Wert zum Zurückschreiben.
5. Was das PDH selbst zurückgeschrieben hat, gilt nicht als fremde Änderung
   (Vergleich mit dem zuletzt zurückgeschriebenen Stand).
6. Übersteigt die Zahl neuer Datensätze die Obergrenze, hält das Profil an
   und meldet es. Vor dem ersten echten Lauf fragt das Cockpit mit der Zahl
   nach.

Nicht übersetzbare Werte und fehlende Pflichtfelder lassen nur die betroffene
Zeile scheitern; sie steht mit Grund im Lauf.

### 4.3 Hinausgehend

Ein Dienst am aktiven Server arbeitet die Warteliste ab, ausgelöst durch die
Änderung, spätestens nach wenigen Sekunden:

- **Ändern:** genau eine fremde Zeile über ihren Schlüssel, nur Spalten mit
  Richtung `hinaus` oder `beides`.
- **Anhängen:** eine Zeile in der Nebentabelle je neuem Kommentar.
- Je Verknüpfung streng in Reihenfolge. Scheitert ein Eintrag, bleiben er und
  alle späteren derselben Verknüpfung liegen; andere Verknüpfungen laufen
  weiter.
- Nach wiederholten Fehlern wächst die Pause (höchstens 15 Minuten), nach dem
  ersten Erfolg gilt wieder der normale Takt.
- Trifft der Befehl null Zeilen (Meldung im Fremdsystem verschwunden), wird
  das im Lauf und am Datensatz gemeldet; die Verknüpfung bleibt, nichts wird
  angelegt.

### 4.4 Schutz der fremden Datenbank

- Der Abgleich kennt genau zwei schreibende Befehlsformen: `UPDATE` einer
  Zeile über ihren Schlüssel und `INSERT` in die Nebentabelle. `DELETE`,
  `TRUNCATE`, `DROP`, `MERGE` und `INSERT` in die Meldungstabelle kann der
  Code nicht erzeugen.
- Tabellen- und Spaltennamen stammen ausschließlich aus dem gelesenen Aufbau
  der Datenbank und werden mit den Mitteln des Treibers gequotet. Werte gehen
  immer als Parameter. Kein freier SQL-Text beim Schreiben.
- Lesende Abfragen bleiben wie bisher auf `SELECT` beschränkt
  (`validateSelectSQL`).
- Steht „Schreiben erlaubt“ an der Verbindung auf nein, kann kein Profil
  hinausgehende Zuordnungen scharf schalten.
- Das Cockpit weist darauf hin, wenn der Datenbankbenutzer mehr darf als
  nötig (z. B. `db_owner` oder `sysadmin`).

### 4.5 Cockpit

Neuer Bereich „Abgleich“ bei Import/Export. Übersicht aller Profile mit
Zustand, letztem Lauf, offenen Einträgen und Fehlern. Das Cockpit eines
Profils hat fünf Schritte:

1. **Quelle:** Verbindung, Tabelle, Schlüsselspalte, Änderungsspalte; Aufbau
   der Datenbank zur Auswahl, auf Wunsch die ersten Zeilen.
2. **Ziel:** Ticket oder Störmeldung, Abteilung, Vorgabewerte, Filter,
   Zeitfenster.
3. **Zuordnungen:** Tabelle fremde Spalte – Richtung – PDH-Feld,
   Werteübersetzung je Zeile, Nebentabelle für Kommentare. Unzugeordnete
   Pflichtfelder sind markiert.
4. **Test:** siehe 4.6.
5. **Betrieb:** Takt, scharf schalten, anhalten, Läufe, Fehler, einzelne
   Einträge erneut versuchen.

Ein importierter Datensatz zeigt seine Herkunft (Fremdsystem, fremde Nummer)
und den Stand des Abgleichs.

Die Schritte 1 bis 3 sind so gebaut, dass Paket 2 sie über dieselben
Funktionen füllen kann wie die Oberfläche.

### 4.6 Test

- **Stufe 1 – Trockenlauf:** schreibt nirgends. Hereinkommend die Liste der
  Datensätze, die entstünden, mit Feldwerten und markierten Fehlern.
  Hinausgehend für eine gewählte Meldung und Probewerte der genaue Befehl mit
  Parametern.
- **Stufe 2 – Schreibversuch mit Rücknahme:** Vorher liest das Cockpit die
  Auslöser (Trigger) der betroffenen Tabellen und warnt mit Namen. Dann
  schreibt das PDH die Probeänderung in einer Transaktion, liest die Zeile zur
  Kontrolle und nimmt die Transaktion zurück. Ergebnis: bestanden oder der
  Fehler der Datenbank.
- Scharf schalten geht nur nach bestandenen Stufen ohne markierte Fehler.
  Ohne hinausgehende Zuordnungen entfällt Stufe 2.
- Jede Änderung an Quelle oder Zuordnungen setzt das Profil auf `entwurf`
  und hält es an.

### 4.7 Verbindung, VPN, Zugangsdaten

- **Weg:** `direkt` oder ein Tunnel der Art `outbound` aus `vpn_peers`. Der
  Abgleich baut keine Tunnel. Vor jedem Lauf: Tunnel steht, Adresse der
  Datenbank liegt in den Netzen des Tunnels. Sonst Lauf überspringen und
  melden; die Warteliste bleibt.
- **Prüfung der Verbindung** zeigt die Kette einzeln: Tunnel, Adresse, Port,
  Anmeldung, Leserecht, Schreibrecht.
- **Verschlüsselung zur Datenbank:** an (Vorgabe) oder aus; Zertifikat prüfen
  mit einmal bestätigtem Fingerabdruck.
- **Zugangsdaten:** Kennwörter in `import_export_connections.config` werden
  mit `encryptSecret`/`decryptSecret` gespeichert. Eine Migration bzw. ein
  Schritt beim Start verschlüsselt vorhandene Kennwörter aller Verbindungen;
  bestehende Verbindungen laufen unverändert weiter. Die Oberfläche zeigt ein
  gespeichertes Kennwort nie wieder an.
- **Schonung:** Zeitgrenze und Zeilen-Obergrenze je Abfrage, höchstens ein
  Lauf je Verbindung gleichzeitig.

**Offene Punkte für den Plan (zuerst messen):**

1. Läuft eine Datenbankverbindung aus dem PDH über einen ausgehenden Tunnel
   (`pdhoutN`) ohne Änderung am VPN-Teil? Laut Code ja, ungetestet.
2. Stehen ausgehende Tunnel nach einem Rollentausch am neuen aktiven Server?
   Wenn nicht, gehört das Mitwandern in dieses Paket.

### 4.8 Verbund

Abgleich und Warteliste arbeiten nur am aktiven Server. Verknüpfungen,
Warteliste und Läufe liegen in der PDH-Datenbank und sind damit nach einem
Rollentausch am neuen aktiven Server vorhanden. Während der Wartungssperre
ruht der Abgleich wie die vorhandenen Zeitsteuerungen.

## 5. Rechte

Neue Rechte in der Kategorie „Daten & Schnittstellen“:

| Schlüssel | Erlaubt |
|---|---|
| `sync.view` | Profile, Läufe, Fehler, Herkunft am Datensatz sehen |
| `sync.edit` | Profile anlegen, Zuordnungen ändern, Test fahren |
| `sync.run` | scharf schalten, anhalten, erneut versuchen |
| `sync.admin` | Bereichsadministrator; löscht als Einziger Profile samt Verknüpfungen |

- Migration: Rollen mit `import.write` bekommen `sync.view`, `sync.edit`,
  `sync.run`; Rollen mit Administratorrechten zusätzlich `sync.admin`.
- Ein Profil gehört einer Abteilung; Prüfung mit `hasPermRec`. Löschrouten in
  `gateRules`, `sync.admin` in `areaAdminPerm`.
- Importierte Datensätze folgen den Ticket- bzw. Störungsrechten. Wer einen
  Datensatz ändern darf, löst das Zurückschreiben aus.
- Beim Löschen eines Profils bleiben die importierten Datensätze im PDH; im
  Fremdsystem ändert sich nichts.

## 6. Fehlerbilder

| Lage | Verhalten |
|---|---|
| Fremddatenbank nicht erreichbar | Lauf übersprungen, Warteliste bleibt, Pause wächst |
| Tunnel steht nicht | wie oben, eigener Hinweis |
| Wert nicht übersetzbar, Pflichtfeld fehlt | nur diese Zeile scheitert, Grund im Lauf |
| Schreiben abgelehnt (Recht, Sperre, Pflichtfeld) | Eintrag bleibt, erneuter Versuch, Fehler am Datensatz |
| Fremde Zeile verschwunden | Meldung, Verknüpfung bleibt, nichts wird angelegt |
| Zu viele neue Meldungen | Profil hält an und meldet es |
| Beide Seiten ändern dasselbe Feld | PDH gewinnt, Fremdwert im Verlauf |
| Rollentausch mit gefüllter Warteliste | neuer aktiver Server arbeitet weiter, nichts doppelt |

## 7. Prüfung

- **Tests:** neue und geänderte Zeilen, keine Dubletten, Widerspruch,
  Reihenfolge und Nachholen der Warteliste, eigenes Zurückgeschriebenes wird
  nicht wieder importiert, Obergrenze, Rechte, Verschlüsselung vorhandener
  Kennwörter. Ein eigener Test belegt, dass kein Löschbefehl erzeugt werden
  kann.
- **Labor:** SQL Server im Docker-Container mit Beispieltabelle „Meldungen“
  und Kommentartabelle: Import, Zurückschreiben, Trockenlauf, Schreibversuch
  mit Rücknahme, auch mit Auslöser an der Tabelle.
- **Verbund:** Rollentausch mit gefüllter Warteliste.
- **Abnahme auf .14/.17:** dieselbe Beispieldatenbank, direkt und über einen
  VPN-Tunnel; Oberfläche in Fiori und Fluent, hell und dunkel, schmal und breit.

## 8. Handbuch und Version

- Neues Kapitel „Abgleich mit Fremdsystemen“ (So geht's: Verbindung,
  Datenbankbenutzer im Fremdsystem mit Mindestrechten, Profil, Zuordnen,
  Testen, Scharfschalten, Fehler beheben), Skizze des Cockpits, Eintrag in
  `helpChapterIDs`.
- Kapitel „Administration“ (neue Rechte) und VPN (Tunnel-Auswahl an
  Verbindungen) ergänzen.
- `CHANGELOG.md` 0.80.2: „Neu“ der Abgleich, „Sicherheit“ verschlüsselte
  Zugangsdaten und neue Rechte.
