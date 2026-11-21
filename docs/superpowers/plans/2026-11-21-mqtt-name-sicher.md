# MQTT-Name sicher einrichten (0.82.13) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein Broker-Name stört nie einen anderen Namen am Server und wird erst scharf, wenn alle zugehörigen Einträge stehen; Port-Änderungen ziehen Tunnel und nginx nach.

**Architecture:** Zwei Schutzschichten. Der Update-Agent hört nur dann auf IPv6, wenn alle anderen 443-Seiten es auch tun, und vergleicht vor/nach dem Neuladen die Zertifikate aller anderen Namen (Rückfall auf IPv4-Fassung, sonst Rücknahme). Das PDH merkt neue/geänderte Namen als „vorgemerkt“ vor (kein nginx, keine Tunnel-Zeile), richtet Zertifikat, Eintrag im Haus und Firewall ein und schaltet den Namen erst scharf, wenn alles grün ist; Tunnel-Ziele heißen `127.0.0.1` statt `localhost`.

**Tech Stack:** Go 1.27, chi, html/template + htmx, pgx; Update-Agent `cmd/update-agent` (nginx, ufw); Cloudflare- und UniFi-Clients in `internal/integrations`.

**Spec:** `docs/superpowers/specs/2026-11-21-mqtt-name-sicher-design.md`

## Global Constraints

- Version 0.82.13 (nächste Korrekturversion über 0.82.12), Eintrag in `CHANGELOG.md` unter `### Behoben` und `### Geändert`.
- Kein neues Recht, keine Migration (Zustand im Verbindungs-`config`-JSON).
- Von Hand angelegte nginx-Seiten werden nie geändert.
- „Scharf erst, wenn alles grün ist“: vorher keine nginx-Seite und keine Tunnel-Zeile für den Namen.
- Bestehende Namen (ohne Vormerkung) verhalten sich wie bisher – kein Ausfall durch das Update.
- Texte deutsch, für Anwender im Betrieb; Oberfläche in Fiori/Fluent, hell/dunkel, 320–1366 px.
- Tests in Git Bash: `eval "$(grep -E "^\s+export PDH_TEST_(DSN|MSSQL_DSN)=" deploy/lab/mssql/LIESMICH.md)"; export PDH_TEST_SNMP=127.0.0.1:1161; go test ./...`

## Review Focus

1. Viele Namen, Platzhalter (`_`, `*.firma.de`, `~regex`) oder nicht erreichbare Namen: Die Prüfung im Agenten überspringt sie bzw. wertet „vorher keine Antwort“ nicht als Störung und bleibt unter 10 s (Handler-Frist 45 s). → Tests in Task 2.
2. Bearbeiten ohne Namensänderung darf die Vormerkung nicht verlieren (das Formular trägt den Schlüssel nicht mit). → Test in Task 4.
3. Ein fremder DNS-Eintrag für den Namen bei Cloudflare wird nie selbsttätig ersetzt; die Zeile bleibt rot mit dem bestehenden „Ersetzen“-Knopf. → Test in Task 5.
4. Port-Änderung ohne Cloudflare-Zugang oder ohne Recht `system.cloudflare`: kein Fehler, Zeile zeigt den genauen Eintrag. → Test in Task 5.
5. Älterer Update-Agent ohne Prüfung: Die Vormerkung im PDH schützt trotzdem (kein Seiten-Auftrag, solange vorgemerkt). → Test in Task 4 (Ziel leer für vorgemerkte Namen).

---

### Task 1: Agent – IPv6 nur, wenn alle anderen 443-Seiten auch auf IPv6 hören

**Files:**
- Modify: `cmd/update-agent/certs.go` (Fälle `pdh` :737, `mqtt` :760; `certStatusHandler` :1228–1241; `certSiteTexts` :1190)
- Test: `cmd/update-agent/certs_v6rule_test.go` (neu)

