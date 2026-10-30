# Update im Verbund und Wartungsfenster – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein Auslöser (Knopf oder Wartungsfenster) bringt alle Rechner des Verbunds bzw. den Einzelserver auf denselben Commit, mit Countdown, Wartungssperre und Wartungsbildschirm, und setzt bei jedem Fehler ohne Datenverlust auf den alten Stand zurück.

**Architecture:** pdh-ha auf dem aktiven Server dirigiert einen gespiegelten Auftrag (reine Zustandsmaschine `ha.UpdateStep`), der Update-Agent jedes Rechners führt aus (`install_update.sh --commit/--rollback`). Die Rückfallkopie ist die gestoppte Datenbank des Servers, der die neue Version noch nicht ausführt. Das PDH sperrt während der kritischen Phase alle Routen (Middleware ganz vorn) und zeigt den Wartungsbildschirm. Auf dem Einzelserver dirigiert der Agent selbst.

**Tech Stack:** Go 1.24 (chi, pgx), Bash, PostgreSQL (pg_ctlcluster, pg_rewind, pg_dump), Go-Templates, Docker Compose (Zeuge, Labor).

**Spec:** `docs/superpowers/specs/2026-10-30-verbund-update-design.md` (Abschnittsnummern unten = Spec-Abschnitte)

## Global Constraints

- Sprache: Code-Kommentare, Texte, Commits, Handbuch, Changelog auf Deutsch, für Anwender verständlich (AGENTS.md).
- Jeder neue Oberflächentext über `{{t "…"}}` bzw. `tr(...)`; Einträge in `web/i18n/{en,ro,tr,mk}.json` im selben Task (`TestI18nCatalogsComplete`).
- Oberfläche in Fiori und Fluent, hell/dunkel; Farben nur über `--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`. Seiten mit eigenem Kopf setzen `document.documentElement.dataset.style` und haben eine Fiori-Variante.
- Neue Aktionen prüfen ein Recht; Rechte per Migration (Kategorie `'System & Sicherheit'`), nächste Nummer **154**.
- Standardwerte (Spec 4): Fenster aus; Beginn „nach dem Abgleich“; Uhrzeit 02:00; spätestens 05:00; einspielen ja; Art „ohne Unterbrechung“; Rollentausch aus; Countdown 2 min (1–15); Höchstdauer Sperre 30 min (10–120); Prüffrist 10 min; Mindestplatz 2 GB; Zeugen-Helfer alle 5 min, Prüffrist 2 min; „Zeuge veraltet“ nach 30 min.
- Update-Zweig `PDH_UPDATE_BRANCH`, Standard `main`.
- Docker-Betrieb des PDH (`PDH_UPDATE_MODE=docker`): Verhalten wie bisher, kein Auftrag, keine Sperre (nur Testsysteme).
- Produktion 10.10.0.202 wird nie als Testknoten benutzt; Merge nach main nur mit Freigabe des Nutzers.
- Version: `VERSION` 0.77.8, oberster `CHANGELOG.md`-Eintrag gleich (`TestChangelogMatchesVersion`).
- Vor jedem Commit: `go build ./...` und `go test ./<betroffene Pakete>/...` grün; am Ende `go test ./...`.

## Review Focus

1. **Sitzung eines Administrators über den Tausch hinweg:** Der Administrator bleibt angemeldet, wenn B aktiv wird (JWT hängt am gemeinsamen Geheimnis, `session.not_before` kommt aus der replizierten DB). Test in Task 9: `TestLockAdminKeepsSessionAfterRestart`.
2. **Schreibversuch während der Sperre:** darf nichts schreiben, auch nicht über `/api/v1` mit Bearer-Token, htmx oder Webhook. Test in Task 9: `TestLockRefusesWrites`.
3. **Neustart von pdh-ha mitten im Auftrag** (install_update.sh baut pdh-ha neu): Auftrag wird aus `update-job.json` geladen und setzt in derselben Phase fort, Fristen laufen ab gespeichertem `PhaseAt`. Tests: Task 2 `TestUpdateStepResumeAfterRestart`, Labor-Szenario `update-neustart`.
4. **GitHub nicht erreichbar oder Ziel nicht im Zweig:** Fenster meldet „Update-Zweig nicht erreichbar“; `install_update.sh --commit` bricht vor jeder Änderung ab. Tests: Task 6 `TestInstallScriptCommitGuard`, Task 7 `TestWindowSkipsWhenNoTarget`.
5. **Uhrumstellung / Wochentag:** „spätestens bis“ und Wochentag in lokaler Zeit des Servers. Test in Task 5: `TestNextWindowDST` (Nacht 25.10.2026, Europe/Berlin).

---

## Dateiübersicht

| Datei | Verantwortung |
|---|---|
| `internal/ha/update.go` (neu) | Auftrag, Phasen, `UpdateStep`, `UpdateReady`, `UpdateView` |
| `internal/ha/window.go` (neu) | `Window`, `WindowDue`, `NextWindow` (auch vom Agenten genutzt) |
| `internal/ha/step.go`, `types.go`, `sync.go` | `CmdSwapFreeze`/`CmdSwapAbandon`, `ActDemoteFreeze`/`ActAbandon`, `PeerObs.Commit`, `SyncIn.ForceAsync` |
| `cmd/pdh-ha/pgactor.go`, `rejoin.go` | Einfrieren, Aufgeben, Hochstufen aus gestopptem Zustand |
| `cmd/pdh-ha/update_job.go` (neu) | Auftrag laden/speichern/spiegeln, `UpdateAction`s ausführen |
| `cmd/pdh-ha/agent_client.go` (neu) | Lokaler Update-Agent (HTTP mit Token oder Labor-Befehl), `/health` des PDH |
| `cmd/pdh-ha/messages.go`, `transport.go`, `api.go`, `main.go`, `observe.go`, `store.go` | Nachrichten, API, CLI, Commit im Kern, Fenster in `clusterConfig` |
| `install_update.sh` | `--commit`, `--rollback`, `--no-restart`, `--finish`, `PDH_UPDATE_BRANCH` |
| `cmd/update-agent/update.go` (neu) | `/v1/update` mit Rumpf, gespeicherter Status |
| `cmd/update-agent/window.go`, `job.go` (neu) | Zeitplan im `drTick`; Einzelserver-Auftrag |
| `internal/web/maint_lock.go` (neu), `web/templates/maint_lock.gohtml` (neu) | Sperre, Sitzungs-Epoche, `/wartung/*`, Wartungsbildschirm |
| `web/templates/base.gohtml` | Countdown-Band |
| `internal/web/maint_window.go` (neu), `widgets/ha.gohtml`, `widgets/dr.gohtml`, `core_settings.*` | Kasten „Wartungsfenster“, Knopf, Fortschritt, „Zeuge veraltet“ |
| `cmd/server/main.go`, `mirror_start.go` | Middleware, Start gesperrt, Sicherung auslassen, Spiegel „Programm zu alt“, `/health` |
| `migrations/154_verbund_update.up.sql` (neu) | Rechte |
| `deploy/ha-witness/*` | Helfer `updater` |
| `deploy/ha-lab/*` | Labor-Agent, Schalter, Szenarien |
| `help.gohtml`, `help_figures.gohtml`, `help.go`, `CHANGELOG.md`, `VERSION`, Spec 2026-10-24 | Handbuch, Version |

---

### Task 1: Commit im Kern, Tausch mit Einfrieren/Aufgeben (`internal/ha`)

**Files:**
- Modify: `internal/ha/types.go` (`PeerObs` :186, Aktionen :245, `CommandKind` :268, `Input` :279)
- Modify: `internal/ha/step.go` (`decide` :116, `swap` :253)
- Modify: `internal/ha/sync.go` (`SyncIn` :6, `SyncStep` :30)
- Modify: `cmd/pdh-ha/observe.go` (`obsFrom` :19 übernimmt `m.Commit`; `inputLocked` :32 setzt `Input.Commit = d.commit`)
- Test: `internal/ha/step_test.go`, `internal/ha/sim_test.go`, `internal/ha/sync_test.go`

