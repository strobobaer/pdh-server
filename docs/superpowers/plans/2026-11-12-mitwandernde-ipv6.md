# Mitwandernde IPv6-Adresse – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Der aktive Server des Verbunds trägt zusätzlich eine IPv6-Adresse aus dem Anbieter-Präfix mit festem Ende, und das PDH hält dazu an der UniFi UDM Pro je Name im Haus einen IPv6-Eintrag aktuell.

**Architecture:** Der Verbund-Dienst `pdh-ha` bekommt eine eigene Einheit `vip6Ctl`, die der vorhandene Abgleich der Zugangswege nach der IPv4-Adresse aufruft; Fehler dort sind nie eine fehlgeschlagene Aktion. Die Weboberfläche liest die aktuelle Adresse aus dem Status von `pdh-ha` und zieht die Einträge an der UDM Pro alle 5 Minuten nach. Der Update-Agent lässt die PDH-Seite von nginx auch auf IPv6 hören.

**Tech Stack:** Go 1.24, `net/netip`, `golang.org/x/net/icmp` und `golang.org/x/net/ipv6` (im Modul, wird von indirekt zu direkt), `ip` aus iproute2, Docker-Labor `deploy/ha-lab`.

**Spec:** `docs/superpowers/specs/2026-11-12-mitwandernde-ipv6-design.md`

## Global Constraints

- Zusatz: ohne IPv6 im Netz, ohne UniFi und ohne gesetztes Feld bleibt jedes Verhalten wie in 0.80.0. Ein Fehler in diesem Teil hält nie einen Rollentausch auf, ruft nie `actionFailed` und schaltet nie die Automatik ab.
- Nur im Verbund und nur mit eingeschalteter mitwandernder IPv4-Adresse (`vip_on`).
- Die Adresse liegt immer als `/128` mit `preferred_lft 0` auf der Karte.
- Kein neues Recht, keine Migration. Kennung des IPv6-Eintrags je Name in `app_settings` unter `unifi.id6.<name>`.
- IPv6-Einträge an der UDM Pro: Es zählt, wohin ein Eintrag zeigt, nicht wer ihn angelegt hat. Jeder Name mit IPv4-Eintrag auf die mitwandernde IPv4-Adresse bekommt den IPv6-Eintrag. Ein vorhandener IPv6-Eintrag mit dem eingestellten Ende wird nachgezogen, einer mit anderem Ziel nie ohne Rückfrage geändert. Gelöscht werden nur IPv6-Einträge, die das PDH selbst angelegt hat. Für IPv4-Einträge gelten die Regeln aus 0.80.0 unverändert.
- Texte für Anwender auf Deutsch, kurze Sätze. Farben nur über CSS-Variablen; Oberfläche in Fiori und Fluent, hell und dunkel prüfen.
- Version 0.80.1 (`VERSION`, `CHANGELOG.md`), Handbuch im selben Schritt.
- Tests laufen in Git Bash: `go test ./...`. `gofmt -l` meldet wegen CRLF fast alles – stattdessen `go vet`.
- Commits auf Deutsch, in Anwendersprache, ohne Zusatzzeilen.
- Produktion 10.10.0.202 wird nie angefasst. Push, Einspielen auf .14/.17 und Merge nur mit Freigabe des Anwenders.

## Review Focus

1. Netz mit zwei öffentlichen Präfixen oder nur temporären Adressen: Es wird genau eine Adresse gesetzt, nie eine je Präfix (Test in Task 2).
2. Präfix wechselt, während der Server passiv ist und später aktiv wird: keine alte Adresse bleibt liegen (Test in Task 4).
3. Ende so gewählt, dass die Adresse einem festen Server gehört (z. B. dessen SLAAC-Adresse): wird nicht gesetzt und nie entfernt (Test in Task 2).
4. Feld geleert, während die UDM Pro nicht erreichbar ist: Einträge bleiben gemerkt und werden beim nächsten Lauf entfernt (Test in Task 7).
5. Server ohne IPv6 im Kernel (`/proc/net/if_inet6` fehlt): nginx-Seite bleibt bei IPv4, `pdh-ha` meldet einmal einen Hinweis und sonst nichts (Tests in Task 2 und Task 8).

---

### Task 1: Einstellung „IPv6-Ende“ im Verbund-Dienst

**Files:**
- Modify: `cmd/pdh-ha/access.go`
- Modify: `cmd/pdh-ha/api.go` (`apiAccess`, Stelle, an der `accessConfig` aus der Anfrage übernommen wird)
- Test: `cmd/pdh-ha/access_test.go`

**Interfaces:**
- Produces:
  - `accessConfig.VIP6Suffix string` mit `json:"vip6_suffix,omitempty"`
  - `func parseVIP6Suffix(s string) (netip.Addr, error)` – liefert `::<Ende>`; leerer Text ist ein Fehler (Aufrufer prüft vorher auf leer)
  - `func vip6Addr(prefix netip.Prefix, suffix netip.Addr) netip.Addr` – obere 64 Bit aus `prefix`, untere aus `suffix`
  - `apiAccess.VIP6Suffix string` mit `json:"vip6_suffix"`

- [ ] **Step 1: Tests schreiben**

