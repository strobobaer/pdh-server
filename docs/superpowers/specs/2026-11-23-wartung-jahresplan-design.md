# Wartung: Jahresplan im Kalender, Auftrag mit Vorlauf – Entwurf

Stand: 23.11.2026 · Branch `feature/wartung-jahresplan` (ab `main` 0.82.45)

## Worum es geht

Der Anwender will die Wartungen **zur Personalplanung** ein Jahr im Voraus im
Kalender sehen und einzeln umplanen: einen Termin auf einen anderen Tag legen
oder einer Person zuweisen. Ein echter Durchführungsauftrag soll aber nur für
den **nächsten** Termin entstehen, und zwar automatisch **5 Arbeitstage vorher**
(einstellbar unter „Wartungsregeln“).

Heute legt das PDH je aktivem Plan genau einen offenen Auftrag an – sofort nach
dem Erledigen des vorigen (`internal/modules/maintenance/schedule.go`,
`ScheduleNext`, `ensureTaskForPlan`, stündlich `EnsurePlanTasks`). Der Kalender
(`internal/web/calendar_board.go`, Ebene „Wartungen“) zeigt nur diese
vorhandenen Aufträge. Künftige Termine rechnet nur der Jahresplan der Wartung
und die Vorschau eines Plans aus (`PlannedDates`), gespeichert werden sie nicht.

## Entscheidungen des Anwenders (23.11.2026)

- Zweck: Personalplanung. „Es reicht, wenn die geplanten Termine (Auswahl Knopf
  „Jahresplan erzeugen“) stehen, aber nur der nächste als Durchführungsauftrag
  mit einem Vorlauf von einer Woche, also 5 AT (einstellbar unter
  Wartungsregeln), automatisch erzeugt.“
- Geplante Termine lassen sich einzeln verschieben und besetzen („Ja“).
- Die Einzelheiten unten (Jahresplan erzeugen, Kalender, Auftrag automatisch,
  Wartungsregeln, Verhalten bei Änderungen, Outlook) hat der Anwender mit
  „Passt“ bestätigt.
- Bauweg A (eigene Tabelle für geplante Termine) – „A“.
- Nach der Prüfung des Entwurfs („1a 2a 3a 4a“):
  1. **Rhythmus bleibt am gerechneten Tag.** Ein verschobener Termin ändert
     nur sich selbst; die folgenden Termine und `next_due_at` rechnen vom
     gerechneten Tag weiter (Modus „fest“). Im Modus „ab Durchführung“ zählt
     wie bisher der tatsächliche Abschluss.
  2. **Verspätet erledigt, feste Termine dazwischen vorbei:** Die vergangenen
     geplanten Termine werden sichtbar als **„entfallen“** markiert; weiter
     geht es mit dem nächsten künftigen Termin (kein Rückstau, wie heute).
  3. **Planer-Reiter „Wartung“, Planungsfolie des Infobildschirms und
     Gantt-Diagramm im Leitstand** zeigen die geplanten Termine mit,
     gekennzeichnet als „geplant“. Im Planer gelten für sie dieselben
     Warnungen (Abwesenheit, keine Schicht) sowie Verschieben und Zuweisen.
  4. **Nachricht beim Entstehen des Auftrags:** Die zugewiesene Person bzw.
     Gruppe bekommt dieselbe Nachricht wie bei einer Zuweisung von Hand.

## Wege, die zur Wahl standen

| Weg | Idee | Dafür | Dagegen |
|---|---|---|---|
| **A (gewählt)** | Eigene Tabelle „geplante Termine“; ein Hintergrundlauf macht 5 Arbeitstage vorher den Auftrag daraus | Aufträge, Kennzahlen, Leitstand, App und „ein offener Auftrag je Plan“ bleiben unberührt; klare Trennung geplant/Auftrag | Kalender und Outlook brauchen eine eigene Ebene |
| B | Geplante Termine als Aufträge mit neuem Status „geplant“ | Kalender, Verschieben, Outlook vorhanden | jede Stelle, die offene Aufträge zählt (Startseite, Leitstand, App, Kennzahlen, Erinnerungen), müsste den Status kennen |
| C | Nur Abweichungen speichern, Rest rechnen | wenig Daten | „Jahresplan erzeugen“ hätte nichts zu tun; Zusammenführen fehleranfällig |

