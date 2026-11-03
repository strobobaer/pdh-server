# Wartungsfenster mit Nachfrage – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bei „Automatik aus (Wartung)“ fragt das Wartungsfenster 3 Minuten nach, bevor es einspielt. Der Knopf „Update installieren“ geht auch in der Wartung. Ein laufendes Update lässt sich vor der Sperre im PDH abbrechen.

**Architecture:** Neue Phase `nachfrage` (`UpAsk`) als erster Schritt des Auftrags im Zustandsautomaten (`internal/ha/update.go`). pdh-ha legt Fenster-Aufträge in der Wartung in dieser Phase an und bekommt `POST /v1/update/confirm`. Das PDH zeigt die Nachfrage im vorhandenen Countdown-Band und im Fortschrittskasten und leitet Durchführen/Absagen/Abbrechen an pdh-ha weiter.

**Tech Stack:** Go 1.x, html/template, htmx, Vanilla-JS im Band (`base.gohtml`).

**Spec:** `docs/superpowers/specs/2026-11-01-wartungsfenster-nachfrage-design.md` (freigegeben 2026-11-01)

## Global Constraints

- Alle Texte (Oberfläche, Ereignisse, Handbuch, CHANGELOG, Commits) auf Deutsch, für Anwender im Betrieb.
- Wartezeit fest: `UpdateAskWait = 3 * time.Minute`.
- Ereignis-/Ergebnistexte wörtlich (`<Name>` = `UpdateJob.name()`, z. B. „Update auf 0.77.93“):
  - „Wartungsfenster: <Name> wartet auf Bestätigung (bis HH:MM)“
  - „<Name> bestätigt von <Wer>“
  - „<Name> abgesagt von <Wer>“, Ergebnis „Update abgesagt von <Wer>“
  - Ergebnis „Update entfallen: nicht bestätigt“, „Update entfallen: Rolle gewechselt“, „Update entfallen: <Grund>“
  - Alle diese Ereignisse sind `info`, nie `alarm`. In `UpAsk` wird `MarkFaulty` nie gesetzt.
- Kein neues Recht: Durchführen = `system.manage_roles` (wie `canManageRoles`), Absagen/Abbrechen = `system.maintenance_abort`.
- Einzelserver unverändert. Fenster-Rollentausch (`swap`) bleibt in der Wartung gesperrt.
- Farben im PDH nur über Variablen (`--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--amber`, `--red`). Fiori und Fluent, hell und dunkel.
- Versionierung: Einträge in den obersten CHANGELOG-Abschnitt `0.77.8`, solange `git show origin/main:VERSION` nicht `0.77.8` ist; sonst nächste Korrekturversion.
- Commits ohne Zuschreibungszeilen. Kein Push, kein Merge ohne ausdrückliche Freigabe.
- Der Produktionsserver 10.10.0.202 wird nie angefasst.

## Review Focus

1. Nicht angemeldeter Aufruf von `/wartung/status` während der Nachfrage darf weder Version noch Nachfrage verraten → Test in Task 3.
2. Durchführen nach Ablauf der Frist (Seite noch offen): Der Benutzer sieht eine klare Meldung „Keine Nachfrage offen“, keine stille Wirkung → Tests in Task 2 und Task 3.
3. Zwei Durchführen-Klicks im selben Takt (Band und Kasten): Der zweite bekommt „Keine Nachfrage offen“ → Test in Task 2.
4. POST `/wartung/durchfuehren` von fremder Seite (`Sec-Fetch-Site: cross-site`) → 403 → Test in Task 3.
5. Neustart von pdh-ha während der Nachfrage darf den Auftrag nicht als „Neustart“ abbrechen (`beforeLock` enthält `UpAsk` nicht) → Test in Task 2.

---

### Task 1: Phase „nachfrage“ im Zustandsautomaten

**Files:**
- Modify: `internal/ha/update.go` (Phasen-Konstanten Z. 31–41, `UpdateJob`, `UpdateObs`, `step()`, `abort()`)
- Modify: `internal/ha/update_ready.go` (`ReadyObs`, `UpdateReady`)
- Modify: `internal/ha/update_view.go` (`UpdateView`, `View`, `viewStep`)
- Test: `internal/ha/update_test.go`

