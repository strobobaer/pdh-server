# ESPPDH: Firmware übers Netz aufspielen (0.82.23) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Das PDH spielt gebaute ESPPDH-Firmware per Knopf (je Gerät) und als Sammelaktion („alle veralteten“) übers Netz auf; das Gerät meldet seine Firmware-Kennung.

**Architecture:** Die erzeugte YAML bekommt `ota: platform: esphome` (Kennwort je Gerät), `safe_mode:` und einen Info-Wert mit der Bau-Kennung (nicht im `ConfigHash`). Ein neuer `Runner.Upload` ruft `esphome upload … --ota-platform esphome --file firmware.ota.bin` wie der Bau in einem eigenen Arbeitsordner auf. Aufträge stehen in `esppdh_ota_runs`; ein Lauf hält `runMu` wie der Bau; Erfolg erst, wenn das Gerät die neue Kennung meldet.

**Tech Stack:** Go 1.27, chi, html/template + htmx, pgx, ESPHome 2026.10.x (Umgebung des Servers).

**Spec:** `docs/superpowers/specs/2026-11-22-esppdh-ota-design.md`

## Global Constraints

- Version 0.82.23 (bei Merge erneut gegen `origin/main` prüfen), Migration 194, Eintrag in `migrations/checksums.txt` (`PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`).
- Neues Recht `esppdh.ota` „ESPPDH: Firmware übers Netz aufspielen“, Kategorie „Daten & Schnittstellen“, an alle Rollen/Benutzer mit `esppdh.edit` (samt `department_id`; global zuerst wie Migration 193).
- Einzelgerät: `esppdhCan(r, "esppdh.ota", id)`; Sammelaktion: Recht muss **überall** gelten (`h.rbac.Scope(...).all`).
- Kein Aufspielen am Spiegel (`h.Mirror().On`), in der Wartung (`h.MaintLocked()`), ohne Umgebung oder wenn ESPPDH aus ist (`esppdhBuildBlocked`).
- Nur an eine Adresse, die `esppdhAddressOK` besteht **und** in einem freigegebenen Netz liegt: nicht leeres `NetSubnet` des Geräts und (WLAN) `net_wlans.esppdh_ok` bzw. (Kabel) eingetragenes `segment_id`.
- Veraltet (Sammelaktion) = `ConfigHash` ≠ `built_hash` oder gemeldete Kennung ≠ letzter erfolgreicher Bau; ESPHome-Version allein zählt nicht.
- Kennwörter nie im Protokoll (`esppdh.Redact` mit `SecretValues`).
- Gemeinsame Vorlage nie direkt ausführen (Wächter `TestNoDirectSharedTemplateExecute`): Fragmente über `h.renderFragment`.
- Texte deutsch; Fiori/Fluent, hell/dunkel, 320–1366 px.

## Review Focus

1. Gerät ohne Update-Baustein (alte Firmware, keine Kennung gemeldet): Knopf erklärt „erst einmal per USB“, kein Upload-Versuch. → Test Task 4.
2. Adresse im falschen Netz / kein Netz eingetragen / WLAN ohne Haken: kein Upload, Grund genannt. → Test Task 4.
3. Gleichzeitig Bau und Aufspielen am selben Gerät oder zwei Aufspielungen: höchstens ein aktiver Lauf je Gerät (Unique-Index), `runMu` serialisiert ESPHome. → Test Task 4.
4. Gerät meldet nach dem Upload nicht die neue Kennung (z. B. Rückfall in den Sicherheitsmodus): Status „aufgespielt, meldet sich nicht“, nicht „ok“. → Test Task 4.
5. Sammelaktion: ein Fehler überspringt nur dieses Gerät; Abbrechen beendet den Rest; Geräte ohne Baustein/Adresse werden mit Grund übersprungen. → Test Task 5.

---

### Task 1: Firmware – Update-Baustein, Sicherheitsmodus, Firmware-Kennung

**Files:**
- Modify: `internal/modules/esppdh/yaml.go` (`Device` :31, Secrets :57, `ValidateDevice` :137/:212, `Generate` :228/:279/:289, `ConfigHash` :305), `internal/modules/esppdh/topics.go` (:22–60), `internal/modules/esppdh/samples.go`
- Test: `internal/modules/esppdh/ota_yaml_test.go` (neu), Golden `testdata/*.yaml` neu schreiben

