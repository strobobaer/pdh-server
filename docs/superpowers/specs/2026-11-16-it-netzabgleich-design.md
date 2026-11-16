# IT-Assets aus dem Netz: Abgleich und Überwachung – Paket 1 UniFi (0.80.17)

Stand: 16.11.2026 · Anwender: „Mach das“ (Entscheidungen unten als Vorschlag von Claude markiert)

## Warum

Die IT-Asset-Liste (`/it`) wird heute von Hand gepflegt und veraltet. Das Netz
weiß aber schon, welche Geräte da sind, welche IP, Firmware und welcher Port.
Das PDH soll diese Daten selbst holen, die Liste aktuell halten und melden,
wenn ein wichtiges Gerät ausfällt – ohne dass jemand eine zweite
Überwachungssoftware betreiben muss.

Freigegebene Reihenfolge (Stand 15.11.2026, Punkt 2): UniFi zuerst,
TP-Link Omada gleich aufgebaut und getrennt einschaltbar, SNMP v3 als dritte
Quelle; Überwachung online/offline, Ports, Firmware; Störmeldung bei Ausfall
(Empfänger/Karenz einstellbar, Vorschlag IT, 5 min).

Dieses Dokument beschreibt **Paket 1: UniFi** und legt die gemeinsame Form
fest, an die Paket 2 (Omada) und Paket 3 (SNMP v3) später andocken.

## Ziele

1. **Netzgeräte automatisch:** Switches, Access Points, Gateways der UniFi-Anlage
   werden als IT-Assets angelegt und laufend aktualisiert (Name beim Anlegen,
   Modell, MAC, IP, Firmware, Seriennummer soweit geliefert).
2. **Clients als Vorschläge:** Clients (PCs, Drucker, Handys, Gäste) werden
   **nicht** automatisch angelegt. Sie erscheinen in einer Vorschlagsliste und
   werden per Klick übernommen oder dauerhaft ausgeblendet. Übernommene Clients
   werden danach wie Netzgeräte aktualisiert und überwacht.
   *(Vorschlag Claude: verhindert, dass Hunderte wechselnde Handys die Liste fluten.)*
3. **Überwachung:** je überwachtem Asset Zustand online/offline/unbekannt,
   „zuletzt gesehen“, Firmware mit Hinweis „Update verfügbar“, Ports
   (Nummer, Name, auf/ab, Geschwindigkeit, PoE) und Uplink (an welchem Gerät/Port).
4. **Störmeldung:** Ist ein überwachtes Asset länger als die Karenzzeit
   (Standard 5 min) offline, legt der Wächter ein **Alarm-Ticket** an –
   gleiche Regeln wie Schwellwert-Alarme 0.80.15 (`requires_root_cause`,
   Abschluss nur über „Ticket lösen“ mit Ursache). Verantwortlich: einstellbar
   (Benutzer oder Gruppe), Standard die Gruppe „IT“, falls vorhanden.
   *(Vorschlag Claude: Ticket statt Störung – Störungen gehören zu Anlagen der Produktion.)*

## Nicht-Ziele

- Nichts an den UniFi-Geräten ändern (nur lesen).
- Kein Verlauf/Diagramm der Online-Zeiten, keine Bandbreiten-Statistik.
- Kein Alarm bei einzelnen Ports oder Firmware – nur Anzeige.
- Omada und SNMP v3 (eigene Pakete, nächste Korrekturversionen).

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- Die IT-Liste wird mit Handys/Gästen geflutet.
- **Alarm-Sturm:** Ist die UDM Pro selbst nicht erreichbar oder der API-Schlüssel
  ungültig, dürfen **keine** Offline-Tickets für alle Geräte entstehen.
  Zustand wird dann „unbekannt“, die Quelle zeigt den Fehler.
- Von Hand gepflegte Angaben (Name, Standort, Notizen, Zuständige, Kaufdaten)
  werden vom Abgleich überschrieben.
- Ein von Hand außer Dienst gestelltes Gerät wird vom Abgleich wieder aktiviert.
- Das PDH braucht UniFi, um zu laufen (Integrationen sind optional).

## Ansätze

| | Ansatz | Abwägung |
|---|---|---|
| **A (gewählt)** | Vorhandene UniFi-Anbindung (`internal/integrations/unifi`, API-Schlüssel + Fingerabdruck) um Geräte/Clients erweitern; Abgleich + Wächter im IT-Modul, nach dem Muster des Alarm-Wächters 0.80.15 | Keine zweite Zugangsdatenpflege, bewährte Sicherheit (Pin, nur Hausnetz), Alarm-Tickets wie gewohnt. Quelle als Schnittstelle → Omada/SNMP docken an. |
| B | Über den Explorer/Daten-Client generisch Werte holen | Explorer ist für Prozessdaten (MQTT/OPC UA/Modbus), passt nicht zu Gerätelisten. |
| C | Externes Werkzeug (Uptime Kuma, UniFi Poller) und nur Ergebnis importieren | Zweite Software im Betrieb, widerspricht dem Ziel. |