```go
func TestParseVIP6Suffix(t *testing.T) {
	for in, want := range map[string]string{"15": "::15", " 0:0:0:15 ": "::15", "aa:15": "::aa:15", "ffff:ffff:ffff:ffff": "::ffff:ffff:ffff:ffff"} {
		got, err := parseVIP6Suffix(in)
		if err != nil || got.String() != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "::", "1:0:0:0:15", "2a02::15", "g", "10.0.0.1", "15/64", "fe80::1%eth0"} {
		if _, err := parseVIP6Suffix(bad); err == nil {
			t.Fatalf("%q angenommen", bad)
		}
	}
}

func TestVIP6Addr(t *testing.T) {
	sfx, _ := parseVIP6Suffix("15")
	if got := vip6Addr(netip.MustParsePrefix("2a02:f90:e198:1700:215:5dff:fe00:b202/64"), sfx); got.String() != "2a02:f90:e198:1700::15" {
		t.Fatal(got)
	}
}

func TestValidateAccessVIP6(t *testing.T) {
	cl := testClusterTwoNodes("10.77.1.11", "10.77.1.12") // wie die Nachbartests der Datei aufbauen
	ok := accessConfig{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "15"}
	if err := validateAccess(cl, ok); err != nil {
		t.Fatal(err)
	}
	for _, a := range []accessConfig{
		{VIP: "10.77.1.50/24", VIPOn: false, VIP6Suffix: "15"},
		{VIP6Suffix: "15"},
		{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "2a02::1"},
	} {
		if err := validateAccess(cl, a); err == nil {
			t.Fatalf("%+v angenommen", a)
		}
	}
}
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/pdh-ha/ -run 'VIP6'` → FAIL (undefiniert).

- [ ] **Step 3: Umsetzen.** `parseVIP6Suffix`: `netip.ParseAddr("::"+TrimSpace(s))`, ablehnen bei Zone, bei oberen 64 Bit ungleich null, bei unteren 64 Bit gleich null. Fehlertext: `IPv6-Ende %q ungültig – erwartet z. B. 15`. `validateAccess`: gesetztes Ende ohne `VIPOn` → `Das IPv6-Ende gilt nur zusammen mit eingeschalteter mitwandernder Adresse`. `apiAccess` und die Übernahme aus der Anfrage um das Feld ergänzen (leerer Text löscht).

- [ ] **Step 4: Tests grün** – `go test ./cmd/pdh-ha/ -run 'VIP6|Access'` → PASS.

- [ ] **Step 5: Commit** – `Ausfallschutz: Einstellung „IPv6-Ende“ für die mitwandernde Adresse`

---

### Task 2: `vip6Ctl` – Präfix wählen, Adresse setzen und wegnehmen

**Files:**
- Create: `cmd/pdh-ha/vip6.go`
- Test: `cmd/pdh-ha/vip6_test.go`

**Interfaces:**
- Consumes: `runner` (`cmd/pdh-ha/runner.go`), `toolMissing`, `parseVIP6Suffix`, `vip6Addr` (Task 1)
- Produces:
  - `type vip6Ctl struct` mit `func newVIP6Ctl(r runner, iface string, suffix netip.Addr, na naSender) *vip6Ctl`
  - `type naSender interface { Announce(ctx context.Context, iface string, addr netip.Addr) error }` (Umsetzung in Task 3; `nil` = keine Ankündigung)
  - `type vip6State struct { Want netip.Addr; Held []netip.Addr; NoPrefix, NoIPv6 bool }` – `Want` ist die Null-Adresse ohne Präfix; `Held` sind alle Adressen der Karte mit dem eingestellten Ende und `/128`
  - `func (v *vip6Ctl) State(ctx context.Context) (vip6State, error)`
  - `func (v *vip6Ctl) Take(ctx context.Context) (netip.Addr, error)` – setzt `Want`, entfernt abweichende `Held`, kündigt an; ohne Präfix `errNoPrefix6`
  - `func (v *vip6Ctl) Release(ctx context.Context) error` – entfernt alle `Held`
  - `var errNoPrefix6`, `type errDAD6 struct{ Addr netip.Addr }`

- [ ] **Step 1: Tests schreiben.** Fake-`runner` wie `vipState` in `access_worker_test.go`: hält die Zeilen von `ip -6 -o addr show`, `ip -6 addr add/del` ändern sie. Testnamen und Kernaussagen:

```go
const card6 = `2: eth0    inet6 2a02:f90:e198:1700:39a7:59a1:40e1:51f5/64 scope global temporary dynamic \       valid_lft 80000sec preferred_lft 10000sec
2: eth0    inet6 2a02:f90:e198:1700:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 86000sec preferred_lft 14000sec
2: eth0    inet6 fe80::215:5dff:fe00:b202/64 scope link \       valid_lft forever preferred_lft forever`

// TestVIP6StateWant: Want == 2a02:f90:e198:1700::15, Held leer, NoPrefix false.
// TestVIP6StateTwoPrefixes: zweite Zeile 2a02:aaaa:0:1:…/64 mit valid_lft 100sec → das Präfix mit 86000sec gewinnt.
// TestVIP6StateOnlyTemporaryOrDeprecated: nur "temporary" bzw. "deprecated" bzw. "tentative" → NoPrefix true, Want Null.
// TestVIP6StateULAIgnored: fd00::/8-Adresse zählt nicht als Präfix (nur 2000::/3).
// TestVIP6StateNoIPv6: runner meldet für "ip -6 …" den Fehler aus dem Kernel ohne IPv6 → NoIPv6 true, kein Fehler.
// TestVIP6Take: genau ein "ip -6 addr add 2a02:f90:e198:1700::15/128 dev eth0 preferred_lft 0"; na.Announce einmal sofort.
// TestVIP6TakeIdempotent: zweiter Take ohne weiteres add.
// TestVIP6TakePrefixChanged: Karte hält 2a02:aaaa:0:1::15/128, Präfix ist jetzt 2a02:f90:…: add neu, dann del alt (Reihenfolge prüfen).
// TestVIP6TakeDADFailed: nach add zeigt die Zeile "dadfailed tentative" → del, Rückgabe *errDAD6.
// TestVIP6TakeOwnFixedAddress: Ende ergibt die Adresse, die schon als /64 auf der Karte liegt → Fehler "gehört fest zu diesem Server", kein add, kein del.
// TestVIP6Release: del für jede Held-Adresse; "Cannot assign requested address" gilt als Erfolg; die /64-Adressen bleiben unberührt.
// TestVIP6ReleaseNeverTouchesNon128: Zeile <Präfix>::15/64 wird nie gelöscht.
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/pdh-ha/ -run VIP6` → FAIL.

