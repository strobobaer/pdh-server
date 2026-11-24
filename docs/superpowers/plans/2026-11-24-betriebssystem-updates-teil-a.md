# Betriebssystem-Updates im PDH – Teil A (Einzelserver) – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Auf einem Einzelserver (systemd) zeigt das PDH offene Betriebssystem-Pakete, spielt sie auf Knopfdruck, mit dem PDH-Update (Haken) oder im Wartungsfenster (nur Sicherheitsupdates) kontrolliert ein – mit Countdown, Sperre, Sicherung, Neustart und Prüfung danach.
**Architecture:** Ein festes Root-Skript `pdh-os-update` (nur `check|install|status`) wird mit jedem PDH-Update nach `/usr/local/sbin` installiert; der Update-Agent ruft es nur über `systemd-run` auf, hält eine gemeinsame Sperre gegen andere Läufe und führt eine Schrittmaschine (`osSteps`: stoppen, einspielen, Neustart, starten, prüfen), die sowohl der eigene Auftrag „Betriebssystem“ (`os-job.json`) als auch der PDH-Auftrag mit Haken benutzt. Das PDH zeigt eine Karte unter „GitHub-Updates“ und prüft das neue Recht `system.os_update`.
**Tech Stack:** Go (Update-Agent `cmd/update-agent`, `internal/ha` nur Anzeige, `internal/web`), Bash (Skript, `install_update.sh`), PostgreSQL-Migration 222, Go-Templates (Karte, Handbuch).
**Spec:** docs/superpowers/specs/2026-11-24-betriebssystem-updates-design.md

## Global Constraints

- Skript-Aktionen: nur `check`, `install`, `status`; jede andere Eingabe und jedes zusätzliche Argument endet mit Code 2 und `Aufruf: pdh-os-update check|install|status` auf stderr.
- apt, Skript und Neustart laufen nur über `systemd-run --wait --pipe --collect --quiet --unit=pdh-os-<aktion>-<unix>` außerhalb der Agent-Sandbox (`ProtectSystem=full`, `/usr/local/sbin` ist dort nicht beschreibbar); nie direkt im Agenten.
- Umgebung im Skript: `DEBIAN_FRONTEND=noninteractive`, `NEEDRESTART_MODE=l`, `LC_ALL=C.UTF-8`; apt mit `-q -o DPkg::Lock::Timeout=300`, `-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold`; Befehl `upgrade --with-new-pkgs`, nie `full-upgrade`, `dist-upgrade`, `remove`, `purge`, `autoremove`.
- Recht `system.os_update`, Bezeichnung „Betriebssystem aktualisieren“, Kategorie „System & Sicherheit“, Migration 222 (vor dem Merge gegen `origin/main` neu prüfen und ggf. umnummerieren); vergeben wie in 154 an alle Rollen, die `system.manage_roles` mit `department_id IS NULL` haben; gilt nur überall.
- Eine Sperre im Agenten (`lockAcquire`) für Betriebssystem-Auftrag, PDH-Update (Skriptläufe und Einzelserver-Auftrag), ESPHome-Einrichtung und Netz-Änderung; Besitzernamen als Konstanten, Meldung an den Anwender nennt den Besitzer („Ein PDH-Update läuft …“).
- Neustart nur, wenn `/var/run/reboot-required` nach dem Einspielen existiert; höchstens ein Neustart je Auftrag.
- Während `install` ist `pdh` gestoppt (Sperre und Sicherung vorher); nach Fehler oder Neustart wird `pdh` wieder gestartet; `/health` muss innerhalb 10 min (`jobVerifyWait`) antworten; Warten auf das Hochfahren höchstens 20 min (`osBootWait`); apt höchstens 45 min (`osInstallTimeout`).
- Platz: `/var/cache/apt` mindestens 1 GiB, `/boot` mindestens 200 MiB (nicht per „trotzdem“ übergehbar); „alles grün“ (`ha.UpdateReady`, Modus `ha.Single`) wie beim PDH-Update, übergehbar mit `force`.
- Docker-Betrieb und Verbundknoten: keine Aufträge in Teil A (Karte zeigt Status und Hinweis „Im Verbund folgt das Betriebssystem-Update mit Teil B“ bzw. `dockerNotAvailMsg`); der Zeuge wird nie angefasst.
- Der Haken „beim PDH-Update mit aktualisieren“ liegt im Agenten (`os-state.json`, Feld `with_pdh_update`), gilt für Knopf und Wartungsfenster; er wird beim Start eines Auftrags in den Auftrag kopiert (`WithOS`).
- CRLF-Regel: bestehende Dateien nur mit dem Edit-Werkzeug ändern, nie mit Skript-Umschreiben; neue Dateien mit LF.
- Alle Texte deutsch; Tests in Git Bash (`cd /d/pdh-os-update && go test …`); kein Push, kein Merge.
- Version: nächste freie Korrekturversion beim Merge, Platzhalter `0.82.58` (ein anderer Zweig zielt auf `0.82.57`); beim Merge `VERSION` und `CHANGELOG.md` neu gegen `origin/main` prüfen.

## Review Focus

1. **Server kommt nach dem Neustart nicht zurück, Auftrag meldet trotzdem „fertig“** – Test `TestOSStepsBootNeverChanges` (Task 4): gleiche `boot_id` nach 20 min ⇒ Auftrag `abgebrochen` mit „Neustart hat nicht stattgefunden“, `pdh` wird gestartet, Sperr-Vermerk entfernt; `TestOSStepsVerifyTimeout`: `/health` antwortet nach dem Neustart 10 min nicht ⇒ `abgebrochen`, nicht `fertig`.
2. **Agent oder Rechner stirbt mitten im Auftrag (auch während apt)** – `TestOSJobResumeMidInstall` (Task 5): Zustand `Step=install` aus `os-job.json` ⇒ `install` läuft genau einmal erneut, danach höchstens ein Neustart; Skripttest `TestOSScriptInstallRepairsDpkg` (Task 1): `dpkg --configure -a` vor dem `upgrade`.
3. **apt läuft, während `pdh`/PostgreSQL in Betrieb sind, oder innerhalb der Agent-Sandbox** – `TestOSJobHappyPath` (Reihenfolge `psql-lock` → `stop-pdh` → `pg_dump` → `os install` → `start-pdh` → `psql-unlock`) und `TestOSExecUsesSystemdRun` (Task 3): jeder Aufruf geht als `systemd-run … /usr/local/sbin/pdh-os-update <aktion>`; `TestFinishInstallsOSScript` (Task 2): Installation des Skripts über `systemd-run`.
4. **Gleichzeitige Läufe (PDH-Update ↔ Betriebssystem, Wartungsfenster ↔ Knopf, Netz-Änderung, ESPHome)** – `TestLockExclusive`, `TestStartSingleJobRefusedWhileOS`, `TestNetApplyRefusedWhileOS`, `TestEsppdhInstallRefusedWhileOS` (Task 3), `TestWindowSingleOSWhileJobActive` (Task 6): zweiter Starter bekommt die Meldung, nichts läuft doppelt, der Sperrbesitz überlebt einen Neustart des Agenten (`TestLockRestoredAfterRestart`, Task 5).
5. **Falsche Bediener und falsche Umgebung** – `TestOSRoutesNeedPermission` (Task 7: 403 ohne `system.os_update`, Karte ohne Knöpfe), `TestOSStatusCluster`/`TestOSStatusDocker`/`TestOSScriptOutdated` (Task 3: Verbund und Docker lehnen Aufträge sauber ab, ein noch altes Skript auf dem Server – z. B. 10.10.0.202 – wird als „veraltet“ gemeldet statt falsch gelesen), `TestMigrationOSUpdatePermission` (Task 7: Rollen ohne überall geltendes `manage_roles` bekommen das Recht nicht).

---

## Task 1: Skript `scripts/pdh-os-update` mit Aktion `status`

