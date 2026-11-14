# Datennetz Paket 1 – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Das PDH erreicht Fremddatenbanken über ein Tailscale-Datennetz (Cloud-Tailnet): ein PDH-Daten-Client auf dem Datenbankserver bietet nur feste Datenbankports an, das PDH verbindet sich über eine lokale Brücke und sieht Status und Version jedes Clients.

**Architecture:** Gemeinsame Bausteine liegen in `internal/datanet` (Knoten-Schnittstelle `Node` mit tsnet-Umsetzung, TCP-Weiterleitung, Datenbankprüfung, Meldeprotokoll, lokale Brücken, Knoten-Verwaltung ohne Blockieren). Das Programm `cmd/pdh-data-client` nutzt sie als Dienst (Windows/systemd). Das PDH startet einen Knoten je Server im Hintergrund, lauscht im Datennetz auf Port 8443 für Meldungen und leitet Verbindungen mit `route=net:<Client-ID>` über `127.0.0.1:<Zufallsport>` ins Datennetz.

**Tech Stack:** Go (Modul `pdh`), `tailscale.com/tsnet`, `golang.org/x/sys/windows/svc`, PostgreSQL-Migrationen, chi, Go-Templates (Fiori/Fluent).

**Spec:** `docs/superpowers/specs/2026-11-14-datennetz-tailscale-design.md` (verbindlich: „Entscheidungen nach der Prüfung“ 1–14) und `AGENTS.md`.

## Global Constraints

- Nur Paket 1 (Entscheidung 14): Client + Cloud-Netz + Weg und Brücke + Rückkanal. **Keine** Selbst-Updates, keine Signatur (Paket 2), **kein** Headscale/internes Netz (Paket 3). Spalte `net` ist vorbereitet, Wert in Paket 1 immer `cloud`.
- Zusatzfunktion: Das PDH muss ohne Datennetz laufen; Netz standardmäßig **aus**; Start nie blockierend (Entscheidung 10): PDH in unter 5 s bereit, auch bei hängender Steuerung.
- Ein PDH-Knoten **je Server** mit eigener Identität und eigenem Zustand auf der Platte (Entscheidung 2); Tag `tag:pdh`, Clients `tag:pdh-data`.
- Rückkanal: genau ein Port im Datennetz, `8443` (Entscheidung 4), nicht die Weboberfläche. Client-Token nur gehasht (SHA-256) gespeichert.
- Ziele je Client: feste Portliste, Ziel `127.0.0.1` oder fest eingetragene Adresse, beim Client konfiguriert; das PDH kann keine anderen Ziele anfordern (Entscheidung 5).
- Der Client kennt **keine** Datenbank-Passwörter und prüft nur die TCP-Erreichbarkeit.
- Takt Prüfung/Meldung 60 s; offline = kein Kontakt seit 3 Takten (180 s).
- Weg gespeichert als `route=net:<Client-ID>`; bestehende Werte `direct` und `vpn:<id>` bleiben unverändert gültig.
- Rechte `datanet.view`, `datanet.edit`, `datanet.admin` (Bereichsadministrator; endgültig entfernen nur admin), Clients ohne Abteilung (global).
- Nächste freie Migration: **160** (zuletzt `159_setup_times.up.sql`, auch auf `origin/main`); vor dem Merge erneut prüfen.
- Version: `VERSION` → `0.80.7` (main steht auf 0.80.6), neuer Eintrag oben in `CHANGELOG.md`.
- Texte, Handbuch, Commits auf Deutsch, Anwendersprache. Oberfläche in Fiori und Fluent, hell/dunkel, nur Variablen (`--bg`, `--text`, `--accent` …).
- Bauen ohne CGO (`CGO_ENABLED=0`) für `windows/amd64` und `linux/amd64`.
- Tests in Git Bash: `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...`. Echte Tailscale-Verbindungen nie in Tests: alles über die Schnittstelle `datanet.Node` mit Attrappe.

## Review Focus

1. **Brücke und TLS:** Über die Brücke verbindet sich der Treiber mit `127.0.0.1`. SQL Server mit `use_tls=true` ohne Fingerabdruck und MySQL mit TLS müssen den Zertifikatsnamen weiterhin gegen den eingetragenen Host prüfen (`hostnameincertificate` bzw. `tls.Config.ServerName`), sonst scheitert jede Anmeldung oder die Prüfung fällt still weg. Test in Aufgabe 6.
2. **Benannte SQL-Server-Instanz mit Weg Datennetz:** Die Portsuche über den SQL Browser (UDP 1434) geht durchs Datennetz nicht. Ohne Port muss das Speichern mit klarer Meldung scheitern statt später zu hängen. Test in Aufgabe 6.
3. **Gefälschte Meldungen:** Die Adresse eines Clients wird aus der Absenderadresse der Verbindung im Datennetz übernommen, nie aus dem Meldungsinhalt; gesperrte Clients bekommen 403, zu große Meldungen 413. Tests in Aufgabe 5.
4. **Sperren wirkt sofort:** Nach Sperren oder Entfernen werden offene Brücken zum Client geschlossen und neue Verbindungen abgelehnt („Client gesperrt“). Test in Aufgabe 6.
5. **Einmal-Schlüssel nicht liegen lassen:** Nach dem ersten erfolgreichen Beitritt löschen Client (aus `config.json`) und PDH (aus `app_settings`) den Einmal-Schlüssel; ohne Zustand und ohne Schlüssel ruft niemand `tsnet.Up` auf (das würde auf eine interaktive Anmeldung warten), sondern zeigt „Schlüssel fehlt“. Tests in Aufgaben 3 und 5.

---

## Dateiübersicht

| Datei | Aufgabe |
|---|---|
| `internal/datanet/node.go`, `tsnode.go`, `nodetest/fake.go` | Knoten-Schnittstelle, tsnet-Umsetzung, Attrappe (1) |
| `internal/datanet/forward.go`, `probe.go`, `report.go`, `bridge.go`, `target.go` | Weiterleitung, Prüfung, Protokoll, Brücken, Ziele (2) |
| `internal/datanet/manager.go` | Knoten starten ohne Blockieren (5) |
| `cmd/pdh-data-client/main.go`, `config.go`, `run.go`, `service_windows.go`, `service_linux.go`, `service_other.go` | Client (1, 3) |
| `migrations/160_datanet.up.sql` | Tabellen, Rechte (4) |
| `internal/web/datanet_store.go` | Clients speichern, Token, Status (4) |
| `internal/web/datanet_node.go`, `datanet_report.go` | Knoten im PDH, Rückkanal (5) |
| `internal/web/conn_net.go`, Änderungen `conn_route.go`, `sql_preview.go`, `connection_checks.go`, `import_export.go`, `widgets/connection_list.gohtml` | Weg und Brücke (6) |
| `internal/web/datanet_pages.go`, `web/templates/datanet.gohtml`, `nav.go`, `handler.go`, `perm_gate.go` | Oberfläche (7) |
| `Makefile`, `Dockerfile`, `install_update.sh` | Bau und Auslieferung (1) |
| `web/templates/help.gohtml`, `widgets/help_figures.gohtml`, `internal/web/help.go`, `CHANGELOG.md`, `VERSION` | Handbuch, Version (8) |

