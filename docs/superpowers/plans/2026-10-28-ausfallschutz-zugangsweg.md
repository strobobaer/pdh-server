# Ausfallschutz Teil 5 – Zugangsweg Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Nach Rollentausch oder Ausfall erreichen Benutzer und Geräte den aktiven Server automatisch: mitwandernde Adresse (pdh-ha setzt sie selbst), Cloudflare-Tunnel auf beiden Servern mit demselben Token, Skripte beim Rollenwechsel.

**Architecture:** pdh-ha bekommt einen Block `access` in der Verbund-Konfiguration und ein Bauteil `vipCtl`, das die Adresse über die vorhandene `runner`-Schnittstelle (`ip`, `arping`) setzt. Ein eigener Arbeiter gleicht in jedem Takt den Soll-Zustand (aus Rolle und laufender Übergabe) mit dem Ist-Zustand ab – nie über die Aktions-Warteschlange, damit Adressfehler nie „Aktion fehlgeschlagen → Automatik aus“ auslösen. Rollenwechsel lösen Skripte aus `/etc/pdh/ha-hooks/` aus. Das PDH reicht das Tunnel-Token über den Socket an pdh-ha weiter; pdh-ha legt es ab und startet cloudflared nur am aktiven Server. Die Oberfläche bekommt die Karte „Zugangsweg“, ein Skript stellt eine bisherige Server-Adresse um.

**Tech Stack:** Go 1.24, pdh-ha (`cmd/pdh-ha`, mTLS-Port 7480, Unix-Socket), chi/html/template (PDH), iproute2, iputils-arping, systemd, netplan, Docker (Labor).

**Spec:** `docs/superpowers/specs/2026-10-28-ausfallschutz-zugangsweg-design.md`

## Global Constraints

- Mitwandernde Adresse nur IPv4 mit Präfix (`10.10.0.202/24`); nie auf dem Zeugen; nie gleich einer Adresse in `Nodes[].Addrs`.
- Gratuitous ARP: `arping -U -c 3 -I <karte> <ip>` sofort und nach 5 s; Duplikat-Prüfung `arping -D -c 2 -w 3 -I <karte> <ip>` (Ausgang 1 = Antwort = belegt).
- Alarme zur Adresse höchstens einmal je Stunde je Inhalt (Entprellung wie Teil 4).
- Skripte: Ordner `/etc/pdh/ha-hooks/`, alphabetisch, je 30 s, Ausgabe höchstens 2 KB im Ereignis; nur Dateien von root, nicht für Gruppe/andere schreibbar.
- Skript-Variablen exakt: `PDH_HA_ROLLE`, `PDH_HA_AMTSZEIT`, `PDH_HA_KNOTEN`, `PDH_HA_EIGENE_ADRESSE`, `PDH_HA_PARTNER_ADRESSE`, `PDH_HA_VIP` (ohne Präfix, leer wenn keine).
- Tunnel-Token: `/etc/pdh/ha/cloudflared.env` (root, 0600, Zeile `TUNNEL_TOKEN=…`), Ergänzung `/etc/systemd/system/cloudflared.service.d/pdh-ha.conf`; Autostart bleibt aus.
- Prüfsummen: SHA-256 hex (Token; Skripte über „Name\0Inhalt\0“ sortiert).
- Testadresse im Testverbund: **10.10.0.15/24**; Labor: `10.77.1.50/24` (Netz `lan`).
- Einzelserver ohne Verbund und Verbund mit `vip_on=false`: Verhalten unverändert.
- Neue Nebenversion `0.78.0` (`VERSION`, `CHANGELOG.md`), Handbuch im selben Schritt; beide Design-Stile hell/dunkel, nur Farbvariablen.
- Commits ohne Attribution-Zeilen; Code-Kommentare deutsch, Stil wie Umgebung.

**Spec-Korrekturen (beim Planen gefunden, in Task 1 in die Spec übernehmen):**
- Die Adresse wird **nur über den Abgleich** gesetzt (Soll = Rolle `aktiv` und keine eigene Übergabe läuft), nicht in `pgActor.Do`. `ActVIPTake`/`ActVIPRelease` bleiben im Kern, werden in `pgActor.Do` ohne Wirkung und ohne Ereignis quittiert. Grund: Ein Fehler in `Do` schaltet über `actionFailed` Wartung ein und die Automatik ab – das darf die Adresse nie auslösen; und ein Abgleich, der beim Rollentausch nicht auf die Übergabe achtet, holte die Adresse sofort zurück.
- Beobachtungsmodus braucht keine Sonderregel: Dort ändern automatische Entscheidungen die Rolle nie, die Rolle ist also immer echt (Ausgangslage oder Handbefehl) – Adresse und Skripte folgen ihr.
- Test (2.6) prüft nicht per Ping/HTTP, sondern per TLS-Verbindung zu pdh-ha auf Port 7480 an der Prüfadresse; das Zertifikat muss zum erwarteten Knoten gehören. Geht im Labor (kein PDH auf 8090), am Zeugen ohne Ping-Recht und beweist, dass die Adresse beim richtigen Server landet.
- Umstellung Schritt 4: Entfernt netplan die feste .202, setzt pdh-ha sie im nächsten Takt neu – Unterbrechung höchstens wenige Sekunden (Duplikat-Prüfung bis 3 s), nicht „keine“.
- Umstellung Schritt 2 braucht einen neuen Befehl „Adressen eines Knotens ändern“ (Task 7): pg_hba, `listen_addresses` und `primary_conninfo` hängen an den Adressen; `listen_addresses` verlangt einen PostgreSQL-Neustart.

## Review Focus

