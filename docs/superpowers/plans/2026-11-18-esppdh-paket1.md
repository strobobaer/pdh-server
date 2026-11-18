# ESPPDH Paket 1 – Gerät bauen und flashen (0.82.0) – Umsetzungsplan

> **Für ausführende Agenten:** PFLICHT-FERTIGKEIT: superpowers:subagent-driven-development
> (empfohlen) oder superpowers:executing-plans. Aufgabe für Aufgabe abarbeiten; die
> Schritte sind Kästchen (`- [ ]`) zum Abhaken.

**Ziel:** Ein Anwender legt im PDH ein ESP-Gerät an, wählt Bausteine, lässt die
Firmware am Server bauen, flasht sie per USB und das Gerät sendet mit eigenem
Zugang an den eingebauten MQTT-Broker – sichtbar im Explorer.

**Aufbau:** Reine Logik (Platinen, Bausteine, YAML-Erzeuger, Bau-Schlange) liegt
im neuen Paket `internal/modules/esppdh` ohne Datenbank und ohne HTTP. Seiten,
Rechte und Speicherung liegen in `internal/web/esppdh*.go`. Der Broker bekommt
einen einzigen Anmelde-Hook, der Hauptzugang und Gerätezugänge mit Themen-Rechten
kennt. ESPHome läuft als fremdes Programm aus einer eigenen Python-Umgebung, die
der Update-Agent einrichtet.

**Technik:** Go, PostgreSQL (pgx), chi, HTML-Vorlagen mit htmx, mochi-mqtt
`v2.7.9`, ESPHome (Python-venv unter `/opt/pdh-esphome`), ESP Web Tools (im
Repository mitgeliefert, kein CDN).

**Entwurf:** `docs/superpowers/specs/2026-11-18-esppdh-design.md` – Plan und
Entwurf gehören zusammen, Ausführende lesen beide.

## Für alle Aufgaben gültig

- Version **0.82.0** (vom Anwender festgelegt); `VERSION` und `CHANGELOG.md`
  erst in Aufgabe 9 anheben.
- Branch `feature/esppdh`. Jede Aufgabe endet mit `go vet ./...` und
  `go test ./...` (Git Bash, `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'`,
  `PDH_TEST_SNMP=127.0.0.1:1161`) und einem Commit auf Deutsch ohne Attributionszeilen.
- Regeln aus `AGENTS.md` gelten: Handbuch im selben Schritt, Fiori und Fluent
  hell/dunkel nur über CSS-Variablen, alles Neue braucht ein Recht, Löschen nur
  Bereichsadministrator, neue Kennungen mit `pdh_uuid_v7()`, neue Migration in
  `migrations/checksums.txt` eintragen
  (`PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`).
- Nächste freie Migration: **179** (178 liegt auf `fix/efficio-fingerabdruck`).
  Vor dem Merge gegen `origin/main` prüfen und sonst umnummerieren.
- Das PDH muss ohne ESPHome und ohne Internet laufen. Fehlt die Umgebung, zeigt
  die Seite einen Hinweis; nichts anderes darf davon abhängen.
- Kein ESPPDH im Docker-Betrieb und nichts bauen am Spiegelserver (`h.Mirror().On`).
- Kennwörter (WLAN, MQTT, Notzugang) nie im Klartext in Datenbank, Protokoll,
  Bau-Verlauf, Sicherung oder in einer Datei unter `uploads/`.
- Themen eines Geräts: `esppdh/<kennung>/…`; Benutzername am Broker
  `esppdh-<kennung>`. `<kennung>` ist 3–32 Zeichen aus `a-z`, `0-9`, `-`.
- Paket 1 legt **keine** Zuordnungen und Messstellen an (Paket 2), kein OTA,
  keinen freien Editor (Paket 3).

## Abweichungen vom Entwurf (bitte bestätigen)

1. **Firmware liegt verschlüsselt.** Der Entwurf nennt `uploads/esppdh/`. Der
   Ordner `uploads/` wird aber ohne Anmeldung ausgeliefert (`cmd/server/main.go:478`)
   und steckt in jeder Sicherung; eine Firmware enthält WLAN- und MQTT-Kennwort.
   Deshalb: Ablage als `uploads/esppdh/<geräte-id>/<bau-id>.bin.enc`
   (AES-256-GCM mit `esppdhKey()`), Herausgabe nur über eine Route mit
   `esppdh.edit`. Gespiegelt wird sie damit weiterhin.
2. **Bau-Verlauf per Abfrage alle 2 s** (htmx) statt Dauerverbindung – der
   Server trennt Dauerverbindungen nach 120 s, und das Muster gibt es schon
   beim Verbund-Update.
