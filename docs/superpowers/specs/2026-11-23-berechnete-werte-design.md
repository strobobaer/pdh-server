# Berechnete Werte (virtuelle Messwerte) – Entwurf

Stand: 23.11.2026 · Paket 2 aus
`docs/superpowers/specs/2026-11-22-copilot-zuordnen-design.md` (Ziel 6 und
Abschnitt „Gültigkeit der Formeln“) · Branch `feature/berechnete-werte`

## Worum es geht

Der Anwender will „verschiedenste Berechnungen anstellen“ und das Ergebnis
als **virtuellen Messwert** haben: ein Wert, der aus anderen Messwerten
gerechnet wird und sich danach überall wie ein echter Messwert verhält.
Beispiele: Gesamtleistung einer Halle (Summe der Pressen), Wirkungsgrad
(Verhältnis zweier Werte), „Anlage läuft“ (Leistung über einer Schwelle).

Heute geht das nur in den Energiedaten (berechnete Messstellen), nur mit
+ − × ÷ und nur für Energie. Alle anderen Stellen im PDH (Schnellübersicht,
Export-Felder, Copilot, Zuordnungs-Übersicht) kennen nur Werte, die direkt
von einer Verbindung kommen.

## Entscheidungen des Anwenders

- **Weg A** (23.11.2026, „A passt“): Der virtuelle Messwert ist eine
  Zuordnung an einer fest eingebauten Quelle „Berechnete Werte“.
- Rechenumfang, Rechenzeitpunkt, Gültigkeit und Rechte wie in der Nachricht
  mit den drei Wegen vorgeschlagen (kein Einwand): zusätzliche Funktionen,
  Rechnen bei Änderung eines Eingangswerts, leerer Wert mit Grund statt
  falscher Zahl, Fassungen mit „gültig ab“, Rechte wie bei Zuordnungen.
- Aus dem bestätigten Entwurf vom 22.11.2026: Formelgenerator am Formular,
  Vorschläge vom Copilot nur mit „Übernehmen“, ein Rechenkern für alles.

## Wege, die zur Wahl standen

| Weg | Idee | Dafür | Dagegen |
|---|---|---|---|
| **A (gewählt)** | Virtueller Messwert = Zuordnung an der eingebauten Quelle „Berechnete Werte“ | erscheint ohne Umbau überall, wo Zuordnungen genutzt werden; über eine Energie-Messstelle auch Verlauf und Efficio | ohne Messstelle nur der letzte Wert |
| B | Eigener Bereich mit eigener Tabelle und eigenem Verlauf | Verlauf von sich aus | Export, Copilot, Energiedaten müssten ihn einzeln kennenlernen; zwei Arten Messwerte |
| C | Berechnete Energie-Messstellen ausbauen | Formel und Minuten-Verlauf vorhanden | auf Energie zugeschnitten, nur Minutentakt, kein Export-Feld, nicht auf der Schnellübersicht |

## Ziele

1. Berechnete Werte anlegen, ändern und löschen – mit Anlage, Bezeichnung,
   Einheit, Nachkommastellen und Formel.
2. Formelgenerator: Eingangswerte aus einer Liste wählen, Rechenzeichen und
   Funktionen anklicken, sofortige Prüfung, Probe mit den Werten von jetzt.
3. Mehr Rechenumfang im gemeinsamen Rechenkern (siehe „Rechenkern“).
4. Der berechnete Wert ist eine Zuordnung und damit ohne weiteren Umbau
   nutzbar als Export-Feld, in der Zuordnungs-Übersicht, im Copilot und als
   Quelle einer Energie-Messstelle. Auf der Schnellübersicht der Anlage
   erscheint er nach derselben Regel wie jede andere Zuordnung (siehe
   „Offen für den Anwender“, Punkt 2).
5. Fassungen der Formel mit „gültig ab“ (heute oder später), mit Angabe, wer
   sie wann angelegt hat.
6. Der Copilot schlägt berechnete Werte und Formeln vor; angelegt wird erst
   mit „Übernehmen“.

## Nicht-Ziele (dieses Paket)

- **Gültigkeit für die berechneten Energie-Messstellen samt rückwirkendem
  Neurechnen** – eigenes Folgepaket „2b“ (siehe „Schnitt“).