Der Anwender hat das Vorgehen Claude überlassen („Mach das“) → **A**.

## Gemeinsame Form (für alle drei Quellen)

Go-Schnittstelle im IT-Modul (Paket `internal/modules/it/netsync` o. ä.):

```go
type Source interface {
    Kind() string                          // "unifi", später "omada", "snmp"
    Fetch(ctx) (Snapshot, error)            // eine vollständige Momentaufnahme
}
type Snapshot struct {
    Devices []NetDevice   // Infrastruktur, wird automatisch angelegt
    Clients []NetClient   // nur Vorschläge / Aktualisierung übernommener
}
```

`NetDevice`: ExternalID, MAC, Name, Model, Kind (switch/ap/gateway/other →
it-Typ `network`), IP, Firmware, FirmwareUpdatable, Serial, State
(online/offline/unknown), Ports[], UplinkDeviceID, UplinkPort.
`NetClient`: ExternalID, MAC, Name/Hostname, IP, Wired/Wireless/VPN,
UplinkDeviceID, UplinkPort (soweit geliefert).

**Schlüssel für die Zuordnung:** MAC (klein, mit Doppelpunkten normiert).
Gibt es schon ein von Hand angelegtes Asset mit gleicher MAC, wird es
verknüpft statt doppelt angelegt.

## Datenmodell (Migration 164)

`it_assets` erhält:

| Spalte | Bedeutung |
|---|---|
| `net_source TEXT` | `unifi`/… oder NULL (von Hand) |
| `net_external_id TEXT` | ID in der Quelle |
| `net_monitored BOOLEAN DEFAULT false` | wird überwacht (Netzgeräte und übernommene Clients: true; abschaltbar je Asset) |
| `net_state TEXT` | `online`/`offline`/`unknown` |
| `net_last_seen TIMESTAMPTZ` | zuletzt online gesehen |
| `net_state_since TIMESTAMPTZ` | seit wann im aktuellen Zustand |
| `firmware TEXT`, `firmware_update BOOLEAN` | Firmware + Update verfügbar |
| `net_uplink TEXT` | lesbarer Uplink, z. B. „USW-24 · Port 7“ |
| `net_gone BOOLEAN DEFAULT false` | von der Quelle nicht mehr gemeldet (nicht löschen) |

Eindeutig: `(net_source, net_external_id)` wo gesetzt; Index auf `lower(mac_address)`.

Neue Tabellen:

- `it_asset_ports(asset_id, idx, name, up, speed_mbps, poe, PRIMARY KEY(asset_id, idx))` – wird bei jedem Detail-Abruf ersetzt.
- `it_net_candidates(source, mac, external_id, name, ip, kind, uplink, last_seen, ignored, PRIMARY KEY(source, mac))` – Client-Vorschläge; übernommene verschwinden, ignorierte bleiben mit `ignored = true`.
- `it_net_alarm_state(asset_id PK, state normal/alarm, offline_since, ticket_id)` – wie `energy_threshold_state`.
- Einstellungen in `app_settings`: `it.net.unifi.enabled`, `it.net.interval_s` (60), `it.net.grace_min` (5), `it.net.responsible_user`, `it.net.responsible_group`, `it.net.priority` (high), `it.net.last_run`, `it.net.last_error`.

## Abläufe

**Abgleich (alle 60 s, nur auf dem aktiven Server, nicht in Wartung/Standby):**
1. `Fetch` an der Quelle. Fehler → alle Assets dieser Quelle auf `unknown`,
   Fehler in `it.net.last_error`, **kein** Alarm. Nach Behebung normal weiter.
2. Je Netzgerät: Asset per (Quelle, ID) oder MAC finden, sonst anlegen
   (Typ `network`, Status `active`, `created_by` = Systembenutzer, Hersteller „Ubiquiti“).
   Überschrieben werden **nur** Quell-Felder: IP, MAC, Modell, Seriennummer,
   Firmware, Zustand, Uplink, Ports. Name nur beim Anlegen.
3. Übernommene Clients aktualisieren (IP, Zustand); online = in der Clientliste.
4. Nicht übernommene Clients in `it_net_candidates` schreiben/auffrischen
   (Vorschläge älter als 30 Tage ohne Sichtung entfernen, ausgeblendete bleiben).
5. Assets der Quelle, die nicht mehr gemeldet werden: `net_gone = true`,
   Zustand `unknown`. Kein Löschen.
6. Assets mit Status `retired` oder `inactive` werden weiter aktualisiert,
   aber nicht überwacht und nie reaktiviert.

Firmware/Ports kommen aus den Gerätedetails; diese werden höchstens alle
15 min je Gerät abgefragt *(Vorschlag Claude: Last auf der UDM gering halten)*.