- [ ] **Step 3: Umsetzen.** Lesen mit `ip -6 -o addr show dev <Karte> scope global`. Präfix: Zeilen mit `/64`, Adresse in `2000::/3`, ohne die Wörter `temporary`, `deprecated`, `tentative`, `dadfailed`; größtes `valid_lft` gewinnt (`forever` > jede Zahl), bei Gleichstand die kleinere Adresse. `Take`: nach dem `add` bis zu 3 s im Abstand von 250 ms lesen, bis die Zeile weder `tentative` noch `dadfailed` trägt; `dadfailed` → `del` und `*errDAD6`. Zweite Ankündigung nach 5 s in einer Goroutine wie bei `vipCtl.Take`. Ein Fehler von `Announce` wird zurückgegeben, die Adresse bleibt liegen. Sperre `mu` wie bei `vipCtl`.

- [ ] **Step 4: Tests grün** – `go test ./cmd/pdh-ha/ -run VIP6` → PASS.

- [ ] **Step 5: Commit** – `Ausfallschutz: IPv6-Adresse aus dem Präfix der Netzwerkkarte bilden, setzen und wegnehmen`

---

### Task 3: Ankündigung im Netz (Neighbor Advertisement)

**Files:**
- Create: `cmd/pdh-ha/vip6_na.go`
- Test: `cmd/pdh-ha/vip6_na_test.go`
- Modify: `go.mod` (`golang.org/x/net` wird direkte Abhängigkeit; `go mod tidy`)

**Interfaces:**
- Consumes: `naSender` (Task 2)
- Produces:
  - `func naPacket(addr netip.Addr, mac net.HardwareAddr) []byte` – ICMPv6 Typ 136, Code 0, Flags nur Override (`0x20` im ersten Flag-Byte), Zieladresse `addr`, Option 2 (Target Link-Layer Address) mit `mac`; Prüfsumme null (setzt der Kernel)
  - `type icmpNA struct{}` mit `Announce` – sendet `naPacket` zweimal im Abstand von 200 ms an `ff02::1%<Karte>` mit Hop-Limit 255, Absender ist `addr`

- [ ] **Step 1: Test schreiben**

```go
func TestNAPacket(t *testing.T) {
	mac, _ := net.ParseMAC("00:15:5d:00:b2:02")
	b := naPacket(netip.MustParseAddr("2a02:f90:e198:1700::15"), mac)
	if len(b) != 32 || b[0] != 136 || b[1] != 0 || b[4] != 0x20 || b[5] != 0 {
		t.Fatalf("Kopf: % x", b[:8])
	}
	if netip.AddrFrom16([16]byte(b[8:24])).String() != "2a02:f90:e198:1700::15" {
		t.Fatal("Ziel")
	}
	if b[24] != 2 || b[25] != 1 || !bytes.Equal(b[26:32], mac) {
		t.Fatalf("Option: % x", b[24:])
	}
}
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/pdh-ha/ -run NAPacket` → FAIL.

- [ ] **Step 3: Umsetzen.** `icmpNA.Announce`: `icmp.ListenPacket("ip6:ipv6-icmp", addr.String()+"%"+iface)`, über `ipv6.PacketConn` `SetMulticastHopLimit(255)` und `SetMulticastInterface`. Kann nicht gebunden werden (Adresse noch nicht fertig), einmal nach 300 ms erneut versuchen. Der Sender läuft nur unter Linux; die Datei bekommt kein Build-Tag, der Socket-Fehler unter Windows wird als Fehler zurückgegeben (Tests rufen `Announce` nicht).

- [ ] **Step 4: Tests grün** – `go test ./cmd/pdh-ha/ -run 'NAPacket|VIP6'` und `go vet ./cmd/pdh-ha/` → PASS, `go build ./...` ohne Fehler.

- [ ] **Step 5: Commit** – `Ausfallschutz: IPv6-Adresse wird nach dem Setzen im Netz angekündigt`

---

### Task 4: Abgleich, Status und Verdrahtung im Dienst

**Files:**
- Modify: `cmd/pdh-ha/access_worker.go`
- Modify: `cmd/pdh-ha/daemon.go` (bei Zeile 1051, wo `newAccessWorker(newVIPCtl(…))` entsteht)
- Modify: `cmd/pdh-ha/api.go` und die Datei mit der Textausgabe von `pdh-ha status` (dort, wo „Adresse %s: hier“ gedruckt wird)
- Test: `cmd/pdh-ha/access_worker_test.go`, `cmd/pdh-ha/vip6_status_test.go`

**Interfaces:**
- Consumes: `vip6Ctl`, `vip6State`, `errNoPrefix6`, `errDAD6`, `icmpNA`
- Produces:
  - `accessWorker.vip6 *vip6Ctl` (nil = aus), gesetzt über `func (w *accessWorker) withVIP6(v, cleanup *vip6Ctl) *accessWorker`
  - `func (w *accessWorker) held6() (addr string, note string)` – aktuelle Adresse am Server (`""` = keine) und Hinweistext (`""` = keiner)
  - `apiAccess.VIP6 string` (`json:"vip6"`, Adresse beim Halter, sonst leer), `apiAccess.VIP6Note string` (`json:"vip6_note"`)
  - Statuszeile `Adresse <IPv6>: hier` bzw. `bei <Server>`; Hinweiszeilen unten

- [ ] **Step 1: Tests schreiben** (Fake-Runner bedient IPv4 und IPv6):