**Interfaces:**
- Produces: `PeerObs.Commit string`; `Input.Commit string`; `CmdSwapFreeze CommandKind = "swap_freeze"`, `CmdSwapAbandon CommandKind = "swap_abandon"`; `ActDemoteFreeze`, `ActAbandon` (ActionKind); `SyncIn.ForceAsync bool` → `SyncStep` liefert leeres Sync-Ziel, solange gesetzt.
- Semantik: `swap()` wird `swap(k ActionKind)`; `CmdSwap`→`ActDemote`, `CmdSwapFreeze`→`ActDemoteFreeze`, `CmdSwapAbandon`→`ActAbandon`. VIP abgeben, `HandoverPending`, Warten auf `DemoteDone`, `ActHandover` bleiben identisch.

- [ ] **Step 1: Failing tests**
  - `TestSwapFreezeEmitsDemoteFreeze`: aktiver Knoten, Partner gesehen+passiv, `Command: CmdSwapFreeze` → Aktionen enthalten `ActDemoteFreeze` und `ActVIPRelease`, kein `ActDemote`; Rolle danach `Passive`; `HandoverPending == Term+1`.
  - `TestSwapAbandonEmitsAbandon`: dasselbe mit `CmdSwapAbandon` → `ActAbandon`.
  - `TestSyncForceAsync`: Standby verbunden, `ForceAsync: true` → Sync-Ziel `""`.
  - Simulator: `"tausch-einfrieren"` (a: `CmdSwapFreeze`, 15 Takte → `want("a", Passive, 2)`, `want("b", Active, 2)`) und `"ruecktausch-aufgeben"` (danach b: `CmdSwapAbandon` → `want("a", Active, 3)`, `want("b", Passive, 3)`); `nodeStep` setzt `demoteDone` auch für die neuen Aktionen.
- [ ] **Step 2:** `go test ./internal/ha/ -run 'Swap|ForceAsync|tausch' -v` → FAIL (undefinierte Namen).
- [ ] **Step 3:** umsetzen; der Daemon (`replSyncLocked`, repl.go:101) reicht `ForceAsync` aus Task 4 durch (bis dahin `false`).
- [ ] **Step 4:** `go test ./internal/ha/... ./cmd/pdh-ha/...` → PASS (alle bestehenden Szenarien grün).
- [ ] **Step 5:** Commit `pdh-ha: Tausch mit Einfrieren und Rücktausch im Kern, Commit im Status`.

---

### Task 2: Zustandsmaschine des Auftrags (`internal/ha/update.go`)

**Files:**
- Create: `internal/ha/update.go`, `internal/ha/update_test.go`

**Interfaces:**
- Produces (exakt so verwenden):

```go
type UpdateKind string   // "rolling" (ohne Unterbrechung) | "sequential" (alle nacheinander) | "swap" (nur Rollentausch)
type UpdatePhase string
const (
    UpNone UpdatePhase = ""; UpPrepare UpdatePhase = "vorbereiten"; UpVerifyPrep UpdatePhase = "pruefen_vorher"
    UpCountdown UpdatePhase = "countdown"; UpLocked UpdatePhase = "gesperrt"; UpVerifyNew UpdatePhase = "pruefen_neu"
    UpRelease UpdatePhase = "freigabe"; UpFollow UpdatePhase = "nachziehen"
    UpRollback UpdatePhase = "zuruecksetzen"; UpDone UpdatePhase = "fertig"; UpFailed UpdatePhase = "abgebrochen"
)
type UpdateJob struct {
    ID string; Kind UpdateKind; Source string           // Source: "knopf" | "zeitplan"
    Target, TargetVersion, OldVersion string
    Old map[string]string                                // Knoten-ID → Alt-Commit
    First, Second string                                 // First führt X zuerst aus und ist danach aktiv (rolling: B, sequential: A)
    Phase UpdatePhase; PhaseAt, StartedAt, CountdownUntil, LockedAt, ReleasedAt time.Time
    Attempts int; Result string; MarkFaulty bool; AbortedBy string; Seq uint64
}
type UpdateAck struct{ JobID, Op, Node string; OK bool; Err string; Seq uint64 }
type UpdateObs struct {
    Now time.Time; Self string; SelfRole Role; SelfCommit string
    PartnerID string; Partner PeerObs
    SelfPDHOK, PartnerPDHOK bool   // /health: erwartete Version, migrations_ok
    MirrorSync bool
    Ack *UpdateAck
    Countdown, LockMax, Verify time.Duration
    Abort string                   // Name des Abbrechenden, "" = keiner
    Takeover bool                  // echte Übernahme/Hand-Übernahme seit Start
}
type UpdateActionKind string // "node_op" | "local_op" | "command" | "event"
type UpdateAction struct {
    Kind UpdateActionKind
    Node, Op, Commit string        // Op: "install" | "install_norestart" | "rollback" | "freeze" | "thaw" | "reinit"
    Command CommandKind
    EventKind, Text string
}
func UpdateStep(job UpdateJob, o UpdateObs) (UpdateJob, []UpdateAction)
func (j UpdateJob) Locked() bool   // Countdown vorbei und noch nicht freigegeben (LockedAt gesetzt, ReleasedAt leer)

type ReadyObs struct {
    Mode Mode; SelfRole Role; PartnerSeen bool; PartnerRole Role; SameTerm bool
    Maint, Busy, JobOpen bool
    SelfCommit, PartnerCommit string; Resume bool
    MirrorSync bool; LagBytes, LagLimit int64
    BackupConfigured, LastBaseOK, LastFilesOK bool
    FreeSelf, FreePartner int64
    WitnessExpected, WitnessSeen bool
}
func UpdateReady(o ReadyObs) (ok bool, reason string, hints []string)

type UpdateView struct {
    Active bool `json:"active"`; Kind string `json:"kind"`; Phase string `json:"phase"`
    Steps []string `json:"steps"`; Step int `json:"step"`; Percent int `json:"percent"`
    Text string `json:"text"`; RemainingS int `json:"remaining_s"`
    Locked bool `json:"locked"`; CountdownUntil time.Time `json:"countdown_until"`
    FromVersion string `json:"from_version"`; ToVersion string `json:"to_version"`
    RollingBack bool `json:"rolling_back"`; Result string `json:"result"`; Faulty []string `json:"faulty"`
}
func (j UpdateJob) View(now time.Time, lockMax time.Duration, faulty []string) UpdateView
```

- Übergänge (Spec 5.1, 5.2, 5.4, 7) – jede Zeile ist ein Fall in `TestUpdateStepTable`:

| Phase | Bedingung | Neue Phase | Aktionen |
|---|---|---|---|
| Prepare (rolling), Eintritt | – | Prepare | `node_op install X` an First |
| Prepare (sequential), Eintritt | – | Prepare | `local_op install_norestart X` |
| Prepare | Ack ok | VerifyPrep | – |
| Prepare/VerifyPrep | Ack fehlerhaft oder `Now-PhaseAt > Verify` | Failed | rolling: `node_op rollback Old[First]`; Event alarm; `MarkFaulty=true` |
| VerifyPrep | rolling: `Partner.Commit==X && PartnerPDHOK`; sequential: sofort | Countdown | `CountdownUntil=Now+Countdown` |
| Countdown | `Now>=CountdownUntil` | Locked | `LockedAt=Now`; rolling: `command CmdSwapFreeze`; sequential: `node_op freeze` an Second, danach (nächster Schritt nach Ack) `local_op install X` |
| Locked | rolling: `Self==First && SelfRole==Active`; sequential: `SelfCommit==X` | VerifyNew | – |
| VerifyNew | `SelfPDHOK` | Release | `node_op thaw` an Second |
| VerifyNew | `Now-PhaseAt > Verify` | Rollback | `command CmdSwapAbandon`; `MarkFaulty=true`; Event alarm |
| Release | `MirrorSync` | Follow | `ReleasedAt=Now`; rolling: `node_op install X` an Second; sequential: `node_op install X` an Second |
| Follow | Ack ok und `Partner.Commit==X && PartnerPDHOK` | Done | Event info „Update auf <TargetVersion> abgeschlossen“ |
| Follow | Ack fehlerhaft, `Attempts==0` | Follow | `Attempts=1`, erneut `install` |
| Follow | Ack fehlerhaft, `Attempts>=1` | Done | `node_op reinit X` an Second; Event alarm |
| Rollback | Self ist aktiv (nach Rücktausch) und noch kein Rollback-Auftrag an Partner | Rollback | `node_op rollback Old[Partner]` (Partner: rejoin, dann Alt-Commit) |
| Rollback | Ack ok und `MirrorSync` | Failed | `ReleasedAt=Now`; Event alarm mit Grund |
| Locked/VerifyNew/Rollback | `Now-LockedAt > LockMax` | Rollback (bzw. Failed, wenn schon Rollback) | wie VerifyNew-Fehler; Event alarm „Höchstdauer überschritten“; im Rollback: `ReleasedAt=Now` (Sperre endet), dringender Alarm |
| vor Locked | `Abort!=""` | Failed | rolling: `node_op rollback Old[First]`; `AbortedBy=Abort` |
| Locked/VerifyNew | `Abort!=""` | Rollback | wie VerifyNew-Fehler, aber `MarkFaulty=false`, `AbortedBy=Abort` |
| jede außer Done/Failed | `Takeover` | Failed | Event alarm „Auftrag abgebrochen: Übernahme“ |
| Kind swap | Prepare/VerifyPrep entfallen; Countdown → Locked mit `command CmdSwap`; VerifyNew: neuer Aktiver `SelfPDHOK` → Done | | |

- `View`: Schritte `["Abmelden","Rollentausch","Datenbank aktualisieren","Prüfen","Freigabe"]` (sequential: „Neustart“ statt „Rollentausch“); `Step`/`Percent` je Phase: Countdown 0/5, Locked 1/20, VerifyNew 3/60, Release 4/90, Follow/Done 4/100, Rollback `RollingBack=true`; `RemainingS = LockMax-(Now-LockedAt)` (≥0); Texte je Phase im Test festhalten.

- [ ] **Step 1:** `TestUpdateStepTable` (eine Zeile je Tabellenzeile, Phase und Aktionen verglichen), `TestUpdateStepResumeAfterRestart` (Job in `VerifyNew`, `PhaseAt` vor 11 min → Rollback), `TestUpdateReady` (je ein Fall pro Spiegelstrich Spec 6; Zeuge fehlt → ok mit Hinweis; Einzelbetrieb: nur Platz und Grundsicherung), `TestUpdateViewLocked`.
- [ ] **Step 2:** `go test ./internal/ha/ -run Update -v` → FAIL.
- [ ] **Step 3:** umsetzen (reine Funktionen, keine Uhr, kein I/O).
- [ ] **Step 4:** `go test ./internal/ha/...` → PASS.
- [ ] **Step 5:** Commit `pdh-ha: Zustandsmaschine für das Update im Verbund`.

---

### Task 3: Einfrieren, Aufgeben, Hochstufen aus gestopptem Zustand (`pgActor`)

**Files:**
- Modify: `cmd/pdh-ha/pgactor.go` (`Do` :96, `demote` :121, `promote` :177), `cmd/pdh-ha/rejoin.go` (`rejoin` :181), `cmd/pdh-ha/daemon.go` (`actionWorker` :671)
- Test: vorhandene pgActor-Tests mit Attrappen-Runner (`cmd/pdh-ha/primary_test.go` o. ä.)

**Interfaces:**
- Consumes: `ActDemoteFreeze`, `ActAbandon` (Task 1).
- Produces:
  - `func (a *pgActor) demoteFreeze(ctx context.Context) error` = `demote` ohne `becomeStandby`/`start` (fence, `waitCaughtUp`, `ctl stop`).
  - `func (a *pgActor) abandon(ctx context.Context) error` = `stopServices`, `ctl stop` ohne `waitCaughtUp`.
  - `func (a *pgActor) freeze(ctx context.Context) error` = Standby: `ctl stop` (Daten bleiben, `standby.signal` bleibt).
  - `promote`: ist der Cluster gestoppt, zuerst `ctl start`; ist er danach Primary (kein `standby.signal`), `pg_promote` auslassen; sonst wie bisher.
  - Auftauen = vorhandenes `rejoin(ctx, term)` (Probelauf von pg_rewind entscheidet, ob zurückgespult werden muss).
  - `actionWorker`: nach `ActDemoteFreeze`/`ActAbandon` wie nach `ActDemote` `d.demoteDone`/`d.demoteFailed` setzen.

- [ ] **Step 1:** Tests: `TestDemoteFreezeStopsWithoutStandby` (Befehlsfolge enthält `stop`, keine `standby.signal`, kein `start`), `TestAbandonSkipsCatchUp`, `TestFreezeStopsStandby`, `TestPromoteFromStoppedPrimary` (→ `start`, kein `pg_promote`), `TestPromoteFromStoppedStandby` (→ `start`, `pg_promote`).
- [ ] **Step 2:** `go test ./cmd/pdh-ha/ -run 'Freeze|Abandon|PromoteFromStopped' -v` → FAIL.
- [ ] **Step 3:** umsetzen; `observeActor` protokolliert die neuen Aktionen wie die anderen.
- [ ] **Step 4:** `go test ./cmd/pdh-ha/...` → PASS.
- [ ] **Step 5:** Commit `pdh-ha: Datenbank einfrieren und Rücktausch ohne Datenverlust`.

---

### Task 4: Auftrag im Daemon, Nachrichten, Agenten-Aufruf

**Files:**
- Create: `cmd/pdh-ha/update_job.go`, `cmd/pdh-ha/agent_client.go`, `cmd/pdh-ha/update_job_test.go`
- Modify: `cmd/pdh-ha/transport.go` (`message` :51, `deliverTypes` :100), `messages.go` (`handle` :15), `daemon.go` (`tickNodeLocked` :874), `observe.go` (`statusLocked` :152), `repl.go` (`replSyncLocked` :101), `store.go`, `api.go` (`apiStatus` :39)