1. **Rollentausch: Adresse darf nicht zurückspringen** – der abgebende Server bleibt bis zur Übergabe `aktiv`; der Abgleich muss `HandoverPending` beachten, sonst Duplikat-Alarm beim neuen aktiven (Task 3, `TestAccessWantDuringHandover`).
2. **Neustart des aktiven Servers mit gespeicherter Rolle `aktiv`, Partner hat inzwischen übernommen** – darf die Adresse nicht holen, solange sie im Netz antwortet (Task 2 `TestVIPTakeDuplicate`, Task 10 Szenario `vip-neustart`).
3. **Netzwerkkarte mit mehreren Adressen/Karten oder Docker-Brücken** – Kartenwahl darf nicht raten (Task 1, `TestPickInterfaceAmbiguous` mit `docker0` im selben Netz).
4. **Skript hängt oder liest von stdin** – Zeitbegrenzung greift, stdin ist leer, nächster Rollenwechsel wartet und läuft danach (Task 5, `TestHooksTimeoutThenNext`).
5. **Token im PDH gelöscht** – pdh-ha entfernt `cloudflared.env`, stoppt den Tunnel, kein Alarm in Schleife (Task 6, `TestTunnelApplyEmpty`).

---

### Task 1: Verbund-Konfiguration `access`, Prüfungen, Kartenwahl

**Files:**
- Create: `cmd/pdh-ha/access.go`, `cmd/pdh-ha/access_test.go`
- Modify: `cmd/pdh-ha/store.go` (`clusterConfig`, `nodeConfig`)
- Modify: `docs/superpowers/specs/2026-10-28-ausfallschutz-zugangsweg-design.md` (Spec-Korrekturen oben in 2.2, 2.5, 2.6, 6 eintragen)

**Interfaces:**
- Produces:
  - `type accessConfig struct { VIP string \`json:"vip,omitempty"\`; VIPOn bool \`json:"vip_on,omitempty"\`; HooksOn bool \`json:"hooks_on,omitempty"\`; Tested *vipTested \`json:"vip_tested,omitempty"\` }`
  - `type vipTested struct { Net string \`json:"net"\`; Ifaces map[string]string \`json:"ifaces"\`; At time.Time \`json:"at"\` }` (Net = Netz als Präfix, z. B. `10.10.0.0/24`; Ifaces = Knoten → Karte)
  - Feld `Access accessConfig \`json:"access"\`` in `clusterConfig`; Feld `VIPInterface string \`json:"vip_interface,omitempty"\`` in `nodeConfig`
  - `func parseVIP(s string) (netip.Prefix, error)` – nur IPv4, Präfix 8–32, nicht Netz-/Broadcast-Adresse
  - `func validateAccess(cl clusterConfig, a accessConfig) error` – Fehlertexte deutsch: „Adresse … gehört schon zu Knoten …“, „Adresse … liegt in keinem Netz von …“
  - `type ifAddr struct { Iface string; Prefix netip.Prefix }`
  - `func pickInterface(addrs []ifAddr, vip netip.Prefix, override string) (string, error)`
  - `func listIfAddrs() ([]ifAddr, error)` (über `net.Interfaces`, nur `up`, ohne Loopback)
  - `func (t *vipTested) covers(vip netip.Prefix, ifaces map[string]string) bool`

- [ ] **Step 1: Tests schreiben** (`access_test.go`)
  - `TestParseVIP`: `10.10.0.202/24` ok; `10.10.0.202` (ohne Präfix), `fe80::1/64`, `10.10.0.0/24`, `10.10.0.255/24` → Fehler.
  - `TestValidateAccess`: VIP gleich `Nodes[0].Addrs[0]` (Host-Teil, Port egal) → Fehler mit Knotenname; VIP außerhalb der Netze beider Knoten → Fehler; gültige → nil; `VIPOn` ohne `VIP` → Fehler.
  - `TestPickInterface`: eine passende Karte → Name; Override gesetzt und vorhanden → Override; Override nicht vorhanden → Fehler.
  - `TestPickInterfaceAmbiguous`: `eth0 10.10.0.14/24` und `docker0 10.10.0.1/24` → Fehler enthält „nicht eindeutig“ und „vip_interface“; keine passende → Fehler „keine Netzwerkkarte im Netz“.
  - `TestVIPTestedCovers`: gleiches Netz und gleiche Karten → true; anderes Netz oder andere Karte → false; nil → false.
  - `TestClusterConfigAccessRoundTrip`: alte JSON ohne `access` lädt mit leerem Block; Speichern/Laden erhält alle Felder.
- [ ] **Step 2: Laufen lassen** – `go test ./cmd/pdh-ha -run 'VIP|Access|PickInterface'` → FAIL (nicht definiert).
- [ ] **Step 3: Implementieren** in `access.go` und `store.go`; Spec-Korrekturen in die Spec schreiben.
- [ ] **Step 4: Laufen lassen** – dieselben Tests PASS; `go test ./cmd/pdh-ha` grün.
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: Verbund-Einstellung Zugangsweg mit Prüfungen und Kartenwahl"`

### Task 2: `vipCtl` – Adresse haben, übernehmen, abgeben

**Files:**
- Create: `cmd/pdh-ha/vip.go`, `cmd/pdh-ha/vip_test.go`

**Interfaces:**
- Consumes: `runner` (`Run(ctx, argv, env, stdin) ([]byte, error)`), `parseVIP`
- Produces:
  - `type vipCtl struct { r runner; iface string; vip netip.Prefix; sleep func(time.Duration); mu sync.Mutex }`
  - `func newVIPCtl(r runner, iface string, vip netip.Prefix) *vipCtl` (`sleep = time.Sleep`)
  - `func (v *vipCtl) Has(ctx context.Context) (bool, error)` – `ip -o -4 addr show dev <iface>`, sucht `inet <ip>/<bits>`
  - `func (v *vipCtl) Take(ctx context.Context) error` – hat sie schon → nil ohne weitere Befehle; sonst Duplikat-Prüfung, `ip addr add <ip>/<bits> dev <iface>`, `arping -U …` sofort, zweite Runde nach 5 s im Hintergrund (Goroutine, Fehler ignoriert)
  - `func (v *vipCtl) Release(ctx context.Context) error` – `ip addr del …`; Ausgabe „Cannot assign requested address“ gilt als Erfolg
  - `type errDuplicate struct{ MAC string }` mit `Error()` = „Adresse <ip> wird noch von einem anderen Gerät benutzt (MAC <mac>)“; MAC aus der arping-Ausgabe `[aa:bb:…]`, sonst „unbekannt“
  - `func (v *vipCtl) probeFree(ctx context.Context, ip netip.Addr) error` – nur Duplikat-Prüfung (für Task 4)