## Ziele

1. Knopf **„Jahresplan erzeugen“** am Wartungsplan und als Sammelknopf „für
   alle aktiven Pläne“: legt die geplanten Termine der **nächsten 12 Monate**
   ab heute an. Einen Monat vor Ende des Zeitraums verlängert das PDH von
   selbst um weitere Termine (nur für Pläne, deren Jahresplan erzeugt wurde).
2. Kalender: neue Ebene **„Wartungstermine“** mit den geplanten Terminen,
   gestrichelt; die echten Aufträge stehen wie bisher unter „Wartungen“.
3. Ein geplanter Termin lässt sich im Kalender auf einen anderen Tag **ziehen**
   und einer **Person oder Gruppe zuweisen**; vorbelegt ist die Person bzw.
   Gruppe aus dem Plan.
4. **Auftrag automatisch 5 Arbeitstage vor dem Termin**: aus dem nächsten
   geplanten Termin wird der Durchführungsauftrag; er übernimmt Tag und
   Person/Gruppe des geplanten Termins. Weiter höchstens ein offener Auftrag
   je Plan.
5. Bereich **„Wartungsregeln“** in der Wartung mit der Vorgabe „Vorlauf
   5 Arbeitstage“; ein Plan kann einen eigenen Vorlauf haben.
6. Geplante Termine, die einer Person zugewiesen sind, gehen in ihren
   Outlook-Kalender, wenn sie dort „Wartungen“ eingeschaltet hat.

## Nicht-Ziele

- Kapazitätsplanung (Stunden je Person und Woche, Überlast-Warnung).
- Geplante Termine in der App und am Anlagenterminal (die Planungsfolie des
  Infobildschirms zeigt sie dagegen, siehe Entscheidung 3).
- Zeitraum über 12 Monate hinaus wählbar.
- Mehrere offene Aufträge je Plan.

## Was auf keinen Fall passieren darf

- Ein geplanter Termin zählt irgendwo als offener Auftrag (Startseite,
  Leitstand, Kennzahlen, Erinnerungen, App, Übernahme-Liste „Wartungen“).
- Zwei offene Aufträge für denselben Plan; ein geplanter Termin wird zweimal
  zum Auftrag.
- Eine von Hand verschobene oder besetzte Planung wird still überschrieben.
- Eine fällige Wartung geht verloren, weil kein Auftrag entsteht (z. B. Termin
  in der Vergangenheit, Lauf in der Wartungssperre ausgefallen).

## Wartungsregeln und Vorlauf

- Neue Einstellung **„Vorlauf für den Durchführungsauftrag“ in Arbeitstagen**,
  Vorgabe **5**, erlaubt 0 bis 60, in `app_settings`.
- Ort: Wartung → Bereich „Wartungsregeln“. Ändern darf nur der
  Bereichsadministrator `maintenance.admin` (mein Vorschlag; es betrifft alle
  Pläne). Ansehen alle mit `maintenance.view`.
- **Arbeitstage**: Montag bis Freitag ohne Feiertage (Bundesland aus
  `planner_holiday_state`) und ohne Betriebsruhe (`planner_closures`) – wie der
  Planer (`internal/web/planner.go`, `freeDays`). Die Rechnung muss im
  Wartungsmodul nutzbar sein; es bleibt bei einer Feiertagsliste.