**Files:**
- Create: `scripts/pdh-os-update` (Ausgangspunkt: `C:/Users/michael/AppData/Local/Temp/claude/04675cd6cb4cf604/efce4eba-a419-41f5-b30d-d2f94d87d31c/scratchpad/os-update/pdh-os-update`, 1:1 kopieren, dann ändern)
- Create: `cmd/update-agent/os_script_test.go`

**Interfaces:**
- Consumes: nichts.
- Produces: `/usr/local/sbin/pdh-os-update check|install|status`; `status` schreibt genau ein JSON-Objekt auf stdout, sonst nichts:
  `{"ok":true,"script_version":2,"system":"Debian GNU/Linux 13 (trixie)","kernel":"6.12.0-test","checked_at":"2026-11-24T16:41:00+01:00","upgradable":[{"name":"openssl","old":"3.5.3-1","new":"3.5.4-1","security":true}],"security_count":2,"reboot_required":true,"reboot_pkgs":["linux-image-7.0.0-45-generic"]}`
  `checked_at` = Inhalt der Stempeldatei `/var/lib/pdh-os-update.checked` (ISO-8601, von `check` und `install` nach erfolgreichem `apt-get update` geschrieben), leer wenn es sie nicht gibt. `status` ruft nie `apt-get update` auf. `security` = einer der Suite-Namen in `apt list --upgradable` endet auf `-security`.
- Testschalter: nur bei `PDH_OS_TEST=1` gelten `PDH_OS_ROOT` (Präfix für `/var/run/reboot-required[.pkgs]`, `/etc/os-release`, `/var/lib/pdh-os-update.checked`, `/var/log/pdh-os-update.log`); sonst feste Pfade (sudo/root-Aufruf bleibt unbeeinflusst). `apt-get`, `apt`, `uname`, `date` werden über `PATH` gefunden (Test legt Attrappen davor).

- [ ] **Step 1: Failing tests** in `cmd/update-agent/os_script_test.go` (Paket `main`; überspringen mit `t.Skip`, wenn `bash` fehlt). Hilfen: `osScriptEnv(t) *osScriptEnvT` legt Temp-Ordner mit `stubs/apt-get` (hängt `"$*"` und `DEBIAN_FRONTEND=$DEBIAN_FRONTEND NEEDRESTART_MODE=$NEEDRESTART_MODE` an `$LOG`, Exit `${APT_RC:-0}`), `stubs/apt` (gibt `Listing...` und die drei Zeilen unten aus), `stubs/uname` (`6.12.0-test`) und `root/etc/os-release` (`PRETTY_NAME="Debian GNU/Linux 13 (trixie)"`) an; `run(args ...string) (stdout, stderr string, code int)` startet `bash <repo>/scripts/pdh-os-update` mit `PATH=stubs:$PATH PDH_OS_TEST=1 PDH_OS_ROOT=<root> LOG=<datei>`.
  Attrappe `apt list --upgradable`:
  ```
  openssl/stable-security 3.5.4-1 amd64 [upgradable from: 3.5.3-1]
  curl/stable-updates,stable-security 8.14.1-2 amd64 [upgradable from: 8.14.1-1]
  vim/stable 9.1.1-1 amd64 [upgradable from: 9.1.0-1]
  ```
  Tests:
  - `TestOSScriptSyntax`: `bash -n scripts/pdh-os-update` Exit 0.
  - `TestOSScriptUnknownAction`: für `[]`, `["dist-upgrade"]`, `["install","-y"]`, `["status","x"]`, `["CHECK"]` Exit 2, stderr enthält `Aufruf: pdh-os-update check|install|status`, apt-get-Log leer.
  - `TestOSScriptStatusJSON`: Exit 0; stdout ist gültiges JSON (`json.Unmarshal`), beginnt mit `{`, enthält nicht `NEUSTART`; `len(upgradable)==3`; `upgradable[0]=={Name:"openssl",Old:"3.5.3-1",New:"3.5.4-1",Security:true}`; `upgradable[2].Security==false`; `security_count==2`; `system=="Debian GNU/Linux 13 (trixie)"`; `kernel=="6.12.0-test"`; `reboot_required==false`; apt-get-Log leer. Danach `root/var/run/reboot-required` anlegen und `reboot-required.pkgs` mit `linux-image-7.0.0-45-generic\n` ⇒ `reboot_required==true`, `reboot_pkgs==["linux-image-7.0.0-45-generic"]`.
  - `TestOSScriptInstallArgs`: `install` ⇒ Log enthält die Zeilen `-q -o DPkg::Lock::Timeout=300 update` und `-q -o DPkg::Lock::Timeout=300 -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade --with-new-pkgs`, sowie `DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l`; Log enthält nie `full-upgrade|dist-upgrade|remove|purge|autoremove`; Protokolldatei enthält eine Zeile, die mit `== ` beginnt; mit Reboot-Datei enthält stdout `NEUSTART NÖTIG`; mit `APT_RC=100` ist der Exit-Code ≠ 0.
  - `TestOSScriptInstallRepairsDpkg`: Im Log steht `dpkg --configure -a` (Attrappe `stubs/dpkg` schreibt `dpkg $*`) vor der `upgrade`-Zeile.
  - `TestOSScriptCheckWritesStamp`: `check` ⇒ Stempeldatei existiert und `status` liefert `checked_at` ≠ `""`; `check` gibt die Zeilen von `apt list --upgradable` ohne `Listing...` aus.