---

### Task 1: Abhängigkeit `tailscale.com`, Knoten-Schnittstelle und Bau des Clients

Risiko zuerst: Ist `tailscale.com` nicht baubar, endet die Ausführung hier mit Bericht an den Anwender.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/datanet/node.go`, `internal/datanet/tsnode.go`, `internal/datanet/nodetest/fake.go`, `internal/datanet/node_test.go`
- Create: `cmd/pdh-data-client/main.go`, `cmd/pdh-data-client/main_test.go`
- Modify: `Makefile`, `Dockerfile`, `install_update.sh`, `cmd/update-agent/update_test.go`

**Interfaces:**
- Produces:
  ```go
  package datanet
  const ReportPort = 8443
  const TagPDH, TagClient = "tag:pdh", "tag:pdh-data"
  type NodeConfig struct{ Dir, Hostname, AuthKey, ControlURL string } // ControlURL leer = tailscale.com
  type Node interface {
      Up(ctx context.Context) ([]netip.Addr, error)            // eigene Datennetz-Adressen
      Listen(network, addr string) (net.Listener, error)
      DialContext(ctx context.Context, network, addr string) (net.Conn, error)
      PeerTags(ctx context.Context, remoteAddr string) ([]string, error) // Tags des Gegenübers (WhoIs)
      Close() error
  }
  func HasState(dir string) bool                 // Zustand von tsnet vorhanden (tailscaled.state)
  func NewTSNode(cfg NodeConfig) (Node, error)   // tsnet.Server, Logf leise (zerolog debug)
  ```
  `nodetest.Fake` implementiert `Node` auf echtem Loopback: `Up` liefert `Addrs` oder blockiert bis ctx-Ende, wenn `BlockUp` gesetzt; `Listen` lauscht auf `127.0.0.1:<port aus addr oder 0>` und merkt sich die Zuordnung in `Ports map[int]string`; `DialContext` wählt über `Routes map[string]string` (Datennetz-Adresse → Loopback-Adresse); `PeerTags` liefert `Tags[remoteIP]`.
  `cmd/pdh-data-client`: `var version = "dev"` (per `-ldflags -X main.version=…`), Befehl `version` gibt `pdh-data-client <version> <GOOS>/<GOARCH>` aus.
  Ausliefernamen: `bin/pdh-data-client-linux-amd64`, `bin/pdh-data-client-windows-amd64.exe`.

- [ ] **Step 1: Baubarkeit prüfen (Git Bash, Ergebnis im Commit-Text festhalten)**

  ```bash
  cd /d/pdh-server && cp go.mod go.sum "$TMP/" && go get tailscale.com@latest && go mod tidy
  grep -E '^go |^toolchain|tailscale.com |golang.org/x/(sys|net|crypto) ' go.mod
  ```
  Erwartet: `go get` ohne Fehler. Notieren: neue `go`-Zeile (z. B. `go 1.25.x`), Versionssprünge von `x/sys`, `x/net`, `x/crypto`, `pgx`, `go-mssqldb`. Danach `go build ./... && go vet ./internal/web/ ./cmd/...` muss durchlaufen und `go test ./...` (mit `PDH_TEST_DSN`) grün bleiben wie vorher.
  **Abbruch:** Scheitert `go get` oder brechen bestehende Tests durch angehobene Abhängigkeiten, `go.mod`/`go.sum` aus `$TMP` zurückkopieren, nichts einchecken und dem Anwender berichten (Version, Fehlermeldung, Vorschlag: ältere `tailscale.com`-Version mit passender Go-Version).

- [ ] **Step 2: Failing test** `internal/datanet/node_test.go`

  ```go
  func TestHasState(t *testing.T) {
      d := t.TempDir()
      if HasState(d) { t.Fatal("leeres Verzeichnis hat keinen Zustand") }
      os.WriteFile(filepath.Join(d, "tailscaled.state"), []byte("{}"), 0o600)
      if !HasState(d) { t.Fatal("Zustand nicht erkannt") }
  }
  func TestFakeIsNode(t *testing.T) { var _ Node = &nodetest.Fake{} }
  ```
  `cmd/pdh-data-client/main_test.go`: `TestVersionLine` – `versionLine()` beginnt mit `"pdh-data-client dev "` und endet mit `runtime.GOOS+"/"+runtime.GOARCH`.
  `cmd/update-agent/update_test.go`: `TestInstallScriptBuildsDataClient` – `install_update.sh` enthält `./cmd/pdh-data-client`, `GOOS=windows`, `GOOS=linux`, `CGO_ENABLED=0`, `pdh-data-client-windows-amd64.exe.neu`; der Bau steht vor `mv -f "$bin_dir/pdh.neu"`.

- [ ] **Step 3: Tests laufen lassen** – `go test ./internal/datanet/... ./cmd/pdh-data-client/ ./cmd/update-agent/ -run 'HasState|Fake|VersionLine|BuildsDataClient'` → FAIL (nicht definiert).

- [ ] **Step 4: Umsetzen**
  - `node.go`: Konstanten, `NodeConfig`, `Node`, `HasState`.
  - `tsnode.go`: `tsNode` um `*tsnet.Server` (`Hostname`, `Dir`, `AuthKey`, `ControlURL`, `Logf` auf zerolog-Debug, `UserLogf` auf Info); `Up` gibt `TailscaleIPs()` zurück; `PeerTags` über den LocalClient (`WhoIs(ctx, remoteAddr)` → `Node.Tags`; Paketname je nach Version `tailscale.com/client/local` oder `tailscale.com/client/tailscale`).
  - `nodetest/fake.go` wie oben.
  - `cmd/pdh-data-client/main.go`: `main` mit Befehlsweiche (`version`; übrige Befehle folgen in Aufgabe 3, bis dahin Hilfe-Text und Exit 2).
  - `Makefile` Ziel `build-data-client` und Aufruf in `build`: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(shell cat VERSION)" -o bin/pdh-data-client-linux-amd64 ./cmd/pdh-data-client`, dasselbe mit `GOOS=windows` → `.exe`.
  - `install_update.sh`: beide Client-Bauten nach `bin/…​.neu` im selben Block wie `pdh.neu`/`update-agent.neu`, in `rm -f` aufnehmen und mit `mv -f` umbenennen; `ver="$(cat "$repo_dir/VERSION")"` für `-X main.version`. Kommentar „derzeit 1.24“ auf die neue Go-Version aus Step 1 ändern.
  - `Dockerfile`: `golang:1.24-bookworm` (Bau-Stufe **und** `COPY --from` in `codeserver`) auf die von `go.mod` verlangte Version heben (offizielles Image setzt `GOTOOLCHAIN=local`); in der Bau-Stufe beide Clients nach `/out/clients/` bauen; in `app`: `COPY --from=build --chown=10001:10001 /out/clients ./clients` und `ENV PDH_DATA_CLIENT_DIR=/app/clients`.

