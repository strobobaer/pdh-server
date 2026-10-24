# Ausfallschutz Teil 1 – Einzelbetrieb: Implementierungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein Einzel-Server sichert Postgres (Grundsicherung + WAL-Archiv), Dateien und Konfiguration von PDH, Nextcloud und n8n laufend auf ein Netzlaufwerk; `pdh-restore.sh` baut daraus einen neuen Server bzw. eine VM auf (neuester Stand oder Zeitpunkt); das PDH zeigt Status, alarmiert und führt den Status der VM-Sicherung.

**Architecture:** Die Arbeit macht der Update-Agent (root, systemd) mit eigener Schleife – unabhängig davon, ob das PDH läuft. Postgres archiviert per `archive_command` in einen lokalen Zwischenspeicher (Spool); der Agent komprimiert und verschiebt die WAL-Dateien aufs Laufwerk, macht nach Zeitplan `pg_basebackup` (ganzer Cluster, also auch die Nextcloud-Datenbank) und rsync. Das PDH konfiguriert den Agenten über `/v1/dr/*`, zeigt den Status im neuen Reiter „Ausfallschutz“ der Server-Einstellungen und meldet Probleme an die Admins.

**Tech Stack:** Go 1.24 (cmd/update-agent, internal/web), Bash (Host-Skripte, `pdh-restore.sh`), PostgreSQL aus dem Ubuntu-Paket (`runuser -u postgres`, `pg_basebackup`), rsync, gzip, Docker (nur für den n8n-Container).

**Spec:** `docs/superpowers/specs/2026-10-24-ausfallschutz-design.md` (Abschnitte 2, 11, 13, 15.1)

## Global Constraints

- Produktion = systemd-Installation. Im Agenten-Modus `docker` antworten alle `/v1/dr/*`-Aufrufe mit `{"ok":false,"error":"Ausfallschutz-Sicherung nur bei systemd-Installationen (Docker ist das Testsystem)"}`; der Reiter zeigt das als Hinweis.
- Datenbank muss lokal sein: `PDH_DATABASE_HOST` leer, `localhost`, `127.0.0.1`, `::1` oder beginnt mit `/` – sonst lehnt das PDH das Einschalten ab mit „Ausfallschutz-Sicherung braucht die Datenbank auf diesem Server“.
- Standardwerte: Grundsicherung täglich um 02 Uhr (`base_hour` 0–23), Aufbewahrung 14 Tage (`keep_days` 1–365), Dateiabgleich alle 60 min (`file_sync_minutes` 5–1440), WAL spätestens alle 5 min (`wal_max_minutes` 1–60 → `archive_timeout`), VM-Sicherung Warnung nach 7 Tagen (`dr.vm_max_days` 1–90). Alle Werte im Reiter einstellbar.
- Nextcloud und n8n werden mitgesichert, wenn vorhanden: Nextcloud-Verzeichnis aus der PDH-Einstellung (Standard `/var/www/nextcloud`, vorhanden wenn `config/config.php` existiert), n8n-Container aus `PDH_N8N_CONTAINER` bzw. Standard `pdh-admin-n8n`. Fehlen sie, wird das still übersprungen und im Status angezeigt („nicht gefunden“).
- Zielordner: `<Laufwerk>/PDH-Ausfallschutz/<Hostname>` (Hostname auf `[a-z0-9-]` reduziert). Layout: `wal/<WAL>.gz`, `base/<JJJJMMTTTHHMMSSZ>/{base.tar.gz,info.json}`, `files/<label>/…`, `papierkorb/<JJJJ-MM-TT>/<label>/…`, `pdh-restore.sh`.
- Kein Benutzerwert landet im Skripttext: Host-Skripte bekommen Werte nur über Umgebungsvariablen (`hostCommandEnv`).
- Die bestehende ZIP-Datensicherung bleibt unverändert.
- Texte in Anwendersprache (Deutsch, kurze Sätze); Handbuch und `CHANGELOG.md`/`VERSION` laut AGENTS.md.

## Review Focus

1. Netzlaufwerk nicht eingebunden oder voll, während WAL verschoben wird → Datei bleibt im Spool, nichts geht verloren; Alarm, wenn der Spool > 2 GiB oder die älteste Datei > 1 h alt ist. (Task 3)
2. Einschalten auf einem Server ohne `conf.d` bzw. mit `wal_level = minimal` → klare Fehlermeldung, keine halb geschriebene Konfiguration, Postgres läuft unverändert weiter. (Task 2)
3. n8n-Container wird für den Abgleich pausiert und der Abgleich bricht ab (Fehler, Agent-Neustart) → Container muss trotzdem wieder laufen (`trap … docker unpause`; beim Agent-Start pausierten Container fortsetzen). (Task 5)
4. Abbruch der Grundsicherung (Platte voll, Agent-Neustart) → `.part`-Ordner zählt nie als Sicherung, wird beim nächsten Lauf entfernt; Aufräumen löscht nie die neueste vollständige Grundsicherung oder WAL, die sie braucht. (Task 4)
5. `pdh-restore.sh --time` vor der ältesten Grundsicherung → Abbruch mit Meldung statt stiller Wiederherstellung auf einen anderen Stand. (Task 7)