**Interfaces:**
- Consumes: `UpdateStep`, `UpdateJob`, `UpdateAction`, `UpdateAck` (Task 2); `CmdSwapFreeze`/`CmdSwapAbandon` (Task 1); `freeze`, `rejoin` (Task 3).
- Produces:
  - `message`-Felder `Job *ha.UpdateJob \`json:"job,omitempty"\``, `Op string \`json:"op,omitempty"\``, `JobID string \`json:"job_id,omitempty"\``, `Ack *ha.UpdateAck \`json:"ack,omitempty"\``; Arten `update_job`, `update_node`, `update_ack` in `deliverTypes`.
  - `func (s store) loadJob() (ha.UpdateJob, error)` / `saveJob(ha.UpdateJob) error` → `<state>/update-job.json`; `loadFaulty() ([]string, error)` / `saveFaulty([]string) error` → `<state>/update-faulty.json` (atomar über `writeFileAtomic`).
  - `type agentClient interface { Install(ctx context.Context, commit string, noRestart bool) error; Rollback(ctx context.Context, commit string) error; Finish(ctx context.Context) error }`
  - `func newAgentClient() agentClient`: `PDH_HA_AGENT_CMD` gesetzt (Labor) → Befehl mit `install|install_norestart|rollback|finish <commit>`; sonst HTTP an `PDH_UPDATE_AGENT_URL` (Standard `http://127.0.0.1:8091`) mit Token `PDH_UPDATE_AGENT_TOKEN`, beide aus `/opt/pdh-server/.env` (Pfad über `PDH_HA_PDH_ENV`); `POST /v1/update {"commit","rollback","no_restart","finish"}`, danach `GET /v1/status` pollen bis `running=false` (Frist 20 min), Ergebnis `success`.
  - `func pdhHealthOK(ctx context.Context, wantVersion string) bool`: `GET http://127.0.0.1:<PDH_SERVER_PORT, Standard 8090>/health` (Port aus derselben .env), ok bei 200, `version==wantVersion` und `migrations_ok==true` (Feld aus Task 8; fehlt es, zählt nur die Version).
  - Im Takt, aktiver Server mit offenem Auftrag: `UpdateObs` bauen (`Countdown`/`LockMax` aus `clusterConfig.Window`, `Verify` 10 min), `UpdateStep`, Job speichern und bei gestiegenem `Seq` `update_job` an den Partner, Aktionen ausführen: `node_op` → `update_node`; `local_op` → Agent über `enqueueFnLocked` (repl.go:245), Ergebnis als lokales `UpdateAck`; `command` → `d.cmd`; `event` → `d.event`. `MarkFaulty` → Commit in `update-faulty.json`.
  - Passiver Server übernimmt `update_job` nur mit höherem `Seq`; wird er aktiv, dirigiert er weiter.
  - `update_node` beim Empfänger (nur vom Verbund-Partner, `kindOfLocked`): `install`/`install_norestart`/`rollback` → Agent (bei `rollback` vorher `rejoin`, falls die eigene Datenbank abgezweigt ist); `freeze` → `pgActor.freeze`; `thaw` → `rejoin`; `reinit` → vorhandener `replicaInit`-Ablauf, danach Agent `install X`. Danach `update_ack`.
  - `SyncIn.ForceAsync = job.Locked()` in `replSyncLocked`.
  - `apiStatus` bekommt `Update ha.UpdateView \`json:"update"\`` und `Faulty []string \`json:"faulty"\``.

- [ ] **Step 1:** Tests: `TestUpdateJobRoundTrip`, `TestUpdateNodeOpCallsAgent` (Attrappe `agentClient`: eingehendes `update_node install X` → `Install(X,false)`, dann `update_ack ok`), `TestUpdateJobMirrorHigherSeq`, `TestAgentClientCmd` (Testskript protokolliert Argumente), `TestAgentClientHTTP` (httptest: Bearer und Rumpf), `TestPDHHealthOK`, `TestForceAsyncWhileLocked`.
- [ ] **Step 2:** `go test ./cmd/pdh-ha/ -run 'UpdateJob|UpdateNode|AgentClient|PDHHealth|ForceAsync' -v` → FAIL.
- [ ] **Step 3:** umsetzen.
- [ ] **Step 4:** `go test ./cmd/pdh-ha/... && go test -race ./cmd/pdh-ha/` → PASS.
- [ ] **Step 5:** Commit `pdh-ha: Update-Auftrag spiegeln und auf allen Servern ausführen`.

---

### Task 5: Fenster-Einstellungen, API und CLI in pdh-ha

**Files:**
- Create: `internal/ha/window.go`, `internal/ha/window_test.go`
- Modify: `cmd/pdh-ha/api.go` (Router :208, `updateSettings` :556 als Vorbild), `cmd/pdh-ha/main.go` (`runCLI` :68, `usage` :26), `cmd/pdh-ha/store.go` (`clusterConfig` :42)
- Test: `cmd/pdh-ha/api_test.go`, `cmd/pdh-ha/main_test.go`

**Interfaces:**
- Produces:

```go
type Window struct {
    Mode string       `json:"mode"`          // "off" | "daily" | "weekly"
    Weekday int       `json:"weekday"`       // 0=So … 6=Sa
    Begin string      `json:"begin"`         // "before_files" | "after_files"
    Clock string      `json:"clock"`         // "HH:MM", nur ohne Grundsicherung
    Latest string     `json:"latest"`        // "HH:MM"
    Install bool      `json:"install"`
    Kind UpdateKind   `json:"kind"`          // "rolling" | "sequential"
    Swap bool         `json:"swap"`
    CountdownMin int  `json:"countdown_min"`
    LockMaxMin int    `json:"lock_max_min"`
}
func DefaultWindow() Window
func (w Window) Validate() error
type WindowState struct{ HasBackup, BaseDoneToday, FilesStartedAfterBase, FilesDoneAfterBase bool }
func WindowDue(w Window, st WindowState, lastRun, now time.Time) (due bool, skipReason string)
func NextWindow(w Window, baseHour int, hasBackup bool, now time.Time) time.Time
```

  - `clusterConfig.Window ha.Window \`json:"window"\`` (verteilt wie `Settings` über `publishClusterLocked`); `PUT /v1/window` (gleiche Rechteprüfung wie `updateSettings`); `apiStatus.Settings`-Ausgabe enthält `window`.
  - `GET /v1/update` → `ha.UpdateView`.
  - `POST /v1/update/start {"commit","source","kind","force"}`: prüft `UpdateReady`; nicht grün → 409 `{"ok":false,"error":<Grund>,"hints":[…]}`; `force:true` nur mit `source:"knopf"`, setzt `kind="sequential"`; Commit in `faulty` und `source:"zeitplan"` → 409 „Version als fehlerhaft gemerkt“.
  - `POST /v1/update/abort {"by"}`; `DELETE /v1/update/faulty/{commit}`.
  - CLI: `pdh-ha update status`, `pdh-ha update abort`, `pdh-ha update start <commit> [--kind rolling|sequential]` (+ Zeilen im `usage`).

- [ ] **Step 1:** Tests: `TestWindowValidate` (1–15, 10–120, Uhrzeitformat), `TestWindowDue` (vor/nach Abgleich; „spätestens“ überschritten → `skipReason`; heute schon gelaufen → false; wöchentlich anderer Tag → false; ohne Sicherung ab `Clock`), `TestNextWindowDST`, `TestAPIUpdateStartNotReady`, `TestAPIUpdateStartFaulty`, `TestAPIUpdateAbort`, `TestCLIUpdateStatus`.
- [ ] **Step 2:** `go test ./internal/ha/ ./cmd/pdh-ha/ -run 'Window|APIUpdate|CLIUpdate' -v` → FAIL.
- [ ] **Step 3:** umsetzen.
- [ ] **Step 4:** `go test ./internal/ha/... ./cmd/pdh-ha/...` → PASS.
- [ ] **Step 5:** Commit `pdh-ha: Schnittstelle und Befehle für Update und Wartungsfenster`.

---

### Task 6: `install_update.sh` mit Ziel, Zurückdrehen, Update-Zweig; Agent `/v1/update`

**Files:**
- Modify: `install_update.sh` (Z.46–56 fetch/merge, Z.102–137 Bau/Neustart)
- Create: `cmd/update-agent/update.go`, `cmd/update-agent/update_test.go`
- Modify: `cmd/update-agent/main.go` (`jobStatus` :21, `updateHandler` :147, `runUpdate` :187), `internal/web/core_settings.go` (`checkGitHubUpdate` :373 liest `PDH_UPDATE_BRANCH`)