- [ ] **Step 5: Prüfen**
  ```bash
  go test ./internal/datanet/... ./cmd/pdh-data-client/ ./cmd/update-agent/
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$TMP/c.exe" ./cmd/pdh-data-client
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$TMP/c" ./cmd/pdh-data-client
  ls -l "$TMP/c" "$TMP/c.exe" bin/pdh 2>/dev/null; go build -o "$TMP/pdh.exe" ./cmd/server && ls -l "$TMP/pdh.exe"
  ```
  Erwartet: PASS, beide Builds ohne Fehler. Größen von Client und PDH vorher/nachher im Commit-Text notieren (Entscheidung 13).

- [ ] **Step 6: Commit** – `git add go.mod go.sum internal/datanet cmd/pdh-data-client Makefile Dockerfile install_update.sh cmd/update-agent/update_test.go && git commit -m "Datennetz: tailscale.com aufgenommen, Knoten-Schnittstelle, Bau des Daten-Clients"`

---

### Task 2: Bausteine Weiterleitung, Prüfung, Meldeprotokoll, Brücken

**Files:**
- Create: `internal/datanet/target.go`, `forward.go`, `probe.go`, `report.go`, `bridge.go`
- Test: `internal/datanet/target_test.go`, `forward_test.go`, `bridge_test.go`, `report_test.go`

**Interfaces:**
- Consumes: `Node`, `nodetest.Fake` (Aufgabe 1)
- Produces:
  ```go
  type Target struct{ Port int `json:"port"`; Addr string `json:"addr"` } // Addr host:port lokal
  func ParseTarget(s string) (Target, error) // "1433" → {1433,"127.0.0.1:1433"}; "1433=10.0.0.5:1433"
  func ValidateTargets(ts []Target) error   // 1–16 Ziele, Ports 1–65535, keine doppelten, Port ≠ ReportPort
  func Forward(ctx context.Context, ln net.Listener, target string, allow func(remote net.Addr) bool) // bis ctx-Ende
  func Probe(ctx context.Context, addr string, timeout time.Duration) error // TCP-Verbindung auf und zu
  type TargetStatus struct{ Port int `json:"port"`; OK bool `json:"ok"`; Error string `json:"error,omitempty"`; CheckedAt time.Time `json:"checked_at"` }
  type Report struct{ ClientID, Version, OS, Hostname string; Targets []TargetStatus } // JSON: client_id, version, os, hostname, targets
  type ReportResponse struct{ IntervalSeconds int `json:"interval_seconds"`; PDHVersion string `json:"pdh_version"` }
  const ReportPath = "/v1/report"; const MaxReportBytes = 64 << 10; const DefaultInterval = 60 * time.Second
  func ClampInterval(sec int) time.Duration // 15 s … 600 s, 0 → DefaultInterval
  type Dialer interface{ DialContext(ctx context.Context, network, addr string) (net.Conn, error) }
  func NewBridges(d Dialer) *Bridges
  func (b *Bridges) Addr(remote string) (string, error) // "127.0.0.1:<port>", je remote ein dauerhafter Listener
  func (b *Bridges) CloseHost(ip string)                // alle Brücken zu dieser Datennetz-Adresse schließen, offene Verbindungen trennen
  func (b *Bridges) Close()
  ```

- [ ] **Step 1: Failing tests**
  - `TestParseTarget`: `"1433"` → `{1433, "127.0.0.1:1433"}`; `"3306=10.0.0.5:3306"` → wie angegeben; Fehler für `""`, `"0"`, `"70000"`, `"1433=nohostport"`, `"abc"`.
  - `TestValidateTargets`: Fehler bei leer, bei 17 Zielen, doppeltem Port, Port 8443.
  - `TestForwardPipesAndFilters`: Echo-Server auf Loopback, `Forward` mit `allow` = true → geschriebene Bytes kommen zurück; mit `allow` = false wird die Verbindung sofort geschlossen (Lesen liefert EOF, kein Byte erreicht den Echo-Server).
  - `TestProbe`: offener Port → nil; geschlossener Port → Fehler innerhalb `timeout + 1 s`.
  - `TestBridgeRoundTrip`: `Dialer` = `nodetest.Fake{Routes: {"100.64.0.7:1433": <echo>}}`; `Addr("100.64.0.7:1433")` beginnt mit `127.0.0.1:`; zweiter Aufruf liefert dieselbe Adresse; Echo über die Brücke klappt.
  - `TestBridgeCloseHost`: offene Verbindung über die Brücke wird nach `CloseHost("100.64.0.7")` getrennt; neuer `Addr`-Aufruf baut eine neue Brücke.
  - `TestReportJSON`: Feldnamen `client_id`, `version`, `os`, `hostname`, `targets[].port/ok/checked_at`; `ClampInterval(0)==60s`, `(5)==15s`, `(9999)==600s`.

- [ ] **Step 2:** `go test ./internal/datanet/` → FAIL.
- [ ] **Step 3: Umsetzen.** `Forward`: je Verbindung Goroutine, Ziel mit 10 s Zeitgrenze wählen, zwei `io.Copy` mit Halbschließen; ctx-Ende schließt den Listener. `Bridges`: Listener `net.Listen("tcp","127.0.0.1:0")`, Zuordnung `map[remote]*bridge` unter Mutex, offene Verbindungen je Brücke merken, damit `CloseHost` sie trennt.
- [ ] **Step 4:** `go test -race ./internal/datanet/` → PASS.
- [ ] **Step 5: Commit** – `Datennetz: Weiterleitung, Prüfung, Meldeprotokoll und lokale Brücken`

---

### Task 3: PDH-Daten-Client (Konfiguration, Lauf, Dienst)

**Files:**
- Create: `cmd/pdh-data-client/config.go`, `run.go`, `service_windows.go`, `service_linux.go`, `service_other.go`
- Modify: `cmd/pdh-data-client/main.go`
- Test: `cmd/pdh-data-client/config_test.go`, `run_test.go`, `service_linux_test.go`