**Interfaces:**
- Produces: `Device.OTAPassword string`, `Device.FirmwareID string` (Bau-Kennung, geht **nicht** in `ConfigHash` ein); `const secretOTA = "ota_password"`; `const topicFirmware = "firmware"`, `nameFirmware = "Firmware"`; `func otaNode() *yaml.Node` (`- platform: esphome`, `password: !secret ota_password`); `func firmwareNode(d Device) *yaml.Node` (text_sensor `platform: template`, Name „Firmware“, `lambda: return {"<FirmwareID>"};`, `update_interval: 60s`, `state_topic: infoTopic(slug, topicFirmware)`, `retain: true`).
- [ ] **Step 1: Tests:** `TestGenerateHasOTAAndSafeMode` (YAML enthält `ota:` mit `platform: esphome` und `password: !secret ota_password`, `safe_mode:`; secrets.yaml enthält `ota_password`); `TestFirmwareIDNotInConfigHash` (gleiches Gerät mit `FirmwareID` "a" und "b" → gleicher `ConfigHash`, aber `Generate` unterscheidet sich); `TestFirmwareTopic` (`Topics(d)` enthält `esppdh/<slug>/info/firmware`, Gruppe „Gerät“, Interval 0); `TestValidateDeviceNeedsOTAPassword` („Das Kennwort für Updates übers Netz fehlt.“); bestehende `TestTopicsMatchGeneratedYAML`, `TestGenerateNoSecretsInConfig` grün; `TestEsphomeConfigAccepts` (läuft mit Docker) akzeptiert die neue YAML.
- [ ] **Step 2:** `go test ./internal/modules/esppdh -run 'TestGenerateHasOTA|TestFirmwareID|TestFirmwareTopic|TestValidateDeviceNeedsOTA'` → FAIL.
- [ ] **Step 3:** Implementieren; `ConfigHash` erzeugt mit `FirmwareID = ""`; Wurzelreihenfolge: … web_server, `ota`, `safe_mode`, mqtt …; `samples.go` setzt `OTAPassword`; Golden neu: `go test ./internal/modules/esppdh -run TestGenerateGolden -update`.
- [ ] **Step 4:** `go test ./internal/modules/esppdh` → PASS.
- [ ] **Step 5:** Commit `ESPPDH: Firmware mit Update-Baustein, Sicherheitsmodus und Firmware-Kennung (0.82.23)`.

### Task 2: Runner – Aufspielen mit ESPHome

**Files:**
- Modify: `internal/modules/esppdh/build.go` (neben `Config` :166)
- Test: `internal/modules/esppdh/upload_test.go`

**Interfaces:**
- Produces: `type UploadJob struct { ID, Slug, Address string; Config, Secrets, Firmware []byte; SecretValues []string }`; `func (r *Runner) Upload(ctx context.Context, job UploadJob, progress func(string)) (log string, err error)` – Ordner wie `prepareBuildDir`, schreibt `firmware.ota.bin` (0600), führt `upload esppdh.yaml --device <Address> --ota-platform esphome --file firmware.ota.bin` mit 5 min Zeitlimit über `execute` aus, schwärzt mit `Redact`, löscht den Ordner immer; Adresse muss eine IPv4 sein (sonst Fehler ohne Aufruf).
- [ ] **Step 1: Tests** (mit `Runner.Command`-Ersatz): `TestUploadArgs` (Argumente genau wie oben; Datei `firmware.ota.bin` im Arbeitsordner mit dem Inhalt; Ordner danach weg); `TestUploadRedacts` (Kennwort im Ausgabetext → geschwärzt); `TestUploadRejectsBadAddress` (`"evil; rm"` → Fehler, kein Aufruf); `TestUploadFailureReturnsError` (Befehl mit Exit 1 → Fehler, Log enthält Ausgabe).
- [ ] **Step 2:** `go test ./internal/modules/esppdh -run TestUpload` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./internal/modules/esppdh` → PASS.
- [ ] **Step 5:** Commit `ESPPDH: Firmware mit ESPHome übers Netz aufspielen (Runner) (0.82.23)`.

### Task 3: Daten, Recht, Kennwort, Bau setzt Kennung

**Files:**
- Create: `migrations/194_esppdh_ota.up.sql`
- Modify: `migrations/checksums.txt`, `internal/web/esppdh_store.go` (`esppdhDevice` :68, `esppdhSelect` :92, `esppdhScan` :102, neu `esppdhOTAPassword` nach :116), `internal/web/esppdh_check.go` (`esppdhResolve` :128–139: `dev.OTAPassword`), `internal/web/esppdh_build.go` (`esppdhJobFor` :313–346: `dev.FirmwareID = c.ID`, `SecretValues` + OTA-Kennwort), `internal/web/esppdh_update.go` (`esppdhProbeJob` :815: Kennwort)
- Test: `internal/web/esppdh_ota_store_test.go` (Build-Tag `integration`, Labor-DB)

**Interfaces:**
- Produces: Spalte `esppdh_devices.ota_password_enc`; Tabelle `esppdh_ota_runs (id uuid pdh_uuid_v7, device_id → esppdh_devices ON DELETE CASCADE, build_id → esppdh_builds ON DELETE SET NULL, batch_id uuid NULL, status text CHECK IN ('queued','running','ok','unconfirmed','failed','canceled','skipped'), log text, address text, requested_by uuid NULL, created_at, started_at, finished_at)`, Unique-Index „ein aktiver Lauf je Gerät“ (`status IN ('queued','running')`); Recht `esppdh.ota`; `func (h *Handler) esppdhOTAPassword(ctx context.Context, d *esppdhDevice) (string, error)` (Muster `esppdhWebPassword`).
- [ ] **Step 1: Tests:** `TestEsppdhOTAPasswordLazy` (erstes Lesen legt an, zweites liefert dasselbe, Spiegel → Fehler); `TestEsppdhOTARightGranted` (Rolle mit `esppdh.edit` überall → `esppdh.ota` überall nach Migration – Migration im Test erneut ausführen wie `TestCompanyEditGrantPrefersGlobal`); `TestEsppdhJobForSetsFirmwareID` (YAML des Bauauftrags enthält die Lauf-Kennung, `ConfigHash` des Geräts unverändert).
- [ ] **Step 2:** `go test -tags integration ./internal/web -run 'TestEsppdhOTA|TestEsppdhJobForSetsFirmwareID'` → FAIL.
- [ ] **Step 3:** Migration + Code; Prüfsumme eintragen.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -run Esppdh` grün.
- [ ] **Step 5:** Commit `ESPPDH: Kennwort und Aufträge für Updates übers Netz, Recht esppdh.ota (0.82.23)`.