- **Je Plan**: Feld „Vorlauf (Arbeitstage)“, leer = Vorgabe aus den
  Wartungsregeln. Das bisherige Feld „Vorlauf“ (`lead_days`, Kalendertage,
  steuert heute nur „anstehend“) wird darauf umgestellt:
  - neue Spalte, NULL = Vorgabe;
  - Migration: bisher 0 → NULL (Vorgabe); bisher > 0 → aufgerundet
    `ceil(lead_days × 5 / 7)` Arbeitstage, höchstens 60;
  - die Stellen, die heute mit `lead_days` „anstehend“ rechnen
    (`internal/web/handler.go` `maintUpcoming`, `global_dashboard.go`,
    `assignment_new.go` Übernahme-Liste „Wartungen“, `work_board.go` Chip
    „Geplant“, `repo_tasks.go` liest nur, Testdaten
    `internal/web/testdata/work_base_maintenance.sql`), rechnen mit dem neuen
    Vorlauf. Weil zwei davon in SQL rechnen und Feiertage dort nicht bekannt
    sind, speichert der Auftrag beim Anlegen den Tag, ab dem er „anstehend“
    ist (mein Vorschlag; für alte Aufträge aus `due_date − lead_days`);
  - `lead_days` wird in der Oberfläche nicht mehr angeboten (beim Bauen
    prüfen, ob es danach noch Leser gibt).
- Der **Auftrag entsteht am Tag**, der so viele Arbeitstage vor dem Termin
  liegt (Vorlauf 0 = am Termin selbst). Liegt der Termin auf einem freien Tag,
  zählt rückwärts ab dem letzten Arbeitstag davor. Ist der Tag schon vorbei
  (Termin kurzfristig angelegt, Lauf ausgefallen), entsteht der Auftrag
  sofort.

## Wie Aufträge entstehen (geändert)

- **Gilt für alle aktiven Pläne**, auch ohne erzeugten Jahresplan (meine
  Auslegung von „nur der nächste … automatisch erzeugt“): Nach dem Erledigen
  oder Überspringen setzt `ScheduleNext` wie bisher `next_due_at`, legt den
  Folgeauftrag aber **nicht mehr sofort** an. Der Auftrag entsteht erst, wenn
  der Vorlauf erreicht ist.
  - Ohne Jahresplan: Termin = `next_due_at`, Person/Gruppe aus dem Plan.
  - Mit Jahresplan: Termin, Person und Gruppe aus dem nächsten noch nicht
    beauftragten geplanten Termin.
- Der Hintergrundlauf (heute stündlich `EnsurePlanTasks`) prüft je Plan ohne
  offenen Auftrag, ob der Vorlauf erreicht ist, und legt dann den Auftrag an.
  Sperre je Plan wie bisher (`pg_advisory_xact_lock`), Wartungssperre wie
  bisher (`MaintLocked`). Zusätzlich nach jedem Abschluss sofort prüfen.
- Beim **Anlegen, Ändern, Duplizieren und Wiedereinschalten** eines Plans
  entsteht der Auftrag nur noch, wenn der Vorlauf erreicht ist (heute sofort:
  `service.go`, `repo_plans.go` `SetPlanActive`). Dasselbe gilt für alle
  anderen Wege über `ensureTaskForPlan`: Knopf und API „Aufträge anlegen“
  (`POST /maintenance/generate`, `api.go` `/tasks/generate`,
  `Service.EnsurePlanTask`).
- **Ein Auftrag von Hand mit Plan** (`CreateTask` mit `plan_id`) zählt wie
  heute als offener Auftrag des Plans; er wird mit dem nächsten noch nicht
  beauftragten geplanten Termin verknüpft, wenn dessen Tag gleich ist,
  sonst mit keinem (mein Vorschlag).
- **Planänderung und offener Auftrag:** `UpdatePlan` überträgt heute
  Fälligkeit, Person und Gruppe auf den offenen, noch nicht begonnenen
  Auftrag (`repo_plans.go:227-231`). Stammt der Auftrag aus einem von Hand
  verschobenen oder besetzten geplanten Termin, übernimmt er nur, was nicht
  von Hand geändert wurde.
- **Rhythmus (Entscheidung 1):** Der geplante Termin speichert den gerechneten
  Tag zusätzlich zum (ggf. verschobenen) Tag; der Auftrag ebenso. Im Modus
  „fest“ rechnet `ScheduleNext` vom gerechneten Tag weiter, nicht vom
  verschobenen. `next_due_at` ist der gerechnete Tag des nächsten Termins.