**Interfaces:**
- Consumes: `Node`, `NodeConfig`, `NewTSNode`, `HasState`, `Target`, `ParseTarget`, `ValidateTargets`, `Forward`, `Probe`, `Report`, `ReportResponse`, `ReportPath`, `ClampInterval`, `TagPDH`, `ReportPort`
- Produces (für Aufgabe 7, Installationsbefehl):
  ```
  pdh-data-client install --id <uuid> --token <token> --pdh <ip>[,<ip>] --target <port[=host:port]> [--target …] [--authkey tskey-…] [--hostname <name>]
  pdh-data-client uninstall
  pdh-data-client run          # Vordergrund bzw. vom Dienst gestartet
  pdh-data-client version
  ```
  ```go
  type Config struct {
      ClientID string `json:"client_id"`; Token string `json:"token"`; AuthKey string `json:"auth_key,omitempty"`
      Hostname string `json:"hostname"`; PDH []string `json:"pdh"`; Targets []datanet.Target `json:"targets"`; StateDir string `json:"state_dir"`
  }
  func LoadConfig(path string) (Config, error); func SaveConfig(path string, c Config) error // 0600
  func (c Config) Validate() error
  func Run(ctx context.Context, c Config, node datanet.Node, save func(Config) error) error
  func systemdUnit(binPath string) string
  ```
  Pfade: Windows `%ProgramData%\PDH-Datennetz\config.json`, Zustand `%ProgramData%\PDH-Datennetz\state`, Programm `%ProgramFiles%\PDH-Datennetz\pdh-data-client.exe`; Linux `/etc/pdh-data-client/config.json`, `/var/lib/pdh-data-client`, `/usr/local/bin/pdh-data-client`. Dienstname `pdh-data-client`, Anzeigename „PDH-Daten-Client“.

- [ ] **Step 1: Failing tests**
  - `TestConfigValidate`: Fehler ohne `client_id` (keine UUID), ohne `token`, ohne `pdh`, mit ungültigem Ziel; `PDH`-Einträge ohne Port bekommen beim Laden `:8443`.
  - `TestConfigSaveMode` (nur Linux, sonst Skip): Datei hat Rechte `0600`.
  - `TestRunForwardsOnlyToPDHPeers`: `nodetest.Fake` mit `Tags{"127.0.0.1": {"tag:pdh"}}` → Verbindung auf den gelauschten Port erreicht den Echo-Server; mit Tags `{"tag:other"}` wird sie geschlossen.
  - `TestRunReportsAndClearsAuthKey`: PDH-Attrappe = `httptest.Server` auf Loopback, `Fake.Routes` leitet `100.64.0.1:8443` dorthin; `Run` mit `AuthKey:"tskey-x"` → erste Meldung trifft innerhalb 2 s ein mit `Authorization: Bearer <token>`, `client_id`, `version`, einem `TargetStatus` je Ziel (OK für offenen, Fehler für geschlossenen Port); `save` wurde mit leerem `AuthKey` aufgerufen.
  - `TestRunTriesNextPDH`: erste PDH-Adresse antwortet 503, zweite 200 → Meldung landet bei der zweiten.
  - `TestRunNeedsKey`: leeres `StateDir`, kein `AuthKey` → `Run` gibt sofort Fehler „Einmal-Schlüssel fehlt“ zurück, `Fake.Up` wurde nicht aufgerufen.
  - `TestSystemdUnit`: enthält `ExecStart=/usr/local/bin/pdh-data-client run`, `Restart=always`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ReadWritePaths=/var/lib/pdh-data-client /etc/pdh-data-client`.

- [ ] **Step 2:** `go test ./cmd/pdh-data-client/` → FAIL.
- [ ] **Step 3: Umsetzen.**
  - `Run`: `NeedsKey` prüfen (kein `HasState(StateDir)` und leerer `AuthKey` → Fehler); `node.Up` mit Wiederholung (10 s, verdoppelnd bis 5 min) bis Erfolg oder ctx-Ende; nach Erfolg `AuthKey` leeren und `save`; je Ziel `node.Listen("tcp", ":<Port>")` und `datanet.Forward` mit `allow` = `PeerTags` enthält `TagPDH`; Meldeschleife: `Probe` je Ziel (5 s), POST `http://<pdh>` + `ReportPath` über `http.Client{Transport: &http.Transport{DialContext: node.DialContext}, Timeout: 15 s}`, Adressen der Reihe nach bis 200; Takt aus `ClampInterval(resp.IntervalSeconds)`.
  - `install`: Konfiguration aus Schaltern bauen, `Validate`, Programm an den festen Pfad kopieren, Konfiguration speichern (Windows: Ordner-ACL nur SYSTEM/Administratoren über `icacls`), Dienst einrichten und starten. Windows über `golang.org/x/sys/windows/svc/mgr` (Start automatisch, Wiederherstellung `ServiceRestart` nach 10 s, dreimal), `run` erkennt `svc.IsWindowsService()` und läuft als Dienst. Linux: `systemdUnit` nach `/etc/systemd/system/pdh-data-client.service`, `systemctl daemon-reload && systemctl enable --now pdh-data-client`. `uninstall`: Dienst anhalten und entfernen, Programm und Konfiguration löschen, Zustand löschen.
  - `service_other.go` (`//go:build !windows && !linux`): `install`/`uninstall` melden „nicht unterstützt“.
- [ ] **Step 4:** `go test ./cmd/pdh-data-client/ && GOOS=windows go vet ./cmd/pdh-data-client/ && GOOS=linux go vet ./cmd/pdh-data-client/` → PASS.
- [ ] **Step 5: Commit** – `Datennetz: PDH-Daten-Client mit Weiterleitung, Meldung und Dienst`

---

### Task 4: Migration 160, Rechte und Speicher der Clients

**Files:**
- Create: `migrations/160_datanet.up.sql`, `internal/web/datanet_store.go`
- Modify: `internal/web/rbac_guard.go` (`permCategoryOrder`: `"Datennetz"` hinter `"VPN"`)
- Test: `internal/web/datanet_migration_test.go`, `internal/web/datanet_store_test.go`

**Interfaces:**
- Produces:
  ```sql
  datanet_clients(id UUID PK DEFAULT pdh_uuid_v7(), name VARCHAR(80) NOT NULL, net VARCHAR(10) NOT NULL DEFAULT 'cloud' CHECK (net IN ('cloud','intern')),
    token_hash CHAR(64) NOT NULL, addr VARCHAR(64) NOT NULL DEFAULT '', hostname VARCHAR(120) NOT NULL DEFAULT '',
    version VARCHAR(40) NOT NULL DEFAULT '', os VARCHAR(40) NOT NULL DEFAULT '', targets JSONB NOT NULL DEFAULT '[]',
    last_seen_at TIMESTAMPTZ, created_by UUID REFERENCES users(id) ON DELETE SET NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ, revoked_by UUID REFERENCES users(id) ON DELETE SET NULL)
  -- UNIQUE(name) WHERE revoked_at IS NULL
  datanet_events(id, at, user_id, client_id → ON DELETE SET NULL, client_name, action VARCHAR(20), note TEXT)
  ```
  ```go
  const permDataNetView, permDataNetEdit, permDataNetAdmin = "datanet.view", "datanet.edit", "datanet.admin"
  const dataNetRef = "datanet_client"
  type dataNetClient struct{ ID, Name, Net, Addr, Hostname, Version, OS string; Targets []datanet.TargetStatus; LastSeen, Created time.Time; Revoked bool; RevokedAt time.Time }
  func (h *Handler) dataNetCreate(ctx context.Context, name, userID string) (c dataNetClient, token string, err error) // Token 32 Byte, base64url
  func (h *Handler) dataNetList(ctx context.Context) ([]dataNetClient, error)
  func (h *Handler) dataNetGet(ctx context.Context, id string) (dataNetClient, error)
  func (h *Handler) dataNetAuth(ctx context.Context, id, token string) (dataNetClient, error) // errDataNetToken, errDataNetRevoked
  func (h *Handler) dataNetSaveReport(ctx context.Context, id, addr string, rep datanet.Report) error
  func (h *Handler) dataNetRevoke(ctx context.Context, id, userID string) error
  func (h *Handler) dataNetDelete(ctx context.Context, id, userID string) error
  func dataNetState(c dataNetClient, now time.Time, interval time.Duration) string // "revoked" | "new" (nie gemeldet) | "offline" | "db_down" | "ok"
  ```

