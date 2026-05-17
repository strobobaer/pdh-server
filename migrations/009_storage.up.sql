-- 009_storage.up.sql – Lager, Lagerorte, Lagerplätze

CREATE TABLE IF NOT EXISTS warehouses (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    location    VARCHAR(255),
    active      BOOLEAN NOT NULL DEFAULT true,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS storage_locations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    name         VARCHAR(100) NOT NULL,  -- z.B. "Regal A", "Zone 1"
    description  TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS storage_places (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    storage_location_id UUID NOT NULL REFERENCES storage_locations(id) ON DELETE CASCADE,
    name                VARCHAR(100) NOT NULL,  -- z.B. "Fach 1", "Ebene 3"
    description         TEXT,
    capacity            VARCHAR(50),
    current_parts       INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sloc_warehouse ON storage_locations(warehouse_id);
CREATE INDEX IF NOT EXISTS idx_splace_location ON storage_places(storage_location_id);

-- Beispieldaten
INSERT INTO warehouses (id, name, description, location, created_by)
SELECT gen_random_uuid(), 'Hauptlager', 'Zentrales Ersatzteillager', 'Halle B', id
FROM users WHERE role='admin' LIMIT 1
ON CONFLICT DO NOTHING;
