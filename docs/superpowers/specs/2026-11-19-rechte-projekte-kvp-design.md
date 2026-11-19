# Ansichtsrecht für Projekte und KVP (0.82.3) – Entwurf

Stand 19.11.2026. Entscheidungen des Anwenders (19.11.2026, „3 ja, ja“):

1. Projekte und KVP bekommen je ein **eigenes Ansichtsrecht**.
2. Die Historie von **Aufgaben und Wartungsaufträgen** sieht jeder, der den
   Vorgang öffnen kann (wie die Seite selbst).

Version **0.82.3**, Migration **183**, Branch `feature/rechte-projekte-kvp`.

## Ausgangslage (0.82.2)

- Es gibt `projects.edit`, `projects.admin`, `kvp.manage`, `kvp.admin` – aber
  kein Recht zum Ansehen. Projekt- und KVP-Seiten prüfen nur die Anmeldung;
  der Menüeintrag steht bei allen.
- Projekte haben eine Abteilung über ihre Anlage bzw. ihre Gruppe und
  unterliegen der Abteilungssperre der Rolle. KVP-Vorschläge haben eine
  Abteilung nur über ihre Anlage und unterliegen keiner Abteilungssperre.
- Das Handbuch und die KVP-Seite sagen ausdrücklich „Alle sehen alle
  Vorschläge“.
- Projekt- und KVP-Daten erscheinen außerdem: in der Schnittstelle
  (`/api/v1/projects`), in Historie, Zusatzfeldern, Verknüpfungen und
  Kommentaren (`/records/…`), auf der Seite „Kategorien“, im Copilot
  (Projekte), im Kalender (Projekte, auch im Leitstand), in der Projektauswahl
  von Aufgaben, bei Partnern (Liste und Auswahl), auf der Benutzerseite
  (Zuständigkeiten), in der Anlagen-Historie, im Druck (KVP-A3) und im
  Startseiten-Baustein „KVP“.

## Ziel

Wer Projekte bzw. KVP-Vorschläge nicht sehen soll, sieht sie nirgends –
außer denen, an denen er selbst beteiligt ist. Nach dem Update sieht jeder
genau das, was er vorher sah; erst wenn ein Administrator einer Rolle das
neue Recht entzieht, ändert sich etwas.

## Die neuen Rechte

| Recht | Beschriftung | Kategorie |
|---|---|---|
| `projects.view` | Projekte: ansehen | Aufgaben & Projekte |
| `kvp.view` | KVP: alle Vorschläge ansehen | KVP |

- **Migration 183** gibt beide Rechte **jeder bestehenden Rolle** (auch
  „viewer“), ohne Abteilung – heute sieht jeder Angemeldete alles; niemand
  wird ausgesperrt.
- Ansehen darf **nur**, wer `projects.view` bzw. `kvp.view` hat (oder den
  Bereichsadministrator `projects.admin` / `kvp.admin`, der alle Rechte des
  Bereichs einschließt) – oder beteiligt ist. `projects.edit` und
  `kvp.manage` schließen das Ansehen **nicht** ein: `projects.edit` haben seit
  Migration 148 fast alle Rollen, sonst wäre das neue Recht wirkungslos.
  Bearbeiten und Steuern wirken nur an Vorgängen, die man sehen darf.
- Wie jedes Recht kann es auf eine **Abteilung** beschränkt werden. Geprüft
  wird je Datensatz mit `hasPermRec` (Projekt: Abteilung der Anlage oder der
  Gruppe; KVP: Abteilung der Anlage). Datensätze ohne Abteilung sieht, wer
  das Recht irgendwo hat (bestehende Regel).

## Beteiligte sehen immer

Ohne das Recht sieht man trotzdem jeden Vorgang, an dem man beteiligt ist:

- **Projekt:** angelegt von, verantwortlich, zugewiesen, Mitglied der
  zugewiesenen Gruppe.
- **KVP:** eingereicht von, angelegt von, Mitwirkende, Prüfer,
  verantwortlich, verantwortlich für eine Maßnahme.

