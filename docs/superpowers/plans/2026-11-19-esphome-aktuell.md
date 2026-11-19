# ESPPDH: ESPHome hält sich selbst aktuell (0.82.1) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jeder Server prüft täglich auf eine neuere ESPHome-Version, richtet sie neben der laufenden ein, probt sie mit allen Gerätekonfigurationen und schaltet nur bei bestandener Probe um.

**Architecture:** Die bisherige Umgebung `/opt/pdh-esphome` bleibt als **Grundumgebung** (Mindestversion aus `scripts/esphome.version`, vom Update gepflegt). Selbst geholte Versionen liegen daneben in `/opt/pdh-esphome-versions/<version>/`; zwei kleine Zeigerdateien (`active`, `previous`) sagen, welche gilt. Der Update-Agent (root) richtet ein, schaltet um und räumt auf; das PDH (Benutzer `pdh`) fragt PyPI, probt und entscheidet. Der Zustand je Server liegt in einer Datei im Arbeitsordner, weil der Spiegel nicht in die Datenbank schreiben kann.

**Tech Stack:** Go 1.27, bash, PyPI-JSON (`https://pypi.org/pypi/esphome/json`), bestehender Update-Agent, htmx-Karte im Reiter „ESPPDH“.

**Spec:** `docs/superpowers/specs/2026-11-19-esphome-aktuell-design.md` (vom Anwender am 19.11.2026 bestätigt). Grundlage: `docs/superpowers/specs/2026-11-18-esppdh-design.md`.

**Branch:** `feature/esphome-aktuell` ab `main` mit 0.82.0. Keine Migration, kein neues Recht.

## Global Constraints

- Karenz: nur Versionen, die seit mindestens **3 Tagen** veröffentlicht sind.
- Nur stabile Versionen der Form `^\d{4}\.\d{1,2}\.\d{1,3}$`; nichts mit `a`, `b`, `rc`, `dev`; keine zurückgezogenen (`yanked`).
- Unter **3 GB** frei (Dateisystem von `/opt`) wird kein Kandidat eingerichtet.
- Eine gescheiterte Version wird nicht erneut versucht, bis die PDH-Version sich ändert oder „Jetzt prüfen“ gedrückt wird.
- Firmware wird nie von selbst neu gebaut oder verteilt.
- Der Agent holt ausschließlich `esphome==<version>` von PyPI; Versionsangabe nur in obiger Form; Pfade nur unter `/opt/pdh-esphome-versions/`.
- Schalter „ESPHome selbst aktuell halten“: Standard **ein** (`app_settings`-Schlüssel `esppdh.autoupdate`, nur `"0"` heißt aus).
- Alle Aktionen prüfen `canServerConfig` (wie „Umgebung einrichten“). Der Schalter ist am Spiegel gesperrt (409, `mirrorReadOnlyMsg`); „Jetzt prüfen“ und „Zurück zur vorigen Version“ sind am Spiegel erlaubt.
- Docker-Betrieb, fehlende Umgebung, ausgeschaltetes ESPPDH, nicht eingerichteter Agent: nichts tun, kein Fehler, PDH läuft unverändert.
- Tests laufen unter Windows (Git Bash) und Linux; fremde Programme nur über ersetzbare Befehlsfunktionen, HTTP nur gegen `httptest`.
- AGENTS.md gilt: Handbuch und Änderungsprotokoll im selben Schritt, Deutsch, beide Stile über CSS-Variablen, kein Push/Merge ohne Freigabe.
- Abweichung vom Entwurf (dort „höchstens drei Umgebungen“): Es sind bis zu vier – Grundumgebung, laufende, vorige, Kandidat. Die Grundumgebung bleibt, damit ein PDH-Update mit höherer Mindestversion sofort eine brauchbare Umgebung hat.

## Review Focus