```go
// TestAccessVIP6FollowsV4: want=true → erst "ip addr add 10.77.1.50/32", danach "ip -6 addr add …::15/128 …"; Ereignis "Adresse 2a02:…::15 übernommen".
// TestAccessVIP6ReleaseWithV4: want=false → IPv6 und IPv4 weg; Ereignis "Adresse 2a02:…::15 abgegeben".
// TestAccessVIP6ErrorDoesNotBlock: "ip -6 addr add" scheitert → held() für IPv4 true, changedTo(true) für IPv4 gefeuert, genau ein Alarm "IPv6-Adresse … nicht übernommen: …", bei 10 weiteren Takten kein zweiter (vipAlarmRepeat).
// TestAccessVIP6PassiveClearsStale: Rolle passiv, Karte hält 2a02:aaaa:0:1::15/128 → wird entfernt (Review Focus 2).
// TestAccessVIP6PrefixChange: aktiv, Präfix wechselt zwischen zwei Takten → neue Adresse gesetzt, alte weg, Ereignis "IPv6-Präfix hat gewechselt: … → …".
// TestAccessVIP6NoPrefix: NoPrefix → kein add, held6() liefert note "Kein IPv6-Präfix an eth0 – die IPv6-Adresse ist nicht gesetzt", genau ein Ereignis "warn".
// TestAccessVIP6Off: vip6 == nil und cleanup == nil → kein einziger "ip -6"-Aufruf.
// TestAccessVIP6SuffixCleared: vip6 == nil, cleanup mit Ende 15, Karte hält <Präfix>::15/128 → im ersten Takt entfernt, danach kein "ip -6"-Aufruf mehr.
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/pdh-ha/ -run 'AccessVIP6'` → FAIL.

- [ ] **Step 3: Umsetzen.**
  - `step`: der vorhandene IPv4-Ablauf bleibt unverändert und ruft am Ende jedes Zweigs `w.step6(ctx, want && has4)` auf, wobei `has4` der IPv4-Stand nach dem Schritt ist. `step6` gibt nie einen Fehler zurück und ruft nie `changedTo`/`setHave` für IPv4.
  - `step6(want)`: `State`; `want` → `Take`, sonst `Release`, wenn `Held` nicht leer. Meldungen über `w.once`/`w.alarm` (Entprellung wie IPv4).
  - Alarmtexte: `IPv6-Adresse %s nicht übernommen: %v`, `IPv6-Adresse %s wird schon von einem anderen Gerät benutzt` (bei `errDAD6`), `IPv6-Adresse %s nicht abgegeben: %v`.
  - `daemon.go`: aus `accessConfig.VIP6Suffix` einen `vip6Ctl` mit `icmpNA{}` bauen und per `withVIP6` anhängen. Wird das Ende geleert oder geändert, bekommt der neue Arbeiter als `cleanup` einen `vip6Ctl` für das alte Ende (im ersten Takt `Release`, dann nil). Das alte Ende steht in der bisherigen Konfiguration, die `daemon` beim Übernehmen einer neuen in der Hand hat.
  - `ufw`: ist `ufw` aktiv und steht in `/etc/default/ufw` `IPV6=no`, liefert `held6` den Hinweis `Die Firewall dieses Servers (ufw) hat IPv6 abgeschaltet – Port 443 ist über IPv6 nicht erreichbar`. Lesen der Datei über den vorhandenen `runner` (`cat`), höchstens einmal je Minute.
  - Status: `apiAccess.VIP6` trägt der Halter ein; der Partner zeigt den Wert des Halters wie bei `Holder`. Dafür meldet jeder Server seine `held6`-Adresse im Heartbeat neben `VIPHeld` (`VIP6Held string`, `json:"vip6_held,omitempty"`).

- [ ] **Step 4: Tests grün** – `go test ./cmd/pdh-ha/` → PASS (gesamtes Paket, ca. 2 Minuten).

- [ ] **Step 5: Commit** – `Ausfallschutz: IPv6-Adresse wandert mit, Status nennt sie; Fehler bei IPv6 halten keinen Rollentausch auf`

---

### Task 5: UniFi-Client für IPv6-Einträge

**Files:**
- Modify: `internal/integrations/unifi/unifi.go`
- Test: `internal/integrations/unifi/unifi_test.go` (vorhandene Testserver-Helfer nutzen)

**Interfaces:**
- Produces (Signaturen bleiben, Verhalten erweitert):
  - `func (c *Client) Create(ctx context.Context, name, ip string) (string, error)` – IPv4 → `A_RECORD`/`ipv4Address`, IPv6 → `AAAA_RECORD`/`ipv6Address`; ältere Schnittstelle `record_type` `A` bzw. `AAAA`
  - `func (c *Client) Update(ctx context.Context, id, name, ip string) error` – ebenso
  - `checkA` wird zu `func checkRecord(name, ip string) (v6 bool, err error)`

- [ ] **Step 1: Tests schreiben**

```go
// TestCreateAAAA: Create(ctx, "nc.firma.de", "2a02:f90:e198:1700::15") sendet {"type":"AAAA_RECORD","enabled":true,"domain":"nc.firma.de","ipv6Address":"2a02:f90:e198:1700::15","ttlSeconds":<ttlSeconds>} und kein Feld ipv4Address.
// TestUpdateAAAA: PUT auf …/policies/<id> mit demselben Körper.
// TestCreateAAAALegacy: ältere Schnittstelle → {"key":"nc.firma.de","record_type":"AAAA","value":"2a02:f90:e198:1700::15","enabled":true}.
// TestCheckRecord: abgelehnt werden "fe80::1", "::1", "::", "fd00::1%eth0", "2a02::15/64", "::ffff:10.0.0.1"; angenommen "2a02:f90:e198:1700::15" (v6=true) und "10.10.0.15" (v6=false).
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./internal/integrations/unifi/ -run 'AAAA|CheckRecord'` → FAIL.

