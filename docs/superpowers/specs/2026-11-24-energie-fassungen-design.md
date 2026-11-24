# Energie-Messstellen: Fassungen der Formel und rückwirkendes Neurechnen – Entwurf

Stand: 24.11.2026 · Paket 2b aus
`docs/superpowers/specs/2026-11-23-berechnete-werte-design.md` (Abschnitt
„Schnitt“) und `docs/superpowers/specs/2026-11-22-copilot-zuordnen-design.md`
(Abschnitt „Gültigkeit der Formeln“) · noch nicht bestätigt

## Worum es geht

Eine berechnete Energie-Messstelle hat heute genau eine Formel. Wer sie
ändert, ändert sie ab der nächsten Minute; was vorher gerechnet wurde, bleibt.
Zwei Dinge fehlen:

1. **Planen:** „Ab 1. Januar kommt Presse 4 in die Hallensumme“ lässt sich
   nicht vorab eintragen.
2. **Berichtigen:** „Presse 4 läuft schon seit dem 3. November, die Formel
   war seitdem falsch“ lässt sich nicht nachholen.

Paket 2b gibt der Formel **Fassungen mit „gültig ab“** (wie bei den
berechneten Werten aus 0.82.45) und rechnet bei einem Datum in der
Vergangenheit die gespeicherten Minutenwerte **neu**.

## Was schon feststeht (bestätigt am 22. und 23.11.2026)

- Gültig ab einem Datum, 00:00 Uhr Ortszeit; keine Uhrzeit.
- Wo es einen Verlauf gibt (Energiedaten), rechnet das PDH ab dem Datum neu.
- Schon verschickte Exporte bleiben, wie sie waren.
- Ändern und löschen lässt sich nur eine geplante Fassung; jede Fassung
  zeigt Status (geplant · gilt · abgelöst), Formel und wer sie wann angelegt hat.

## Bestand (am Code geprüft)

- Formel steht an der Messstelle (`energy_meters.formula`), keine Fassungen.
- Der Sampler rechnet jede Minute und legt den Wert in `energy_values` ab
  (Aufbewahrung 400 Tage). Es gibt keine Tages- oder Monatswerte daneben –
  `energy_values` ist der einzige Speicher, Neurechnen ist also möglich.
- Berechnete Zähler speichern einen fortlaufenden Stand (jede Minute baut auf
  der vorigen auf). Eine Berichtigung verschiebt alle späteren Werte dieser
  Messstelle und aller Messstellen, die sie in ihrer Formel nutzen.
- Efficio-Export hängt Zeilen an Tagesdateien an und merkt sich, bis wohin er
  ist; einmal erzeugte Zeilen schreibt er nie neu.
- Schwellwert-Wächter prüft jede Minute einmal (`checked_until`); geprüfte
  Minuten prüft er nicht noch einmal.

## Vorschlag

### Fassungen

- Neue Tabelle `energy_formula_versions` (Migration 214): Messstelle,
  „gültig ab“, Formel, angelegt von/am. Jede heutige berechnete Messstelle
  bekommt eine erste Fassung mit ihrer jetzigen Formel.
- `energy_meters.formula` bleibt die **geltende** Formel – so ändern sich
  Sampler, Prüfungen und Export für den laufenden Betrieb nicht. Um
  Mitternacht (und beim Start) stellt das PDH die Formel jeder Messstelle
  auf die Fassung, die ab heute gilt.
- Regeln wie bei den berechneten Werten: es gilt das späteste „gültig ab“,
  das nicht in der Zukunft liegt; je Datum höchstens eine geplante Fassung;
  Tippfehler am selben Tag behebt eine weitere Fassung „gültig ab heute“.
- Die Prüfungen von heute (unbekannte Messstelle, Kreis, Momentanwert mit
  Zähler gemischt) gelten für jede Fassung.

### Rückwirkend neu rechnen

- Liegt „gültig ab“ in der Vergangenheit, zeigt das PDH **vor dem Speichern**,
  was das bedeutet: Zeitraum, Zahl der betroffenen Messstellen (diese und
  alle, die auf ihr aufbauen), betroffene Export-Tage. Erst nach „Neu
  rechnen“ läuft es.
