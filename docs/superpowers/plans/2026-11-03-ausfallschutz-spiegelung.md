# Ausfallschutz Teil 6: Datei-Spiegelung und Nextcloud – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Der aktive Server spiegelt Dateien fast in Echtzeit auf den passiven, und Nextcloud schaltet mit der Rolle um, ohne dass Clients einen anderen Server sehen.

**Architecture:** Reine Regeln (Quellen, Einstellungen, Empfänger-Prüfung) liegen in `internal/ha`. In `cmd/pdh-ha` kommen ein eigener TLS-Kanal auf Port 7480 (`pdh-mirror/1`), ein `mirrorWorker` nach dem Muster des `accessWorker` und ein weiterer `serviceCtl` für Nextcloud im `pgActor` dazu. Der letzte Abgleich läuft im Rollentausch nach `fence()`, also wenn die Dienste schon stehen.

**Tech Stack:** Go, rsync (`--server` über eigenen Kanal), `github.com/fsnotify/fsnotify` v1.7.0, html/template, Bash (Labor, Installationsskript).

**Spec:** `docs/superpowers/specs/2026-11-03-ausfallschutz-spiegelung-design.md`

## Vorbereitung

Zweig `feature/ausfallschutz-spiegelung` ab `feature/verbund-update` (nach dem Merge von PR #14: ab `origin/main`). Spec und dieser Plan sind der erste Commit des Zweigs.

## Global Constraints

- Alle Texte (Oberfläche, Ereignisse, Handbuch, CHANGELOG, Commits) auf Deutsch, für Anwender im Betrieb. Keine Attributionszeilen in Commits.
- Standardwerte: Spiegelung aus, Nextcloud mitnehmen aus, Abstand 1 min, Papierkorb 14 Tage, Alarm nach 5 min, Frist letzter Abgleich 120 s, Bandbreite unbegrenzt.
- Rsync: `-a --numeric-ids --delete --backup --backup-dir=…`; Sammelzeit inotify 2 s.
- Papierkorb: `<Elternordner des Ziels>/.pdh-papierkorb/<JJJJ-MM-TT>/<Label>/`.
- `/etc/pdh/ha/` wird nie gespiegelt, `.env` und cloudflared auch nicht.
- Nextcloud darf nie eine Umschaltung auslösen: Fehler dort nur als Alarm.
- `pdh-ha` startet Dienste nie auf einem nicht aktiven Server. Heartbeat-Felder sind optional (`omitempty`).
- Neue Oberfläche in Fiori und Fluent, hell und dunkel und bei 390 px prüfen; Farben nur über Variablen.
- Migration 155 (vor dem Merge prüfen, ob die Nummer noch frei ist); Version: nächste Korrekturversion nach `git show origin/main:VERSION`, nie eine Nebenversion. CHANGELOG und Handbuch im selben Zug.
- Tests: `go test ./...`. Tests mit echtem rsync tragen `//go:build linux` und laufen im Labor-Container. Gezielte `-run`-Läufe von `internal/web` können wegen `i18nDir` falsch fehlschlagen; maßgeblich ist der Paketlauf.
- Getestet wird nur auf 10.10.0.14/.17, nie auf 10.10.0.202.

## Review Focus

1. **„Zeiten speichern“ auf bestehendem Formular:** `haDoSettings` baut die `ha.Settings` komplett neu. Ohne Vorkehrung schaltet das Speichern der Zeiten die Spiegelung still aus. Erwartung: Mirror-Felder bleiben unverändert (Test in Task 11).
2. **Datei- und Ordnernamen mit Leerzeichen, Umlauten, Zeilenumbruch oder führendem `-`:** Nextcloud-Nutzer haben sie ständig. Erwartung: Übertragung klappt, Pfade sind NUL-getrennt (`--from0`), nichts wird als Option gelesen (Test in Task 5).
3. **Pfad aus inotify liegt außerhalb der Quelle oder ist ein Symlink nach außen:** Erwartung: wird verworfen, `--copy-links` wird nicht gesetzt (Test in Task 5).
4. **Datenordner am Empfänger ist ein nicht eingehängtes Laufwerk:** rsync würde sonst auf die Systemplatte schreiben. Erwartung: Nach dem Erstabgleich lehnt der Empfänger ab, wenn `.ocdata` im Ziel fehlt (Test in Task 6).
5. **Quelle am Absender plötzlich leer, etwa Laufwerk nicht eingehängt:** Sonst räumt `--delete` den Spiegel aus. Erwartung: Vor jedem vollen Lauf wird geprüft, dass die Quelle nicht leer ist, wenn der letzte Lauf Dateien gesehen hat; bei `nextcloud-data` zusätzlich `.ocdata`. Sonst Alarm und kein Lauf (Test in Task 4).

---

### Task 1: Einstellungen und Quellen

**Files:**
- Modify: `internal/ha/types.go`
- Create: `internal/ha/mirror.go`
- Test: `internal/ha/mirror_test.go`

**Interfaces:**
- Produces: Felder `Settings.MirrorOn bool`, `MirrorNextcloud bool`, `MirrorEvery time.Duration`, `MirrorTrashDays int`, `MirrorAlarmAfter time.Duration`, `MirrorFinalMax time.Duration`, `MirrorBWLimitKB int`, `NCDataDir string`, `NCAppDir string`; JSON `mirror_on`, `mirror_nextcloud`, `mirror_every_s`, `mirror_trash_days`, `mirror_alarm_min`, `mirror_final_s`, `mirror_bwlimit_kb`, `nc_data_dir`, `nc_app_dir`.
- Produces: `type MirrorSource struct{ Label, Path string; Instant bool; Exclude []string }`, `func MirrorSources(repo string, s Settings) []MirrorSource`, `func MirrorSourceByLabel(repo string, s Settings, label string) (MirrorSource, bool)`, `func MirrorTrashDir(target, day, label string) string`.

- [ ] **Step 1: Tests schreiben** (`TestMirrorDefaults`, `TestMirrorSettingsJSONOldFile`, `TestMirrorValidate`, `TestMirrorSources`, `TestMirrorTrashDir`)
  - Standardwerte exakt wie in den Global Constraints.
  - JSON ohne die neuen Felder (alte `cluster.json`) ergibt die Standardwerte, nicht 0.
  - `Validate`: Abstand 30–3600 s, Papierkorb 1–365, Alarm 1–1440 min, Frist 10–600 s, Bandbreite ≥ 0; `NCDataDir`, `NCAppDir` absolut und mit Nextcloud an Pflicht; Pfade `/`, `/etc`, `/etc/pdh`, `/etc/pdh/ha`, `/var`, `/opt` abgelehnt.
  - Quellen ohne Nextcloud: `uploads`, `chat_files`, `etc-pdh`; mit Nextcloud zusätzlich `nextcloud-data`, `nextcloud-app`, `nginx`, `ssl-pdh-nextcloud`, `letsencrypt`.
  - `uploads`, `chat_files`, `nextcloud-data` haben `Instant: true`, die anderen nicht.
  - Ausschlüsse: `etc-pdh` enthält `--exclude=/ha/`; `nextcloud-data` enthält `--exclude=/appdata_*/preview/`, `--exclude=*.part`, `--exclude=/nextcloud.log`; `nextcloud-app` schließt den Datenordner aus, wenn er darin liegt.
  - `MirrorTrashDir("/var/www/nextcloud", "2026-11-03", "nextcloud-app") == "/var/www/.pdh-papierkorb/2026-11-03/nextcloud-app"`.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./internal/ha/ -run Mirror` → FAIL, Bezeichner fehlen.
- [ ] **Step 3: Umsetzen.** `DefaultSettings`, `settingsJSON`, `MarshalJSON`, `UnmarshalJSON` und `Validate` erweitern: `UnmarshalJSON` beginnt für die neuen Felder mit den Standardwerten; Standard `NCAppDir = /var/www/nextcloud`.
- [ ] **Step 4: Ausführen:** `go test ./internal/ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Einstellungen und Quellen der Datei-Spiegelung`

### Task 2: Empfänger-Prüfung

**Files:**
- Create: `internal/ha/mirror_recv.go`
- Test: `internal/ha/mirror_recv_test.go`

**Interfaces:**
- Produces: `type MirrorRecvObs struct{ PeerKind string; SelfRole Role; SelfTerm, SenderTerm uint64; MirrorOn bool; LabelKnown bool }`, `func MirrorRecvRefuse(o MirrorRecvObs) string` (leer = erlaubt).
- Produces: `func MirrorServerArgs(client []string, target, trash string) ([]string, error)` – prüft die von rsync gesendeten Argumente und liefert die auszuführenden.

- [ ] **Step 1: Tests schreiben** (`TestMirrorRecvRefuse`, `TestMirrorServerArgs`)
  - Reihenfolge und Texte: Zeuge „Zeuge bekommt keine Dateien“; eigene Rolle nicht passiv „Dieser Server ist nicht passiv“; `SenderTerm < SelfTerm` „Absender hat eine alte Amtszeit“; Spiegelung aus „Spiegelung ist aus“; Label unbekannt „Unbekannte Quelle“.
  - `SenderTerm == SelfTerm` und `SenderTerm > SelfTerm` sind erlaubt.
  - `MirrorServerArgs`: Erwartete Form `rsync --server -<kurz> [erlaubte lange Optionen] . <pfad>`. Das Ergebnis ersetzt `<pfad>` durch `target` und setzt `--backup-dir=<trash>` selbst.
  - Erlaubt: Kurzoptionen nach `^-[a-zA-Z]*(e\.[a-zA-Z0-9.]*)?$`; lang: `--delete`, `--delete-delay`, `--delete-after`, `--numeric-ids`, `--backup`, `--from0`, `--files-from=-`, `--partial-dir=.pdh-teil`, `--delay-updates`, `--bwlimit=<Zahl>`.
  - Abgelehnt: `--sender`, `--rsh=…`, `--rsync-path=…`, `--backup-dir=…` vom Client, `--files-from=/etc/passwd`, `--remove-source-files`, `--temp-dir=…`, `--log-file=…`, unbekannte Optionen, mehr als ein Pfad, Pfad mit `..`, Aufruf ohne `--server`.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./internal/ha/ -run MirrorRecv` → FAIL.
- [ ] **Step 3: Beide Funktionen in `mirror_recv.go` umsetzen.**
- [ ] **Step 4: Ausführen:** `go test ./internal/ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Empfänger prüft Absender, Amtszeit und rsync-Argumente`

### Task 3: Kanal auf Port 7480

**Files:**
- Create: `cmd/pdh-ha/mirror_channel.go`, `cmd/pdh-ha/mirror_rsync_linux_test.go`
- Modify: `cmd/pdh-ha/transport.go` (`alpnMirror`, `NextProtos`, Verteilung in `handleConn`), `cmd/pdh-ha/main.go` (Befehl `mirror-pipe`)
- Test: `cmd/pdh-ha/mirror_channel_test.go`

**Interfaces:**
- Consumes: `ha.MirrorRecvRefuse`, `ha.MirrorServerArgs`, `transport.identify`.
- Produces: `const alpnMirror = "pdh-mirror/1"`, `type mirrorRequest struct{ Label string; Term uint64; Argv []string }`, `type mirrorReceiver interface{ Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) (argv []string, done func(error), refuse string) }`, Feld `transport.mirrorRecv mirrorReceiver`.
- Produces: `func runMirrorPipe(args []string, in io.Reader, out, errw io.Writer) int` mit Aufruf `pdh-ha mirror-pipe --label L --term N --node ID <host> rsync --server …`.

- [ ] **Step 1: Tests schreiben** (`TestMirrorChannelRefused`, `TestMirrorChannelPipes`, `TestMirrorChannelWitnessRejected`, `TestMirrorChannelNoCert`, `TestMirrorHeartbeatUnaffected`)
  - Ablauf auf der Leitung: eine JSON-Zeile `mirrorRequest`, eine Antwortzeile `{"ok":true}` oder `{"error":"…"}`, danach roher Datenstrom.
  - Abgelehnter Aufruf: `runMirrorPipe` schreibt die Meldung nach stderr und liefert 1.
  - Ohne Client-Zertifikat oder mit Zeugen-Zertifikat: Verbindung wird geschlossen, `Accept` nicht aufgerufen.
  - `dialOnce` bleibt bei `alpnHA`: Eine laufende Verbindung sieht weiter Heartbeats, während gleichzeitig ein Datenstrom läuft (Hilfsprozess als Attrappe für rsync).
  - Linux, echtes rsync (`mirror_rsync_linux_test.go`): Zwei Ordner über den Kanal, Dateien kommen an, Gelöschtes liegt im Papierkorb.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run MirrorChannel` → FAIL.
- [ ] **Step 3: Umsetzen.** `handleConn`: Neuer Zweig für `alpnMirror` mit `identify`; Art prüfen; Lesefrist nur für die erste Zeile (10 s), danach keine Fristen. Server startet `exec.CommandContext` mit den Argumenten aus `Accept` und kopiert in beide Richtungen; Client liest Zertifikate aus dem Speicher wie `pdh-ha` selbst, wählt die Adressen des Knotens aus `cluster.json` und nimmt die erste, die antwortet.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: eigener Kanal für rsync auf Port 7480`

### Task 4: Abgleich und Zustand

**Files:**
- Create: `cmd/pdh-ha/mirror_worker.go`
- Test: `cmd/pdh-ha/mirror_worker_test.go`

**Interfaces:**
- Consumes: `ha.MirrorSources`, `eventLog`, `runner`.
- Produces: `type mirrorWorker`; `newMirrorWorker(cfg mirrorDeps) *mirrorWorker`; `(*mirrorWorker).set(want bool, role ha.Role, term uint64, s ha.Settings)`; `.run(ctx)`; `.runAll(ctx) error`; `.finalSync(ctx, max time.Duration) error`; `.snapshot() mirrorState`.
- Produces: `type mirrorState struct{ On, Running, FirstDone bool; FirstPercent int; LastOK time.Time; LastRun time.Duration; PerLabel map[string]mirrorLabelState; Err string; SlowerThanInterval, WatchLimited bool }`, gespeichert in `/var/lib/pdh-ha/mirror.json`.

- [ ] **Step 1: Tests schreiben** (mit `fakeRsync`, das Argumente aufzeichnet)
  - `TestMirrorRsyncArgv`: `rsync -a --numeric-ids --delete --backup --partial-dir=.pdh-teil --delay-updates -e "pdh-ha mirror-pipe --label uploads --term 7 --node b" <Quelle>/ b:<Label>`; `--bwlimit` nur, wenn gesetzt; Ausschlüsse der Quelle; kein `--copy-links`.
  - `TestMirrorOnlyActive`: Bei `passiv` oder `want=false` läuft nichts; Wechsel weg von `aktiv` bricht einen Lauf ab.
  - `TestMirrorNoOverlap`: Ein Lauf länger als der Abstand: kein zweiter gleichzeitig, `SlowerThanInterval=true`, kein Alarm.
  - `TestMirrorAlarmAfter`: Fehler kürzer als `MirrorAlarmAfter`: kein Alarm; danach genau einer (entprellt); nach Erfolg Ereignis „Spiegelung läuft wieder“.
  - `TestMirrorMissingSource`: Quelle fehlt: übersprungen, kein Fehler.
  - `TestMirrorSourceEmptyRefused` (Review Focus 5): Die Quelle sah beim letzten Lauf Dateien und ist jetzt leer: kein Lauf, Alarm „Quelle <Label> ist leer – nicht gespiegelt“. `nextcloud-data` ohne `.ocdata`: dasselbe.
  - `TestMirrorFirstDoneOnlyAfterAll`: `FirstDone` erst, wenn alle Quellen einmal fehlerfrei durch sind; Speicherung übersteht Neustart.
  - `TestMirrorDiskCheck`: Vor dem Erstabgleich Platz zu klein: Alarm „Zu wenig Platz am Partner“, kein Lauf. Platz kommt aus dem Heartbeat (`message.Disk`).
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run Mirror` → FAIL.
- [ ] **Step 3: Umsetzen** nach dem Muster von `accessWorker`: `mu` schützt nur Felder, unter `mu` wird nichts ausgeführt; Anstoß über `kick`, Abstand aus `Settings.MirrorEvery`; Alarme über `eventLog` mit eigener Entprellung.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Abgleich der Dateien am aktiven Server`

### Task 5: Sofort-Übertragung mit inotify

**Files:**
- Create: `cmd/pdh-ha/mirror_watch.go`
- Modify: `go.mod` (fsnotify direkt), `cmd/pdh-ha/mirror_worker.go`
- Test: `cmd/pdh-ha/mirror_watch_test.go`

**Interfaces:**
- Consumes: `mirrorWorker`, `ha.MirrorSource`.
- Produces: `type mirrorWatch`; `newMirrorWatch(sources []ha.MirrorSource, settle time.Duration, emit func(label string, paths []string), limited func()) *mirrorWatch`; `.run(ctx)`; `.pending() map[string][]string`; im Worker `.runPaths(ctx, label string, paths []string) error` mit `--files-from=- --from0`.

- [ ] **Step 1: Tests schreiben**
  - `TestMirrorWatchSettles`: Mehrere Änderungen binnen 2 s ergeben einen Aufruf mit allen Pfaden, Doppelte einmal.
  - `TestMirrorWatchNewDir`: Neuer Ordner wird beobachtet, eine Datei darin sofort gemeldet.
  - `TestMirrorWatchExcluded`: `*.part`, `appdata_x/preview/…` werden nicht gemeldet.
  - `TestMirrorRunPathsNames` (Review Focus 2): `"a b.txt"`, `"ä.pdf"`, `"zeile\num.txt"`, `"-rf"`: stdin ist NUL-getrennt mit relativen Pfaden; Argumente enthalten `--from0`.
  - `TestMirrorWatchOutside` (Review Focus 3): Pfad außerhalb der Quelle wird verworfen; rsync läuft ohne `--copy-links`, ein Symlink bleibt Symlink.
  - `TestMirrorWatchLimit`: Fehler beim Hinzufügen eines Ordners (`ENOSPC`) ruft `limited()` einmal auf; `WatchLimited=true`; der Abgleich läuft weiter.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run MirrorWatch` → FAIL.
- [ ] **Step 3: Umsetzen.** Gelöschte Dateien behandelt nur der volle Abgleich. Fehlschläge von `runPaths` bleiben in `pending`. `finalSync` überträgt zuerst `pending`, dann `nextcloud-app`, `etc-pdh`, `nginx`, `ssl-pdh-nextcloud`, `letsencrypt`. Ist `WatchLimited` gesetzt oder der Watch nicht bereit, läuft stattdessen ein voller Abgleich.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Änderungen sofort übertragen (inotify)`

### Task 6: Empfänger, Papierkorb, Rückkehrer

**Files:**
- Create: `cmd/pdh-ha/mirror_recv.go`
- Test: `cmd/pdh-ha/mirror_recv_test.go`

**Interfaces:**
- Consumes: `mirrorReceiver`, `ha.MirrorRecvRefuse`, `ha.MirrorServerArgs`, `ha.MirrorTrashDir`.
- Produces: `type mirrorInbox` (implementiert `mirrorReceiver`); `.pruneTrash(now time.Time, keepDays int) error`; `.trashInfo() (bytes int64, files int)`; `.emptyTrash(ctx) error`; `.orphans() int`.
- Produces: neue Nachricht `T: "mirror_trash_empty"`.

- [ ] **Step 1: Tests schreiben**
  - `TestInboxAccept`: Liefert Argumente mit Zielpfad aus der eigenen Liste und Papierkorb des heutigen Tags.
  - `TestInboxRefuseOldTerm`: Abgelehnt mit Ereignis am Empfänger „Dateien von <Knoten> abgelehnt: Absender hat eine alte Amtszeit“.
  - `TestInboxMissingTarget`: Zielordner fehlt: Er wird nur angelegt, wenn der Elternordner existiert; sonst Ablehnung „Zielordner fehlt“.
  - `TestInboxDataNotMounted` (Review Focus 4): `nextcloud-data` nach dem Erstabgleich ohne `.ocdata` im Ziel: Ablehnung „Datenordner nicht erreichbar“ und Alarm. Vor dem Erstabgleich ist ein leeres Ziel erlaubt.
  - `TestInboxNginxReload`: Nach `nginx` mit Änderung: `nginx -t`, dann `systemctl reload nginx`; bei Fehler von `nginx -t` Alarm und kein Neuladen; ohne Änderung nichts.
  - `TestPruneTrash`: Ordner älter als 14 Tage weg, jüngere bleiben; fremde Namen bleiben.
  - `TestOrphans`: Erster Lauf nach dem Wechsel von aktiv auf passiv: Dateizuwachs im heutigen Papierkorb wird als Rückkehrer gezählt; spätere Läufe nicht mehr. Der Zähler bleibt bis zum Leeren.
  - `TestEmptyTrash`: Nur `.pdh-papierkorb`-Ordner der eigenen Quellen werden gelöscht.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run 'Inbox|Trash|Orphans'` → FAIL.
- [ ] **Step 3: Umsetzen.** Prune einmal täglich auf jedem Server (auch am aktiven: alte Reste nach Rollenwechsel).
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Dateien empfangen, Papierkorb und Rückkehrer`

### Task 7: Einbau in den Daemon, Status und Befehle

**Files:**
- Modify: `cmd/pdh-ha/daemon.go`, `cmd/pdh-ha/transport.go` (`message`), `cmd/pdh-ha/observe.go`, `cmd/pdh-ha/api.go`, `cmd/pdh-ha/main.go`
- Create: `cmd/pdh-ha/mirror_api.go`
- Test: `cmd/pdh-ha/mirror_api_test.go`

**Interfaces:**
- Consumes: `mirrorWorker`, `mirrorInbox`.
- Produces: `message.MirrorFirst bool`, `message.MirrorAt time.Time`, `message.MirrorOrphans int`, `message.MirrorTrashB int64`, `message.EnvSum string`, `message.NCVer string` (Nextcloud- und PHP-Fassung), alle `omitempty`.
- Produces: `apiStatus.Mirror apiMirror` (`json:"mirror"`): Zustand aus `mirrorState`, `Trash` und `Orphans` des Partners, `EnvSame bool`, `NCVerSame bool`; Socket-Routen `GET /v1/mirror`, `POST /v1/mirror/run`, `POST /v1/mirror/trash-empty`; `func envSum(path string, ignore []string) string`.

- [ ] **Step 1: Tests schreiben**
  - `TestMirrorFirstCarriedOver`: Der Empfänger übernimmt `FirstDone` vom aktiven Partner aus dem Heartbeat; ein neuer aktiver Server verlangt also keinen neuen Erstabgleich.
  - `TestStatusMirrorLine`: `pdh-ha status` zeigt `Spiegelung: läuft, letzter Abgleich vor 12 s`, `Spiegelung: aus`, `Spiegelung: Erstabgleich 40 %`, am Zeugen keine Zeile.
  - `TestMirrorRunRefusedOnPassive`: `pdh-ha mirror run` am passiven Server: „Nur auf dem aktiven Server möglich“.
  - `TestEnvSum`: Ignoriert `PDH_UPDATE_BRANCH`, `PDH_UPDATE_TOKEN`, `PDH_SERVER_HOST`, `PDH_DATABASE_HOST` und alle `PDH_HA_*`; Reihenfolge und Kommentare ändern nichts; ein anderer Wert ändert die Summe.
  - `TestDaemonStartsWorkerOnlyWhenOn`: Mit `MirrorOn=false` läuft kein Worker, aber der Empfänger lehnt ab; Einschalten über Einstellungen startet ihn ohne Neustart.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run 'Mirror|EnvSum'` → FAIL.
- [ ] **Step 3: Umsetzen.** Worker dort setzen, wo `d.access.set` sitzt (`daemon.go`); nur auf Servern, nicht am Zeugen; `transport.mirrorRecv` beim Start setzen; `heartbeatLocked` füllt die Felder; `mirror_trash_empty` löst `emptyTrash` am Empfänger aus.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Spiegelung im Dienst, Status und Befehle`

### Task 8: Rollentausch und Update

**Files:**
- Modify: `cmd/pdh-ha/pgactor.go`, `cmd/pdh-ha/api.go` (`command`), `cmd/pdh-ha/update_api.go` (`readyObsLocked`), `internal/ha/update_ready.go`
- Test: Bestandsdateien von `pgActor` in `cmd/pdh-ha`, `internal/ha/update_test.go`

**Interfaces:**
- Consumes: `mirrorWorker.finalSync`, `mirrorState.FirstDone`.
- Produces: Feld `pgActor.mirrorFinal func(ctx context.Context) error` (nil = keine Spiegelung); `ReadyObs.MirrorOn, MirrorFirstDone bool`.

- [ ] **Step 1: Tests schreiben**
  - `TestDemoteRunsFinalSyncAfterFence`: Reihenfolge `fence`, `mirrorFinal`, `waitCaughtUp`, `stop`; dasselbe in `demoteFreeze`.
  - `TestDemoteFinalSyncFails`: Scheitert `mirrorFinal`: `unfence`, Dienste wieder an, Datenbank schreibbar; Fehlertext „Letzter Abgleich der Dateien nicht fertig: …“; Rolle unverändert.
  - `TestAbandonNoFinalSync`: `abandon` und `force_takeover` rufen `mirrorFinal` nicht auf.
  - `TestSwapRefusedDuringFirstSync`: `command("swap")` mit `MirrorOn` und `!FirstDone`: Fehler „Erstabgleich der Dateien läuft – Rollentausch erst danach möglich“. `force_takeover` wird nicht gesperrt.
  - `TestUpdateReadyMirror`: `MirrorOn && !MirrorFirstDone` ergibt den Grund „Erstabgleich der Dateien läuft“; Spiegelung aus: kein Grund, aber Hinweis „Datei-Spiegelung ist aus: Dateien können nach dem Tausch fehlen“.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ ./internal/ha/ -run 'Demote|Abandon|SwapRefused|UpdateReady'` → FAIL.
- [ ] **Step 3: Umsetzen.** `mirrorFinal` läuft mit Frist `MirrorFinalMax`; die Frist von `waitCaughtUp` zählt separat. Der Kontext der Aktion muss beide Fristen abdecken. `UpdateSwapWait` bleibt 3 Minuten: Der Reiter warnt bei `MirrorFinalMax` über 150 s, dass ein Update mit Rollentausch dann abbrechen kann.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/ ./internal/ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: letzter Abgleich beim Rollentausch, Sperre im Erstabgleich`

### Task 9: Nextcloud je Rolle und Prüflauf

**Files:**
- Create: `cmd/pdh-ha/nextcloud.go`
- Modify: `cmd/pdh-ha/pgactor.go` (`stopServices`, `reconcile`, Start nach `promote`/`unfence`, `keepAutostartOff`), `cmd/pdh-ha/daemon.go`
- Test: `cmd/pdh-ha/nextcloud_test.go`

**Interfaces:**
- Consumes: `serviceCtl`, `ha.Settings.MirrorNextcloud`, `NCAppDir`, `NCDataDir`, `runner`, `pgCtl`.
- Produces: `newNextcloudCtl(r runner, enabled func() bool) serviceCtl` (Labor: `PDH_HA_NC_CMD`); `func ncTrust(ctx context.Context, r runner, appDir string, addr string) error`; `func ncCheckFiles(ctx context.Context, q ncQuery, dataDir string, since time.Time) (missing []string, err error)`; `func ncCertCovers(sitesDir, addr string) (covered, hasTLS bool, err error)`; Feld `pgActor.nc serviceCtl`; `apiMirror.NCMissing []string` (höchstens 200) und `NCMissingTotal int`, `apiMirror.CertCovers *bool` (nil = kein TLS).

- [ ] **Step 1: Tests schreiben**
  - `TestNextcloudCtlStartStop`: Start: `/etc/cron.d/nextcloud.pdh-aus` zurück auf `/etc/cron.d/nextcloud`, PHP-FPM-Einheiten starten, `certbot.timer` starten, falls vorhanden. Stop in umgekehrter Reihenfolge; Autostart von PHP-FPM und Certbot aus. PHP-Einheit aus `systemctl list-unit-files 'php*-fpm.service'`.
  - `TestNextcloudCtlDisabled`: Mit `MirrorNextcloud=false` oder ohne `occ` macht jede Methode nichts und `Active` meldet `false`.
  - `TestStopServicesOrder`: Reihenfolge Tunnel, Nextcloud, pdh.
  - `TestPromoteStartsNextcloudAfterDB`: `nc.Start` erst bei schreibbarer Datenbank.
  - `TestNextcloudFailNoFailover`: `nc.Start` scheitert: Alarm „Nextcloud nicht gestartet: …“, `promote` und `reconcile` liefern kein Fehlerergebnis, das die Automatik abschaltet.
  - `TestReconcilePassiveStopsNextcloud`: Am passiven Server wird Nextcloud gestoppt und bleibt aus.
  - `TestNcTrust`: Adresse fehlt: `occ config:system:set trusted_domains <nächster Index> --value=<Adresse>`; vorhanden: kein Schreibzugriff; nur am aktiven Server.
  - `TestNcCheckFiles`: Abfrage `oc_filecache` verbunden mit `oc_storages`, nur `path LIKE 'files/%'`, keine Ordner, `storage_mtime >= since`; `home::alice` + `files/a.txt` wird `<Daten>/alice/files/a.txt`; vorhanden: nicht gemeldet, fehlt: gemeldet. Tabellenpräfix aus `config.php` (`dbtableprefix`), Standard `oc_`.
  - `TestNcCertCovers`: `ssl_certificate` aus `sites-enabled/*nextcloud*`; Adresse im SAN: `covered=true`; fehlt: `false`; ohne `ssl_certificate`: `hasTLS=false`.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run 'Nextcloud|Nc|StopServices|Promote|ReconcilePassive'` → FAIL.
- [ ] **Step 3: Umsetzen.** Prüflauf einmal nach jeder Übernahme mit `since = MirrorAt` des letzten bekannten Abgleichs, nachdem Nextcloud läuft; Ergebnis als Alarm „Nach der Übernahme fehlen N Nextcloud-Dateien“. PDH-Anhänge: nur Anzahl, aus der Tabelle der Anhänge, im selben Lauf. `occ maintenance:data-fingerprint` läuft nicht automatisch; der Reiter nennt den Befehl, wenn etwas fehlt.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `pdh-ha: Nextcloud schaltet mit der Rolle um, Prüflauf nach der Übernahme`

### Task 10: Einrichtung und Pakete

**Files:**
- Modify: `scripts/install-nextcloud-nginx-postgres.sh`, `scripts/pdh-ha-join.sh`, `scripts/install-pdh-ha.sh`
- Test: `cmd/pdh-ha/scripts_test.go`

**Interfaces:**
- Produces: `install-nextcloud-nginx-postgres.sh --verbund aktiv-neu|aktiv-vorhanden|passiv`.

- [ ] **Step 1: Tests schreiben** (im Stil von `scripts_test.go`)
  - `TestNextcloudVerbundPassiv`: kein `psql`, `createdb`, `occ`, `systemctl enable postgresql`, `restart postgresql`; PHP-FPM wird gestoppt und nicht aktiviert; kein aktiver Cron (`/etc/cron.d/nextcloud.pdh-aus` statt `nextcloud`).
  - `TestNextcloudVerbundAktivNeu`: Anlegen wie bisher; kein `restart postgresql`, kein `enable postgresql`.
  - `TestNextcloudVerbundAktivVorhanden`: Nur Prüfungen, nichts geschrieben, Abbruch mit Meldung, wenn `config.php` fehlt.
  - `TestNextcloudOhneVerbundUnveraendert`: Ohne `--verbund` verhält sich das Skript wie heute (`--restore`, Normalmodus).
  - `TestJoinPackagesRsync`: `rsync` steht in der Paketliste von `pdh-ha-join.sh` und `install-pdh-ha.sh`, nur für Server, nicht für den Zeugen.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./cmd/pdh-ha/ -run 'NextcloudVerbund|NextcloudOhne|JoinPackages'` → FAIL.
- [ ] **Step 3: Umsetzen.** `--verbund` gibt am Ende die Kennungen von `www-data` und `pdh` aus und bricht mit Meldung ab, wenn sie von `--uid-www-data`/`--uid-pdh` abweichen (am ersten Server abgelesen, Befehl steht in der Ausgabe). Warnung, wenn `pg_hba.conf` die Rolle `nextcloud` lokal nicht zulässt. Offenen Punkt aus Spec 14 klären: `sudo -u www-data php occ status` mit nur lesbarer Datenbank ausprobieren und das Ergebnis im Ledger festhalten; `--verbund passiv` braucht `occ` ohnehin nicht.
- [ ] **Step 4: Ausführen:** `go test ./cmd/pdh-ha/` → ok.
- [ ] **Step 5: Commit:** `Nextcloud-Skript: Verbund-Modus ohne Eingriff in Postgres, rsync als Paket`

### Task 11: Oberfläche und Rechte

**Files:**
- Create: `migrations/155_file_mirror.up.sql`, `migrations/155_file_mirror.down.sql`, `internal/web/ha_mirror.go`, `web/templates/widgets/ha_mirror.gohtml`
- Modify: `internal/web/ha.go` (`haDoSettings`), `internal/web/handler.go`, `internal/web/perm_gate.go`, `web/templates/widgets/ha.gohtml`, `web/i18n/{en,mk,ro,tr}.json`
- Test: `internal/web/ha_mirror_test.go`

**Interfaces:**
- Consumes: `/v1/status` (`apiMirror`), `/v1/mirror/run`, `/v1/mirror/trash-empty`, `PUT /v1/settings`.
- Produces: Rechte `system.file_mirror`, `system.file_mirror_admin`; `areaAdminPerm["file_mirror"] = "system.file_mirror_admin"`; Routen `POST /admin/server-config/ha/mirror`, `/ha/mirror-run`, `/ha/mirror-trash-empty`; Vorlage `{{define "ha-mirror"}}`.

- [ ] **Step 1: Tests schreiben**
  - `TestHASettingsKeepMirror` (Review Focus 1): `haDoSettings` liest die aktuellen Einstellungen und behält alle Mirror-Felder.
  - `TestHAMirrorSettingsSaved`: Formular setzt die Mirror-Felder, Zeiten bleiben. Ungültige Werte: Fehlermeldung, nichts gespeichert.
  - `TestHAMirrorPerms`: Ohne `system.file_mirror`: 403 auf `mirror` und `mirror-run`; ohne `system.file_mirror_admin`: 403 auf `mirror-trash-empty`, mit `system.file_mirror` allein ebenfalls.
  - `TestMirrorMigration155`: Rechte vorhanden, Rollen mit `system.server_config` (überall geltend) haben beide; auf Abteilung begrenzte Quellrechte geben sie nicht (Muster Migration 154).
  - `TestHAMirrorCardStates`: Aus; läuft mit Zeit; Erstabgleich mit `<progress>`; Warnungen für Fehler, `SlowerThanInterval`, `WatchLimited` mit Befehl `sudo sysctl -w fs.inotify.max_user_watches=…`, `EnvSame=false`, `NCVerSame=false`, `CertCovers=false`, `PDH_NEXTCLOUD_BASEURL` mit fester Adresse, `MirrorFinalMax` über 150 s, `NCMissing`, Rückkehrer; am Zeugen und im Einzelbetrieb keine Karte.
  - `TestI18nCatalogsComplete` bleibt grün.
- [ ] **Step 2: Ausführen, Fehlschlag prüfen:** `go test ./internal/web/` → FAIL.
- [ ] **Step 3: Umsetzen.** Muster: `haRoutePerm` aus `maint_window.go`, Vorlage als eigener `define`, eingebunden in `ha-admin`. Der Knopf „Papierkorb leeren“ fragt mit `confirm` nach und erscheint nur mit dem Recht.
- [ ] **Step 4: Ausführen:** `go test ./internal/web/` → ok.
- [ ] **Step 5: Im Browser prüfen.** Vorlage rendern wie beim Wartungs-Band (vorübergehende Testdatei, Edge über DevTools-Protokoll), Fiori und Fluent, hell und dunkel, 390 und 1366 px: kein Überlauf (`scrollWidth == innerWidth`), Knöpfe Kontrast ≥ 4,2.
- [ ] **Step 6: Commit:** `PDH: Karte Datei-Spiegelung, Rechte system.file_mirror und system.file_mirror_admin`

### Task 12: Labor

**Files:**
- Modify: `deploy/ha-lab/ha-lab.sh`, `deploy/ha-lab/compose.yaml`, `deploy/ha-witness/Dockerfile` (Stufe `lab-pg`: `rsync`), `deploy/ha-lab/README.md`
- Create: `deploy/ha-lab/nc-dummy.sh`

**Interfaces:**
- Consumes: `PDH_HA_NC_CMD`, `pdh-ha mirror status|run`, `PUT /v1/settings`.
- Produces: Szenarien `spiegel-datei`, `spiegel-loeschen`, `spiegel-tausch`, `spiegel-tausch-frist`, `spiegel-ausfall`, `spiegel-rueckkehr`, `spiegel-heartbeat`, `spiegel-nextcloud`, `spiegel-aus`.

- [ ] **Step 1: Szenarien schreiben**
  - `spiegel-datei`: Datei mit Leerzeichen und Umlaut in `uploads`; binnen 10 s auf node2 mit gleichem Inhalt und Besitzer.
  - `spiegel-loeschen`: Löschen auf node1; nach einem Abstand auf node2 weg und im Papierkorb.
  - `spiegel-tausch`: Schreib-Schleife auf node1 bis zum Tausch; nach dem Tausch fehlt keine bestätigte Datei.
  - `spiegel-tausch-frist`: Bandbreite klein, große Datei, Frist 10 s: Tausch bricht ab, node1 bleibt aktiv, PDH läuft wieder.
  - `spiegel-ausfall`: node1 hart anhalten, `oc_filecache`-Attrappe mit einer nicht übertragenen Datei: Alarm mit dem Namen.
  - `spiegel-rueckkehr`: node1 hart weg, Datei auf node1 neu, Übernahme, node1 zurück: Datei im Papierkorb von node1, Zähler 1, alter Absender wurde abgelehnt (Ereignis).
  - `spiegel-heartbeat`: 500 MB Dateilast: kein Rollenwechsel und keine Amtszeit-Änderung über 2 min.
  - `spiegel-nextcloud`: Attrappe läuft nur am aktiven Server, auch nach Tausch und Neustart.
  - `spiegel-aus`: Spiegelung aus: nichts übertragen, Verhalten wie bisher, Empfänger lehnt ab.
- [ ] **Step 2: Ausführen:** `ha-lab.sh spiegel-datei` bis `spiegel-aus` einzeln, dann `ha-lab.sh alle` und `ha-lab.sh race`. Erwartet: `BESTANDEN: alle 45 Szenarien` (36 + 9), Race ohne `DATA RACE`.
- [ ] **Step 3: Gefundene Fehler beheben, jeweils mit einem Test im betroffenen Paket.**
- [ ] **Step 4: Commit:** `Labor: neun Szenarien zur Datei-Spiegelung`

### Task 13: Handbuch, Änderungsprotokoll, Gesamt-Spec

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `ausfallschutz`), `web/templates/widgets/help_figures.gohtml`, `CHANGELOG.md`, `VERSION`, `docs/superpowers/specs/2026-10-24-ausfallschutz-design.md`