- [ ] **Step 3: Umsetzen.** `policyFor(name, ip string, v6 bool) policy` und `legacyFor(...)` ersetzen `policyA`/`legacyA`. Fehlertext für IPv6: `%q ist keine nutzbare IPv6-Adresse`.

- [ ] **Step 4: Tests grün** – `go test ./internal/integrations/unifi/` → PASS.

- [ ] **Step 5: Commit** – `UniFi: IPv6-Einträge (Typ AAAA) anlegen und ändern`

---

### Task 6: Feld im Reiter Ausfallschutz

**Files:**
- Modify: `internal/web/ha.go` (Status-Struktur bei Zeile 65: `VIP6Suffix string json:"vip6_suffix"`, `VIP6 string json:"vip6"`, `VIP6Note string json:"vip6_note"`)
- Modify: `internal/web/ha_access.go` (`haDoAccess`, `vipRow`)
- Modify: `web/templates/widgets/ha.gohtml` (Formular der mitwandernden Adresse)
- Modify: `web/i18n/en.json`, `mk.json`, `ro.json`, `tr.json` (neue Texte)
- Test: `internal/web/ha_access_test.go`

**Interfaces:**
- Consumes: `apiAccess`-Felder aus Task 1 und 4
- Produces: Formularfeld `vip6_suffix`; `haView`-Zeile der Adresse nennt die IPv6-Adresse

- [ ] **Step 1: Tests schreiben**

```go
// TestHADoAccessSendsVIP6Suffix: Formular vip6_suffix=" 15 " → Anfrage an pdh-ha enthält "vip6_suffix":"15"; leeres Feld sendet "".
// TestVIPRowShowsVIP6: Access{VIP:"10.77.1.50/24",VIPOn:true,Holder:"pdh",VIP6:"2a02:f90:e198:1700::15"} → State enthält "2a02:f90:e198:1700::15".
// TestVIPRowVIP6Note: VIP6Note gesetzt → Level mindestens "warn", Text der Zeile enthält die Note.
// TestHAAccessFormRendersVIP6Field: gerendertes Widget enthält name="vip6_suffix" mit dem gespeicherten Wert und den Hilfetext unten.
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./internal/web/ -run 'VIP6'` → FAIL.

- [ ] **Step 3: Umsetzen.** Feld direkt unter der mitwandernden Adresse: Beschriftung `IPv6-Ende (Zusatz)`, Platzhalter `leer = aus, z. B. 15`, Hilfetext `Der aktive Server bekommt zusätzlich die IPv6-Adresse aus dem Präfix des Netzes mit diesem Ende. Ohne IPv6 im Netz passiert nichts.` Das Feld nutzt die vorhandenen Klassen `form-group`/`form-input`. Recht und Route bleiben die des Formulars.

- [ ] **Step 4: Tests grün, Oberfläche ansehen** – `go test ./internal/web/ -run 'HA|VIP'` → PASS. Lokal starten und den Reiter in Fiori und Fluent, hell und dunkel, bei 390 px und 1366 px ansehen.

- [ ] **Step 5: Commit** – `Ausfallschutz: Feld „IPv6-Ende“ bei der mitwandernden Adresse`

---

### Task 7: IPv6-Einträge für die Namen im Haus und Nachzieh-Lauf

**Files:**
- Create: `internal/web/certs_lan6.go`
- Modify: `internal/web/certs_lan.go` (`certLANMatch` bekommt den Typ, `certLANRemove`, `certDoLAN` mit `overwrite`)
- Modify: `internal/web/certs.go` (`StartCertRenewal` startet den zweiten Takt), `internal/web/certs_store.go` (Schlüssel)
- Modify: `web/templates/widgets/cloudflare.gohtml` (Block „Namen im Haus“)
- Test: `internal/web/certs_lan6_test.go`, `internal/web/certs_lan_test.go`

**Interfaces:**
- Consumes: `certLANClient` (unverändert), Status `Access.VIP6`, `Access.VIP6Suffix`
- Produces:
  - `const keyUnifiID6Prefix = "unifi.id6."`, `keyUnifiHint6Prefix = "unifi.hint6."`
  - `func certLANMatchType(recs []unifi.Record, typ, name, ip, ownID string) (string, unifi.Record)` – `certLANMatch` ruft es mit `"A"`
  - `type lan6Target struct { V4, V6 string; Suffix netip.Addr; Wanted bool }` – mitwandernde IPv4- und IPv6-Adresse aus dem Status (`V6 == ""` = unbekannt), eingestelltes Ende, `Wanted` = Ende gesetzt
  - `func certLAN6Sync(ctx context.Context, d certDeps, t lan6Target) (changed int)` – arbeitet auf allen Einträgen der UDM Pro, nicht nur auf den Namen der Zertifikate
  - `func certLAN6Names(recs []unifi.Record, v4 string) []string` – normalisierte Namen mit einem Eintrag Typ A auf `v4`, sortiert, ohne Dopplungen
  - `func lan6HasSuffix(value string, suffix netip.Addr) bool` – untere 64 Bit der Adresse gleich dem Ende
  - `func (h *Handler) certLAN6Target(ctx context.Context) lan6Target`
  - `certPanel.LAN6 string` (Adresse für die Liste von Hand, `""` = kein IPv6)

- [ ] **Step 1: Tests schreiben** (UniFi im Speicher wie in `certs_lan_test.go`):