3. **Broker ohne Anmeldung:** Auch wenn der Broker anonyme Clients erlaubt,
   müssen sich Clients mit Benutzernamen `esppdh-…` anmelden und bleiben auf
   ihr Thema beschränkt.
4. **Recht `esppdh.admin`:** Der Entwurf leitet es von `import.admin` ab; das
   Recht gibt es nicht. Stattdessen: Rollen mit `import.write` **und**
   `system.server_config` überall.

## Worauf die Prüfung besonders achtet

1. **Gerät sendet unter fremdem Thema** (`esppdh/anderes/…` oder ein Thema einer
   Steuerung) → Broker verwirft; Test in Aufgabe 1.
2. **Eingaben mit YAML-Zeichen** (Name `"x: y\n  lambda: …"`, SSID mit `#`, `'`,
   Zeilenumbruch) → landen als sauber maskierte Zeichenkette, nie als Struktur;
   Test in Aufgabe 3.
3. **Bau hängt oder Platte ist voll** → Abbruch nach Zeitlimit, Kindprozesse
   beendet, Arbeitsordner aufgeräumt, nächster Lauf startet; Test in Aufgabe 5.
4. **Kennwort im Bau-Verlauf** (ESPHome gibt bei Fehlern Konfigurationszeilen
   aus) → Verlauf wird vor dem Speichern geschwärzt; Test in Aufgabe 5.
5. **Zwei Geräte mit gleicher Kennung, Gerät gelöscht oder gesperrt, während es
   verbunden ist** → Kennung eindeutig; Verbindung wird sofort getrennt und
   Wiederanmeldung scheitert; Tests in Aufgabe 1 und 4.

---

### Aufgabe 1: Broker – Gerätezugänge mit Themen-Rechten

**Dateien:**
- Ändern: `internal/core/mqttbroker/manager.go` (Hooks `:178-189`, `credentialAuthHook` `:400-421`)
- Neu: `internal/core/mqttbroker/devices.go`, `internal/core/mqttbroker/devices_test.go`

**Schnittstellen (liefert):**
```go
type DeviceAccount struct{ Username, Password, TopicPrefix string } // TopicPrefix z. B. "esppdh/halle1-s0/"
func (m *Manager) SetDeviceAccounts(id string, accts []DeviceAccount) // ersetzt die Liste; trennt Clients, deren Zugang fehlt oder dessen Kennwort sich änderte
func (m *Manager) DeviceOnline(id, username string) bool
```

- [ ] **Schritt 1: Tests schreiben** (`devices_test.go`, echter Broker auf freiem
  Port wie in `manager_tls_test.go`, Client paho):
  - `TestDeviceLoginAndOwnTopic`: Gerät `esppdh-a`/`pw` verbindet, veröffentlicht
    `esppdh/a/status`; ein mit dem Hauptzugang verbundener Client empfängt es.
  - `TestDeviceForeignTopicDropped`: Gerät `esppdh-a` veröffentlicht
    `esppdh/b/status` und `werk/linie1/takt`; der Hauptclient empfängt nach
    500 ms **nichts**.
  - `TestDeviceCannotReadOthers`: Gerät `esppdh-a` abonniert `#`; eine Nachricht
    auf `werk/x` kommt nicht an, eine auf `esppdh/a/cmd/ota` kommt an.
  - `TestDeviceWrongPassword`: Anmeldung scheitert.
  - `TestMainAccountUnchanged`: Hauptzugang darf wie bisher alles.
  - `TestAnonymousBrokerStillChecksDevices`: `AllowAnonymous: true`; Client ohne
    Benutzer darf alles, `esppdh-a` mit falschem Kennwort wird abgelehnt und mit
    richtigem auf `esppdh/a/` beschränkt.
  - `TestRemoveAccountDisconnects`: verbundenes Gerät; `SetDeviceAccounts(id, nil)`
    → Verbindung endet binnen 2 s, `DeviceOnline` ist falsch, Wiederanmeldung scheitert.
- [ ] **Schritt 2:** `go test ./internal/core/mqttbroker -run Device` → scheitert (Typ fehlt).
- [ ] **Schritt 3: Umsetzen.** Ein einziger Hook `pdhAuthHook` ersetzt
  `credentialAuthHook` **und** `auth.AllowHook` (bei mochi genügt ein „ja“
  irgendeines Hooks – ein zweiter Hook könnte nichts einschränken). Regeln:
  Benutzername mit Präfix `esppdh-` → nur Gerätezugänge, Schreiben und Lesen nur
  unter `TopicPrefix`; sonst Hauptzugang bzw. anonym wie bisher, ohne
  Einschränkung. Gerätezugänge liegen je Broker unter Mutex; sie überleben
  `Restart` (im Manager je `id` halten, nicht im laufenden Server). Trennen über
  `server.Clients.GetAll()` + `cl.Properties.Username` + `server.DisconnectClient`.
  Kennwortvergleich mit `subtle.ConstantTimeCompare`.