„Sehen“ umfasst Detailseite, Historie, Zusatzfelder, Verknüpfungen,
Kommentare (lesen und schreiben wie bisher), Anhänge-Liste und den KVP-Druck.

## Was sich je Stelle ändert

**Projekte**

- Menüeintrag „Projekte“: nur mit Ansichtsrecht (irgendwo).
- Liste `/projects`: mit Recht alle sichtbaren (Abteilung wie bisher), ohne
  Recht nur „Meine Projekte“ (beteiligt). Über einen Link erreichbar bleibt
  sie für alle.
- Detailseite, Schnittstelle (Liste und Einzelabruf), `/records/…`
  (Historie, Zusatzfelder, Verknüpfungen, Meta), Seite „Kategorien“:
  Recht für diesen Datensatz oder beteiligt, sonst 403 (Schnittstelle) bzw.
  „Keine Berechtigung“.
- Copilot, Kalender (Seite und Leitstand mit Anmeldung), Projektauswahl im
  Aufgaben-Formular, Partner (Liste und Auswahl), Benutzerseite
  (Zuständigkeiten), Anlagen-Historie: nur Projekte, die der Benutzer sehen
  darf.
- **Bleibt:** der Projektname auf Aufgabenkarten und an der Aufgabe (er ist
  Teil der Aufgabe); Verweise in Chat-Nachrichten und Benachrichtigungen
  (sie gehen nur an Beteiligte bzw. Gesprächsteilnehmer).

**KVP**

- **Einreichen bleibt für alle** (Kachel „KVP-Idee“, Schnellaktion,
  Formular). Wer einreicht, ist beteiligt und sieht seinen Vorschlag.
- Menüeintrag „KVP“ bleibt für alle. Ohne Recht zeigt die Seite nur „Meine
  Vorschläge“ und den Knopf „Idee einreichen“ – keine Kennzahlen
  (Einsparungen, Prämien, Zählung je Person), keine Tafel aller Vorschläge.
- Detailseite, Druck (A3), Historie, Zusatzfelder, Kommentare, Seite
  „Kategorien“: Recht für diesen Vorschlag oder beteiligt.
- Startseiten-Baustein „KVP“ (Zahlen): nur mit Recht.
- Benachrichtigungen und Erinnerungen bleiben unverändert (sie gehen an
  Steuernde, Prüfer und Beteiligte).

**Aufgaben und Wartungsaufträge**

- Historie wie die Seite: Anmeldung plus Abteilungssperre (die Seiten prüfen
  kein eigenes Recht). Rücknahme der Entscheidung R8 aus 0.82.2.

## Sicherheit – in derselben Version

Beim Erkunden gefunden; beide werden mit behoben:

1. **Leitstand-Kalender ohne Anmeldung** (`/global/calendar`): Mit `set=1`
   in der Adresse kann ein Gast Vorgangsarten, Abteilung, Gruppe und Person
   selbst wählen und sieht dann Störungen, Tickets, Aufgaben und Projekte
   aller Abteilungen; ohne gültigen Leitstand fällt der Baustein auf den
   vollen Kalender zurück (am Code gelesen). **Neu:** Gäste bekommen genau
   die Einstellung des Leitstands (Arten, Abteilung, Gruppe); Angaben in der
   Adresse zählen für Gäste nicht; ohne gültigen Leitstand „nicht gefunden“.
   Angemeldete sehen dort nur, was sie auch sonst sehen dürfen.
2. **Ordnerlisten unter `/uploads/`**: Der Ordner wird mit dem
   Standard-Dateiserver ausgeliefert, der bei Ordnern eine Liste der Dateien
   zeigt – ohne Anmeldung (am Testverbund am 19.11.2026 bestätigt:
   `GET /uploads/` liefert ohne Anmeldung die Liste der Unterordner). Damit
   sind Anhänge aller Vorgänge auffindbar. **Neu:** keine Ordnerlisten mehr
   (Ordner → „nicht gefunden“); einzelne Dateien wie bisher.