---

## Dateiübersicht

| Datei | Aufgabe |
|---|---|
| `cmd/update-agent/dr.go` (neu) | Typen, Zustand, Validierung, Endpunkte `/v1/dr/*`, Schleife |
| `cmd/update-agent/dr_postgres.go` (neu) | Postgres-Archivierung ein/aus, Grundsicherung |
| `cmd/update-agent/dr_files.go` (neu) | WAL verschieben, Aufräumen, rsync, Papierkorb |
| `cmd/update-agent/dr_apps.go` (neu) | Nextcloud- und n8n-Quellen erkennen, n8n pausieren |
| `cmd/update-agent/dr_test.go` (neu) | Tests aller reinen Funktionen + Skript-Syntax |
| `cmd/update-agent/main.go` | Routen registrieren, Schleife starten |
| `install-update-agent.sh` | Paket `rsync` |
| `scripts/install-nextcloud-nginx-postgres.sh` | Option `--restore` (Pakete/Webserver ohne Neuinstallation) |
| `scripts/pdh-restore.sh` (neu), `restore.go` (neu) | Wiederaufbau-Skript, eingebettet als `pdh.RestoreScript` |
| `internal/web/dr.go` (neu), `internal/web/dr_test.go` (neu) | Reiter-Daten, Handler, Agent-Aufrufe, Alarme |
| `web/templates/widgets/dr.gohtml` (neu), `web/templates/server_config.gohtml`, `internal/web/server_config.go`, `internal/web/handler.go` | Reiter „Ausfallschutz“ |
| `web/templates/help.gohtml`, `internal/web/help.go`, `web/templates/widgets/help_figures.gohtml` | Handbuch-Kapitel `ausfallschutz` |
| `CHANGELOG.md`, `VERSION`, `README.md` | Version, Hinweis |

---

### Task 1: Agent – Konfiguration, Zustand, Endpunkte

**Files:**
- Create: `cmd/update-agent/dr.go`, `cmd/update-agent/dr_test.go`
- Modify: `cmd/update-agent/main.go:75-91`

**Interfaces:**
- Produces (JSON-Vertrag mit dem PDH, Task 8 spiegelt ihn):
  ```go
  type drConfig struct {
      Enabled         bool   `json:"enabled"`
      Target          string `json:"target"`            // absoluter Ordner auf dem Laufwerk
      BaseHour        int    `json:"base_hour"`
      KeepDays        int    `json:"keep_days"`
      FileSyncMinutes int    `json:"file_sync_minutes"`
      WALMaxMinutes   int    `json:"wal_max_minutes"`
      NextcloudDir    string `json:"nextcloud_dir"`     // "" = /var/www/nextcloud
      N8NContainer    string `json:"n8n_container"`     // "" = pdh-admin-n8n
  }
  type drBase struct {
      Name    string    `json:"name"`
      Created time.Time `json:"created"`
      Bytes   int64     `json:"bytes"`
  }
  type drStatus struct {
      OK           bool      `json:"ok"`
      Supported    bool      `json:"supported"`
      Config       drConfig  `json:"config"`
      Archiving    bool      `json:"archiving"`
      SpoolFiles   int       `json:"spool_files"`
      SpoolBytes   int64     `json:"spool_bytes"`
      SpoolOldest  time.Time `json:"spool_oldest"`
      LastWALShip  time.Time `json:"last_wal_ship"`
      LastWALFile  string    `json:"last_wal_file"`
      LastBase     time.Time `json:"last_base"`
      LastFileSync time.Time `json:"last_file_sync"`
      Sources      []string  `json:"sources"`  // Labels des letzten Abgleichs, z. B. "uploads", "nextcloud-data", "n8n-compose"
      Missing      []string  `json:"missing"`  // z. B. "Nextcloud nicht gefunden"
      Bases        []drBase  `json:"bases"`
      Running      string    `json:"running"` // "", "base", "files"
      Error        string    `json:"error"`
      ErrorAt      time.Time `json:"error_at"`
  }
  ```
- Gemeinsame Typen für Task 4/5/7 (hier anlegen, damit jede Aufgabe für sich kompiliert):
  ```go
  type drSource struct {
      Label  string   `json:"label"`
      Path   string   `json:"path"`
      File   bool     `json:"file"`
      Filter []string `json:"filter,omitempty"`
  }
  type drVolume struct {
      Name       string `json:"name"`
      Mountpoint string `json:"mountpoint"`
  }
  type drN8NInfo struct {
      Container  string     `json:"container"`
      ComposeDir string     `json:"compose_dir"`
      Volumes    []drVolume `json:"volumes"`
  }
  ```