- [ ] **Step 1: Tests schreiben** (Fake-`runner`, der Befehle aufzeichnet und je Befehlsanfang Ausgabe/Fehler liefert; `sleep` als no-op)
  - `TestVIPHas`: Ausgabe mit `inet 10.77.1.50/24` → true; ohne → false.
  - `TestVIPTake`: nicht vorhanden, `arping -D` Ausgang 0 → Befehlsfolge `ip -o -4 addr show dev eth0`, `arping -D -c 2 -w 3 -I eth0 10.77.1.50`, `ip addr add 10.77.1.50/24 dev eth0`, `arping -U -c 3 -I eth0 10.77.1.50`.
  - `TestVIPTakeAlreadyHeld`: vorhanden → nur `ip … show`, kein add/arping.
  - `TestVIPTakeDuplicate`: `arping -D` liefert Fehler (Ausgang 1) mit Ausgabe `Unicast reply from 10.77.1.50 [02:42:0A:4D:01:0B]` → `errDuplicate{MAC:"02:42:0A:4D:01:0B"}`, kein `ip addr add`.
  - `TestVIPRelease`: `ip addr del 10.77.1.50/24 dev eth0`; Fehler mit „Cannot assign requested address“ → nil; anderer Fehler → Fehler.
  - `TestVIPToolMissing`: runner meldet `exec: "arping": executable file not found` → Fehler „arping fehlt – sudo bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update <Commit> ausführen“ (gleich für `ip`); damit scheitert auch der Test (Task 4) und das Einschalten bleibt gesperrt.
- [ ] **Step 2: Laufen lassen** – `go test ./cmd/pdh-ha -run TestVIP` → FAIL.
- [ ] **Step 3: Implementieren** in `vip.go` (`mu` schützt Take/Release gegen gleichzeitige Aufrufe aus Abgleich und Test).
- [ ] **Step 4: Laufen lassen** – PASS.
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: mitwandernde Adresse setzen und abgeben mit Duplikat-Prüfung"`

### Task 3: Abgleich im Daemon, Heartbeat, Status

**Files:**
- Create: `cmd/pdh-ha/access_worker.go`, `cmd/pdh-ha/access_worker_test.go`
- Modify: `cmd/pdh-ha/daemon.go` (`tickNodeLocked`, Start/Stop des Arbeiters), `cmd/pdh-ha/pgactor.go` (`Do`: VIP-Aktionen quittieren), `cmd/pdh-ha/transport.go` (`message`), `cmd/pdh-ha/observe.go` (`heartbeatLocked`, `obsFrom`, Status), `cmd/pdh-ha/api.go` (`apiStatus`, `apiNode`)

**Interfaces:**
- Consumes: `vipCtl`, `pickInterface`, `listIfAddrs`, `accessConfig`
- Produces:
  - `func accessWant(role ha.Role, handoverPending uint64, a accessConfig) bool` – `a.VIPOn && role == ha.Active && handoverPending == 0`
  - `type accessWorker struct { … }` mit `func (w *accessWorker) set(want bool, role ha.Role, term uint64)` (nicht blockierend, letzter Wert gewinnt) und `func (w *accessWorker) run(ctx context.Context)` (je Signal und spätestens jede Sekunde: `Has` → bei Abweichung `Take`/`Release`; Fehler → `alarmOnce("vip", …)`, entprellt 1 h je Text; erfolgreicher Wechsel → Ereignis `info` „Adresse 10.77.1.50 übernommen“/„abgegeben“)
  - `func (w *accessWorker) held() bool` – letzter bekannter Ist-Zustand
  - `func (w *accessWorker) waitHeld(ctx context.Context, max time.Duration) bool` (für Task 5)
  - `message.VIP bool \`json:"vip,omitempty"\``, `message.Hooks string \`json:"hooks,omitempty"\``, `message.Tun string \`json:"tun,omitempty"\`` (Hooks/Tun füllen Task 5/6)
  - `apiStatus.Access apiAccess`; `type apiAccess struct { VIP string \`json:"vip"\`; VIPOn bool \`json:"vip_on"\`; HooksOn bool \`json:"hooks_on"\`; Iface string \`json:"iface"\`; IfaceErr string \`json:"iface_err"\`; Holder string \`json:"holder"\`; Tested *vipTested \`json:"tested"\`; Hooks apiHooks \`json:"hooks"\`; Tunnel apiTunnel \`json:"tunnel"\` }` (`apiHooks`/`apiTunnel` hier leer anlegen, Task 5/6 füllen)
  - `apiNode.VIPHeld bool \`json:"vip_held"\``, `apiNode.HooksSum string \`json:"hooks_sum"\``, `apiNode.TunnelSum string \`json:"tunnel_sum"\``
  - `Holder` = eigene Kennung, wenn `held()`, sonst Kennung des Partners mit `VIPHeld`, sonst ""
  - `printStatus` (`cmd/pdh-ha/main.go:310`): bei gesetzter `access.vip` Zeile „Adresse 10.10.0.15: hier“ / „bei <id>“ / „niemand“ (plus „(aus)“ ohne `vip_on`); Task 5 ergänzt „Skripte: <n>, gleich|verschieden, letzter Lauf ok|Fehler“, Task 6 „Tunnel: läuft hier|aus, Token gleich|verschieden“