**Wächter (nach jedem Abgleich):** je überwachtem, aktivem Asset:
- `offline` seit ≥ Karenz und Zustand `normal` → Alarm: Ticket anlegen
  (oder ist das Ticket des letzten Alarms noch offen → Eintrag daran),
  Nachricht an Verantwortliche.
- wieder `online` → Zustand `normal`, Eintrag „wieder erreichbar um …“ am Ticket;
  Ticket bleibt offen bis „Ticket lösen“ mit Ursache.
- `unknown` ändert nichts (keine Alarme bei Quellfehler).
Ticket-Titel: „Netzgerät offline: <Name>“, Beschreibung mit IP, Uplink,
zuletzt gesehen, Link zum Asset. Sink-Umsetzung nach `internal/web/energy_alarms.go`.

## Oberfläche

- **`/it` Liste:** Abzeichen Netz-Zustand (grün online, rot offline,
  grau unbekannt), Quelle-Symbol, Abzeichen „Update“ bei Firmware; Filter
  „nur offline“. Kopf-Knopf „Netz-Abgleich“ (mit Zähler neuer Vorschläge).
- **`/it/net` „Netz-Abgleich“ (neue Seite):** je Quelle Karte mit an/aus,
  letzter Lauf, Fehler, Anzahl Geräte/Clients; Einstellungen Intervall,
  Karenz, Verantwortlich, Priorität; Liste der Client-Vorschläge mit
  „Übernehmen“ (Typ wählbar: Arbeitsplatz/Drucker/Telefon/Tablet/Server/Sonstiges),
  „Ausblenden“, Suche; Reiter „Ausgeblendet“ zum Zurückholen.
  Ist UniFi noch nicht verbunden: Hinweis mit Link zum Reiter „UniFi“ der
  Server-Einstellungen (nur mit `system.unifi`).
- **`/it/{id}` Detail:** Karte „Netz“ mit Zustand seit, zuletzt gesehen,
  Firmware (+ Update), Uplink, Ports-Tabelle; Schalter „überwachen“;
  „Quelle trennen“ (Asset bleibt, wird nicht mehr abgeglichen).
- Beide Stile, hell/dunkel, 320–1366 px.

## Rechte (Migration 164)

- `it.netsync` – „Netz-Abgleich einrichten und Vorschläge übernehmen“ (Kategorie IT);
  bekommen alle Rollen mit `it.edit` (gleiche Abteilung) und `it.admin`.
- Ansehen von Zustand/Ports: `it.view` (wie bisher je Datensatz).
- Schalter „überwachen“/„Quelle trennen“ am Asset: `it.edit` per `hasPermRec`.
- Verbindung zur UDM Pro selbst bleibt bei `system.unifi`.
- Endgültiges Löschen unverändert `it.admin`.

## UniFi-Schnittstelle (Form der Schnittstelle, in `unifi.go` ergänzen)

Amtlich (UniFi Network Integration API, apidoc-cdn.ui.com):
`GET /v1/sites/{siteId}/devices` (Seiten), `GET /v1/sites/{siteId}/devices/{id}`
(Firmware, `firmwareUpdatable`, `interfaces.ports[]`), `GET /v1/sites/{siteId}/clients`
(Seiten; `type` WIRED/WIRELESS/VPN, `macAddress`, `ipAddress`, `uplinkDeviceId`).
Feldnamen vor dem Bau gegen die aktuelle OpenAPI prüfen und im Abschnitt
„Form der Schnittstelle“ bündeln; Tests mit Beispiel-JSON in `testdata`.
Gleiche Sicherheit wie bisher (Pin, nur Hausnetz-Adressen, Fristen, Größenbegrenzung).

## Versionen, Handbuch

- 0.80.17, CHANGELOG `### Neu` + `### Sicherheit` (neues Recht).
- Handbuch: Kapitel IT um „Netz-Abgleich“ erweitern (So geht's: einschalten,
  Vorschlag übernehmen, Alarm-Ticket lösen); Seite `/it/net` in `data-pages`;
  Kapitel Administration (Recht). Skizze in `help_figures.gohtml` ergänzen.

## Dem Bauenden überlassen

Paket-/Dateiaufteilung, genaue Ports-Darstellung auf schmalen Bildschirmen,
Texte der Nachrichten, Testaufbau (Fake-Quelle für Abgleich und Wächter).

## Entscheidungen von Claude (bitte beim Lesen prüfen)

1. Clients nur als Vorschläge, nicht automatisch.
2. Störmeldung = Alarm-Ticket mit Ursache-Pflicht (wie 0.80.15), Standard Gruppe „IT“, Priorität hoch.
3. Kein Alarm bei Quellfehler, Ports oder Firmware.
4. Verschwundene Geräte werden markiert, nie gelöscht.
5. Gerätedetails (Firmware/Ports) höchstens alle 15 min.