- `func drDefaults() drConfig` – Werte aus Global Constraints, `Enabled=false`.
- `func validateDRConfig(c *drConfig) error` – Grenzen aus Global Constraints; `Target`, `NextcloudDir` absolut, `filepath.Clean(x)==x`, kein `..`, keine Steuerzeichen; `Target` liegt unter `mountRoot()` oder ist bei `Enabled` ein existierender Ordner (lokales Laufwerk); `N8NContainer` passt auf `^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`.
- Zustand in `<PDH_DR_STATE_DIR|/var/lib/pdh-dr>/state.json` (Config + letzte Zeiten); `func (a *updateAgent) drLoad() error`, `func (a *updateAgent) drSave() error`; Feld `dr drState` im Agenten mit eigenem `sync.Mutex`.
- Endpunkte: `GET /v1/dr/status` → `drStatus`; `POST /v1/dr/config` (Body `drConfig`) → `{"ok":true}` bzw. Fehler; `POST /v1/dr/run` (Body `{"what":"base"|"files"}`) → startet asynchron, `{"ok":false,"error":"läuft bereits"}` wenn `Running != ""`.
- `func (a *updateAgent) drSupported() bool` – `a.mode == "systemd"`.

- [ ] **Step 1: Tests** – `TestValidateDRConfig` (Tabelle): Standardwerte + Ziel `/mnt/pdh/sicherung/PDH-Ausfallschutz/srv1` gültig; `keep_days` 0 und 366, `base_hour` 24, `file_sync_minutes` 4, `wal_max_minutes` 61, Ziel `relativ/pfad`, `/mnt/pdh/../etc`, `"/mnt/pdh/x\n"`, `n8n_container` `"a;b"` ungültig. `TestDRStateRoundTrip`: `PDH_DR_STATE_DIR=t.TempDir()`, speichern/laden ergibt gleiche Config. `TestDRDockerRefused`: Agent mit `mode:"docker"`, `GET /v1/dr/status` via `httptest` → `supported:false` und Meldung aus Global Constraints.
- [ ] **Step 2:** `go test ./cmd/update-agent/ -run 'DR'` → FAIL (undefiniert).
- [ ] **Step 3:** `dr.go` implementieren, Routen in `main.go` registrieren (`agent.authorized(...)`), `agent.drLoad()` beim Start.
- [ ] **Step 4:** `go test ./cmd/update-agent/` → PASS.
- [ ] **Step 5:** Commit `feat(agent): Ausfallschutz-Konfiguration und Status-Endpunkte`.

### Task 2: Agent – Postgres-Archivierung ein/aus

**Files:**
- Create: `cmd/update-agent/dr_postgres.go`
- Test: `cmd/update-agent/dr_test.go`

**Interfaces:**
- Consumes: `drConfig`, `a.hostCommandEnv(ctx, false, script, env)` (`cmd/update-agent/nextcloud.go:75`).
- Produces:
  - `const drSpool = "/var/lib/postgresql/pdh-wal-spool"`
  - `func drPostgresConf(walMaxMinutes int) string` – Inhalt von `conf.d/pdh-ausfallschutz.conf`:
    ```
    # PDH Ausfallschutz – vom Update-Agenten verwaltet, nicht von Hand ändern
    archive_mode = on
    archive_command = 'test ! -f /var/lib/postgresql/pdh-wal-spool/%f && cp %p /var/lib/postgresql/pdh-wal-spool/%f.part && mv /var/lib/postgresql/pdh-wal-spool/%f.part /var/lib/postgresql/pdh-wal-spool/%f'
    archive_timeout = <walMaxMinutes>min
    ```
  - `const drEnableScript`, `const drDisableScript` (Bash; Dateiinhalt nur aus Env `PDH_DR_CONF`).
  - `func (a *updateAgent) drApplyPostgres(ctx context.Context, c drConfig) (archiving bool, err error)`.
- Enable-Skript: `config_file` per `runuser -u postgres -- psql -Atc 'SHOW config_file'`; Abbruch mit Meldung, wenn `<dir>/conf.d` fehlt oder `SHOW wal_level` = `minimal`; Spool anlegen (`install -d -o postgres -g postgres -m 700`); Datei über `.tmp` + `mv` schreiben; `SHOW archive_mode` = `off` → `systemctl restart postgresql`, sonst `SELECT pg_reload_conf()`; danach muss `archive_mode` = `on` sein, sonst Datei entfernen, Neustart, Fehler. Letzte Ausgabezeile `ARCHIVING=1`.
- Disable-Skript: Datei entfernen, `systemctl restart postgresql`; Spool bleibt, bis die Schleife ihn geleert hat; Ausgabe `ARCHIVING=0`.
- `POST /v1/dr/config` ruft `drApplyPostgres` nur, wenn sich `Enabled` oder `WALMaxMinutes` ändert.

