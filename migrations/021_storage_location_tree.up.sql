-- 021_storage_location_tree.up.sql
--
-- Ersetzt die starre 3-Ebenen-Struktur (warehouses -> storage_locations ->
-- storage_places) durch einen generischen, beliebig tief schachtelbaren Baum
-- nach demselben Muster wie das infrastructure-Modul (parent_id + type-Enum).
--
-- Neue Typen: lagerort, regal, fach, platz.
-- "lagerort" ersetzt das bisherige "Lager" (Warehouse) als oberste Ebene.

DO $$ BEGIN
    CREATE TYPE storage_node_type AS ENUM ('lagerort', 'regal', 'fach', 'platz');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS storage_nodes (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id     UUID REFERENCES storage_nodes(id) ON DELETE CASCADE,
    name          VARCHAR(255) NOT NULL,
    type          storage_node_type NOT NULL,
    description   TEXT,
    location      VARCHAR(255), -- nur bei "lagerort" sinnvoll befüllt (ex-Warehouse.Location)
    capacity      VARCHAR(50),
    current_parts INT NOT NULL DEFAULT 0,
    active        BOOLEAN NOT NULL DEFAULT true,
    created_by    UUID NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_storage_nodes_parent ON storage_nodes(parent_id);
CREATE INDEX IF NOT EXISTS idx_storage_nodes_type   ON storage_nodes(type);

-- ── Bestehende Daten übernehmen (nur falls die alten Tabellen noch existieren) ──

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'warehouses') THEN

        -- Lager -> lagerort (oberste Ebene, kein parent_id). location wird übernommen.
        INSERT INTO storage_nodes (id, parent_id, name, type, description, location, active, created_by, created_at)
        SELECT id, NULL, name, 'lagerort', description, location, active, created_by, created_at
        FROM warehouses
        ON CONFLICT (id) DO NOTHING;

        -- Regale -> regal (Kind des jeweiligen Lagers)
        INSERT INTO storage_nodes (id, parent_id, name, type, description, active, created_by, created_at)
        SELECT sl.id, sl.warehouse_id, sl.name, 'regal', sl.description, true, w.created_by, sl.created_at
        FROM storage_locations sl
        JOIN warehouses w ON w.id = sl.warehouse_id
        ON CONFLICT (id) DO NOTHING;

        -- Fächer -> fach (Kind des jeweiligen Regals)
        INSERT INTO storage_nodes (id, parent_id, name, type, description, capacity, current_parts, active, created_by, created_at)
        SELECT sp.id, sp.storage_location_id, sp.name, 'fach', sp.description, sp.capacity, sp.current_parts,
               true, w.created_by, sp.created_at
        FROM storage_places sp
        JOIN storage_locations sl ON sl.id = sp.storage_location_id
        JOIN warehouses w ON w.id = sl.warehouse_id
        ON CONFLICT (id) DO NOTHING;

        -- Alte Tabellen NICHT löschen (Sicherheitsnetz), nur umbenennen.
        ALTER TABLE warehouses RENAME TO warehouses_legacy;
        ALTER TABLE storage_locations RENAME TO storage_locations_legacy;
        ALTER TABLE storage_places RENAME TO storage_places_legacy;
    END IF;
END $$;