- [ ] **Step 1: Handbuch.** Hinweis „Hochgeladene Dateien werden noch nicht gespiegelt…“ entfernen. Neu: `.hb-howto` „Spiegelung einschalten“, „Nextcloud in den Verbund nehmen“ (Reihenfolge der drei `--verbund`-Fälle, Zertifikat, Name), Fehlerbilder aus Spec Abschnitt 8, Papierkorb, Hinweis: Der letzte Abgleich gehört zur kurzen Sperre des Tauschs. Abschnitt „Administration“: beide Rechte.
- [ ] **Step 2: Skizze anpassen und Gesamt-Spec korrigieren** (Abschnitt 9: Kanal statt SSH; Abschnitt 16: Satz zu Nextcloud).
- [ ] **Step 3: CHANGELOG und VERSION.** Nächste Korrekturversion; `### Neu`, `### Sicherheit`.
- [ ] **Step 4: Ausführen:** `go test ./...` → ok, auch `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`.
- [ ] **Step 5: Commit:** `Handbuch und Änderungsprotokoll: Datei-Spiegelung und Nextcloud im Verbund`

### Task 14: Abnahme im echten Netz

Findet auf 10.10.0.14/.17 mit Zeuge statt. Der Anwender führt die Befehle aus; jede Nachricht nennt sie vollständig, mit Rechner und erwartetem Ergebnis. Erst nach dem Merge von PR #14 und dem Wechsel der Testserver auf `main` beginnen (Updates laufen nur vorwärts: einmal von Hand).