- [ ] **Schritt 4:** Tests grün, dazu `go test ./internal/core/mqttbroker ./internal/web -run 'Mqtt|TIA|Broker'`.
- [ ] **Schritt 5:** Commit „MQTT-Broker: Gerätezugänge mit eigenem Thema“.

### Aufgabe 2: Compiler-Umgebung, Schalter und Zustand

**Dateien:**
- Neu: `scripts/install-esphome.sh`, `scripts/esphome.version`,
  `cmd/update-agent/esppdh.go`, `cmd/update-agent/esppdh_test.go`,
  `internal/modules/esppdh/env.go`, `internal/modules/esppdh/env_test.go`,
  `internal/web/esppdh_settings.go`, `internal/web/esppdh_settings_test.go`,
  `web/templates/widgets/esppdh_settings.gohtml`
- Ändern: `install_update.sh` (Block nach dem code-server-Block `:315-320`),
  `cmd/update-agent/main.go` (Routen `:104-145`), Vorlage der
  Server-Einstellungen (Karte einhängen), `internal/web/handler.go` (Routen)

**Schnittstellen (liefert):**
```go
// internal/modules/esppdh
const VenvDir = "/opt/pdh-esphome"          // überschreibbar mit PDH_ESPHOME_DIR
const WorkDir = "/var/lib/pdh-esphome"      // überschreibbar mit PDH_ESPHOME_WORKDIR; gehört Benutzer pdh
func PinnedVersion() string                  // Inhalt von scripts/esphome.version, eingebettet (go:embed)
type EnvState struct{ Installed bool; Version string; Outdated bool; Docker bool; FreeMB int64; Problem string }
func CheckEnv(ctx context.Context) EnvState  // ruft "<venv>/bin/esphome version", Zeitlimit 10 s
// internal/web
const esppdhSetEnabled = "esppdh.enabled"    // app_settings, "1" = an
func (h *Handler) esppdhEnabled(ctx context.Context) bool
func inDocker() bool                         // /.dockerenv oder /run/.containerenv
```
Agent: `POST /v1/esppdh/install` (Körper `{}`) führt `scripts/install-esphome.sh`
über `hostCommandEnv` aus und antwortet `{"ok":true,"version":"…"}`; im
Docker-Betrieb `{"ok":false,"error":"ESPPDH gibt es im Docker-Betrieb nicht"}`.

- [ ] **Schritt 1: ESPHome-Version festlegen.** Die aktuelle stabile Version bei
  PyPI nachsehen (`pip index versions esphome`), genau diese eine Zeile in
  `scripts/esphome.version` schreiben und im Commit nennen.
- [ ] **Schritt 2: Tests schreiben.** `env_test.go`: `CheckEnv` mit
  `PDH_ESPHOME_DIR` auf einen Temp-Ordner mit Attrappe `bin/esphome` (Shell-Skript,
  gibt `Version: 2099.1.0` aus) → `Installed`, `Version=="2099.1.0"`,
  `Outdated==true`; ohne Ordner → `Installed==false`, `Problem` leer.
  `esppdh_settings_test.go`: Schalter nur mit `canServerConfig`; am Spiegel 409;
  im Docker-Betrieb (Variable `esppdhDockerHook` im Test) Hinweis statt Knopf.
  `cmd/update-agent/esppdh_test.go`: Docker-Modus antwortet mit dem Fehlertext oben.
- [ ] **Schritt 3: `scripts/install-esphome.sh` schreiben.** Idempotent:
  `python3 -m venv /opt/pdh-esphome` (fehlt `python3-venv`, per apt nachholen),
  `pip install --upgrade "esphome==$(cat scripts/esphome.version)"`,
  `/var/lib/pdh-esphome` anlegen und `chown pdh:pdh`. Gibt am Ende die Version aus.
- [ ] **Schritt 4: `install_update.sh`** – Block „nur wenn eingerichtet“:
  `if [[ -x /opt/pdh-esphome/bin/esphome ]]; then bash scripts/install-esphome.sh || echo "WARNING: ESPHome konnte nicht aktualisiert werden – Update trotzdem fertig." >&2; fi`
- [ ] **Schritt 5: Agent-Auftrag und Karte „ESPPDH“ in den Server-Einstellungen**
  umsetzen: Schalter, Zustand (eingerichtet / Version / „veraltet – wird mit dem
  nächsten Update angehoben“ / freier Platz), Knopf „Umgebung einrichten“
  (`h.agentJSON(ctx, "POST", "/v1/esppdh/install", …)`), Hinweis „Der erste Bau je
  Platinentyp lädt mehrere hundert MB aus dem Internet“. Routen
  `POST /admin/server-config/esppdh` und `POST /admin/server-config/esppdh/install`.