### Task 4: Einzelgerät „Firmware aufspielen“

**Files:**
- Create: `internal/web/esppdh_ota.go`, `web/templates/widgets/esppdh_ota.gohtml`, `internal/web/esppdh_ota_test.go`
- Modify: `internal/web/handler.go` (Routen bei :1192), `internal/web/perm_gate.go` (:189–192), `web/templates/esppdh_device.gohtml` (nach :174), `internal/web/esppdh.go` (`ESPPDHDevicePageData` :576, Seite füllt `OTA esppdhOTAView`)

**Interfaces:**
- Produces:
  - `func esppdhOTANetOK(addr, subnet string, wired, wlanReleased, segmentSet bool) (bool, string)` – rein: Subnetz leer → „Für das Netz des Geräts ist auf der Seite Netzwerk kein Adressbereich eingetragen“; WLAN ohne Haken → „Das WLAN ist nicht für ESPPDH-Geräte freigegeben“; Kabel ohne Netz → „Netz am Kabel ist nicht eingetragen“; Adresse nicht im Subnetz → „Adresse … liegt in keinem freigegebenen Netz“.
  - `type esppdhOTAView struct { CanPush bool; Reason string; Address, Installed, Latest string; Run *esppdhOTARunView; PortHint string }`.
  - `func (h *Handler) esppdhOTAState(ctx context.Context, r *http.Request, d esppdhDevice) esppdhOTAView` – prüft Recht, Sperren, letzten ok-Bau, gemeldete Kennung (leer → „Gerät hat noch keinen Update-Baustein – einmal per USB flashen“), Adresse (`esppdhAddressOK`) und Netz (`esppdhOTANetOK`).
  - `type esppdhOTADeps struct { upload func(ctx context.Context, job esppdh.UploadJob, progress func(string)) (string, error); reported func(ctx context.Context) string; sleep func(time.Duration) }` und `func esppdhOTARun(ctx context.Context, d esppdhOTADeps, job esppdh.UploadJob, want string, wait time.Duration) (status, log string)` – Upload; danach bis `wait` (3 min, alle 5 s) auf gemeldete Kennung == `want` warten → `ok`, sonst `unconfirmed`; Upload-Fehler → `failed`.
  - `POST /import/esppdh/{id}/ota` (`esppdh.ota` am Gerät) → Auftrag anlegen (Unique-Index → „läuft schon“), im Hintergrund `runMu` nehmen, Lauf ausführen, Status speichern; `GET /import/esppdh/{id}/ota/{rid}` → Fragment mit Fortschritt (Polling 2 s wie `esppdh-build`).