**Interfaces:**
- Produces:
  - `UpAsk UpdatePhase = "nachfrage"`
  - `const UpdateAskWait = 3 * time.Minute`
  - `UpdateJob.AskUntil time.Time`, `UpdateJob.ConfirmedBy string`
  - `UpdateObs.Confirm string` (Name des Bestätigenden, `""` = keiner), `UpdateObs.ConfirmBlocked string` (Grund, `""` = keiner)
  - `ReadyObs.MaintAllowed bool`
  - `UpdateView.AskUntil time.Time` (`json:"ask_until"`, nur in `UpAsk` gesetzt)

- [ ] **Step 1: Failing tests schreiben** – neue Fälle in `TestUpdateStepTable` (Hilfen `upJob`, `upObs`, `evA` wie vorhanden) bzw. eigene Tests:
  - `nachfrage wartet`: `j := upJob(UpdateRolling, UpAsk); j.AskUntil = now.Add(time.Minute)` → Phase bleibt `UpAsk`, keine Aktionen.
  - `nachfrage abgelaufen`: `AskUntil = now` → Phase `UpFailed`, `Result == "Update entfallen: nicht bestätigt"`, `MarkFaulty == false`, Aktionen nur `evA("info", "Update auf 0.77.9 entfallen: nicht bestätigt")` (Version wie in `upJob`).
  - `nachfrage bestätigt`: `o.Confirm = "Max Muster"` → Phase `UpPrepare`, `PhaseAt.IsZero()`, `ConfirmedBy == "Max Muster"`, Ereignis `info` „… bestätigt von Max Muster“, keine node_op/local_op.
  - `nachfrage bestätigt, nicht grün`: `o.Confirm = "Max"; o.ConfirmBlocked = "Spiegel nicht synchron"` → `UpFailed`, `Result == "Update entfallen: Spiegel nicht synchron"`.
  - `nachfrage abgesagt`: `o.Abort = "Max"` → `UpFailed`, `Result == "Update abgesagt von Max"`, `AbortedBy == "Max"`, keine node_op/local_op.
  - `nachfrage abgesagt und bestätigt`: `o.Abort = "A"; o.Confirm = "B"` → `Result == "Update abgesagt von A"`.
  - `nachfrage Übernahme`: `o.Takeover = true` → `UpFailed`, `Result == "Update entfallen: Rolle gewechselt"`, Ereignis `info` (nicht `alarm`).
  - `TestUpdateReady`: `Maint: true` → nicht ok mit „Wartung eingeschaltet“; `Maint: true, MaintAllowed: true` → ok.
  - `TestUpdateViewPhases`: `UpAsk` → `Text == "Wartet auf Bestätigung"`, `Percent == 0`, `AskUntil` gleich `j.AskUntil`; in `UpPrepare` ist `AskUntil.IsZero()`.

- [ ] **Step 2: Tests laufen lassen, sie schlagen fehl**
  Run: `go test ./internal/ha/ -run 'TestUpdateStepTable|TestUpdateReady|TestUpdateViewPhases'`
  Expected: FAIL (`undefined: UpAsk` o. ä.)

- [ ] **Step 3: Umsetzen**
  - Phase, Konstante und Felder wie in Interfaces.
  - `step()`: Im `o.Takeover`-Zweig zuerst `if r.j.Phase == UpAsk` → Ereignis `info` und `finish(UpFailed, "Update entfallen: Rolle gewechselt")`, sonst wie bisher. Im `switch` neu `case UpAsk: r.ask()`.
  - `func (r *upRun) ask()`: Vorrang `ConfirmBlocked` (mit gesetztem `Confirm`) → `Confirm` → Frist (`!o.Now.Before(j.AskUntil)`). Bei Bestätigung `j.Phase = UpPrepare`, `j.PhaseAt = time.Time{}`, `clearWait()`, nicht `enter()` (`prepare()` erkennt den Eintritt an leerem `PhaseAt`).
  - `abort()`: `case UpAsk:` → `AbortedBy`, Ereignis `info` „<Name> abgesagt von <Wer>“, `finish(UpFailed, "Update abgesagt von "+o.Abort)`, `return true`. Kein `undoPrep`.
  - `UpdateReady`: `add(o.Maint && !o.MaintAllowed, "Wartung eingeschaltet")`.
  - `View`: `AskUntil` nur bei `j.Phase == UpAsk`. `viewStep`: `case UpAsk: return 0, 0, "Wartet auf Bestätigung"`.

