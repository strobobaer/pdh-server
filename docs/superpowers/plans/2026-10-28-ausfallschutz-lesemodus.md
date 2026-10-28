# Ausfallschutz Teil 4 – Lesemodus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Das PDH läuft auf dem passiven Server als „Spiegel – nur lesen“: Admins (und bei Ausfall des aktiven Servers alle) können sich anmelden und ansehen, nichts wird geschrieben.

**Architecture:** Neues Paket `internal/mirror` erkennt den Modus (`pg_is_in_recovery()`/`transaction_read_only`) und überwacht ihn; `cmd/server/main.go` überspringt im Spiegelmodus alle schreibenden Startschritte und Hintergrundjobs und beendet sich bei Moduswechsel. Im Web-Handler sperrt eine Middleware alle Schreibanfragen, eine Anmelde-Schranke setzt Regel C über den pdh-ha-Status um, ein pgx-Tracer macht aus durchgerutschten Schreibzugriffen (SQLSTATE 25006) eine freundliche Meldung. pdh-ha steuert `pdh` und `cloudflared` getrennt und lässt `pdh` am passiven Knoten laufen, sobald die Datenbank Standby ist.

**Tech Stack:** Go 1.24, chi, pgx/v5 (pgxpool, QueryTracer), html/template, PostgreSQL 18, systemd, Docker (Labor).

**Spec:** `docs/superpowers/specs/2026-10-27-ausfallschutz-lesemodus-design.md`

## Global Constraints

- Bandtext Spiegel: „Spiegel – nur lesen · Datenstand vor <Dauer> · Aktiver Server: <Adresse> [öffnen]“ (orange).
- Bandtext ohne aktiven Server: „Aktiver Server nicht erreichbar – Daten vom TT.MM. HH:MM“ (rot), Admins mit Knopf zum Reiter Ausfallschutz.
- Bandtext eingeschränkt: „Spiegel wartet auf das Update des aktiven Servers“.
- Sperrmeldung: „Dieser Server ist der Spiegel – nur lesen. Änderungen bitte am aktiven Server.“ – HTTP 409.
- Anmeldehinweis Nicht-Admin: „Bitte den aktiven Server verwenden“ mit Link.
- Wächter: alle 5 s; Datenbank 60 s nicht erreichbar → beenden.
- Admin = `canServerConfig` (`actorIsAdmin && hasPerm("system.server_config")`).
- pdh-ha-Status zwischenspeichern ca. 5 s (`GET /v1/status` über `h.haDo`, Socket `PDH_HA_SOCKET` bzw. `/run/pdh-ha/api.sock`).
- `/health` liefert zusätzlich `"mode": "spiegel"` (bzw. `"normal"`).
- Einzelserver ohne Verbund: Verhalten unverändert.
- Neue Nebenversion `0.77.0` (`VERSION`, `CHANGELOG.md`), Handbuch im selben Schritt.
- Commits ohne Attribution-Zeilen; Code-Kommentare deutsch, Stil wie Umgebung.

**Spec-Korrekturen (beim Planen gefunden, in Task 1 in die Spec übernehmen):**
- `haWatch` läuft im Spiegelmodus **nicht**: es schreibt `app_settings` (Ereignis-Zähler) und Nachrichten an Admins – das macht der aktive Server.
- `VPNStartup` entfällt im Spiegelmodus (WireGuard gehört zum aktiven Server).
- Die 25006-Abfangstelle ist ein pgx-`QueryTracer` am Pool, der die Anfrage markiert, plus Middleware, die eine 5xx-Antwort markierter Anfragen durch die Sperrmeldung ersetzt (es gibt keine zentrale Fehlerstelle).

## Review Focus