- [ ] **Step 1: Tests** – `TestDRPostgresConf`: `drPostgresConf(5)` enthält `archive_timeout = 5min` und genau das `archive_command` oben. `TestDRScriptsSyntax`: `drEnableScript`, `drDisableScript` per `bash -n` (Muster `nextcloud_test.go:44`, ohne bash übersprungen; spätere Tasks ergänzen ihre Skripte hier). `TestDREnableScriptGuards`: enthält `conf.d`, `wal_level`, `minimal`, `systemctl restart postgresql`, `pg_reload_conf`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; in `POST /v1/dr/config` einbinden; Fehler in `drStatus.Error`.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(agent): Postgres-WAL-Archivierung für den Ausfallschutz`.

### Task 3: Agent – WAL verschieben und Schleife

**Files:**
- Create: `cmd/update-agent/dr_files.go`
- Modify: `cmd/update-agent/dr.go`, `cmd/update-agent/main.go`
- Test: `cmd/update-agent/dr_test.go`

**Interfaces:**
- Produces:
  - `func drHostPath(mode, p string) string` – `systemd`: `"/proc/1/root"+p`, sonst `p` (Dateizugriff im Host-Namensraum trotz `PrivateTmp`).
  - `func drShipWAL(spool, walDir string) (shipped int, last string, err error)` – je Datei ohne `.part`, sortiert: gzip nach `walDir/<f>.gz.part`, `fsync`, `rename` auf `.gz`, erst dann Quelle löschen; existiert `<f>.gz` schon mit gleicher entpackter Größe → Quelle löschen; Abbruch beim ersten Fehler (Rest bleibt). `.history`-Dateien genauso.
  - `func drSpoolInfo(spool string) (files int, bytes int64, oldest time.Time)`.
  - `func drBaseDue(last time.Time, hour int, now time.Time) bool` – fällig, wenn `last` null oder `now` ≥ heutiger Termin `hour:00` (Ortszeit) und `last` < diesem Termin.
  - `func drFilesDue(last time.Time, minutes int, now time.Time) bool`.
  - `func (a *updateAgent) drLoop(ctx context.Context)` – alle 60 s: Spool-Info, `drShipWAL` (auch bei `Enabled=false`, bis Spool leer); bei `Enabled`: Dateiabgleich (Task 5) und Grundsicherung (Task 4) wenn fällig, nie zwei Läufe gleichzeitig (`Running`); Fehler → `Error`/`ErrorAt`, erfolgreicher Lauf derselben Art löscht ihn.
- Vor jedem Lauf: Ziel existiert und ist beschreibbar (Testdatei anlegen/löschen), sonst Fehler „Netzlaufwerk nicht erreichbar: <Ziel>“, nichts im Spool löschen.

- [ ] **Step 1: Tests** – `TestDRShipWAL`: Spool mit `000000010000000000000001`, `…02`, `…03.part` → `…01.gz`, `…02.gz` (entpackt gleich Original) im Ziel, `.part` bleibt, `shipped=2`, `last="…02"`. `TestDRShipWALTargetMissing`: Ziel fehlt → Fehler, Spool unverändert. `TestDRShipWALIdempotent`: Ziel hat `…01.gz` schon → Quelle gelöscht, kein Fehler. `TestDRBaseDue` (feste Zone): last=null → true; last gestern 02:05, now heute 01:59 → false; now heute 02:00 → true; last heute 02:01, now heute 23:00 → false. `TestDRFilesDue`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; `go agent.drLoop(ctx)` in `main.go` nach `drLoad`.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(agent): WAL-Dateien aufs Netzlaufwerk verschieben`.

### Task 4: Agent – Grundsicherung und Aufräumen

**Files:**
- Modify: `cmd/update-agent/dr_postgres.go`, `cmd/update-agent/dr_files.go`
- Test: `cmd/update-agent/dr_test.go`

**Interfaces:**
- Consumes: `drHostPath`, `drBase`, `drSource`/`drN8NInfo` (Task 1; befüllt erst ab Task 5).
- Produces:
  - `const drBaseScript` – Env `PDH_DR_OUT` (Ordner `.part`): `start=$(runuser -u postgres -- psql -Atc "SELECT pg_walfile_name(pg_current_wal_lsn())")`; `runuser -u postgres -- pg_basebackup -D - -Ft -X fetch -z --checkpoint=fast > "$PDH_DR_OUT/base.tar.gz"`; `sync`; Ausgabe `START_WAL=…` und `PG_VERSION_NUM=…` (`SHOW server_version_num`).
  - `type drBaseInfo struct { Name string; Created time.Time; StartWAL string; PGVersionNum int; PDHVersion string; Commit string; RepoDir string; Sources []drSource; N8N *drN8NInfo }` (JSON snake_case) als `info.json`.
  - `func (a *updateAgent) drRunBase(ctx context.Context) error` – Name `time.Now().UTC().Format("20060102T150405Z")`, schreibt nach `base/.<name>.part/`, `info.json`, Umbenennen auf `base/<name>`, `pdh-restore.sh` (Task 7) ins Ziel kopieren, aufräumen. `PDHVersion` = `pdh.Version()`, `Commit` = `git -C <repoDir> rev-parse HEAD`; `Sources`/`N8N` = Ergebnis des letzten Abgleichs.
  - `func drPrunePlan(bases []drBaseInfo, keepDays int, now time.Time) (remove []string, oldestStart string)` – entfernt Grundsicherungen älter als `keepDays`, die neueste bleibt immer; `oldestStart` = `StartWAL` der ältesten behaltenen.
  - `func drPruneWAL(walDir, oldestStart string) (removed int, err error)` – löscht `*.gz` mit 24-stelligem Namen < `oldestStart` (String-Vergleich), `*.history.gz` bleiben.
  - `func drPruneTrash(trashDir string, keepDays int, now time.Time) error` – Tagesordner `JJJJ-MM-TT` älter als `keepDays`.
  - Beim Start eines Laufs alte `base/.*.part` löschen.