- [ ] **Schritt 6:** Tests grün; Commit „ESPPDH: Compiler-Umgebung, Schalter und Zustand“.

### Aufgabe 3: Platinen, Bausteine und YAML-Erzeuger

**Dateien:**
- Neu: `internal/modules/esppdh/boards.go`, `blocks.go`, `yaml.go`,
  `yaml_test.go`, `blocks_test.go`, `testdata/*.yaml` (Erwartungsdateien)

**Schnittstellen (liefert):**
```go
type Board struct{ Key, Label, Platform, ESPHomeBoard string; Ethernet bool; ADCPins, InputPins []string }
func Boards() []Board            // esp32dev, esp32-s3, esp32-c3, d1-mini (esp8266), wt32-eth01, olimex-poe, other
func BoardByKey(key string) (Board, bool)

type Block struct{ Kind string; Params map[string]string }  // Kind: "s0", "sct013", "pzem", "counter"
type BlockDef struct{ Kind, Label, Help string; Fields []Field }
type Field struct{ Key, Label, Type, Default, Help string; Required bool; Options []string } // Type: pin|number|select|text
func BlockDefs() []BlockDef
func ValidateBlocks(b Board, blocks []Block) []string        // deutsche Meldungen; leer = in Ordnung

type Device struct {
    Slug, Name, BoardKey, CustomBoard string
    Wired bool; SSID, WLANPassword string; WLANHidden bool
    APPassword string
    BrokerHost string; BrokerPort int; BrokerTLS bool
    MQTTUser, MQTTPassword string
    Blocks []Block
}
func Generate(d Device) (config, secrets []byte, err error) // config enthält nur !secret-Verweise
func ConfigHash(d Device) string                            // SHA-256 über config + Geheimnisse
```
Bausteine und ihre Felder (Werte aus dem Entwurf):
- `s0` – Pin, Impulse je kWh (Vorgabe 1000) → Leistung (W) und Zählerstand (kWh).
- `sct013` – ADC-Pin, Zangentyp (`5A/1V`, `30A/1V`, `50A/1V`, `100A/50mA`),
  Bürdenwiderstand (nur bei `100A/50mA`), Netzspannung (Vorgabe 230) → Strom (A)
  und Scheinleistung (VA).
- `pzem` – RX-Pin, TX-Pin, Adresse (Vorgabe 1) → Spannung, Strom, Leistung, Energie.
- `counter` – Pin, Entprellzeit ms (Vorgabe 50), Flanke (`steigend`/`fallend`)
  → Stückzahl gesamt.

Themen: Zustand `esppdh/<slug>/status` (`online`/`offline`, Letzter Wille,
gespeichert), Werte `esppdh/<slug>/<baustein>-<n>/<wert>`.

- [ ] **Schritt 1: Tests schreiben.**
  - `TestGenerateGolden`: je eine Erwartungsdatei für (esp32dev + s0),
    (d1-mini + counter), (wt32-eth01 verkabelt + pzem), (esp32-s3 + sct013 + s0);
    Vergleich Byte für Byte, `-update`-Schalter schreibt sie neu.
  - `TestGenerateNoSecretsInConfig`: `config` enthält weder WLAN-, MQTT- noch
    AP-Kennwort; `secrets` enthält alle drei.
  - `TestGenerateEscapesInput`: Name `"x: y\n  lambda: |-\n    boom"`, SSID
    `Werk #1 'ost'` → `config` und `secrets` lassen sich mit einem YAML-Parser
    (`gopkg.in/yaml.v3`; falls nicht in `go.mod`, hinzufügen) lesen und die
    Werte kommen unverändert als Zeichenkette zurück; kein zusätzlicher Schlüssel.
  - `TestValidateBlocks`: SCT-013 an einem Pin ohne ADC → Meldung; derselbe Pin
    zweimal → Meldung; `other` → keine Pin-Prüfung; verkabelt auf Platine ohne
    Ethernet → Meldung; kein Baustein → Meldung.
  - `TestSlugRule`: `Halle 1` ungültig, `halle1-s0` gültig, 33 Zeichen ungültig.
  - `TestEsphomeConfigAccepts` (überspringt ohne `CheckEnv().Installed`):
    `esphome config` nimmt jede Erwartungsdatei an.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.** YAML über Strukturen und `yaml.v3` erzeugen, nicht
  per Textverkettung; `!secret` als Tag-Knoten. Feste Reihenfolge der Schlüssel,
  damit die Erwartungsdateien stabil sind. Nur MQTT, keine `api:`. Notzugang:
  `wifi.ap` mit `ssid: "esppdh-<slug>"` und `captive_portal:` (nicht bei
  verkabelten Geräten). `ota:` bleibt in Paket 1 aus.