- Der Lauf arbeitet im Hintergrund, Tag für Tag, in der Reihenfolge der
  Abhängigkeiten. Die Messstelle zeigt „wird neu gerechnet (… %)“. Solange
  er läuft, setzt der Sampler für die betroffenen Messstellen aus; der Lauf
  rechnet am Ende bis zur aktuellen Minute und gibt dann frei.
- Gerechnet wird mit genau den Regeln des Samplers (derselbe Rechenweg):
  Zählerbezüge als Zuwachs, fehlender Eingang = in dieser Minute kein Wert.
- Bricht der Lauf ab (Neustart, Fehler), bleibt der alte Stand vollständig
  erhalten: geschrieben wird erst am Ende eines Tages in einem Zug, und der
  Lauf setzt beim nächsten Start dort fort. Jeder Lauf steht mit Zeitraum,
  Person und Ergebnis im Verlauf der Messstelle.
- Am Spiegelserver und während einer Wartung startet kein Lauf.

### Was nicht neu gerechnet wird

- **Alarme:** Der Wächter bewertet die Vergangenheit nicht neu. Tickets
  bleiben; ab dem Ende des Laufs prüft er mit den neuen Werten weiter.
- **Efficio-Export:** siehe Entscheidung 2.

## Entscheidungen, die dem Anwender gehören

1. **Wie weit zurück?** Vorschlag: bis zum ältesten gespeicherten Wert
   (400 Tage). Alternative: höchstens bis zum Ersten des Vormonats – das
   schützt abgeschlossene Monate vor späten Änderungen.
2. **Efficio-Export bei rückwirkender Formel.** Der Entwurf vom 22.11.
   sagte: „auch für Exporte, die noch nicht verschickt sind“. Am Code zeigt
   sich: der Export schreibt erzeugte Zeilen nie neu.
   - **a (Vorschlag):** Tagesdateien, von denen noch **nichts** verschickt
     ist, werden nach dem Lauf neu erzeugt. Dateien, die ganz oder teilweise
     verschickt sind, bleiben; der Lauf listet sie als „schon verschickt,
     nicht geändert“.
   - **b:** zusätzlich eine Berichtigungsdatei für verschickte Tage – nur
     sinnvoll, wenn Efficio Berichtigungen annimmt (weiß ich nicht).
   - **c:** Export bleibt immer unberührt.
3. **Wer darf rückwirkend rechnen?** Vorschlag: geplante Fassungen mit
   „Energiedaten bearbeiten“ (`energy.edit`) wie heute die Formel; eine
   Fassung in der Vergangenheit nur der Bereichsadministrator
   (`energy.admin`), weil sie gespeicherte Werte ersetzt. Kein neues Recht.
4. **Mehr Rechenumfang für Messstellen?** Seit 0.82.45 kann der Rechenkern
   Vergleiche, Potenzen und Funktionen; die Messstellen lehnen das bisher
   ab. Vorschlag: für **Momentanwerte** (z. B. Leistung) freigeben; für
   **Zähler und Verbrauch** weiter nur + − × ÷, weil alles andere den
   Zuwachs verfälscht.
5. **Copilot:** Vorschlag: der Copilot darf eine Fassung nur vorschlagen
   (Änderungsliste, „Übernehmen“), nie eine in der Vergangenheit.

## Umfang und Version

Nächste Korrekturversion nach 0.82.49, Migration 214, kein neues Recht.
Handbuch-Kapitel „Energiedaten“ (berechnete Messstellen) und
Änderungsprotokoll werden mitgepflegt. Oberfläche: in der Messstellen-Tabelle
unter der Formel der Abschnitt „Fassungen“ wie auf der Seite „Berechnete
Werte“; geprüft in Fiori und Fluent, hell und dunkel, 320–1366 px.

## Nicht in diesem Paket

- Fassungen für andere Angaben der Messstelle (Einheit, Art, Quelle).
- Neubewertung alter Alarme.
- Tages- oder Monatsauswertungen der Energiedaten.
