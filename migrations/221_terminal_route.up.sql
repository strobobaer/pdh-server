-- 221_terminal_route.up.sql
--
-- Verbindungsweg des PDH-Terminals zum Server (ab Terminal 0.4.7 im Header X-Terminal-Route bei der Statusabfrage):
-- 'lan' = Terminal erreicht PDH direkt im Haus, 'tailnet' = Rückfall über das Tailnet. Angezeigt in der Liste
-- Kiosk-Terminals und in Gekoppelte Geräte. NULL = LAN (alte Terminals ohne Kopfzeile). Nur hinzufügend.

ALTER TABLE terminal_devices ADD COLUMN IF NOT EXISTS route TEXT NULL
	CHECK (route IS NULL OR route IN ('lan', 'tailnet'));
