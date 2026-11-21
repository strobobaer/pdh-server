# ESPPDH Teil 1 (0.82.11) – Webseite, Adresse, Werte an Anlage und Terminal, Handshake – Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jedes ESPPDH-Gerät bringt Webserver v3 mit Kennwort, meldet Adresse und Laufzeit-Zähler; das PDH zeigt die letzten Werte und den Handshake im Reiter „ESPPDH“ der Anlage, am Anlagenterminal (nach Kartenanmeldung) und auf der Anlagen-Schnellübersicht.

**Architecture:** Der eingebaute Broker (`internal/core/mqttbroker`) hält je Broker einen Speicher der letzten Nachrichten unter `esppdh/` (gefüllt im vorhandenen `activityHook.OnPublished`) und kennt die Absenderadresse verbundener Geräte. Der Erzeuger (`internal/modules/esppdh`) liefert neben YAML eine Liste der Themen mit Name, Einheit und Gruppe. `internal/web` baut daraus eine gemeinsame Ansicht je Gerät, die Reiter, Terminal, Geräteseite und Schnellübersicht verwenden; der Handshake ist ein gemeinsamer Baustein auch für Heartbeat-Zuordnungen.

**Tech Stack:** Go, chi, html/template (`.gohtml`), htmx, pgx/PostgreSQL, mochi-mqtt v2.7.9, ESPHome-YAML.

**Spec:** `docs/superpowers/specs/2026-11-21-esppdh-sensoren-webserver-design.md` (Teil 1)

## Global Constraints

- Version **0.82.11** (`VERSION`, oberster Eintrag `## [0.82.11] – 2026-11-21` in `CHANGELOG.md`), nächste Migration **186**, in `migrations/checksums.txt` eintragen mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- Tests in Git Bash: `go vet ./...`, `go test ./...` mit `PDH_TEST_DSN` (Labor-DB, `deploy/lab/mssql/LIESMICH.md`).
- Nur lesende ESPHome-Arten; **keine Durchreiche** der Geräte-Webseite durch das PDH.
- Webserver: `web_server: { port: 80, version: 3, auth: { username: pdh, password: !secret web_password } }`, **kein** `ota` mit `platform: web_server`; `local: true` auf allen ESP32-Plattformen, auf `esp8266` ohne `local` (Hinweis „Webseite des Geräts braucht Internet am Browser“).
- Themen: Adresse `esppdh/<kennung>/info/adresse` (retain), Laufzeit `esppdh/<kennung>/info/laufzeit` (Sensor `uptime`, `update_interval: 10s`, **ohne** retain).
- Handshake „läuft“, solange der letzte neue Wert jünger als **3 × Intervall** ist (Gerät: 3 × 10 s = 30 s; Zuordnung: `heartbeatSilentSQL`), sonst „steht seit …“.
- Neues Recht `esppdh.values` – Label „ESPPDH: Sensorwerte ansehen (Anlage und Terminal)“, Kategorie „Daten & Schnittstellen“, an Rollen `admin`, `manager`, `asset_manager`, `technician`.
- Werte älter als 3 × Aktualisierungsintervall grau; Reiter aktualisiert alle 10 s (htmx), Terminal-Sensoren alle 10 s, Handshake der Schnellübersicht alle 10 s.
- Texte Deutsch, für Anwender im Betrieb; beide Stile (Fiori/Fluent), hell/dunkel, 320–1366 px; Farben über `--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`.
- Handbuch und `CHANGELOG.md` im selben Schritt; Kennwörter nie im Klartext in DB, Sicherung, Protokoll, YAML-Download.

## Review Focus

- Broker nicht aktiv / PDH als Spiegel → Reiter und Terminal zeigen „Werte gerade nicht verfügbar“, keine alten Zahlen (Task 4).
- Gerät meldet eine Adresse, die nicht seine Verbindungsadresse ist, oder `127.0.0.1`/eine Serveradresse → kein Link, „Adresse nicht bestätigt“ (Task 4).
- Laufzeitwert, der nicht weiterkommt (Gerät still, Wert noch im Speicher) → Handshake „steht“ (Task 4).
- Terminal ohne Kartenanmeldung oder Person ohne `esppdh.values` in der Abteilung der Anlage → keine Sensoren, keine Werte im JSON (Task 6).
- Bestehende Geräte ohne `web_password_enc` → Kennwort entsteht beim nächsten Speichern/Bauen, Bau scheitert nicht (Task 3).