1. **PyPI liefert Unerwartetes** (kein JSON, leere Liste, Version ohne Dateien, Datum ohne Zeitzone, 5 MB Antwort): kein Kandidat, „Prüfung nicht möglich“, kein Absturz → Task 1 `TestPickCandidate`, Task 4 `TestUpdateCheckBadPyPI`.
2. **Ein Gerät ist schon mit der laufenden Version kaputt** (z. B. WLAN nicht mehr freigegeben): darf den Kandidaten nicht scheitern lassen → Task 4 `TestProbeIgnoresAlreadyBroken`.
3. **Zeigerdatei zeigt ins Leere oder auf Älteres als die Mindestversion** (Umgebung von Hand gelöscht, PDH-Update hob die Mindestversion): ESPPDH nimmt die Grundumgebung → Task 2 `TestActiveBinFallback`.
4. **Prüfung und Bau gleichzeitig; Neustart mitten in der Probe**: nie zwei ESPHome-Läufe zugleich; nach Neustart kein halber Kandidat als „laufend“ → Task 4 `TestProbeWaitsForBuild`, Task 3 `TestVersionsDiscardOrphans`.
5. **Platte voll / pip scheitert / Agent zu alt (404)**: bisherige Version bleibt, klare Meldung im Reiter, kein täglicher Neuversuch derselben Version → Task 4 `TestUpdateInstallFails`, `TestUpdateAgentTooOld`.

---

### Task 1: Versionen wählen, Chip-Familie, Musterkonfigurationen

**Files:**
- Create: `internal/modules/esppdh/update.go`, `internal/modules/esppdh/update_test.go`, `internal/modules/esppdh/samples.go`
- Modify: `internal/modules/esppdh/boards.go` (Feld `Chip`), `internal/modules/esppdh/yaml_test.go` (nutzt `Samples()`), `internal/modules/esppdh/testdata/` (neu: `esp32-c3-counter.yaml`, `.secrets.yaml`)

**Interfaces – Produces:**
- `func CompareVersions(a, b string) int` – numerisch je Teil, wie `internal/web/version.go:compareVersions`.
- `var StableVersionRe = regexp.MustCompile(`^\d{4}\.\d{1,2}\.\d{1,3}$`)`
- `type Release struct{ Version string; Uploaded time.Time; Yanked bool; RequiresPython string }`
- `func ParsePyPI(body []byte) ([]Release, error)` – aus `releases`: je Version früheste `upload_time_iso_8601`, `yanked` wenn alle Dateien zurückgezogen, `requires_python` der ersten Datei; Versionen ohne Dateien entfallen; mehr als 4 MB → Fehler.
- `func PickCandidate(rels []Release, installed string, py [2]int, now time.Time, skip string) string` – höchste Version mit: `StableVersionRe`, `> installed`, nicht `Yanked`, `now − Uploaded ≥ 72h`, `!= skip`, `RequiresPython` erfüllt (nur Form `>=X.Y[,<…]`; `py == [0,0]` oder unlesbare Angabe: Filter aus). Sonst `""`.
- `Board.Chip string` mit Werten `esp32`, `esp32s3`, `esp32c3`, `esp8266`; `func ChipOf(boardKey, customBoard string) string` (Platine „other“: Ergebnis von `PlatformOf`).
- `type Sample struct{ Name string; Device Device }`; `func Samples() []Sample` – die bisherigen vier `goldenCases` plus `esp32-c3-counter` (ESP32-C3, WLAN, ein Stückzähler); Kennwörter feste Prüfwerte.

- [ ] **Schritt 1: Tests schreiben.** `TestCompareVersions` (`2026.10.2 < 2026.11.0`, `2026.9.3 < 2026.10.0`, gleich = 0). `TestParsePyPI` (Datei mit drei Versionen, eine ohne Dateien, eine `yanked`). `TestPickCandidate` als Tabelle: neuere stabile mit 4 Tagen → gewählt; mit 2 Tagen → `""`; `2026.11.0b1` → nie; `yanked` → nie; `skip` gleich Kandidat → nächstniedrigere neuere oder `""`; `requires_python ">=3.13"` bei `py={3,12}` → übersprungen; `installed` leer → `""`; leere Liste → `""`. `TestChipOf` für alle Platinen. `TestGenerateGolden` läuft über `Samples()`.
- [ ] **Schritt 2:** `go test ./internal/modules/esppdh -run 'Compare|PyPI|PickCandidate|ChipOf|Golden'` scheitert (fehlende Funktionen).
- [ ] **Schritt 3: Umsetzen.** Neue Erwartungsdatei mit `go test ./internal/modules/esppdh -run TestGenerateGolden -update` erzeugen und lesen.
- [ ] **Schritt 4:** Tests grün; `GOOS=linux go vet ./internal/modules/esppdh`.
- [ ] **Schritt 5:** Commit „ESPPDH: ESPHome-Versionen wählen, Chip-Familie, Musterkonfigurationen“.