1. **Anmeldung eines Nicht-Admins, während der aktive Server erreichbar ist** – darf nicht hineinkommen, auch nicht über RFID-Anmeldung oder eine schon laufende Sitzung (Task 4, `TestMirrorGateExistingSession`).
2. **GET-Seite, die nebenbei schreibt** (letzte Aktivität, Spracheinstellung, „gelesen“-Markierungen) – Seite muss trotzdem anzeigen statt 500 (Task 3 Tracer-Test, Task 7 Crawler).
3. **Übernahme während Benutzer am Spiegel angemeldet sind** – Sitzung bleibt gültig, nach dem Neustart des PDH (≤ 10 s) kann geschrieben werden (Task 7 Szenario `hochstufen`).
4. **Spiegel hat schon das neuere Programm, die Datenbank noch nicht die Migrationen** – kein Absturz, eingeschränkter Modus (Task 2, `TestSchemaBehind`; Task 7 `version`).
5. **htmx-Klick auf „Speichern“** – Meldung als Hinweis statt leerer Austausch im Seitenteil (Task 3, `TestReadOnlyBlocksHTMX`).

---

### Task 1: Paket `internal/mirror` – Modus erkennen und überwachen

**Files:**
- Create: `internal/mirror/mirror.go`
- Test: `internal/mirror/mirror_test.go`
- Modify: `docs/superpowers/specs/2026-10-27-ausfallschutz-lesemodus-design.md` (Spec-Korrekturen oben in Abschnitt 3/4 eintragen)

**Interfaces:**
- Produces:
  - `type Mode int` mit `Normal`, `ReadOnly`; `func (m Mode) String() string` → `"normal"` / `"spiegel"`
  - `type Prober interface { QueryRow(ctx context.Context, sql string, args ...any) pgx.Row }` (erfüllt von `*pgxpool.Pool`)
  - `func Detect(ctx context.Context, p Prober) (Mode, error)` – eine Abfrage `SELECT pg_is_in_recovery(), current_setting('transaction_read_only') = 'on'`
  - `func Watch(ctx context.Context, p Prober, start Mode, every, lost time.Duration, onChange func(reason string))` – blockiert; ruft `onChange` genau einmal bei abweichendem Modus oder wenn `Detect` länger als `lost` durchgehend fehlschlägt, dann Rückkehr
  - `func ReplayLag(ctx context.Context, p Prober) (lag time.Duration, at time.Time, err error)` – `SELECT now() - pg_last_xact_replay_timestamp(), pg_last_xact_replay_timestamp()`; NULL → `err == nil`, `at.IsZero()`

- [ ] **Step 1: Tests schreiben** (Fake-`Prober` mit Liste von Antworten/Fehlern)
  - `TestDetect`: `(false,false)` → `Normal`; `(true,false)` → `ReadOnly`; `(false,true)` → `ReadOnly`; Fehler → Fehler.
  - `TestWatchChange`: Start `ReadOnly`, Antworten `ReadOnly, ReadOnly, Normal` mit `every=1ms` → `onChange` einmal, Grund enthält `"spiegel → normal"`.
  - `TestWatchDBLost`: nur Fehler, `lost=5ms` → `onChange`, Grund enthält `"Datenbank"`; kurze Fehlerfolge (< `lost`) gefolgt von gleichem Modus → kein `onChange` bis `ctx` endet.
  - `TestReplayLagNull`: NULL-Zeile → `at.IsZero()`, kein Fehler.
- [ ] **Step 2:** `go test ./internal/mirror` → FAIL (Paket fehlt).
- [ ] **Step 3:** Implementieren wie in Interfaces.
- [ ] **Step 4:** `go test ./internal/mirror` → PASS.
- [ ] **Step 5:** Spec-Korrekturen eintragen, commit `feat(mirror): Spiegelmodus erkennen und überwachen`.

### Task 2: Startpfad im Spiegelmodus (`cmd/server`)

**Files:**
- Modify: `cmd/server/main.go` (Start ~Zeile 95–135, 252–283, `/health` ~301, `loadServerSettings` ~436)
- Create: `cmd/server/mirror_start.go` (Hilfsfunktionen), Test: `cmd/server/mirror_start_test.go`
- Modify: `internal/web/handler.go` (Feld + Setter)

