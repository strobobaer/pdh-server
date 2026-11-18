# IT-Netzabgleich Paket 1 (UniFi) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Netzgeräte aus UniFi automatisch als IT-Assets führen, Clients als Vorschläge anbieten, überwachte Assets beobachten und bei Ausfall ein Alarm-Ticket anlegen.

**Architecture:** `internal/integrations/unifi` bekommt Lese-Aufrufe für Geräte, Gerätedetails und Clients. Ein neues Paket `internal/modules/it/netsync` enthält die quellenunabhängige Form (`Source`, `Snapshot`), den Abgleich in die Datenbank und den Alarm-Wächter (Muster `energie.AlarmWatch`). `internal/web` verbindet: UniFi-Quelle aus den vorhandenen Einstellungen, Ticket-Senke (Muster `energy_alarms.go`), Seite `/it/net`, Erweiterungen an `/it` und `/it/{id}`.

**Tech Stack:** Go 1.27, pgx v5, chi, html/template + htmx, PostgreSQL-Migrationen in `migrations/`.

**Spec:** `docs/superpowers/specs/2026-11-16-it-netzabgleich-design.md`

## Global Constraints

- Version **0.80.17**, `VERSION` und oberster `CHANGELOG.md`-Eintrag `## [0.80.17] – 2026-11-16` gleich (`TestChangelogMatchesVersion`).
- Migration **164** (`migrations/164_it_netsync.up.sql`); vor dem Merge gegen `origin/main` prüfen.
- Alle Texte, Kommentare, Commits auf Deutsch, für Anwender verständlich.
- Neues Recht `it.netsync` (Kategorie `IT`), Rollen mit `it.edit` bekommen es mit ihrer Abteilung, `it.admin` hat es über Bereichsadmin.
- Nur lesen an UniFi. Gleiche Sicherheit wie `unifi.Client` heute (Pin, `houseDial`, Fristen, Größenbegrenzung) – keine neue HTTP-Logik daneben.
- Läuft nicht auf dem Spiegelserver (`h.Mirror().On`), ruht in der Wartungssperre (`h.MaintLocked`). Ohne UniFi-Einstellungen oder bei `it.net.unifi.enabled != "1"`: nichts tun, kein Fehler im Log je Minute.
- Quellfehler → Zustand `unknown`, **nie** Alarm.
- Abgleich überschreibt nur: `ip_address`, `mac_address`, `model`, `firmware`, `firmware_update`, `net_state*`, `net_last_seen`, `net_uplink`, `net_gone`, Ports. Name, Standort, Notizen, Zuständige, Status nie (Name nur beim Anlegen).
- Oberfläche: Farben über Variablen (`--bg`, `--text`, `--muted`, `--border`, `--accent`), beide Stile, hell/dunkel, 320–1366 px ohne seitliches Überlaufen.
- `go test ./...` in Git Bash; Datenbanktests brauchen `PDH_TEST_DSN` (Labor-DB `pdh_test`, Muster `testPool` in `internal/modules/energie/migration_integration_test.go`).

## Review Focus

1. **UDM nicht erreichbar / Schlüssel falsch:** alle UniFi-Assets `unknown`, kein Ticket, Fehler auf `/it/net` sichtbar; nach Behebung kein „Nachhol-Alarm“ für die Zeit davor (Offline-Dauer zählt erst ab einem echten `offline`) → Test in Task 4 `TestWatchUnknownNoAlarm` + Task 3 `TestMarkUnknown` (setzt `net_state_since`).
2. **Asset mit gleicher MAC schon von Hand angelegt** (Schreibweise `AA-BB-CC-…` oder Großbuchstaben): wird verknüpft, nicht doppelt angelegt; Name/Standort bleiben → Task 3 `TestApplyLinksByMAC`.
3. **Firmware-Update an einem Switch** (`UPDATING` mehrere Minuten): kein Alarm → Task 5 `TestUnifiStateMap` (UPDATING → unknown).
4. **Asset auf „Außer Dienst“/„Inaktiv“ gesetzt oder Überwachung abgeschaltet, während ein Alarm offen ist:** keine weiteren Einträge, Status wird nie zurückgesetzt → Task 3 `TestApplyKeepsManualFields`, Task 4 `TestWatchRetiredSilencesAlarm`.
5. **Client übernommen, der gerade nicht verbunden ist** (Handy weg): nach Karenz Ticket – deshalb beim Übernehmen Überwachung für Typ `phone`/`tablet` standardmäßig **aus**, für alle anderen an → Task 3 `TestAdoptMonitoredByType`.