- Rechnen über die Zeit: Mittel der letzten Stunde, Laufzeit zählen,
  Änderung gegenüber vorher. Braucht einen Verlauf.
- Eigener Verlauf für berechnete Werte. Wer einen Verlauf will, legt eine
  Energie-Messstelle mit dem berechneten Wert als Quelle an.
- Rückwirkendes Neurechnen berechneter Werte (es gibt nichts, woraus).
- Texte als Ergebnis, Umrechnen von Einheiten, Formeln auf der Export-Seite.

## Was auf keinen Fall passieren darf

- **Still falsche Zahlen:** Ein fehlender oder veralteter Eingangswert darf
  nicht als 0 oder mit dem alten Stand in die Rechnung gehen, ohne dass man
  es sieht. Dann lieber kein Wert, mit Grund.
- Kennungen (UUIDs) in der Oberfläche oder im Handbuch.
- Eine zweite Formelsprache neben der der Energiedaten.
- Der Copilot legt etwas an oder löscht etwas ohne „Übernehmen“.
- Kennwörter oder Verbindungsgeheimnisse gehen an den Copilot.

## Offen für den Anwender (mit der Bestätigung des Entwurfs entschieden)

1. **Schnitt 2a/2b** – Gültigkeit und rückwirkendes Neurechnen für die
   berechneten Energie-Messstellen als eigenes Folgepaket (nächster
   Abschnitt). Vorschlag: ja.
2. **Schnellübersicht der Anlage** – Sie zeigt heute je Anlage höchstens
   acht Zuordnungen, und nur solche, deren Bezeichnung oder Notiz nach
   einem Energiewert klingt (`plant_watch.go`, Wortliste `watchEnergyRe`).
   Ein berechneter Wert „Gesamtleistung“ erscheint dort, „Wirkungsgrad“
   oder „Anlage läuft“ nicht. Vorschlag: in diesem Paket so lassen (gleiche
   Regel für alle Zuordnungen); berechnete Werte dort immer zu zeigen, samt
   Einheit und Grund bei „kein Wert“, wäre eine eigene kleine Erweiterung.

## Schnitt: 2a jetzt, 2b danach (mein Vorschlag)

Der Entwurf vom 22.11.2026 nennt in Paket 2 auch „Gültigkeit für die
berechneten Messstellen der Energiedaten“ mit rückwirkendem Neurechnen. Das
greift in die Zähler-Rechnung der Energiedaten ein (Basiszeitpunkt, Zuwachs,
schon abgelegte Minutenwerte, noch nicht verschickte Efficio-Dateien) und
hat mit den virtuellen Messwerten keinen Code gemeinsam außer dem
Rechenkern. Deshalb:

- **2a (dieser Entwurf):** virtuelle Messwerte, Formelgenerator, mehr
  Rechenumfang, Fassungen mit „gültig ab“, Copilot.
- **2b (eigener kurzer Entwurf danach):** dieselben Fassungen für die
  berechneten Energie-Messstellen, rückwirkendes Neurechnen dort.

An den Energie-Messstellen ändert sich mit 2a nichts, auch nicht der
Rechenumfang (Begründung im Abschnitt „Rechenkern“).

## So sieht es aus

Eingebaute Verbindung „Berechnete Werte“ in der Liste der
Import-Verbindungen (immer vorhanden, als „eingebaut“ gekennzeichnet). Ihre
Seite hat statt Einstellungen, Explorer und Vorlagen nur die Liste und das
Formular:

```
Berechnete Werte                                    [+ Berechneter Wert]
┌──────────────────────────────────────────────────────────────────────┐
│ Gesamtleistung Halle 2      Halle 2             37,6 kW    vor 3 s   │
│   = A + B + C               gilt seit 23.11.2026 · 1 geplant         │
│ Wirkungsgrad Presse 1       Halle 2 › Presse 1  kein Wert            │
│   = RUNDEN(A / B * 100; 1)  ⚠ B „Leistung Antrieb“: seit 14:02 alt   │
└──────────────────────────────────────────────────────────────────────┘

Berechneter Wert
 Bezeichnung [Gesamtleistung Halle 2     ]   Anlage [Halle 2 ▾]
 Einheit [kW]   Nachkommastellen [1]
 Eingangswerte höchstens [10] Minuten alt (leer = Alter wird nicht geprüft)

 Eingangswerte                                  [Wert suchen …        ]
  A   Halle 2 › Presse 1 › Leistung      12,4   vor 2 s    [entfernen]
  B   Halle 2 › Presse 2 › Leistung      11,9   vor 3 s    [entfernen]
  C   Halle 2 › Presse 3 › Leistung      13,3   vor 2 s    [entfernen]

 Formel  [ A + B + C                                               ]
  [+] [−] [×] [÷] [(] [)] [<] [>] [=]   [MIN] [MAX] [MITTELWERT]
  [SUMME] [RUNDEN] [BETRAG] [WENN] …
  ✓ Formel ist in Ordnung · Probe mit den Werten von jetzt: 37,6 kW

 Gültig ab [23.11.2026]   (heute = gilt sofort)
 [Speichern]

 Fassungen
  geplant   01.12.2026  A + B + C + D   A. Muster, 23.11.2026 09:14  [Ändern] [Löschen]
  gilt      23.11.2026  A + B + C       A. Muster, 23.11.2026 09:10
  abgelöst  02.11.2026  A + B           B. Beispiel, 02.11.2026 07:30
```

- **Eingangswerte mit Kurznamen:** Wer einen Wert aus der Suche wählt, bekommt
  ihn mit dem nächsten freien Buchstaben (A, B, C …) in die Liste; ein Klick
  auf die Zeile setzt den Kurznamen an der Schreibmarke in die Formel. Der
  Kurzname lässt sich ändern (Buchstabe, dann Buchstaben, Ziffern oder `_`,
  höchstens 20 Zeichen, kein Funktionsname, je Formel eindeutig). Höchstens
  50 Eingangswerte je Formel.
- **Auswahl der Eingangswerte:** alle Zuordnungen, die auch als Export-Feld
  wählbar sind (derselbe Pool wie `availableExportSources`), dazu andere
  berechnete Werte. Die Suche findet nach Bezeichnung, Anlagenpfad und
  Verbindung und zeigt den Wert von jetzt.
- **Sofortige Prüfung** beim Tippen (kurz verzögert): Schreibfehler mit
  Stelle, unbekannter Kurzname, Kreisbezug („A → Gesamtleistung Halle 2 →
  …“ mit Bezeichnungen, nicht Kennungen), nicht benutzter Eingangswert
  (nur Hinweis).
- **Probe:** Ergebnis mit den Werten von jetzt, oder der Grund, warum es
  keinen Wert gibt. Die Probe speichert nichts.
- In Listen außerhalb der Seite (Zuordnungs-Übersicht, Export-Feld-Auswahl)
  steht bei einem berechneten Wert als Quelle „berechnet“ samt Formel in
  lesbarer Form, keine Kennung.
- Die Zuordnungs-Übersicht bekommt einen Knopf „Berechneter Wert“, der auf
  die Seite führt.

## Rechenkern

Ein Kern für Energie-Messstellen und berechnete Werte: der heutige
`internal/modules/energie/formula.go` zieht in ein gemeinsames Paket (z. B.
`internal/core/formula`), die Energiedaten nutzen ihn von dort. Keine Kopie.
Alles Bisherige bleibt gültig (`[Kennung]`, + − × ÷ und * /, Klammern,
Komma oder Punkt als Dezimalzeichen, höchstens 2000 Zeichen).

Neu:

- **Bezüge ohne Klammern:** ein Name aus Buchstabe + Buchstaben/Ziffern/`_`
  ist ein Bezug, wenn keine „(“ folgt. `[A]` und `A` sind dasselbe.
- **Vergleiche:** `<  <=  >  >=  =  <>` – Ergebnis 1 (wahr) oder 0 (falsch);
  binden schwächer als + und −.
- **Potenz:** `^` (bindet stärker als × und ÷).
- **Funktionen** (Groß-/Kleinschreibung egal, Trennzeichen zwischen den
  Angaben ist `;`, weil das Komma Dezimalzeichen ist):

  | Funktion | Bedeutung |
  |---|---|
  | `MIN(a; b; …)`, `MAX(a; b; …)` | kleinster, größter Wert |
  | `SUMME(a; b; …)`, `MITTELWERT(a; b; …)` | Summe, Mittel |
  | `BETRAG(a)` | ohne Vorzeichen |
  | `RUNDEN(a; stellen)` | kaufmännisch runden, Stellen 0 bis 6 |
  | `WURZEL(a)` | Quadratwurzel; negativ → kein Wert |
  | `WENN(bedingung; dann; sonst)` | Bedingung ≠ 0 gilt als wahr; **nur der gewählte Zweig wird gerechnet** (so lässt sich Teilen durch 0 abfangen: `WENN(B > 0; A / B; 0)`) |
  | `UND(a; b; …)`, `ODER(a; b; …)`, `NICHT(a)` | Ergebnis 1 oder 0 |

  Englische Namen gelten als gleichwertig (`IF`, `SUM`, `AVG`/`AVERAGE`,
  `ABS`, `ROUND`, `SQRT`, `AND`, `OR`, `NOT`) – für Excel-Gewohnte und den
  Copilot. Angezeigt und vom Generator eingesetzt werden die deutschen.
- **Kein Wert** wie bisher, wenn ein Bezug keinen Wert hat, durch 0 geteilt
  wird oder das Ergebnis keine endliche Zahl ist. Der Kern nennt dabei den
  Grund (welcher Bezug, welche Stelle), damit die Oberfläche ihn zeigen kann.
- Fehlermeldungen des Kerns passen für beide Nutzer (heute sprechen sie von
  „Messstelle“).

**Energie-Messstellen bleiben beim bisherigen Umfang.** Ihre Formeln
rechnen bei Zählern auf dem Zuwachs seit dem Basiszeitpunkt, und die Basis
rückt jede Minute nach (`sampler.go`, `evalVirtual`). `RUNDEN`, `MIN`,
`MAX`, `WENN`, Vergleiche und `^` würden dort Zuwächse verfälschen oder
verschlucken. Deshalb lehnt das Speichern einer Messstelle Formeln mit dem
neuen Umfang weiter ab (Meldung: nur + − × ÷, Klammern und `[Kennung]`);
der Kern sagt dafür, ob eine Formel den neuen Umfang nutzt. Ob und für
welche Messstellen-Arten der neue Umfang freigegeben wird, klärt Paket 2b.
Wer die neuen Funktionen für Energie braucht, rechnet sie in einem
berechneten Wert und nimmt den als Quelle einer Messstelle.

## Wie gerechnet wird

- **Wann:** innerhalb von etwa einer Sekunde, nachdem sich ein Eingangswert
  geändert hat (mehrere Änderungen kurz hintereinander werden
  zusammengefasst), und zusätzlich alle 10 Sekunden als Sicherheitsnetz. Das
  Sicherheitsnetz erledigt auch den Wechsel der Fassung um Mitternacht und
  das Ablaufen des Höchstalters.
  - Alle Stellen, die `import_mappings.last_value` schreiben (heute sieben
    in `internal/web`: MQTT, OPC UA, Modbus, SQL, Web, Excel, gespeicherte
    Abfragen), stoßen die Rechnung über **eine** gemeinsame Funktion an. Ein
    Wächter-Test schlägt fehl, wenn eine Stelle den Wert schreibt, ohne
    anzustoßen (Muster: `TestNoUTCDayTruncate`).
- **Reihenfolge:** Berechnete Werte dürfen andere berechnete Werte nutzen;
  gerechnet wird in der Reihenfolge der Bezüge (`EvalOrder`). Kreisbezüge
  werden beim Speichern abgelehnt – geprüft gegen die geltende und alle
  geplanten Fassungen der anderen Werte.
- **Eingangswerte lesen:** eine Zahl mit Punkt oder Komma (wie
  `energie.ParseValue`); dazu `true/false`, `on/off`, `ein/aus`, `ja/nein`
  als 1/0 (Groß-/Kleinschreibung egal). Alles andere ist „keine Zahl“.
- **Ergebnis:** auf die Nachkommastellen des Werts gerundet (0 bis 6,
  Vorgabe 2), als Text mit Dezimalpunkt und ohne Exponent in
  `import_mappings.last_value` – so lesen es Export, Energiedaten und
  weitere Formeln wie jeden anderen Wert. `last_received_at` ist der
  Zeitpunkt des jüngsten Eingangswerts (Stand der Daten), nicht der
  Rechenzeitpunkt. Geschrieben wird nur, wenn sich Wert oder Zeitpunkt
  geändert haben.
- **Kein Wert, mit Grund:** `last_value` wird leer (NULL), der Grund steht
  am berechneten Wert und in der Liste. Gründe: Eingangswert hat noch nie
  einen Wert geliefert · ist keine Zahl · ist älter als das Höchstalter ·
  gibt es nicht mehr · Teilen durch 0 · Ergebnis keine Zahl. Genannt wird
  immer Kurzname und Bezeichnung des Eingangswerts. `last_received_at`
  bleibt dabei gesetzt (Zeitpunkt, zu dem der Wert leer wurde) – eine
  Energie-Messstelle mit dem berechneten Wert als Quelle darf für diese
  Zeit keinen Wert ablegen und nicht auf eine ältere Lesung zurückfallen
  (eigener Test).
- **Höchstalter** der Eingangswerte, je berechnetem Wert: im Formular mit
  **10 Minuten vorbelegt**, 1 bis 44640 Minuten (31 Tage). Ist ein
  Eingangswert älter, gibt es keinen Wert. Wer das Feld bewusst leert
  (langsame Quellen wie eine Excel-Datei), bekommt den letzten Stand, wie
  alt er auch ist – dann steht am Wert und in der Liste dauerhaft „Alter
  der Eingangswerte wird nicht geprüft“. Das Formular zeigt je Eingangswert
  Wert und Alter. So ist ein veralteter Eingangswert nie unsichtbar:
  entweder wird geprüft, oder man sieht, dass nicht geprüft wird.
- **Ausfallschutz:** Gerechnet wird nur dort, wo auch der Energie-Sampler
  läuft (nicht in der Wartung; Muster `Skip: h.MaintLocked`), nicht am
  Spiegel.

## Fassungen und Gültigkeit

- Jeder berechnete Wert hat mindestens eine Fassung: Formel, Eingangswerte
  (Kurzname → Zuordnung) und „gültig ab“ (Datum, 00:00 Uhr Ortszeit des
  Servers, über die vorhandene Ortstag-Hilfe `localDate`).
- **Gültig ab ist heute oder später.** Heute = gilt sofort. Ein Datum in der
  Vergangenheit lehnt das PDH ab und sagt, warum: Berechnete Werte haben
  keinen Verlauf, es gäbe nichts neu zu rechnen.
- Es gilt die Fassung mit dem spätesten „gültig ab“, das nicht in der
  Zukunft liegt; bei gleichem Datum die zuletzt angelegte. So lässt sich ein
  Tippfehler am selben Tag mit einer weiteren Fassung „gültig ab heute“
  beheben; die fehlerhafte bleibt als „abgelöst“ sichtbar.
- **Ändern und löschen** lässt sich nur eine geplante Fassung (Datum in der
  Zukunft). Geltende und abgelöste Fassungen bleiben, wie sie sind. Für ein
  künftiges Datum gibt es je Wert höchstens eine geplante Fassung.
- Jede Fassung zeigt Status (geplant · gilt · abgelöst), Datum, Formel in
  lesbarer Form, wer sie wann angelegt hat.
- Bezeichnung, Anlage, Einheit, Nachkommastellen und Höchstalter gehören
  zum Wert, nicht zur Fassung; sie gelten sofort.
- Wird ein Eingangswert einer abgelösten Fassung später gelöscht, behält die
  Fassung seine Bezeichnung als Text.

## Löschen und Schutz der Eingangswerte

- Einen berechneten Wert löschen: wie eine Zuordnung (Recht und Abteilung
  siehe unten). Seine Fassungen gehen mit. Wird er selbst als Eingangswert,
  Export-Feld oder Quelle einer Energie-Messstelle genutzt, sagt das PDH vor
  dem Löschen, wo.
- Eine Zuordnung, die in einer **geltenden oder geplanten** Fassung als
  Eingangswert steht, lässt sich nicht löschen; die Meldung nennt die
  berechneten Werte. Zuordnungen verschwinden heute auf drei Wegen, alle
  drei sind zu schützen: von Hand (`import_mappings.go`,
  `ImportMappingDeleteWeb`), beim Löschen einer ganzen Verbindung
  (Fremdschlüssel mit `ON DELETE CASCADE`, `import_export.go`) und beim
  Löschen einer gespeicherten Abfrage mit „samt Zuordnungen“
  (`connection_detail.go`, `with_mappings=1` – dort wird ein Fehler heute
  verschluckt). Kein Weg darf einen genutzten Eingangswert still entfernen;
  passiert es trotzdem, zeigt der berechnete Wert „kein Wert: Eingangswert
  gibt es nicht mehr“.
- Die eingebaute Verbindung „Berechnete Werte“ lässt sich nicht löschen,
  nicht abschalten, nicht umbenennen und nicht ein zweites Mal anlegen; in
  „Neue Verbindung“ steht die Art nicht zur Wahl.

## Energiedaten

- Ein berechneter Wert ist als Quelle einer Messstelle wählbar wie jede
  Zuordnung und bekommt darüber Verlauf, Schwellen und Efficio.
- Als Quelle nur für die Arten **Momentanwert** und **Zählerstand**. Bei
  **Verbrauch** zählt die Messstelle jede einzelne Lesung zusammen; ein
  berechneter Wert wird aber zusammengefasst geschrieben, das ergäbe falsche
  Summen. Das Formular der Messstelle lehnt die Kombination mit Begründung ab.

## Rechte

- **Kein neues Recht** (mein Vorschlag, wie in Paket 1): ansehen mit
  `import.read`, anlegen/ändern/löschen mit `import.write` **an der Anlage
  des berechneten Werts** (`assetGuardFor`, Abteilung zählt; beim Wechsel
  der Anlage an alter und neuer).
- Als Eingangswert wählbar ist jede Zuordnung, die die Person mit
  `import.read` sehen kann – derselbe unternehmensweite Pool wie bei den
  Export-Feldern, ohne Abteilungsgrenze (mein Vorschlag, Bestand so).
- Nichts davon am Spiegel oder in der Wartung schreiben (`mirrorGate`,
  Wartungssperre wie bei den übrigen Zuordnungen).

## Copilot

Auf der Seite „Berechnete Werte“ kennt die Copilot-Seitenleiste die
Verbindung (wie bei Paket 1) und hat diese Werkzeuge:

- vorhandene berechnete Werte mit Formel, Eingangswerten, Stand und Grund,
- wählbare Eingangswerte (Bezeichnung, Anlagenpfad, Verbindung, Wert von
  jetzt, Alter) und der Anlagenbaum,
- **Vorschlag** einer Änderungsliste: je Zeile anlegen oder neue Fassung,
  mit Bezeichnung, Anlage, Einheit, Nachkommastellen, Höchstalter,
  Eingangswerten, Formel, „gültig ab“ und Begründung. **Löschen** schlägt
  der Copilot wie in Paket 1 nur vor („Löschen bitte von Hand“); die Zeile
  lässt sich nicht übernehmen.

Das PDH prüft jede Zeile wie das Formular (Schreibweise, unbekannte
Eingangswerte, Kreisbezug, Recht an der Anlage) und legt den Vorschlag in
`copilot_proposals` ab (24 Stunden, nur für die Person, die gefragt hat).
In der Seitenleiste erscheint die Änderungsliste mit Formel in lesbarer
Form und Probe-Ergebnis; angelegt wird nur, was angehakt ist, mit
„Übernehmen“, über denselben Schreibweg wie das Formular.

- Der Haken „Im Internet nach einer Vorlage suchen“ erscheint auf dieser
  Seite nicht (für Formeln ohne Nutzen).
- Der Hinweis zum Datenschutz bleibt wie in Paket 1: Bezeichnungen,
  Anlagennamen und Werte gehen an den Anbieter des Copilot, Geheimnisse nie.
- Ohne Sprachmodell gibt es hier keinen Vorschlagsknopf (für Formeln gibt
  es keine sinnvolle Regel ohne Modell).

## Daten

- Migration **205** (Nummer und Version vor dem Merge gegen `main` prüfen;
  Stand heute: `main` 0.82.36, letzte Migration 204; Version voraussichtlich
  **0.82.37**):
  - eingebaute Verbindung: eine Zeile in `import_export_connections`
    (`direction = 'import'`, `kind = 'calc'`, Name „Berechnete Werte“), per
    Migration angelegt, höchstens eine (eindeutiger Teilindex),
  - Angaben zum berechneten Wert (1:1 zur Zuordnung): Einheit,
    Nachkommastellen, Höchstalter, Stand der letzten Rechnung, Grund,
  - Fassungen: Zuordnung, gültig ab, Formel, angelegt von/am,
  - Eingangswerte je Fassung: Kurzname, Zuordnung (bleibt beim Löschen der
    Zuordnung als Zeile mit Bezeichnung erhalten).
- Kennungen mit `pdh_uuid_v7()` (UUID-Regel, `TestUUIDv7Policy`).
- `migrations/checksums.txt` mit
  `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.

## Was mitgepflegt wird

- Handbuch, Kapitel `connections`: „So legst du einen berechneten Wert an“,
  „So änderst du eine Formel zu einem Stichtag“, Liste der Funktionen mit
  je einem Beispiel, Rechte; Skizze in `help_figures.gohtml`. Kapitel
  Energiedaten: neue Funktionen, berechneter Wert als Quelle (nicht für
  Verbrauch).
- `CHANGELOG.md` (`### Neu`), `VERSION`, Übersetzungen in
  `web/i18n/{en,ro,tr,mk}.json`, AGENTS.md Punkt 24.
- Fiori und Fluent, hell und dunkel, 320 bis 1366 px; Farben über die
  Variablen. Das Formular muss am Handy bedienbar sein (Liste der
  Eingangswerte und Knöpfe untereinander).

## Prüfung

- Rechenkern: Tests je Funktion, Vorrang, träges `WENN`, Fehlerstellen,
  Bezüge mit und ohne Klammern; die vorhandenen Tests der Energiedaten
  bleiben unverändert grün.
- Rechnung mit Labor-Datenbank: Wert folgt dem Eingangswert, Kette aus zwei
  berechneten Werten, jeder Grund für „kein Wert“, Höchstalter,
  Fassungswechsel am Stichtag (mit gestellter Uhr), nichts in der Wartung.
- Rechte: ohne `import.write`, fremde Abteilung, Wechsel der Anlage.
- Schutz: genutzter Eingangswert lässt sich auf keinem Weg löschen.
- Copilot: Vorschlag wird geprüft, fremde Person kann ihn nicht übernehmen,
  nicht angehakte Zeilen bleiben weg (Muster der Tests aus Paket 1).
- Browserprüfung am Testverbund mit dem Prüfkonto (dort gibt es
  MQTT-Zuordnungen; Werte lassen sich über den eingebauten Broker stellen).

## Dem Bauenden überlassen

- Namen der Tabellen, Spalten, Routen und des Pakets für den Rechenkern.
- Ob die Angaben zum berechneten Wert in einer eigenen Tabelle oder in
  Spalten an `import_mappings` liegen.
- Womit ein genutzter Eingangswert geschützt wird (Prüfung in den
  Löschwegen, Fremdschlüssel oder Auslöser in der Datenbank) – solange jeder
  Weg erfasst ist und die Meldung die berechneten Werte nennt.
- Wie der Anstoß technisch läuft (Kanal mit Zusammenfassen o. Ä.) und wie
  der Wächter-Test die Schreibstellen findet.
- `source_ref` der Zuordnung eines berechneten Werts (muss je Wert
  eindeutig sein; in der Oberfläche nie roh zeigen).
- Ob `copilot_proposals` eine Spalte für die Art des Vorschlags bekommt oder
  die Art in den Zeilen steht.
- Aufbau des Formulars im Einzelnen (htmx/JS), solange die Skizze oben
  getroffen ist: Eingangswerte mit Kurznamen, Knöpfe für Zeichen und
  Funktionen, Prüfung beim Tippen, Probe, Fassungen.