**Interfaces:**
- Consumes: `mirror.Detect`, `mirror.Watch`, `database.PendingMigrations(ctx, pool, dir) (pending []string, initialized bool, err error)`
- Produces:
  - `type web.MirrorInfo struct { On bool; Restricted bool }` und `func (h *Handler) SetMirror(m MirrorInfo)`, `func (h *Handler) Mirror() MirrorInfo`
  - `func schemaBehind(pending []string, initialized bool) bool` – `initialized && len(pending) > 0`
  - `func loadServerSettingsReadOnly(db *database.DB) *config.Config` – nur `config.ApplyDBSettings` + `config.Load` + `Validate` + `logger.Init`; fehlt/ungültig `PDH_AUTH_JWTSECRET` → `log.Fatal` („im Spiegel nicht erzeugbar“)
  - Exit-Code bei Moduswechsel: `os.Exit(3)` nach Protokolleintrag `"spiegelmodus gewechselt – neustart"`

- [ ] **Step 1: Test** `TestSchemaBehind` (Tabelle: nicht initialisiert → false; keine offenen → false; offene → true).
- [ ] **Step 2:** `go test ./cmd/server -run SchemaBehind` → FAIL.
- [ ] **Step 3: Implementieren.** Direkt nach `openDatabase`: `mode, err := mirror.Detect(...)` (Fehler → wie heute weiter, `Normal`). Bei `ReadOnly`:
  - überspringen: `BackupBeforeUpdate`, `RunMigrations`, `grafanadb.EnsureReader`, Einrichtungsassistent (keine Benutzer → `log.Fatal`), `loadServerSettings` → `loadServerSettingsReadOnly`;
  - `Restricted = schemaBehind(database.PendingMigrations(...))`;
  - nach `NewHandler`: `webHandler.SetMirror(...)`; alle Aufrufe Zeilen 264–283 außer `ConfigureMail` und `LoadIconOverrides` überspringen (ein `if mode != mirror.ReadOnly { … }`-Block, `maintSvc.StartScheduler` und `faultSvc.StartPendingTicketWatcher` eingeschlossen);
  - `go mirror.Watch(ctx, db.Pool, mode, 5*time.Second, 60*time.Second, func(r string){ log…; os.Exit(3) })` – **auch im Normalmodus** (fängt Abschottung);
  - `/health`: Feld `"mode": mode.String()`.
- [ ] **Step 4:** `go test ./cmd/server ./internal/web` → PASS; `go build ./...`.
- [ ] **Step 5:** Commit `feat(server): Start im Spiegelmodus ohne Schreibzugriffe`.

### Task 3: Schreibsperre, Tracer und versteckte Schreibzugriffe

**Files:**
- Create: `internal/web/mirror.go`, Test: `internal/web/mirror_test.go`, Template `web/templates/mirror_readonly.gohtml`
- Modify: `internal/web/handler.go:331-336` (Middleware in `Routes()` direkt nach `authMiddleware`), `internal/web/presence.go:32` (`presenceTouch` → früh zurück im Spiegel), `internal/web/i18n.go:186` (`UPDATE users SET language` nur außerhalb des Spiegels)
- Modify: `pkg/database/database.go:17-30` (Tracer in `poolCfg.ConnConfig.Tracer`), Test `pkg/database/readonly_tracer_test.go`
- Modify: `web/templates/base.gohtml` (htmx-Listener ~1954: Ereignis `pdh-toast` mit vorhandenem Toast anzeigen, falls noch nicht vorhanden)