- [ ] **Schritt 4:** Tests grün; Commit „ESPPDH: Platinen, Bausteine und Erzeuger der Konfiguration“.

### Aufgabe 4: Geräte – Tabellen, Rechte, Liste und Detailseite

**Dateien:**
- Neu: `migrations/179_esppdh.up.sql`, `internal/web/esppdh.go`,
  `internal/web/esppdh_store.go`, `internal/web/esppdh_test.go`,
  `web/templates/esppdh.gohtml`, `web/templates/esppdh_device.gohtml`
- Ändern: `internal/web/handler.go` (Routen), `perm_gate.go` (`areaAdminPerm`,
  `gateRules`), `nav.go`, `web/templates/import.gohtml` (Verweis „ESPPDH“ im
  Kopf), `web/i18n/{en,ro,tr,mk}.json`, `internal/web/mqtt_broker.go`
  (`reconcileMqttBroker` lädt Gerätezugänge)

**Tabellen (Migration 179):**
- `esppdh_devices(id UUID PK DEFAULT pdh_uuid_v7(), slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
  infrastructure_id UUID NOT NULL REFERENCES infrastructure, connection_id UUID NOT NULL REFERENCES import_export_connections ON DELETE RESTRICT,
  board_key TEXT NOT NULL, custom_board TEXT NOT NULL DEFAULT '', wired BOOLEAN NOT NULL DEFAULT false,
  wlan_id UUID REFERENCES net_wlans ON DELETE SET NULL, segment_id UUID REFERENCES net_segments ON DELETE SET NULL,
  mqtt_password_enc TEXT NOT NULL, ap_password_enc TEXT NOT NULL DEFAULT '', locked BOOLEAN NOT NULL DEFAULT false,
  built_hash TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', created_by UUID, created_at, updated_at)`
- `esppdh_blocks(id UUID PK DEFAULT pdh_uuid_v7(), device_id UUID NOT NULL REFERENCES esppdh_devices ON DELETE CASCADE,
  position INT NOT NULL, kind TEXT NOT NULL, params JSONB NOT NULL DEFAULT '{}')`
- `esppdh_builds(id UUID PK DEFAULT pdh_uuid_v7(), device_id UUID NOT NULL REFERENCES esppdh_devices ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('queued','running','ok','failed','canceled')), log TEXT NOT NULL DEFAULT '',
  config_hash TEXT NOT NULL, esphome_version TEXT NOT NULL DEFAULT '', firmware_sha256 TEXT NOT NULL DEFAULT '',
  firmware_size BIGINT NOT NULL DEFAULT 0, requested_by UUID, created_at, started_at TIMESTAMPTZ, finished_at TIMESTAMPTZ)`
- Rechte in Kategorie „Daten & Schnittstellen“: `esppdh.view`, `esppdh.edit` an
  alle Rollen und Einzelrechte mit `import.write` (mit deren `department_id`,
  Muster Migration 177); `esppdh.admin` an Rollen mit `import.write` **und**
  `system.server_config` überall.

**Schnittstellen (liefert):**
```go
const permESPView, permESPEdit, permESPAdmin = "esppdh.view", "esppdh.edit", "esppdh.admin"
const esppdhRef = "esppdh_device"   // Datensatzart für hasPermRec, Abteilung über die Anlage – Muster meterRef (Energie-Messstellen)
func (h *Handler) esppdhKey() []byte                                   // sha256("pdh-esppdh:"+Schlüssel), Muster networkKey
func (h *Handler) esppdhLoad(ctx context.Context, id string) (esppdhDevice, error)
func (h *Handler) esppdhBuildInput(ctx context.Context, id, reqHost string) (esppdh.Device, error) // löst WLAN (h.networkWLANSecret) und Broker (tiaBrokerName, mqttPort, tiaDefaults) auf
func (h *Handler) esppdhSyncBroker(ctx context.Context, connID string) // SetDeviceAccounts aus allen nicht gesperrten Geräten der Verbindung
```
Seiten und Routen (Seitenschlüssel `esppdh`, Titel „ESPPDH“, Bereich „Import“):
`GET /import/esppdh` (Liste), `POST /import/esppdh` (anlegen),
`GET /import/esppdh/{id}`, `POST /import/esppdh/{id}` (speichern samt Bausteinen),
`POST /import/esppdh/{id}/lock` (sperren/entsperren),
`POST /import/esppdh/{id}/password` (neues MQTT-Kennwort),
`POST /import/esppdh/{id}/delete` (nur `esppdh.admin`),
`GET /import/esppdh/{id}/config.yaml` (Konfiguration ohne Kennwörter).