### Task 2: Mehrere Umgebungen nebeneinander, Konfigurationsprüfung

**Files:**
- Create: `internal/modules/esppdh/versions.go`, `internal/modules/esppdh/versions_test.go`
- Modify: `internal/modules/esppdh/env.go`, `internal/modules/esppdh/build.go`, deren Tests

**Interfaces – Consumes:** `CompareVersions`, `StableVersionRe` (Task 1).

**Interfaces – Produces:**
- `const VersionsDir = "/opt/pdh-esphome-versions"` (überschreibbar mit `PDH_ESPHOME_VERSIONS_DIR`, nur für Tests); darin Zeigerdateien `active` und `previous` (eine Zeile, Version) und je Version ein Ordner.
- `func VersionBin(version string) string` – `<VersionsDir>/<version>/bin/esphome`; `""` bei ungültiger Version.
- `func ActiveVersionPointer() string` – Inhalt von `active`, wenn gültig, der Ordner da ist **und** `CompareVersions(v, PinnedVersion()) >= 0`; sonst `""`.
- `func PreviousVersionPointer() string` – ebenso für `previous`; `"base"` bedeutet Grundumgebung.
- `esphomeBin()` liefert `VersionBin(ActiveVersionPointer())`, sonst wie bisher die Grundumgebung. Neu exportiert: `func ActiveBin() string`.
- `EnvState.Outdated` = installierte Version **älter** als `PinnedVersion()` (bisher: ungleich). Neues Feld `EnvState.Base bool` (läuft die Grundumgebung).
- `Runner.Bin string` – leer = `ActiveBin()`; `esphomeCommand` nutzt es.
- `func (r *Runner) Config(ctx context.Context, job BuildJob) (log string, err error)` – wie `Build`, aber `config ./esppdh.yaml`, Zeitlimit 3 min, gleicher Laufordner mit `secrets.yaml`, gleiche schlanke Umgebung, gleiches Aufräumen, Ausgabe über `Redact`.
- `func FreeMBAt(path string) int64` (exportiert `freeMB`).
- Zustand je Server: `type UpdateState struct{ CheckedAt time.Time; Result string; Installed string; FailedVersion, FailedPDH, FailedLog string; NotifiedVersion string }`; `func LoadUpdateState() UpdateState`; `func SaveUpdateState(UpdateState) error` – Datei `<WorkDir>/update-state.json`, 0600, atomar (Temp-Datei + Rename); fehlend oder unlesbar = leerer Zustand.