---

### Task 1: Migration 164 und Recht

**Files:**
- Create: `migrations/164_it_netsync.up.sql`
- Test: `internal/modules/it/netsync/migration_integration_test.go`

**Interfaces:**
- Produces: Spalten und Tabellen laut Spec-Abschnitt „Datenmodell“, dazu genau:
  - `it_assets.net_state TEXT NOT NULL DEFAULT 'unknown' CHECK (net_state IN ('online','offline','unknown'))`, `net_monitored BOOLEAN NOT NULL DEFAULT false`, `net_gone BOOLEAN NOT NULL DEFAULT false`, `firmware_update BOOLEAN NOT NULL DEFAULT false`, `firmware TEXT NOT NULL DEFAULT ''`, `net_uplink TEXT NOT NULL DEFAULT ''`, `net_source TEXT`, `net_external_id TEXT`, `net_last_seen TIMESTAMPTZ`, `net_state_since TIMESTAMPTZ`
  - Unique-Index `it_assets_net_source_ext` auf `(net_source, net_external_id) WHERE net_source IS NOT NULL`
  - Index `it_assets_mac_norm` auf `lower(replace(mac_address,'-',':'))`
  - `it_asset_ports(asset_id UUID REFERENCES it_assets ON DELETE CASCADE, idx INT, connector TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'UNKNOWN', speed_mbps INT NOT NULL DEFAULT 0, max_speed_mbps INT NOT NULL DEFAULT 0, poe_state TEXT NOT NULL DEFAULT '', PRIMARY KEY(asset_id, idx))`
  - `it_net_candidates(source TEXT, mac TEXT, external_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL CHECK (kind IN ('wired','wireless')), uplink TEXT NOT NULL DEFAULT '', last_seen TIMESTAMPTZ NOT NULL, ignored BOOLEAN NOT NULL DEFAULT false, PRIMARY KEY(source, mac))`
  - `it_net_alarm_state(asset_id UUID PRIMARY KEY REFERENCES it_assets ON DELETE CASCADE, state TEXT NOT NULL DEFAULT 'normal' CHECK (state IN ('normal','alarm')), offline_since TIMESTAMPTZ, ticket_id UUID REFERENCES tickets ON DELETE SET NULL)`
  - Recht `it.netsync` / „Netz-Abgleich einrichten und Vorschläge übernehmen“ / `IT`, an Rollen mit `it.edit` (Abteilung übernehmen, „überall“ gewinnt – genau wie das Grant-Statement in `163_energy_thresholds.up.sql`).
  - `permCategoryOrder` enthält `IT` schon – prüfen, nichts ergänzen, wenn vorhanden.