**Interfaces:**
- Produces: `func (a *updateAgent) certWantV6(sitesDir, skipEntry string) bool` – `certHasIPv6()` und jede andere Datei im Seiten-Ordner, die auf Port 443 hört (`(?m)^\s*listen\s+(?:[0-9.]+:)?443\b`), hört auch auf `[…]:443` (`(?m)^\s*listen\s+\[[^\]]*\]:443\b`); nicht lesbare Dateien zählen als „nicht IPv6“.
- Produces: `type certSiteFile struct{ entry, text string }` und `func (a *updateAgent) certSiteFiles() []certSiteFile` (`certSiteTexts` bleibt als Hülle).

- [ ] **Step 1: Tests schreiben** (Hilfen `newCertFixture`, `certSetIPv6(t,true)`, `mqttInstall` aus `certs_mqtt_test.go`):
  - `TestCertV6OffWhenOtherSiteIPv4Only`: `conf.d/hand.conf` = `server { listen 443 ssl; server_name pdh.firma.de; }` → MQTT-Seite enthält `listen 443 ssl;`, **nicht** `listen [::]:443 ssl;`, enthält `# ipv6: aus (nginx)` nicht (keine Rückfall-Markierung nötig, Regel greift vorher).
  - `TestCertV6OnWhenAllOthersIPv6`: `hand.conf` mit `listen 443 ssl;` und `listen [::]:443 ssl;` → MQTT-Seite enthält `listen [::]:443 ssl;`.
  - `TestCertV6IgnoresPort80Sites`: andere Datei nur `listen 80;` → IPv6-Zeilen da.
  - `TestCertStatusNotStaleWhenOthersIPv4Only`: MQTT-Seite ohne IPv6-Zeilen + `hand.conf` nur IPv4 → Status `site=true`, `mqtt_upstream` gesetzt (kein täglicher Neuschrieb).
- [ ] **Step 2:** `go test ./cmd/update-agent -run 'TestCertV6|TestCertStatusNotStale'` → FAIL (IPv6-Zeilen vorhanden bzw. Seite gilt als veraltet).
- [ ] **Step 3:** `certWantV6` einbauen: in den Fällen `pdh` (`skipEntry = certPDHSite`) und `mqtt` (`skipEntry = entry`) statt `certHasIPv6()`; `siteV4` nur bilden, wenn `v6` wahr ist (wie bisher). Im Status je Datei `certSiteStale(f.text, a.certWantV6(sitesDir, f.entry))`.
- [ ] **Step 4:** `go test ./cmd/update-agent` → PASS (alle bisherigen Tests mit `certSetIPv6` bleiben grün).
- [ ] **Step 5:** Commit `Update-Agent: IPv6 nur, wenn alle anderen Seiten auf Port 443 auch IPv6 hören (0.82.13)`.

### Task 2: Agent – Zertifikate der anderen Namen vor/nach dem Neuladen vergleichen

**Files:**
- Create: `cmd/update-agent/certs_guard.go`
- Modify: `cmd/update-agent/certs.go` (`certInstallHandler` :705–917)
- Test: `cmd/update-agent/certs_guard_test.go`

**Interfaces:**
- Consumes: `certSiteFiles` (Task 1).
- Produces:
  - `var certProbe = func(ctx context.Context, addr, name string) (string, error)` – TLS zu `addr` (`127.0.0.1:443`, `[::1]:443`) mit SNI `name`, `InsecureSkipVerify`, Frist 2 s; Rückgabe hex-SHA-256 des ersten Zertifikats.
  - `func (a *updateAgent) certOtherNames(skipEntry string) []string` – `server_name`-Werte aller Dateien mit 443-Zeile außer `skipEntry`; ohne `_`, ohne Werte mit `*` oder `~`, klein geschrieben, doppelte entfernt, höchstens 32.
  - `func certProbeAll(ctx context.Context, names []string, v6 bool) map[string]string` – Schlüssel `addr+"|"+name`; alle Proben gleichzeitig; Fehler → `""`.
  - `func certDisturbed(before, after map[string]string) (key string, bad bool)` – erster Schlüssel mit `before != ""` und `after != before` (sortiert, damit die Meldung stabil ist).