- [ ] **Step 4: Tests laufen lassen**
  Run: `go test ./internal/ha/`
  Expected: PASS

- [ ] **Step 5: Commit**
  `git add internal/ha && git commit -m "Update im Verbund: Schritt „Nachfrage“ vor dem Einspielen (bestätigen, absagen, Frist 3 min)"`

---

### Task 2: pdh-ha – Nachfrage anlegen, bestätigen, CLI; Update-Agent

**Files:**
- Modify: `cmd/pdh-ha/update_api.go` (`startUpdate`, `startUpdateReqLocked`, Mux, `printUpdate`, `cmdUpdate`)
- Modify: `cmd/pdh-ha/update_job.go` (`startUpdateLocked`, `abortUpdate` daneben `confirmUpdate`, `updateTickLocked`, `d.up`-Felder)
- Modify: `cmd/pdh-ha/main.go` (Konstante `usage`)
- Modify: `cmd/update-agent/window.go` (`windowToHA`)
- Test: `cmd/pdh-ha/update_api_test.go`, `cmd/update-agent/window_test.go`

**Interfaces:**
- Consumes (Task 1): `ha.UpAsk`, `ha.UpdateAskWait`, `UpdateJob.AskUntil`, `UpdateObs.Confirm/ConfirmBlocked`, `ReadyObs.MaintAllowed`.
- Produces:
  - `func (d *daemon) startUpdate(req updateStartReq) (hints []string, asking bool, err error)` (alle Aufrufer anpassen)
  - `type updateConfirmReq struct { By string `json:"by"`; updateStartReq }` (nutzt dessen Sicherungsangaben)
  - `func (d *daemon) confirmUpdate(req updateConfirmReq) error`
  - `POST /v1/update/confirm` → 200 `{"ok":true}`, 409 `{"ok":false,"error":"Update entfallen: <Grund>"}`, sonst `apiFail`
  - Antwort von `POST /v1/update/start` neu mit `"asking": true|false`
  - CLI `pdh-ha update confirm`

- [ ] **Step 1: Failing tests schreiben** (in `update_api_test.go` mit `updAPI`/`updCall`, Wartung über `d.p.Maint = true` bzw. Partner-Sicht mit `Maint`):
  - `TestAPIUpdateStartAskInMaint`: Wartung an, `source: "zeitplan"`, Commit `x1` → 200, `asking == true`, `d.up.job.Phase == ha.UpAsk`, `AskUntil == wall+3min`. Ereignis „Wartungsfenster: Update auf x1 wartet auf Bestätigung (bis HH:MM)“ ist vorhanden.
  - `TestAPIUpdateStartKnopfInMaint`: Wartung an, `source: "knopf"` → 200, `asking == false`, Phase `UpPrepare`.
  - `TestAPIUpdateStartNoMaintNoAsk`: Wartung aus, `zeitplan` → Phase `UpPrepare`, `asking == false`.
  - `TestAPIUpdateStartSwapInMaint`: Wartung an, `zeitplan`, `kind: "swap"` → 409 mit „Wartung eingeschaltet“.
  - `TestAPIUpdateConfirm`:
    - außerhalb `UpAsk` → Fehler „Keine Nachfrage offen“;
    - in `UpAsk` → 200, nach `d.tick()` Phase `UpPrepare` und `ConfirmedBy`;
    - zweiter Aufruf vor dem Takt → „Keine Nachfrage offen“;
    - Partner nicht synchron → 409 „Update entfallen: Spiegel nicht synchron“, nach `d.tick()` `UpFailed` mit diesem Ergebnis.
  - `TestUpdateAskSurvivesRestart`: Auftrag in `UpAsk` speichern, Daemon neu laden (wie vorhandene Neustart-Tests über `loadJob`) → `d.up.restartAbort == ""`, Phase weiter `UpAsk`.
  - `window_test.go` (`TestWindowTickHandsToHA`-Muster): pdh-ha-Attrappe antwortet `{"ok":true,"asking":true}` → `LastResult == "Update auf <v> wartet auf Bestätigung"`.