```go
// Ziel in allen Tests: V4 "10.10.0.15", V6 "2a02:f90:e198:1700::15", Ende 15.
// TestLAN6SyncCreatesForPDHName: A-Eintrag des PDH (Kennung "a1") auf V4 → Create(name, V6) einmal, unifi.id6.<name> gesetzt, changed == 1.
// TestLAN6SyncCreatesForManualName: von Hand angelegter A-Eintrag "wiki.firma.de" → V4, Name steht in keiner Zertifikatszeile → Create("wiki.firma.de", V6), unifi.id6.wiki.firma.de gesetzt.
// TestLAN6SyncSkipsOtherTargets: A-Eintrag auf 10.10.0.99 → kein IPv6-Eintrag.
// TestLAN6SyncNameNormalised: A-Eintrag "Wiki.Firma.DE." → genau ein Create mit "wiki.firma.de"; zweiter Lauf ohne Create.
// TestLAN6SyncUpdatesOwnAfterPrefixChange: eigener AAAA-Eintrag auf 2a02:aaaa:0:1::15 → Update auf V6, Protokollzeile "ipv6-eintrag im haus geändert".
// TestLAN6SyncUpdatesManualWithSuffix: von Hand angelegter AAAA-Eintrag (keine gemerkte Kennung) auf 2a02:aaaa:0:1::15 → Update auf V6; unifi.id6.<name> bleibt leer (nicht übernommen).
// TestLAN6SyncForeignOtherTargetUntouched: AAAA gleichen Namens auf 2a02:f90:e198:1700::99 → kein Update, kein Create, unifi.hint6.<name> = "An der UDM Pro steht für <name> schon ein IPv6-Eintrag auf <adresse>. Nichts geändert."
// TestLAN6SyncTwoAAAASameName: ein AAAA mit dem Ende und einer ohne → der mit dem Ende wird nachgezogen, der andere bleibt, kein Create.
// TestLAN6SyncRemovesWhenOff: Wanted=false, unifi.id6.<name> gesetzt → Delete(id), Schlüssel geleert; ein von Hand angelegter AAAA-Eintrag bleibt stehen.
// TestLAN6SyncRemovesWhenANoLongerPoints: gemerkter AAAA-Eintrag, der A-Eintrag des Namens zeigt nicht mehr auf V4 bzw. ist weg → Delete(id), Schlüssel geleert.
// TestLAN6SyncRemoveKeptOnError: Delete scheitert → Schlüssel bleibt, nächster Lauf versucht es wieder (Review Focus 4).
// TestLAN6SyncUnknownTarget: Wanted=true, V6 "" → nichts angelegt, nichts geändert, nichts gelöscht.
// TestLAN6SyncNoV4: V4 "" (keine mitwandernde IPv4-Adresse bekannt) → nichts angelegt, nichts gelöscht.
// TestLAN6SyncManyNames: 40 Namen auf V4 → 40 Create in einem Lauf, Reihenfolge nach Name; scheitert einer, laufen die übrigen weiter und der Fehler steht als Hinweis beim Namen.
// TestLAN6SyncNoUnifi: d.lan == nil bzw. errUnifiOff → keine Anfrage, changed == 0, kein Hinweis.
// TestLANRemoveAlsoRemovesAAAA: certLANRemove löscht auch den Eintrag aus unifi.id6.<name>.
// TestOverwriteAlsoOverwritesAAAA: certDoLAN mit overwrite=true ändert einen fremden AAAA-Eintrag auf target, übernimmt ihn nicht als eigenen.
// TestLANListShowsAAAAByHand: Panel ohne UniFi, LAN6 gesetzt → gerenderter Block enthält "Typ AAAA", die Adresse und "ändert sich, wenn der Anbieter das Präfix wechselt".
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./internal/web/ -run 'LAN6|LANRemoveAlso|OverwriteAlso|LANList'` → FAIL.

- [ ] **Step 3: Umsetzen.**
  - `certLAN6Target`: `haStatusFetch` mit 2 s Frist; `Wanted = Configured && Access.VIPOn && Access.VIP6Suffix != ""`, `V4 = vipAddr(Access.VIP)`, `V6 = Access.VIP6` (nur wenn `net.ParseIP` eine IPv6-Adresse ergibt), `Suffix` aus `Access.VIP6Suffix` (gleiche Regel wie `parseVIP6Suffix`; in `internal/web` als kleine eigene Funktion `lan6Suffix`, kein Import aus `cmd/pdh-ha`).
  - Je Name in `certLAN6Names(recs, V4)`: eigener Eintrag (Kennung aus `unifi.id6.<name>`) stimmt → nichts; weicht ab → `Update`. Sonst ein AAAA-Eintrag mit dem Ende (`lan6HasSuffix`) → `Update`, nicht merken. Sonst ein AAAA-Eintrag mit anderem Ziel → Hinweis. Sonst `Create` und Kennung merken.
  - Aufräumen: für jeden Schlüssel `unifi.id6.*`, dessen Name nicht (mehr) in `certLAN6Names` steht oder wenn `Wanted` falsch ist → `Delete`, Schlüssel leeren. Dafür braucht `certDeps` eine Funktion, die Einstellungen nach Präfix auflistet (`settingsPrefix(ctx, prefix) map[string]string`); gibt es sie noch nicht, in `certs_store.go` ergänzen.
  - Hinweise zu Namen, die das PDH nicht als Zertifikat führt, stehen im Block „Namen im Haus“ in einer eigenen kurzen Liste „Weitere Namen an der UDM Pro“ (Name, Hinweistext); ohne Hinweis erscheinen sie dort nicht.
  - Takt: in `StartCertRenewal` ein zweiter `certSchedule` mit `first: 3 * time.Minute, every: 5 * time.Minute, afterRole: time.Minute, poll: 30 * time.Second`; läuft nicht am Spiegel und nicht in der Wartung (`h.maintSkip("zertifikate")`); Frist je Lauf 2 Minuten; `recover` wie `certStep`.
  - Der Lauf liest `Records` nur, wenn `wanted` oder mindestens ein Schlüssel `unifi.id6.*` gesetzt ist – sonst keine Anfrage an die UDM Pro.
  - Protokoll `componentLog("zertifikate")`: `ipv6-eintrag im haus angelegt`, `… geändert`, `… entfernt`, je mit Name, Adresse, Kennung.
  - Vorlage: unter jeder Zeile der Liste von Hand eine zweite Zeile `Typ AAAA → <Adresse>` mit dem Satz `Dieser Eintrag ändert sich, wenn der Anbieter das Präfix wechselt.`; mit UniFi stattdessen der Satz `IPv6 trägt das PDH selbst ein und hält es aktuell.` Der Hinweis `unifi.hint6.<name>` erscheint in der Zeile des Namens wie der IPv4-Hinweis.

