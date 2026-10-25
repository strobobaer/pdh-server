# Ausfallschutz Teil 2 – pdh-ha-Kern (Beobachtungsmodus): Implementierungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein neuer Dienst `pdh-ha` koppelt zwei Server und einen optionalen Zeugen, tauscht über LAN und VPN per mTLS Heartbeats aus und entscheidet Rollen und Amtszeiten nach den Duo-/Trio-Regeln – im Beobachtungsmodus: Aktionen werden nur protokolliert; das PDH zeigt Betriebsart, Knoten, Wege und Ereignisse und alarmiert.

**Architecture:** Die Entscheidungslogik ist eine reine Funktion `ha.Step` (Paket `internal/ha`, simulierte Uhr, keine Ein-/Ausgabe). `cmd/pdh-ha` (systemd, root; Modi `node`/`witness`) liefert ihr Beobachtungen aus dauerhaften TLS-Verbindungen (Port 7480, ALPN `pdh-ha/1`; Beitritt per HTTPS auf demselben Port), speichert Zustand atomar und führt Aktionen über `observeActor` aus (nur Ereignis). Das PDH spricht über den Unix-Socket `/run/pdh-ha/api.sock`; installiert wird `pdh-ha` auf dem ersten Knoten vom Update-Agenten, auf weiteren Knoten von `pdh-ha-join.sh`.