- **Verspätet erledigt (Entscheidung 2):** Vergangene, noch nicht beauftragte
  geplante Termine eines Plans im Modus „fest“ bekommen beim Abschluss den
  Zustand **„entfallen“** (bleiben sichtbar, werden nicht beauftragt). Das
  gilt auch für Pläne mit kurzem Rhythmus, deren Termine während eines
  offenen Auftrags vorbeigehen. Ausnahme zum Abschnitt „Geplante Termine“:
  „sofort beauftragt“ gilt nur für den nächsten Termin, wenn kein offener
  Auftrag besteht und sein Vorlauftag verpasst wurde.
- **Nachricht (Entscheidung 4):** Entsteht der Auftrag automatisch, bekommt
  die zugewiesene Person bzw. Gruppe dieselbe Nachricht wie bei einer
  Zuweisung von Hand (vorhandenen Weg nutzen, nichts Neues erfinden).
- **Nach dem Abschluss** zeigen die Detailseite des erledigten Auftrags
  (heute Link „Nächster Auftrag“, `maintenance_web.go:636-640`), die Meldung
  „Nächster Termin“ (`completion.go:414-417`) und die Mail zum Abschluss
  (`maintenance_protocol.go:128`, `NextDueForTask`) den nächsten **geplanten**
  Termin (ggf. verschoben) mit Hinweis „Auftrag entsteht am …“, solange es
  noch keinen Auftrag gibt.
- Der geplante Termin wird beim Beauftragen mit dem Auftrag **verknüpft** und
  verschwindet aus der Ebene „Wartungstermine“ (der Auftrag steht unter
  „Wartungen“).
- Bestehende offene Aufträge bleiben beim Einspielen, wie sie sind.
- Der Knopf „Fehlende Aufträge anlegen“ folgt derselben Regel; Tooltip
  anpassen.
- Pläne mit sehr kurzem Rhythmus (täglich, Vorlauf ≥ Abstand): Es bleibt bei
  einem offenen Auftrag; der nächste entsteht nach dem Erledigen, sobald sein
  Vorlauf erreicht ist.

## Geplante Termine

- **Jahresplan erzeugen** (je Plan und Sammelknopf): rechnet mit der
  vorhandenen Termin-Rechnung (`PlannedDates`, `AddInterval`, `nextDueFor`) die
  Termine ab `next_due_at` bis heute + 12 Monate und legt die fehlenden als
  geplante Termine an. Nichts doppelt. Der Plan merkt sich, dass sein
  Jahresplan erzeugt wurde.
- **Verlängern**: Ein Lauf (täglich genügt) ergänzt bei diesen Plänen die
  Termine, sobald der letzte geplante Termin weniger als einen Monat vor
  „heute + 12 Monate“ liegt.
- Ein geplanter Termin hat: Plan, Datum, Person, Gruppe, „von Hand geändert“,
  angelegt/geändert von wem und wann, Verknüpfung zum Auftrag (nach dem
  Beauftragen). Kennungen mit `pdh_uuid_v7()`.
- **Neu rechnen**: Ändert sich der Rhythmus eines Plans (Einheit, Anzahl,
  Modus, Fälligkeit) oder wird eine Wartung „ab Durchführung“ erledigt (der
  nächste Termin verschiebt sich), rechnet das PDH die **nicht von Hand
  geänderten**, noch nicht beauftragten Termine neu. Von Hand verschobene oder
  besetzte bleiben stehen; liegen sie nicht mehr auf einem Termin der neuen
  Rechnung, zeigen sie den Hinweis **„weicht vom Plan ab“**.
- **Plan abschalten oder löschen** entfernt seine noch nicht beauftragten
  geplanten Termine. Wieder einschalten: Jahresplan neu erzeugen.
- Ändert sich nur die Person/Gruppe am Plan: nicht von Hand geänderte geplante
  Termine übernehmen die neue Besetzung.
- Ein geplanter Termin in der Vergangenheit, der nicht beauftragt wurde, wird
  beim nächsten Lauf sofort beauftragt, nicht still verworfen.

## Kalender