- [ ] **Step 1: Tests** – `TestDRPrunePlan`: 20, 10, 1 Tage alt, keep 14 → nur die 20-tägige weg, `oldestStart` = StartWAL der 10-tägigen; alle zu alt → neueste bleibt. `TestDRPruneWAL`: `…01.gz`, `…05.gz`, `…09.gz`, `00000002.history.gz`, oldest `…05` → nur `…01.gz` gelöscht. `TestDRPruneTrash`. `TestDRScriptsSyntax` + `drBaseScript` (enthält `-X fetch`, `--checkpoint=fast`).
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; `drLoop` ruft `drRunBase` bei `drBaseDue`; `POST /v1/dr/run {"what":"base"}`; `Bases` im Status aus `base/*/info.json`.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(agent): tägliche Grundsicherung mit Aufbewahrung`.

### Task 5: Agent – Dateiabgleich inkl. Nextcloud und n8n

**Files:**
- Create: `cmd/update-agent/dr_apps.go`
- Modify: `cmd/update-agent/dr_files.go`, `install-update-agent.sh:17-25`
- Test: `cmd/update-agent/dr_test.go`

**Interfaces:**
- Consumes: `drSource`, `drN8NInfo`, `drVolume` (Task 1).
- Produces:
  - `func drFileSources(repoDir string, exists func(string) bool) []drSource` – PDH, nur vorhandene: `uploads`→`<repo>/uploads`, `chat_files`→`<repo>/chat_files`, `env`→`<repo>/.env` (File), `etc-pdh`→`/etc/pdh`, `systemd`→`/etc/systemd/system` (Filter `--include=pdh*.service --exclude=*`), `wireguard-bin`→`/usr/local/bin/pdh-wireguard` (File).
  - `func drNextcloudSources(ncDir, dataDir string, exists func(string) bool) []drSource` – wenn `<ncDir>/config/config.php` existiert: `nextcloud-app`→`ncDir`, `nextcloud-data`→`dataDir` (wenn ≠ "" und nicht unter `ncDir`), `nginx`→`/etc/nginx`. `dataDir` liefert das Host-Skript `drNCDataScript` (Env `PDH_DR_NCDIR`): `php -r 'include getenv("PDH_DR_NCDIR")."/config/config.php"; echo $CONFIG["datadirectory"] ?? "";'`.
  - `func drParseN8N(inspectJSON []byte) (drN8NInfo, error)` – aus `docker inspect <container>`: Label `com.docker.compose.project.working_dir`, `Mounts` mit `Type=="volume"` (Name, Source).
  - `func drN8NSources(info drN8NInfo) []drSource` – `n8n-compose`→`ComposeDir` (falls gesetzt), je Volume `n8n-vol-<Name>`→`Mountpoint`.
  - `func drRsyncArgs(src drSource, target, day string) []string` – `-rtl --delete --backup --backup-dir=<target>/papierkorb/<day>/<label> --exclude=.git`, danach `src.Filter`; Verzeichnisse mit abschließendem `/`; Ziel `<target>/files/<label>/` (Datei: `<target>/files/<label>`). Keine Owner-/Rechte-Optionen (CIFS).
  - `func (a *updateAgent) drRunFiles(ctx context.Context) error` – Quellen sammeln (PDH, Nextcloud, n8n), je Quelle rsync über festes Host-Skript `drRsyncScript` (Argumente als `PDH_DR_ARG_0..N`); n8n-Volumes: vorher `docker pause <container>`, im Skript `trap 'docker unpause …' EXIT`; danach `drPruneTrash`; `Sources`/`Missing` im Status setzen.
  - Beim Agent-Start: ist der n8n-Container pausiert, `docker unpause`.
- `install-update-agent.sh`: `command -v rsync >/dev/null 2>&1 || missing_packages+=(rsync)`.
- Nextcloud wird nicht in den Wartungsmodus gesetzt (Abgleich läuft live; dokumentieren in Task 9).

- [ ] **Step 1: Tests** – `TestDRFileSources` (Fake-`exists`): `uploads`, `.env` vorhanden, `chat_files` nicht → Labels `uploads`, `env` + Host-Quellen laut Fake. `TestDRNextcloudSources`: mit `config.php` und Datenordner `/var/ncdata` → `nextcloud-app`, `nextcloud-data`, `nginx`; Datenordner unter `ncDir` → kein `nextcloud-data`; ohne `config.php` → leer. `TestDRParseN8N`: Beispiel-JSON (Label `/opt/n8n`, Volume `n8n_data` mit `/var/lib/docker/volumes/n8n_data/_data`, ein Bind-Mount) → ComposeDir `/opt/n8n`, genau ein Volume. `TestDRRsyncArgs`: `uploads` exakt `[-rtl --delete --backup --backup-dir=/t/papierkorb/2026-10-24/uploads --exclude=.git /r/uploads/ /t/files/uploads/]`; `systemd` endet mit `--include=pdh*.service --exclude=* /etc/systemd/system/ /t/files/systemd/`. `TestDRScriptsSyntax` + `drRsyncScript` (enthält `docker unpause` im `trap`), `drNCDataScript`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren, in `drLoop` und `POST /v1/dr/run {"what":"files"}` einbinden.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(agent): PDH, Nextcloud und n8n aufs Netzlaufwerk spiegeln`.