- [ ] **Step 1: Tests schreiben**
  - `TestPrintStatusAccess`: Status mit VIP am Partner → Ausgabe enthält „Adresse 10.77.1.50: bei node2“.
  - `TestAccessWant`: Tabelle – aktiv/0/on → true; passiv, abgeschaltet, wiedereingliederung → false; `vip_on=false` → false.
  - `TestAccessWantDuringHandover`: aktiv mit `HandoverPending=5` → false.
  - `TestAccessWorkerTakesAndReleases` (Fake-runner): `set(true)` → `ip addr add` ausgeführt, Ereignis „übernommen“; `set(false)` → `ip addr del`.
  - `TestAccessWorkerRepairs`: Soll true, Fake meldet nach Übernahme wieder „nicht vorhanden“ → im nächsten Durchlauf erneut `ip addr add`.
  - `TestAccessWorkerDuplicateAlarmOnce`: zwei Durchläufe mit Duplikat → genau ein Alarm-Ereignis.
  - `TestPGActorIgnoresVIPActions`: `Do(ActVIPTake)`/`Do(ActVIPRelease)` → nil, kein Ereignis.
  - `TestHeartbeatVIPField`: `held()` true → `m.VIP == true`; Status des Partners zeigt `vip_held`.
  - `TestStatusAccessHolder`: eigene Adresse gehalten → `holder == self`; Partner meldet `VIP` → `holder == partner`.
  - `TestWitnessNoAccess`: Zeuge startet keinen Arbeiter.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**: Arbeiter beim Start (`startLocked`, nur `Kind=="node"`) mit Karte aus `pickInterface(listIfAddrs(), vip, node.VIPInterface)` anlegen; bei geänderter `access`-Konfiguration neu anlegen (alte Adresse vorher abgeben, falls gehalten). In `tickNodeLocked` nach `ha.Step`: `d.access.set(accessWant(out.Persist.Role, out.HandoverPending, d.cl.Access), out.Persist.Role, out.Persist.Term)`. `listIfAddrs` über eine Funktionsvariable im Daemon, damit Tests Karten vortäuschen.
- [ ] **Step 4: Laufen lassen** – `go test ./cmd/pdh-ha` grün (auch vorhandene Daemon-/Transport-Tests).
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: Adresse folgt der Rolle im Takt, Status und Heartbeat"`

### Task 4: Test der Adresse und Einstellung über Socket und CLI

**Files:**
- Create: `cmd/pdh-ha/vip_check.go`, `cmd/pdh-ha/vip_check_test.go`
- Modify: `cmd/pdh-ha/daemon.go` (Verbund-Mux: `POST /v1/probe-addr`, `/v1/probe-hold`, `/v1/probe-release` für alle Knotenarten), `cmd/pdh-ha/api.go` (`PUT /v1/access`, `POST /v1/vip/test`), `cmd/pdh-ha/main.go` (`pdh-ha access`, `pdh-ha vip test`)

**Interfaces:**
- Consumes: `vipCtl.probeFree`, `newVIPCtl` mit Prüfadresse, Verbund-HTTPS-Client wie `replica.go:64` (Zertifikat des Verbunds)
- Produces:
  - Verbund `POST /v1/probe-addr` `{"addr":"10.77.1.50","expect":"node2"}` → TLS-Verbindung zu `addr:7480` (Verbund-CA, Zeitlimit 3 s), Zertifikat muss auf `expect` lauten; Antwort `{"ok":bool,"err":string}`
  - Verbund `POST /v1/probe-hold` `{"addr":"10.77.1.60/24","seconds":20}` → Prüfadresse annehmen (mit Duplikat-Prüfung), nach Ablauf selbst abgeben; `POST /v1/probe-release` `{"addr"}` gibt sofort ab. Nur Knoten (`Kind=="node"`), nur für Adressen im Netz der eigenen Karte.
  - `type vipCheckResult struct { Holder string \`json:"holder"\`; Checker string \`json:"checker"\`; OK bool \`json:"ok"\`; Err string \`json:"err"\` }`
  - `func (d *daemon) runVIPCheck(ctx context.Context, addr netip.Prefix) ([]vipCheckResult, error)` – nur am CA-Halter oder aktiven Knoten; Prüfadresse belegt (`probeFree`) → Fehler „Prüfadresse ist belegt – freie Testadresse angeben“; dann je Server nacheinander: Server hält die Prüfadresse (eigener direkt, Partner über `probe-hold`), jeder andere Knoten (Partner, Zeuge; eigener Knoten lokal) prüft mit `expect` = Halter; danach abgeben, auch bei Fehlern. Alles ok → `cl.Access.Tested = &vipTested{Net, Ifaces, At}` veröffentlichen.
  - Socket `POST /v1/vip/test` `{"addr":"<ip/präfix>"}` (leer = `access.vip`) → `{"results":[…]}`
  - Socket `PUT /v1/access` `{"vip","vip_on","hooks_on"}` → `validateAccess`; `vip_on` nur mit `Tested.covers(vip, ifaces)`, sonst Fehler „Bitte zuerst testen“; Rechte wie `updateSettings` (CA-Halter oder aktiv)
  - CLI: `pdh-ha access [--vip X] [--vip-on|--vip-off] [--hooks-on|--hooks-off]`, `pdh-ha vip test [--adresse X]` (druckt je Richtung „node1 hält, node2 prüft: ok“ bzw. Fehler; Ausgang 1 bei Fehler)

- [ ] **Step 1: Tests schreiben**
  - `TestProbeAddrHandler`: TLS-Testserver mit Zertifikat „node2“ → `expect=node2` ok; `expect=node1` → `ok=false`, Fehler enthält „gehört zu node2“.
  - `TestRunVIPCheckBothWays` (Fakes für Halten/Prüfen): beide ok → Ergebnisse ok, `Tested` gesetzt mit Netz `10.77.1.0/24`.
  - `TestRunVIPCheckBusy`: `probeFree` meldet Duplikat → Fehler „Prüfadresse ist belegt“, nichts angenommen.
  - `TestRunVIPCheckReleasesOnFailure`: Prüfung schlägt fehl → Prüfadresse trotzdem abgegeben, `Tested` unverändert.
  - `TestAccessPutNeedsTest`: `vip_on=true` ohne passenden Test → „Bitte zuerst testen“; mit Test für dasselbe Netz und dieselben Karten → ok.
  - `TestAccessPutWitnessRefused`: am Zeugen → Fehler wie `clusterEditable`.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**.
- [ ] **Step 4: Laufen lassen** – PASS, `go test ./cmd/pdh-ha` grün.
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: Adresse testen und Zugangsweg einstellen"`