- [ ] **Schritt 1: Tests.** `TestActiveBinFallback`: ohne Zeiger → Grundumgebung; Zeiger auf vorhandene neuere Version → deren Programm; Zeiger auf fehlenden Ordner → Grundumgebung; Zeiger älter als Mindestversion → Grundumgebung; Zeiger mit `../x` → Grundumgebung. `TestOutdatedOnlyWhenOlder` (gleich und neuer: nicht veraltet). `TestRunnerConfig` mit ersetzter Befehlsfunktion: Argumente `config ./esppdh.yaml`, Laufordner danach weg, Kennwort im Ergebnis geschwärzt, Fehler bei Rückgabewert ≠ 0. `TestUpdateStateRoundTrip`, `TestUpdateStateCorrupt`.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./internal/modules/esppdh` grün; `GOOS=linux go build ./...`.
- [ ] **Schritt 5:** Commit „ESPPDH: mehrere ESPHome-Umgebungen nebeneinander, Konfigurationsprüfung“.

### Task 3: Skript und Update-Agent – Kandidat einrichten, umschalten, aufräumen

**Files:**
- Modify: `scripts/install-esphome.sh`, `cmd/update-agent/esppdh.go`, `cmd/update-agent/main.go`, `cmd/update-agent/esppdh_test.go`
- Create: `cmd/update-agent/esppdh_versions.go`, `cmd/update-agent/esppdh_versions_test.go`

**Skript:** neue Umgebungsvariable `PDH_ESPHOME_VERSION`. Gesetzt: Form wie Global Constraints prüfen, sonst Abbruch; Zielordner ist dann **immer** `/opt/pdh-esphome-versions/<version>` (ein mitgegebenes `PDH_ESPHOME_DIR` wird ignoriert); Elternordner `install -d -m 0755 -o root`; vor dem Anlegen prüfen, dass unter `/opt` mindestens 3072 MB frei sind („Zu wenig Platz …“). Nicht gesetzt: Verhalten unverändert (Grundumgebung, Version aus `esphome.version`). Neue Prüffunktion `esphome_version_ok VERSION`.

**Agent – Produces** (alle hinter `authorized`, im Docker-Modus `{"ok":false,"error":esppdhDockerMsg}`):
- `POST /v1/esppdh/install` nimmt jetzt optional `{"version":"2026.11.1"}`: richtet den Kandidaten ein (gleicher Hintergrundauftrag, `--setenv=PDH_ESPHOME_VERSION`). `esppdhStatus` bekommt `Target string` (leer = Grundumgebung).
- `GET /v1/esppdh/versions` → `{"ok":true,"active":"…","previous":"…","installed":["…"]}` (`previous` kann `"base"` sein).
- `POST /v1/esppdh/versions` mit `{"action":"activate"|"discard"|"rollback","version":"…"}`:
  - `activate`: Ordner muss da sein und `bin/esphome version` die Version nennen; `previous` = bisheriges `active` oder `"base"`; `active` atomar schreiben (0644); alle anderen Versionsordner löschen.
  - `discard`: Ordner löschen, wenn er weder `active` noch `previous` ist.
  - `rollback`: `active` und `previous` tauschen (`"base"` = Zeiger `active` entfernen); ohne `previous` → Fehler „Es gibt keine vorige Version“.
  - Löschen nur über eine Funktion `removeVersionDir(version)`: Version in gültiger Form, Pfad genau `<VersionsDir>/<version>`, kein Verweis, enthält `pyvenv.cfg` oder ist leer.
- Beim Start des Agenten und vor jedem `install`: Ordner, die weder `active` noch `previous` noch laufender Auftrag sind, löschen (verwaiste Kandidaten nach Neustart).

- [ ] **Schritt 1: Tests.** Skript: `TestEsppdhVersionGuard` (`2026.11.1` ok; `2026.11.0b1`, `1.2`, `2026.11.1; rm`, leer → abgelehnt), `TestEsppdhCandidateIgnoresDir`. Agent mit Temp-Ordner als `VersionsDir` und ersetztem `esppdhRun`: `TestVersionsActivate` (Zeiger gesetzt, `previous` = `base`, fremde Ordner weg), `TestVersionsActivateWrongBinary` (Programm nennt andere Version → Fehler, Zeiger unverändert), `TestVersionsDiscardKeepsActive`, `TestVersionsRollback` (hin und zurück, ohne `previous` Fehler), `TestVersionsRejectsBadVersion` (`..`, `/`, leer), `TestVersionsDiscardOrphans`, `TestInstallCandidatePassesVersion` (Umgebung des Skripts enthält `PDH_ESPHOME_VERSION`), `TestVersionsDocker`.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./cmd/update-agent` grün; `bash -n scripts/install-esphome.sh`; `GOOS=linux go vet ./cmd/update-agent`.
- [ ] **Schritt 5:** Commit „ESPPDH: Update-Agent richtet ESPHome-Versionen nebeneinander ein und schaltet um“.

### Task 4: Ablauf im PDH – prüfen, proben, umschalten, melden