- [ ] **Step 4: Tests grün** – `go test ./internal/web/` → PASS (ca. 6 Minuten).

- [ ] **Step 5: Commit** – `Namen im Haus: IPv6-Eintrag an der UDM Pro wird angelegt und nach einem Präfixwechsel nachgezogen`

---

### Task 8: nginx-Seite des PDH hört auf IPv6

**Files:**
- Modify: `cmd/update-agent/certs.go` (`certSiteConf`, Aufrufer, Abgleich vorhandener Seiten)
- Test: `cmd/update-agent/certs_test.go` bzw. `certs_fix_test.go`

**Interfaces:**
- Produces: `func certSiteConf(name, upstream string, redirect, v6 bool) string`; `var certHasIPv6 = func() bool` (prüft, ob `/proc/net/if_inet6` existiert und nicht leer ist; Variable für Tests)

- [ ] **Step 1: Tests schreiben**

```go
// TestCertSiteConfV6: v6=true → enthält "    listen 80;\n    listen [::]:80;\n" (bei redirect) und "    listen 443 ssl;\n    listen [::]:443 ssl;\n".
// TestCertSiteConfNoV6: v6=false → kein "[::]" in der Ausgabe; Ausgabe byte-gleich zur bisherigen (Vergleich mit einem festen Text im Test).
// TestCertSiteRewrittenForV6: vorhandene pdh.conf im alten Stand (Kopfzeile certSiteHeader, ohne "[::]"), certHasIPv6 true → der Abgleich schreibt sie neu, nginx -t und reload je einmal; zweiter Lauf schreibt nicht.
// TestCertSiteNotRewrittenWithoutV6: certHasIPv6 false → Datei bleibt, kein reload.
// TestCertSiteForeignFileUntouched: Datei ohne certSiteHeader → nie neu geschrieben.
```

- [ ] **Step 2: Fehlschlag prüfen** – `go test ./cmd/update-agent/ -run 'CertSite'` → FAIL.

- [ ] **Step 3: Umsetzen.** Neu geschrieben wird nur über den vorhandenen Weg mit Sicherung, `nginx -t` und Zurückrollen bei Fehler. Vergleich „Soll gleich Ist“ auf den ganzen Dateiinhalt.

- [ ] **Step 4: Tests grün** – `go test ./cmd/update-agent/` → PASS.

- [ ] **Step 5: Commit** – `Zertifikate: nginx-Seite des PDH hört auch auf IPv6`

---

### Task 9: Labor-Szenario `vip6-tausch`

**Files:**
- Modify: `deploy/ha-lab/compose.yaml` (LAN-Netz `10.77.1.0/24` bekommt `enable_ipv6: true` und das Subnetz `2001:db8:77:1::/64`; Knoten bekommen `sysctls: net.ipv6.conf.all.disable_ipv6=0`)
- Modify: `deploy/ha-lab/ha-lab.sh` (Szenario, Liste `alle`), `deploy/ha-lab/README.md`

**Interfaces:**
- Consumes: Befehl zum Setzen der Zugangseinstellung, den die vorhandenen `vip-*`-Szenarien nutzen (dort nachsehen), ergänzt um `vip6_suffix`

- [ ] **Step 1: Szenario schreiben.** Ablauf mit festen Erwartungen:
  1. `vip6_suffix=50` setzen → auf dem aktiven Knoten zeigt `ip -6 -o addr show dev <Karte>` die Zeile `2001:db8:77:1::50/128`, auf dem passiven nicht; `pdh-ha status` nennt die Adresse. Das Labor-Präfix `2001:db8:77:1::/64` liegt in `2000::/3` und zählt damit als öffentliches Präfix.
  2. Vom Zeugen aus `ping -6 -c 2 2001:db8:77:1::50` → Antwort; `ip -6 neigh` zeigt die MAC des aktiven Knotens.
  3. Tausch → Adresse liegt auf dem anderen Knoten; binnen 5 s zeigt `ip -6 neigh` am Zeugen dessen MAC, ohne dass der Eintrag vorher gelöscht wurde. **Das ist die Messung aus der Spec 4.2.** Dauer in das Protokoll schreiben.
  4. Rücktausch → wieder beim ersten.
  5. Präfixwechsel nachstellen: auf dem aktiven Knoten `ip -6 addr add 2001:db8:99:1::2/64 dev <Karte> valid_lft 999999 preferred_lft 999999` und der alten /64-Adresse `valid_lft 60 preferred_lft 0` geben → binnen 10 s liegt `2001:db8:99:1::50/128` an und `2001:db8:77:1::50/128` ist weg.
  6. `vip6_suffix` leeren → keine `::50/128` mehr auf beiden Knoten, IPv4-Adresse unverändert.
  7. Zum Schluss die vorhandene Prüfung „alle bestätigten Zeilen vorhanden“.

- [ ] **Step 2: Lauf** – `bash deploy/ha-lab/ha-lab.sh vip6-tausch` (über den Hilfscontainer `pdh-ha-lab-runner`) → `BESTANDEN: vip6-tausch`.

