-- 060_export_mappings.up.sql
--
-- Export im selben Prinzip wie Import, nur umgekehrt: statt einen extern
-- beobachteten Wert einer Infrastruktur zuzuordnen, wird ein bereits
-- importierter Wert (import_mappings - die "Zuordnungen" jeder
-- Import-Verbindung) fuer eine Export-Verbindung ausgewaehlt und bekommt
-- einen freien Zielfeldnamen (Spaltenname/Platzhalter im Export) sowie
-- eine optionale Notiz. "Jetzt exportieren" schreibt die aktuellen Werte
-- aller so markierten Felder in die konfigurierte Zieldatei.

CREATE TABLE IF NOT EXISTS export_mappings (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id     UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    import_mapping_id UUID NOT NULL REFERENCES import_mappings(id) ON DELETE CASCADE,
    field_name        VARCHAR(150) NOT NULL,
    note              VARCHAR(500) NOT NULL DEFAULT '',
    last_exported_at  TIMESTAMPTZ,
    created_by        UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_export_mappings_connection ON export_mappings (connection_id);