**Tech Stack:** Go 1.24 (nur Standardbibliothek: `crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `net/http`), Bash, systemd, ufw, `ping` (iputils), Docker Compose (nur Testlabor und Zeugen-Container).

**Spec:** `docs/superpowers/specs/2026-10-25-pdh-ha-kern-design.md` (Gesamt: `2026-10-24-ausfallschutz-design.md`)

## Global Constraints

- Beobachtungsmodus: Keine Aktion verändert das System außer `pdh-ha`s eigenen Dateien und ufw-Regeln für Port 7480. Jede Aktion erzeugt das Ereignis `würde jetzt: <Aktion>` (Art `action`).
- Rollen (Text, so auch in JSON/Anzeige): `aktiv`, `passiv`, `abgeschaltet`, `wiedereingliederung`, `zeuge`. „Wartung“ (Automatik aus) ist ein clusterweites Flag, keine Rolle; neu eingerichtete Cluster starten mit Wartung an.
- Betriebsart folgt aus der Knotenliste: 1 Knoten = `einzel`, 2 Knoten = `duo`, 2 Knoten + Zeuge = `trio`. Ein Zeuge ohne zweiten Knoten wird abgelehnt: „Zuerst den zweiten Server hinzufügen“.
- Standardzeiten (alle einstellbar): Takt 1 s (1–5), Selbst-Abschaltung 10 s (3–300), Übernahme Trio 30 s (10–600), Übernahme Duo 60 s (10–600), Wartezeit nach Umschaltung 15 min (0–1440), Umschaltungen je Tag 3 (1–20). Abgelehnt, wenn eine Übernahmezeit < Selbst-Abschaltung + 2 × Takt + 5 s, Meldung: „Übernahmezeit muss mindestens Selbst-Abschaltung + 2 × Takt + 5 s sein“.
- Weg lebendig = Nachricht in den letzten 3 Takten; Gegenstelle gesehen = mindestens ein Weg lebendig. Entscheidungen nur nach monotoner Uhr; Wanduhr nur für Anzeige, Tageszähler und Wartezeit.
- Port 7480/tcp. ALPN `pdh-ha/1` = Heartbeat-Strom (Client-Zertifikat Pflicht), `http/1.1` = nur `POST /v1/join` (ohne Client-Zertifikat). TLS 1.3.
- Zertifikate: CA ECDSA P-256, 10 Jahre, CN `PDH-HA CA <cluster-id>`; Knoten/Zeuge ECDSA P-256, 2 Jahre, CN = Kennung, OU = `node` | `witness`; Erneuerung ab 30 Tagen Restlaufzeit.
- Kennung: Hostname auf `[a-z0-9-]` reduziert, 1–32 Zeichen (Muster `drTarget` in `internal/web/dr.go`).
- Beitrittscode: 32 Byte Geheimnis, 30 min gültig, einmalig, höchstens 5 Fehlversuche je Minute und Quelladresse.
- Pfade: `/etc/pdh/ha/` (0700: `node.json`, `cluster.json`, `ca.crt`, `ca.key` nur auf dem CA-Knoten, `node.crt`, `node.key`), `/var/lib/pdh-ha/state.json`, `/var/lib/pdh-ha/events.jsonl` (Rotation 5 MiB, 3 Dateien), Socket `/run/pdh-ha/api.sock` (0660, Gruppe `pdh`). Überschreibbar über `PDH_HA_ETC`, `PDH_HA_STATE`, `PDH_HA_SOCKET` (Tests, Labor). Das PDH und der Update-Agent lesen `PDH_HA_SOCKET` ebenfalls.
- In Teil 2 ist `data_current` immer `true` mit `data_assumed: true`; Ereignisse zu Übernahmen enthalten „(Datenstand angenommen, keine Replikation)“.
- Alarme tragen in Teil 2 den Zusatz „(Beobachtung)“; je Ereignisart höchstens ein Alarm je 15 min.
- Produktion = systemd. Docker nur für Testlabor und optionalen Zeugen-Container.
- Texte Deutsch, Anwendersprache; Handbuch und `CHANGELOG.md`/`VERSION` laut AGENTS.md (neue Nebenversion 0.75.0).

## Review Focus

1. Knoten startet nach Stromausfall neu, Partner noch nicht da → nie sofort `aktiv` (außer Einzel), sondern nach Spec 5.4; im Duo nach Übernahmezeit mit Gateway wieder `aktiv`. (Task 3 `TestStepBoot`)
2. Absturz zwischen „Stimme entschieden“ und „gespeichert“ → nach Neustart keine zweite Stimme für dieselbe Amtszeit; Speichern vor dem Senden. (Task 6 `TestWitnessPersistsBeforeReply`, `TestDaemonPersistsBeforeSend`)
3. Beitrittscode abgefangen, erraten oder per Man-in-the-Middle umgeleitet → einmalig, Fehlversuchsgrenze, neuer Knoten prüft den CA-Fingerabdruck und bricht ab. (Task 4 `TestJoinCodesRedeem`, Task 5 `TestJoinFlow`)
4. Verbindung offen, Gegenstelle sendet nichts mehr (hängender Prozess) → Weg nach 3 Takten tot, Verbindung wird abgebrochen und neu aufgebaut. (Task 5 `TestTransportStalledPeer`)
5. `pdh-ha` im Duo/Trio nicht erreichbar → Update-Agent sichert nicht „auf Verdacht“, sondern meldet „pdh-ha nicht erreichbar – Sicherung pausiert“; ohne `pdh-ha` (Socket-Datei fehlt) läuft die Sicherung wie bisher. (Task 7 `TestDRIsActive`)

---

## Dateiübersicht

| Datei | Aufgabe |
|---|---|
| `internal/ha/types.go` | Rollen, Einstellungen, Ein-/Ausgabe-Typen, `DefaultSettings`, `Settings.Validate` |
| `internal/ha/step.go` | `Step` – Entscheidungen eines Knotens, `Allowed` |
| `internal/ha/witness.go` | `Vote` – Stimmvergabe des Zeugen |
| `internal/ha/*_test.go` | Tabellentests, Simulation, Fuzz |
| `cmd/pdh-ha/main.go` | Befehle `run`, `setup`, `join-code`, `join`, `status`, `version` |
| `cmd/pdh-ha/store.go` | `node.json`, `cluster.json`, `state.json` (atomar), Ereignisprotokoll |
| `cmd/pdh-ha/pki.go` | CA, Zertifikat aus CSR, Prüfung, Fingerabdruck |
| `cmd/pdh-ha/joincode.go` | Code kodieren/prüfen, Einmal-Ablage, Fehlversuchsgrenze |
| `cmd/pdh-ha/transport.go` | Listener/Dialer, ALPN-Weiche, Zeilenprotokoll, Wege, Join-Endpunkt |
| `cmd/pdh-ha/daemon.go` | Takt-Schleife Knoten/Zeuge, `observeActor`, Cluster-Verteilung, Erneuerung |
| `cmd/pdh-ha/system.go` | Gateway, Ping, Host-Kennung, ufw-Regeln |
| `cmd/pdh-ha/api.go` | Unix-Socket-API |
| `scripts/install-pdh-ha.sh` (neu) | bauen, installieren, Unit – von Agent, Update und Join-Skript genutzt |
| `scripts/pdh-ha-join.sh` (neu) | frischer Ubuntu-Server/Zeuge: Pakete, Repo auf Commit, Install, Beitritt; `--update` |
| `install_update.sh` | `pdh-ha` beim Update mitbauen, falls installiert |
| `cmd/update-agent/ha.go` (neu), `main.go`, `dr.go` | `/v1/ha/install`, Sicherung nur auf aktivem Knoten |
| `deploy/ha-witness/` (neu) | Zeugen-Container |
| `deploy/ha-lab/` (neu) | Testlabor |
| `internal/web/ha.go`, `internal/web/ha_test.go`, `web/templates/widgets/ha.gohtml` (neu) | Socket-Client, Ansicht, Handler, Alarme |
| `web/templates/widgets/dr.gohtml`, `internal/web/server_config.go`, `internal/web/handler.go` | Einbindung in den Reiter „Ausfallschutz“ |
| `web/templates/help.gohtml`, `internal/web/help.go`, `web/templates/widgets/help_figures.gohtml` | Kapitel `spiegelbetrieb` |
| `CHANGELOG.md`, `VERSION` | 0.75.0 |

---

### Task 1: `internal/ha` – Typen und Einstellungen

**Files:**
- Create: `internal/ha/types.go`, `internal/ha/types_test.go`

**Interfaces:**
- Produces:
  ```go
  type Role string
  const (Active Role = "aktiv"; Passive Role = "passiv"; Fenced Role = "abgeschaltet"; Rejoin Role = "wiedereingliederung"; Witness Role = "zeuge")
  type Mode string
  const (Single Mode = "einzel"; Duo Mode = "duo"; Trio Mode = "trio")
  type Settings struct {
      Tick, FenceAfter, TakeoverTrio, TakeoverDuo, Cooldown time.Duration
      MaxPerDay int
  } // JSON: tick_s, fence_s, takeover_trio_s, takeover_duo_s, cooldown_min, max_per_day (eigene Marshal/Unmarshal)
  func DefaultSettings() Settings
  func (s Settings) Validate() error
  func (s Settings) Takeover(m Mode) time.Duration
  type PeerObs struct {
      ID, Kind string          // Kind "node" | "witness"
      Seen     bool            // vom Daemon berechnet (Global Constraints)
      LastSeen time.Duration   // monoton; nie gesehen seit Start = 0
      Role Role; Term uint64; Version string
      Gateway, DataCurrent, Maint bool; MaintSeq uint64
  }
  type Persisted struct {
      Term uint64; Role Role
      LastSwitch time.Time; SwitchDay string; SwitchCount int // Wanduhr, Tag "2006-01-02"
      Maint bool; MaintSeq uint64
      PartnerVersion string
      VotedTerm uint64; VotedFor string // nur Zeuge
  } // JSON snake_case
  type ActionKind string // "promote","demote","fence","unfence","vip_take","vip_release","request_vote","handover","rejoin"
  type Action struct { Kind ActionKind; Term uint64 }
  type Event struct { Kind, Text string } // Kind "role","action","alarm","info"
  type CommandKind string // "", "swap","force_takeover","maint_on","maint_off"
  type Input struct {
      Now time.Duration; Wall time.Time; Mode Mode; Version string
      Gateway, DataCurrent, DataAssumed, Booting bool
      Partner, Witness *PeerObs
      VoteGranted uint64 // >0: Zeuge stimmt dieser Amtszeit zu
      Handover uint64    // >0: aktiver Partner übergibt diese Amtszeit
      Command CommandKind
      Settings Settings; Persist Persisted
      HoldUntil time.Duration; VotePending uint64; VoteAskedAt time.Duration // aus vorigem Output
  }
  type Output struct {
      Persist Persisted; Actions []Action; Events []Event
      HoldUntil time.Duration; VotePending uint64; VoteAskedAt time.Duration
  }
  ```

- [ ] **Step 1: Tests** – `TestDefaultSettings` (Werte aus Global Constraints, `Validate()==nil`). `TestSettingsValidate` (Tabelle): Takt 0 s und 6 s, Fence 2 s, MaxPerDay 0 → Fehler; TakeoverTrio 16 s bei Fence 10 s/Takt 1 s → Fehler mit genau der Meldung aus Global Constraints; 17 s → ok. `TestSettingsJSON`: Rundreise `{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3}`.
- [ ] **Step 2:** `go test ./internal/ha/` → FAIL (undefiniert).
- [ ] **Step 3:** `types.go` implementieren.
- [ ] **Step 4:** `go test ./internal/ha/` → PASS.
- [ ] **Step 5:** Commit `feat(ha): Typen und Einstellungen des Entscheidungskerns`.

### Task 2: `internal/ha` – Grundregeln und Zeugenstimme

**Files:**
- Create: `internal/ha/step.go`, `internal/ha/witness.go`, `internal/ha/step_test.go`, `internal/ha/witness_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces: `func Step(in Input) Output`; `type VoteRequest struct{ From string; Term uint64 }`; `func Vote(p Persisted, req VoteRequest, nodes []PeerObs, now time.Duration, s Settings) (granted bool, next Persisted)`.

Regeln (Spec 5.1, 5.2, 5.3 Punkte 1–2). Hilfsfunktion `absent(p *PeerObs, now, d time.Duration) bool` = `p == nil || (!p.Seen && now-p.LastSeen >= d)`:
- `Single`: Rolle `aktiv`, keine Aktionen.
- Partner oder Zeuge gesehen mit höherer Amtszeit → `Term` übernehmen; war `aktiv`/`abgeschaltet` → `wiedereingliederung` + `rejoin`, im nächsten Schritt `passiv`.
- Selbst und Partner `aktiv` mit gleicher Amtszeit → `Maint=true`, `MaintSeq++`, Alarm „Zwei aktive Server mit gleicher Amtszeit – Automatik aus“.
- Trio `aktiv`: `absent(Partner, FenceAfter) && absent(Witness, FenceAfter)` → `abgeschaltet` + `fence`, `vip_release`. Duo `aktiv`: `absent(Partner, FenceAfter) && !Gateway` (Gateway-Flag kommt entprellt vom Daemon) → dito.
- `abgeschaltet` → `aktiv` + `unfence`, `vip_take`, wenn keine höhere Amtszeit gesehen und (Trio: Partner oder Zeuge gesehen; Duo: Partner gesehen oder Gateway).
- Trio `passiv`: `absent(Partner, TakeoverTrio)` → `request_vote{Term+1}`, `VotePending=Term+1`, `VoteAskedAt=Now`; Wiederholung frühestens nach 5 Takten. `VoteGranted == VotePending` → `Term=VotePending`, `aktiv`, `promote`, `vip_take`, `LastSwitch=Wall`, Zähler++; bei `DataAssumed` hängt das Rollen-Ereignis „(Datenstand angenommen, keine Replikation)“ an.
- Duo `passiv`: `absent(Partner, TakeoverDuo) && Gateway` → wie oben ohne Stimme.
- `Vote`: gewährt nur wenn `req.Term > p.VotedTerm` und kein Knoten außer `req.From` mit Rolle `aktiv` gesehen oder vor weniger als `TakeoverTrio` zuletzt gesehen; dann `VotedTerm=req.Term`, `VotedFor=req.From`.

- [ ] **Step 1: Tests** – `TestStepTable` (je Zeile Input → Rolle, Amtszeit, Aktionen): Einzel; Trio aktiv sieht nur Zeugen → bleibt; Trio aktiv sieht niemanden 9 s → bleibt, 10 s → `abgeschaltet` + `fence`,`vip_release`; Trio passiv Partner 29 s weg → nichts, 30 s → `request_vote{2}`; Wiederholung nach 2 Takten → keine, nach 5 Takten → erneut; mit `VoteGranted=2` → `aktiv`, Term 2, `promote`; Duo aktiv ohne Partner, Gateway ok, 120 s → bleibt; ohne Gateway 10 s → `abgeschaltet`; Duo passiv 60 s, Gateway ok → `aktiv` Term 2; ohne Gateway → `passiv`; aktiv sieht höhere Amtszeit → `wiedereingliederung` + `rejoin`, nächster Schritt `passiv`; doppelt aktiv gleiche Amtszeit → `Maint=true` + Alarm; Trio `abgeschaltet` sieht Zeugen wieder → `aktiv` + `unfence`. `TestVote`: gewährt; zweite Bitte gleiche Amtszeit anderer Knoten → abgelehnt; aktiver Knoten vor 5 s gesehen → abgelehnt; niedrigere Amtszeit → abgelehnt.
- [ ] **Step 2:** `go test ./internal/ha/` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./internal/ha/` → PASS.
- [ ] **Step 5:** Commit `feat(ha): Abschalten, Übernahme, Amtszeit und Zeugenstimme`.

### Task 3: `internal/ha` – Bremsen, Wartung, Handbefehle, Neustart, Simulation

**Files:**
- Modify: `internal/ha/step.go`, `internal/ha/step_test.go`
- Create: `internal/ha/sim_test.go`

**Interfaces:**
- Produces zusätzlich: `func Allowed(in Input) (ok bool, reason string)` (für die Anzeige „Übernahme gesperrt: …“).

Regeln (Spec 5.3, 5.4):
- Automatische Übernahme und Abschaltung nur bei `!Maint`. Übernahme zusätzlich nur wenn `PartnerVersion == Version`, `Wall-LastSwitch >= Cooldown`, `DataCurrent`, Zähler (bei neuem `SwitchDay` 0) `< MaxPerDay`. Scheitert sie nur am Zähler → `Maint=true`, Alarm „Zu viele Umschaltungen heute – Automatik aus“. `Allowed`-Gründe in dieser Reihenfolge: „Wartung“, „Versionen unterschiedlich“, „Wartezeit nach Umschaltung“, „Datenstand nicht aktuell“, „Tageshöchstzahl erreicht“.
- `maint_on`/`maint_off` → `Maint` setzen, `MaintSeq++`; gesehene Gegenstelle mit höherem `MaintSeq` → deren `Maint`/`MaintSeq` übernehmen.
- `swap` (nur `aktiv`, Partner gesehen und `passiv`): `passiv` + `demote`, `vip_release`, `handover{Term+1}`. Partner mit `Handover == Term+1` → `Term=Handover`, `aktiv`, `promote`, `vip_take`; zählt nicht, `LastSwitch` unverändert, auch bei Wartung.
- `force_takeover` (nur nicht `aktiv`): `Term = max(eigene, Partner, Zeuge)+1`, `aktiv`, `promote`, `vip_take`, Alarm „Übernahme erzwungen“.
- `Booting` mit `Persist.Role == Active` und Mode ≠ Single → `abgeschaltet`, `HoldUntil = Now + Settings.Takeover(Mode)`. Vor `HoldUntil` nur „Partner gesehen mit Term ≤ eigener“ → `aktiv` + `unfence`. Ab `HoldUntil`: Trio → `aktiv`, wenn Zeuge gesehen mit Term ≤ eigener; Duo → `aktiv`, wenn `Gateway`; sonst normale Regeln aus Task 2. `Booting` mit `passiv` → `passiv`.
- Gesehener Partner aktualisiert `PartnerVersion`.

- [ ] **Step 1: Tests** – `TestStepBrakes` (Wartung; Version anders; Wartezeit 14 min/15 min; Datenstand false; Zähler 3 → `Maint=true` + Alarm; neuer Tag setzt Zähler zurück; `Allowed`-Texte). `TestStepCommands` (swap beidseitig inkl. Wartung; force_takeover bei eigener 7, Partner 6, Zeuge 7 → Term 8 + Alarm; swap als passiv → ignoriert). `TestStepMaintSpread`. `TestStepBoot` (passiv bleibt; aktiv + Partner gleiche Amtszeit → `aktiv`; aktiv allein Trio: 29 s `abgeschaltet`, 30 s mit Zeuge → `aktiv`, ohne Zeuge → bleibt; Duo 60 s mit Gateway → `aktiv`, ohne → bleibt; Einzel sofort `aktiv`). `TestSimTrio` (`sim_test.go`): zwei `Step`-Knoten und ein `Vote`-Zeuge, Nachrichtenzustellung je Takt, Verbindungsmatrix; Szenarien aktiv weg, Direktleitung weg, Zeuge weg, Rückkehr; nach jedem Takt: nie zwei `aktiv`, jede Amtszeit höchstens einmal `aktiv` geworden. `FuzzSimTrio(seed int64)`: 2000 Takte, zufällige Ausfälle einzelner Verbindungen (1–120 Takte, Verzug ≤ 1 Takt), gleiche Invarianten. `TestSimDuo`: aktiv weg, Gateway-Ausfall beim aktiven.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./internal/ha/ && go test ./internal/ha/ -run '^$' -fuzz FuzzSimTrio -fuzztime 30s` → PASS.
- [ ] **Step 5:** Commit `feat(ha): Bremsen, Wartung, Rollentausch und Neustartregeln`.

### Task 4: `pdh-ha` – Ablage, Zertifikate, Beitrittscode

**Files:**
- Create: `cmd/pdh-ha/store.go`, `cmd/pdh-ha/pki.go`, `cmd/pdh-ha/joincode.go`, `cmd/pdh-ha/store_test.go`, `cmd/pdh-ha/pki_test.go`, `cmd/pdh-ha/joincode_test.go`

**Interfaces:**
- Consumes: `ha.Persisted`, `ha.Settings`, `ha.Mode`, `ha.Event`.
- Produces:
  ```go
  type nodeConfig struct { ID, Kind string; Addrs []string; CAHolder bool }   // node.json, Kind "node"|"witness"
  type clusterNode struct { ID, Kind string; Addrs []string; Serial string }
  type clusterConfig struct {
      ClusterID string; Version uint64; Nodes []clusterNode
      Settings ha.Settings; GatewayTargets []string; Revoked []string // Seriennummern hex
  } // cluster.json, JSON snake_case
  func (c clusterConfig) Mode() ha.Mode
  type store struct { etc, state string }
  func newStore() store
  func (s store) loadNode() (nodeConfig, error);        func (s store) saveNode(nodeConfig) error
  func (s store) loadCluster() (clusterConfig, error);  func (s store) saveCluster(clusterConfig) error
  func (s store) loadState() (ha.Persisted, error);     func (s store) saveState(ha.Persisted) error
  func writeFileAtomic(path string, data []byte, perm os.FileMode) error // temp + fsync + rename + fsync Ordner
  type loggedEvent struct { Seq uint64; At time.Time; Kind, Text string }
  func openEventLog(path string, maxBytes int64, keep int) *eventLog
  func (l *eventLog) add(e ha.Event)
  func (l *eventLog) since(seq uint64, limit int) []loggedEvent
  func newCA(clusterID string) (certPEM, keyPEM []byte, err error)
  func newKeyCSR(id, kind string) (keyPEM, csrPEM []byte, err error)
  func signCSR(caCertPEM, caKeyPEM, csrPEM []byte, kind string, days int) ([]byte, error)
  func fingerprint(certPEM []byte) string // sha256 hex über DER
  func peerIdentity(cs tls.ConnectionState, revoked []string) (id, kind string, err error)
  type joinCode struct { Addrs []string `json:"a"`; FP string `json:"fp"`; Secret string `json:"s"`; Role string `json:"r"`; Commit string `json:"c"`; Cluster string `json:"id"` }
  func (j joinCode) String() string            // base64url(JSON), ohne Padding
  func parseJoinCode(s string) (joinCode, error)
  func (j *joinCodes) issue(role string, now time.Time) string            // Geheimnis; gespeichert nur als sha256, nur im Speicher
  func (j *joinCodes) redeem(secret, srcIP string, now time.Time) (role string, err error)
  ```

- [ ] **Step 1: Tests** – `TestWriteFileAtomic` (Inhalt, Rechte 0600, keine Temp-Reste). `TestStoreRoundTrip` (node/cluster/state in `t.TempDir()` über `PDH_HA_ETC`/`PDH_HA_STATE`). `TestClusterMode` (1 Knoten → einzel; 2 → duo; 2 + Zeuge → trio). `TestEventLogRotate` (maxBytes 200, keep 3 → höchstens 3 Dateien; `since` liefert fortlaufende `Seq`, auch über Rotation). `TestSignCSR`: prüft gegen CA, CN/OU stimmen, Laufzeit 730 Tage ± 1; CN `"../x"` → Fehler. `TestPeerIdentity`: gültig → id/kind; Seriennummer gesperrt → „Zertifikat gesperrt“; fremde CA scheitert im Handshake (`tls.Server`/`tls.Client` über `net.Pipe`). `TestJoinCode`: Rundreise; kaputter Text → Fehler. `TestJoinCodesRedeem`: richtig → Rolle; zweites Mal → „Beitrittscode bereits benutzt“; nach 31 min → „Beitrittscode abgelaufen“; 5 falsche von einer IP, danach der richtige in derselben Minute → „Zu viele Versuche, bitte eine Minute warten“ und der Code bleibt gültig.
- [ ] **Step 2:** `go test ./cmd/pdh-ha/` → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./cmd/pdh-ha/` → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): Ablage, Zertifizierungsstelle und Beitrittscode`.

### Task 5: `pdh-ha` – Transport und Beitritt

**Files:**
- Create: `cmd/pdh-ha/transport.go`, `cmd/pdh-ha/transport_test.go`

**Interfaces:**
- Consumes: Task 4.
- Produces:
  ```go
  type message struct {
      T string `json:"t"` // "hb","vote_req","vote_resp","handover","cluster","renew_req","renew_resp","removed"
      // hb-Felder laut Spec 3, zusätzlich hostid, mseq, cver
      ID, Role, Ver, Commit, PG, PDH, Disk, HostID string
      Term, Seq, MaintSeq, ClusterVer uint64
      DataCurrent, DataAssumed, Gateway, Maint bool; Wall time.Time
      VoteTerm uint64; Granted bool  // vote_req, vote_resp, handover
      Cluster *clusterConfig         // cluster
      CSR, Cert string               // renew_req, renew_resp
  } // JSON-Feldnamen wie Spec 3, Rest snake_case
  type pathStat struct { Addr string; Alive bool; LastRecv time.Duration; Err string }
  type peerView struct { Last message; LastSeen time.Duration; Paths []pathStat }
  type inbound struct { From string; Msg message }
  func newTransport(self nodeConfig, cl clusterConfig, cert tls.Certificate, ca *x509.CertPool, mono func() time.Duration, ev *eventLog) *transport
  func (t *transport) serve(ctx context.Context, ln net.Listener, join http.Handler) // ALPN-Weiche
  func (t *transport) setCluster(cl clusterConfig)
  func (t *transport) broadcast(m message)
  func (t *transport) sendTo(id string, m message)
  func (t *transport) incoming() <-chan inbound
  func (t *transport) peers(tick time.Duration) map[string]peerView
  type joinRequest struct { Secret, ID, CSR string; Addrs []string }
  type joinResponse struct { Cert, CA string; Cluster clusterConfig }
  func joinHandler(st store, codes *joinCodes, onJoin func(clusterConfig)) http.Handler // nur auf dem CA-Knoten
  func joinRemote(ctx context.Context, code joinCode, req joinRequest) (joinResponse, error) // prüft FP
  ```
- Je Weg eine Verbindung; die kleinere Kennung (Stringvergleich) baut auf, der Zeuge baut nie auf. Neuaufbau mit Backoff 1, 2, 5 s. Lesefrist je Zeile = 3 Takte. Zeilen > 64 KiB → Verbindung schließen.

- [ ] **Step 1: Tests** (echtes TLS auf `127.0.0.1:0`) – `TestTransportHeartbeat`: zwei Transporte, zwei Wege (zwei Listener) → beide `Alive`; ein Listener zu → dieser Weg nach 3 Takten tot, Gegenstelle weiter gesehen. `TestTransportStalledPeer`: Gegenstelle hält Verbindung offen und sendet nichts → Weg tot nach 3 Takten, Neuaufbau. `TestTransportRejectsForeign` (fremde CA bzw. gesperrt → keine Nachricht; Ereignis „Verbindung abgelehnt: <Adresse>“ höchstens einmal je Minute je Adresse). `TestJoinFlow`: Code ausstellen, `joinRemote` → Zertifikat, Cluster enthält neuen Knoten, `Version` +1; falscher Fingerabdruck → „Fingerabdruck der Zertifizierungsstelle stimmt nicht“, Code bleibt unbenutzt. `TestJoinWitnessNeedsTwoNodes` (Meldung aus Global Constraints).
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test -race ./cmd/pdh-ha/` → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): mTLS-Heartbeat über mehrere Wege und Beitritt`.

### Task 6: `pdh-ha` – Dienst, System, API, Befehle

**Files:**
- Create: `cmd/pdh-ha/main.go`, `cmd/pdh-ha/daemon.go`, `cmd/pdh-ha/system.go`, `cmd/pdh-ha/api.go`, `cmd/pdh-ha/daemon_test.go`, `cmd/pdh-ha/system_test.go`, `cmd/pdh-ha/api_test.go`

**Interfaces:**
- Consumes: Tasks 1–5.
- Produces (Vertrag für Task 7 und Task 9):
  ```go
  // GET /v1/status
  type apiStatus struct {
      OK bool; Mode ha.Mode; Self apiNode; Peers []apiNode; Maint bool
      Settings ha.Settings; GatewayTargets []string
      Blocked string     // ha.Allowed-Grund, "" = frei
      Observe bool       // in Teil 2 immer true
      Warnings []string  // "eingeschränkter Ausfallschutz (kein Zeuge)", "Zeuge und <id> laufen auf derselben Hardware", "Uhrzeit von <id> weicht um <n> s ab", "Versionen unterschiedlich", "Version überschrieben (Test)"
      CAHolder bool
  }
  type apiNode struct { ID, Kind string; Role ha.Role; Term uint64; Version string; Gateway, Seen bool; LastSeenAgo, ClockSkew float64; Paths []pathStat }
  // GET  /v1/events?since=<seq>&limit=<n> → {"events":[{"seq","at","kind","text"}]}
  // POST /v1/setup {"addrs":[…]} · POST /v1/join-code {"role":"node"|"witness"} → {"code","expires"}
  // POST /v1/maintenance {"on":bool} · POST /v1/command {"cmd":"swap"|"force_takeover"}
  // POST /v1/nodes/remove {"id","gone":bool} · PUT /v1/settings {"settings","gateway_targets"}
  // Fehler: HTTP 400 {"ok":false,"error":"…"}
  ```
- `system.go`: `func defaultGateway(procNetRoute string) (string, error)`; `func ping(ctx context.Context, addr string) bool` (`ping -c1 -W1 -n`); `func hostID() string` (Hyper-V `/var/lib/hyperv/.kvp_pool_3`, Schlüssel `PhysicalHostNameFullyQualified`; sonst bei `systemd-detect-virt` = `none` die `/sys/class/dmi/id/product_uuid`; sonst ""); `func ufwRules(cl clusterConfig, selfID string, joinOpen bool) [][]string` (Argumentlisten mit `comment pdh-ha`); `func applyUFW(ctx context.Context, rules [][]string) error` (nur wenn ufw aktiv; vorhandene `pdh-ha`-Regeln ersetzen).
- `daemon.go`: Takt-Schleife: `transport.peers` → `ha.Input` (Gateway ok, wenn ein Ziel in den letzten 3 Takten antwortete; `DataCurrent=true`, `DataAssumed=true`) → `ha.Step` → **erst `saveState`, dann senden** → Aktionen an `actor` → Ereignisse ins Protokoll. Zeuge: `vote_req` → `ha.Vote` → **erst speichern, dann `vote_resp`**. `type actor interface{ Do(ctx context.Context, a ha.Action) error }`; `observeActor` schreibt „würde jetzt: <Aktion>“. Fehler oder Panic in `Do` → Wartung an + Alarm. Höhere `ClusterVer` im Heartbeat eines Knotens (nie vom Zeugen) → `cluster`-Nachricht anfordern/annehmen, speichern, `setCluster`, ufw anwenden. Restlaufzeit < 30 Tage → `renew_req` an den CA-Knoten. `removed` → zurück auf Einzel, Ereignis „Aus dem Cluster entfernt“. Uhrabweichung > 5 s → Warnung.
- `main.go`: `pdh-ha run`, `pdh-ha setup --addr a,b`, `pdh-ha join [--witness] <Code>`, `pdh-ha join-code [--witness]`, `pdh-ha status [--json]` (über Socket), `pdh-ha version`; Variablen `version`, `commit` per `-ldflags -X`; Flag `--version-override` nur fürs Labor.

- [ ] **Step 1: Tests** – `TestDefaultGateway` (Beispieltext `/proc/net/route` → `10.10.0.1`). `TestHostIDHyperV` (Beispieldatei: Datensätze aus 512-Byte-Schlüssel + 2048-Byte-Wert → Hostname). `TestUFWRules` (Duo: Gegenstelle mit zwei Adressen → zwei `allow from <a> to any port 7480 proto tcp comment pdh-ha`; eigene Adressen nicht; `joinOpen` → zusätzlich `allow 7480/tcp comment pdh-ha-join`). `TestDaemonPersistsBeforeSend` und `TestWitnessPersistsBeforeReply` (Fake-Transport und Fake-Store protokollieren Reihenfolge). `TestObserveActorOnly` (alle Aktionsarten → nur Ereignisse „würde jetzt: …“). `TestDaemonActorPanic` → Wartung an + Alarm. `TestAPI` (Unix-Socket in `t.TempDir()`: status, events since, join-code, maintenance, settings mit ungültiger Zeit → 400 + Meldung aus Global Constraints, Entfernen des aktiven Knotens → „Aktiven Server erst per Rollen tauschen abgeben“). `TestThreeDaemons` (zwei Knoten + Zeuge im Prozess, Takt 50 ms, Zeiten entsprechend skaliert: setup → join → join Zeuge → trio, Wartung aus, Knoten 1 stoppen → Knoten 2 nach Übernahmezeit `aktiv` Term 2, Ereignis „würde jetzt: promote“).
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test -race ./cmd/pdh-ha/ && go vet ./cmd/pdh-ha/ ./internal/ha/ && GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/pdh-ha` → PASS.
- [ ] **Step 5:** Commit `feat(pdh-ha): Dienst mit Beobachtungsmodus, Socket-API und Befehlen`.

### Task 7: Installation, Join-Skript, Update, Agent

**Files:**
- Create: `scripts/install-pdh-ha.sh`, `scripts/pdh-ha-join.sh`, `cmd/update-agent/ha.go`, `cmd/update-agent/ha_test.go`
- Modify: `install_update.sh:100-111`, `cmd/update-agent/main.go:95-102`, `cmd/update-agent/dr.go` (Schleife, Status)

**Interfaces:**
- `install-pdh-ha.sh <repo_dir>`: `GOTOOLCHAIN=auto go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$(cat VERSION) -X main.commit=$(git -c safe.directory=… rev-parse HEAD)" -o /usr/local/bin/pdh-ha ./cmd/pdh-ha`; Paket `iputils-ping`; Gruppe `pdh` anlegen, falls sie fehlt; `/etc/pdh/ha` 0700; Unit `pdh-ha.service` (`ExecStart=/usr/local/bin/pdh-ha run`, `Restart=always`, `RestartSec=2`, `RuntimeDirectory=pdh-ha`, `RuntimeDirectoryMode=0750`, `RuntimeDirectoryPreserve=yes`, `StateDirectory=pdh-ha`; der Dienst läuft als root und setzt Gruppe `pdh` und 0660 auf den Socket selbst); `systemctl daemon-reload && systemctl enable --now pdh-ha` bzw. `restart`.
- `pdh-ha-join.sh [--witness] <Code>` / `pdh-ha-join.sh --update <Commit>`: Ubuntu prüfen, Pakete `git golang-go iputils-ping ufw python3`, Commit aus dem Code lesen (`base64 -d` nach base64url-Umwandlung + `python3 -c 'import json,sys;print(json.load(sys.stdin)["c"])'`), Repo `https://github.com/strobobaer/pdh-server.git` nach `/opt/pdh-ha-src` auf diesen Commit, `install-pdh-ha.sh /opt/pdh-ha-src`, `pdh-ha join [--witness] <Code>`, Ausgabe „Beigetreten als <Kennung> (<Rolle>)“.
- `install_update.sh`: nach dem Bauen von `bin/update-agent`: `if [[ -x /usr/local/bin/pdh-ha ]]; then bash "$repo_dir/scripts/install-pdh-ha.sh" "$repo_dir"; fi`.
- Agent `cmd/update-agent/ha.go`: `POST /v1/ha/install {"addrs":[…]}` startet asynchron über `hostCommandEnv` `install-pdh-ha.sh` und `pdh-ha setup --addr …` (Werte nur über Umgebungsvariablen, Adressen geprüft mit `net.ParseIP`); `GET /v1/ha/install` → `{"running":bool,"done":bool,"error":"…","log":"letzte 40 Zeilen"}`; Modus `docker` → `{"ok":false,"error":"Ausfallschutz mit zwei Servern nur bei systemd-Installationen (Docker ist das Testsystem)"}`.
- `func (a *updateAgent) drIsActive(ctx context.Context) (bool, error)`: Socket-Datei (`PDH_HA_SOCKET`, Standard `/run/pdh-ha/api.sock`) fehlt → `true, nil`; `GET /v1/status` → `Self.Role == "aktiv"`; Fehler → `false, err`. `drLoop` überspringt WAL-Verschieben, Grundsicherung und Dateiabgleich, wenn nicht aktiv; Fehler → `drStatus.Error = "pdh-ha nicht erreichbar – Sicherung pausiert"`; passiv → neues Feld `drStatus.Paused = "passiver Knoten"` (JSON `paused`; PDH zeigt es in Task 9 im Bereich Netzlaufwerk-Sicherung).

- [ ] **Step 1: Tests** – `TestDRScriptsSyntax` um `scripts/install-pdh-ha.sh` und `scripts/pdh-ha-join.sh` erweitern (`bash -n` per Pfad `../../scripts/…`). `TestHAInstallScriptContent`: enthält `RuntimeDirectory=pdh-ha`, `-X main.commit=`, `iputils-ping`. `TestDRIsActive` (Fake-Server auf Unix-Socket in `t.TempDir()` über `PDH_HA_SOCKET`): Datei fehlt → true; aktiv → true; passiv → false; Socket-Datei da, kein Server → Fehler. `TestHAInstallDockerRefused`. `TestHAInstallRejectsBadAddr` (`"10.0.0.1;x"` → 400).
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren.
- [ ] **Step 4:** `go test ./cmd/update-agent/ && bash -n scripts/install-pdh-ha.sh scripts/pdh-ha-join.sh install_update.sh` → PASS.
- [ ] **Step 5:** Commit `feat: pdh-ha installieren, beitreten, mit aktualisieren; Sicherung nur auf aktivem Knoten`.

### Task 8: Zeugen-Container und Testlabor

**Files:**
- Create: `deploy/ha-witness/Dockerfile`, `deploy/ha-witness/entrypoint.sh`, `deploy/ha-witness/compose.yaml`, `deploy/ha-lab/compose.yaml`, `deploy/ha-lab/ha-lab.sh`, `deploy/ha-lab/README.md`

**Interfaces:**
- Dockerfile mehrstufig (Build-Kontext = Repo-Wurzel): `golang:1.24` baut `pdh-ha` mit Build-Args `VERSION`, `COMMIT`; Laufzeit `debian:bookworm-slim` + `iputils-ping`; `entrypoint.sh`: kein `/var/lib/pdh-ha/state.json` und `PDH_HA_JOIN` gesetzt → `pdh-ha join --witness "$PDH_HA_JOIN"`; dann `exec pdh-ha run`. Volumes `/etc/pdh/ha`, `/var/lib/pdh-ha`; Port 7480. Kein ufw im Container.
- Labor-`compose.yaml`: Dienste `node1`, `node2`, `witness` (dasselbe Image; Knoten ohne `--witness`), `gw` (alpine, antwortet auf Ping); Netze `lan` (node1, node2, gw), `vpn` (node1, node2), `wit` (node1, node2, witness); feste IPs; Gateway-Ziel der Knoten = IP von `gw`. Laborzeiten: Takt 1 s, Fence 5 s, Takeover Trio 15 s, Duo 20 s, Cooldown 0, MaxPerDay 3.
- `ha-lab.sh up|down|status|<Szenario>|alle`: `up` baut, startet node1, `pdh-ha setup`, Codes erzeugen, node2 und witness beitreten, Zeiten setzen, Wartung aus. Szenarien (jeweils frisches `up`): `aktiv-weg` (`docker stop node1` → node2 `aktiv`), `leitung-weg` (node2 von `lan` und `vpn` trennen → node1 bleibt `aktiv`, node2 `passiv`), `zeuge-weg` (Zeuge stoppen → node1 bleibt `aktiv`), `hin-und-her` (node1 wiederholt trennen/verbinden → höchstens 3 Umschaltungen, danach Wartung an), `rueckkehr` (node1 stoppen → node2 übernimmt; node1 starten → `wiedereingliederung` → `passiv`), `neustart-aktiv` (`docker restart node1` bei laufendem node2 → node1 wieder `aktiv`, node2 `passiv`), `version-anders` (node2 mit `--version-override 0.0.0`, node1 stoppen → keine Übernahme, `Blocked` = „Versionen unterschiedlich“). Prüfung jede Sekunde per `docker exec … pdh-ha status --json`: nie zwei `aktiv`. Ausgabe `BESTANDEN` / `NICHT BESTANDEN: <Grund>`, Rückgabewert 0/1.

- [ ] **Step 1:** Dateien anlegen; `bash -n deploy/ha-lab/ha-lab.sh deploy/ha-witness/entrypoint.sh`; `docker compose -f deploy/ha-lab/compose.yaml config -q` → ok.
- [ ] **Step 2:** `bash deploy/ha-lab/ha-lab.sh alle` → alle `BESTANDEN`. Ohne Docker auf dem Entwicklungsrechner: im Bericht als offen vermerken; der Nutzer führt es aus.
- [ ] **Step 3:** Commit `test: Docker-Testlabor und Zeugen-Container für pdh-ha`.

### Task 9: PDH – Betriebsart, Übersicht, Alarme

**Files:**
- Create: `internal/web/ha.go`, `internal/web/ha_test.go`, `web/templates/widgets/ha.gohtml`
- Modify: `web/templates/widgets/dr.gohtml` (oben `{{template "ha-admin" .HA}}`; Anzeige `paused`), `internal/web/dr.go` (Feld `Paused` in `drStatus`), `internal/web/server_config.go:183,282` (Feld `HA haView` neben `DR`, laden nur bei Reiter `dr`), `internal/web/handler.go:561-566` (Routen), Start von `haWatch` an derselben Stelle wie `drWatch`

**Interfaces:**
- Consumes: Vertrag aus Task 6 und `/v1/ha/install` aus Task 7; `h.agentJSON`, `h.notifyAdmins`, `h.appSetting`, `h.setAppSetting`; `ha.Settings.Validate`.
- Produces:
  ```go
  type haView struct {
      Installed bool; Err string; Status haStatus // Spiegel von apiStatus
      Events []haEvent; Install haInstall; JoinCode, JoinCmd string; JoinExpires time.Time
      SuggestAddrs []string // LAN- und WireGuard-Adresse dieses Servers
  }
  func (h *Handler) haClient() *http.Client // Unix-Socket aus PDH_HA_SOCKET, Timeout 3 s
  func (h *Handler) haLoadView(ctx context.Context) haView
  func (h *Handler) haWatch(ctx context.Context)
  ```
- Routen (alle `canServerConfig`, sonst 403; Rückleitung wie `drRedirect` auf `tab=dr`): `POST /admin/server-config/ha/setup`, `POST …/ha/join-code` (`role`), `POST …/ha/maintenance`, `POST …/ha/command` (`force_takeover` nur mit Feld `confirm` = `ÜBERNEHMEN`, sonst Meldung „Bitte ÜBERNEHMEN eintippen“), `POST …/ha/remove`, `POST …/ha/settings`, `GET …/ha/join-script` (eingebettetes `scripts/pdh-ha-join.sh`, Muster `pdh.RestoreScript` in `restore.go`).
- Anzeige (`ha-admin`): ohne Socket „Betriebsart: Einzel“, Adressvorschlag, Knopf „Ausfallschutz einrichten“, Fortschritt der Installation; sonst Betriebsart, Banner „Beobachtungsmodus – es wird nichts umgeschaltet“, Warnungen, Tabelle Knoten (Kennung, Art, Rolle, Amtszeit, Version, Gateway, zuletzt gesehen, Wege je Adresse grün/rot mit Alter), „Übernahme gesperrt: <Blocked>“, Knöpfe, Zeiten-Formular, Ereignisse (neueste 50). Beitrittscode einmalig mit fertigem Befehl `sudo bash pdh-ha-join.sh [--witness] <Code>`, Gültigkeit und Download-Link.
- `haWatch`: alle 30 s Ereignisse seit `ha.event_seq` (App-Einstellung) holen; Alarme (`kind == "alarm"`) je Text-Art höchstens einmal je 15 min (`ha.alarm_last.<sha1 der ersten 40 Zeichen>`) an `notifyAdmins`: „⚠️ **Ausfallschutz (Beobachtung)**\n<Text>\nServer-Einstellungen → Ausfallschutz“.

- [ ] **Step 1: Tests** – `TestHAViewNoSocket` (Rendern von `dr-admin` zeigt „Betriebsart: Einzel“ und den Knopf). `TestHAViewTrio` (Fake-Server auf Unix-Listener in `t.TempDir()`: Banner, Rollen, Wege, Warnung). `TestHAForbidden` (ohne Recht → 403 für alle `ha`-Routen). `TestHAForceNeedsConfirm`. `TestHASettingsInvalid` (Meldung aus Global Constraints in der Rückleitung, kein Aufruf am Socket). `TestHAAlarmThrottle` (zwei gleiche Alarme in 15 min → ein `notifyAdmins`; anderer Text → eigener). `TestDRPausedShown`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren (Stil und CSS-Klassen wie `widgets/dr.gohtml`).
- [ ] **Step 4:** `go test ./internal/web/` → PASS.
- [ ] **Step 5:** Commit `feat: Ausfallschutz – Betriebsart, Knotenübersicht und Alarme im PDH`.

### Task 10: Handbuch, Version

**Files:**
- Modify: `web/templates/help.gohtml`, `internal/web/help.go` (`helpChapterIDs`: `spiegelbetrieb` direkt nach `ausfallschutz`), `web/templates/widgets/help_figures.gohtml`, `CHANGELOG.md`, `VERSION`

- [ ] **Step 1:** Neues Kapitel direkt nach `ausfallschutz`: `<section class="hb-ch" id="spiegelbetrieb" data-title="Ausfallschutz mit zwei Servern" data-pages="server-config">` mit Skizze `hb-fig-spiegelbetrieb` (zwei Server, Zeuge, Wege LAN/VPN) und `.hb-howto`: „Zweiten Server hinzufügen“, „Zeugen hinzufügen (Programm oder Container)“, „Server oder Zeugen entfernen“, „Übersicht lesen“ (Rollen, Amtszeit, Wege, Ereignisse „würde jetzt“, Übernahme gesperrt), Zeugen-Regel (eigene Hardware, eigener Strom), Hinweis „In dieser Version wird noch nichts umgeschaltet“; Bildschirmfoto-Bereich `{{template "hb-shots" (dict "Ref" (index $s "spiegelbetrieb") "Can" $can)}}`. Im Kapitel `ausfallschutz` ergänzen: „Mit zwei Servern sichert nur der aktive Server aufs Netzlaufwerk.“ `CHANGELOG.md`: `## [0.75.0] – <Datum>` unter `### Neu`: „Ausfallschutz mit zwei Servern und optionalem Zeugen im Beobachtungsmodus: Server koppeln, Heartbeat über LAN und VPN, Rollen und Ereignisse ansehen – es wird noch nichts umgeschaltet.“ und „Testlabor für den Ausfallschutz (Docker).“; unter `### Geändert`: „Die Netzlaufwerk-Sicherung läuft mit zwei Servern nur auf dem aktiven Server.“ `VERSION` = `0.75.0`.
- [ ] **Step 2:** `go test ./...` → PASS (u. a. `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`).
- [ ] **Step 3:** Commit `docs: Handbuch-Kapitel Ausfallschutz mit zwei Servern, Version 0.75.0`.

### Abschluss: Prüfung im echten Netz

- [ ] `pdh-backup` auf 0.75.0 aktualisieren → „Ausfallschutz einrichten“ → Dienst `pdh-ha` läuft, Betriebsart Einzel.
- [ ] Zweite Hyper-V-VM (Ubuntu): `sudo bash pdh-ha-join.sh <Code>` → Duo; im Reiter beide Knoten, Wege LAN und WireGuard grün, Hinweis „kein Zeuge“.
- [ ] Zeuge als Container auf `pdh-server` (`deploy/ha-witness`) → Trio, Hinweis verschwindet.
- [ ] Automatik an; WireGuard-Weg trennen → ein Weg rot, kein „würde“-Ereignis; VM 2 stoppen → nur Alarm „Weg ausgefallen“; Knoten 1 stoppen → auf VM 2 zeigt `pdh-ha status` nach ~30 s `aktiv` Term 2 und „würde jetzt: promote“; Knoten 1 starten → `wiedereingliederung` → `passiv`.
- [ ] Netzlaufwerk-Sicherung auf Knoten 1 zeigt „passiver Knoten“ (pausiert); „Rollen tauschen“ → Knoten 1 wieder `aktiv`, Sicherung läuft weiter.
