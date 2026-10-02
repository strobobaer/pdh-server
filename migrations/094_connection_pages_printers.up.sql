-- 094_connection_pages_printers.up.sql
--
-- 1) Eigene Seite je Import-/Export-Verbindung:
--    - mehrere benannte Abfragen je Verbindung (SQL-SELECT, REST-/Web-Endpunkt,
--      Modbus-Registerblock, OPC-UA-Knotenliste, Excel/CSV-Zeile) mit eigenem
--      Intervall und letztem Ergebnis; Zuordnungen verweisen darauf mit dem
--      Quellwert "q:<abfrage-id>:<spalte>"
--    - Ergebnis der letzten Pruefung (Checks) an der Verbindung
--    - eigene Vorlagen (Abfragen + Zuordnungen) je Verbindungstyp
-- 2) Druckerintegration: Zebra ZT4xx (ZPL), Dymo (DYMO Connect am PC),
--    Netzwerkdrucker (IPP / RAW 9100).

CREATE TABLE IF NOT EXISTS connection_queries (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id    UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    name             VARCHAR(150) NOT NULL,
    spec             JSONB NOT NULL DEFAULT '{}'::jsonb,
    interval_minutes INT NOT NULL DEFAULT 0,
    enabled          BOOLEAN NOT NULL DEFAULT true,
    sort             INT NOT NULL DEFAULT 0,
    last_run_at      TIMESTAMPTZ,
    last_ok          BOOLEAN,
    last_message     TEXT NOT NULL DEFAULT '',
    last_rows        INT NOT NULL DEFAULT 0,
    last_result      JSONB,
    created_by       UUID REFERENCES users(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_connection_queries_connection ON connection_queries (connection_id, sort);

ALTER TABLE import_export_connections
    ADD COLUMN IF NOT EXISTS last_check    JSONB,
    ADD COLUMN IF NOT EXISTS last_check_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS connection_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    direction   VARCHAR(10) NOT NULL CHECK (direction IN ('import', 'export')),
    kind        VARCHAR(30) NOT NULL,
    name        VARCHAR(150) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_connection_templates_kind ON connection_templates (direction, kind);

CREATE TABLE IF NOT EXISTS printers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind          VARCHAR(20) NOT NULL CHECK (kind IN ('zebra', 'dymo', 'network')),
    name          VARCHAR(150) NOT NULL,
    location      VARCHAR(200) NOT NULL DEFAULT '',
    config        JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    is_default    BOOLEAN NOT NULL DEFAULT false,
    last_check    JSONB,
    last_check_at TIMESTAMPTZ,
    last_print_at TIMESTAMPTZ,
    created_by    UUID REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO permissions (key, label, category) VALUES
    ('printers.manage', 'Drucker einrichten und prüfen', 'Drucker'),
    ('printers.use',    'Direkt auf Drucker drucken',   'Drucker')
ON CONFLICT (key) DO NOTHING;

-- Einrichten: Administratoren; direkt drucken: alle Rollen (wie bisher der Etikettendruck)
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE (r.key = 'admin' AND p.key IN ('printers.manage', 'printers.use'))
   OR p.key = 'printers.use'
ON CONFLICT DO NOTHING;