**Files:**
- Create: `internal/web/esppdh_update.go`, `internal/web/esppdh_update_test.go`
- Modify: `internal/web/esppdh_build.go` (Lauf-Sperre), `internal/web/handler.go` (Felder), `cmd/server/main.go` (Start)

**Interfaces – Consumes:** Task 1–3; `h.agentJSON`, `h.esppdhList`, `h.esppdhBuildInput(ctx, id, "")`, `h.usersWithPerm`, `h.systemNotify`, `h.Mirror()`, `h.esppdhStateOf`, laufende PDH-Version (eingebettete `VERSION`).

**Interfaces – Produces:**
- Handler-Felder: `esppdhPyPIURL string` (leer = `https://pypi.org/pypi/esphome/json`), `esppdhProber func(bin string) esppdhProbeRunner` (nil = echter Runner), `esppdhUpd struct{ mu sync.Mutex; running bool }`, `esppdhNow func() time.Time`.
- `type esppdhProbeRunner interface { Config(ctx context.Context, job esppdh.BuildJob) (string, error); Build(ctx context.Context, job esppdh.BuildJob, progress func(string)) esppdh.BuildResult }`
- `func (h *Handler) esppdhAutoUpdate(ctx context.Context) bool` – `appSetting(ctx, "esppdh.autoupdate", "1") != "0"`.
- `func (h *Handler) esppdhUpdateRun(ctx context.Context, force bool) (msg string, err error)` – der ganze Ablauf, höchstens einer zur Zeit (sonst `msg` „Eine Prüfung läuft schon“).
- `func (h *Handler) StartESPPDHUpdate(ctx context.Context)` – 5-Minuten-Takt; fällig, wenn `CheckedAt` älter als 24 h (erste Prüfung frühestens 10 min nach dem Start); in `main.go` neben `StartESPPDH` (außerhalb `!mirrorOn`).
- In `esppdh_build.go`: `esppdhQ.runMu sync.Mutex`, gehalten von `esppdhRunBuild` für die Dauer eines Baus; `func (h *Handler) esppdhQueueIdle(ctx context.Context) bool` (kein `queued`/`running`).

**Ablauf `esppdhUpdateRun`:**
1. Abbrechen ohne Fehler mit Klartext-`msg`, wenn Docker, ESPPDH aus, Umgebung fehlt, Agent nicht eingerichtet; ohne `force` auch, wenn der Schalter aus ist.
2. Installierte Version über `esppdh.CheckEnv`; PyPI mit 20 s Zeitlimit, `User-Agent: pdh-server`, Antwort höchstens 4 MB. Fehler → `Result = "Prüfung nicht möglich: …"`, `CheckedAt` setzen, Ende.
3. `skip` = `FailedVersion`, wenn `FailedPDH` gleich der laufenden PDH-Version und nicht `force`. `PickCandidate`. Kein Kandidat → `Result = "ESPHome <v> ist aktuell"`.
4. `POST /v1/esppdh/install {"version":…}`, dann `GET` alle 5 s bis `Done` (höchstens 25 min). 404 → Meldung „Der Update-Agent ist zu alt – zuerst das PDH aktualisieren“, **nicht** als gescheiterte Version vermerken.
5. Probe mit `VersionBin(kandidat)`:
   a. `Config` für jede `esppdh.Samples()`-Konfiguration.
   b. `Config` für jedes nicht gesperrte Gerät, dessen Eingabe sich auflösen lässt. Scheitert ein Gerät, dasselbe mit der laufenden Version: scheitert es dort auch, zählt es nicht (im Ergebnis als „übersprungen“ nennen).
   c. Nur am aktiven Server (`!h.Mirror().On`): je `ChipOf` unter den geprobten Geräten **ein** echter `Build` (erstes Gerät der Familie). Vor jedem Probebau warten, bis `esppdhQueueIdle` (alle 30 s, höchstens 60 min; danach Probe vertagt = kein Fehlschlag, Kandidat verwerfen, `Result = "Probe vertagt – Bau-Schlange belegt"`), dann `runMu` halten. Ergebnisdateien verwerfen.