### Task 5: Skripte beim Rollenwechsel

**Files:**
- Create: `cmd/pdh-ha/hooks.go`, `cmd/pdh-ha/hooks_test.go`
- Modify: `cmd/pdh-ha/daemon.go` (Rollenwechsel erkennen), `cmd/pdh-ha/observe.go` (Heartbeat `Hooks`, Status), `cmd/pdh-ha/api.go` (`POST /v1/hooks/run`), `cmd/pdh-ha/main.go` (`pdh-ha hook run`)

**Interfaces:**
- Consumes: `accessWorker.waitHeld`, `accessConfig.HooksOn`, `apiHooks`
- Produces:
  - `const hooksDir = "/etc/pdh/ha-hooks"` (Tests: Feld im Läufer)
  - `type hookFile struct { Name string; Path string; Unsafe string }` (Unsafe = Grund, "" = ok)
  - `func scanHooks(dir string) ([]hookFile, string, error)` – zweiter Wert Prüfsumme (SHA-256 hex über sortiert „Name\0Inhalt\0“ aller ausführbaren regulären Dateien); fehlender Ordner → leer, "", nil
  - `type hookEnv struct { Role ha.Role; Term uint64; Node, OwnAddr, PartnerAddr, VIP string }` mit `func (e hookEnv) vars() []string` (Namen aus Global Constraints)
  - `type hookResult struct { Name string \`json:"name"\`; OK bool \`json:"ok"\`; Out string \`json:"out"\`; Dur float64 \`json:"dur"\` }`
  - `type hookRunner struct { dir string; timeout time.Duration; … }`, `func (h *hookRunner) run(ctx context.Context, e hookEnv) []hookResult` – `exec.CommandContext`, stdin leer, Ausgabe auf die letzten 2 KB gekürzt, Abbruch nach `timeout` (30 s) mit Ausgabe „Zeitüberschreitung nach 30 s“
  - `func (h *hookRunner) enqueue(e hookEnv)` – serielle Warteschlange, Reihenfolge erhalten
  - `apiHooks{ Names []string \`json:"names"\`; Sum string \`json:"sum"\`; Same bool \`json:"same"\`; Last *hookRun \`json:"last"\` }`, `type hookRun struct { At time.Time; Role ha.Role; Results []hookResult }` (JSON klein geschrieben)
  - Socket `POST /v1/hooks/run` → mit aktueller Rolle ausführen, Antwort `{"results":[…]}`; CLI `pdh-ha hook run` druckt je Skript „ok“/Fehler plus Ausgabe

- [ ] **Step 1: Tests schreiben** (Temp-Ordner mit Shell-Skripten; nur Linux – unter Windows `t.Skip`, Lauf im golang:1.24-Container auf LF-Kopie wie in Teil 4)
  - `TestScanHooksOrderAndSum`: `20-b`, `10-a`, nicht ausführbare `README` → Reihenfolge `10-a, 20-b`, `README` ignoriert; Inhaltsänderung ändert die Prüfsumme.
  - `TestScanHooksUnsafe`: Datei mit Modus 0777 → `Unsafe` enthält „für andere schreibbar“; beim Lauf übersprungen mit `OK=false`, Ausgabe „unsicher – übersprungen“.
  - `TestHooksEnv`: Skript schreibt `env` in Datei → alle sechs Variablen mit erwarteten Werten, `PDH_HA_VIP=10.77.1.50` (ohne `/24`).
  - `TestHooksTimeoutThenNext`: Skript `sleep 5` bei `timeout=200ms` → `OK=false`, Ausgabe enthält „Zeitüberschreitung“; ein zweiter `enqueue` läuft danach durch.
  - `TestHooksFailureAlarm`: Ausgang 3 → Alarm-Ereignis mit letzter Ausgabezeile.
  - `TestPrintStatusHooks`: zwei Skripte, Partner gleich, letzter Lauf ok → „Skripte: 2, gleich, letzter Lauf ok“.
  - `TestRoleChangeTriggersHooks` (Daemon-Test): Rolle passiv → aktiv mit `hooks_on` → ein Lauf mit `Role=aktiv`; ohne `hooks_on` → keiner; Zeuge → nie; erster Takt nach Start → keiner.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**: in `tickNodeLocked` Rolle vor `ha.Step` merken; bei Änderung und `HooksOn` `enqueue`; der Läufer wartet bei Rolle aktiv und `VIPOn` vorher `waitHeld(ctx, 10*time.Second)`. Prüfsumme im 30-s-Abgleich neu einlesen; Heartbeat `Hooks`; Status `Same` = Partner-Prüfsumme gleich.
- [ ] **Step 4: Laufen lassen** – PASS.
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: Skripte beim Rollenwechsel"`

### Task 6: Cloudflare-Tunnel auf beiden Servern

**Files:**
- Create: `cmd/pdh-ha/tunnel_setup.go`, `cmd/pdh-ha/tunnel_setup_test.go`, `internal/web/ha_tunnel.go`, `internal/web/ha_tunnel_test.go`
- Modify: `cmd/pdh-ha/api.go` (`PUT /v1/tunnel`, Status), `cmd/pdh-ha/observe.go` (Heartbeat `Tun`), `cmd/server/main.go` (Abgleich starten – auch im Spiegelmodus)

