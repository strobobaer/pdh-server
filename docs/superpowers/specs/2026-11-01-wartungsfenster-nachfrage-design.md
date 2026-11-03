# Wartungsfenster mit Nachfrage

Stand: 2026-11-01 · Status: Entwurf zur Durchsicht ·
Erweitert: `2026-10-30-verbund-update-design.md` (Abschnitte 4, 5, 8, 10, 11) ·
Version: wird Teil von 0.77.8 (noch nicht veröffentlicht)

## 1. Ziel und Umfang

Das Wartungsfenster spielt im Verbund heute ohne Rückfrage ein, sofern „alles
grün“ ist. Ist der Verbund auf **„Automatik aus (Wartung)“** gestellt, lehnt
pdh-ha jedes Update ab („Wartung eingeschaltet“), über das Fenster genauso wie
über den Knopf.

Neu:

1. **Automatik aus (Wartung):** Das Wartungsfenster fragt vor dem Update nach.
   Ein Berechtigter hat **3 Minuten**, um zu bestätigen. Bestätigt er, läuft
   das Update trotz Wartung im gewohnten Ablauf. Sagt er ab oder antwortet
   niemand, **entfällt** das Update.
2. **Knopf „Update installieren“ in der Wartung:** Der Klick gilt als
   Bestätigung. Das Update läuft auch bei „Automatik aus“.
3. **Automatik an:** Das Fenster spielt wie bisher ohne Nachfrage ein.
   Neu ist, dass ein laufendes Update **vor der Sperre** jederzeit über einen
   Knopf **Abbrechen** gestoppt werden kann: im Countdown-Band und im
   Fortschrittskasten. Bisher ging das nur im Sperrbildschirm oder per
   `pdh-ha update abort`.

Nicht im Umfang:

- Benachrichtigung per E-Mail, Chat oder Push bei einer Nachfrage.
- Einstellbare Wartezeit (fest 3 Minuten).
- Einzelserver. Dort gibt es keinen Automatik-Schalter, alles bleibt wie
  heute.
- Rollentausch-Aufträge (`swap`) des Fensters. Sie bleiben in der Wartung
  gesperrt, weil sie nichts einspielen.

## 2. Begriffe

- **Nachfrage:** neuer erster Schritt eines Auftrags, Phase `nachfrage`
  (`UpAsk`).
- **Entfallen:** Ende eines Auftrags in der Nachfrage ohne jede Änderung:
  nichts eingespielt, nichts zurückgedreht, nichts als fehlerhaft gemerkt.
- **Wartung im Verbund:** `Maint` dieses Servers oder des Partners, wie in
  `readyObsLocked`.

## 3. Ablauf

| Schalter | Wartungsfenster (`zeitplan`) | Knopf (`knopf`) |
|---|---|---|
| Automatik an | wie bisher | wie bisher |
| Automatik aus (Wartung) | **Nachfrage** (3 min) → bestätigt: normaler Ablauf; abgesagt/abgelaufen: entfällt | normaler Ablauf (der Klick ist die Bestätigung) |

### 3.1 Anlegen

`startUpdateReqLocked` (cmd/pdh-ha/update_api.go):

- Die Prüfung „alles grün“ bekommt die Angabe, ob die Wartung hindert.
  `ReadyObs` erhält `MaintAllowed bool`. Ist es gesetzt, entfällt der Grund
  „Wartung eingeschaltet“, alle anderen Gründe gelten weiter.
- `MaintAllowed` ist gesetzt bei `src == knopf` sowie bei `src == zeitplan`
  mit Art `rolling` oder `sequential`.
- Ist die Wartung im Verbund eingeschaltet und `src == zeitplan`, beginnt der
  Auftrag in `UpAsk` mit `AskUntil = jetzt + UpdateAskWait` (3 min).
  Ereignis (info): „Wartungsfenster: Update auf <Version> wartet auf
  Bestätigung (bis HH:MM)“.
- Sonst beginnt der Auftrag wie bisher in `UpPrepare`.
- Das Fenster gilt in beiden Fällen als für heute gelaufen
  (`WindowLastRun` wie bisher beim Anlegen).

### 3.2 Nachfrage (`UpAsk`, internal/ha/update.go)

Neue Felder in `UpdateJob`: `AskUntil time.Time`, `ConfirmedBy string`.
Neue Eingabe in den Schritt-Optionen: `Confirm string` (Name des
Bestätigenden, `""` = keiner), analog zu `Abort`.

Der Schritt in `UpAsk`, Vorrang von oben nach unten:

1. **Abort gesetzt:** `finish(UpFailed, "Update abgesagt von <Name>")`,
   Ereignis (info) „Update auf <Version> abgesagt von <Name>“.
2. **Confirm gesetzt:** `ConfirmedBy = Name`, Ereignis (info) „Update auf
   <Version> bestätigt von <Name>“, weiter mit `UpPrepare` (PhaseAt null, der
   nächste Schritt schickt wie bisher den Auftrag los).