- Neue Ebene **„Wartungstermine“** (eigener Schlüssel in `calKinds`, Farbe wie
  „Wartungen“), Einträge **gestrichelt**, Text „geplant“; mit Hinweis „weicht
  vom Plan ab“, wo zutreffend. Ein- und ausschaltbar wie die anderen Ebenen.
- Sichtbar mit `maintenance.view`, gefiltert nach Abteilung wie die Ebene
  „Wartungen“ (Anlage des Plans). Personenzeilen: ein besetzter geplanter
  Termin erscheint bei der Person bzw. Gruppe.
- **Ziehen** auf einen anderen Tag verschiebt den geplanten Termin (setzt „von
  Hand geändert“). Recht `maintenance.edit` an der Anlage des Plans
  (`hasPermRec`), nicht am Spiegel, nicht in der Wartung. Ziehen in die
  Vergangenheit lehnt das PDH ab. Anders als bei Aufträgen
  (`calendar_board.go:654`, `recordInvolved`) dürfen Beteiligte ohne Recht
  geplante Termine **nicht** verschieben (mein Vorschlag: es ist Planung,
  keine eigene Arbeit). Entsteht der Auftrag an einem verschobenen Tag
  innerhalb des Vorlaufs, gilt der Vorlauf vom neuen Tag aus.
- Personenzeilen: Ein geplanter Termin ohne Person erscheint bei der
  verantwortlichen Person des Plans, wie ein Auftrag (`calPeople`).
- **Besetzen**: Klick auf den Termin öffnet ein kleines Fenster mit Person und
  Gruppe (gleiche Auswahl wie beim Auftrag), „Speichern“ und „Zurück zum
  Plan“ (Datum und Besetzung wieder aus der Rechnung, „von Hand geändert“
  aus). Gleiches Recht.
- Der Leitstand-Kalenderbaustein (`global_dashboard.go`) zeigt die Ebene mit;
  für Gäste nur, wenn auch „Wartungen“ für Gäste sichtbar sind (Bestand
  prüfen).
- Widget „Mein Kalender“: zeigt eigene geplante Termine mit („geplant“).

## Wartungsseite

- Am Plan: Knopf **„Jahresplan erzeugen“** (bzw. „Jahresplan aktualisieren“,
  wenn schon erzeugt), Zahl der geplanten Termine, Feld „Vorlauf
  (Arbeitstage)“ mit Hinweis auf die Vorgabe.
- Übersicht: Sammelknopf **„Jahresplan für alle aktiven Pläne erzeugen“**
  (`maintenance.edit`; je Plan zählt die Abteilung der Anlage – Pläne anderer
  Abteilungen werden übersprungen und gezählt).
- Der vorhandene Jahresplan der Wartung (✓ erledigt · ● offen · ○ geplant)
  zeigt für Pläne mit erzeugtem Jahresplan die gespeicherten Termine
  (verschobene am neuen Tag) statt der reinen Rechnung.

## Planer, Infobildschirm, Leitstand (Entscheidung 3)

- **Planer-Reiter „Wartung“** (`internal/web/planner_maint.go`): zeigt neben
  den Aufträgen die geplanten Termine (gestrichelt, „geplant“) mit denselben
  Warnungen (Abwesenheit, keine Schicht). Verschieben und Zuweisen dort
  ändern den geplanten Termin wie im Kalender, mit denselben Rechten.
- **Planungsfolie des Infobildschirms** (`terminal_plan.go`, `planMaintRows`)
  und **Gantt-Diagramm im Leitstand** (`global_dashboard.go`, `GanttItems`):
  zeigen geplante Termine als „geplant“ mit; nur ansehen.

## Outlook

- Die vorhandene Übertragung (`internal/web/microsoft_calendar.go`, Einstellung
  „Wartungen“) nimmt geplante Termine mit, die der Person zugewiesen sind, als
  Termin „PDH · Wartung (geplant) · …“. Wird daraus ein Auftrag, ersetzt der
  Auftrag den geplanten Eintrag (kein doppelter Termin in Outlook). Die
  Tabelle `microsoft_calendar_links` erlaubt heute nur feste Arten
  (`migrations/052_microsoft_calendar.up.sql:27`); die Migration erweitert
  die Regel. Gruppen gehen wie bisher nicht nach Outlook.