6. Bestanden → `POST /v1/esppdh/versions {"action":"activate"}`; `Result = "Auf ESPHome <v> umgestellt"`; `FailedVersion` leeren.
   Gescheitert → `discard`; `FailedVersion`, `FailedPDH`, `FailedLog` (letzte 40 Zeilen, geschwärzt) setzen; `Result = "ESPHome <v> hat die Probe nicht bestanden – es bleibt bei <alt>"`; wenn `NotifiedVersion != v`: `h.systemNotify(ctx, h.usersWithPerm(ctx, "esppdh.admin", ""), …)` und `NotifiedVersion = v` (am Spiegel schlägt die Meldung fehl – ignorieren, `NotifiedVersion` dort nicht setzen).
7. Immer: `CheckedAt`, `Installed` speichern.

- [ ] **Schritt 1: Tests** (Agent und PyPI als `httptest`-Server, `esppdhProber` als Attrappe, Temp-Ordner als Arbeitsordner): `TestUpdateNoCandidate`; `TestUpdateSwitches` (Reihenfolge install → Config für alle Muster und Geräte → Build je Chip-Familie → activate); `TestUpdateProbeFails` (discard, Zustand, genau eine Meldung, zweiter Lauf ohne `force` fragt den Agenten nicht mehr); `TestUpdateRetryAfterPDHUpdate` (andere PDH-Version → erneuter Versuch); `TestUpdateForceRetries`; `TestProbeIgnoresAlreadyBroken`; `TestProbeWaitsForBuild` (laufender Bau hält `runMu` → Probebau startet erst danach); `TestUpdateMirrorNoBuild` (am Spiegel nur `Config`); `TestUpdateCheckBadPyPI` (HTML, 500, Zeitlimit, 5 MB); `TestUpdateInstallFails`; `TestUpdateAgentTooOld`; `TestUpdateSwitchOff` (ohne `force` nichts, mit `force` läuft); `TestUpdateDocker`; `TestUpdateSingleFlight`.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./internal/web -run 'Update|Probe|ESPPDH|Esppdh'` grün; `GOOS=linux go build ./...`.
- [ ] **Schritt 5:** Commit „ESPPDH: ESPHome täglich prüfen, proben und umschalten“.

### Task 5: Reiter „ESPPDH“ und Gerätehinweis

**Files:**
- Modify: `internal/web/esppdh_settings.go`, `web/templates/widgets/esppdh_settings.gohtml`, `internal/web/handler.go` (Routen), `internal/web/mirror.go` (`mirrorAllowedWrite`), `internal/web/esppdh.go` + `web/templates/esppdh_device.gohtml` (Hinweis), Tests dazu, i18n-Schlüssel wie bei den vorhandenen ESPPDH-Texten

**Routen** (alle `canServerConfig`, sonst 403):
- `POST /admin/server-config/esppdh/auto` (`on=1|0`) – setzt `esppdh.autoupdate`; am Spiegel 409.
- `POST /admin/server-config/esppdh/check` – startet `esppdhUpdateRun(ctx, true)` im Hintergrund, antwortet sofort mit der Karte („Prüfung läuft …“, Karte fragt alle 2 s nach, solange `esppdhUpd.running`); am Spiegel erlaubt.
- `POST /admin/server-config/esppdh/rollback` – `hx-confirm`; Agent `rollback`; danach die verlassene Version als gescheitert vermerken (`FailedPDH` = laufende PDH-Version); am Spiegel erlaubt.

**Karte zeigt:** installierte Version und ob Grundumgebung; Mindestversion; „Zuletzt geprüft: <Zeit> – <Result>“; bei `FailedVersion` einen Hinweis im Ton „warn“ mit aufklappbarer Ausgabe (`<details>`, Text als `<pre>`); Schalter; Knöpfe „Jetzt prüfen“ und – nur wenn der Agent ein `previous` meldet – „Zurück zur vorigen Version (<v>)“. Farben nur über Variablen.

**Gerätehinweis:** Auf der Geräteseite beim letzten erfolgreichen Bau, wenn `esphome_version` älter ist als `LoadUpdateState().Installed`: „Firmware mit ESPHome <x> gebaut, auf dem Server ist <y>. Neu bauen und flashen ist möglich, aber nicht nötig.“ (Ton „info“).

- [ ] **Schritt 1: Tests.** `TestEsppdhAutoSwitch` (ohne Recht 403, Spiegel 409, Wert gespeichert); `TestEsppdhCheckNow` (startet Lauf, zweiter Klick startet keinen zweiten, am Spiegel erlaubt – `mirrorAllowedWrite`); `TestEsppdhRollback` (Agent-Aufruf, Zustand, Knopf fehlt ohne `previous`); `TestEsppdhPanelShowsFailure` (`<script>` im Verlauf erscheint maskiert); `TestDeviceOlderFirmwareHint` (älter → Hinweis, gleich → keiner, Zustand leer → keiner).
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./internal/web -run 'Esppdh|ESPPDH|Mirror|I18n'` grün.
- [ ] **Schritt 5:** Commit „ESPPDH: Reiter zeigt ESPHome-Aktualisierung, Schalter und Knöpfe“.

