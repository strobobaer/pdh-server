# ESPPDH-Freigabe in UniFi – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Knopf je Netz der Seite „Netzwerk“: Firewall-Regel „Server → Geräte-Netz, TCP 3232/8266“ in UniFi anlegen; geht das nicht, Ticket an die IT.

**Architecture:** UniFi-Client bekommt Zonen, Regeln lesen und Regel anlegen (offizielle Schnittstelle ab Network 10). Eine reine Entscheidungsfunktion bestimmt aus Server-Adressen, Netzen, Zonen und Regeln: nicht nötig / vorhanden / anlegen / Grund für Ticket. Der Knopf führt das aus; Ticket über die Empfänger der IT-Netz-Alarme.

**Tech Stack:** Go, html/template, pgx, vorhandener `unifi.Client`.

**Spec:** `docs/superpowers/specs/2026-11-22-esppdh-unifi-freigabe-design.md`

## Global Constraints

- Nur TCP 3232 und 8266, nur „ALLOW“, Quelle = IPv4-Adressen der Server (eigene, Partner aus `pdh-ha`, gemeinsame), Ziel = Adressbereich des Netzes.
- Regelname `PDH – ESPPDH-Update <Netzname>`; nie ändern oder löschen.
- Recht `system.network_admin`; nicht am Spiegel, nicht in der Wartung.
- Ticket nur einmal je Netz, solange offen (Schlagwort `esppdh-freigabe:<netz-id>` in `tickets.tags`).
- Version: nächste Korrekturversion über `main`; Handbuch (Kapitel „Netzwerk“ und ESPPDH) und CHANGELOG.

## UniFi-Schnittstelle (aus developer.ui.com, Network 10.6.106)

- `GET /v1/sites/{site}/firewall/zones` → Seite `{count,totalCount,data:[{id,name,networkIds[],metadata{origin}}]}`
- `GET /v1/sites/{site}/firewall/policies` → Seite, Regel `{id,name,enabled,…}`
- `POST /v1/sites/{site}/firewall/policies` mit
  `{name, description, enabled:true, loggingEnabled:false, action:{type:"ALLOW", allowReturnTraffic:true},
    ipProtocolScope:{ipVersion:"IPV4", protocolFilter:{type:"NAMED_PROTOCOL", matchOpposite:false, protocol:{name:"tcp"}}},
    source:{zoneId, trafficFilter:{type:"IP_ADDRESS", ipAddressFilter:{type:"IP_ADDRESSES", matchOpposite:false, items:[{type:"IP_ADDRESS", value}]}}},
    destination:{zoneId, trafficFilter:{type:"IP_ADDRESS", ipAddressFilter:{type:"IP_ADDRESSES", matchOpposite:false, items:[{type:"SUBNET", value:"10.20.0.0/24"}]},
      portFilter:{type:"PORTS", matchOpposite:false, items:[{type:"PORT_NUMBER", value:3232},{type:"PORT_NUMBER", value:8266}]}}}}`
- 404 auf den Zonen → Controller zu alt („Firewall-Regeln erst ab UniFi Network 10“).

## Review Focus

1. Server im Geräte-Netz oder gleiche Zone: keine Regel, kein Ticket.
2. Netz ohne Adressbereich / nicht für ESPPDH genutzt: kein Knopf.
3. Doppeltes Drücken: keine zweite Regel, kein zweites Ticket.
4. UniFi aus, zu alt, ohne Schreibrecht, Fehler: Ticket mit Grund und allen Angaben.
5. Netz in UniFi nicht zu finden (weder Kennung noch Adressbereich): Ticket.

---

### Task 1: UniFi-Client – Zonen, Regeln, Regel anlegen

**Files:** Modify `internal/integrations/unifi/unifi.go`; Test `internal/integrations/unifi/unifi_firewall_test.go`

- Produces: `type FirewallZone struct{ID, Name string; NetworkIDs []string}`, `type FirewallPolicy struct{ID, Name string; Enabled bool}`, `type FirewallAllow struct{Name, Description, SrcZone, DstZone string; SrcIPs []string; DstSubnet string; Ports []int}`, `func (c *Client) FirewallZones(ctx) ([]FirewallZone, error)`, `func (c *Client) FirewallPolicies(ctx) ([]FirewallPolicy, error)`, `func (c *Client) CreateFirewallAllow(ctx, FirewallAllow) (string, error)`, `var ErrFirewallUnsupported`.
- [ ] Tests (httptest): Zonen seitenweise; 404 → `ErrFirewallUnsupported`; POST-Körper genau wie oben (Ports, Subnetz, Quelle, TCP, ALLOW).
- [ ] Implementieren, Tests grün, Commit.

### Task 2: Entscheidung, Knopf, Ticket

**Files:** Create `internal/web/esppdh_fw.go`, `internal/web/esppdh_fw_test.go`; Modify `handler.go` (Route), `perm_gate.go`, `network.go` (Seite: Zustand je Netz)

- Produces: `func esppdhFwDecide(in esppdhFwInput) esppdhFwPlan` (rein); `POST /admin/network/segments/{id}/esppdh-fw`; `h.esppdhFwServerIPs(ctx) []string`; Ticket über `itNetRecipients`.
- [ ] Tests: Server im Netz → nicht nötig; gleiche Zone → nicht nötig; Regel mit Namen da → vorhanden; sonst anlegen mit richtigen Zonen; Netz/Zone nicht gefunden → Grund. Knopf: ohne Recht 403; UniFi aus → Ticket genau einmal (zweites Drücken: Hinweis auf offenes Ticket).
- [ ] Implementieren, Tests grün, Commit.

### Task 3: Oberfläche, Handbuch, Version

**Files:** `web/templates/network.gohtml`, `web/templates/widgets/esppdh_ota.gohtml` (Verweis), `web/templates/help.gohtml`, `CHANGELOG.md`, `VERSION`, `AGENTS.md`

- [ ] Netz-Zeile: Knopf „Freigabe für ESPPDH-Updates in UniFi anlegen“ (nur mit Recht, nur Netze mit Adressbereich und ESPPDH-Nutzung), Zustand „Ticket offen“ mit Verweis. Render-Test.
- [ ] Handbuch „So geht's“, CHANGELOG, VERSION; volle Tests; Commit.