- [ ] **Step 2: Run** `cd /d/pdh-os-update && go test ./cmd/update-agent -run 'TestOSScript' -count=1` – erwartet FAIL (Datei fehlt).
- [ ] **Step 3: Implement** `scripts/pdh-os-update`: Referenzskript übernehmen; `case "${1:-}"` mit `check|install|status`, davor `[ $# -le 1 ] || usage` (Aufrufmuster `usage() { echo "Aufruf: pdh-os-update check|install|status" >&2; exit 2; }`); `install` beginnt nach dem Protokollkopf mit `dpkg --configure -a --force-confdef --force-confold` (Fehler nur Warnung), dann `update`, dann `upgrade --with-new-pkgs`; `set -o pipefail` bleibt, damit der `tee`-Pfad den Fehlercode weitergibt; „NEUSTART NÖTIG“-Zeile nur für `check`/`install`. JSON in Bash zusammensetzen (Paketnamen und Versionen passen auf `[A-Za-z0-9.+:~_-]`; `system` aus `PRETTY_NAME` mit `\` und `"` maskiert). Datei ausführbar im Git (`git update-index --chmod=+x`).
- [ ] **Step 4: Run** wie Step 2 – erwartet PASS.
- [ ] **Step 5: Commit** `git add scripts/pdh-os-update cmd/update-agent/os_script_test.go && git commit -m "Betriebssystem-Skript ins Repository, neue Aktion status mit JSON"`

## Task 2: Skript mit jedem PDH-Update installieren

**Files:**
- Modify: `install_update.sh` (systemd-Zweig, nach dem Neustart von `pdh`, vor dem nginx-Block „Zertifikate“)
- Modify (Test): `cmd/update-agent/update_test.go` (`newFinishEnv`: Datei `scripts/pdh-os-update` aus dem Repo in die Testumgebung kopieren)
- Test: `cmd/update-agent/os_script_test.go`

**Interfaces:**
- Consumes: `scripts/pdh-os-update` (Task 1).
- Produces: `${PDH_OS_UPDATE_BIN:-/usr/local/sbin/pdh-os-update}`, root:root 0755 – installiert über `systemd-run --wait --pipe --collect --quiet --unit=pdh-os-script-$(date +%s) install -D -m 0755 -o root -g root "$repo_dir/scripts/pdh-os-update" "$os_bin"` (ohne `systemd-run` direkt `install …`). Fehler nur `WARNING: pdh-os-update konnte nicht installiert werden – siehe Ausgabe oben` auf stderr, Update bleibt erfolgreich. Nicht bei `--no-restart` (der Block liegt hinter dessen `exit 0`) und nicht im Docker-Zweig.

- [ ] **Step 1: Failing test** `TestFinishInstallsOSScript` (Muster `TestFinishWithCertsInstallsNginx`, Hilfen `newFinishEnv`, `e.ready`, `e.run(t, "0", "--finish")`):
  - Lauf `--finish` ⇒ Exit 0; Log enthält `systemd-run --wait --pipe --collect --quiet --unit=pdh-os-script-` und `install -D -m 0755 -o root -g root `, endet auf `/scripts/pdh-os-update ` + Zielpfad; die Zeile steht nach `systemctl restart pdh`.
  - Mit `PDH_TEST_SD_RC=1` ⇒ Exit 0 und Ausgabe enthält `WARNING: pdh-os-update konnte nicht installiert werden`.
  - Lauf mit `--no-restart` (Aufruf wie in den vorhandenen Tests dieser Datei) ⇒ Log enthält nicht `pdh-os-script`.
- [ ] **Step 2: Run** `go test ./cmd/update-agent -run TestFinishInstallsOSScript -count=1` – erwartet FAIL.
- [ ] **Step 3: Implement** im Skript `install_update.sh` einen Block `os_bin="${PDH_OS_UPDATE_BIN:-/usr/local/sbin/pdh-os-update}"` mit Hilfsfunktion `os_install()` nach dem Muster `nginx_apt()` (Zeilen ~270–285); `[[ -f "$repo_dir/scripts/pdh-os-update" ]] || echo "WARNING: …" >&2`. Edit-Werkzeug verwenden.
- [ ] **Step 4: Run** wie Step 2 sowie `go test ./cmd/update-agent -run 'TestFinish' -count=1` – erwartet PASS (alte Tests bleiben grün).
- [ ] **Step 5: Commit** `git commit -am "PDH-Update installiert pdh-os-update nach /usr/local/sbin (über systemd-run)"`

## Task 3: Agent – Status, Prüfen, Haken, gemeinsame Sperre

**Files:**
- Create: `cmd/update-agent/os_update.go`, `cmd/update-agent/os_update_test.go`
- Modify: `cmd/update-agent/main.go` (Feld `os osRuntime` und Hook-Felder in `updateAgent`; `loadOSState()` im systemd-Zweig; drei Routen), `cmd/update-agent/update.go` (`startUpdate`), `cmd/update-agent/net.go` (`netApplyHandler`), `cmd/update-agent/esppdh.go` (`esppdhInstallHandler`), `cmd/update-agent/job.go` (`startSingleJob`)

**Interfaces:**
- Produces (Go):
  ```go
  const (lockOS = "Betriebssystem-Update"; lockPDH = "PDH-Update"; lockESPHome = "ESPHome-Einrichtung"; lockNet = "Netzwerk-Änderung")
  var osActions = map[string]bool{"check": true, "install": true, "status": true}
  const osScriptPath = "/usr/local/sbin/pdh-os-update"
  type osPackage struct { Name, Old, New string; Security bool } // JSON name/old/new/security
  type osScriptStatus struct { OK bool; ScriptVersion int `json:"script_version"`; System, Kernel string; CheckedAt string `json:"checked_at"`; Upgradable []osPackage; SecurityCount int `json:"security_count"`; RebootRequired bool `json:"reboot_required"`; RebootPkgs []string `json:"reboot_pkgs"` }
  func (a *updateAgent) lockAcquire(owner string) (release func(), busyBy string) // gleicher Besitzer erneut: erlaubt (Zähler); anderer: busyBy != ""
  func (a *updateAgent) osExec(ctx context.Context, action string) ([]byte, error) // lehnt Aktionen außerhalb osActions ab; Hook a.osRun nil = systemd-run
  func (a *updateAgent) osStatus(ctx context.Context) (osScriptStatus, error)      // Zwischenspeicher 20 s, nach check geleert
  func (a *updateAgent) osWithUpdate() bool
  func (a *updateAgent) osAvailable(ctx context.Context) (ok bool, reason string, cluster bool)
  ```
  Hook-Felder: `osRun func(ctx context.Context, action string) ([]byte, error)`, `bootID func() string` (nil = `/proc/sys/kernel/random/boot_id`).
  `osState` in `<drStateDir>/os-state.json` (0600): `{"with_pdh_update":bool,"last_run":{"at":RFC3339,"packages":int,"rebooted":bool,"ok":bool,"message":string}}`.
- Endpunkte (Bearer wie bisher):
  - `GET /v1/os/status` → 200 immer: `{"ok":true,"available":bool,"reason":"","cluster":bool,"docker":bool,"script_outdated":bool,"system":"","kernel":"","package_count":0,"security_count":0,"packages":[osPackage… höchstens 200],"reboot_required":false,"reboot_pkgs":[],"checked_at":"RFC3339 oder leer","with_pdh_update":false,"job":ha.UpdateView oder null,"last_run":{…}}`. `available=false` bei Docker (`reason=dockerNotAvailMsg`, `docker:true`, ohne Paketdaten), bei Verbund (`cluster:true`, `reason="Im Verbund folgt das Betriebssystem-Update mit Teil B"`, Paketdaten werden trotzdem geliefert), bei pdh-ha eingerichtet aber nicht erreichbar (`reason="pdh-ha nicht erreichbar: …"`), bei fehlendem/altem Skript (`script_outdated:true`, `reason="Das Betriebssystem-Skript ist veraltet oder fehlt – es wird mit dem nächsten PDH-Update erneuert"`).
  - `POST /v1/os/check` → ruft `check` dann `status`, antwortet wie GET; 409 `{"ok":false,"error":"Ein PDH-Update läuft …"}` wenn ein anderer Besitzer die Sperre hält; im Docker-/Verbundfall 400 mit `reason`.
  - `POST /v1/os/config` `{"with_pdh_update":bool}` → `{"ok":true}`; Docker/Verbund: 400 `{"ok":false,"error":reason}`.
- Sperr-Einbau: `startUpdate` nimmt `lockPDH` (Freigabe, wenn `a.status.Running` endet), `startSingleJob` hält `lockPDH` bis zum Ende des Auftrags (`jobFinish`) und gibt bei Besitz durch `lockOS` zurück `errors.New("Ein Betriebssystem-Update läuft bereits")`; `esppdhInstallHandler` und `netApplyHandler` lehnen mit „Ein Betriebssystem-Update läuft – bitte danach erneut versuchen“ ab, solange `lockOS` gehalten wird; `esppdhInstallHandler` hält `lockESPHome`, `netApplyHandler` `lockNet` für ihre Laufzeit (der OS-Start prüft sie).
  Hinweis: `singleJobActive()` wird in Task 5 auf den OS-Auftrag erweitert.

- [ ] **Step 1: Failing tests** in `os_update_test.go` (Hilfe `osTestAgent(t)`: `t.Setenv("PDH_DR_STATE_DIR", t.TempDir())`, `t.Setenv("PDH_HA_ETC", t.TempDir())`, `&updateAgent{mode:"systemd", repoDir:t.TempDir(), token: strings.Repeat("x",32)}`, `a.osRun` = Attrappe `osFake` mit Aufrufliste, einstellbarer Ausgabe für `status` (JSON wie Task 1) und Fehlern):
  - `TestOSStatusHandler`: Attrappe liefert 2 Pakete (1 Sicherheit), `reboot_required:true`, `system:"Debian GNU/Linux 13"` ⇒ `GET /v1/os/status` (httptest über `a.authorized`-Mux, Bearer) 200 mit `available:true`, `package_count:2`, `security_count:1`, `reboot_required:true`, `with_pdh_update:false`, `job:null`; ohne Bearer 401.
  - `TestOSStatusDocker`: `mode:"docker"` ⇒ `available:false`, `docker:true`, `reason==dockerNotAvailMsg`, `osRun` nie aufgerufen.
  - `TestOSStatusCluster`: `haInstalled(t)` + `haFakeSocket(t, `{"ok":true,"configured":true,"mode":"duo","self":{"id":"a","role":"aktiv"}}`)` ⇒ `available:false`, `cluster:true`, `reason` enthält `Teil B`, `package_count` gesetzt; `POST /v1/os/config` ⇒ 400 mit demselben Grund.
  - `TestOSScriptOutdated`: `osRun("status")` liefert Fehler mit Ausgabe `Aufruf: pdh-os-update check|install` ⇒ `script_outdated:true`, `available:false`, `reason` enthält `nächsten PDH-Update`.
  - `TestOSExecUsesSystemdRun`: ohne `osRun` den echten Aufbau prüfen über Hook `a.osCmd func(name string, args ...string) *exec.Cmd`: für `install` ist `name=="systemd-run"` und `args` beginnt mit `["--wait","--pipe","--collect","--quiet","--unit=pdh-os-install-<ziffern>", "/usr/local/sbin/pdh-os-update","install"]`; `osExec(ctx,"full-upgrade")` und `osExec(ctx,"install -y")` liefern Fehler `unbekannte Aktion`, ohne den Hook zu rufen; `reflect.DeepEqual(osActions, map[string]bool{"check":true,"install":true,"status":true})`.
  - `TestOSCheckHandler`: `POST /v1/os/check` ⇒ Aufrufliste `["check","status"]`; zweiter Aufruf von `status` innerhalb 20 s wird aus dem Zwischenspeicher bedient (Aufrufliste wächst nicht); `a.lockAcquire(lockPDH)` gehalten ⇒ 409 mit Fehler, der `PDH-Update` enthält, Aufrufliste unverändert.
  - `TestOSConfigPersists`: `POST /v1/os/config {"with_pdh_update":true}` ⇒ `GET` zeigt `true`; neuer Agent mit gleichem `PDH_DR_STATE_DIR` und `loadOSState()` liefert `osWithUpdate()==true`; Datei `os-state.json` hat Modus 0600 (nicht unter Windows prüfen: `runtime.GOOS != "windows"`).
  - `TestLockExclusive`: `r1,_ := lockAcquire(lockOS)`; `lockAcquire(lockPDH)` ⇒ `busyBy==lockOS`; `lockAcquire(lockOS)` erneut ok; nach beiden Freigaben `lockAcquire(lockPDH)` ok.
  - `TestStartUpdateRefusedWhileOS`: `lockOS` gehalten ⇒ `a.startUpdate("abc","install")` liefert `ok==false`.
  - `TestStartSingleJobRefusedWhileOS`: gleiches für `startSingleJob(ctx, srcButton, false)` ⇒ Fehler enthält `Betriebssystem-Update`.
  - `TestNetApplyRefusedWhileOS` und `TestEsppdhInstallRefusedWhileOS`: Handler mit gültigem Rumpf (Muster in `net_test.go`/`esppdh_test.go`) antworten mit Meldung, die `Betriebssystem-Update läuft` enthält.
- [ ] **Step 2: Run** `go test ./cmd/update-agent -run 'TestOS|TestLock|TestStartUpdateRefused|TestStartSingleJobRefused|TestNetApplyRefused|TestEsppdhInstallRefused' -count=1` – erwartet FAIL (nicht kompilierbar).
- [ ] **Step 3: Implement** `os_update.go` (Typen und Signaturen oben; `osExec` baut `systemd-run`-Aufruf, Zeitlimit `check` 10 min, `status` 30 s, `install` `osInstallTimeout`=45 min; Ausgabe über `limitedBuffer`); `osStatus` fehlertolerant: Exit-Fehler mit `Aufruf:` im Text ⇒ `ErrScriptOutdated`; `osAvailable` nutzt `a.haCluster(ctx)`; Routen in `main.go` neben „Update auf dem Einzelserver“. `startUpdate`, `startSingleJob`, `netApplyHandler`, `esppdhInstallHandler` mit dem Edit-Werkzeug anpassen; `errNoNewVersion` wird erst in Task 6 gebraucht.
- [ ] **Step 4: Run** wie Step 2 plus `go test ./cmd/update-agent -count=1` – erwartet PASS.
- [ ] **Step 5: Commit** `git add -A cmd/update-agent && git commit -m "Update-Agent: Betriebssystem-Status, Prüfen, Haken und gemeinsame Sperre"`

## Task 4: Agent – Schrittmaschine mit Neustart und Wiederaufnahme

**Files:**
- Modify: `cmd/update-agent/os_update.go` (oder neu `cmd/update-agent/os_steps.go`), `cmd/update-agent/job_host.go` (`jobRebootScript`)
- Modify (Test): `cmd/update-agent/job_test.go` (`jobHostLabel`: `jobRebootScript` ⇒ `"reboot"`; neue Hilfe `assertOrder`)
- Test: `cmd/update-agent/os_steps_test.go`

**Interfaces:**
- Consumes: `osExec`, `osStatus`, `jobHost`, `jobStopPDH`, `pdhHealthy`, `jobStartScript`, `jobVerifyWait`, `a.clock`/`a.now()`.
- Produces:
  ```go
  const jobRebootScript = `systemctl reboot`
  const (osInstallTimeout = 45 * time.Minute; osBootWait = 20 * time.Minute)
  // Schritte: "" → "stop" → "install" → ("reboot" → "boot")? → "start" → "verify" → "done"
  type osSteps struct {
      Step     string    `json:"step"`
      Pending  int       `json:"pending"`   // Pakete vor dem Einspielen
      BootID   string    `json:"boot_id"`
      StepAt   time.Time `json:"step_at"`
      Rebooted bool      `json:"rebooted"`
      Failed   bool      `json:"failed"`
      Msg      string    `json:"msg"`       // Fehlertext bzw. Ergebnis („3 Pakete, Neustart ja“)
  }
  // osAdvance führt höchstens einen Schritt aus; persist wird vor Neustart und vor install aufgerufen.
  // true = Schrittmaschine beendet (Step=="done"; bei Failed ist pdh wieder gestartet).
  func (a *updateAgent) osAdvance(ctx context.Context, st *osSteps, persist func()) (done bool)
  func (st osSteps) installing() bool // Step=="install"
  ```
  Regeln: `""`: `check`, dann `status`; `Pending=len(Upgradable)`; 0 ⇒ `Step="done"`, `Msg="Keine Pakete zu aktualisieren"`; sonst `Step="stop"`. `stop`: `jobStopPDH`; Fehler ⇒ `Failed`, `Step="start"`. `install`: `persist()`, `osExec("install")`; Fehler ⇒ `Failed`, `Msg=` letzte 3 Zeilen der Ausgabe (`lastLines(out,3)`), `Step="start"`; Erfolg ⇒ `osStatus` neu lesen (Zwischenspeicher leeren): `RebootRequired` ⇒ `Step="reboot"`, sonst `"start"`. `reboot`: `BootID=bootID()`, `StepAt=now`, `Step="boot"`, `persist()`, danach `jobHost(jobRebootScript)`; Fehler ⇒ `Failed`, `Step="start"`. `boot`: gleiche `bootID()` und `now-StepAt<osBootWait` ⇒ warten (`false`); gleiche und ≥ `osBootWait` ⇒ `Failed`, `Msg="Neustart hat nicht stattgefunden – bitte Konsole prüfen"`, `Step="start"`; andere `boot_id` ⇒ `Rebooted=true`, `Step="start"`. `start`: `jobHost(jobStartScript)` (idempotent); Fehler ⇒ `Failed`; `Step="verify"`, `StepAt=now`. `verify`: `pdhHealthy(ctx,"","")` ok ⇒ `Step="done"`; Fehler und `now-StepAt<jobVerifyWait` ⇒ warten; sonst `Failed`, `Msg="Server antwortet nach dem Neustart nicht: …"`, `Step="done"`.

- [ ] **Step 1: Failing tests** (`jobTestAgent` + `osFake`; `a.bootID` Attrappe mit setzbarem Wert; Schleife `for !a.osAdvance(ctx,&st,func(){}) { clk.add(time.Minute) }` mit Obergrenze 100):
  - `TestOSStepsNoPending`: `status` ohne Pakete ⇒ `Step=="done"`, Labelfolge enthält weder `stop-pdh` noch `os install`.
  - `TestOSStepsNoReboot`: 3 Pakete, `reboot_required:false` ⇒ `assertOrder(rec.all(), "os check","os status","stop-pdh","os install","os status","start-pdh")`; kein `reboot`; `st.Failed==false`, `st.Rebooted==false`, `Step=="done"`.
  - `TestOSStepsReboot`: `reboot_required` nach `install` ⇒ Labelfolge enthält `reboot` genau einmal und `os install` genau einmal; nach dem Label `reboot` bleibt `osAdvance` bei `Step=="boot"`, solange `bootID` gleich ist; Wechsel der `boot_id` ⇒ weiter bis `done`, `Rebooted==true`; `persist` wurde vor `os install` und vor dem Label `reboot` aufgerufen (Zähler, Reihenfolge über gemeinsamen Protokollschreiber).
  - `TestOSStepsBootNeverChanges`: `boot_id` bleibt, Uhr +20 min ⇒ `Failed==true`, `Msg` enthält `Neustart hat nicht stattgefunden`, Label `start-pdh` folgt, `Step=="done"`.
  - `TestOSStepsVerifyTimeout`: Health-Server (`health`) liefert 503 (Attrappe `health.set` kann Status; falls nicht vorhanden, `a.gitRun`-ähnlich Hook `a.healthFn`), Uhr +10 min ⇒ `Failed==true`, `Msg` beginnt mit `Server antwortet nach dem Neustart nicht`.
  - `TestOSStepsInstallFails`: `osFake.installErr` = Fehler mit Text `E: Sub-process /usr/bin/dpkg returned an error code (1)` ⇒ `Failed`, `Msg` enthält diesen Text, kein `reboot`, `start-pdh` folgt, Ende `done`.
  - `TestOSStepsStopFails`: `pdh-active` bleibt wahr (pdh lässt sich nicht stoppen) ⇒ `Failed`, kein `os install`.
- [ ] **Step 2: Run** `go test ./cmd/update-agent -run 'TestOSSteps' -count=1` – erwartet FAIL.
- [ ] **Step 3: Implement** `osAdvance` wie oben; Konstante `jobRebootScript` in `job_host.go`; `bootID` liest `/proc/sys/kernel/random/boot_id` (getrimmt), leer ⇒ Fehler „boot_id nicht lesbar“ ⇒ kein Neustart (`Failed`).
- [ ] **Step 4: Run** wie Step 2 – erwartet PASS.
- [ ] **Step 5: Commit** `git commit -am "Update-Agent: Schrittmaschine für Betriebssystem-Pakete mit Neustart"`

## Task 5: Agent – Auftrag „Betriebssystem“ (Einzelserver)

**Files:**
- Create: `cmd/update-agent/os_job.go`, `cmd/update-agent/os_job_test.go`
- Modify: `internal/ha/update.go` (Konstante `UpdateOS UpdateKind = "os"`, Feld `OSStep string` in `UpdateJob`), `internal/ha/update_view.go` (Schritte und Texte), `cmd/update-agent/job.go` (`singleJobActive`, `jobView`, `jobStep`, `abortSingleJob`, `jobHandler`), `cmd/update-agent/main.go` (`loadOSJob()` im systemd-Zweig, Route `POST /v1/os/job/start`)
- Test: `internal/ha/update_view_test.go` (neu oder bestehende Datei ergänzen)

**Interfaces:**
- Produces:
  ```go
  type osJob struct { Job ha.UpdateJob `json:"job"`; CountdownMin, LockMaxMin int; Dump string; OS osSteps `json:"os"`; Source string }
  func osJobFile() string // <drStateDir>/os-job.json
  func (a *updateAgent) startOSJob(ctx context.Context, source string, force bool) ([]string, error) // wie startSingleJob: jobNotReady = 409
  func (a *updateAgent) loadOSJob()
  func (a *updateAgent) osJobStep(ctx context.Context) bool
  const osMinFreeApt = int64(1) << 30; osMinFreeBoot = int64(200) << 20
  ```
  `ha.UpdateJob.OSStep` spiegelt `osSteps.Step`. `View` für `UpdateOS`: `Steps = ["Abmelden","Pakete einspielen","Neustart","Prüfen","Freigabe"]`; Phase `UpPrepare` Schritt 0/Text „Betriebssystem wird geprüft“; `UpCountdown` 0/5 „Abmeldung in Kürze – bitte Arbeit speichern“; `UpLocked` mit `OSStep` `stop|install`: Schritt 1/Prozent 25 „Betriebssystem-Pakete werden eingespielt“, `reboot|boot`: Schritt 2/Prozent 55 „Server startet neu“, `start|verify`: Schritt 3/Prozent 75 „Server wird geprüft“; `UpRelease` Schritt 4/90 „Freigabe“; `UpDone` 4/100 „Betriebssystem-Update abgeschlossen“; `UpFailed` 4/100 „Betriebssystem-Update abgebrochen“. `Name(j)` für `UpdateOS`: „Betriebssystem-Update“.
  Phasenablauf: `UpPrepare` (Vorabprüfung, `check`, Pakete > 0, Platz) → `UpCountdown` (`CountdownMin` aus Wartungsfenster/Standard) → `UpLocked` (`jobLockScript` mit `PDH_UPD_COMMIT="os-update"`, dann `OS`-Schritte ab `stop`, davor `jobDumpScript` nach dem Stoppen wie `jobLocked`) → `UpRelease` (`jobUnlock`; Ergebnis in `os-state.json` `last_run`, `jobFinish`-Gegenstück) → `UpDone`/`UpFailed`. `singleJobActive()` ist wahr, wenn PDH- oder OS-Auftrag offen; `jobView()` liefert den offenen der beiden; `abortSingleJob` bricht den offenen ab, beim OS-Auftrag nur solange `OS.Step` in `{"", "stop"}` und Phase vor `UpLocked`-Install, sonst Fehler „Die Pakete werden bereits eingespielt – Abbruch nicht mehr möglich“. Der OS-Auftrag hält `lockOS` von `startOSJob` bis `UpDone/UpFailed` und nimmt sie in `loadOSJob()` wieder auf.
  Start-Verweigerungen (Fehlertexte exakt): Docker `dockerNotAvailMsg`; Verbund „Im Verbund folgt das Betriebssystem-Update mit Teil B“; Skript veraltet wie Task 3; andere Sperre „Ein PDH-Update läuft bereits“; keine Pakete „Keine Pakete zu aktualisieren“; Platz `jobNotReady{reason: "Zu wenig freier Platz in /var/cache/apt (mindestens 1 GB)"}` bzw. `/boot (mindestens 200 MB)` (auch mit `force` nicht übergehbar ⇒ als normaler Fehler, nicht `jobNotReady`).

- [ ] **Step 1: Failing tests** `os_job_test.go` (`jobTestAgent`, `osFake`, `runJob`-Variante `runOSJob(t,a,clk,max)` ruft `a.osJobStep`):
  - `TestOSJobHappyPath`: 4 Pakete (1 Sicherheit), Datensicherung als eingerichtet gemeldet (wie in den PDH-Auftragstests, `a.dr.Config`/`LastBase`), `startOSJob(ctx,srcButton,false)` ⇒ `assertOrder(rec.all(), "os check","psql-lock","stop-pdh","pg_dump","os install","start-pdh","psql-unlock")`; `view.Kind=="os"`, `view.Steps` gleich `[]string{"Abmelden","Pakete einspielen","Neustart","Prüfen","Freigabe"}`; Endphase `fertig`; `os-state.json` `last_run` `{packages:4, rebooted:false, ok:true}`; `lockAcquire(lockPDH)` danach wieder frei.
  - `TestOSJobWithReboot`: `reboot_required` nach Einspielen ⇒ Auftrag bleibt aktiv bei `OS.Step=="boot"`; neuer Agent (gleiche Zustandsordner, `wireJobFakes`, `loadOSJob()`), `bootID` geändert ⇒ Auftrag endet `fertig`, `last_run.rebooted==true`; Label `os install` und `reboot` je genau einmal über beide Agenten.
  - `TestOSJobResumeMidInstall`: Zustand mit `OS.Step=="install"` aus `os-job.json`, neuer Agent ⇒ `os install` einmal erneut, danach `done`, höchstens ein `reboot`.
  - `TestLockRestoredAfterRestart`: nach `loadOSJob()` eines offenen Auftrags liefert `lockAcquire(lockPDH)` `busyBy==lockOS`.
  - `TestOSJobInstallFails`: `installErr` ⇒ Phase `abgebrochen`, `Result` enthält die apt-Zeile, Labels `start-pdh` und `psql-unlock` vorhanden, kein `reboot`, `windowSnapshot().Faulty` leer.
  - `TestOSJobRefusals` (Tabelle): Docker, Verbund (`haInstalled`+`haFakeSocket` mit `"mode":"duo"`), keine Pakete, `freeSpace("/var/cache/apt")` = 100 MB, `freeSpace("/boot")` = 100 MB (auch `force=true` ⇒ Fehler), Sicherung nicht eingerichtet ⇒ `jobNotReady` (409 über `POST /v1/os/job/start`) und mit `{"force":true}` 200.
  - `TestOSJobExcludesPDHJob`: laufender PDH-Auftrag ⇒ `startOSJob` Fehler enthält `PDH-Update`; laufender OS-Auftrag ⇒ `startSingleJob` Fehler enthält `Betriebssystem-Update`.
  - `TestOSJobAbort`: im `UpCountdown` ⇒ `abortSingleJob("Anna")` ⇒ Phase `abgebrochen`, `Result=="Abgebrochen von Anna"`, kein `psql-lock`; bei `OS.Step=="install"` ⇒ Fehler `Die Pakete werden bereits eingespielt`.
  - `TestOSJobHandlers`: `POST /v1/os/job/start` ⇒ 200 `{"ok":true,"hints":[]}`; `GET /v1/update/job` zeigt `kind:"os"`.
  - In `internal/ha`: `TestViewOSKind` prüft Schritte und Texte je Phase/`OSStep` aus der Beschreibung oben (Tabelle mit den sechs Zuständen).
- [ ] **Step 2: Run** `go test ./cmd/update-agent ./internal/ha -run 'TestOSJob|TestLockRestored|TestViewOSKind' -count=1` – erwartet FAIL.
- [ ] **Step 3: Implement** `os_job.go` nach dem Muster `job.go` (`startSingleJob`, `jobStep`, `jobPut`-Entsprechung `osJobPut` mit `writeStateJSON(osJobFile(),…)`), Schleife `jobKick` erweitern (ein Schleifen-Goroutine für beide Arten, `jobStep` ruft je nach offenem Auftrag `osJobStep`); `ha`-Änderungen mit dem Edit-Werkzeug.
- [ ] **Step 4: Run** wie Step 2 plus `go test ./cmd/update-agent ./internal/ha ./cmd/pdh-ha -count=1` – erwartet PASS (pdh-ha unverändert grün).
- [ ] **Step 5: Commit** `git add -A cmd/update-agent internal/ha && git commit -m "Update-Agent: Auftrag Betriebssystem für den Einzelserver mit Neustart und Wiederaufnahme"`

## Task 6: Haken im PDH-Auftrag und Wartungsfenster mit Sicherheitsupdates

**Files:**
- Modify: `cmd/update-agent/job.go` (`singleJob.WithOS`, `singleJob.OS`, `jobStep` Fall `UpRelease`, `startSingleJob`), `cmd/update-agent/job_host.go` (`errNoNewVersion` in `updateTarget`), `cmd/update-agent/window.go` (`windowSingle`), `internal/ha/update_view.go` (Text `OSStep` bei `UpRelease`)
- Test: `cmd/update-agent/job_os_test.go`

**Interfaces:**
- Produces:
  ```go
  var errNoNewVersion = errors.New("Keine neue Version im Update-Zweig") // von updateTarget zurückgegeben
  // singleJob: WithOS bool `json:"with_os"`; OS osSteps `json:"os"`
  func (a *updateAgent) windowOS(ctx context.Context) (string, bool) // Betriebssystem-Fenster ohne PDH-Update
  ```
  Ablauf im PDH-Auftrag: `startSingleJob` setzt `s.WithOS = a.osWithUpdate()`. In `jobStep` Fall `ha.UpRelease`: ist `s.WithOS` und `s.OS.Step != "done"`, ruft er `osAdvance(ctx,&s.OS, persist)` (Phase bleibt `UpRelease`, `Job.OSStep=s.OS.Step`) und kehrt zurück; erst bei `done` folgen `jobUnlock` und `jobFinish(UpDone, …)`. Ergebnis: ohne Pakete `Update auf X abgeschlossen`; mit Erfolg `… abgeschlossen; Betriebssystem: N Pakete, Neustart ja/nein`; bei `Failed` trotzdem `UpDone` mit `… abgeschlossen; Betriebssystem nicht aktualisiert: <Msg>` (kein Zurücksetzen, nicht „fehlerhaft“ markieren). Mit `OSStep != ""` zeigt `viewStep` in `UpRelease` Schritt 4/92 und die Texte aus Task 5 („Betriebssystem-Pakete werden eingespielt“, „Server startet neu“, „Server wird geprüft“).
  Wartungsfenster `windowSingle(ctx, w)`: (1) `w.Install` ⇒ `startSingleJob(ctx, srcWindow, false)`; Erfolg ⇒ „Update gestartet“ (mit Haken fährt der PDH-Auftrag die Pakete mit). Fehler außer `errNoNewVersion` ⇒ wie bisher. (2) Sonst (Install aus oder `errNoNewVersion`): ist `osWithUpdate()` falsch ⇒ bisherige Meldung (`Nichts zu tun: …` bzw. Fehlertext), `false`. Sonst `windowOS`: `osExec("check")`, `osStatus`; `SecurityCount==0` ⇒ „Keine Sicherheitsupdates offen“, `false`; sonst `startOSJob(ctx, srcWindow, false)` ⇒ „Betriebssystem-Update gestartet (N Sicherheitsupdates)“, `true`. Verbund (`windowToHA`) bleibt unverändert (Teil B).

- [ ] **Step 1: Failing tests** `job_os_test.go`:
  - `TestSingleJobWithOS`: `osWithUpdate` an, `osFake` 3 Pakete ⇒ PDH-Auftrag bis `fertig`; `assertOrder(rec.all(), "script --commit "+jobNewSHA+" --no-restart","script --finish","os check","os status","stop-pdh","os install","start-pdh","psql-unlock")`; `view.Result`/`LastResult` enthält `Betriebssystem: 3 Pakete, Neustart nein`; kein `script --rollback`.
  - `TestSingleJobWithOSReboot`: Neustart nötig ⇒ Auftrag aktiv bei `OS.Step=="boot"`, Agent-Neustart (`loadSingleJob`) mit neuer `boot_id` ⇒ `fertig`; `os install` und `reboot` je genau einmal; `psql-unlock` erst danach.
  - `TestSingleJobWithOSNoPending`: 0 Pakete ⇒ kein `stop-pdh` nach `script --finish`, Ergebnis ohne „Betriebssystem“.
  - `TestSingleJobWithOSInstallFails`: ⇒ Phase `fertig`, `Result` enthält `Betriebssystem nicht aktualisiert`, `windowSnapshot().Faulty` leer, `start-pdh` vorhanden.
  - `TestSingleJobWithoutOSUnchanged`: Haken aus ⇒ Labelfolge ohne `os …` (bestehende Auftragstests bleiben grün).
  - `TestSingleJobWithOSSnapshot`: Haken nach dem Start ausschalten ⇒ Auftrag macht die Pakete trotzdem mit (Wert aus `WithOS`).
  - `TestSingleJobAbortDuringOS`: in `UpRelease` mit `OS.Step=="install"` ⇒ `abortSingleJob` Fehler „bereits beendet bzw. zurückgesetzt“.
  - `TestWindowSingleOSNoPDHUpdate`: `jobFakeGit` mit Remote gleich HEAD (⇒ `errNoNewVersion`), Haken an, 2 Sicherheitspakete ⇒ `windowSingle` startet OS-Auftrag (`jobView().Kind=="os"`), Ergebnis enthält `Betriebssystem-Update gestartet (2 Sicherheitsupdates)`.
  - `TestWindowSingleOSVariants` (Tabelle): Haken aus ⇒ kein OS-Auftrag, Ergebnis `Keine neue Version im Update-Zweig`; Haken an, 0 Sicherheit/5 normale ⇒ Ergebnis `Keine Sicherheitsupdates offen`, kein Auftrag; `w.Install=false` + Haken + Sicherheit ⇒ OS-Auftrag; neue PDH-Version + Haken ⇒ PDH-Auftrag mit `WithOS==true`, kein OS-Auftrag.
  - `TestWindowSingleOSWhileJobActive`: bei laufendem PDH-Auftrag startet `windowTick` nichts (bestehendes `jobActive`), und ein direkter Aufruf `windowOS` liefert die Sperrmeldung statt eines zweiten Auftrags.
- [ ] **Step 2: Run** `go test ./cmd/update-agent -run 'TestSingleJobWithOS|TestSingleJobWithoutOS|TestSingleJobAbortDuringOS|TestWindowSingleOS' -count=1` – erwartet FAIL.
- [ ] **Step 3: Implement** wie beschrieben (Edit-Werkzeug; `updateTarget` gibt `errNoNewVersion` zurück, Text unverändert); `windowSingle` und `windowOS`; `ha`-Textzweig.
- [ ] **Step 4: Run** wie Step 2 plus `go test ./cmd/update-agent ./internal/ha -count=1` – erwartet PASS.
- [ ] **Step 5: Commit** `git commit -am "PDH-Auftrag und Wartungsfenster: Betriebssystem mit Haken, Sicherheitsupdates ohne PDH-Update"`

## Task 7: Migration, Recht und Karte in den Core-Einstellungen

**Files:**
- Create: `migrations/222_os_update_permission.up.sql`, `internal/web/os_updates.go`, `internal/web/os_updates_test.go`, `web/templates/widgets/os_update_card.gohtml` (Teil-Vorlage, eingebunden in `web/templates/core_settings.gohtml` direkt hinter dem Ende der Karte „GitHub-Updates“, vor `{{with .NC}}`)
- Modify: `migrations/checksums.txt` (über `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen -count=1`), `internal/web/handler.go` (drei Routen neben Zeile ~1124), `internal/web/core_settings.go` (`CoreSettingsPageData.OS *osView`, Aufruf in `CoreSettingsPage`, Takt in `checkUpdateIfDue`), `internal/web/perm_gate.go` (Regel), `web/templates/widgets/ha.gohtml` (Zeile 58: Titel „Betriebssystem-Update läuft“ bei `Kind "os"`)
- Test: `pkg/database/os_update_migration_test.go` (Integrationstest, Muster eines vorhandenen Rechte-Migrationstests; Build-Tag wie dort)

**Interfaces:**
- Consumes: Agent-Endpunkte aus Task 3 und 5; `h.agentJSON`, `h.agentRaw` (`internal/web/nextcloud_updates.go`), `updateAskPut/updateAskConsume`.
- Produces:
  ```go
  const permOSUpdate = "system.os_update"
  type osView struct { Available, Docker, Cluster, ScriptOutdated, CanAct, WithPDHUpdate, RebootRequired bool; Reason, System, Kernel, CheckedAt string; PackageCount, SecurityCount int; Packages []osPackageView; RebootPkgs []string; LastRun *osLastRun; Job *ha.UpdateView; Err string }
  func (h *Handler) osUpdateViewFor(ctx context.Context, r *http.Request) *osView
  func (h *Handler) OSUpdateCheckWeb(w http.ResponseWriter, r *http.Request)   // POST /core/settings/os-update/check
  func (h *Handler) OSUpdateInstallWeb(w http.ResponseWriter, r *http.Request) // POST /core/settings/os-update/install  (force=1&token=…)
  func (h *Handler) OSUpdateConfigWeb(w http.ResponseWriter, r *http.Request)  // POST /core/settings/os-update/config   (with_pdh_update=1|leer)
  ```
  Migration:
  ```sql
  INSERT INTO permissions (key, label, category) VALUES ('system.os_update', 'Betriebssystem aktualisieren', 'System & Sicherheit') ON CONFLICT (key) DO NOTHING;
  INSERT INTO role_permissions (role_id, permission_id) SELECT DISTINCT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key='system.os_update' AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id=rp.permission_id WHERE rp.role_id=r.id AND rp.department_id IS NULL AND o.key='system.manage_roles') ON CONFLICT DO NOTHING;
  ```
  Verhalten: Die Seite verlangt weiter `system.manage_roles` (`CoreSettingsPage`); `CanAct = h.hasPerm(r, permOSUpdate)`. Karte zeigt je Zustand: Kopfzeile System/Kernel; „N Pakete offen (davon M Sicherheit)“ bzw. „aktuell“; „Neustart nötig“ mit `RebootPkgs`; „geprüft <Zeit>“; „letzter Lauf: <Zeit> – N Pakete, Neustart ja/nein, ok/Fehler“; laufender Auftrag als Fortschritt (`Job.Text`, `Job.Percent`); Knöpfe „Betriebssystem prüfen“ und „Betriebssystem aktualisieren“ (Bestätigungsdialog: „Betriebssystem jetzt aktualisieren? Die Anwender werden dabei abgemeldet, der Server startet bei Bedarf neu.“) und Haken „beim PDH-Update mit aktualisieren“ nur mit `CanAct && Available`; Docker/Verbund: Hinweis `Reason`, Knöpfe und Haken ausgeblendet bzw. deaktiviert mit Hinweis „Im Verbund folgt das Betriebssystem-Update mit Teil B“; Agent nicht erreichbar: Hinweis. Rückfrage „nicht alles grün“: Query `ask=os`, Bestätigung als Formular an `/core/settings/os-update/install` mit `force=1` und Einmal-Token. Gate-Regel: `gr(wPost, permOSUpdate, "", false, `^/core/settings/os-update/(?:check|install|config)$`)`. `checkUpdateIfDue` ruft nach der GitHub-Prüfung (gleiche Bedingung `update_auto_check_enabled`, gleiches Intervall) `POST /v1/os/check` (Fehler nur ins Protokoll). Server-Protokoll: beim Erzeugen von `osView` wird ein `last_run.at`, das größer ist als der Wert der Einstellung `os_update_last_logged`, einmal mit `componentLog("os-update").Info().Int("pakete",…).Bool("neustart",…).Bool("ok",…).Msg("Betriebssystem aktualisiert: N Pakete, Neustart ja/nein")` geschrieben und die Einstellung fortgeschrieben.

- [ ] **Step 1: Failing tests**
  - `TestMigrationOSUpdatePermission` (Integration): Rolle A hat `manage_roles` mit `department_id IS NULL`, Rolle B `manage_roles` nur mit Abteilung, Rolle C nichts ⇒ nach Migration besitzt nur A `system.os_update`; Recht in Kategorie `System & Sicherheit`, Label `Betriebssystem aktualisieren`; Migration zweimal ausführbar (ON CONFLICT).
  - `TestOSRoutesNeedPermission`: Handler mit Attrappen-Agent (`httptest.Server`, `h.updateAgentURL/Token`); Benutzer mit `manage_roles` ohne `system.os_update` ⇒ `POST …/check`, `…/install`, `…/config` 403 (Agent nie gerufen); mit Recht ⇒ Weiterleitung 303 auf `/core/settings?notice=…`.
  - `TestOSCardRender`: gerenderte Seite (`CoreSettingsPage` mit Attrappen-Agent, 19 Pakete/3 Sicherheit, `reboot_required`) enthält `19 Pakete offen`, `3 Sicherheit`, `Neustart nötig`, Knopf „Betriebssystem aktualisieren“ und das Kontrollkästchen `name="with_pdh_update"` nur mit Recht; ohne `system.os_update` enthält sie die Zahlen, aber weder `os-update/install` noch `with_pdh_update`; Docker ⇒ Text `im Docker-Betrieb nicht verfügbar`, keine Knöpfe; Verbund ⇒ Text `Im Verbund folgt das Betriebssystem-Update mit Teil B`; Agent antwortet 404 (zu alt) ⇒ Hinweis, Seite rendert.
  - `TestOSInstallAskFlow`: Agent antwortet 409 `{"ok":false,"error":"Sicherung nicht eingerichtet","hints":["x"]}` ⇒ Weiterleitung enthält `ask=os`; Wiederholung mit `force=1` und Token ⇒ Agent erhält `{"force":true}`; falscher Token ⇒ Agent erhält `{"force":false}`.
  - `TestOSConfigWeb`: `with_pdh_update=1` ⇒ Agent erhält `{"with_pdh_update":true}`, leer ⇒ `false`.
  - `TestOSAutoCheck`: `checkUpdateIfDue` mit fälligem Intervall ⇒ Agent erhält `POST /v1/os/check`; bei `update_auto_check_enabled=false` nicht.
  - `TestOSLastRunLoggedOnce`: zweimal `osUpdateViewFor` mit gleichem `last_run.at` ⇒ ein Protokolleintrag (Zähler über Test-Logsenke wie in vorhandenen `componentLog`-Tests, sonst Einstellung `os_update_last_logged` prüfen).
  - Seitentests `TestHelpCoversAllPages`/`TestHelpChaptersMatch` bleiben grün (kein neuer `Page`-Wert).
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestOS' -count=1` und `go test -tags integration ./pkg/database -run 'TestMigrationOSUpdatePermission' -count=1` (Labor-Datenbank, `PDH_TEST_DSN`) – erwartet FAIL.
- [ ] **Step 3: Implement** Migration, Handler (`h.agentRaw`/`h.agentJSON`), Karte als Teil-Vorlage mit Gestaltungsvariablen (`var(--bg2)`, `var(--border)`, `var(--muted)`, `var(--green)`/`var(--amber)`/`var(--red)`; keine festen Farben; Knöpfe als `btn`/`btn-primary` wie die GitHub-Karte, am Handy umbrechend), Routen, Gate-Regel, `ha.gohtml`-Titel. `permCategoryOrder` enthält „System & Sicherheit“ schon (prüfen). Danach `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen -count=1`.
- [ ] **Step 4: Run** `go test ./internal/web -count=1`, `go test ./pkg/database -run TestMigrationsFrozen -count=1`, `go vet ./...`, Migrationsintegrationstest wie Step 2 – erwartet PASS. Browserprüfung (lokal gerendert oder Testverbund, Fiori/Fluent, hell/dunkel, 320/390/768/1024/1366 px, kein Überlauf, keine Konsolenfehler) und Ergebnis im Commit-Text nennen.
- [ ] **Step 5: Commit** `git add -A migrations internal/web web && git commit -m "Core-Einstellungen: Karte Betriebssystem, Recht system.os_update (Migration 222)"`

## Task 8: Handbuch, Skizze, Änderungsprotokoll, Version, offener Punkt

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `id="versions"` ~Z.2347: Abschnitt „So aktualisierst du das Betriebssystem“ als `.hb-howto`/`.hb-steps`; Kapitel „Administration“: Recht „Betriebssystem aktualisieren“ mit Hinweis „gilt überall“), `web/templates/widgets/help_figures.gohtml` (`hb-fig-update`: Karte „Betriebssystem“ unter „GitHub-Updates“ ergänzen), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (Abschnitt „Offene Punkte“: neuer Punkt 26 „Betriebssystem-Updates Teil A“, Stand, Teil B offen, Abnahme am Testverbund, 202 durch den Anwender)
- Test: `internal/web/help_test.go` (vorhandene Tests), `internal/web/changelog_test.go` (vorhanden)

