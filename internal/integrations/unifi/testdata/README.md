Diese Antworten sind aus der Dokumentation geschrieben, nicht von einem Gerät mitgeschnitten.

- `sites.json`, `dns_policies.json`, `dns_policy_created.json`: nach der amtlichen
  OpenAPI-Beschreibung der UniFi Network API 10.3.58
  (https://apidoc-cdn.ui.com/network/v10.3.58/integration.json, gelesen am 12.11.2026),
  Schemas „Site overview page“, „IntegrationDnsPolicyPageDto“, „DNS policy“,
  „IntegrationDnsARecordDto“.
- `static_dns.json`: ältere, nicht dokumentierte Schnittstelle
  `/proxy/network/v2/api/site/<name>/static-dns`. Feldnamen aus Quellen Dritter
  (external-dns-unifi-webhook v0.4.0, `internal/unifi/types.go`), am Gerät ungeprüft.

- `devices.json`, `devices_page2.json`, `device_detail.json`, `clients.json`: nach der
  amtlichen OpenAPI-Beschreibung 10.3.58, Schemas „Adopted device overview (page)“,
  „Adopted device details“, „Port overview“, „Port PoE overview“, „Device features“,
  „Client overview (page)“, „Wired/Wireless client overview“. `devices.json` und
  `devices_page2.json` sind zwei Seiten derselben Liste (3 Geräte). Im Gerätedetail ist
  `features` ein Objekt, in der Liste ein Feld aus Zeichenketten.

- `networks.json`, `network_detail_gateway.json`, `network_detail_unmanaged.json`,
  `wifi_broadcasts.json`, `wifi_detail_wpa2.json`, `wifi_detail_enterprise.json`,
  `wifi_detail_open.json`: aus der Dokumentation geschrieben, nicht mitgeschnitten – nach
  der amtlichen OpenAPI-Beschreibung 10.3.58, Schemas „Network overview/details“ und
  „Wifi broadcast overview/details“. Das Kennwort in `wifi_detail_wpa2.json` ist erfunden.

Nach der Abnahme am Gerät des Anwenders durch echte Antworten (ohne Schlüssel) ersetzen.
