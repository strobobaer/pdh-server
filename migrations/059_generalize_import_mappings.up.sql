-- 059_generalize_import_mappings.up.sql
--
-- Die Wertzuordnung (Quellwert -> Infrastruktur -> freie Bezeichnung/
-- Notiz) ist nicht mehr MQTT-spezifisch, sondern das einheitliche Muster
-- fuer alle Import-Konnektoren (MQTT-Topic, Excel-Spalte, DB-Spalte,
-- REST-JSON-Pfad, ...). Tabelle/Spalte entsprechend umbenannt - Daten
-- und Fremdschluessel bleiben erhalten.

ALTER TABLE mqtt_import_mappings RENAME TO import_mappings;
ALTER TABLE import_mappings RENAME COLUMN topic TO source_ref;
ALTER INDEX IF EXISTS idx_mqtt_import_mappings_connection RENAME TO idx_import_mappings_connection;