- [ ] **Step 1: Tests:** `TestEsppdhOTANetOK` (Tabelle: alle vier Gründe, gültiger Fall ok); `TestEsppdhOTARunStates` (Upload-Fehler → failed; Upload ok + Kennung kommt → ok; Kennung bleibt alt → unconfirmed, ohne echtes Warten: `sleep` ersetzt); `TestEsppdhOTAViewNeedsUSBFirst` (keine gemeldete Kennung → `CanPush=false`, Reason enthält „per USB“); `TestEsppdhOTAPanelRenders` (Knopf „Firmware aufspielen“ nur mit `CanPush`; sonst Grund; Portsatz „Port 3232“).
- [ ] **Step 2:** `go test ./internal/web -run 'TestEsppdhOTA'` → FAIL.
- [ ] **Step 3:** Implementieren; Gate-Regel `gr(wPost, "esppdh.ota", esppdhRef, false, ^/import/esppdh<id>/ota$)`; Geräteseite: Karte „Firmware übers Netz“ nach dem Flash-Block mit installierter/neuester Kennung, Grund oder Knopf (Bestätigung), Hinweis Update-Port (3232 ESP32, 8266 ESP8266) und Netzfreigabe.
- [ ] **Step 4:** `go test ./internal/web -run 'Esppdh|TestNoDirectSharedTemplateExecute'` → PASS.
- [ ] **Step 5:** Commit `ESPPDH: Firmware je Gerät übers Netz aufspielen (0.82.23)`.

### Task 5: Sammelaktion „Alle veralteten Geräte aktualisieren“

**Files:**
- Create: `internal/web/esppdh_ota_batch.go`, `internal/web/esppdh_ota_batch_test.go`
- Modify: `web/templates/esppdh.gohtml` (Karte „Geräte“ :41–60: Knopf und Stand je Zeile), `internal/web/esppdh.go` (`ESPPDHPageData` :346), `internal/web/handler.go`, `internal/web/perm_gate.go`

**Interfaces:**
- Consumes: `esppdhOTAState`, `esppdhOTARun` (Task 4).
- Produces:
  - `func esppdhOutdated(configHash, builtHash, reported, lastOK string) bool` – `configHash != builtHash || reported != lastOK`.
  - `type esppdhBatchItem struct { DevID, Name, Reason string; NeedsBuild bool }`, `type esppdhBatchResult struct { DevID, Status, Note string }`.
  - `type esppdhBatchDeps struct { list func(ctx context.Context) []esppdhBatchItem; build func(ctx context.Context, devID string) (buildID string, err error); push func(ctx context.Context, devID, buildID string) string; canceled func() bool }` und `func esppdhBatchRun(ctx context.Context, d esppdhBatchDeps) []esppdhBatchResult` – je Gerät: mit `Reason` → `skipped`; sonst bei `NeedsBuild` bauen und warten, dann aufspielen; Fehler → nächstes Gerät; `canceled()` → Rest `canceled`.
  - `POST /import/esppdh/ota-all` (Recht überall) → Bestätigungsliste per htmx; `POST /import/esppdh/ota-all/start` → Lauf mit `batch_id`; `POST /import/esppdh/ota-all/{batch}/cancel`; Übersicht zeigt je Gerät den Stand des letzten Laufs der Sammelaktion.
- [ ] **Step 1: Tests:** `TestEsppdhOutdated` (Tabelle); `TestEsppdhBatchRun` (drei Geräte: eins mit Reason → skipped mit Grund, eins mit Fehler beim Aufspielen → failed, Rest läuft weiter; eins mit `NeedsBuild` → erst build dann push); `TestEsppdhBatchCancel` (`canceled` nach dem ersten → Rest canceled); `TestEsppdhBatchNeedsGlobalRight` (Recht nur in Abteilung → 403).
- [ ] **Step 2:** `go test ./internal/web -run 'TestEsppdhOutdated|TestEsppdhBatch'` → FAIL.
- [ ] **Step 3:** Implementieren (ein Sammellauf gleichzeitig; Stand in `esppdh_ota_runs` mit `batch_id`).
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `ESPPDH: alle veralteten Geräte nacheinander aktualisieren (0.82.23)`.

### Task 6: Handbuch, Änderungsprotokoll, Version, offene Punkte

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `esppdh`: How-to vor :2793; Rechte :2325–2332), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (Punkt 21)

- [ ] **Step 1:** Handbuch: „So geht's: Firmware übers Netz aufspielen“ (einmal per USB nötig; Netz auf der Seite Netzwerk freigeben und Adressbereich eintragen; Firewall vom Server zum Gerät Port 3232/8266; Knopf; Erfolg erst, wenn das Gerät die neue Firmware meldet), „So geht's: alle veralteten Geräte aktualisieren“, Recht; CHANGELOG 0.82.23 (`### Neu`, `### Geändert`: „Nach dem Update gelten alle ESPPDH-Geräte einmal als ‚neu bauen‘ und brauchen die neue Firmware einmal per USB“, `### Sicherheit`: neues Recht).
- [ ] **Step 2:** `go build ./... && go vet ./...`, volle Reihe `go test ./...` mit Labor-DB, `go test -tags integration ./internal/web -run 'Esppdh|Terminal|Anlagenterminal|Company'` → PASS.
- [ ] **Step 3:** Commit `0.82.23: Handbuch und Änderungsprotokoll`.