**Interfaces:**
- Produces:
  - `install_update.sh [--commit <sha>] [--rollback <sha>] [--no-restart] [--finish]`:
    - `branch="${PDH_UPDATE_BRANCH:-main}"`; `git fetch --prune origin "$branch"`.
    - `--commit`: `git merge-base --is-ancestor <sha> FETCH_HEAD`, sonst Abbruch „Ziel nicht im Update-Zweig“ vor jeder Änderung; dann `merge --ff-only <sha>`.
    - `--rollback`: `git cat-file -e <sha>` sonst Abbruch; `git reset --hard <sha>`; danach Bau und Neustart wie normal.
    - `--no-restart`: nur bauen (`bin/pdh`, `bin/update-agent`; pdh-ha nach `bin/pdh-ha.neu` über `install-pdh-ha.sh --build-only`), keine Dienste anfassen.
    - `--finish`: gebaute Dateien einsetzen ohne erneuten Bau (pdh-Neustart gemäß `should-run-pdh`, pdh-ha installieren und neu starten, Agent ersetzen).
    - Ohne Schalter: Verhalten wie heute, nur Zweig über `PDH_UPDATE_BRANCH`.
  - `scripts/install-pdh-ha.sh --build-only` (nur Bau nach `bin/pdh-ha.neu`) und `--install-built` (fertige Datei einsetzen, Dienst neu starten).
  - `POST /v1/update` nimmt optional `{"commit","rollback","no_restart","finish"}`; ungültiger SHA (`^[0-9a-f]{7,40}$`) → 400.
  - `jobStatus` mit Feld `Commit string \`json:"commit"\``, gespeichert in `/var/lib/pdh-dr/update-status.json` (über `drStateDir()`), beim Start geladen.

- [ ] **Step 1:** Tests: `TestInstallScriptCommitGuard` (`merge-base --is-ancestor` steht vor `merge --ff-only`; `drBashSyntax`), `TestInstallScriptBranch`, `TestInstallScriptRollbackGuard` (`cat-file -e` vor `reset --hard`), `TestUpdateHandlerBody` (Rumpf → Argumente `--commit <sha>`; ungültig → 400), `TestUpdateStatusPersisted`; `TestFullNodeScripts` bleibt grün.
- [ ] **Step 2:** `go test ./cmd/update-agent/ -run 'InstallScript|UpdateHandler|UpdateStatus' -v` → FAIL.
- [ ] **Step 3:** umsetzen; `runUpdate(args []string)`.
- [ ] **Step 4:** `go test ./cmd/update-agent/... ./internal/web/ -run 'Update|GitHub'` → PASS.
- [ ] **Step 5:** Commit `Update-Agent: bestimmten Stand einspielen und zurückdrehen`.

---

### Task 7: Zeitplan im Agenten und Einzelserver-Auftrag

**Files:**
- Create: `cmd/update-agent/window.go`, `cmd/update-agent/job.go`, `cmd/update-agent/window_test.go`, `cmd/update-agent/job_test.go`
- Modify: `cmd/update-agent/dr_files.go` (`drTick` :212, Dateiabgleich :276), `cmd/update-agent/main.go` (Router :84–109)

**Interfaces:**
- Consumes: `ha.Window`, `ha.WindowDue`, `ha.NextWindow`, `ha.UpdatePhase`, `ha.UpdateView`, `ha.UpdateReady`; pdh-ha `POST /v1/update/start` (Task 5); `install_update.sh`-Schalter (Task 6).
- Produces:
  - `windowState{Window ha.Window; LastRun time.Time; LastResult string; Faulty []string}` in `<drStateDir>/window.json` (nur Einzelserver; im Verbund kommt `Window` aus `GET /v1/status` → `settings.window`).
  - `func (a *updateAgent) windowTick(ctx context.Context, now time.Time)` aus `drTick` (auch bei DR aus, dann `HasBackup=false`); Beginn „vor dem Abgleich“ und Fenster fällig → in diesem Takt kein `drStartRun("files")`, bis das Fenster beendet ist.
  - Ziel: `git ls-remote origin refs/heads/$PDH_UPDATE_BRANCH` im `repoDir`; gleich installiertem Commit oder in `Faulty` → nichts; nicht erreichbar → `LastResult` „Update-Zweig nicht erreichbar“.
  - Verbund (`drHASelf` aktiv): `POST /v1/update/start` an pdh-ha mit `source:"zeitplan"`; `Install=false, Swap=true` → `kind:"swap"` ohne Commit; 409 → `LastResult` = Grund (Meldung kommt als pdh-ha-Ereignis).
  - Einzelserver (systemd): `singleJob` gespeichert in `<drStateDir>/update-job.json` mit Phasen Prepare (`install_update.sh --commit X --no-restart`), Countdown, Locked (`psql`: `app_settings update.backup_skip_commit = X` und `session.not_before = now`; `pg_dump -Fc` nach `<drStateDir>/vor-update-<X>.dump`; `install_update.sh --finish`), VerifyNew (`/health`, Frist 10 min), Release. Fehler → Rollback (pdh stoppen, `pg_restore --clean --if-exists`, `install_update.sh --rollback <Alt>`, prüfen), X nach `Faulty`. Datenbank aus `DATABASE_URL` in `<repoDir>/.env`. Fortschritt als `ha.UpdateView` (gleiche Schritte, „Neustart“).
  - Routen (Token): `GET /v1/update/job` → `ha.UpdateView`; `POST /v1/update/job/start {"force":bool}`; `POST /v1/update/job/abort {"by"}`; `GET|POST /v1/window`; `DELETE /v1/window/faulty/{commit}`.

- [ ] **Step 1:** Tests: `TestWindowTickHandsToHA` (`haFakeSocket` mit Verbund-Status; fällig → POST `/v1/update/start` mit `source:"zeitplan"`), `TestWindowSkipsWhenNoTarget`, `TestWindowSkipsFaulty`, `TestWindowHoldsFileSync`, `TestSingleJobRollbackOnHealthFail` (Attrappen für Skript, psql, pg_dump/pg_restore; Befehlsfolge geprüft), `TestSingleJobPersistsAcrossRestart`.
- [ ] **Step 2:** `go test ./cmd/update-agent/ -run 'Window|SingleJob' -v` → FAIL.
- [ ] **Step 3:** umsetzen (Host-Befehle über `a.haRunFn()`/`hostCommandEnv` wie die DR-Läufe).
- [ ] **Step 4:** `go test ./cmd/update-agent/... && go test -race ./cmd/update-agent/` → PASS.
- [ ] **Step 5:** Commit `Update-Agent: Wartungsfenster und Update mit Rückweg auf dem Einzelserver`.

---

### Task 8: Rechte, Spiegel-Schutz, Sicherung auslassen, `/health`

**Files:**
- Create: `migrations/154_verbund_update.up.sql`
- Modify: `cmd/server/mirror_start.go` (`schemaBehind` :86), `cmd/server/main.go` (:115–128, :141–150, `/health` :391), `internal/web/mirror_info.go`, `internal/web/mirror.go` (Band-Text, `mirrorRestrictedURL` :169 → `/admin/server-config?tab=dr`)
- Test: `cmd/server/mirror_start_test.go`, `internal/web/mirror_test.go`, Rechte-Integrationstest

**Interfaces:**
- Produces:
  - Rechte `system.maintenance_window` („Wartungsfenster einstellen“) an alle Rollen mit `system.server_config`; `system.maintenance_abort` („Automatische Wartung abbrechen“) an alle Rollen mit `system.manage_roles` oder einem Recht `%.admin`; Kategorie `'System & Sicherheit'`; Muster `153_display_docs.up.sql:8–15`.
  - `func schemaAhead(applied, known []int) bool` → `restricted`, `MirrorInfo.Reason string`; Band zeigt „Programm zu alt für diese Datenbank“.
  - `func skipUpdateBackup(get func(string) string, commit string) bool` (`app_settings update.backup_skip_commit == buildCommit`) → `BackupBeforeUpdate` ausgelassen, Log „Sicherung vor Update ausgelassen – Rückfallkopie vorhanden“.
  - `/health` zusätzlich `"commit": buildCommit`, `"migrations_ok": bool` (false bei offenen Migrationen bzw. `restricted`).

