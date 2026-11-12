-- 161_uuid_v7_terminals.up.sql
--
-- 155 (kiosk_terminals) und 157 (terminal_devices) entstanden nach 122 und
-- legten ihre IDs noch mit gen_random_uuid() an. Wie alle Tabellen seit 122
-- erzeugen sie neue IDs jetzt mit pdh_uuid_v7(); bestehende IDs bleiben.
ALTER TABLE kiosk_terminals ALTER COLUMN id SET DEFAULT pdh_uuid_v7();
ALTER TABLE terminal_devices ALTER COLUMN id SET DEFAULT pdh_uuid_v7();