- [ ] **Step 2: Tests laufen lassen, sie schlagen fehl**
  Run: `go test ./cmd/pdh-ha/ ./cmd/update-agent/ -run 'Ask|Confirm|KnopfInMaint|SwapInMaint|HandsToHA'`
  Expected: FAIL

- [ ] **Step 3: Umsetzen**
  - `startUpdateReqLocked`:
    - nach `readyObsLocked`: `ro.MaintAllowed = src == srcKnopf || kind != ha.UpdateSwap`;
    - `ask := src == srcZeitplan && ro.Maint && kind != ha.UpdateSwap`;
    - bei `ask`: `j.Phase, j.PhaseAt, j.AskUntil = ha.UpAsk, wall, wall.Add(ha.UpdateAskWait)` vor `startUpdateLocked`;
    - Ereignis mit `j.AskUntil.Local().Format("15:04")`;
    - `asking` zurückgeben.
  - `startUpdateLocked`: Phase nur dann auf `UpPrepare`/leeres `PhaseAt` setzen, wenn `j.Phase != ha.UpAsk`.
  - `beforeLock` bleibt unverändert (ohne `UpAsk`).
  - `d.up` bekommt `confirm, confirmBlocked string`.
  - `confirmUpdate`, unter `d.mu`:
    - Auftrag offen und `UpAsk`, `d.up.confirm == "" && d.up.confirmBlocked == ""`, sonst „Keine Nachfrage offen“;
    - Rolle aktiv, sonst „Bestätigen nur auf dem aktiven Server“;
    - `ro := readyObsLocked(...)` mit `MaintAllowed = true`, `JobOpen = false`;
    - nicht grün → `d.up.confirmBlocked = reason`, `notReadyError{reason: "Update entfallen: " + reason}`;
    - sonst `d.up.confirm = by` (leer → „unbekannt“).
  - `updateTickLocked`: `o.Confirm = u.confirm`, `o.ConfirmBlocked = u.confirmBlocked`; nach dem Schritt beide leeren, neben `u.abort`.
  - Mux: `POST /v1/update/confirm` wie `/start` (409 bei `notReadyError`). Start-Antwort mit `"asking"`.
  - `printUpdate`: in Phase `nachfrage` die Zeile „Nachfrage: wartet auf Bestätigung bis HH:MM“.
  - `cmdUpdate`: `case "confirm"` → POST mit `by: "Befehlszeile"`, Ausgabe „Bestätigt – das Update beginnt.“
  - `usage`: Zeile `update confirm                      Nachfrage des Wartungsfensters bestätigen`.
  - `windowToHA`: `resp` erhält `Asking bool `json:"asking"``; bei `Asking` lautet das Ergebnis `what + " wartet auf Bestätigung"`.

- [ ] **Step 4: Tests laufen lassen**
  Run: `go test ./cmd/pdh-ha/ ./cmd/update-agent/ ./internal/ha/`
  Expected: PASS

- [ ] **Step 5: Commit**
  `git add cmd/pdh-ha cmd/update-agent && git commit -m "pdh-ha: Wartungsfenster fragt bei Automatik aus nach; Knopf-Update auch in der Wartung; pdh-ha update confirm"`

---

### Task 3: PDH – Durchführen/Absagen/Abbrechen außerhalb der Sperre, Status