### Task 6: Nextcloud-Installer – Option `--restore`

**Files:**
- Modify: `scripts/install-nextcloud-nginx-postgres.sh`
- Test: `cmd/update-agent/nextcloud_test.go` (vorhandener `bash -n`-Test deckt die Datei ab; ergänzen: Hilfe-Text enthält `--restore`)

**Interfaces:**
- Produces: `sudo bash scripts/install-nextcloud-nginx-postgres.sh --restore [--php-version X] [--install-dir PATH] [--data-dir PATH]` – führt Paketinstallation und PHP-/PHP-FPM-/Redis-Einstellungen aus, **ohne** Datenbank/Rolle anzulegen, ohne `occ maintenance:install`, ohne vorhandene nginx-Site zu überschreiben; `--domain`, `--admin-pass`, `--db-pass` sind dann nicht Pflicht. Zum Schluss `occ maintenance:data-fingerprint` und `occ maintenance:mode --off` als `www-data`, `systemctl restart php*-fpm nginx`.

- [ ] **Step 1:** Test ergänzen (`--restore` in `usage`) → FAIL.
- [ ] **Step 2:** Option einbauen: Pflichtprüfung und Installationsschritte mit `if [[ $RESTORE != 1 ]]` umschließen.
- [ ] **Step 3:** `go test ./cmd/update-agent/ -run Nextcloud` → PASS.
- [ ] **Step 4:** Commit `feat: Nextcloud-Installer mit Wiederherstellungsmodus`.

### Task 7: Wiederaufbau-Skript `pdh-restore.sh`

**Files:**
- Create: `scripts/pdh-restore.sh`, `restore.go` (Paket `pdh`: `//go:embed scripts/pdh-restore.sh` → `var RestoreScript string`)
- Modify: `cmd/update-agent/dr_postgres.go` (Kopie nach `<target>/pdh-restore.sh`, 0755)
- Test: `cmd/update-agent/dr_test.go` (`TestRestoreScript`)

**Interfaces:**
- Consumes: Ziel-Layout, `info.json` (Task 4), `files/<label>` (Task 5), `--restore` (Task 6).
- Aufruf: `sudo bash pdh-restore.sh --from <…/PDH-Ausfallschutz/<Host>> [--time "JJJJ-MM-TT HH:MM:SS"] [--base <Name>] [--yes]`
- Ablauf (Ausgaben `info`/`ok`/`warn`/`fail` wie `internal/web/vpn_host.go:86-89`):
  1. root, Ubuntu; `--from` enthält `base/` und `wal/`; Grundsicherung: `--base` oder neueste mit `created` ≤ `--time` (ohne `--time`: neueste). Keine passende → `fail "Keine Grundsicherung vor <Zeit> – älteste ist <Name>"`. `info.json` mit `python3 -c` lesen.
  2. Postgres-Hauptversion = `pg_version_num / 10000`; `postgresql-<ver>` installieren, fehlt das Paket → PGDG-Archiv (`apt.postgresql.org`) einrichten.
  3. Sicherheitsabfrage (ohne `--yes`). Cluster `<ver>/main` stoppen, Datenverzeichnis leeren, `base.tar.gz` entpacken, `chown -R postgres:`; `recovery.signal`; in `postgresql.auto.conf`: `restore_command = 'gunzip -c <from>/wal/%f.gz > %p'`, `recovery_target_action = 'promote'`, bei `--time` `recovery_target_time = '<time>'`; starten; warten bis `SELECT pg_is_in_recovery()` = `f` (max. 30 min, Fortschritt); danach `restore_command`/`recovery_target_*` wieder entfernen. Enthält auch die Nextcloud-Datenbank und alle Rollen.
  4. PDH: Benutzer `pdh` anlegen, Repo nach `repo_dir` klonen (`https://github.com/strobobaer/pdh-server.git`) und auf `commit` setzen; `files/env` → `<repo>/.env` (0600, pdh), `files/uploads`, `files/chat_files` zurück (`chown -R pdh:`), `files/etc-pdh` → `/etc/pdh`, `files/systemd/pdh*.service` → `/etc/systemd/system/`, `files/wireguard-bin` → `/usr/local/bin/pdh-wireguard`; `go build` wie `install-update-agent.sh`; `install-update-agent.sh` ausführen; `systemctl enable --now pdh`.
  5. Nextcloud (wenn `files/nextcloud-app` vorhanden): Pfade aus `info.json`-`sources`; `nextcloud-app`, `nextcloud-data`, `nginx` zurückkopieren, `chown -R www-data:` für App und Daten; `bash <repo>/scripts/install-nextcloud-nginx-postgres.sh --restore --install-dir … --data-dir …`.
  6. n8n (wenn `info.json` `n8n` hat): `docker.io` + `docker-compose-v2` installieren; Compose-Ordner zurück; je Volume `docker volume create <Name>` und Inhalt aus `files/n8n-vol-<Name>` in den Mountpoint kopieren; `docker compose up -d` im Compose-Ordner.
  7. Abschluss-Hinweise: VPN-Installationsskript erneut ausführen, Cloudflare-Tunnel prüfen, `configure-pdh-admin-tools.sh` ausführen, Ausfallschutz im neuen Server wieder einschalten.