3. **Frist abgelaufen** (`Now >= AskUntil`): `finish(UpFailed, "Update
   entfallen: nicht bestätigt")`, Ereignis (info) „Update auf <Version>
   entfallen: nicht bestätigt“.
4. Sonst warten.

`MarkFaulty` wird in `UpAsk` nie gesetzt. Es ist kein Alarm, denn das
Entfallen ist das gewollte Verhalten bei „Automatik aus“.

**Erneute Prüfung bei Bestätigung:** pdh-ha prüft beim Bestätigen „alles
grün“ erneut (mit `MaintAllowed`, ohne „Ein Auftrag läuft bereits“ für den
eigenen Auftrag, also wie `Resume`). Ist sie nicht grün, endet der Auftrag
mit „Update entfallen: <Grund>“, und die Schnittstelle meldet den Grund
zurück. Damit gilt der Zustand von jetzt, nicht der von vor bis zu 3 Minuten.

**Neustart von pdh-ha während der Nachfrage:** `AskUntil` ist eine feste
Uhrzeit im gespeicherten Auftrag. Nach dem Neustart läuft die Nachfrage
weiter oder entfällt sofort, wenn die Frist vorbei ist.

**Rollenwechsel während der Nachfrage** (nur von Hand möglich, die Automatik
ist aus): Der Auftrag entfällt mit „Update entfallen: Rolle gewechselt“. Das
gilt auch, wenn der dirigierende Server nicht mehr aktiv ist.

**Automatik wird während der Nachfrage eingeschaltet:** keine Sonderregel. Die
Nachfrage läuft weiter, bis bestätigt, abgesagt oder abgelaufen. So bleibt das
Verhalten vorhersehbar.

### 3.3 Abbrechen vor der Sperre (alle Schalterstellungen)

Unverändert in pdh-ha (`abort()` behandelt `UpPrepare`, `UpVerifyPrep` und
`UpCountdown` schon mit `undoPrep`). Neu ist nur, dass das PDH den Abbruch
auch außerhalb der Sperre anbietet (Abschnitt 5).

## 4. Schnittstellen

pdh-ha (Unix-Socket, root/Agent wie bisher):

- **neu** `POST /v1/update/confirm {"by"}`: nur in `UpAsk`. Sonst Fehler
  „Keine Nachfrage offen“. Fehler mit Grund, wenn die erneute Prüfung nicht
  grün ist.
- `POST /v1/update/abort {"by"}`: in `UpAsk` bedeutet das „absagen“.
- `GET /v1/update` (`UpdateView`) erhält `ask_until` (Null außerhalb der
  Nachfrage). `phase = "nachfrage"`.
- `printUpdate` (`pdh-ha update status`) zeigt in der Nachfrage
  „wartet auf Bestätigung bis HH:MM“.
- **neu** CLI `pdh-ha update confirm` (wie `abort`, `by = Befehlszeile`).

Update-Agent (`windowToHA`): Er entscheidet nichts selbst und gibt den
Fensterlauf wie bisher an `/v1/update/start` weiter. Die Antwort enthält neu
`"asking": true`, wenn der Auftrag mit der Nachfrage beginnt. Der Agent trägt
dann als Ergebnis „Update auf <Version> wartet auf Bestätigung“ ein. Das
spätere Ende („entfallen …“, „abgesagt …“, „abgeschlossen“) erscheint wie bei
jedem anderen Auftrag in „Letztes Ergebnis“.

## 5. Oberfläche (PDH)

**Routen:**

- **neu** `POST /wartung/durchfuehren`: Recht wie „Update installieren“
  (`canManageRoles`), leitet an `/v1/update/confirm` weiter
  (`by` = Anzeigename).
- `POST /wartung/abbrechen`: gilt künftig auch außerhalb der Sperre
  (Nachfrage, Vorbereiten, Prüfen, Countdown). Recht
  `system.maintenance_abort` wie bisher.
- `GET /wartung/status` liefert zusätzlich `phase`, `ask_until`,
  `to_version`, `can_confirm` und `can_abort` (je nach Recht des Anfragenden).

**Band auf allen Seiten** (`base.gohtml`, das vorhandene Countdown-Band
erweitern):

- **Nachfrage:** „Wartungsfenster: Update auf 0.77.93 durchführen? Noch
  2:41 min“ mit den Knöpfen **Durchführen** und **Absagen**. Sichtbar nur für
  Benutzer mit `can_confirm` oder `can_abort`. Der jeweilige Knopf erscheint
  nur mit dem passenden Recht. Andere Benutzer sehen in der Nachfrage nichts,
  denn es ist noch offen, ob überhaupt gewartet wird.
- **Countdown:** wie bisher, zusätzlich **Abbrechen** für `can_abort`.
- Abfrage alle 15 s wie bisher. In der Nachfrage und im Countdown zählt die
  Seite sekündlich herunter.
- Beide Knöpfe fragen per `confirm()` zurück („Update jetzt durchführen?“ bzw.
  „Update absagen?“ / „Update abbrechen und zurücksetzen?“).