### Task 6: Handbuch, Version, Änderungsprotokoll, Gesamtlauf

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `esppdh`, Abschnitt `hb-esp-env` und Sicherheit), `web/templates/widgets/help_figures.gohtml` (Skizze des Reiters, falls sie die Karte zeigt), `VERSION`, `CHANGELOG.md`, `AGENTS.md`, `scripts/install-esphome.sh` (Kopfkommentar), `esphome.go` (Kommentar „Mindestversion“)

- [ ] **Schritt 1: Handbuch.** Neuer Unterabschnitt „ESPHome bleibt von selbst aktuell“ mit „So geht's“: was täglich geschieht (prüfen · daneben einrichten · Probe · umschalten), 3 Tage Karenz, was bei gescheiterter Probe passiert und wer die Meldung bekommt, Schalter, „Jetzt prüfen“, „Zurück zur vorigen Version“, dass jeder Server selbst prüft und kurz verschiedene Versionen haben kann, dass Firmware nie von selbst neu gebaut wird, dass ohne Internet die Version bleibt, Platzbedarf (3 GB frei). Den alten Satz „Version kommt mit PDH-Updates“ ersetzen: die Datei im PDH ist die Mindestversion.
- [ ] **Schritt 2:** `VERSION` → `0.82.1`; `CHANGELOG.md` neuer oberster Eintrag `## [0.82.1] – <Datum>`: `### Neu` (ESPHome hält sich selbst aktuell, mit Probe; Schalter und Knöpfe; Hinweis am Gerät), `### Geändert` („veraltet“ nur noch unter der Mindestversion; PDH-Updates stufen ESPHome nicht mehr herab), `### Sicherheit` (Agent holt nur `esphome` in geprüfter Versionsform, Karenz 3 Tage). `AGENTS.md`: Punkt 0.82.1 auf „gebaut, Freigabe offen“.
- [ ] **Schritt 3:** `go build ./... && go vet ./... && go test ./...` (Labor-DB, siehe `deploy/lab/mssql/LIESMICH.md`), `GOOS=linux go vet ./...`; `go test -race ./internal/modules/esppdh ./cmd/update-agent` im Linux-Container.
- [ ] **Schritt 4:** Commit „Version 0.82.1: ESPHome hält sich selbst aktuell“.
- [ ] **Schritt 5 (Controller, nach Freigabe des Anwenders):** Prüfung vor dem Merge laut AGENTS.md, Push, Merge, Verbund-Update; am Testverbund: Umgebung einrichten, „Jetzt prüfen“ auf beiden Servern, Reiter in Fiori/Fluent, hell/dunkel, 320–1366 px.

## Was nur am Testverbund oder vom Anwender geprüft werden kann

- Echter Lauf gegen PyPI mit echtem pip als root über systemd-run; Dauer und Platzbedarf.
- Probebau als Benutzer `pdh` mit einer zweiten Umgebung (gemeinsamer PlatformIO-Zwischenspeicher).
- Verhalten, wenn ESPHome tatsächlich eine unverträgliche Version veröffentlicht.