**Interfaces:**
- Consumes: `Handler.Mirror()`
- Produces:
  - `func (h *Handler) MirrorMiddleware(next http.Handler) http.Handler`
  - `func mirrorAllowedWrite(path string) bool` – `true` für `/login`, `/login/*`, `/logout`, `/lang`, `/admin/server-config/ha/*`
  - `func mirrorRefuse(w http.ResponseWriter, r *http.Request)` – 409; bei `HX-Request: true` Header `HX-Reswap: none` und `HX-Trigger: {"pdh-toast":{"text":"<Sperrmeldung>","kind":"warn"}}`, sonst Seite `mirror_readonly.gohtml` (Text + Link aktiver Server)
  - `func database.ReadOnlyMark(ctx context.Context) (context.Context, *atomic.Bool)` und `type readOnlyTracer` (implementiert `pgx.QueryTracer`; `TraceQueryEnd` setzt die Marke bei `*pgconn.PgError` mit `Code == "25006"` und protokolliert `"schreibzugriff im spiegel"` mit dem SQL-Anfang)
- Middleware-Ablauf: Spiegel aus → durchreichen. Spiegel an: Marke in den Kontext; Methode nicht GET/HEAD/OPTIONS und `!mirrorAllowedWrite` → `mirrorRefuse`. Sonst über einen Antwort-Puffer (Muster `statusRecorder` aus `request_once.go`): Status ≥ 500 **und** Marke gesetzt → Puffer verwerfen, `mirrorRefuse`.

- [ ] **Step 1: Tests schreiben**
  - `TestReadOnlyBlocksWrites`: Spiegel an, `POST /tickets/x/comment` → 409, Body enthält Sperrmeldung; `GET /` → durchgereicht.
  - `TestReadOnlyAllowsAuth`: `POST /login`, `GET /logout`, `POST /lang`, `POST /admin/server-config/ha/command` → durchgereicht.
  - `TestReadOnlyBlocksHTMX`: `POST` mit `HX-Request: true` → 409, `HX-Reswap: none`, `HX-Trigger` enthält `pdh-toast`.
  - `TestReadOnlyOffPassesAll`: Spiegel aus, `POST` → durchgereicht.
  - `TestReadOnlyTracerConverts500`: Handler setzt die Marke aus dem Kontext und schreibt 500 → Antwort 409 mit Sperrmeldung; ohne Marke bleibt 500.
  - `TestPresenceSkippedInMirror`: im Spiegel ändert `presenceTouch` `presence.saved` nicht.
  - `pkg/database`: `TestReadOnlyTracerMarks` – `TraceQueryEnd` mit `&pgconn.PgError{Code:"25006"}` setzt die Marke, anderer Code nicht, Kontext ohne Marke ohne Panik.
