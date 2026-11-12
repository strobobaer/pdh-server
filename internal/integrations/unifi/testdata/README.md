Diese Antworten sind aus der Dokumentation geschrieben, nicht von einem Gerät mitgeschnitten.

- `sites.json`, `dns_policies.json`, `dns_policy_created.json`: nach der amtlichen
  OpenAPI-Beschreibung der UniFi Network API 10.3.58
  (https://apidoc-cdn.ui.com/network/v10.3.58/integration.json, gelesen am 12.11.2026),
  Schemas „Site overview page“, „IntegrationDnsPolicyPageDto“, „DNS policy“,
  „IntegrationDnsARecordDto“.
- `static_dns.json`: ältere, nicht dokumentierte Schnittstelle
  `/proxy/network/v2/api/site/<name>/static-dns`. Feldnamen aus Quellen Dritter
  (external-dns-unifi-webhook v0.4.0, `internal/unifi/types.go`), am Gerät ungeprüft.

Nach der Abnahme am Gerät des Anwenders durch echte Antworten (ohne Schlüssel) ersetzen.