- [ ] **Step 1: Tests** – `TestRestoreScript`: `bash -n` auf `pdh.RestoreScript`; enthält `recovery.signal`, `recovery_target_action = 'promote'`, `gunzip -c`, `pg_is_in_recovery`, `apt.postgresql.org`, `Keine Grundsicherung vor`, `--restore`, `docker volume create`, `docker compose up -d`.
- [ ] **Step 2:** Test → FAIL.
- [ ] **Step 3:** Skript + `restore.go`; Agent kopiert es nach jeder Grundsicherung ins Ziel.
- [ ] **Step 4:** Test → PASS; `bash scripts/pdh-restore.sh --help` gibt den Aufruf aus.
- [ ] **Step 5:** Commit `feat: Wiederaufbau-Skript pdh-restore.sh`.

### Task 8: PDH – Reiter „Ausfallschutz“ und Alarme

**Files:**
- Create: `internal/web/dr.go`, `internal/web/dr_test.go`, `web/templates/widgets/dr.gohtml`
- Modify: `internal/web/server_config.go:182,276` (Feld `DR drView` + Laden), `web/templates/server_config.gohtml:60,204` (Reiter `data-tab="dr"` mit Punkt rot/grün, Bereich `data-pane="dr"`), `internal/web/handler.go:562` (Routen), `internal/web/backup.go:1307-1313` (`StartBackupSystem` startet `drWatch`)

**Interfaces:**
- Consumes: `h.agentJSON` (`internal/web/nextcloud_updates.go:75`), `h.canServerConfig`, `h.appSetting`/`h.setAppSetting`, `networkDrive.Path()`, `h.notifyAdmins`, `h.ncDir(ctx)` (`nextcloud_updates.go:70`).
- Produces:
  - `drConfig`, `drBase`, `drStatus` – identische Felder/JSON-Tags wie Task 1 (Kopie im Paket `web`).
  - `type drView struct { Status drStatus; AgentErr string; Drives []networkDrive; DriveKey string; DBLocal bool; VMLast string; VMStamp string; VMMaxDays int; VMAge string; VMWarn bool; Alerts []string }`
  - `func (h *Handler) drLoadView(ctx context.Context) drView`
  - `func drTarget(drivePath, hostname string) string` – `<drivePath>/PDH-Ausfallschutz/<host>`, kleingeschrieben, alles außer `[a-z0-9-]` → `-`, leer → `server`.
  - `func dbIsLocal(host string) bool` – Regel aus Global Constraints.
  - `func drAlerts(st drStatus, vmLast time.Time, vmMaxDays int, now time.Time) []string` – nur bei `Config.Enabled`: `Error` → „Fehler: <Error>“; `SpoolBytes > 2<<30` oder `SpoolOldest` > 1 h → „WAL-Dateien stauen sich (<n> Dateien) – Netzlaufwerk prüfen“; `LastBase` > 26 h → „Letzte Grundsicherung ist älter als 26 Stunden“; `LastFileSync` > 3 × `FileSyncMinutes` → „Dateiabgleich hängt seit <Zeit>“; `vmLast` gesetzt und > `vmMaxDays` Tage → „VM-Sicherung ist <n> Tage alt“.
  - `func (h *Handler) drWatch(ctx context.Context)` – alle 5 min; Agent nicht erreichbar bei eingeschaltetem Ausfallschutz (`dr.enabled` = `1`) → „Update-Agent nicht erreichbar – Ausfallschutz-Sicherung läuft nicht“; Meldungen verbinden, nur bei Änderung gegenüber `dr.alert_last` `notifyAdmins("⚠️ **Ausfallschutz**\n…\nServer-Einstellungen → Ausfallschutz")`; leer → `dr.alert_last` leeren.
  - App-Settings: `dr.enabled`, `dr.drive`, `dr.vm_last`, `dr.vm_stamp`, `dr.vm_max_days`, `dr.alert_last`. VM-Zeitstempel: ist `dr.vm_stamp` gesetzt, gilt die Änderungszeit dieser Datei auf dem gewählten Laufwerk, sonst `dr.vm_last`.
  - Routen (Prüfung `canServerConfig` wie `vpnAdminGuard`): `POST /admin/server-config/dr/settings` (Laufwerk, Werte, an/aus, `nextcloud_dir` = `h.ncDir(ctx)` → `POST /v1/dr/config`; vor dem ersten Einschalten Hinweis „Postgres startet einmal neu – das PDH ist wenige Sekunden nicht erreichbar.“), `POST /admin/server-config/dr/run` (`what`), `POST /admin/server-config/dr/vm`, `GET /admin/server-config/dr/restore-script` (Download `pdh-restore.sh`, `text/x-shellscript`, wie `VPNScriptWeb`).
- Anzeige: Archivierung an/aus, letzte WAL-Übertragung + Datei, Zwischenspeicher, letzte Grundsicherung, letzter Dateiabgleich mit Quellen (PDH, Nextcloud, n8n) und `Missing`, Liste der Grundsicherungen, Knöpfe „Grundsicherung jetzt“, „Dateien jetzt abgleichen“, Skript-Download, Bereich „VM-Sicherung“, Hinweis „Die Freigabe enthält Zugangsdaten (.env, Nextcloud-config.php) – nur für Administratoren freigeben.“; im Docker-Modus nur der Hinweis aus Global Constraints.

