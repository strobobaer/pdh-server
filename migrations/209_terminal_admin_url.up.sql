-- 209_terminal_admin_url.up.sql
--
-- Adresse der Verwaltungsseite des PDH-Terminals (ab Terminal 0.4.5 im Header X-Terminal-Admin-URL
-- bei der Statusabfrage). PDH zeigt daraus ein Zahnrad in „Gekoppelte Geräte" und in der Liste der
-- Kiosk-Terminals. Leer = das Terminal hat keine Adresse gemeldet.

ALTER TABLE terminal_devices ADD COLUMN IF NOT EXISTS admin_url TEXT NOT NULL DEFAULT '';