- [ ] **Step 1:** Tests: `TestSchemaAhead`, `TestMirrorReasonInBand`, `TestSkipUpdateBackup`, `TestMirrorRestrictedURL` (`tab=dr`), Rechte-Migration (Rolle mit `system.manage_roles` hat danach `system.maintenance_abort`; Rolle mit `system.server_config` hat `system.maintenance_window`).
- [ ] **Step 2:** `go test ./cmd/server/ ./internal/web/ -run 'SchemaAhead|MirrorReason|SkipUpdateBackup|MirrorRestrictedURL|Maintenance' -v` → FAIL.
- [ ] **Step 3:** umsetzen.
- [ ] **Step 4:** `go test ./cmd/server/... ./internal/web/... ./pkg/database/...` → PASS.
- [ ] **Step 5:** Commit `Rechte für das Wartungsfenster, Spiegel erkennt zu altes Programm`.

---

### Task 9: Wartungssperre im PDH (Middleware, Sitzungen, Start gesperrt)

**Files:**
- Create: `internal/web/maint_lock.go`, `internal/web/maint_lock_test.go`
- Modify: `cmd/server/main.go` (Middleware direkt nach :380, vor `StreamsMiddleware`; `LoadMaintLock` zwischen :302 und :378), `internal/web/handler.go` (`authMiddleware` :2153, Routen `/wartung*`, `Handler`-Felder)

**Interfaces:**
- Consumes: `ha.UpdateView` über `h.haDo(ctx, GET, "/v1/update", nil, &v)` (Verbund, `haConfigured`) bzw. `h.agentJSON(ctx, GET, "/v1/update/job", nil, &v)` (Einzelserver systemd); Recht `system.maintenance_abort`.
- Produces:
  - `func (h *Handler) maintView(ctx context.Context) ha.UpdateView` (1 s zwischengespeichert, `sync.RWMutex`; Abfragefehler → letzter Stand, nach 30 s ohne Antwort `Locked=false`).
  - `func (h *Handler) MaintLockMiddleware(next http.Handler) http.Handler`, bei `Locked`:
    - frei: `GET /wartung/status`, `GET|POST /wartung/anmelden`, `POST /wartung/abbrechen`, `/static/`, `/vendor/`, Icons, `GET /health`;
    - angemeldeter Inhaber von `system.maintenance_abort`: jeder andere Pfad → Wartungsbildschirm mit Knopf;
    - sonst HTML → Wartungsbildschirm (503); `HX-Request` → 503 + `HX-Redirect: /wartung`; `/api/`, Webhooks, `Accept` ohne `text/html`, `text/event-stream` → 503 JSON `{"error":"Wartung","retry_after":30}` mit `Retry-After: 30`.
    - `GET /wartung` außerhalb der Sperre → Weiterleitung `/`.
  - Sitzungs-Epoche: `app_settings session.not_before` (Unix-Sekunden); `h.sessionNotBefore atomic.Int64`, beim Start geladen; beim ersten Sehen von `Locked` gesetzt (falls neuer) und `h.CloseStreams()`. `authMiddleware`: JWT `iat < not_before` → `clearSessionCookies`, außer Benutzer mit `system.maintenance_abort`. API-Bearer-Tokens bleiben gültig (während der Sperre 503).
  - `func (h *Handler) LoadMaintLock(ctx context.Context)` in `main.go` vor `ListenAndServe`.
  - `GET /wartung/status` → `ha.UpdateView` (ohne Anmeldung).
  - `POST /wartung/abbrechen` (Recht, CSRF wie andere Formulare) → pdh-ha `POST /v1/update/abort {"by":<Anzeigename>}` bzw. Agent `/v1/update/job/abort`; Protokolleintrag.
  - `POST /wartung/anmelden`: wie `LoginPost`, aber nur Benutzer mit Recht; sonst „Anmeldung während der Wartung nur für Administratoren“.

- [ ] **Step 1:** Tests (Zustandsquelle als Attrappe): `TestLockRefusesWrites` (Tabelle HTML-POST, HX-POST, API-POST mit Bearer, Webhook-POST, SSE-GET → Handler nicht aufgerufen, 503, Header), `TestLockAllowsStatusAndStatic`, `TestLockAdminSeesScreenWithButton`, `TestLockLogsOutNonAdmins`, `TestLockAdminKeepsSessionAfterRestart`, `TestLockLoginOnlyAdmins`, `TestLockFailOpenAfter30s`, `TestLockAbortCallsHA`.
- [ ] **Step 2:** `go test ./internal/web/ -run Lock -v` → FAIL.
- [ ] **Step 3:** umsetzen; Bildschirm vorerst schlicht (Task 10 gestaltet ihn).
- [ ] **Step 4:** `go test ./internal/web/... ./cmd/server/...` → PASS.
- [ ] **Step 5:** Commit `PDH: Wartungssperre mit Abmeldung und Sperre aller Schnittstellen`.

---

### Task 10: Wartungsbildschirm und Countdown-Band

**Files:**
- Create: `web/templates/maint_lock.gohtml`
- Modify: `internal/web/maint_lock.go` (Rendern wie Sonderfall `login` in `render` :955), `web/templates/base.gohtml` (Band neben `.Mirror`-Band :1078; Abfrage nach Push-Muster :2990)
- Test: `internal/web/maint_lock_test.go`, i18n-Tests

**Interfaces:**
- Consumes: `ha.UpdateView`, Routen aus Task 9.
- Produces:
  - Bildschirm mit eigenem `<head>` nach Muster `login.gohtml:3–55` (`dataset.palette`, `dataset.style`, Fiori-Regeln): „Automatische Wartung läuft“, „<from_version> → <to_version>“, Schrittleiste (`steps`/`step`), Balken (`percent`), `text`, Restzeit; `rolling_back` → „Wartung wird rückgängig gemacht – die bisherige Version wird wiederhergestellt“; nicht mehr gesperrt → „Wartung beendet“, nach 3 s `/login`. JS fragt `/wartung/status` alle 2 s (`cache:'no-store'`); Fehler → „Server wird neu gestartet …“ und weiter fragen. Mit Recht: Knopf „Abbrechen und zurücksetzen“ (`confirm("Wartung abbrechen und bisherige Version wiederherstellen?")`); ohne: Verweis „Als Administrator anmelden“.
  - Band in `base.gohtml` (nicht in `html.pdh-embedded`): alle 15 s `fetch('/wartung/status',{pdhQuiet:true})`; `countdown_until` in der Zukunft → „Automatische Wartung in m:ss min – bitte Eingaben speichern“, sekündlich herunterzählen; `locked` → `location.href='/wartung'`.

- [ ] **Step 1:** Tests: `TestMaintScreenRender` (Schritte, Versionen, `data-style`; Knopf nur mit Recht), `TestMaintScreenRollingBack`, `TestBaseHasMaintBand`; i18n-Katalogtest nach Ergänzung grün.
- [ ] **Step 2:** `go test ./internal/web/ -run 'MaintScreen|MaintBand|I18n' -v` → FAIL.
- [ ] **Step 3:** umsetzen; Übersetzungen in allen vier Katalogen.
- [ ] **Step 4:** `go test ./internal/web/...` → PASS; Browser: Bildschirm, Band, Admin-Ansicht in Fiori/Fluent, hell/dunkel, 320/390/768/1024/1366 px (Zustandsquelle per Test-Handler auf „gesperrt“).
- [ ] **Step 5:** Commit `PDH: Wartungsbildschirm mit Fortschritt und Hinweis-Band`.

---

### Task 11: Einstellungen, Knopf, Fortschritt, „Zeuge veraltet“