- [ ] **Step 1: Failing tests**
  - `TestDataNetMigration` (DB): Tabellen vorhanden; Rechte `datanet.view/edit/admin` in Kategorie `Datennetz`; Invarianten wie in `ruesten/migration_integration_test.go`: jede Rolle mit `vpn.use` oder (global) `system.server_config` hat `datanet.view` und `datanet.edit`; Rolle `admin` hat `datanet.admin`; jede Rolle mit `sync.admin` hat `datanet.admin`; keine `datanet.*`-Zuordnung hat eine `department_id`.
  - `TestDataNetStore` (DB): `dataNetCreate` liefert Token ≥ 43 Zeichen, in der Tabelle steht nur `sha256(token)` als Hex; `dataNetAuth` mit falschem Token → `errDataNetToken`; nach `dataNetRevoke` → `errDataNetRevoked`; `dataNetSaveReport` setzt `addr`, `version`, `last_seen_at`; je Aktion ein `datanet_events`-Eintrag (`create`, `revoke`, `delete`).
  - `TestDataNetState` (ohne DB, Tabelle): nie gemeldet → `new`; zuletzt vor 179 s → `ok`; vor 181 s → `offline`; ein Ziel `OK=false` → `db_down`; gesperrt → `revoked` (Vorrang).

- [ ] **Step 2:** `PDH_TEST_DSN=… go test ./internal/web/ -run 'DataNet(Migration|Store|State)'` → FAIL.
- [ ] **Step 3: Umsetzen.** Migration nach Muster `158_extsync.up.sql`:
  - `datanet.view`, `datanet.edit` an Rollen, die `vpn.use` oder `system.server_config` **ohne Abteilung** haben (heutige VPN-Rechte: `vpn.use` für eigene Zugänge, VPN-Verwaltung über `system.server_config`, siehe `internal/web/vpn.go` und Migration 137).
  - `datanet.admin` an Rolle `admin`, an Rollen mit `sync.admin` (Bereichsadministrator der Verbindungen/Abgleich) und an Rollen mit globalem `system.server_config`, die schon ein anderes `%.admin` haben.
  - Kommentar im Kopf der Migration, warum genau diese Rollen.
  Token-Vergleich mit `subtle.ConstantTimeCompare` auf den Hashes.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web/ -run 'Perm|Help'` bleibt grün.
- [ ] **Step 5: Commit** – `Datennetz: Migration 160, Rechte und Speicher der Clients`

---

### Task 5: PDH-Knoten ohne Blockieren und Rückkanal auf Port 8443

**Files:**
- Create: `internal/datanet/manager.go`, `internal/datanet/manager_test.go`
- Create: `internal/web/datanet_node.go`, `internal/web/datanet_report.go`
- Modify: `internal/web/handler.go` (Felder), `cmd/server/main.go`
- Test: `internal/web/datanet_report_test.go`, `internal/web/datanet_node_test.go`

**Interfaces:**
- Consumes: `Node`, `NodeConfig`, `HasState`, `NewTSNode`, `NewBridges`, `Report`, `ReportResponse`, `MaxReportBytes`, `ReportPort`, Speicherfunktionen aus Aufgabe 4
- Produces:
  ```go
  // internal/datanet
  type State string // "off" | "needs_key" | "starting" | "running" | "error"
  type Status struct{ State State; Err string; Addrs []netip.Addr; Since time.Time }
  func NewManager(newNode func(NodeConfig) (Node, error), upTimeout time.Duration) *Manager
  func (m *Manager) Start(cfg NodeConfig, onUp func(Node)) // kehrt sofort zurück; Wiederholung 10 s … 5 min
  func (m *Manager) Stop()
  func (m *Manager) Status() Status
  func (m *Manager) Node() (Node, bool) // nur bei running
  // internal/web
  func (h *Handler) StartDataNet(ctx context.Context, active bool) // aus main.go, auf beiden Servern; Rückkanal nur active
  func (h *Handler) dataNetServerName() string                    // os.Hostname, klein, [a-z0-9-]
  func (h *Handler) dataNetSettings(ctx context.Context) (enabled bool, authKey string)
  func (h *Handler) dataNetSetAuthKey(ctx context.Context, server, key string) error // verschlüsselt, dataNetKey()
  func (h *Handler) dataNetReportHandler() http.Handler          // POST datanet.ReportPath
  ```
  Handler-Felder: `dataNet *datanet.Manager`, `dataNetBridges *datanet.Bridges`.
  `app_settings`: `datanet.cloud.enabled` (`"true"` = an, sonst aus), `datanet.cloud.authkey.<server>` (verschlüsselt mit `encryptSecret(h.dataNetKey(), …)`, `dataNetKey` = SHA-256 von `"pdh-datanet:"+secret` wie `vpnKey`), `datanet.cloud.node.<server>` = JSON `{"addrs":[…],"hostname":"pdh-<server>","seen":"…"}`.
  Zustandsordner: `PDH_DATANET_DIR`, sonst `config/datanet/cloud`. Knotenname `pdh-<server>`.

- [ ] **Step 1: Failing tests**
  - `TestManagerStartNeverBlocks`: Fabrik liefert `nodetest.Fake{BlockUp: true}`; `Start` kehrt in < 100 ms zurück; `Status().State == "starting"`; nach `upTimeout` (Test: 200 ms) `"error"` mit Text „Zeitgrenze“; `Stop` beendet alles (keine hängende Goroutine, `-race`).
  - `TestManagerNeedsKey`: leerer `Dir`, kein `AuthKey` → `"needs_key"`, Fabrik nie aufgerufen.
  - `TestManagerRunning`: Fake mit `Addrs` → `"running"`, `onUp` genau einmal, `Node()` liefert ihn.
  - `TestStartDataNetOffByDefault` (DB): ohne `datanet.cloud.enabled` → `Status().State == "off"`; `StartDataNet` dauert < 5 s, auch wenn die Fabrik (Test-Feld `h.dataNetNewNode`) blockiert.
  - `TestDataNetReportAPI` (DB, `httptest` gegen `dataNetReportHandler`): ohne/mit falschem Bearer → 401; gesperrter Client → 403; Körper > 64 KiB → 413; GET → 405; gültig → 200 mit `{"interval_seconds":60,"pdh_version":<Version>}`, gespeichertes `addr` ist die IP aus `r.RemoteAddr` (Anfrage mit `RemoteAddr = "100.64.0.9:5555"` und `hostname`-Feld „falsch“ im Körper: `addr` = `100.64.0.9`).
  - `TestDataNetAuthKeyCleared` (DB): nach `onUp` ist `datanet.cloud.authkey.<server>` leer und `datanet.cloud.node.<server>` enthält die Adressen.
- [ ] **Step 2:** `go test ./internal/datanet/ ./internal/web/ -run 'Manager|DataNet(Report|AuthKey)|StartDataNet'` → FAIL.
- [ ] **Step 3: Umsetzen.** `StartDataNet` liest Einstellungen in einer Goroutine (Zeitgrenze 3 s), ruft `Manager.Start`; `onUp`: Knotendaten speichern, nur wenn `active` den Schlüssel löschen und `node.Listen("tcp", ":8443")` mit `http.Server{Handler: dataNetReportHandler(), ReadHeaderTimeout: 10 s}` bedienen; `h.dataNetBridges = datanet.NewBridges(node)`. In `cmd/server/main.go` **außerhalb** des Blocks `if !mirrorOn` aufrufen: `webHandler.StartDataNet(context.Background(), !mirrorOn)` (Spiegel: Knoten online, aber kein Rückkanal und keine Schreibzugriffe). Fehler nur ins Protokoll, nie `Fatal`.
- [ ] **Step 4:** `go test -race ./internal/datanet/ && PDH_TEST_DSN=… go test ./internal/web/ -run 'DataNet|StartDataNet'` → PASS.
- [ ] **Step 5: Commit** – `Datennetz: Knoten des PDH im Hintergrund und Rückkanal für Meldungen`

---

### Task 6: Weg `route=net:<Client-ID>` mit lokaler Brücke für alle Treiber

**Files:**
- Create: `internal/web/conn_net.go`, `internal/web/conn_net_test.go`
- Modify: `internal/web/conn_route.go` (`connRoute`, `parseConnRoute`, `connQueryTimeout`, `connPollInterval`, `connRouteReady`, `MssqlFingerprintWeb`), `internal/web/sql_preview.go` (`openMysqlDB`, `mssqlParse`), `internal/web/connection_checks.go` (`checkMssqlNetwork`), `internal/web/import_export.go` (erlaubte Felder MySQL: `route`, `vpn_poll_minutes`, `vpn_timeout_seconds`; `normalizeMssqlRouteFields`; Formulardaten), `web/templates/widgets/connection_list.gohtml`, `web/templates/import.gohtml`, `web/templates/export.gohtml`, `internal/web/connection_detail.go`
- Test: `internal/web/conn_route_test.go` (erweitern)

**Interfaces:**
- Consumes: `dataNetGet`, `dataNetState`, `Manager.Status`, `Bridges.Addr`, `Bridges.CloseHost`
- Produces:
  ```go
  type connRoute struct{ ViaVPN bool; PeerID string; ViaNet bool; ClientID string }
  func (r connRoute) Remote() bool // ViaVPN || ViaNet: Zeitgrenze und Takt wie heute beim VPN-Weg (vpn_*-Felder)
  // conn_net.go – Paketvariable, gesetzt von StartDataNet, in Tests direkt
  var connNetResolve func(ctx context.Context, clientID, port string) (bridgeAddr string, err error)
  func connDialTarget(cfg map[string]string, defPort string) (host, port, tlsName string, err error)
     // direkt/VPN: host, port aus cfg, tlsName = host; Datennetz: "127.0.0.1", Brückenport, tlsName = cfg["host"]
  func (h *Handler) dataNetRouteReady(ctx context.Context, clientID, port string) (bool, string)
  func (h *Handler) connNetOptions(ctx context.Context) []connTunnelOption // nicht gesperrte Clients, Name „Datennetz: <Name>“
  ```
  Gründe (Anzeige): „Datennetz ist ausgeschaltet“, „Datennetz nicht verbunden“, „Client nicht gefunden“, „Client gesperrt“, „Client offline (letzter Kontakt …)“, „Port <n> bietet der Client nicht an“, „Datenbank am Client nicht erreichbar“.

- [ ] **Step 1: Failing tests** (in `conn_route_test.go` bzw. `conn_net_test.go`)
  - `TestParseConnRouteNet`: `net:abc` → `ViaNet`, `ClientID "abc"`; `net:` → direkt; `vpn:x` unverändert; `connQueryTimeout` für `net:` = `vpn_timeout_seconds` (Vorgabe 60 s).
  - `TestConnDialTargetNet`: `connNetResolve` als Attrappe → `"127.0.0.1:40000"`; `connDialTarget({"route":"net:c1","host":"sql01","port":"1433"}, "1433")` → `127.0.0.1`, `40000`, `tlsName "sql01"`; Weg direkt → `sql01`, `1433`.
  - `TestMssqlParseNetKeepsCertName`: `mssqlParse` mit Weg Datennetz und `use_tls=true` → `p.Host == "127.0.0.1"`, `p.Port == 40000`, `p.HostInCertificateProvided == true` und `p.TLSConfig.ServerName == "sql01"`; mit `instance_name` und leerem Port → Fehler „Beim Weg Datennetz bitte den Port eintragen (benannte Instanz geht nicht)“.
  - `TestMysqlNetTLSServerName`: Hilfsfunktion `mysqlConfig(config)` (aus `openMysqlDB` herausgezogen) liefert `Addr "127.0.0.1:40000"` und `TLS.ServerName == "sql01"` bei `use_tls=true`.
  - `TestNormalizeRouteNet`: `normalizeMssqlRouteFields` behält `net:<id>`; MySQL-Felder nehmen `route` an.
  - `TestDataNetRouteReady` (DB): je Grund oben ein Fall (Manager-Attrappe `off`/`starting`, Client fehlt, gesperrt, letzte Meldung vor 200 s, Port 3306 nicht in den gemeldeten Zielen, Ziel `OK=false`) mit genau dem Text; alles gut → `true`.
  - `TestRevokeClosesBridges`: Bridges mit Fake, offene Verbindung über die Brücke; `dataNetRevoke` über den Handler-Weg (Aufgabe 7 ruft dieselbe Funktion `h.dataNetRevokeAndCut`) trennt sie. Hier definieren: `func (h *Handler) dataNetRevokeAndCut(ctx, id, userID string) error` = `dataNetRevoke` + `Bridges.CloseHost(addr)`; ebenso `dataNetDeleteAndCut`.
- [ ] **Step 2:** `PDH_TEST_DSN=… go test ./internal/web/ -run 'ConnRoute|ConnDialTarget|MssqlParseNet|MysqlNet|NormalizeRoute|DataNetRouteReady|RevokeCloses'` → FAIL.
- [ ] **Step 3: Umsetzen.**
  - `connRouteReady`: bei `ViaNet` → `dataNetRouteReady(ctx, ClientID, port aus cfg)`; keine Adressprüfung des VPN-Wegs (Entscheidung 3).
  - `connNetResolve` (gesetzt in `StartDataNet`): Client laden, `dataNetRouteReady` prüfen, `Bridges.Addr(addr+":"+port)`.
  - `mssqlParse` und `openMysqlDB` nehmen Host/Port aus `connDialTarget`; SQL Server: `hostnameincertificate=<tlsName>` in die DSN, wenn `encrypt=true` und Weg Datennetz (Fingerabdruck-Prüfung bleibt wie sie ist); MySQL: `cfg.TLS = &tls.Config{ServerName: tlsName}` statt `"true"`.
  - `checkMssqlNetwork`: bei Datennetz statt „Tunnel“ eine Zeile „Datennetz“ (`dataNetRouteReady`), DNS-Lookup und Absenderadresse entfallen; Port-Prüfung läuft gegen die Brücke.
  - Formular: Auswahl „Weg“ bekommt eine Gruppe `<optgroup label="Datennetz">` mit `net:<id>`; dieselben Weg-Felder auch für MySQL (eigenes kleines Template `route-select` ohne Fingerabdruck, von `mssql-route-fields` mitbenutzt); `mssqlEnsureRoute` erkennt `net:` für „Client nicht mehr vorhanden“. Formular-Datenfeld `NetClients` neben `Tunnels`, nur befüllt, wenn das Datennetz eingeschaltet ist.
  - `connection_detail.go`: vpn-Felder auch bei `ViaNet` zeigen, Weg als „Datennetz: <Name>“.
- [ ] **Step 4:** `PDH_TEST_DSN=… go test ./internal/web/` → PASS (ganzes Paket, bestehende Tests der Wege bleiben grün).
- [ ] **Step 5: Commit** – `Datennetz: Weg „Datennetz“ an Verbindungen über lokale Brücke`

---

### Task 7: Oberfläche „Datennetz“ – Übersicht, Anlegen, Freigabe-Zeile, Sperren, Entfernen, Downloads

**Files:**
- Create: `internal/web/datanet_pages.go`, `web/templates/datanet.gohtml`
- Modify: `internal/web/handler.go` (Routen, `BaseData.CanDataNet`), `internal/web/nav.go` (Eintrag in Gruppe `data`, Symbol `ti-topology-star-3`), `internal/web/perm_gate.go` (`gateRules`, `areaAdminPerm[dataNetRef]`), `web/static/…` nur falls nötig (CSS über Variablen)
- Test: `internal/web/datanet_pages_test.go`

**Interfaces:**
- Consumes: alles aus Aufgaben 4–6, Installationsbefehl aus Aufgabe 3
- Produces – Routen:
  | Methode/Pfad | Recht |
  |---|---|
  | `GET /datanet` | `datanet.view` |
  | `POST /datanet/clients` (name) | `datanet.edit` |
  | `POST /datanet/clients/{id}/revoke` | `datanet.edit` |
  | `POST /datanet/clients/{id}/delete` | `datanet.admin` (gateRule) |
  | `POST /datanet/settings` (enabled, authkey je Server) | `datanet.admin` |
  | `GET /datanet/download/{file}` | `datanet.edit`, nur die zwei Dateinamen aus Aufgabe 1 |
  ```go
  func dataNetACL(ports []int) string      // JSON-Ausschnitt für die Tailnet-Richtlinie
  func dataNetInstallCmd(goos, id, token string, pdhAddrs []string, ports []int) string
  func dataNetClientDir() string           // PDH_DATA_CLIENT_DIR, sonst Ordner der laufenden Programmdatei
  ```
  `dataNetACL([]int{1433,3306})` ergibt genau:
  ```json
  "tagOwners": { "tag:pdh": ["autogroup:admin"], "tag:pdh-data": ["autogroup:admin"] },
  "acls": [
    { "action": "accept", "src": ["tag:pdh"], "dst": ["tag:pdh-data:1433,3306"] },
    { "action": "accept", "src": ["tag:pdh-data"], "dst": ["tag:pdh:8443"] }
  ]
  ```
  Installationsbefehl Windows (Eingabeaufforderung als Administrator) bzw. Linux (`sudo`): `pdh-data-client install --id <id> --token <token> --pdh <ip1>,<ip2> --target 1433 --authkey <EINMAL-SCHLÜSSEL>`; Platzhalter `<EINMAL-SCHLÜSSEL>` bleibt stehen, darunter der Hinweis: „Im Tailscale-Admin unter Settings → Keys einen Einmal-Schlüssel erzeugen: *Reusable* aus, *Pre-approved* an, Tag `tag:pdh-data`.“

- [ ] **Step 1: Failing tests**
  - `TestDataNetPagesRequirePermission`: jede Route ohne passendes Recht → 403 (Muster `TestSyncPagesRequirePermission`); `delete` mit nur `datanet.edit` → 403.
  - `TestDataNetCreateShowsTokenOnce` (DB): POST `/datanet/clients` → Antwort enthält Token und Installationsbefehl beider Systeme; danach zeigt `GET /datanet` den Token nicht mehr (nur Name, Zustand „noch keine Meldung“).
  - `TestDataNetACL`: genaue Zeichenfolge oben; leere Portliste → Platzhalter `tag:pdh-data:<PORT>`; Ports sortiert und ohne Doppelte.
  - `TestDataNetInstallCmd`: Windows-Befehl beginnt mit `pdh-data-client.exe install`, Linux-Befehl mit `sudo ./pdh-data-client-linux-amd64 install`; beide enthält `--pdh 100.64.0.1,100.64.0.2` (Knoten beider Server aus `datanet.cloud.node.*`); ohne bekannten PDH-Knoten erscheint statt des Befehls „Erst das Datennetz des PDH einschalten“.
  - `TestDataNetDownload`: unbekannter Dateiname → 404 (`../etc/passwd`, `pdh`); bekannter → Datei mit `Content-Disposition: attachment`; Seite zeigt SHA-256 jeder vorhandenen Datei.
  - `TestDataNetSettingsKeyEncrypted` (DB): gespeicherter Wert in `app_settings` beginnt nicht mit `tskey-`.
  - `TestDataNetPageRenders`: Seite rendert mit leerer Liste und mit je einem Client pro Zustand (`ok`, `offline`, `db_down`, `revoked`, `new`), Version abweichend vom PDH wird mit „andere Version als das PDH“ markiert.
- [ ] **Step 2:** `PDH_TEST_DSN=… go test ./internal/web/ -run 'DataNet(Pages|Create|ACL|InstallCmd|Download|SettingsKey|PageRenders)'` → FAIL.
- [ ] **Step 3: Umsetzen.** Seite mit drei Karten: „Datennetz des PDH“ (Schalter an/aus, Zustand je Server aus `Manager.Status` und `datanet.cloud.node.*`, Feld Einmal-Schlüssel je Server, Hinweis zu tailscale.com: Gerätenamen liegen dort, Inhalte nicht), „Freigabe im Tailnet“ (ACL-Ausschnitt mit Kopierknopf), „Daten-Clients“ (Tabelle Name, Rechner, Version, letzter Kontakt, Ziele mit Zustand, Knöpfe Sperren/Entfernen mit `hx-confirm`; nach dem Sperren Hinweis „Gerät im Tailscale-Admin entfernen“). Sperren/Entfernen rufen `dataNetRevokeAndCut`/`dataNetDeleteAndCut`. `BaseData.Page = "datanet"`. Beide Stile, hell/dunkel prüfen; Tabelle auf 390 px als Karten.
- [ ] **Step 4:** `PDH_TEST_DSN=… go test ./internal/web/` → PASS (inkl. `TestHelpCoversAllPages` – schlägt fehl, bis Aufgabe 8 das Kapitel liefert; deshalb das Kapitel-Gerüst `<section class="hb-ch" id="datennetz" data-title="Datennetz" data-pages="datanet">` mit einem Satz schon hier anlegen und `"datennetz"` in `helpChapterIDs` hinter `"vpn"` eintragen).
- [ ] **Step 5: Commit** – `Datennetz: Seite mit Clients, Freigabe-Zeile, Sperren und Downloads`

---

### Task 8: Handbuch, Änderungsprotokoll, Version und Prüfung vor dem Merge

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `datennetz` ausbauen; Kapitel `connections`: Weg „Datennetz“; Kapitel `admin`: neue Rechte nennen), `web/templates/widgets/help_figures.gohtml` (Skizze PDH ↔ Tailnet ↔ Client), `CHANGELOG.md`, `VERSION`

**Interfaces:** keine neuen.

- [ ] **Step 1: Handbuch schreiben** (Anwendersprache, `.hb-howto`/`.hb-steps`): „So schaltest du das Datennetz ein“ (Einmal-Schlüssel mit `tag:pdh` je Server, Freigabe-Zeile eintragen), „So richtest du einen Daten-Client ein“ (Datei laden, Prüfsumme, Befehl als Administrator, Windows-SmartScreen-Hinweis: Datei ist nicht codesigniert, „Weitere Informationen → Trotzdem ausführen“), „So stellst du eine Verbindung auf den Weg Datennetz um“ (Port statt Instanzname), „Sperren und Entfernen“, Störungstabelle mit den Gründen aus Aufgabe 6. Hinweis: Updates des Clients folgen in einer späteren Version (bis dahin neue Datei installieren: `uninstall`, dann `install`).
- [ ] **Step 2: Version** – `VERSION` = `0.80.7`; in `CHANGELOG.md` oben `## [0.80.7] – 2026-11-14` mit `### Neu` (Datennetz, Daten-Client, Weg Datennetz, Seite) und `### Sicherheit` (neue Rechte und wer sie bekommt; Token nur gehasht; nur feste Ports; Sperren trennt sofort).
- [ ] **Step 3: Prüfung vor dem Merge (AGENTS.md)**
  ```bash
  git fetch origin main && git merge origin/main
  git show origin/main:VERSION; ls migrations | tail -3
  git diff origin/main...HEAD --stat
  grep -rn "datanet\|/datanet\|8443" internal web cmd --include=*.go | grep -v _test | head
  ```
  Erwartet: `VERSION` über main (sonst nächste Korrekturversion, Eintrag nach oben), Migration 160 nicht doppelt (sonst umnummerieren), keine Dopplung von Routen/Rechten. Befunde gleich beheben.