- [ ] **Step 1: Tests schreiben** (`certProbe` im Test ersetzen; die Ersatzfunktion liest die MQTT-Seite der Fixture: enthält sie `listen [::]:443` und ist `addr` `[::1]:443`, liefert sie für `pdh.firma.de` `"B"`, sonst `"A"`):
  - `TestCertGuardFallsBackToIPv4`: `hand.conf` mit IPv4+IPv6 für `pdh.firma.de` (Regel aus Task 1 lässt IPv6 zu), `certSetIPv6(true)` → Antwort `ok:true`, Seite ohne `listen [::]:443 ssl;`, mit `# ipv6: aus (nginx)`, `note` enthält `pdh.firma.de`.
  - `TestCertGuardRollsBack`: Ersatz liefert nach dem Schreiben für jede Fassung `"B"` → `ok:false`, Fehler enthält `würde pdh.firma.de stören`, MQTT-Seite existiert nicht mehr, nginx zweimal neu geladen (`certCountCmds(f, "reload")`).
  - `TestCertGuardIgnoresUnreachable`: vorher `""` (Fehler), nachher `"B"` → `ok:true`.
  - `TestCertOtherNames`: Dateien mit `server_name pdh.firma.de www.firma.de;`, `server_name _;`, `server_name *.firma.de;`, eine Datei nur `listen 80` mit `server_name alt.firma.de;`, die eigene Datei mit `mqtt.firma.de` → genau `[pdh.firma.de www.firma.de]`.
  - `TestCertDisturbed`: Tabelle (`{"a|x":"A"}`/`{"a|x":"A"}` → false; `"A"`→`"B"` → true; `"A"`→`""` → true; `""`→`"B"` → false).
- [ ] **Step 2:** `go test ./cmd/update-agent -run 'TestCertGuard|TestCertOtherNames|TestCertDisturbed'` → FAIL (nicht definiert).
- [ ] **Step 3:** In `certInstallHandler` für Rolle `pdh` und `mqtt` mit `sitePath != "" && withNginx`: `before := certProbeAll(ctx, a.certOtherNames(entry), certHasIPv6())` **vor** dem ersten Schreiben; nach erfolgreichem `certApplyNginxFallback` `after` messen. Bei Störung: gibt es `siteV4` und wurde es noch nicht benutzt → `siteV4` schreiben, `nginx -t`, neu laden, erneut messen; ist es dann in Ordnung → `note = "Die Seite hört nur auf IPv4 – mit IPv6 bekäme " + name + " ein anderes Zertifikat (Datei …)"`. Sonst `rollback()`, `systemctl reload nginx`, `certFail(w, 200, "Die neue nginx-Seite würde " + name + " stören (über " + addr + " käme ein anderes Zertifikat) – zurückgenommen")`.
- [ ] **Step 4:** `go test ./cmd/update-agent` → PASS.
- [ ] **Step 5:** Commit `Update-Agent: neue Seite darf keinen anderen Namen stören – Prüfung mit Rücknahme (0.82.13)`.

### Task 3: PDH – Tunnel-Ziele mit 127.0.0.1, localhost gilt als gleich

**Files:**
- Modify: `internal/web/certs_strict.go:27,51`, `internal/web/cloudflare_api.go:176,211,249,262`, `internal/web/cloudflare_tunnel.go:29`, `internal/web/server_config.go:110`, `internal/web/mqtt_system.go:219,630`
- Test: `internal/web/cloudflare_service_test.go` (neu); bestehende Tests mit `localhost`-Zielen anpassen (`certs_fix_test.go`, `cloudflare_api_test.go`, `mqtt_system_test.go`, `cloudflare_tunnel_test.go`)

**Interfaces:**
- Produces: `const certStrictService = "https://127.0.0.1:443"`; `func cfSameService(a, b string) bool` (gleich, wenn Schema, Port und Pfad gleich und die Rechner gleich sind, wobei `localhost` als `127.0.0.1` gilt; nicht lesbare Angaben nur bei exakt gleichem Text); `func cfIsStrictService(s string) bool` (`cfSameService(s, certStrictService)`).

