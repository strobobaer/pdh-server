-- 138_vpn_disable.up.sql
-- VPN: Verbindungen (Geräte, Standort-Gateways, Tunnel) vorübergehend deaktivieren,
-- ohne sie zu sperren. Deaktiviert = nicht in der Konfiguration des Dienstes
-- pdh-wireguard (Verbindung getrennt); Schlüssel, Adresse und die verteilte
-- Konfiguration bleiben gültig, Aktivieren verbindet wieder (internal/web/vpn.go).
ALTER TABLE vpn_peers ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;
ALTER TABLE vpn_peers ADD COLUMN IF NOT EXISTS disabled_by UUID REFERENCES users(id) ON DELETE SET NULL;