- [ ] **Step 1: Test schreiben** `TestITNetsyncMigration`: nach `RunMigrations` existieren die Spalten (`information_schema.columns`), Insert zweier Assets mit gleichem `(net_source, net_external_id)` scheitert, Recht `it.netsync` existiert, jede Rolle mit `it.edit` hat auch `it.netsync`. Paket `netsync` mit eigener `testPool`-Hilfe (Kopie des Musters, Migrationspfad `../../../../migrations`).
- [ ] **Step 2:** `PDH_TEST_DSN=… go test ./internal/modules/it/netsync/ -run Migration` → FAIL (Spalten fehlen).
- [ ] **Step 3:** Migration schreiben (idempotent: `IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- [ ] **Step 4:** Test → PASS; `go test ./internal/web/ -run 'Perm|Migration'` grün.
- [ ] **Step 5:** Commit „Migration 164: Netz-Abgleich für IT-Assets, Recht it.netsync“.

### Task 2: UniFi – Geräte, Details, Clients lesen

**Files:**
- Modify: `internal/integrations/unifi/unifi.go` (Abschnitt „Form der Schnittstelle“ ergänzen, neue Methoden)
- Create: `internal/integrations/unifi/testdata/devices.json`, `device_detail.json`, `clients.json` (Form laut OpenAPI 10.3.58: Seitenhülle `offset/limit/count/totalCount/data`; Gerät: `id, name, model, macAddress, ipAddress, state, firmwareVersion, firmwareUpdatable, features[]`; Details zusätzlich `uplink.deviceId`, `interfaces.ports[] {idx, connector, state, speedMbps, maxSpeedMbps, poe{state}}`, `features` dort Objekt; Client: `id, name, type, macAddress, ipAddress, uplinkDeviceId, connectedAt`)
- Test: `internal/integrations/unifi/unifi_devices_test.go`

**Interfaces:**
- Produces:
```go
var ErrUnsupported = errors.New(...) // altes Gerät ohne Integration-API
type Device struct { ID, Name, Model, MAC, IP, Firmware, State string; FirmwareUpdatable bool; Features []string }
type Port struct { Idx int; Connector, State string; SpeedMbps, MaxSpeedMbps int; PoEState string } // PoEState "" ohne PoE
type DeviceDetail struct { Device; UplinkDeviceID string; Ports []Port }
type NetClient struct { ID, Name, MAC, IP, Type, UplinkDeviceID string } // Type WIRED/WIRELESS/VPN/TELEPORT
func (c *Client) Devices(ctx context.Context) ([]Device, error)
func (c *Client) DeviceDetail(ctx context.Context, id string) (DeviceDetail, error)
func (c *Client) Clients(ctx context.Context) ([]NetClient, error)
```
  Pfade `/proxy/network/integration/v1/sites/{siteId}/devices[/{id}]`, `/clients`, über `c.resolve` + `c.do`; Seitenschleife wie `Records` (`pageLimit`, `maxPages`). In den Details ist `features` ein Objekt (`switching`, `accessPoint`): Schlüssel als `Features` übernehmen. Auf alten Geräten (`c.legacy`) liefern alle drei `ErrUnsupported`.

- [ ] **Step 1: Tests** mit `fakeUDM` (um die drei Pfade erweitern): `TestDevicesPaged` (zwei Seiten, 3 Geräte, Felder korrekt, API-Schlüssel gesendet), `TestDeviceDetailPorts` (Ports idx/state/speed/PoE, Uplink-ID, Features aus Objekt), `TestClientsTypes` (WIRED/WIRELESS/VPN gelesen, MAC unverändert), `TestDevicesLegacyUnsupported`, `TestDevicesWrongPin` (bricht vor dem Senden des Schlüssels ab – Muster vorhandener Pin-Test).
- [ ] **Step 2:** `go test ./internal/integrations/unifi/` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS (auch alle alten UniFi-Tests).
- [ ] **Step 5:** Commit „UniFi: Geräte, Gerätedetails und Clients lesen“.

### Task 3: netsync – Form und Abgleich in die Datenbank

**Files:**
- Create: `internal/modules/it/netsync/model.go`, `sync.go`
- Test: `internal/modules/it/netsync/sync_integration_test.go`, `model_test.go`

**Interfaces:**
- Produces:
```go
type State string // StateOnline "online", StateOffline "offline", StateUnknown "unknown"
type Port struct { Idx int; Connector, State string; SpeedMbps, MaxSpeedMbps int; PoEState string }
type Device struct { ExternalID, MAC, Name, Model, Kind, IP, Firmware string; FirmwareUpdate bool; State State; UplinkExternalID string; Ports []Port; HasDetail bool }
type Client struct { ExternalID, MAC, Name, IP, Kind, UplinkExternalID string } // Kind "wired"/"wireless"
type Snapshot struct { Devices []Device; Clients []Client }
type Source interface { Kind() string; Fetch(ctx context.Context) (Snapshot, error) }
func NormMAC(s string) string // "AA-BB-CC-DD-EE-FF"/"aabb.ccdd.eeff"/"aabbccddeeff" → "aa:bb:cc:dd:ee:ff"; ungültig → ""
type Store struct{ DB *pgxpool.Pool; SystemUserID string; Manufacturer map[string]string } // Manufacturer["unifi"]="Ubiquiti"
type Result struct { Created, Updated, Gone, Candidates int }
func (s *Store) Apply(ctx context.Context, source string, snap Snapshot, now time.Time) (Result, error)
func (s *Store) MarkUnknown(ctx context.Context, source string, now time.Time) error
func (s *Store) Adopt(ctx context.Context, source, mac, assetType, userID string, now time.Time) (assetID string, err error)
func (s *Store) Ignore(ctx context.Context, source, mac string, ignored bool) error
```
  Regeln (Spec „Abläufe“ 1–6): Zuordnung erst `(net_source, net_external_id)`, dann normierte MAC (nur Assets ohne `net_source`); Anlegen Typ `network`, Status `active`, `net_monitored = true`, `manufacturer` aus `Manufacturer[source]`, `created_by = SystemUserID`. `net_state_since` nur ändern, wenn der Zustand wechselt; `net_last_seen = now` bei online; `net_gone = false` bei jeder Sichtung. Uplink-Text = Name des Assets mit `net_external_id = UplinkExternalID` derselben Quelle (sonst leer). Ports nur ersetzen, wenn `HasDetail`; Firmware/Update immer aus dem Gerät. Übernommene Clients = Assets mit `net_source = source`, die kein Gerät des Snapshots sind: per MAC in `Clients` gefunden → online + IP; sonst offline. Nicht übernommene Clients → `it_net_candidates` upsert (Name, IP, Kind, Uplink, last_seen; `ignored` bleibt); Kandidaten mit `last_seen < now - 30 Tage AND NOT ignored` löschen. Geräte-Assets der Quelle (Typ `network`, mit `net_external_id`), die fehlen: `net_gone = true`, `unknown`. Ein Apply = eine Transaktion. `Adopt` legt Asset aus Kandidat an (Name, IP, MAC aus Kandidat, `net_external_id` = Kandidat-`external_id` oder MAC, Typ gewählt, `net_monitored = assetType NOT IN ('phone','tablet')`, `created_by = userID`, Zustand `online`), löscht den Kandidaten; vorhandenes Asset gleicher MAC ohne Quelle wird verknüpft statt neu angelegt. Unbekannter Kandidat → Fehler `ErrNoCandidate`.

- [ ] **Step 1: Tests:** `TestNormMAC` (Tabelle inkl. ungültig); DB-Tests `TestApplyCreatesDevices`, `TestApplyKeepsManualFields` (Name/Standort/Status `retired` bleiben nach zweitem Apply mit anderem Namen), `TestApplyLinksByMAC` (Hand-Asset `AA-BB-…` wird verknüpft, kein zweites), `TestApplyStateSinceOnlyOnChange`, `TestApplyGone`, `TestApplyCandidates` (Client → Kandidat; ignorierter bleibt ignoriert; 31 Tage alter wird gelöscht), `TestAdoptMonitoredByType` (phone → false, printer → true), `TestAdoptedClientOffline` (fehlt im Snapshot → offline), `TestMarkUnknown` (alle Assets der Quelle `unknown`, `net_state_since = now`). Je Test eigener Quellname (`"test-"+eindeutig`) und Cleanup.
- [ ] **Step 2:** → FAIL. **Step 3:** umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit „IT-Netzabgleich: Momentaufnahme in IT-Assets und Vorschläge übernehmen“.

### Task 4: netsync – Alarm-Wächter

**Files:**
- Create: `internal/modules/it/netsync/alarms.go`
- Test: `internal/modules/it/netsync/alarms_integration_test.go`

**Interfaces:**
- Consumes: Tabellen aus Task 1, Zustände aus Task 3.
- Produces:
```go
type AlarmAsset struct { ID, Name, IP, Uplink, InfrastructureID string; LastSeen *time.Time; OfflineSince time.Time; TicketID string }
type AlarmSink interface {
    OpenTicket(ctx context.Context, tx pgx.Tx, a AlarmAsset) (ticketID string, after func(), err error)
    TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error)
    Repeat(ctx context.Context, tx pgx.Tx, a AlarmAsset) (after func(), err error)                  // erneut offline, Ticket noch offen
    Recovered(ctx context.Context, tx pgx.Tx, a AlarmAsset, at time.Time) (after func(), err error) // Eintrag „wieder erreichbar“
}
type Watch struct { DB *pgxpool.Pool; Sink AlarmSink; Grace func(ctx context.Context) time.Duration }
func (w *Watch) Pass(ctx context.Context, now time.Time) error
```
  Je Asset mit `net_source IS NOT NULL AND net_monitored AND status = 'active'`: `offline` und `now - net_state_since >= Grace` und Zustand `normal` → Alarm (Ticket des letzten Alarms noch offen → `Repeat`, sonst `OpenTicket`), Zustand `alarm`, `offline_since = net_state_since`. `online` und Zustand `alarm` → `Recovered`, Zustand `normal` (ticket_id bleibt gemerkt). `unknown` → nichts. Assets im Zustand `alarm`, die nicht mehr überwacht/aktiv sind oder keine Quelle mehr haben → still auf `normal`. Je Asset eigene Transaktion mit Panik-Schutz (Muster `safeOne` in `energie/alarms.go`); `after` erst nach Commit; Fehler eines Assets halten die anderen nicht auf.

- [ ] **Step 1: Tests** mit Fake-Senke (zählt Aufrufe, `TicketOpen` aus Feld, legt keine Tickets an): `TestWatchGrace` (offline 4 min → nichts, 5 min → OpenTicket genau einmal, nächster Pass → nichts), `TestWatchUnknownNoAlarm`, `TestWatchRecovered`, `TestWatchRepeatOnOpenTicket`, `TestWatchNewTicketWhenClosed`, `TestWatchRetiredSilencesAlarm`, `TestWatchMonitoringOff`, `TestWatchSinkPanicIsolated`.
- [ ] **Step 2:** → FAIL. **Step 3:** umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit „IT-Netzabgleich: Wächter legt bei Ausfall Alarm-Tickets an“.

### Task 5: Anbindung im Server – Quelle, Senke, Takt

**Files:**
- Create: `internal/web/it_netsync.go`, `internal/web/it_netsync_test.go`
- Modify: `internal/web/certs_lan.go` (gemeinsamen Teil von `unifiClient` herausziehen, s. u.), `cmd/server/main.go` (neben `StartEnergyAlarms`: `webHandler.StartITNetsync(context.Background())`)

**Interfaces:**
- Consumes: Task 2, 3, 4.
- Produces:
```go
func (h *Handler) unifiAPI(ctx context.Context) (*unifi.Client, error) // gemeinsame Einstellungs-/Entschlüsselungslogik; unifiClient nutzt sie
const (keyITNetUnifiOn = "it.net.unifi.enabled"; keyITNetInterval = "it.net.interval_s"; keyITNetGrace = "it.net.grace_min";
       keyITNetRespUser = "it.net.responsible_user"; keyITNetRespGroup = "it.net.responsible_group"; keyITNetPriority = "it.net.priority";
       keyITNetLastRun = "it.net.unifi.last_run"; keyITNetLastErr = "it.net.unifi.last_error"; keyITNetLastCount = "it.net.unifi.last_count")