- [ ] **Step 1: Tests:** `TestCfSameService` (Tabelle: `http://localhost:8083` = `http://127.0.0.1:8083`; `https://localhost:443` = `https://127.0.0.1:443`; `http://localhost:8083` ≠ `http://127.0.0.1:8093`; `http://localhost:80` ≠ `https://localhost:80`; `http_status:404` = `http_status:404`); `TestCfCheckTargetTreatsLocalhostAsSame` (Ingress `http://localhost:8083`, Ziel `http://127.0.0.1:8083` → `TunnelOK`); `TestCfKeepStrictRecognizesOldLocalhost` (gespeichertes Ziel `https://localhost:443` bleibt streng); `TestMqttTargets` erwartet `http://127.0.0.1:8083`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestCfSameService|TestCfCheckTarget|TestCfKeepStrict|TestMqttTargets'` → FAIL.
- [ ] **Step 3:** Konstanten und Ziele umstellen (`http://127.0.0.1:8090`, `http://127.0.0.1:80`, `http://127.0.0.1:<WS>`); in `cfCheckTarget` und `cfKeepStrict` `cfSameService`/`cfIsStrictService` statt `==`.
- [ ] **Step 4:** `go test ./internal/web -run 'Cf|Cert|Mqtt|Cloudflare'` → PASS.
- [ ] **Step 5:** Commit `Cloudflare-Tunnel: Ziele mit 127.0.0.1, localhost gilt als gleich (0.82.13)`.

### Task 4: PDH – neue oder geänderte Namen sind „vorgemerkt“

**Files:**
- Modify: `internal/web/mqtt_system.go` (`mqttSystem`, `mqttSystems` :148, `mqttUpstreamFor` :187, `mqttTargets` :206), `internal/web/import_export.go` (Anlegen ~:372, Bearbeiten :470)
- Test: `internal/web/mqtt_pending_test.go` (neu)

**Interfaces:**
- Produces: `const mqttPendingKey = "int_public_pending"`; `func mqttMarkPending(stored, config map[string]string)` (kein Name → Schlüssel weg; neuer/geänderter Name (normalisiert) → `"1"`; gleicher Name → Wert aus `stored` übernehmen); Feld `Pending bool` in `mqttSystem` (aus `config->>'int_public_pending' = '1'`); `func mqttUpstreamIn(systems []mqttSystem, name string) string` (vorgemerkte zählen nicht; `mqttUpstreamFor` nutzt sie).

- [ ] **Step 1: Tests:** `TestMqttMarkPending` (Tabelle: neu mit Name → `"1"`; Name geändert → `"1"`; gleicher Name, nur Port geändert, `stored` ohne Schlüssel → kein Schlüssel; gleicher Name, `stored` `"1"` → `"1"`; Name geleert → kein Schlüssel; Groß/Klein und Punkt am Ende gelten als gleich); `TestMqttUpstreamInSkipsPending` (vorgemerkt → `""`, sonst `http://127.0.0.1:8093`); `TestMqttTargetsSkipsPending`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestMqttMarkPending|TestMqttUpstreamIn|TestMqttTargetsSkipsPending'` → FAIL.
- [ ] **Step 3:** `mqttMarkPending` in Anlegen (`stored` = leere Map) und Bearbeiten nach `validateMqttSystem`, vor `sealConnConfig` aufrufen; `mqttSystems` liest den Schlüssel mit; `mqttUpstreamFor` → `mqttUpstreamIn`. Ergebnis: Für vorgemerkte Namen schickt `certInstall` Rolle `""` (nur Dateien, eigene MQTT-Seite wird entfernt), der tägliche Abgleich bleibt ruhig (Ziel `""` = Status `""`).
- [ ] **Step 4:** `go test ./internal/web -run 'Mqtt|Cert'` → PASS.
- [ ] **Step 5:** Commit `MQTT-Name: neue oder geänderte Namen sind vorgemerkt, bis alles eingerichtet ist (0.82.13)`.