**Nicht in diesem Paket (Vorschlag für später, eigene Entscheidung):**
Anhänge nur mit Anmeldung und Recht am Vorgang. Heute ist jede Datei unter
`/uploads/` ohne Anmeldung abrufbar, wenn man ihren Pfad kennt. Das
abzustellen berührt Logos auf der Anmeldeseite, Bilder auf öffentlichen
Leitständen, Terminals und Anzeige-Dokumente und braucht eine eigene Planung.
Ohne Ordnerlisten sind die Pfade praktisch nicht zu erraten.

## Handbuch und Änderungsprotokoll

- Kapitel „Aufgaben & Projekte“ und „KVP“: wer was sieht, „Meine Projekte“ /
  „Meine Vorschläge“; der Satz „Alle sehen alle Vorschläge“ entfällt (auch
  auf der KVP-Seite).
- Kapitel „Administration“: die zwei neuen Rechte, Beteiligte sehen immer;
  „bearbeiten“/„steuern“ ohne „ansehen“ wirkt nur an eigenen Vorgängen;
  Historie von Aufgaben und Wartung wie die Seite.
- CHANGELOG 0.82.3: `### Neu` (zwei Rechte), `### Geändert` (Historie
  Aufgabe/Wartung), `### Sicherheit` (Leitstand-Kalender, Ordnerlisten).

## Festlegungen (meine, bitte widersprechen, wenn anders gewünscht)

- Die neuen Rechte bekommt nach dem Update **jede** Rolle; einschränken ist
  danach Sache des Administrators.
- Beteiligte sehen ihren Vorgang immer, auch ohne Recht und außerhalb ihrer
  Abteilung (wie heute bei der Abteilungssperre).
- Ohne KVP-Recht bleibt die KVP-Seite mit „Meine Vorschläge“ erreichbar; ohne
  Projekt-Recht verschwindet der Menüeintrag „Projekte“.
- Der Projektname an Aufgaben bleibt sichtbar.

## Festlegungen nach der Gegenlese (19.11.2026, meine)

- **Nur das Ansichtsrecht** entscheidet über das Sehen (siehe oben).
- **Rollen, die später neu angelegt werden,** bekommen die Rechte nicht von
  selbst (wie jedes andere Recht); Einzelrechte von Benutzern
  (`user_permissions`) werden nicht angefasst.
- **Schnittstelle:** gilt für den Benutzer, dem die Anmeldung gehört – dieselben
  Regeln wie auf der Seite (Recht je Datensatz oder beteiligt).
- **Beteiligt** wird immer am aktuellen Stand geprüft; wer nicht mehr
  beteiligt ist, sieht den Vorgang ohne Recht nicht mehr.
- **Vollständigkeit:** Die Liste „Was sich je Stelle ändert“ stammt aus einer
  Durchsicht des ganzen Codes. Der Bauer sucht trotzdem alle Abfragen auf
  `projects` und `kvp_ideas` (und deren Unter-Tabellen) und wendet die Regel
  überall an, wo Titel oder Inhalte bei einem Benutzer ankommen – außer den
  genannten Ausnahmen (Projektname an der Aufgabe, Chat, Benachrichtigungen).
- **KVP-Zahlen an anderer Stelle** (Benutzerseite, Startseite): eigene
  Vorschläge und eigene Prämien sieht jeder; Zahlen über andere nur mit
  `kvp.view`.
- **Leitstand ohne Abteilung** zeigt Gästen weiter alle Abteilungen – aber
  nur die im Leitstand eingestellten Vorgangsarten.
- **Ordnerlisten:** Vor dem Abschalten prüft der Bauer, dass keine Seite des
  PDH Ordnerlisten unter `/uploads/` benutzt.

## Dem Bauer überlassen

- Wie die Listen nach Recht und Abteilung filtern (vorhandene Helfer für
  `hasPermRec` und `scopeAllowedIDs` nutzen oder ergänzen).
- Reihenfolge der Umsetzung und Zuschnitt der Tests; vorhandene Tests, die
  „jeder Angemeldete sieht KVP/Projekt“ festschreiben, sind anzupassen.