**Files:**
- Create: `internal/web/maint_window.go`, `internal/web/maint_window_test.go`
- Modify: `web/templates/widgets/ha.gohtml` (neue Karte nach „Zeiten“ :82), `web/templates/widgets/dr.gohtml` (Einzelserver), `internal/web/ha.go` (`haView` :146, `haLoadView` :462, `haRoute` :756), `internal/web/core_settings.go` (`InstallUpdateWeb` :312), `web/templates/core_settings.gohtml` (:79–109), `internal/web/handler.go` (Routen :597–607)

**Interfaces:**
- Consumes: pdh-ha `PUT /v1/window`, `GET /v1/update`, `POST /v1/update/start`, `DELETE /v1/update/faulty/{c}`; Agent `GET|POST /v1/window`, `POST /v1/update/job/start`, `DELETE /v1/window/faulty/{c}`; Rechte aus Task 8.
- Produces:
  - Routen `POST /admin/server-config/ha/window`, `POST /admin/server-config/ha/faulty-allow` (beide Recht `system.maintenance_window`, über `haRoute`), `POST /admin/server-config/update-window` (Einzelserver, gleiches Recht).
  - `haView` bekommt `Window ha.Window`, `Update ha.UpdateView`, `Faulty []string`, `NextWindow time.Time`, `LastResult string`, `WitnessStale string` („Zeuge veraltet (<v>, Server <v>)“, wenn Zeugen-Commit ≠ Server-Commit und der letzte Auftrag seit > 30 min fertig) und `WitnessCmd string`.
  - `InstallUpdateWeb`: Verbund → `POST /v1/update/start {source:"knopf",kind:Window.Kind,commit:<neuester Commit aus checkGitHubUpdate>}`; 409 → Seite mit Rückfrage „Nicht alles grün (<Grund>). Trotzdem mit kurzer Unterbrechung aktualisieren?“ (Form mit `force=1`); Einzelserver systemd → Agent `/v1/update/job/start`; Docker → wie bisher. Am Spiegel gesperrt mit „wird vom aktiven Server gesteuert“.
  - Core-Karte: im Verbund „Update im Verbund installieren“, darunter Update-Art und Verweis auf das Wartungsfenster; Texte der Karte über `{{t}}`.
  - Ausfallschutz-Reiter: Fortschritt eines laufenden Auftrags; Befehl `pdh-ha-join.sh --update` in `<details>` „Notweg: von Hand aktualisieren“.
  - Einzelserver-Meldungen (Spec 7): `func (h *Handler) maintWatchOnce(ctx context.Context)` im vorhandenen `haWatch`-Takt liest Agent `GET /v1/window` und meldet ein neues `LastResult` per `notifyAdmins` (gleiche Drosselung wie `haNotifyAlarms`).

- [ ] **Step 1:** Tests: `TestMaintWatchNotifiesOnce`, `TestWindowFormSaves` (Formular → PUT-Rumpf an Attrappe), `TestWindowFormNeedsPerm` (403), `TestInstallUpdateClusterConflictAsks`, `TestInstallUpdateForce`, `TestInstallUpdateSingleUsesJob`, `TestWitnessStaleHint`, `TestHAWidgetRendersWindow`.
- [ ] **Step 2:** `go test ./internal/web/ -run 'WindowForm|InstallUpdate|WitnessStale|HAWidget' -v` → FAIL.
- [ ] **Step 3:** umsetzen, Übersetzungen ergänzen.
- [ ] **Step 4:** `go test ./internal/web/...` → PASS; Browser: Kasten und Core-Karte in beiden Stilen, hell/dunkel.
- [ ] **Step 5:** Commit `PDH: Wartungsfenster einstellen, Update im Verbund per Knopf`.

---

### Task 12: Zeuge aktualisiert sich selbst

**Files:**
- Create: `deploy/ha-witness/updater.sh`, `deploy/ha-witness/updater.Dockerfile`, `deploy/ha-witness/updater_test.go` (Paket `hawitness`, ruft `bash` mit Attrappen im `PATH`)
- Modify: `deploy/ha-witness/compose.yaml`, `scripts/pdh-ha-join.sh` (Sperre Z.45), `cmd/pdh-ha/update_job.go` (Zeuge als Programm)

**Interfaces:**
- Produces:
  - Dienst `updater`: Build `updater.Dockerfile` (Basis `docker:27-cli`, dazu `git`, `bash`, `jq`), Volumes `/var/run/docker.sock:/var/run/docker.sock` und `ha-src:/src`, env `PDH_HA_AUTOUPDATE` (Standard `1`), `PDH_HA_REPO` (Standard `https://github.com/strobobaer/pdh-server.git`), `restart: unless-stopped`.
  - `updater.sh` alle 300 s: `docker exec pdh-ha-witness-witness-1 pdh-ha status --json` → Commits beider Server und des Zeugen, `update.active`; beide Server auf X, Zeuge nicht, kein Auftrag → `git -C /src fetch` und `checkout --detach X`, `PDH_HA_COMMIT=X docker compose -p pdh-ha-witness -f /src/deploy/ha-witness/compose.yaml up -d --build witness`; bis 120 s prüfen, dass beide Server `seen` sind; sonst Alt-Commit wieder bauen. Jede Aktion mit Zeitstempel auf stdout. `PDH_HA_AUTOUPDATE=0` → nur schlafen.
  - pdh-ha auf einem Zeugen ohne Container (`/.dockerenv` fehlt, `PDH_HA_SELFUPDATE` ≠ `0`): gleiche Bedingung, höchstens alle 5 min → `systemd-run --unit=pdh-ha-selfupdate-<ts> bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update X --witness`.
  - `pdh-ha-join.sh`: `--update` mit `--witness` erlaubt (baut nur pdh-ha).

- [ ] **Step 1:** Tests: `TestWitnessUpdaterPulls` (Server X, Zeuge Y → Protokoll enthält `checkout --detach X` und `up -d --build witness`), `TestWitnessUpdaterWaitsForJob`, `TestWitnessUpdaterRollsBack`, `TestWitnessUpdaterOff`, `TestJoinScriptWitnessUpdate` (`bash -n`, Sperre entfernt), `TestWitnessSelfUpdateCondition` (pdh-ha).
- [ ] **Step 2:** `go test ./deploy/ha-witness/... ./cmd/pdh-ha/ -run 'WitnessUpdater|WitnessSelf|JoinScript' -v` → FAIL.
- [ ] **Step 3:** umsetzen.
- [ ] **Step 4:** Tests → PASS; `docker compose -f deploy/ha-witness/compose.yaml config` fehlerfrei.
- [ ] **Step 5:** Commit `Zeuge: Helfer zieht den Zeugen auf den Stand der Server nach`.

Hinweis: Spec-Labor-Szenario 9 wird hier als Skript-Test mit Attrappen umgesetzt, weil das Labor keinen Docker-Socket bereitstellt.

---

### Task 13: Labor-Szenarien

**Files:**
- Create: `deploy/ha-lab/lab-agent.sh`
- Modify: `deploy/ha-witness/Dockerfile` (Ziel `lab-pg`: `lab-agent.sh` kopieren, `ENV PDH_HA_AGENT_CMD=/usr/local/bin/lab-agent PDH_HA_COMMIT_FILE=/var/lib/pdh-lab/commit PDH_SERVER_PORT=8090`), `deploy/ha-lab/pdh-dummy.sh`, `deploy/ha-lab/ha-lab.sh` (`SCENARIOS` :44, neue `sc_*`), `cmd/pdh-ha/daemon.go` (ist `PDH_HA_COMMIT_FILE` gesetzt, Commit je Takt aus der Datei)