### Task 5: PDH – Einrichten als Ganzes, scharf schalten, Port-Änderung nachziehen

**Files:**
- Create: `internal/web/mqtt_setup.go`
- Modify: `internal/web/import_export.go` (nach dem Speichern :501–510 und beim Anlegen), `internal/web/mqtt_system.go` (`mqttCheckRows` :438: Zeile „Stand“ oben, Hinweise mit genauem Eintrag), `web/templates/widgets/mqtt_checklist.gohtml`, `web/templates/mqtt_broker.gohtml` (Knopf „Einrichten / erneut prüfen“), `internal/web/handler.go` (Route)
- Test: `internal/web/mqtt_setup_test.go`

**Interfaces:**
- Consumes: `mqttPendingKey`, `mqttUpstreamIn` (Task 4); `cfSameService` (Task 3); vorhandene `certDoAdd`, `certStartIssue`, `certDoLAN(ctx, d, name, false)`, `mqttFirewall`, `mqttSiteSync`, `cfSetIngress`, `PutTunnelConfig`, `CreateRecord`/`UpdateRecord`.
- Produces:
  - `type mqttSetupRights struct{ Cert, LAN, Cloudflare bool }` – beim Speichern aus `certCanManage(r)`, `h.hasPerm(r, "system.unifi")`, `canCloudflare(r)`.
  - `type mqttSetupDeps struct{…}` mit Funktionsfeldern für Zertifikat, Eintrag im Haus, Firewall, Seite, Tunnel und Speichern der Vormerkung (für Tests ersetzbar, `func (h *Handler) mqttSetupDepsLive() mqttSetupDeps`).
  - `func mqttNameSetup(ctx context.Context, d mqttSetupDeps, id string, can mqttSetupRights) mqttSetupResult` – Reihenfolge: Zertifikat (fehlt es und darf `can.Cert` → anlegen und ausstellen, bis 10 min auf Ergebnis warten, alle 10 s nachsehen), Eintrag im Haus (UniFi eingerichtet und `can.LAN` → `certDoLAN` ohne Überschreiben), Firewall öffnen; **scharf**, wenn Zertifikat da, Name im Haus zeigt auf den Server (`mqttLANJudge` grün) und Firewall nicht gesperrt: Vormerkung löschen, `mqttSiteSync(host)`, dann Tunnel (Zugang und `can.Cloudflare` → Zeile `http://127.0.0.1:<WS>` setzen, eigener CNAME „PDH: Tunnel“ anlegen/ändern; fremder Eintrag → nichts ändern, Zeile bleibt rot).
  - `func mqttTunnelFollowPort(ctx context.Context, d mqttSetupDeps, host string, oldWS, newWS int, can mqttSetupRights) error` – nur wenn die vorhandene Zeile für `host` laut `cfSameService` auf `http://127.0.0.1:<oldWS>` zeigt.
  - Route `POST /import/connections/{id}/broker/setup` (`import.write`) → `MqttBrokerSetupWeb`, antwortet mit der Prüfliste.

- [ ] **Step 1: Tests** (nur mit ersetzten `mqttSetupDeps`):
  - `TestMqttSetupArmsWhenAllGreen`: Zertifikat da, Haus grün, Firewall offen → Vormerkung gelöscht, Seite und Tunnel je einmal aufgerufen, Tunnel-Ziel `http://127.0.0.1:8093`.
  - `TestMqttSetupStaysPendingWithoutLAN`: Haus nicht eingerichtet, kein Recht `LAN` → Vormerkung bleibt, Seite und Tunnel **nicht** aufgerufen, Ergebnis enthält Hinweis `A mqtt.firma.de → 192.0.2.202` (Adresse aus den Ersatz-Abhängigkeiten).
  - `TestMqttSetupNoCertRightStaysPending`: kein Zertifikat, `can.Cert` falsch → keine Ausstellung, Vormerkung bleibt.
  - `TestMqttSetupForeignDNSNotReplaced`: Cloudflare hat einen fremden A-Eintrag → Tunnel-Zeile wird nicht geschrieben, Ergebnis-Zeile `Replace` gesetzt, Name trotzdem scharf (nginx-Seite).
  - `TestMqttTunnelFollowPort`: Zeile `http://localhost:8083` → wird `http://127.0.0.1:8093`; Zeile zeigt auf `http://127.0.0.1:9999` → unverändert; ohne Zugang oder ohne `can.Cloudflare` → kein Fehler, kein Schreiben.