- [ ] **Schritt 1: Tests schreiben** (`esppdh_test.go`, Aufbau wie
  `network_test.go` mit `itNetPageEnv`/`itNetDo`):
  - `TestESPPDHGate`: ohne Recht 403 auf jede Route; Löschen nur mit `esppdh.admin`
    (über `matchGate`).
  - `TestESPPDHFlow`: anlegen → Zeile da, `mqtt_password_enc` beginnt mit `v1:`,
    Kennwort steht in keiner Seite; Bausteine speichern → `ValidateBlocks`-Meldung
    erscheint bei falschem Pin und nichts wird gespeichert; gleiche Kennung
    zweimal → Meldung, keine zweite Zeile.
  - `TestESPPDHDepartment`: Benutzer mit `esppdh.edit` nur in Abteilung A darf ein
    Gerät an einer Anlage der Abteilung B weder sehen noch ändern.
  - `TestESPPDHBrokerSync` (echter Broker wie `tia_block_test.go:431`): nach dem
    Anlegen kann sich `esppdh-<slug>` anmelden; nach „sperren“ nicht mehr und
    eine bestehende Verbindung endet; nach dem Löschen ebenso; nach
    `reconcileMqttBroker` (Neustart des Brokers) geht die Anmeldung weiter.
  - `TestESPPDHConfigDownload`: Datei enthält `!secret`, kein Kennwort.
  - `TestESPPDHMigrationGrants`: Rolle mit `import.write` hat `esppdh.view` und
    `esppdh.edit`; `esppdh.admin` nur mit `system.server_config`.
  - `TestESPPDHDisabled`: Schalter aus → Seite zeigt Hinweis mit Verweis auf die
    Server-Einstellungen, Anlegen 409; Docker → eigener Hinweis.
  - `TestESPPDHOnlyIntegratedBroker`: Verbindung mit fremdem Broker lässt sich
    nicht wählen.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Migration, Speicherung und Handler umsetzen.** Anlage über
  das Widget `infra-tree-picker`; WLAN-Auswahl aus `net_wlans` oder „Kabel“ (nur
  bei Ethernet-Platine); Verbindung aus den eingebauten Brokern
  (`broker_mode == "integrated"`). MQTT- und Notzugang-Kennwort beim Anlegen
  zufällig (24 Byte, base64url). Das Notzugang-Kennwort zeigt die Detailseite
  nur mit `esppdh.edit`. Hinweise auf der Detailseite: Broker ohne TLS-Port →
  „Geräte senden unverschlüsselt“; gewähltes Netz ≠ Netz des Servers →
  „Freigabe für <Brokername> Port <n> in der Firewall des Netzes nötig“;
  Lizenzzeile „ESPPDH baut auf ESPHome auf (GPLv3/MIT)“.
- [ ] **Schritt 4:** `esppdhSyncBroker` nach jedem Anlegen, Sperren, Löschen,
  Kennwortwechsel und am Ende von `reconcileMqttBroker` aufrufen.
- [ ] **Schritt 5:** Navigation: Eintrag `{"esppdh", "/import/esppdh", "ti-cpu", "ESPPDH", …}`
  in Gruppe `data` mit `needs(permESPView)`; i18n-Schlüssel „ESPPDH“ in vier Katalogen.
- [ ] **Schritt 6:** Tests grün, Prüfsumme eintragen; Commit „ESPPDH: Geräte, Bausteine und Rechte (Migration 179)“.

### Aufgabe 5: Bauen mit Schlange und Verlauf

**Dateien:**
- Neu: `internal/modules/esppdh/build.go`, `build_test.go`,
  `internal/web/esppdh_build.go`, `internal/web/esppdh_build_test.go`,
  `web/templates/widgets/esppdh_build.gohtml`
- Ändern: `cmd/server/main.go` (Start im Block `if !mirrorOn`, Stopp beim
  Herunterfahren), `internal/web/handler.go`

**Schnittstellen (liefert):**
```go
// internal/modules/esppdh
type BuildJob struct{ ID, Slug string; Config, Secrets []byte; SecretValues []string }
type BuildResult struct{ OK bool; Log string; Factory, OTA []byte; Version string }
type Runner struct{ Timeout time.Duration; MinFreeMB int64; Command func(ctx context.Context, dir string, args ...string) *exec.Cmd }
func NewRunner() *Runner                                   // Timeout 30 min, MinFreeMB 2048, Command = nice -n 19 <venv>/bin/esphome
func (r *Runner) Build(ctx context.Context, job BuildJob, progress func(log string)) BuildResult
func Redact(log string, secrets []string) string           // ersetzt jedes Geheimnis durch "•••"; Grenze 256 KiB (Anfang und Ende bleiben)
// internal/web
func (h *Handler) StartESPPDH(ctx context.Context)          // ein Arbeiter; nimmt 'queued' in Reihenfolge; setzt beim Start hängen gebliebene 'running' auf 'failed'
func (h *Handler) esppdhFirmware(buildID string) ([]byte, error) // liest und entschlüsselt uploads/esppdh/<gerät>/<bau>.bin.enc
```
Routen: `POST /import/esppdh/{id}/build` (einreihen), `POST /import/esppdh/{id}/build/{bid}/cancel`,
`GET /import/esppdh/{id}/build/{bid}` (Verlauf als htmx-Teil, solange
`queued`/`running` mit `hx-trigger="every 2s"`).