- [ ] **Step 2:** `go test ./internal/web -run 'ReadOnly|Presence'` und `go test ./pkg/database -run ReadOnlyTracer` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web ./pkg/database` komplett grün.
- [ ] **Step 5:** Commit `feat(web): Schreibsperre im Spiegelmodus`.

### Task 4: Anmeldung nach Regel C und eingeschränkter Modus

**Files:**
- Modify: `internal/web/mirror.go`, `internal/web/handler.go` (`authMiddleware` ~2178; Login-Seite zeigt Hinweis), `web/templates/login.gohtml` (Hinweis bei `?spiegel=1`)
- Test: `internal/web/mirror_test.go`

**Interfaces:**
- Consumes: `h.haDo(ctx, http.MethodGet, "/v1/status", nil, &haStatus)` (`ha.go:359`), `haStatus.Peers[].Role`/`.Seen`/`.Paths[].Addr`, `h.canServerConfig(r)`
- Produces:
  - `type mirrorPeer struct { Known bool; ActiveSeen bool; ActiveAddr string }`
  - `func (h *Handler) mirrorPeerState(ctx context.Context) mirrorPeer` – 5 s zwischengespeichert (Mutex + Zeitstempel im Handler); Abfrage über ein austauschbares Funktionsfeld, damit Tests ohne Socket laufen; Fehler → `Known=false`; `ActiveAddr` = Host aus `Paths[0].Addr` ohne Port
  - `func mirrorLoginAllowed(admin bool, p mirrorPeer) bool` – `admin || (p.Known && !p.ActiveSeen)`
  - `func mirrorRestrictedAllowed(path string) bool` – `/admin/server-config` (mit Unterpfaden), `/login*`, `/logout`, `/static/*`, `/lang`
- `authMiddleware`: nach Setzen des Benutzers, wenn `Mirror().On`: `!mirrorLoginAllowed(canServerConfig, peer)` → Cookies löschen wie im vorhandenen Fehlerzweig, Redirect `/login?spiegel=1`. `Restricted`: Nicht-Admin → wie oben; Admin auf nicht erlaubtem Pfad → Redirect `/admin/server-config?tab=ha`. Login-Seite bei `spiegel=1`: „Bitte den aktiven Server verwenden“ + Link (`ActiveAddr`, Port der aktuellen Anfrage).

- [ ] **Step 1: Tests schreiben**
  - `TestMirrorLoginAllowed` (Tabelle): Admin × {gesehen, nicht gesehen, unbekannt} → true; Nicht-Admin: gesehen → false, nicht gesehen → true, unbekannt → false.
  - `TestMirrorGateExistingSession`: Spiegel an, Peer `ActiveSeen`, Sitzung Nicht-Admin, `GET /` → 302 `/login?spiegel=1`, Cookie `pdh_token` gelöscht.
  - `TestMirrorRestrictedRedirect`: `Restricted`, Admin, `GET /tickets` → 302 `/admin/server-config?tab=ha`; `GET /admin/server-config` → durchgereicht.
  - `TestMirrorPeerFromStatus`: Status mit Peer `{Role:"aktiv", Seen:true, Paths:[{Addr:"10.10.0.17:7946"}]}` → `ActiveSeen`, `ActiveAddr == "10.10.0.17"`.
- [ ] **Step 2:** `go test ./internal/web -run Mirror` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `feat(web): Anmeldung am Spiegel nach Regel C`.

### Task 5: Band „Spiegel – nur lesen“

**Files:**
- Modify: `internal/web/handler.go` (`BaseData` + Feld, `baseData` ~1062), `internal/web/mirror.go`, `web/templates/base.gohtml` (Band oben im Inhalt), Stylesheet mit vorhandenen Farb-Token
- Test: `internal/web/mirror_test.go`

**Interfaces:**
- Consumes: `mirror.ReplayLag`, `mirrorPeerState`, `Mirror()`
- Produces: `type MirrorBanner struct { Restricted, ActiveSeen, Admin bool; Lag, DataAt, ActiveURL string }`; `BaseData.Mirror *MirrorBanner` (nil außerhalb des Spiegels); `func mirrorLagText(d time.Duration) string`

- [ ] **Step 1: Tests:** `TestMirrorLagText` (0 s → „0 s“, 59 s → „59 s“, 61 s → „1 min“, 3900 s → „1 h 5 min“); `TestMirrorBannerRendered` – Seite mit `Mirror` enthält „Spiegel – nur lesen“ und den Link; `ActiveSeen=false` → „Aktiver Server nicht erreichbar – Daten vom“; `Restricted` → „Spiegel wartet auf das Update des aktiven Servers“; `Mirror == nil` → kein Band.
- [ ] **Step 2:** → FAIL. **Step 3:** Implementieren (orange = Warn-Token, rot = Fehler-Token). **Step 4:** → PASS.
- [ ] **Step 5:** Commit `feat(web): Band im Spiegelmodus`.

### Task 6: pdh-ha – PDH am passiven Server, Tunnel getrennt

**Files:**
- Modify: `cmd/pdh-ha/svc.go`, `cmd/pdh-ha/pgactor.go` (`newPGActor` ~48, `promote` ~161, `fence` ~192, `unfence` ~210, `reconcile` ~229, `recopy` ~282), `cmd/pdh-ha/rejoin.go:186`, `cmd/pdh-ha/replica.go:157,187,190-205`, `cmd/pdh-ha/primary.go:160,387`, `cmd/pdh-ha/daemon.go:373`, `cmd/pdh-ha/main.go:204,256`, `cmd/pdh-ha/messages.go:184`
- Test: `cmd/pdh-ha/svc_test.go`, vorhandene `pgActor`-Tests, `cmd/pdh-ha/replica_test.go:170-190`

**Interfaces:**
- Produces:
  - `serviceCtl` bekommt `Restart(ctx) error`; `systemdService` steuert **genau eine** Einheit, `units()`/`each()` entfallen.
  - `func newServiceCtl(r runner) serviceCtl` → `pdh` (bzw. `PDH_HA_SERVICE_CMD`)
  - `func newTunnelCtl(r runner) serviceCtl` → `cloudflared`, falls installiert (Prüfung per `list-unit-files` bei jedem Aufruf, wie bisher), sonst No-op; Labor: `PDH_HA_TUNNEL_CMD` oder No-op
  - `newPGActor(pg *pgCtl, svc, tun serviceCtl, ev *eventLog, env func() pgEnv) *pgActor`
  - `func shouldRunPDH(st store, pgState func() string) bool` – zusätzlich `true`, wenn Rolle passiv und `pgState() == "standby"`; `main.go` übergibt den Zustand von `pgCtl.status`
- Verhalten (Spec §7):
  - `promote`: am Ende `svc.Restart`, dann `tun.Start`.
  - `fence`: `tun.Stop`, dann `svc.Stop`.
  - `unfence`: `svc.Start`, `tun.Start`.
  - `reconcile` aktiv: wie bisher, `svc.Start` und `tun.Start` getrennt mit eigenem Alarm.
  - `reconcile` nicht aktiv: `tun` läuft → stoppen; dann Datenbank: `standby` → `svc.Start` (Ereignis „PDH als Spiegel gestartet“ nur beim Wechsel von aus nach an); `primary` schreibbar → `fence` wie bisher; `primary` abgeschottet / `stopped` / `error` → `svc.Stop`.
  - `rejoin`, `recopy`, `replica-init`: stoppen `svc` und `tun`; Start übernimmt der nächste `reconcile`.
  - `SetAutostart(false)` für beide bei der Einrichtung; `messages.go` (zurück in den Einzelbetrieb) `SetAutostart(true)` + `Start` für beide.
  - Wartung: Abgleich-Verhalten in der Wartung unverändert lassen.

- [ ] **Step 1: Tests anpassen/schreiben**
  - `TestSystemdServiceSingleUnit`: `Start/Stop/Active/Restart/SetAutostart` → genau `systemctl start|stop|is-active|restart|enable|disable pdh`.
  - `TestTunnelCtlAbsent`: ohne `cloudflared.service` keine `systemctl`-Aufrufe außer `list-unit-files`.
  - `TestReconcilePassiveStandbyStartsPDH`: passiv, PG `standby` → `svc.Start`; `tun` läuft → `tun.Stop`; kein `tun.Start`.
  - `TestReconcilePassiveFencedStopsPDH`: passiv, PG `primary` nur lesend → `svc.Stop`.
  - `TestPromoteRestartsPDH`: Aufrufe enden mit `svc restart`, `tun start`.
  - `replica_test.go`: `shouldRunPDH` passiv + `standby` → true; passiv + `stopped` → false; aktiv → true.
- [ ] **Step 2:** `go test ./cmd/pdh-ha` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./cmd/pdh-ha` → PASS; Race im Container auf LF-Kopie (wie bei 0.76.1): `go test -race ./cmd/pdh-ha ./cmd/update-agent` → ok; `bash deploy/ha-lab/ha-lab.sh alle` → alle `BESTANDEN`.
- [ ] **Step 5:** Commit `feat(pdh-ha): PDH am passiven Server im Spiegelmodus, Tunnel getrennt`.

### Task 7: Spiegel-Labor mit echtem Standby

**Files:**
- Create: `deploy/mirror-lab/compose.yaml`, `deploy/mirror-lab/mirror-lab.sh`, `deploy/mirror-lab/README.md`
- Create: `internal/web/mirror_crawl_test.go` (Build-Tag `mirrorlab`)

**Interfaces:**
- Consumes: Server-Binary aus `cmd/server`, `/health` mit `mode`, `PDH_HA_SOCKET`
- Aufbau: `pg1` (postgres:18, Primary, `wal_level=replica`), `pg2` (Standby per `pg_basebackup -R`), `pdh1` gegen `pg1` (legt Schema an), `pdh2` gegen `pg2` (gleiche `.env` inkl. `PDH_AUTH_JWTSECRET`, `PDH_SETTINGS_KEY`). Admin und Nicht-Admin über `pdh1` anlegen. Für `pdh2` liefert ein kleiner Fake-Socket den pdh-ha-Status (`peers[0].role=aktiv`, `seen` umschaltbar).
- Crawler-Test (Umgebung `PDH_MIRROR_URL`, `PDH_MIRROR_ADMIN`, `PDH_MIRROR_PASS`): anmelden, ab `/` alle internen `href` (gleicher Host, ohne `/logout`, ohne Antworten > 5 MB) per GET besuchen, höchstens 2000 Seiten; jede Antwort ≥ 500 → Fehler mit Pfad.

- [ ] **Step 1: Szenarien in `mirror-lab.sh`** (`up`, `start`, `crawl`, `posts`, `anmeldung`, `hochstufen`, `version`, `down`, `alle`); jedes gibt `BESTANDEN` bzw. `NICHT BESTANDEN: <Grund>` aus:
  - `start`: `pdh2` `/health` → `"mode":"spiegel"`; Protokoll von `pdh2` ohne `migrationen geprüft`.
  - `crawl`: `go test -tags mirrorlab -run TestMirrorCrawl ./internal/web` → ok; Protokoll `pdh2` ohne `schreibzugriff im spiegel`.
  - `posts`: 5 Beispiel-POSTs (Ticket anlegen, Kommentar, Profil, Easy-Mode-Meldung, Upload) → 409.
  - `anmeldung`: Nicht-Admin bei `seen=true` abgewiesen, bei `seen=false` zugelassen.
  - `hochstufen`: `pg_promote()` auf `pg2` → `pdh2` innerhalb 10 s `"mode":"normal"`, ein POST gelingt.
  - `version`: `pdh2` mit zusätzlicher Dummy-Migration starten → Band „wartet auf das Update“, `GET /tickets` → Redirect.
- [ ] **Step 2:** `bash deploy/mirror-lab/mirror-lab.sh alle` über den Hilfscontainer auf dem Windows-Docker (wie `pdh-ha-lab-runner`) → alle `BESTANDEN`. Gefundene Schreibzugriffe in GET-Handlern mit `if h.Mirror().On { … }` umgehen, je mit Test im betroffenen Paket, Labor erneut laufen lassen.
- [ ] **Step 3:** Commit `test(mirror): Labor mit echtem Standby und Crawler`.

### Task 8: Handbuch, Version, Abschluss

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `spiegelbetrieb`: neuer `.hb-howto` „So arbeitest du am Spiegel“ – Anmeldung, was geht, was nicht, Band; Regel „pdh läuft nur am aktiven Server“ ersetzen), `web/templates/widgets/help_figures.gohtml` (Skizze mit Band), `CHANGELOG.md` (`## [0.77.0] – <Datum>`: Neu – Lesemodus am Spiegel, Anmeldung Admins bzw. alle bei Ausfall, Band mit Datenstand; Geändert – PDH läuft auch auf dem passiven Server, Tunnel nur am aktiven), `VERSION` → `0.77.0`

- [ ] **Step 1:** Texte schreiben (kurze Sätze, Schritte).
- [ ] **Step 2:** `go test ./internal/web -run 'Help|Changelog'` → PASS; `go test ./...` → PASS.
- [ ] **Step 3:** Commit `docs(help): Arbeiten am Spiegel, 0.77.0`.
- [ ] **Step 4:** Abnahme auf den Testservern .14/.17 (Benutzer führt aus): Band, Regel C, Rollentausch hin und zurück, Ausfall des aktiven Servers mit Anmeldung eines Nicht-Admins am Spiegel, Update-Reihenfolge Spiegel zuerst.
