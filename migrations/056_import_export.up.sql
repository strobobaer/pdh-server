-- 056_import_export.up.sql
--
-- Grundgeruest fuer die neuen Top-Level-Funktionen "Import" und "Export"
-- (neben Global/Core/Module): eigene Berechtigungskategorien mit
-- lesen/schreiben/ausfuehren sowie eine generische Tabelle fuer
-- Verbindungsprofile (Excel, SQLite, MySQL, MSSQL, MQTT inkl. optional
-- integriertem Broker, Web/REST-API fuer Import; PDF/Excel fuer Export).
-- Die eigentliche Datenuebertragung je Konnektortyp folgt in einer
-- spaeteren Ausbaustufe - hier entsteht zunaechst nur die Rechte- und
-- Verwaltungsgrundlage (Verbindungen anlegen/bearbeiten/loeschen).

INSERT INTO permissions (key, label, category) VALUES
    ('import.read',    'Import ansehen',        'Import'),
    ('import.write',   'Import verwalten',      'Import'),
    ('import.execute', 'Import ausführen',      'Import'),
    ('export.read',    'Export ansehen',        'Export'),
    ('export.write',   'Export verwalten',      'Export'),
    ('export.execute', 'Export ausführen',      'Export')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.category IN ('Import', 'Export')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS import_export_connections (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    direction  VARCHAR(10) NOT NULL CHECK (direction IN ('import', 'export')),
    kind       VARCHAR(30) NOT NULL,
    name       VARCHAR(150) NOT NULL,
    config     JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_import_export_connections_direction_kind
    ON import_export_connections (direction, kind);