**Interfaces:**
- Consumes: `serviceCtl` (Tunnel), `apiTunnel`, `h.cloudflareSettings(ctx).Token`, `h.haDo`, `haInstalled()`
- Produces:
  - `type tunnelSetup struct { r runner; envFile, dropin string; noSystemd bool }`, `func newTunnelSetup(r runner) *tunnelSetup` (Pfade aus Global Constraints; `noSystemd` bei gesetztem `PDH_HA_TUNNEL_CMD`/`PDH_HA_SERVICE_CMD`)
  - `func (t *tunnelSetup) Sum() string` – SHA-256 hex des abgelegten Tokens, "" ohne Datei
  - `func (t *tunnelSetup) Apply(ctx context.Context, token string) (changed bool, err error)` – leer → Datei und Ergänzung entfernen; sonst Datei 0600 atomar, Ergänzung (`[Service]`, `EnvironmentFile=/etc/pdh/ha/cloudflared.env`, `ExecStart=`, `ExecStart=/usr/bin/cloudflared --no-autoupdate tunnel run`), dann `systemctl daemon-reload` und `systemctl disable cloudflared` (außer `noSystemd`)
  - Socket `PUT /v1/tunnel` `{"token":"…"}` → `Apply`; geändert und Tunnel läuft → `Restart`; leer und Tunnel läuft → `Stop`; Antwort `{"sum":"…"}`
  - `apiTunnel{ Sum string \`json:"sum"\`; Installed bool \`json:"installed"\`; Running bool \`json:"running"\` }`; Heartbeat `Tun` = `Sum()`
  - PDH: `func (h *Handler) RunTunnelSync(ctx context.Context)` – beim Start und alle 60 s: Status holen; nicht eingerichtet → nichts; `tokenSum(token) != st.Access.Tunnel.Sum` → `PUT /v1/tunnel`; Fehler nur ins Server-Protokoll (Komponente `ausfallschutz`), gleicher Text höchstens einmal je Stunde
  - `func tokenSum(token string) string` (PDH, gleiche Rechnung; "" für leeres Token)

- [ ] **Step 1: Tests schreiben**
  - `TestTunnelApply` (Temp-Pfade, Fake-runner): Datei mit `TUNNEL_TOKEN=abc`, Modus 0600, Ergänzung mit `EnvironmentFile`, Befehle `daemon-reload` und `disable cloudflared`; zweiter Aufruf mit gleichem Token → `changed=false`, keine Befehle.
  - `TestTunnelApplyEmpty`: leeres Token → beide Dateien weg, `Sum()==""`, `changed=true`; erneut leer → `changed=false`.
  - `TestTunnelPutRestartsRunning`: Tunnel läuft, neues Token → `Restart`; läuft nicht → kein Start (das entscheidet der Abgleich nach Rolle).
  - `TestHeartbeatTunSum`; `TestPrintStatusTunnel`: Tunnel läuft, Prüfsumme des Partners gleich → „Tunnel: läuft hier, Token gleich“.
  - `TestTunnelSyncSendsOnMismatch` (PDH, Fake-Socket): Prüfsumme anders → ein `PUT /v1/tunnel` mit Token; gleich → keins; pdh-ha nicht eingerichtet → keins.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**; in `cmd/server/main.go` `go webHandler.RunTunnelSync(ctx)` starten, wenn `haConfigured()` – in beiden Modi.
- [ ] **Step 4: Laufen lassen** – `go test ./cmd/pdh-ha ./internal/web -run Tunnel` PASS, dann beide Pakete komplett.
- [ ] **Step 5: Commit** – `git commit -m "Tunnel-Token vom PDH an pdh-ha, cloudflared auf beiden Servern eingerichtet"`

### Task 7: Adressen eines Knotens ändern

**Files:**
- Create: `cmd/pdh-ha/addrs.go`, `cmd/pdh-ha/addrs_test.go`
- Modify: `cmd/pdh-ha/api.go` (`PUT /v1/addrs`), `cmd/pdh-ha/main.go` (`pdh-ha addrs set`), `cmd/pdh-ha/messages.go` (`clusterMsgLocked`: geänderte Adressen erkennen), `cmd/pdh-ha/primary.go` (`writeAccess` wiederverwenden)

**Interfaces:**
- Consumes: `publishClusterLocked`, `writeAccess`, `pgActor.pg.writeConf`, `pg.ctl`, `enqueueFnLocked`
- Produces:
  - `func (d *daemon) setNodeAddrs(id string, addrs []string, restartPG bool) error` – nur am CA-Halter; Adressen wie beim Beitritt geprüft (Port 7480 ergänzt), keine gleich `access.vip` („Die mitwandernde Adresse darf kein Verbindungsweg sein“), mindestens eine; veröffentlicht die Knotenliste
  - `func addrsChanged(old, new clusterConfig, id string) bool`
  - `func (a *pgActor) reapplyAddrs(ctx context.Context, ownListenChanged, restartPrimary bool) error` – `writeAccess` + `reload`; Standby: `standbyConf` neu + `restart`; eigene `Listen` geändert: am Standby `restart`, am aktiven nur mit `restartPrimary` (Ereignis „PostgreSQL wird für neue Adressen neu gestartet (wenige Sekunden)“), sonst Ereignis „Neustart von PostgreSQL nötig: sudo pdh-ha addrs set … --neustart“
  - Socket `PUT /v1/addrs` `{"node":"node1","addrs":["10.10.0.203"],"restart_pg":true}`; CLI `pdh-ha addrs set <knoten> <adr,…> [--neustart]`
  - Jeder Knoten mit pgActor ruft nach Übernahme einer Knotenliste mit geänderten Adressen `reapplyAddrs` über `enqueueFnLocked`

- [ ] **Step 1: Tests schreiben**
  - `TestSetNodeAddrsRejectsVIP`: Adresse gleich `access.vip` → Fehler; nicht CA-Halter → Fehler; leere Liste → Fehler.
  - `TestSetNodeAddrsPublishes`: Knotenliste mit neuen Adressen gespeichert, Version erhöht.
  - `TestReapplyAddrsStandby` (Fake-pgCtl wie `pgactor_test.go`): pg_hba neu, `primary_conninfo` mit neuer Partner-Adresse, `restart`.
  - `TestReapplyAddrsPrimaryNoRestart`: aktiv, eigene Listen geändert, ohne `restartPrimary` → nur `reload` + Hinweis-Ereignis; mit → `restart`.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**.
- [ ] **Step 4: Laufen lassen** – PASS, `go test ./cmd/pdh-ha` grün.
- [ ] **Step 5: Commit** – `git commit -m "pdh-ha: Adressen eines Knotens ändern"`

### Task 8: Oberfläche – Karte „Zugangsweg“, Spiegel-Link