- [ ] **Step 2:** `go test ./internal/web -run 'TestMqttSetup|TestMqttTunnelFollowPort'` → FAIL.
- [ ] **Step 3:** `mqtt_setup.go` bauen. Nach dem Speichern (Anlegen und Bearbeiten, `kind == "mqtt"`, Name gesetzt): im Hintergrund (Frist 15 min, `recover` wie `mqttSiteSync`) `mqttNameSetup`; bei gleichem Namen und geändertem WebSocket-Port und nicht vorgemerkt zusätzlich `mqttTunnelFollowPort`. Am Spiegel und in der Wartung nichts tun (wie `mqttSiteSync`). Prüfliste: Zeile „Stand“ – „vorgemerkt – wird scharf, sobald alle Punkte grün sind“ (amber) bzw. „scharf“ (grün); Zeilen ohne Recht oder Zugang nennen den genauen Eintrag (Typ, Name, Ziel) und welches Recht fehlt.
- [ ] **Step 4:** `go test ./internal/web` → PASS.
- [ ] **Step 5:** Commit `MQTT-Name: alles in einem Ablauf einrichten, erst dann scharf; Port-Änderung zieht den Tunnel nach (0.82.13)`.

### Task 6: Handbuch, Änderungsprotokoll, Version, offene Punkte

**Files:**
- Modify: `VERSION` (0.82.13), `CHANGELOG.md`, `web/templates/help.gohtml` (Kapitel `mqttbroker` :2425–2470, `hb-cloudflare-api` :1888–1898, `hb-zertifikate` :1932–1957), `AGENTS.md` (offene Punkte)

- [ ] **Step 1:** CHANGELOG `## [0.82.13] – 2026-11-21`: `### Behoben` – „Ein neuer MQTT-Name kann den Zugang zum PDH nicht mehr unterbrechen: Der Update-Agent prüft nach jeder neuen Seite, ob alle anderen Namen ihr Zertifikat behalten, und nimmt sie sonst zurück.“; „Nach einer Änderung des WebSocket-Ports zeigt die Tunnel-Zeile des MQTT-Namens selbst auf den neuen Port.“ `### Geändert` – „Ein neuer MQTT-Name wird erst scharf geschaltet, wenn Zertifikat, Eintrag im Haus und Firewall stehen; was das PDH nicht selbst darf, zeigt die Prüfliste mit dem genauen Eintrag.“; „Der Cloudflare-Tunnel erreicht die Dienste über 127.0.0.1 statt localhost.“
- [ ] **Step 2:** Handbuch: Anleitung im Kapitel `mqttbroker` (Name eingeben → „vorgemerkt“ → das PDH richtet ein → „scharf“; Knopf „Einrichten / erneut prüfen“); Satz „ändert nichts von selbst“ in `hb-cloudflare-api` um die Ausnahme MQTT-Name ergänzen; in `hb-zertifikate`: IPv6 nur, wenn alle Seiten auf IPv6 hören, und die Prüfung anderer Namen.
- [ ] **Step 3:** `go test ./... ` (volle Reihe, Labor-Datenbank) und `go test -tags integration ./internal/web -run 'Terminal|Anlagenterminal'` → PASS; `TestHelpChaptersMatch`, `TestChangelogMatchesVersion` grün.
- [ ] **Step 4:** Commit `0.82.13: Handbuch und Änderungsprotokoll`.