- [ ] **Schritt 1: Tests schreiben.**
  - `TestRunnerOK`: `Command` ist eine Attrappe, die zwei Dateien
    `…/firmware.factory.bin` und `…/firmware.ota.bin` schreibt → `OK`, beide
    Inhalte da; Arbeitsordner und Geheimnis-Datei danach **gelöscht**.
  - `TestRunnerTimeoutKillsChildren`: Attrappe startet einen Kindprozess und
    schläft; `Timeout` 300 ms → `OK==false`, Verlauf nennt „Zeitlimit“, kein
    Prozess bleibt (Prozessgruppe), Ordner gelöscht.
  - `TestRunnerDiskFull`: `MinFreeMB` höher als frei → sofort Fehler „Zu wenig
    Platz …“, `Command` wird nicht aufgerufen.
  - `TestRedact`: Verlauf mit WLAN-Kennwort → nicht mehr enthalten; 1 MiB Verlauf
    → höchstens 256 KiB mit Hinweiszeile „… gekürzt …“.
  - `TestBuildQueueOneAtATime` (web, Attrappen-Runner mit Sperre): zwei Aufträge
    → der zweite bleibt `queued`, bis der erste fertig ist.
  - `TestBuildStoresEncryptedFirmware`: Datei unter `uploads/esppdh/` endet auf
    `.bin.enc`, enthält die Klartext-Bytes der Attrappe nicht;
    `esppdhFirmware` liefert sie; `built_hash` des Geräts ist gesetzt.
  - `TestBuildRefusedOnMirrorAndWhenDisabled`: 409 am Spiegel, 409 bei Schalter aus
    oder fehlender Umgebung.
  - `TestBuildStaleRunningRecovered`: Zeile `running` vor `StartESPPDH` → danach
    `failed` mit Verlauf „PDH wurde während des Baus neu gestartet“.
  - `TestBuildCancel`: laufender Auftrag → `canceled`, Prozess beendet.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.** Arbeitsordner `<WorkDir>/build/<bau-id>` mit
  Rechten 0700; PlatformIO-Zwischenspeicher dauerhaft unter `<WorkDir>/platformio`
  (`PLATFORMIO_CORE_DIR`), damit nur der erste Bau lädt. Aufruf
  `esphome compile <slug>.yaml`. Verlauf alle 2 s geschwärzt in `esppdh_builds.log`
  schreiben. Je Gerät die letzten 3 Firmware-Dateien behalten, ältere löschen.
- [ ] **Schritt 4:** Detailseite: Knopf „Firmware bauen“, Hinweis „Konfiguration
  seit dem letzten Bau geändert“ (`ConfigHash != built_hash`), Verlauf-Teil,
  Liste der letzten Läufe.
- [ ] **Schritt 5:** Tests grün; Commit „ESPPDH: Firmware am Server bauen, mit Schlange und Verlauf“.

### Aufgabe 6: Firmware herunterladen und per USB flashen

**Dateien:**
- Neu: `web/static/vendor/esp-web-tools/` (Version festhalten in
  `web/static/vendor/esp-web-tools/VERSION`, Lizenzdatei dazu),
  `internal/web/esppdh_flash.go`, `internal/web/esppdh_flash_test.go`
- Ändern: `web/templates/esppdh_device.gohtml`, `internal/web/handler.go`

Routen (alle `esppdh.edit` über `hasPermRec`):
`GET /import/esppdh/{id}/firmware/{bid}/factory.bin`, `…/ota.bin`
(`Content-Disposition: attachment`, `Cache-Control: no-store`),
`GET /import/esppdh/{id}/firmware/{bid}/manifest.json` (Format von ESP Web
Tools: `name`, `version`, `builds[{chipFamily, parts[{path, offset: 0}]}]`).

