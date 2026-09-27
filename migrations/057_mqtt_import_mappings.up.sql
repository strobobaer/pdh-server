-- 057_mqtt_import_mappings.up.sql
--
-- Im MQTT-Sniffer markierte Werte: ein beobachtetes Topic wird einer
-- Infrastruktur zugeordnet (Infrastruktur-Baum-Picker) und bekommt eine
-- freie Bezeichnung sowie eine optionale Zuordnungsnotiz. Grundlage fuer
-- die spaetere echte Datenuebernahme (aktuell nur Zuordnungsverwaltung,
-- keine automatische Verarbeitung eingehender Werte).

CREATE TABLE IF NOT EXISTS mqtt_import_mappings (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id     UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    topic             VARCHAR(500) NOT NULL,
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    name              VARCHAR(150) NOT NULL,
    note              VARCHAR(500) NOT NULL DEFAULT '',
    created_by        UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mqtt_import_mappings_connection ON mqtt_import_mappings (connection_id);