- [ ] **Step 1: Tests** – `TestDRTarget` (`"PDH-Srv_01"` → `/mnt/pdh/nas/PDH-Ausfallschutz/pdh-srv-01`, `""` → `…/server`). `TestDBIsLocal` (`""`, `localhost`, `127.0.0.1`, `::1`, `/var/run/postgresql` → true; `db`, `10.0.0.5` → false). `TestDRAlerts` (Tabelle): alles frisch → leer; ausgeschaltet → leer; je Bedingung genau die Meldung (Spool 3 GiB, Base vor 27 h, Abgleich vor 181 min bei 60, VM 8 Tage bei 7). `TestDRSettingsForbidden`: ohne Server-Einstellungsrecht → 403. `TestDRSettingsSendsConfig`: Fake-Agent (`httptest.NewServer`, `h.updateAgentURL`/`h.updateAgentToken`) empfängt `POST /v1/dr/config` mit `target` aus `drTarget`; DB nicht lokal → Fehlermeldung, kein Aufruf.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren (Template im Stil von `widgets/vpn.gohtml`).
- [ ] **Step 4:** `go test ./internal/web/` → PASS (inkl. vorhandener Template-Render-Tests).
- [ ] **Step 5:** Commit `feat: Reiter Ausfallschutz mit Status und Alarmen`.

### Task 9: Handbuch, Version, Doku

**Files:**
- Modify: `web/templates/help.gohtml` (neues `<section class="hb-ch" id="ausfallschutz" data-pages="server_config">` hinter `vpn`), `internal/web/help.go:48-49` (`"ausfallschutz"` hinter `"vpn"`), `web/templates/widgets/help_figures.gohtml` (Skizze: Server → Netzlaufwerk mit `wal/`, `base/`, `files/`; daneben neuer Server mit `pdh-restore.sh`), `CHANGELOG.md`, `VERSION`, `README.md` (Abschnitt „Ausfallschutz“ mit Verweis aufs Handbuch)

**Inhalt Handbuch** (`.hb-howto`/`.hb-steps`): Einschalten (Laufwerk mit Rolle Sicherung → Reiter Ausfallschutz → Laufwerk wählen → Einschalten; Postgres-Neustart-Hinweis); was gesichert wird (PDH, Nextcloud mit Daten, n8n) und wie alt es höchstens ist (Datenbank ≤ `wal_max_minutes`, Dateien ≤ Abgleich-Intervall; Nextcloud-Dateien werden live abgeglichen, n8n kurz pausiert); Wiederaufbau Schritt für Schritt (frisches Ubuntu, Freigabe einbinden, `sudo bash pdh-restore.sh --from …`, optional `--time`); VM-Sicherung (allgemein: Hypervisor sichert die VM regelmäßig auf ein anderes Ziel; Datum eintragen oder Zeitstempel-Datei); Fehlerbilder (Netzlaufwerk weg → Stau im Zwischenspeicher; Docker-Testsystem → nicht verfügbar).

**Version:** Ist `git show origin/main:VERSION` noch `0.72.0`, den unveröffentlichten Eintrag `0.72.1` in `0.73.0` umbenennen und `VERSION` = `0.73.0`; sonst neuer Eintrag mit nächster Nebenversion. Unter `### Neu`: „Ausfallschutz (Server-Einstellungen): Der Server sichert Datenbank, Dateien und Konfiguration von PDH, Nextcloud und n8n laufend auf ein Netzlaufwerk; mit pdh-restore.sh lässt sich daraus ein neuer Server oder eine VM aufbauen – auch auf einen bestimmten Zeitpunkt.“

- [ ] **Step 1:** Kapitel, ID, Skizze, Changelog, Version schreiben.
- [ ] **Step 2:** `go test ./...` → PASS (u. a. `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`).
- [ ] **Step 3:** Commit `docs: Handbuch-Kapitel Ausfallschutz, Version 0.73.0`.

### Abschluss: Prüfung auf einer systemd-Test-VM

- [ ] Ubuntu-VM mit PDH (systemd), Nextcloud (`install-nextcloud-nginx-postgres.sh`), n8n-Container und Netzlaufwerk; Ausfallschutz einschalten → Postgres startet einmal neu, `archive_mode=on`, nach ≤ 5 min liegen `wal/*.gz` auf dem Laufwerk.
- [ ] „Grundsicherung jetzt“ → `base/<Name>/` mit `info.json`; „Dateien jetzt abgleichen“ → `files/uploads`, `files/nextcloud-data`, `files/n8n-vol-…`; n8n läuft danach wieder.
- [ ] Netzlaufwerk aushängen → Spool wächst, Fehler im Reiter; wieder einhängen → Stau löst sich.
- [ ] Zweite, frische VM: `pdh-restore.sh --from …` (neuester Stand) und einmal mit `--time` vor einer Teständerung → PDH, Nextcloud und n8n laufen, Daten entsprechen dem Zeitpunkt.
- [ ] Ausschalten → Konfigurationsdatei weg, `archive_mode=off`, Spool leer.