- [ ] **Schritt 1: Tests schreiben.** `TestFirmwareDownloadNeedsEdit` (mit
  `esppdh.view` allein 403); `TestFirmwareNotUnderUploads` (die Datei unter
  `uploads/esppdh/` ist verschlüsselt, Klartext kommt nur über die Route);
  `TestManifest` (chipFamily `ESP32`, `ESP32-S3`, `ESP32-C3`, `ESP8266` je nach
  Platine; Pfad zeigt auf `factory.bin` desselben Laufs);
  `TestFirmwareOfOtherDevice404` (Lauf-Kennung eines fremden Geräts).
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.** Knopf „Per USB flashen“ nur, wenn
  `window.isSecureContext && 'serial' in navigator`; sonst Text „USB-Flash
  braucht Chrome oder Edge und eine https-Adresse des PDH – hier die Firmware
  herunterladen“. Skript lokal aus `/static/vendor/esp-web-tools/` laden, nie von
  außen. Bei `other`-Platine nur Herunterladen.
- [ ] **Schritt 4:** Tests grün; Commit „ESPPDH: Firmware herunterladen und USB-Flash im Browser“.

### Aufgabe 7: Zustand des Geräts anzeigen

**Dateien:**
- Ändern: `internal/web/esppdh.go`, `web/templates/esppdh.gohtml`,
  `web/templates/esppdh_device.gohtml`, `internal/web/esppdh_test.go`

- [ ] **Schritt 1: Test** `TestESPPDHOnlineBadge`: Gerät verbunden →
  Liste und Detailseite zeigen „verbunden“ (`h.mqttBrokers.DeviceOnline`); nie
  verbunden und Firmware gebaut → Hinweis „Hat sich noch nicht gemeldet –
  Freigabe für <Brokername> Port <n> prüfen“; gesperrt → „gesperrt“.
- [ ] **Schritt 2:** Umsetzen; Detailseite verweist auf den Explorer der
  Verbindung mit vorbelegtem Thema `esppdh/<slug>/#`.
- [ ] **Schritt 3:** Tests grün; Commit „ESPPDH: Zustand je Gerät“.

(Störmeldung nach Karenz ist Paket 2.)

### Aufgabe 8: Handbuch

**Dateien:**
- Ändern: `web/templates/help.gohtml` (Kapitel `id="esppdh" data-pages="esppdh"`
  nach `tiabaustein`, mit `hb-shots`), `internal/web/help.go` (`helpChapterIDs`),
  `web/templates/widgets/help_figures.gohtml` (Anschluss-Skizzen S0, SCT-013,
  PZEM-004T, Stückzähler mit Lichtschranke), Kapitel „Administration“ (Rechte),
  Kapitel „Netzwerk“ (Satz: ESPPDH-Geräte wählen hier ihr WLAN)

- [ ] **Schritt 1:** Kapitel schreiben, „So geht's“-Schritte: Umgebung
  einrichten · Gerät anlegen · Bausteine wählen und anschließen · Firmware bauen
  · flashen · im Explorer prüfen · Gerät sperren. Dazu: was im eigenen VLAN
  freizugeben ist, Notzugang-WLAN, „misst für die Betriebsführung, nicht für die
  Abrechnung“, Lizenzhinweis ESPHome.
- [ ] **Schritt 2:** `go test ./internal/web -run 'TestHelp|TestI18n'` grün.
- [ ] **Schritt 3:** Commit „Handbuch: Kapitel ESPPDH“.

### Aufgabe 9: Version, Änderungsprotokoll, Schlussprüfung

- [ ] **Schritt 1:** `VERSION` → `0.82.0`; `CHANGELOG.md`-Eintrag (`### Neu`:
  ESPPDH-Seite, Bausteine, Bauen, USB-Flash; `### Sicherheit`: Rechte
  `esppdh.view/edit/admin`, Gerätezugänge mit eigenem Thema, Firmware
  verschlüsselt abgelegt). `AGENTS.md`-Liste nachziehen.
- [ ] **Schritt 2:** `go vet ./...`, `go test ./...`,
  `go test -tags integration ./pkg/database -run TestUUIDv7Policy`.
- [ ] **Schritt 3:** Prüfung vor dem Merge laut `AGENTS.md` (Migrationsnummer,
  Version, Dopplungen); Schlussprüfung des ganzen Branches durch einen frischen Prüfer.
- [ ] **Schritt 4:** Nach Freigabe des Anwenders: Push, Merge, Verbund-Update;
  am Testverbund Umgebung einrichten, ein Gerät bauen, umschalten, Firmware am
  zweiten Server herunterladen; Browserprüfung in Fiori/Fluent, hell/dunkel,
  320–1366 px.

## Was nur der Anwender prüfen kann

- USB-Flash an einer echten Platine (braucht https-Adresse und Chrome/Edge).
- Ein echter Sensor je Baustein; Werte im Explorer.
- Gerät in einem eigenen VLAN mit Firewall-Freigabe.