- [ ] **Fall 1: Spiegelung ohne Nextcloud.** Einschalten, Erstabgleich, PDH-Anhang hochladen: binnen Sekunden auf dem passiven Server; `pdh-ha mirror status`.
- [ ] **Fall 2: Tausch hin und zurück.** Der Anhang öffnet sich im PDH auf beiden Seiten; Dauer der Sperre notieren.
- [ ] **Fall 3: Harter Ausfall.** Aktive VM ausschalten, Übernahme, Prüflauf, Rückkehr, Rückkehrer im Papierkorb.
- [ ] **Fall 4: Nextcloud installieren.** `--verbund aktiv-neu` am aktiven, `--verbund passiv` am passiven Server; den Namen legt der Anwender erst jetzt in Cloudflare an; `10.10.0.15` in `trusted_domains`; Server-Zertifikat aus dem PDH.
- [ ] **Fall 5: Nextcloud umschalten.** Tausch und Ausfall mit Web, Desktop-Client und Handy-App, über Tunnel und `10.10.0.15`: Clients melden sich neu an, ohne Neuabgleich aller Dateien und ohne Konfliktkopien; Freigabe-Link und Kalender funktionieren.
- [ ] **Fall 6: Karten-Test.** `sudo systemd-run --on-active=2 --unit=pdh-netz-test /bin/sh -c 'ip link set eth0 down; sleep 6; ip link set eth0 up'` am aktiven Server: Spiegelung holt selbst auf, Alarm nur nach Überschreiten der Frist.
- [ ] **Abschluss:** Gesamtreview, Prüfung vor dem Merge nach AGENTS.md, Pull-Request; Merge nur mit Freigabe des Anwenders.