**Interfaces:**
- `CHANGELOG.md`: neuer Kopf `## [0.82.58] – 2026-11-24` (Platzhalter; beim Merge nächste freie Korrekturversion), `### Neu`: „Betriebssystem-Updates im PDH: Karte ‚Betriebssystem‘ in den Core-Einstellungen zeigt offene Pakete, Sicherheitsupdates und ‚Neustart nötig‘; ‚Betriebssystem aktualisieren‘ spielt sie mit Countdown, Sicherung und Neustart ein (Einzelserver).“, „Haken ‚beim PDH-Update mit aktualisieren‘: Das PDH-Update spielt dann auch die Betriebssystem-Pakete ein; das Wartungsfenster spielt bei gesetztem Haken auch ohne PDH-Update offene Sicherheitsupdates ein.“; `### Sicherheit`: „Neues Recht ‚Betriebssystem aktualisieren‘ (system.os_update); es bekommen alle Rollen, die Rollen verwalten dürfen.“ `VERSION` auf `0.82.58`.
- Handbuch-Inhalt: Schritte (1 Core-Einstellungen öffnen → Karte „Betriebssystem“, 2 „Betriebssystem prüfen“, 3 „Betriebssystem aktualisieren“ und bestätigen, 4 Wartungsband/`/wartung` abwarten, 5 nach dem Neustart prüft das PDH `/health`), Haken-Erklärung, Fehlerfall („Wenn der Server nach dem Neustart nicht antwortet, kann das PDH nichts tun – an der Konsole anmelden und `journalctl -u pdh` ansehen; Protokoll `/var/log/pdh-os-update.log`“), Hinweis Docker/Verbund („im Verbund folgt mit Teil B“).