- [ ] **Step 3: Messung auswerten.** Lernt der Zeuge in Schritt 3 nicht binnen 5 s um, im Ledger ein Ruling festhalten und in `vip6_na.go` ergänzen: zusätzlich je ein Neighbor Advertisement an die Adressen aus `ip -6 neigh show dev <Karte>` (unicast). Danach Schritt 2 wiederholen.

- [ ] **Step 4: Gesamtlauf** – `bash deploy/ha-lab/ha-lab.sh alle` → `BESTANDEN: alle 56 Szenarien`; Race-Test `go test -race -count=1 ./cmd/pdh-ha/ ./internal/ha/ ./internal/mirror/` im `golang:1.24`-Container → `ok`.

- [ ] **Step 5: Commit** – `Labor: Szenario für die mitwandernde IPv6-Adresse`

---

### Task 10: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel Ausfallschutz beim Abschnitt zur mitwandernden Adresse; Abschnitt „So zeigen die Namen im Haus auf den Server“ bei Zeile 1527)
- Modify: `web/templates/widgets/help_figures.gohtml` (Skizze des Formulars der mitwandernden Adresse, falls sie die Felder zeigt)
- Modify: `CHANGELOG.md`, `VERSION`

- [ ] **Step 1: Handbuch.** Im Kapitel Ausfallschutz ein `.hb-howto` „IPv6-Adresse mitwandern lassen“ mit Schritten: Feld „IPv6-Ende“ füllen (z. B. `15`), speichern, in der Zeile der Adresse die IPv6-Adresse ablesen. Dazu drei Sätze: gilt nur mit eingeschalteter mitwandernder Adresse; ohne IPv6 im Netz passiert nichts; die Adresse ist öffentlich, ob sie von außen erreichbar ist, entscheidet die Firewall des Routers. Bei den Namen im Haus: mit UniFi trägt das PDH für jeden Namen, der an der UDM Pro auf die mitwandernde Adresse zeigt (auch von Hand eingetragene), den IPv6-Eintrag selbst ein und zieht ihn binnen 5 Minuten nach, wenn der Anbieter das Präfix wechselt; ohne UniFi steht der Eintrag in der Liste und muss nach einem Präfixwechsel von Hand geändert werden. Den am 12.11. ergänzten Satz zum Zertifikatsfehler beim Aufruf über die Adresse behalten.

- [ ] **Step 2: Änderungsprotokoll und Version.** `VERSION` → `0.80.1`. Neuer Eintrag ganz oben:

```markdown
## [0.80.1] – <Datum des Abschlusses>

### Neu

- Ausfallschutz: Der aktive Server kann zusätzlich eine IPv6-Adresse tragen, die beim Rollentausch mitwandert – Feld „IPv6-Ende“ bei der mitwandernden Adresse, ein Zusatz, ohne den alles unverändert läuft.
- Namen im Haus: Mit einer UniFi UDM Pro trägt das PDH für jeden Namen, der dort auf die mitwandernde Adresse zeigt, auch den IPv6-Eintrag ein – auch für von Hand eingetragene Namen – und hält ihn aktuell, wenn der Anbieter das Präfix wechselt. Geräte mit IPv6 gehen damit im Haus direkt zum Server.

### Geändert

- Die nginx-Seite des PDH hört auch auf IPv6.
```

- [ ] **Step 3: Prüfen** – `go test ./...` → alles `ok` (u. a. `TestChangelogMatchesVersion`, `TestHelpChaptersMatch`).

- [ ] **Step 4: Commit** – `Handbuch und Änderungsprotokoll 0.80.1: mitwandernde IPv6-Adresse`

---

### Task 11: Abnahme auf dem Testverbund (mit Freigabe)

Braucht die Freigabe des Anwenders für Push und Einspielen. Auf beiden Testservern vorher `PDH_UPDATE_BRANCH=feature/mitwandernde-ipv6` in `/opt/pdh-server/.env` setzen und `sudo systemctl restart pdh-update-agent`; nach dem Merge wieder `main`.

- [ ] **Step 1:** Verbund-Update am aktiven Server: `sudo pdh-ha update start <voller Commit> --version 0.80.1`, Verlauf `sudo pdh-ha update status` → „Update auf 0.80.1 abgeschlossen“.
- [ ] **Step 2:** Im PDH (Reiter Ausfallschutz) „IPv6-Ende“ `15` setzen → `sudo pdh-ha status` zeigt `Adresse 2a02:…::15: hier`; `ip -6 addr show dev eth0` zeigt sie als `/128` mit `preferred_lft 0`.
- [ ] **Step 3:** Binnen 5 Minuten: `nslookup -type=AAAA nc.strobl-home.net 10.10.0.1` (Windows-PC) → die Adresse aus Schritt 2, nicht mehr `2a06:…`.
- [ ] **Step 4:** Vom Windows-PC `curl -6 -s -o NUL -w "%{remote_ip} %{http_code}\n" https://nc.strobl-home.net/` → die Adresse aus Schritt 2 und `302`.
- [ ] **Step 5:** Rollentausch hin und zurück (`sudo pdh-ha swap` am aktiven Server bzw. Knopf im PDH); nach jedem Tausch Schritt 4 wiederholen und die Zeit messen, bis der Windows-PC wieder Antwort bekommt (Ziel: unter 10 s).
- [ ] **Step 6:** Absender prüfen: `ip -6 route get 2606:4700:4700::1111` am aktiven Server nennt als `src` die feste Adresse, nicht `…::15`; der Verbund bleibt verbunden.
- [ ] **Step 7:** Feld leeren → Adresse auf beiden Servern weg, binnen 5 Minuten liefert `nslookup -type=AAAA` wieder die Cloudflare-Adressen; IPv4 unverändert.
- [ ] **Step 8:** Feld für den Betrieb wieder setzen, wenn der Anwender das will. Danach Prüfung vor dem Merge nach `AGENTS.md`; Merge nur mit Freigabe.