**Files:**
- Modify: `internal/web/ha.go` (`haStatus`/`haNode` um `Access`, `VIPHeld`, `HooksSum`, `TunnelSum`; Ansicht-Methoden), `internal/web/handler.go` (Routen neben `/admin/server-config/ha/settings`), `web/templates/widgets/ha.gohtml` (Karte), `internal/web/mirror.go` (`mirrorPeerFrom`), `web/i18n/*.json` (neue Texte wie bei bestehenden Karten)
- Test: `internal/web/ha_test.go`, `internal/web/mirror_test.go`

**Interfaces:**
- Consumes: Socket `PUT /v1/access`, `POST /v1/vip/test`, `POST /v1/hooks/run`, JSON-Namen aus Task 3–6
- Produces:
  - Routen `POST /admin/server-config/ha/access` (`HAAccessWeb`), `POST /admin/server-config/ha/vip-test` (`HAVIPTestWeb`, Feld `addr` optional), `POST /admin/server-config/ha/hooks-run` (`HAHooksRunWeb`) – Rechte wie `HASettingsWeb`
  - `func (v haView) AccessRows() []haAccessRow`, `type haAccessRow struct { Title, Icon, State, Detail, Level string }` (`Level`: `ok|warn|err|off`) – Zeilen Tunnel, Adresse, Skripte
  - Tunnel: „läuft auf <aktiv>“ ok; läuft auf nicht aktivem → err; Prüfsummen verschieden → warn „Token auf den Servern verschieden“; `PDH_CLOUDFLARE_ORIGIN` nicht `localhost`/`127.0.0.1` → warn „Ziel im Cloudflare-Dashboard muss localhost:8090 sein“; Hinweis „pdhadmin-Seiten folgen mit Teil 6“
  - Adresse: Adresse, Halter, letzter Test, Fehler der Kartenwahl; Formular (Adresse, Testadresse, „Testen“, Schalter); Testfehler mit Ursachen (Proxmox-IP-Filter, Switch-ARP-Prüfung/Port-Security, Cloud-Server → Skripte nutzen)
  - Skripte: Namen, „gleich auf beiden“ bzw. warn, letzter Lauf, Schalter, „Jetzt ausführen“
  - `mirrorPeerFrom`: `st.Access.VIPOn && st.Access.VIP != ""` → `ActiveAddr` = VIP ohne Präfix (sonst wie bisher)

- [ ] **Step 1: Tests schreiben**
  - `TestHAAccessRows`: Status-Beispiele → erwartete Level und Texte für alle oben genannten Fälle.
  - `TestHAAccessWebForwards` (Fake-Socket): Formular → `PUT /v1/access` mit `vip`, `vip_on`, `hooks_on`; Fehler von pdh-ha erscheint als Meldung.
  - `TestHAVIPTestWeb`: Ergebnisse je Richtung im Fragment; Fehlerfall mit Ursachenliste.
  - `TestMirrorPeerUsesVIP`: `vip_on` → Link auf `10.77.1.50`; aus → Pfad-Adresse wie bisher.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren**; Karte in `ha.gohtml` nach „Zeiten“, Aufbau wie die übrigen `card-title`-Abschnitte, Farben nur über `--amber`, `--red`, `--accent`, `--muted`.
- [ ] **Step 4: Laufen lassen** – `go test ./internal/web` grün (ca. 5 min); Sichtprüfung im Docker-Testsystem in Fiori und Fluent, hell/dunkel.
- [ ] **Step 5: Commit** – `git commit -m "Ausfallschutz: Karte Zugangsweg, Spiegel-Link auf die mitwandernde Adresse"`

### Task 9: Skripte – Beitritt und Umstellung einer Adresse

**Files:**
- Modify: `scripts/pdh-ha-join.sh` (Pakete `iproute2`, `arping`; bei `--voll` `cloudflared` aus der Cloudflare-Paketquelle; Ordner `/etc/pdh/ha-hooks` 0755 root anlegen)
- Create: `scripts/pdh-ha-adresse.sh`
- Test: `cmd/pdh-ha/scripts_test.go` (Muster der vorhandenen Skript-Tests)

**Interfaces:**
- Consumes: `pdh-ha addrs set`, `pdh-ha vip test`, `pdh-ha access`
- Produces: `pdh-ha-adresse.sh <schritt> [werte]`:
  - `neu <ip/präfix>` – zusätzliche feste Adresse in `/etc/netplan/60-pdh-ha.yaml` für die Karte, `netplan try --timeout 120`
  - `verbund <ip>[,<ip>…]` – `pdh-ha addrs set <eigener knoten> … --neustart` nach Rückfrage (Hinweis: PostgreSQL startet kurz neu)
  - `test <ip/präfix>` – `pdh-ha vip test --adresse …`
  - `pruefliste <alte-ip>` – Fundstellen der alten Adresse: Verbindungen (`ss -tn`), `/etc/fstab`, `/etc/samba/smb.conf` (`pdhnas`), `ufw status`; Hinweis auf SSH-Zugänge und Überwachung
  - `uebergabe <alte-ip/präfix>` – Abbruch, wenn `$SSH_CONNECTION` über die alte Adresse läuft; alte Adresse aus netplan entfernen (`netplan try`), danach `pdh-ha access --vip <alte> --vip-on`
  - jeder Schritt druckt am Ende „Zurücknehmen: …“ mit dem passenden Befehl

- [ ] **Step 1: Test schreiben** – `TestAdresseScriptSyntax`: `bash -n scripts/pdh-ha-adresse.sh`; ohne Argumente → Ausgang 2 und Hilfe mit allen fünf Schritten.
- [ ] **Step 2: Laufen lassen** – FAIL.
- [ ] **Step 3: Implementieren** beider Skripte.
- [ ] **Step 4: Laufen lassen** – PASS.
- [ ] **Step 5: Commit** – `git commit -m "Skripte: Pakete für den Zugangsweg, Umstellung einer Server-Adresse"`

### Task 10: HA-Labor – Szenarien Zugangsweg