**Files:**
- Modify: `internal/web/maint_lock.go` (`maintLock`-Felder, `MaintLockMiddleware`, `maintStatus`, neu `maintMayConfirm`, `maintConfirm`, `maintActionPost`)
- Test: `internal/web/maint_lock_test.go`

**Interfaces:**
- Consumes (Task 2): `POST /v1/update/confirm {"by","backup_configured","last_base_ok","last_files_ok"}`, `UpdateView.AskUntil`, `Phase == "nachfrage"`.
- Produces:
  - `maintLock.confirm func(ctx context.Context, by string) error` (nil → pdh-ha)
  - `maintLock.confirmer func(*users.User) bool` (nil → globales Recht `system.manage_roles`)
  - `func (h *Handler) maintMayConfirm(u *users.User) bool`
  - `func (h *Handler) maintConfirm(ctx context.Context, by string) error`: nur im Verbund (`haNodeConfigured()`), Body mit `h.drBackupFlags(ctx)`, sonst `uiError("Keine Nachfrage offen")`
  - `/wartung/status` zusätzlich (nur bei Sitzung mit einem der Rechte):
    - `phase`
    - `ask_until` (RFC3339, nur `nachfrage`)
    - `to_version`
    - `can_confirm` und `can_abort` (bool)
  - `type maintBox struct { ha.UpdateView; CanConfirm, CanAbort bool }` (für Task 4)

- [ ] **Step 1: Failing tests schreiben** (`maintTestHandler`, `maintDo`, `maintToken`; Fakes `h.mlock.confirm`/`confirmer`/`abort` zählen Aufrufe):
  - `TestStatusAskOnlyWithRight`:
    - View `Phase: "nachfrage", Active: true, AskUntil: t+2min, ToVersion: "0.77.9"`;
    - ohne Cookie → JSON ohne `ask_until`/`to_version`/`phase`;
    - als `admin` (Recht Abbruch, `confirmer` true) → `phase == "nachfrage"`, `ask_until` gesetzt, `can_confirm` und `can_abort` true;
    - als `u1` → nichts davon.
  - `TestConfirmPostUnlocked`:
    - POST `/wartung/durchfuehren` als Benutzer mit `confirmer` true, `Accept: application/json` → 200 `{"ok":true}`, Fake mit „Max Muster“ aufgerufen;
    - ohne Recht → 403, Fake nicht aufgerufen.
  - `TestConfirmPostError`: Fake liefert `errors.New("Keine Nachfrage offen")` → JSON `ok:false`, `error` enthält den Text.
  - `TestConfirmPostCrossSite`: `Sec-Fetch-Site: cross-site` → 403.
  - `TestAbortPostUnlocked`:
    - View `Phase: "countdown"` (nicht gesperrt), POST `/wartung/abbrechen` als `admin`, `Accept: application/json` → `abort`-Fake aufgerufen, 200;
    - als Formular (`Accept: text/html`) → 303 nach `/admin/server-config?tab=dr&msg=…`.
  - `TestConfirmPostLocked`: gesperrte View, POST `/wartung/durchfuehren` → 409, Fake nicht aufgerufen.
  - Vorhandene Tests (`TestLock…`) bleiben grün.

- [ ] **Step 2: Tests laufen lassen, sie schlagen fehl**
  Run: `go test ./internal/web/ -run 'Status|ConfirmPost|AbortPost|TestLock'`
  Expected: FAIL

- [ ] **Step 3: Umsetzen**
  - In `MaintLockMiddleware`, Zweig `!v.Locked`: vor der Umleitung von `/wartung/*` die beiden POST-Pfade an `maintActionPost(w, r, confirm bool)`. Im gesperrten Zweig `/wartung/durchfuehren` → 409 „Keine Nachfrage offen“.
  - `maintActionPost`:
    - `Sec-Fetch-Site`-Prüfung wie `maintAbortPost`;
    - Benutzer über `maintSession`, Recht `maintMayConfirm` bzw. `maintIsAdmin`, ohne Recht 403;
    - Name wie in `maintAbortPost`;
    - Aufruf von `maintConfirm` bzw. `maintAbort`;
    - Protokoll über `componentLog("wartung")`;
    - danach `h.maintTick`;
    - Antwort: JSON (`maintWantsJSON`) `{"ok":true,"msg":…}` / `{"ok":false,"error":…}`, sonst `drRedirect(w, r, msg, err)`;
    - Meldungen „Update bestätigt – es beginnt in Kürze“ bzw. „Abbruch angefordert“.
  - `maintStatus`: Sitzung wie in `maintSession` lesen, die Zusatzfelder nur mit `can_confirm || can_abort`.