- [ ] **Step 4: Gesamtprüfung**
  ```bash
  PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...
  go vet ./... && make build
  ```
  Erwartet: alles PASS (insb. `TestChangelogMatchesVersion`, `TestHelpCoversAllPages`, `TestHelpChaptersMatch`), `bin/pdh-data-client-*` vorhanden. Browser: `/datanet` und Verbindungsformular in Fiori/Fluent, hell/dunkel, 390 und 1366 px.
- [ ] **Step 5: Commit** – `Datennetz: Handbuch, Änderungsprotokoll und Version 0.80.7`

---

### Task 9: Abnahme mit echtem Tailnet (gemeinsam mit dem Anwender)

Nicht automatisierbar; der Ausführende bereitet vor und führt den Anwender durch. Kein Merge vor bestandener Abnahme.

- [ ] **Step 1:** Anwender trägt die Freigabe-Zeile aus `/datanet` in die Tailnet-Richtlinie ein und erzeugt zwei Einmal-Schlüssel (Tag `tag:pdh` für den Testserver, `tag:pdh-data` für den Client).
- [ ] **Step 2:** Testserver mit Version 0.80.7: Datennetz einschalten, Schlüssel eintragen. Erwartet: Zustand „verbunden“ mit 100.x-Adresse binnen 60 s; Schlüsselfeld danach leer; PDH-Neustart ohne Internet ist in < 5 s bereit (Zustand „Fehler“/„startet“, Rest läuft).
- [ ] **Step 3:** Client auf einem Windows-Rechner mit SQL Server (Port 1433) und einem Linux-Rechner mit MySQL (3306) per Installationsbefehl einrichten. Erwartet: beide Dienste laufen (`sc query pdh-data-client` bzw. `systemctl status pdh-data-client`: running); in `/datanet` binnen 60 s Version und „Datenbank erreichbar“.
- [ ] **Step 4:** Je eine Verbindung auf „Datennetz: <Client>“ stellen, „Verbindung prüfen“ und einen Abgleich-Test laufen lassen. Erwartet: Anmeldung klappt, SQL-Server-Fingerabdruck stimmt wie beim direkten Weg.
- [ ] **Step 5:** Gegenproben: anderer Port des Datenbankservers über das Datennetz nicht erreichbar; Datenbank anhalten → „Datenbank am Client nicht erreichbar“ binnen 2 Takten; Client sperren → laufende Abfrage bricht ab, Verbindung meldet „Client gesperrt“; Client-Dienst anhalten → nach 3 Minuten „offline“.
- [ ] **Step 6:** Ergebnis im Pull-Request-Text festhalten (zusammen mit dem Ergebnis der Prüfung aus Aufgabe 8, Step 3).