---

### Task 1: Broker merkt sich ESPPDH-Werte und kennt die Absenderadresse

Abweichung vom Entwurf (§2 „abonniert per SubscribeTopic“): `reconcileMqttConsumer` ruft bei jeder Zuordnungsänderung `UnsubscribeAllTopics` und würde ein eigenes Abo löschen. Deshalb Speicher im Broker.

**Files:**
- Create: `internal/core/mqttbroker/topiccache.go`
- Modify: `internal/core/mqttbroker/manager.go` (`activityHook.OnPublished` ~488; Broker-Laufzeitstruktur, die `inlineTopics` hält)
- Test: `internal/core/mqttbroker/topiccache_test.go`

**Interfaces:**
- Produces:
  - `const CachePrefix = "esppdh/"`
  - `type TopicValue struct { Topic, Payload string; At time.Time }`
  - `func (m *Manager) TopicValues(id, prefix string) ([]TopicValue, bool)` – `false`, wenn kein Broker unter `id` läuft.
  - `func (m *Manager) ClientRemoteIP(id, username string) (string, bool)` – IP ohne Port des verbundenen, nicht geschlossenen Clients mit diesem Benutzernamen.

- [ ] **Step 1: Tests schreiben** – `TestTopicCacheKeepsLastValue` (zwei Publishes auf `esppdh/a/info/laufzeit` → ein Eintrag mit letztem Payload und neuerer `At`), `TestTopicCacheIgnoresOtherPrefixes` (`sps/x` nicht im Speicher), `TestTopicCacheBounded` (mehr als 10 000 Themen → höchstens 10 000, älteste fallen weg; Payload über 256 Byte wird auf 256 gekürzt), `TestTopicValuesBrokerStopped` (`ok == false`), `TestClientRemoteIP` (Test-Broker wie in den vorhandenen Broker-Tests, Client mit Benutzer `esppdh-t1` verbindet über 127.0.0.1 → `"127.0.0.1", true`; unbekannter Benutzer → `false`).
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/core/mqttbroker -run 'TopicCache|TopicValues|ClientRemoteIP' -v` → „undefined: TopicValue“.
- [ ] **Step 3: Implementieren** – Speicher je Broker (Map + Reihenfolge für das Verdrängen), befüllt in `OnPublished` nur für Themen mit `CachePrefix`, mit Mutex; beim Stoppen des Brokers verworfen. `ClientRemoteIP` durchsucht die Clients des mochi-Servers nach `Properties.Username`, `net.SplitHostPort(cl.Net.Remote)`.
- [ ] **Step 4: Laufen lassen, erwartet PASS** – derselbe Befehl.
- [ ] **Step 5: Commit** – `git commit -m "Broker: letzte ESPPDH-Werte und Absenderadresse der Geräte merken (0.82.11)"`

### Task 2: Erzeuger – Webserver, Adresse, Laufzeit, Themenliste

**Files:**
- Modify: `internal/modules/esppdh/yaml.go` (`Device` 31–51, Geheimnisse 54–58 und 265–272, Wurzel 233–263, `blockSensors` 358)
- Create: `internal/modules/esppdh/topics.go`
- Modify: `internal/modules/esppdh/samples.go`, `internal/modules/esppdh/testdata/*.yaml` (mit `-update` neu schreiben)
- Test: `internal/modules/esppdh/yaml_test.go`, `internal/modules/esppdh/topics_test.go`

**Interfaces:**
- Produces:
  - `Device` neu: `WebServer bool`, `WebPassword string`
  - `const secretWeb = "web_password"`
  - `type TopicInfo struct { Topic, Name, Unit, Group string; Interval time.Duration; Handshake bool }`
  - `func Topics(d Device) ([]TopicInfo, error)` – alle Zustands-Themen des Geräts in der Reihenfolge der Bausteine, zuletzt Laufzeit (`Handshake: true`, Gruppe „Gerät“) und Adresse (Gruppe „Gerät“).
  - `const GroupEnergy = "Strom & Energie"`, `GroupAnalogDigital = "Analog & Digital"`, `GroupDevice = "Gerät"`; S0/SCT-013/PZEM → Energie, Stückzähler → Analog & Digital.

- [ ] **Step 1: Tests schreiben**
  - `TestGenerateWebServer`: `WebServer: true, WebPassword: "x"` auf `esp32dev` → YAML enthält `web_server:` mit `version: 3`, `local: true`, `auth.password: !secret web_password`; Geheimnis-Datei enthält `web_password: "x"`; kein `ota:`.
  - `TestGenerateWebServerESP8266NoLocal`: Platine `d1_mini` → `web_server` ohne `local`.
  - `TestGenerateWebServerOff`: `WebServer: false` → kein `web_server`, kein `web_password`.
  - `TestGenerateInfoTopics`: `text_sensor` mit `platform: wifi_info` (bzw. `ethernet_info` bei `Wired`) und `ip_address.state_topic: esppdh/<slug>/info/adresse`; `sensor` enthält `platform: uptime`, `update_interval: 10s`, `state_topic: esppdh/<slug>/info/laufzeit`, `retain: false`.
  - `TestTopicsMatchGeneratedYAML`: für jedes `Samples()`-Gerät sind die `state_topic`-Werte des erzeugten YAML genau die `Topic`-Werte von `Topics(d)` (Menge gleich).
  - `TestGenerateEscapesInput` (164): erwartete Wurzelschlüssel um `web_server`, `text_sensor` ergänzen.
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/modules/esppdh -v` → neue Tests rot.
- [ ] **Step 3: Implementieren** – Wurzelreihenfolge: `esphome`, Plattform, `logger`, `wifi`+`captive_portal`/`ethernet`, `web_server`, `mqtt`, `uart`/`modbus`, `sensor`, `text_sensor`. `ValidateDevice`: `WebServer` ohne `WebPassword` → Meldung „Kennwort der Webseite fehlt“. Namen und Einheiten in `Topics` aus denselben Angaben wie `blockSensors` (gemeinsame Beschreibung je Wert, damit beides nicht auseinanderläuft). `Samples()` setzen `WebServer: true` mit festem Test-Kennwort.
- [ ] **Step 4: Golden-Dateien neu schreiben und prüfen** – `go test ./internal/modules/esppdh -run TestGenerateGolden -update`, dann `go test ./internal/modules/esppdh -v` → PASS; Diff der `testdata/*.yaml` durchsehen (nur `web_server`, `uptime`, `text_sensor`, `web_password` neu).
- [ ] **Step 5: Wenn ESPHome im Labor vorhanden:** `esphome config` gegen eine erzeugte Datei je Familie (ESP32, ESP8266) → gültig; sonst im Task-Bericht vermerken.
- [ ] **Step 6: Commit** – `"ESPPDH: Webserver v3, Adresse und Laufzeit in der Firmware (0.82.11)"`

### Task 3: Migration, Gerätespeicher, Formular, Kennwort anzeigen

**Files:**
- Create: `migrations/186_esppdh_web.up.sql`
- Modify: `migrations/checksums.txt`, `internal/web/esppdh_store.go` (Struktur 68–87, `esppdhSelect` 89–97, `esppdhScan`, `esppdhInsert` 192, `esppdhUpdate` 217), `internal/web/esppdh.go` (`esppdhForm` ~174, Auflösung zum `esppdh.Device`, `esppdhRenderDevice` 705, neue Route), `internal/web/handler.go` (Routen 1155–1169), `internal/web/perm_gate.go` (184), `web/templates/esppdh_device.gohtml`
- Test: `internal/web/esppdh_web_test.go` (neu), `pkg/database` (Frozen)

**Interfaces:**
- Consumes: `esppdh.Device.WebServer/WebPassword` (Task 2)
- Produces:
  - Spalten `esppdh_devices.web_enabled boolean NOT NULL DEFAULT true`, `web_password_enc text NOT NULL DEFAULT ''`
  - `esppdhDevice.WebEnabled bool`, unexportiert `webEnc string`
  - `const permESPValues = "esppdh.values"`
  - `func (h *Handler) esppdhWebPassword(ctx context.Context, d *esppdhDevice) (string, error)` – entschlüsselt; ist `webEnc` leer, erzeugt mit `esppdhNewPassword`, speichert verschlüsselt und gibt es zurück.
  - Route `POST /import/esppdh/{id}/web-password` → `EsppdhWebPasswordWeb` (Fragment mit Kennwort, `Cache-Control: no-store`).

- [ ] **Step 1: Migration schreiben** – Spalten wie oben; Recht nach dem Muster von 183:
  ```sql
  INSERT INTO permissions (key, label, category) VALUES
      ('esppdh.values', 'ESPPDH: Sensorwerte ansehen (Anlage und Terminal)', 'Daten & Schnittstellen')
  ON CONFLICT (key) DO NOTHING;
  INSERT INTO role_permissions (role_id, permission_id)
  SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
   WHERE p.key = 'esppdh.values' AND r.key IN ('admin', 'manager', 'asset_manager', 'technician')
  ON CONFLICT DO NOTHING;
  ```
  Prüfsumme eintragen: `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 2: Tests schreiben (PDH_TEST_DSN)** – `TestEsppdhValuesPermission` (Recht da, genau bei den vier Rollen); `TestEsppdhWebPasswordCreatedOnce` (Gerät ohne `web_password_enc` → `esppdhWebPassword` liefert Kennwort, zweiter Aufruf dasselbe, Spalte enthält es nicht im Klartext); `TestEsppdhWebPasswordRoute` (mit `esppdh.edit` 200 und Kennwort im Fragment; nur `esppdh.view` → 403; Abteilung fremd → 403); `TestEsppdhFormWebEnabled` (Haken aus speichern → `web_enabled = false`, erzeugtes YAML ohne `web_server`); `TestEsppdhConfigDownloadNoWebPassword` (`config.yaml` enthält `!secret web_password`, nicht das Kennwort).
- [ ] **Step 3: Laufen lassen, erwartet FAIL** – `go test ./internal/web -run 'EsppdhValuesPermission|EsppdhWebPassword|EsppdhFormWebEnabled|EsppdhConfigDownloadNoWebPassword' -v`.
- [ ] **Step 4: Implementieren** – Spalten in Select/Scan/Insert/Update; beim Anlegen Kennwort erzeugen; an jeder Stelle, die ein `esppdh.Device` zum Bauen oder Herunterladen zusammensetzt, `WebServer = d.WebEnabled` und `WebPassword` über `esppdhWebPassword`. Formular: Schalter „Webseite des Geräts“ (Vorgabe an), Hilfe „Webseite auf dem Gerät (Port 80) mit eigenem Kennwort; nur im Netz der Geräte erreichbar.“ Geräteseite: Knopf „Kennwort anzeigen“ (nur `CanEdit` und `WebEnabled`), `hx-post` auf die neue Route; Handler protokolliert `componentLog("esppdh").Info().Str("benutzer", …).Str("gerät", slug).Msg("webseiten-kennwort angezeigt")`. Gate: in `perm_gate.go:184` die Alternativen um `/web-password` ergänzen. Hinweis an der Geräteseite bei `esp8266` und eingeschalteter Webseite: „Webseite des Geräts braucht Internet am Browser.“ Den vorhandenen Hinweis „Konfiguration geändert – neu bauen“ (built_hash ≠ ConfigHash) um „Webseite, Adresse und Handshake kommen mit dem nächsten Bau und Flashen.“ ergänzen.
- [ ] **Step 5: Laufen lassen, erwartet PASS** – derselbe Befehl, dazu `go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 6: Commit** – `"ESPPDH: Webseite je Gerät mit Kennwort, Recht esppdh.values (Migration 186, 0.82.11)"`

### Task 4: Gemeinsame Werte-Ansicht, Adressprüfung, Handshake-Baustein

**Files:**
- Create: `internal/web/esppdh_values.go`, `internal/web/handshake.go`, `web/templates/widgets/handshake.gohtml`, `web/templates/widgets/esppdh_values.gohtml`
- Modify: `internal/web/esppdh.go` (Geräteseite zeigt Adresse, Direktlink, Handshake, Werte), `internal/web/handler.go` (Route), `web/templates/esppdh_device.gohtml`
- Test: `internal/web/esppdh_values_test.go`, `internal/web/handshake_test.go`

**Interfaces:**
- Consumes: `Manager.TopicValues`, `Manager.ClientRemoteIP` (Task 1); `esppdh.Topics`, `TopicInfo` (Task 2); `esppdhDevice` (Task 3)
- Produces:
  - `type handshakeView struct { ID, Label, Count string; Running bool; Since string }` (`ID` stabil für die Anzeige des Weiterzählens)
  - `func handshakeFromValue(id, label, count string, at time.Time, interval time.Duration, now time.Time) handshakeView`
  - `func (h *Handler) heartbeatHandshakes(ctx context.Context, infraIDs []string) ([]handshakeView, error)` – Heartbeat-Zuordnungen (`import_mappings.heartbeat_seconds IS NOT NULL`) der Anlagen; `Count = last_value` (beim TIA-Baustein schon der Zähler, `source_ref …/heartbeat#count`), `Running = NOT (heartbeatSilentSQL) AND last_received_at IS NOT NULL`.
  - `func esppdhAddressOK(reported, remoteIP, subnet string, own []net.IP) (ok bool, note string)`
  - `type esppdhValueRow struct { Name, Value, Unit, Age string; Stale bool }`
  - `type esppdhGroupView struct { Name string; Rows []esppdhValueRow }`
  - `type esppdhValuesView struct { ID, Name, Slug, Path string; Online, Available bool; Live esppdhLive; Handshake handshakeView; Address, DirectURL, AddressNote string; Groups []esppdhGroupView }`
  - `func (h *Handler) esppdhValues(ctx context.Context, devs []esppdhDevice, now time.Time) []esppdhValuesView`
  - Teilvorlagen `{{define "handshake"}}` (eine `handshakeView`) und `{{define "esppdh-values"}}` (Gerät + Schalter, ob der Link zur Geräteseite erscheint – nur bei `esppdh.view`).
  - Route `GET /import/esppdh/{id}/values` → `EsppdhValuesWeb` (Fragment, Recht wie Geräteseite).

- [ ] **Step 1: Tests schreiben**
  - `TestEsppdhAddressOK` (Tabelle): gleich Absender + im Netz `10.20.0.0/24` → ok; gleich Absender, Netz leer → ok; ungleich Absender → `"Adresse nicht bestätigt"`; `127.0.0.1`, `169.254.1.1`, `224.0.0.1`, `0.0.0.0` → nicht ok; eigene Serveradresse → nicht ok; außerhalb des Netzes → nicht ok; keine IPv4 → nicht ok.
  - `TestHandshakeFromValue`: Wert vor 5 s bei Intervall 10 s → `Running`; vor 31 s → nicht `Running`, `Since` gefüllt; leerer Wert → nicht `Running`, `Count` „–“.
  - `TestEsppdhValuesBrokerDown`: Broker nicht aktiv → `Available == false`, keine Zeilen.
  - `TestEsppdhValuesGroupsAndStale` (Werte-Quelle über eine kleine Schnittstelle `topicSource` mit `TopicValues`, im Test ersetzt): S0-Gerät → Gruppen „Strom & Energie“ und „Gerät“, Werte mit Einheit; Wert älter als 3 × Intervall → `Stale`.
  - `TestEsppdhValuesStoppedUptime`: Laufzeit-Eintrag vor 40 s → Handshake steht, obwohl ein Wert vorhanden ist.
  - `TestHeartbeatHandshakes` (PDH_TEST_DSN): Heartbeat-Zuordnung mit `last_value '17'`, `last_received_at now()`, Intervall 60 → `Running`, `Count "17"`; `last_received_at` vor 200 s → steht.
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/web -run 'EsppdhAddressOK|HandshakeFromValue|EsppdhValues|HeartbeatHandshakes' -v`.
- [ ] **Step 3: Implementieren** – `esppdhValues` holt je Broker einmal `TopicValues(connID, "esppdh/")`; Zeitangaben mit `terminalAge`; eigene Adressen über `net.InterfaceAddrs()`. `DirectURL = "http://" + Adresse + "/"` nur bei ok und `WebEnabled`. Handshake-Vorlage: Zähler, grün „läuft“ / rot „steht seit …“; kleines Skript merkt sich den letzten Zähler je `ID` (`window.pdhHS`) und setzt nach dem Einfügen (`htmx:afterSwap` bzw. Terminal-Aktualisierung) eine CSS-Klasse für einen kurzen Puls, wenn er sich geändert hat. Geräteseite: Bereich „Werte“ mit `esppdh-values`, `hx-trigger="every 10s"` auf `/import/esppdh/{id}/values`.
- [ ] **Step 4: Laufen lassen, erwartet PASS** – derselbe Befehl.
- [ ] **Step 5: Commit** – `"ESPPDH: Werte, Adresse und Handshake an der Geräteseite (0.82.11)"`

### Task 5: Reiter „ESPPDH“ an der Anlage

**Files:**
- Modify: `internal/web/infra_web.go` (`InfraDetailData` 264–305, `InfraDetail` 307), `web/templates/infra_detail.gohtml` (Reiter 78–91, Fenster 94–455), `internal/web/handler.go` (Route), `internal/web/esppdh_store.go`
- Test: `internal/web/infra_esppdh_test.go`

**Interfaces:**
- Consumes: `esppdhValues`, Teilvorlage `esppdh-values` (Task 4)
- Produces:
  - `func (h *Handler) esppdhByInfra(ctx context.Context, infraIDs []string) ([]esppdhDevice, error)`
  - `InfraDetailData.ESPPDHTab bool`
  - Route `GET /infrastructure/{id}/esppdh` → `InfraESPPDHWeb` (Fragment `infra-esppdh`)

- [ ] **Step 1: Tests schreiben (PDH_TEST_DSN)** – `TestInfraESPPDHTabVisible` (Anlage mit Gerät + Recht `esppdh.values` in ihrer Abteilung → Seite enthält `data-tab="esppdh"`; ohne Gerät oder ohne Recht → nicht); `TestInfraESPPDHFragment` (nur Geräte genau dieser Anlage, Gerät einer Unteranlage fehlt; Link zur Geräteseite nur mit `esppdh.view`; ohne Recht 403).
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/web -run 'InfraESPPDH' -v`.
- [ ] **Step 3: Implementieren** – Reiter-Link `<a class="rec-tab" data-tab="esppdh" href="?tab=esppdh">ESPPDH</a>` und Fenster `data-pane="esppdh"` in `{{if .ESPPDHTab}}`; im Fenster `hx-get="/infrastructure/{{.Node.ID}}/esppdh" hx-trigger="load, every 10s"`. Recht: `hasPermRec(r, permESPValues, "infrastructure", id) || hasPermRec(r, permESPView, "infrastructure", id)`.
- [ ] **Step 4: Laufen lassen, erwartet PASS**.
- [ ] **Step 5: Commit** – `"Anlage: Reiter ESPPDH mit Werten und Handshake (0.82.11)"`

### Task 6: Sensoren am Anlagenterminal

**Files:**
- Modify: `internal/web/terminal_page.go` (`terminalRoutes` 78), `web/templates/terminal.gohtml` (Info ab 101), `web/static/terminal.js` (Aktualisierung ~92)
- Test: `internal/web/terminal_sensors_test.go`

**Interfaces:**
- Consumes: `esppdhByInfra`, `esppdhValues`, `heartbeatHandshakes` (Tasks 4–5); `cardSessionPerson`, `cardPersonCan`, `terminalSubtree`, `terminalUserID`, `terminalGate`
- Produces: Route `GET /terminal/sensors.json` → `TerminalSensorsJSON`; Antwort `{"show": bool, "available": bool, "devices": [esppdhValuesView ohne Links], "handshakes": [handshakeView]}`.

- [ ] **Step 1: Tests schreiben (PDH_TEST_DSN)** – `TestTerminalSensorsNoCard` (`show:false`, keine Geräte); `TestTerminalSensorsCardWithoutPerm` (Kartenperson ohne `esppdh.values` → `show:false`); `TestTerminalSensorsCardWithPerm` (Geräte der Anlage **und** der Unteranlage enthalten, Heartbeat-Zuordnung der Unteranlage in `handshakes`); `TestTerminalSensorsDeptScope` (Recht nur in fremder Abteilung → `show:false`).
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/web -run 'TerminalSensors' -v`.
- [ ] **Step 3: Implementieren** – Zugang wie `TerminalInfoJSON` (`terminalGate`), Person über `cardSessionPerson(ctx, terminalUserID(r))`, Recht `cardPersonCan(ctx, person, permESPValues, "infrastructure", rootID)`. `terminal.gohtml`: Abschnitt „Sensoren“ (`data-sensors`, versteckt bis `show`), große Schrift, Gruppen und Handshake; `terminal.js`: eigener Abruf alle 10 s, beim Ende der Kartensitzung Abschnitt leeren und verstecken; Puls bei geändertem Zähler wie in Task 4.
- [ ] **Step 4: Laufen lassen, erwartet PASS**.
- [ ] **Step 5: Commit** – `"Anlagenterminal: Sensoren und Handshake nach Kartenanmeldung (0.82.11)"`

### Task 7: Handshake auf der Anlagen-Schnellübersicht

**Files:**
- Modify: `internal/web/plant_watch.go` (`watchStats` 38 + `State()` 42, `watchCard` 63, `watchBody` 252), `web/templates/widgets/plant_watch.gohtml` (`pw-cards`), `web/templates/plant_watch.gohtml` (135), `internal/web/handler.go` (Route)
- Test: `internal/web/plant_watch_handshake_test.go`

**Interfaces:**
- Consumes: `esppdhByInfra`, `esppdhValues`, `heartbeatHandshakes` (Tasks 4–5)
- Produces: `watchStats.HandshakeDown int`; `watchCard.Handshakes []handshakeView`; Route `GET /plant-watch/{id}/handshakes` → `PlantWatchHandshakesWeb` (Fragment mit `hx-swap-oob` je Karte `#pw-hs-<anlage>`).

- [ ] **Step 1: Tests schreiben (PDH_TEST_DSN)** – `TestWatchHandshakeRows` (Karte einer Anlage mit Heartbeat-Zuordnung der Unteranlage und ESPPDH-Gerät → zwei Zeilen); `TestWatchStateAmberOnStoppedHandshake` (stehender Handshake, sonst grün → `State() == "amber"`; kritische Störung bleibt `red`); `TestWatchHandshakeCountNeedsESPPDHRight` (ESPPDH-Gerät ohne `esppdh.values`/`esppdh.view` → Zeile ohne Zähler, nur läuft/steht).
- [ ] **Step 2: Laufen lassen, erwartet FAIL** – `go test ./internal/web -run 'WatchHandshake|WatchStateAmber' -v`.
- [ ] **Step 3: Implementieren** – Quellen je Karte: Anlage samt Unteranlagen (wie die übrige Zählung der Karte). In `pw-cards` je Karte Block `id="pw-hs-{{.ID}}"` mit `handshake`-Zeilen; auf der Seite ein verstecktes Element `hx-get="/plant-watch/{{.ID}}/handshakes" hx-trigger="every 10s"`, das nur die Handshake-Blöcke tauscht. Recht der Route wie `PlantWatchCardsWeb`.
- [ ] **Step 4: Laufen lassen, erwartet PASS**.
- [ ] **Step 5: Commit** – `"Anlagen-Schnellübersicht: Handshake je Quelle, gelb wenn er steht (0.82.11)"`

### Task 8: Handbuch, Änderungsprotokoll, Version, Gesamtprüfung

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `esppdh` 2527, `infrastructure` 645, `terminal` 890, `watch` 943, `tiabaustein` 2522), `web/templates/widgets/help_figures.gohtml` (Skizze Gerät/Reiter, falls vorhanden), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (Punkt 16)

- [ ] **Step 1: Handbuch** – „So geht's“-Schritte: Webseite des Geräts (Schalter, Kennwort anzeigen, Direktlink „nur im Netz der Geräte“, ESP8266-Hinweis, neu bauen und flashen); Reiter „ESPPDH“ an der Anlage; Terminal „Sensoren nach Kartenanmeldung“ mit Recht; Schnellübersicht „Handshake: läuft/steht, Karte gelb“; TIA-Baustein: der Zähler `count` ist im PDH sichtbar. Administration: neues Recht `esppdh.values`.
- [ ] **Step 2: CHANGELOG** – `## [0.82.11] – 2026-11-21` mit `### Neu` (Webseite je Gerät, Adresse und Direktlink, Reiter, Terminal, Handshake), `### Sicherheit` (Kennwort je Gerät verschlüsselt, Anzeigen protokolliert, Firmware nicht über die Webseite, neues Recht); `VERSION` = `0.82.11`.
- [ ] **Step 3: Gesamtprüfung** – `go build ./... && go vet ./...`; `go test ./...` mit Labor-DB → alle Pakete grün (bekannter Wackler `TestSetupAndCleanupLab`: einzeln wiederholen); `TestHelpChaptersMatch`, `TestChangelogMatchesVersion` grün.
- [ ] **Step 4: AGENTS.md** – Punkt 16 auf „gebaut auf `feature/esppdh-sensoren`, nicht gepusht“ setzen, Stand-Datum.
- [ ] **Step 5: Commit** – `"ESPPDH Teil 1: Handbuch, Änderungsprotokoll, Version 0.82.11"`

Nach dem Bau (nur mit Freigabe des Anwenders): Merge nach `main` nach der Prüfung aus `AGENTS.md`, Verbund-Update, Browserprüfung (Reiter, Terminal mit/ohne Karte, Schnellübersicht; Fiori/Fluent, hell/dunkel, 320–1366 px), danach 192.0.2.202 nach „192.0.2.202 aktualisieren“.