- [ ] **Step 4: Tests laufen lassen**
  Run: `go test ./internal/web/ -run 'Status|ConfirmPost|AbortPost|TestLock|Maint'`
  Expected: PASS

- [ ] **Step 5: Commit**
  `git add internal/web/maint_lock.go internal/web/maint_lock_test.go && git commit -m "PDH: Update vor der Sperre bestätigen, absagen oder abbrechen"`

---

### Task 4: PDH – Band und Fortschrittskasten

**Files:**
- Modify: `web/templates/base.gohtml` (Band-Markup Z. ~1099, CSS Z. ~231–239, Skript Z. ~3004–3044)
- Modify: `web/templates/widgets/ha.gohtml` (`maint-progress`, Aufruf Z. 100), `web/templates/widgets/dr.gohtml` (Aufruf Z. 8)
- Modify: `internal/web/ha.go` (`haView`: `CanConfirmUpdate`, `CanAbortUpdate`, Methode `UpdateBox() maintBox`; dort setzen, wo `haView` für den Reiter `dr` gebaut wird), `internal/web/maint_window.go` (`maintSingle.JobBox() maintBox` mit beiden Rechten false)
- Test: `internal/web/maint_lock_test.go` bzw. vorhandener Vorlagen-Test für `server_config`

**Interfaces:**
- Consumes (Task 3): `maintBox`, `/wartung/status`-Felder, POST `/wartung/durchfuehren` und `/wartung/abbrechen` (JSON).

- [ ] **Step 1: Failing test schreiben**: `TestMaintProgressButtons` rendert `maint-progress` mit
  - `maintBox{UpdateView{Active:true, Phase:"nachfrage", AskUntil:…}, CanConfirm:true, CanAbort:true}` → HTML enthält `action="/wartung/durchfuehren"` und `action="/wartung/abbrechen"`;
  - `CanConfirm:false` → kein `durchfuehren`;
  - `Phase:"countdown", CanAbort:true` → Abbrechen ja, Durchführen nein;
  - `Phase:"gesperrt"` → keiner der Knöpfe.

- [ ] **Step 2: Test laufen lassen, er schlägt fehl**
  Run: `go test ./internal/web/ -run TestMaintProgressButtons`
  Expected: FAIL

- [ ] **Step 3: Umsetzen**
  - `maint-progress` bekommt `maintBox`. Beide Aufrufe: `{{template "maint-progress" .UpdateBox}}` bzw. `.Job.JobBox` (Methode auf `maintSingle`).
  - In `nachfrage` die Zeile „Wartungsfenster: Update auf {{.ToVersion}} durchführen? Noch bis HH:MM“. Dazu Formulare mit `onsubmit="return confirm(…)"`: **Durchführen** („Update jetzt durchführen?“) und **Absagen** („Update absagen?“).
  - In `vorbereiten`, `pruefen_vorher` und `countdown` Formular **Abbrechen** („Update abbrechen und zurücksetzen?“).
  - Band:
    - Markup: Knopfbereich `<span class="mb-act"></span>` neben `#maint-band-text`;
    - Skript, Nachfrage: wenn `v.phase === 'nachfrage' && v.ask_until`, Text `{{t "Wartungsfenster: Update auf %v durchführen? Noch %s min"}}`, Knöpfe nach `can_confirm`/`can_abort`;
    - Skript, Countdown: zusätzlich **Abbrechen** bei `can_abort`;
    - Klick: `confirm(…)`, dann `fetch(POST, {headers:{Accept:'application/json'}})`, Antwort `msg`/`error` im Band anzeigen, danach `poll()`;
    - Abfrage in Nachfrage und Countdown alle 2 s, sonst 15 s, wie bisher.
  - CSS: `.maint-band .mb-act{margin-left:auto;display:inline-flex;gap:6px}` mit `btn btn-small`, Farben nur über Variablen.