- [ ] **Step 1: Failing test**: `go test ./internal/web -run 'TestHelp|TestChangelog' -count=1` prüft vorab Bestand; ergänze in `internal/web/os_updates_test.go` `TestHelpMentionsOSUpdate`: gerendertes Handbuchkapitel `versions` enthält `So aktualisierst du das Betriebssystem` und `beim PDH-Update mit aktualisieren`, Kapitel „Administration“ enthält `Betriebssystem aktualisieren`; und (neu in derselben Datei) `TestChangelogMentionsOSUpdate`: oberster Eintrag enthält `Betriebssystem` unter `### Neu` und `system.os_update` unter `### Sicherheit`.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestHelpMentionsOSUpdate|TestChangelogMentionsOSUpdate' -count=1` – erwartet FAIL.
- [ ] **Step 3: Implement** Texte (Anwendersprache, kurze Sätze), Skizze, Changelog, `VERSION` per Edit-Werkzeug; AGENTS.md-Punkt knapp (Stand 24.11.2026, Branch `feature/os-update`, Teil B und Abnahme offen).
- [ ] **Step 4: Run** `go test ./internal/web -run 'TestHelp|TestChangelog' -count=1`, dann gesamt `go vet ./... && go test ./... -count=1` (Git Bash, Labor-Datenbank gesetzt) – erwartet PASS. Vor dem Merge: `git fetch origin main`, `VERSION`/`CHANGELOG.md`/Migrationsnummer 222 gegen `origin/main` prüfen und ggf. umnummerieren (`migrations/checksums.txt` neu erzeugen).
- [ ] **Step 5: Commit** `git commit -am "Handbuch, Änderungsprotokoll und Version für Betriebssystem-Updates Teil A"`

---

## Selbstprüfung gegen den Entwurf (Teil A)

- Skript im Repo, `status`, nur feste Aktionen, Installation mit dem Update: Tasks 1–2. Agent Status/Prüfen/Auftrag/Haken im PDH-Auftrag/Wartungsfenster: Tasks 3–6. Karte, Recht, Migration: Task 7. Handbuch/CHANGELOG: Task 8. Automatische Prüfung im Takt der GitHub-Prüfung: Task 7 (`checkUpdateIfDue`). Server-Protokoll: Task 7. Abbrechen: Task 5. Verbund/Docker: Tasks 3, 5, 7.
- Typen durchgängig: `osSteps` (Task 4) in `osJob` (Task 5) und `singleJob` (Task 6); `osScriptStatus`/`osPackage` (Task 3) in Task 4–7; `lockOS/lockPDH` (Task 3) in Task 5; `ha.UpdateJob.OSStep` (Task 5) in Task 6.
