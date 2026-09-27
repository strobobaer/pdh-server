-- 058_mqtt_mapping_values.up.sql
--
-- Automatische Werteuebernahme fuer markierte MQTT-Topics: der zuletzt
-- empfangene Wert je Zuordnung wird direkt an der Zuordnung gespeichert
-- (kein Verlauf/Historie in dieser Ausbaustufe - nur der aktuelle Stand).

ALTER TABLE mqtt_import_mappings ADD COLUMN IF NOT EXISTS last_value TEXT;
ALTER TABLE mqtt_import_mappings ADD COLUMN IF NOT EXISTS last_received_at TIMESTAMPTZ;