- [ ] **Step 4: Tests und Bau**
  Run: `go test ./internal/web/ && go build ./...`
  Expected: PASS, Bau ohne Fehler

- [ ] **Step 5: Browser-Prüfung**
  - Lokale Prüfumgebung mit Attrappen-Zustand `nachfrage` bzw. `countdown`.
  - Band und Kasten in Fiori und Fluent, jeweils hell und dunkel, bei 390 px und 1366 px.
  - Bildschirmfotos im Ledger vermerken.

- [ ] **Step 6: Commit**
  `git add web/templates internal/web && git commit -m "PDH: Nachfrage und Abbrechen im Wartungs-Band und im Fortschrittskasten"`

---

### Task 5: Handbuch, Skizze, Änderungsprotokoll

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `ausfallschutz`, Abschnitt Wartungsfenster um Z. 1938; Kapitel Administration Z. ~1632)
- Modify: `web/templates/widgets/help_figures.gohtml` (neue Skizze „Band mit Nachfrage“)
- Modify: `CHANGELOG.md` (oberster Abschnitt `0.77.8`)

- [ ] **Step 1: Prüfen, ob 0.77.8 noch unveröffentlicht ist**
  Run: `git fetch origin main && git show origin/main:VERSION`
  Expected: nicht `0.77.8`. Sonst nächste Korrekturversion anlegen (`VERSION` + neuer Abschnitt oben).

- [ ] **Step 2: Texte schreiben**
  - **Handbuch, Kapitel „Ausfallschutz“:**
    - `.hb-howto` „Nachfrage beantworten“ mit `.hb-steps`: Band erscheint → **Durchführen** oder **Absagen** → ohne Antwort entfällt das Update nach 3 Minuten und wird am nächsten Termin wieder versucht.
    - Satz: „Bei Automatik an spielt das Fenster ohne Nachfrage ein. Vor der Sperre lässt sich das Update mit Abbrechen stoppen.“
    - Satz: „Update installieren funktioniert auch bei Automatik aus.“
    - Skizze einbinden.
  - **Kapitel „Administration“:** „Automatische Wartung abbrechen“ umfasst jetzt auch Absagen und Abbrechen vor der Sperre. Durchführen braucht dasselbe Recht wie „Update installieren“.
  - **CHANGELOG:**
    - `### Neu`: „Bei „Automatik aus (Wartung)“ fragt das Wartungsfenster vor dem Update nach; ohne Bestätigung innerhalb von 3 Minuten entfällt das Update.“
    - `### Neu`: „Ein laufendes Update lässt sich vor der Sperre im Wartungs-Band und unter Ausfallschutz abbrechen.“
    - `### Geändert`: „„Update installieren“ funktioniert im Verbund auch bei „Automatik aus (Wartung)“.“
    - `### Neu`: „`pdh-ha update confirm` bestätigt die Nachfrage auf der Befehlszeile.“

- [ ] **Step 3: Tests**
  Run: `go test ./internal/web/ -run 'TestHelp|TestChangelog' && go test ./...`
  Expected: PASS

- [ ] **Step 4: Commit**
  `git add web/templates/help.gohtml web/templates/widgets/help_figures.gohtml CHANGELOG.md && git commit -m "Handbuch und Änderungsprotokoll: Nachfrage im Wartungsfenster, Abbrechen vor der Sperre"`

---

### Abschluss (nach Task 5)

- Gesamtprüfung: `go test ./...`, `go build ./...`, `go vet ./...`.
- Abnahme auf den Testservern nach Spec Abschnitt 9 (5 Fälle). Nie 10.10.0.202.
- Prüfung vor dem Merge laut AGENTS.md. Push und Merge nur mit Freigabe.