## Rechte

- Kein neues Recht (mein Vorschlag): ansehen `maintenance.view`, Jahresplan
  erzeugen, verschieben, besetzen `maintenance.edit` an der Anlage des Plans,
  Wartungsregeln ändern `maintenance.admin`. Abteilung zählt (`hasPermRec`).

## Daten

- Migration: nächste freie Nummer über `main` (heute **212**), Version
  voraussichtlich **0.82.46** – vor dem Merge prüfen.
  - Tabelle der geplanten Termine (Plan mit `ON DELETE CASCADE`, gerechneter
    Tag, Tag, Person, Gruppe, von Hand geändert, Zustand geplant · beauftragt ·
    entfallen, Auftrag `ON DELETE SET NULL`, angelegt/geändert von/am);
    höchstens ein noch nicht beauftragter Termin je Plan und gerechnetem Tag.
  - Am Auftrag: gerechneter Tag und Tag „anstehend ab“.
  - Erweiterte Arten in `microsoft_calendar_links`.
  - Spalten am Plan: Vorlauf in Arbeitstagen (NULL = Vorgabe), „Jahresplan
    erzeugt“.
  - Umrechnung des alten `lead_days` wie oben.
- `migrations/checksums.txt` mit
  `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.

## Was mitgepflegt wird

- Handbuch: Kapitel `maintenance` (Jahresplan erzeugen, Vorlauf in
  Arbeitstagen, Wartungsregeln; die Aussage „genau ein offener Auftrag, sofort
  nach Abschluss“ berichtigen), Kapitel `calendar` (Ebene „Wartungstermine“,
  verschieben, besetzen, „weicht vom Plan ab“), Outlook-Abschnitt; Skizze in
  `help_figures.gohtml`.
- `CHANGELOG.md` (`### Neu`; `### Geändert`: Folgeauftrag entsteht erst mit
  dem Vorlauf), `VERSION`, Übersetzungen, AGENTS.md (offene Punkte).
- Fiori und Fluent, hell und dunkel, 320 bis 1366 px.

## Prüfung

- Arbeitstage-Rechnung: Wochenende, Feiertag, Betriebsruhe, Vorlauf 0, Termin
  auf freiem Tag, Jahreswechsel.
- Auftrag entsteht genau am Vorlauftag, nicht vorher; nie zwei offene je Plan;
  verpasster Tag → sofort; Wartungssperre → nichts, danach nachgeholt.
- Jahresplan: 12 Monate, keine Doppelten, Verlängerung, Neurechnen nach
  Rhythmusänderung und verspätetem Abschluss („ab Durchführung“), von Hand
  geänderte bleiben mit Hinweis, Plan abschalten/löschen.
- Kein geplanter Termin in Startseite, Leitstand-Zählern, Übernahme-Liste,
  App, Kennzahlen, Erinnerungen.
- Kalender: Ebene, Ziehen, Besetzen, Rechte (fremde Abteilung → 403),
  Spiegel/Wartung.
- Rhythmus nach verschobenem Termin (Entscheidung 1), „entfallen“ nach
  verspätetem Abschluss (Entscheidung 2), Planänderung setzt einen
  verschobenen Auftrag nicht zurück, Nachricht beim automatischen Auftrag.
- Planer-Reiter, Planungsfolie und Gantt zeigen geplante Termine.
- Outlook: geplanter Termin übertragen, nach dem Beauftragen nicht doppelt.
- Browserprüfung am Testverbund.

## Dem Bauenden überlassen

- Namen von Tabelle, Spalten, Routen, Einstellungsschlüssel.
- Wo die Arbeitstage-Rechnung liegt, solange es nur eine Feiertagsliste gibt.
- Ob „Jahresplan erzeugt“ ein Zeitpunkt oder ein Schalter ist.
- Takt des Verlängerns und ob das Beauftragen im stündlichen Lauf oder in
  einem eigenen läuft.
- Aufbau des Besetzen-Fensters im Kalender.