type unifiSource struct{ api unifiAPIReader; detailEvery time.Duration; lastDetail map[string]time.Time } // netsync.Source
type unifiAPIReader interface { Devices(context.Context) ([]unifi.Device, error); DeviceDetail(context.Context, string) (unifi.DeviceDetail, error); Clients(context.Context) ([]unifi.NetClient, error) }
func unifiState(s string) netsync.State
func unifiKind(features []string) string
func (h *Handler) StartITNetsync(ctx context.Context)
func (h *Handler) itNetRunOnce(ctx context.Context, now time.Time) error // ein Abgleich + Wächter; für Takt, Tests und „Jetzt abgleichen“
type itNetAlarmSink struct{ h *Handler } // netsync.AlarmSink
func itNetAlarmText(a netsync.AlarmAsset) (title, desc string)
```
  `unifiSource.Fetch`: `Devices` + `Clients`; `DeviceDetail` nur für Geräte, deren letzter Detailabruf ≥ 15 min her ist (`HasDetail=true`); Fehler beim Detail → Gerät ohne Detail weiter. `unifiState`: `ONLINE` → online; `OFFLINE`, `CONNECTION_INTERRUPTED`, `ISOLATED` → offline; sonst unknown. `unifiKind`: enthält `switching` → `switch`, `accessPoint` → `ap`, sonst `other`. Clients mit Typ VPN/TELEPORT verwerfen. Takt: Intervall aus Einstellung (Standard 60 s, min 30, max 3600), aus bei Spiegel, ruht bei `MaintLocked`; `unifiSource` bleibt über Läufe erhalten (für `lastDetail`). Fehler beim Fetch → `MarkUnknown`, `last_error` setzen, Log nur beim Wechsel ok→Fehler; Erfolg → `last_error` leeren, `last_run`, `last_count` („<Geräte>/<Clients>“). Senke nach `energyAlarmSink`: Titel „Netzgerät offline: <Name>“, Beschreibung mit IP, Uplink, offline seit, zuletzt gesehen, `/it/<id>`, Hinweis „Bitte beim Erledigen im Feld Ursache beschreiben, warum das Gerät ausgefallen war.“; `RequiresRootCause: true`; Priorität aus Einstellung (Standard `high`); `InfrastructureID` des Assets, falls gesetzt; Anlege-Hinweis aus `record_change_queue` herausnehmen wie dort. Empfänger: Benutzer-Einstellung (aktiv), sonst Gruppe-Einstellung, sonst Gruppe mit Namen „IT“ (case-insensitiv), sonst Inhaber von `it.admin` (ohne Zuständigen). `Recovered`: Kommentar „Wieder erreichbar seit TT.MM.JJJJ hh:mm“ + Nachricht.

- [ ] **Step 1: Tests:** `TestUnifiStateMap` (inkl. UPDATING → unknown), `TestUnifiKindFromFeatures`, `TestUnifiSourceDetailEvery15Min` (Fake-`unifiAPIReader`: zweiter Fetch nach 5 min ohne Details, nach 16 min mit), `TestUnifiSourceSkipsVPN`, `TestITNetAlarmText`, DB-Tests `TestITNetRunOnceDisabled` (Einstellung aus → kein Abruf, kein Fehler), `TestITNetRecipientsFallback` (Benutzer → Gruppe → Gruppe „IT“ → it.admin).
- [ ] **Step 2:** → FAIL. **Step 3:** umsetzen + `main.go`. **Step 4:** → PASS, `go build ./...`, alle `certs_lan`-Tests grün.
- [ ] **Step 5:** Commit „IT-Netzabgleich: UniFi-Quelle, Takt und Alarm-Tickets im Server“.

### Task 6: Oberfläche – Seite „Netz-Abgleich“, Liste, Detail

**Files:**
- Create: `web/templates/it_net.gohtml`, `internal/web/it_net_web.go`, `internal/web/it_net_web_test.go`
- Modify: `internal/web/handler.go` (Routen; `ITAssetView` + `ITPage` um Netzfelder; `ITDetail` um Netzkarte), `web/templates/it.gohtml`, `web/templates/it_detail.gohtml`, `internal/web/perm_gate.go`, `internal/modules/it/it.go` (Felder in `Asset`, `List`/`GetByID` lesen sie mit)

**Interfaces:**
- Consumes: Task 3 (`Store.Adopt`, `Store.Ignore`), Task 5 (`itNetRunOnce`, Einstellungsschlüssel).
- Routen (alle Schreibrouten in `gateRules`):
  - `GET /it/net` → `ITNetPage` (Recht `it.view`; Einstellungen/Knöpfe nur mit `it.netsync`). `BaseData.Page = "it-net"`.
  - `POST /it/net/settings` (`it.netsync`): an/aus, Intervall, Karenz, Verantwortlich (Benutzer **oder** Gruppe), Priorität.
  - `POST /it/net/run` (`it.netsync`): `itNetRunOnce`, zeigt Ergebnis.
  - `POST /it/net/candidates/adopt` (`it.netsync`, Formular `mac`, `type`), `POST /it/net/candidates/ignore` (`mac`, `ignored=1|0`).
  - `POST /it/{id}/net-monitor` (`it.edit` per `hasPermRec`, `on=1|0`), `POST /it/{id}/net-detach` (`it.edit` per `hasPermRec`: `net_source`, `net_external_id` auf NULL, Ports und Alarmzustand löschen).
- Ohne UniFi-Verbindung: Hinweis „UniFi ist noch nicht verbunden“ + Link zum Reiter „UniFi“ der Server-Einstellungen nur bei `system.unifi`.
- `/it`: Abzeichen Zustand (`b-green` online, `b-red` offline, `b-gray` unbekannt) nur bei Assets mit Quelle; „Update“-Abzeichen bei `firmware_update`; Filter `?net=offline`; Kopf-Knopf „Netz-Abgleich“ mit Zähler offener (nicht ausgeblendeter) Vorschläge.
- `/it/{id}`: Karte „Netz“ (Zustand seit, zuletzt gesehen, Firmware, Update, Uplink, Ports-Tabelle mit waagerechtem Scrollen im eigenen Kasten auf schmalen Bildschirmen) + Schalter „überwachen“ und „Quelle trennen“ (mit Rückfrage).

- [ ] **Step 1: Tests:** `TestITNetPagePerms` (ohne `it.view` 403; mit `it.view` ohne `it.netsync`: keine Einstellungsformulare/Übernehmen-Knöpfe im HTML; POST-Routen ohne `it.netsync` 403), `TestITNetAdoptFlow` (DB: Kandidat → POST adopt → Asset existiert, Kandidat weg), `TestITNetMonitorToggleNeedsRecPerm` (Abteilungsrecht auf anderen Datensatz → 403), `TestITListNetBadge` (Render mit Asset `offline` → `b-red`). `TestHelpCoversAllPages` wird erst in Task 7 grün – in diesem Task darf er fehlschlagen, das im Bericht nennen.
- [ ] **Step 2:** → FAIL. **Step 3:** umsetzen (Vorlagen mit Variablen-Farben, keine festen Werte). **Step 4:** → PASS (außer Hilfe-Abdeckung).
- [ ] **Step 5:** Commit „IT-Netzabgleich: Seite Netz-Abgleich, Zustand in Liste und Detail“.

### Task 7: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `infrastructure`: `data-pages` um `it-net`; Abschnitt „Netz-Abgleich“ mit `.hb-howto`/`.hb-steps`: UniFi einschalten, Vorschlag übernehmen, Alarm-Ticket lösen; Kapitel `admin`: Recht `it.netsync`), `web/templates/widgets/help_figures.gohtml` (kleine Skizze Seite Netz-Abgleich), `VERSION` → `0.80.17`, `CHANGELOG.md` neuer oberster Eintrag mit `### Neu` (Netzgeräte aus UniFi, Vorschläge übernehmen, Ausfall-Tickets) und `### Sicherheit` (Recht `it.netsync`).

- [ ] **Step 1:** Ändern.
- [ ] **Step 2:** `go test ./internal/web/ -run 'Help|Changelog'` und `go test ./...` → PASS.
- [ ] **Step 3:** Commit „Handbuch und Änderungsprotokoll 0.80.17: Netz-Abgleich für IT-Assets“.

## Nach den Tasks (Hauptsitzung)

Schlussprüfung des ganzen Branches (Reviewer), Prüfung vor dem Merge laut `AGENTS.md`, dann Anwender um Freigabe für Merge/Push/Testverbund fragen. Abnahme gegen die echte UDM Pro erst nach Freigabe auf dem Testverbund.
