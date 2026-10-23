-- 137_vpn_admin.up.sql
-- VPN: Einrichtung, Gateways, ausgehende Tunnel und alle Zugänge liegen jetzt unter
-- Server-Einstellungen → VPN und brauchen Administratorrechte (system.server_config).
-- WireGuard läuft auf dem Host als systemd-Dienst pdh-wireguard statt im Container.
-- Das eigene Recht „VPN verwalten“ (136) entfällt; Zuordnungen fallen per CASCADE weg.
DELETE FROM permissions WHERE key = 'vpn.manage';
