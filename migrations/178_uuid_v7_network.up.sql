-- 178_uuid_v7_network.up.sql
--
-- 177 (net_segments, net_wlans) legte seine IDs noch mit gen_random_uuid() an.
-- Wie alle Tabellen seit 122 erzeugen sie neue IDs jetzt mit pdh_uuid_v7();
-- bestehende IDs bleiben.
ALTER TABLE net_segments ALTER COLUMN id SET DEFAULT pdh_uuid_v7();
ALTER TABLE net_wlans ALTER COLUMN id SET DEFAULT pdh_uuid_v7();