**Interfaces:**
- Produces:
  - `lab-agent.sh install|install_norestart|rollback|finish <commit>`: schreibt den Commit nach `$PDH_HA_COMMIT_FILE` (bei `install_norestart` erst bei `finish`); Datei `/var/lib/pdh-lab/fail-install` → Exit 1; startet danach `pdh-dummy restart`.
  - `pdh-dummy.sh start`: `/var/lib/pdh-lab/fail-start-<commit>` → Exit 1; als Primary mit Commit ≠ `lab0` → `psql -c "CREATE TABLE IF NOT EXISTS lab.mig_<commit>(x int)"`; kleiner `/health`-Dienst auf 8090 (`busybox httpd` mit CGI oder `socat`), Antwort `{"version":"<VERSION>","migrations_ok":true}`, `fail-health-<commit>` → 500.
  - Szenarien: `update-ok`, `update-b-vorher`, `update-b-nachher`, `update-abbruch`, `update-neustart`, `update-nicht-gruen`, `update-nacheinander`, `update-nacheinander-fehler`, `update-hoechstdauer` (Sperre-Höchstdauer im Szenario per `PUT /v1/window` auf 10 min und Prüffrist über `PDH_HA_UPDATE_VERIFY=60s` verkürzt – Schalter nur für das Labor, in Task 4 auslesen). Jedes prüft Rollen, Commits (`jget` auf `peers[].commit`), Ergebnis aus `pdh-ha update status`; bei Rückweg: `lab.mig_<X>` auf keinem Knoten vorhanden; `check_acked` ohne Verlust.

- [ ] **Step 1:** Szenarien nach Muster `sc_version_anders` (Z.527) schreiben.
- [ ] **Step 2:** `bash deploy/ha-lab/ha-lab.sh update-ok` → zeigt Lücken (FAIL).
- [ ] **Step 3:** Labor-Agent und Schalter umsetzen; vom Labor aufgedeckte Fehler in Tasks 1–5 beheben, jeweils mit Regressionstest im Paket.
- [ ] **Step 4:** `bash deploy/ha-lab/ha-lab.sh alle` → bisherige 23 und neue 9 BESTANDEN; `bash deploy/ha-lab/ha-lab.sh race` grün (auf dem Windows-Docker des Nutzers über den Labor-Helfer).
- [ ] **Step 5:** Commit `Labor: Szenarien für das Update im Verbund`.

---

### Task 14: Handbuch, Skizzen, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `ausfallschutz`, „Versionen & Updates“ ~:1917–1924, „Administration“), `web/templates/widgets/help_figures.gohtml`, `internal/web/help.go` (`helpChapterIDs` :47 nur bei neuem Kapitel), `CHANGELOG.md`, `VERSION`, `docs/superpowers/specs/2026-10-24-ausfallschutz-design.md`

**Interfaces:**
- Produces:
  - `.hb-howto`-Abschnitte: „Wartungsfenster einrichten“, „Was bei einem Update im Verbund passiert“ (neue Skizze der Schritte), „Wartungsbildschirm und Abbrechen“ (inkl. `pdh-ha update abort`), „Zeuge aktualisiert sich selbst“ (Docker-Socket-Hinweis, `PDH_HA_AUTOUPDATE=0`), Update auf dem Einzelserver mit Countdown; Satz „Update bringt pdh-ha nur auf dem ersten Server …“ ersetzt; Hand-Anleitung als Notweg; „Administration“: die zwei neuen Rechte.
  - Skizze des Ausfallschutz-Reiters mit Kasten „Wartungsfenster“.
  - `VERSION` = `0.77.8`; `CHANGELOG.md` `## [0.77.8] – 2026-10-30`: `### Neu` (Update im Verbund per Knopf und Wartungsfenster; Wartungsbildschirm mit Fortschritt und Countdown; Zeuge zieht selbst nach; geplanter Rollentausch im Wartungsfenster), `### Geändert` (Update auf dem Einzelserver mit Countdown, Sperre und automatischem Rückweg; Spiegel erkennt zu altes Programm), `### Behoben` (Verweis des Spiegel-Bands auf den Ausfallschutz-Reiter), `### Sicherheit` (Rechte „Wartungsfenster einstellen“ und „Automatische Wartung abbrechen“; Abmeldung aller Benutzer während der Wartung).
  - Spec 2026-10-24: Vermerk „Teil 8 aufgegangen in `2026-10-30-verbund-update-design.md`“.

- [ ] **Step 1:** Texte, Skizzen, Version ändern.
- [ ] **Step 2:** `go test ./internal/web/ -run 'Help|Changelog|I18n' -v` → bei Fehlschlag Kapitel/IDs/Übersetzungen nachziehen.
- [ ] **Step 3:** `go build ./... && go test ./...` → PASS.
- [ ] **Step 4:** Browser: Handbuch-Kapitel in beiden Stilen, Skizze stimmt.
- [ ] **Step 5:** Commit `Handbuch und Version 0.77.8: Update im Verbund und Wartungsfenster`.

---

### Task 15: Abgleich mit main, Gesamtprüfung, Abnahme im Echtnetz

**Files:** keine neuen; Zweig `test/verbund-update-probe` (nie nach main)

- [ ] **Step 1: Abgleich** (AGENTS.md): `git fetch origin main`, `git merge origin/main`; Dopplungen (`git diff origin/main...HEAD`, grep nach `/v1/update`, `maintenance_`, Migrationsnummer 154, `VERSION`); Nummer/Version ggf. anheben; `go build ./...`, `go test ./...`, Labor `alle`.
- [ ] **Step 2: Gesamtreview** des Zweigs (frischer Reviewer mit Spec, Plan, Diff); Befunde beheben.
- [ ] **Step 3: Probe-Zweig** von `feature/verbund-update`: Commit A „Probe: harmlose Version“ (`VERSION` 0.77.8-probe1, Migration `9001_probe.up.sql`: `CREATE TABLE probe_update(x int)`), Commit B „Probe: kaputt“ (`cmd/server/main.go`: `log.Fatal().Msg("Probe: absichtlich kaputt")` direkt nach dem Start, `VERSION` 0.77.8-probe2). Push nur nach Freigabe des Nutzers.
- [ ] **Step 4: Testserver** (pdh 10.10.0.14, pdh-server 10.10.0.17): Stand `feature/verbund-update` einspielen (passiver Server zuerst), `PDH_UPDATE_BRANCH=test/verbund-update-probe` in `/opt/pdh-server/.env`, Agent neu starten; Zeuge (Windows): neue `compose.yaml` einmalig von Hand (`git pull`, `docker compose up -d --build`).
- [ ] **Step 5: Abnahme** (dem Nutzer Befehle, Rechner und erwartetes Ergebnis jeweils vollständig nennen):
  1. Knopf „Update im Verbund installieren“ auf Probe A: Band, Sperre, Fortschritt, Freigabe; danach beide Server auf A, Rollen vertauscht, `probe_update` vorhanden, kein „Versionen unterschiedlich“; Zeuge nach ≤ 10 min auf A.
  2. Fenster „täglich“, Grundsicherung auf die nächste Stunde: Start nach der Grundsicherung (vor/nach Abgleich je Einstellung) mit Probe B → Zurücksetzen, B als fehlerhaft gemerkt, Daten unverändert, Anwender wieder auf Stand A.
  3. Abbruch durch Administrator in der Sperre → Zurücksetzen, Protokoll mit Namen.
  4. „Wieder zulassen“ der fehlerhaften Version.
- [ ] **Step 6: Aufräumen:** `PDH_UPDATE_BRANCH` entfernen; Testserver auf `feature/verbund-update`; `DROP TABLE probe_update` und Eintrag 9001 aus `schema_migrations` löschen; Probe-Zweig löschen.
- [ ] **Step 7:** Pull-Request mit Prüfergebnis (Dopplungen/Abweichungen); Merge nach main **nur mit Freigabe des Nutzers**.