**Files:**
- Modify: `deploy/ha-witness/Dockerfile` (`lab-pg`: `iproute2 arping`), `deploy/ha-lab/compose.yaml` (node1/node2 `cap_add: [NET_ADMIN, NET_RAW]`, `gw` ebenso), `deploy/ha-lab/ha-lab.sh`, `deploy/ha-lab/README.md`

**Interfaces:**
- Consumes: CLI aus Task 4/5/7
- Produces: neue Szenarien in `SCENARIOS` (Modus `real`, außer genannt):
  - `vip-tausch` (observe): `vip test` grün, `access --vip 10.77.1.50/24 --vip-on`; Halter node1; Tausch → binnen 10 s Halter node2, `gw` erreicht 10.77.1.50 (`ping -c1 -W1`); Wächter alle 0,5 s: nie beide gleichzeitig (`ip -o addr` beider Knoten)
  - `vip-ausfall`: node1 anhalten → node2 übernimmt mit Adresse
  - `vip-abschaltung`: node1 von allen Netzen trennen (wie `aktiv-weg`) → node1 gibt die Adresse binnen Abschaltzeit + 2 s ab
  - `vip-duplikat`: `gw` setzt 10.77.1.50 selbst, node1 wird aktiv → keine Übernahme, Alarm „wird noch von einem anderen Gerät benutzt“; `gw` gibt ab → Übernahme im nächsten Takt
  - `vip-entfernt`: `ip addr del` am Halter → binnen 3 s wieder da
  - `vip-neustart`: aktiven Container anhalten, Partner übernimmt, Container wieder starten → holt die Adresse nicht
  - `hooks`: `/etc/pdh/ha-hooks/10-log` auf beiden (schreibt `$PDH_HA_ROLLE $PDH_HA_AMTSZEIT $PDH_HA_VIP` nach `/var/lib/pdh-ha/hook.log`), `hooks_on`; Tausch → node2 `aktiv`, node1 `passiv` mit gleicher Amtszeit; Skript nur auf einem geändert → Status `same=false`
  - `umstellung` (observe): node1 zusätzlich 10.77.1.13 (`ip addr add`), `addrs set node1 10.77.1.13,10.77.2.11,10.77.3.11 --neustart`, Replikation wieder `sync`; `vip test --adresse 10.77.1.60/24` grün; 10.77.1.11 entfernen und `access --vip 10.77.1.11/24 --vip-on` → Halter node1; Tausch → Halter node2

- [ ] **Step 1: Szenarien schreiben** und in `SCENARIOS`/`SC_MODE` eintragen.
- [ ] **Step 2: Laufen lassen** – `bash scratchpad/lab/start.sh alle` (Protokoll `scratchpad/lab/alle.log`): die bisherigen 15 und die 8 neuen Szenarien BESTANDEN.
- [ ] **Step 3: Fehler beheben** (in der betroffenen Task-Datei, Test ergänzen) bis alles grün.
- [ ] **Step 4: Commit** – `git commit -m "HA-Labor: Szenarien für mitwandernde Adresse, Skripte und Umstellung"`

### Task 11: Handbuch, Skizze, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `spiegelbetrieb`: Abschnitt „Zugangsweg“), `web/templates/widgets/help_figures.gohtml` (`hb-fig-spiegelbetrieb` um Karte „Zugangsweg“), `CHANGELOG.md`, `VERSION`

- [ ] **Step 1: Handbuch** – `.hb-howto`-Blöcke „So richtest du die mitwandernde Adresse ein“ (eintragen → Testen → einschalten), „So stellst du die bisherige Adresse um“ (die fünf Skript-Schritte mit Prüfliste und `netplan try`), „Skripte beim Rollenwechsel“ (Ordner, Rechte, Variablen-Tabelle, `sudo pdh-ha hook run`); Beispiele als `<pre>`: Windows-DNS per `ssh` + `Set-DnsServerResourceRecord`, `nsupdate`, Pi-hole (`/etc/pihole/custom.list` + `pihole reloaddns`), Cloudflare-DNS per `curl -X PUT …/dns_records/<id>`; „Wenn der Test scheitert“ (Proxmox-IP-Filter, Switch-ARP-Prüfung/Port-Security, Cloud-Server). Tunnel: „Ziel im Dashboard immer localhost:8090“.
- [ ] **Step 2: Skizze** anpassen.
- [ ] **Step 3: Version** – `VERSION` = `0.78.0`; `## [0.78.0] – 2026-10-28` mit `### Neu` (mitwandernde Adresse, Tunnel auf beiden Servern automatisch, Skripte beim Rollenwechsel, Umstellungsskript, Karte Zugangsweg) – je ein Satz in Anwendersprache. Ist main beim Zusammenführen weiter, Version dort anpassen.
- [ ] **Step 4: Prüfen** – `go test ./internal/web -run 'Help|Changelog'` und `go build ./...` grün.
- [ ] **Step 5: Commit** – `git commit -m "Handbuch und Version 0.78.0: Zugangsweg"`

### Task 12: Abnahme im Testverbund (mit dem Nutzer)

- [ ] **Step 1:** Zweig nach GitHub pushen (nicht main); Befehle an den Nutzer: .17 `pdh-ha-join.sh --update <commit> --voll`; .14 `checkout -B feature/ausfallschutz-zugangsweg` als Besitzer des Repos + `install_update.sh`; Zeuge `deploy/ha-witness` mit `PDH_HA_COMMIT=<commit> docker compose up -d --build` (macht Claude am Windows-PC).
- [ ] **Step 2:** Nutzer: Karte Zugangsweg – Adresse `10.10.0.15/24`, Testen, einschalten; Tunnel auf .17 eingerichtet (gleiche Prüfsumme).
- [ ] **Step 3:** Nutzer: Tausch hin und zurück mit `ping -t 10.10.0.15` vom Windows-PC und Browser auf `http://10.10.0.15:8090`; Ausfall (`systemctl stop pdh-ha postgresql` am aktiven); Neustart des passiven; Skript `10-log` auf beiden.
- [ ] **Step 4:** Ergebnisse festhalten; Merge nach main nur mit ausdrücklicher Freigabe (aktualisiert die Produktion).