**Fortschrittskasten** (`widgets/ha.gohtml`, Server-Einstellungen →
Ausfallschutz):

- In der Nachfrage dieselben zwei Knöpfe mit Restzeit.
- In Vorbereiten, Prüfen und Countdown der Knopf **Abbrechen**.

**Gestaltung:** Farben über die Variablen (`--bg2`, `--text`, `--accent`,
`--border`). Prüfen in Fiori und Fluent, jeweils hell und dunkel, sowie bei
390 px Breite.

## 6. Rechte

Kein neues Recht. Die Aktionen sind keine neue Befugnis:

- **Durchführen** = ein Update starten → dieselbe Prüfung wie der Knopf
  „Update installieren“ (`canManageRoles`).
- **Absagen / Abbrechen** = „Automatische Wartung abbrechen“
  (`system.maintenance_abort`, gibt es seit 0.77.8).

Das Handbuch-Kapitel „Administration“ beschreibt bei beiden Rechten die neue
Verwendung.

## 7. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Niemand antwortet | nach 3 min „Update entfallen: nicht bestätigt“, nächster Versuch am nächsten Fenstertermin |
| Bestätigt, aber inzwischen nicht grün | „Update entfallen: <Grund>“, Meldung an den Bestätigenden |
| Zwei Benutzer klicken gleichzeitig | der erste gewinnt; der zweite bekommt „Keine Nachfrage offen“ |
| Durchführen und Absagen fast gleichzeitig | Vorrang Absagen vor Bestätigen im selben Schritt (3.2) |
| pdh-ha startet neu | Nachfrage läuft mit gespeicherter Frist weiter |
| Rollenwechsel von Hand | „Update entfallen: Rolle gewechselt“ |
| PDH nicht erreichbar | Bestätigen über `pdh-ha update confirm`; sonst entfällt das Update |

## 8. Handbuch, Änderungsprotokoll, Version

- **Handbuch, Kapitel „Ausfallschutz“, Abschnitt Wartungsfenster:**
  - So-geht's „Nachfrage beantworten“: Band erscheint → Durchführen oder
    Absagen; ohne Antwort entfällt das Update.
  - Ein Satz: „Bei Automatik an spielt das Fenster ohne Nachfrage ein. Vor der
    Sperre lässt sich das Update mit Abbrechen stoppen.“
  - Ein Satz: „Update installieren funktioniert auch bei Automatik aus.“
  - Skizze des Bands in `help_figures.gohtml`.
- **Kapitel „Administration“:** Verwendung der beiden Rechte (Abschnitt 6).
- **CHANGELOG 0.77.8:**
  - `### Neu`: Nachfrage bei Automatik aus.
  - `### Neu`: Abbrechen vor der Sperre.
  - `### Geändert`: Knopf auch in der Wartung.
- `VERSION` bleibt 0.77.8, solange sie nicht auf GitHub main ist.

## 9. Tests und Abnahme

**internal/ha** (Tabellentests am Zustandsautomaten):

- Nachfrage → Frist abgelaufen: entfällt, kein `MarkFaulty`, keine Operation
  an einen Knoten gesendet.
- Nachfrage → Bestätigen → `UpPrepare`, die erste Operation geht los.
- Nachfrage → Absagen: entfällt.
- Absagen und Bestätigen im selben Schritt: abgesagt.
- Neustart (frischer `upRun` mit gespeichertem Auftrag) vor und nach
  `AskUntil`.
- Rollenwechsel in der Nachfrage: entfällt.
- `UpdateReady` mit `Maint` und `MaintAllowed` true/false.

**cmd/pdh-ha:**

- `startUpdateReqLocked`: Wartung + zeitplan → `UpAsk`; Wartung + knopf →
  `UpPrepare`; keine Wartung + zeitplan → `UpPrepare`; Wartung + swap →
  abgelehnt.
- `/v1/update/confirm`: außerhalb von `UpAsk` → Fehler; nicht grün → entfällt
  mit Grund.

**internal/web:**

- `/wartung/durchfuehren`: ohne Recht 403, mit Recht wird weitergeleitet.
- `/wartung/abbrechen` außerhalb der Sperre.
- `/wartung/status` mit `can_confirm` und `can_abort` je nach Recht.
- `TestHelpCoversAllPages` und `TestHelpChaptersMatch` bleiben grün.

**Abnahme auf den Testservern** (nie 10.10.0.202):

1. Automatik aus, Fensterzeit kurz voraus → Band erscheint → **Durchführen**
   → Update läuft durch.
2. Wie 1, keine Antwort → nach 3 min „entfallen: nicht bestätigt“, auf beiden
   Servern nichts geändert.
3. Wie 1 → **Absagen** → „abgesagt von …“.
4. Automatik an, Update über den Knopf → im Countdown **Abbrechen** → B
   wird zurückgedreht, Version nicht fehlerhaft.
5. Automatik aus → Knopf „Update installieren“ → Update läuft.
