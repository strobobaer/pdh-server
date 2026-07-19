-- 027_inventory_per_location_stock.up.sql
--
-- Stellt die Ersatzteil-Bestandsfuehrung von "ein Gesamtbestand pro Teil" auf
-- "Bestand pro Teil UND Lagerort" um (Lagerort = storage_nodes, der neue
-- Lagerort-Baum). Ein Ersatzteil kann jetzt an mehreren Plaetzen liegen.
--
-- spare_parts.stock_qty bleibt als GECACHTER Gesamtwert (Summe ueber alle
-- Lagerorte) erhalten, damit bestehende Abfragen (Mindestbestand, Status,
-- Lagerwert) unveraendert weiterfunktionieren.
--
-- WICHTIG: Diese Version ist idempotent - sicher mehrfach ausfuehrbar, auch
-- wenn Teile davon (z.B. die Spalten-Umbenennung) schon einmal gelaufen sind.

CREATE TABLE IF NOT EXISTS spare_part_stock (
    part_id         UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id) ON DELETE CASCADE,
    qty             NUMERIC(12,3) NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (part_id, storage_node_id)
);

CREATE INDEX IF NOT EXISTS idx_spare_part_stock_node ON spare_part_stock(storage_node_id);
CREATE INDEX IF NOT EXISTS idx_spare_part_stock_part ON spare_part_stock(part_id);

DO $$ BEGIN
    ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'inventory';
EXCEPTION WHEN duplicate_object THEN null; END $$;

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS storage_node_id UUID REFERENCES storage_nodes(id);

-- Umbenennung NUR falls die Spalte noch unter dem alten Namen existiert
-- (idempotent - kein Fehler mehr bei wiederholtem Lauf).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='spare_parts' AND column_name='storage_warehouse') THEN
        ALTER TABLE spare_parts RENAME COLUMN storage_warehouse TO storage_warehouse_legacy;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='spare_parts' AND column_name='storage_location') THEN
        ALTER TABLE spare_parts RENAME COLUMN storage_location TO storage_location_legacy;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='spare_parts' AND column_name='storage_place') THEN
        ALTER TABLE spare_parts RENAME COLUMN storage_place TO storage_place_legacy;
    END IF;
END $$;

-- Bestehenden Gesamtbestand als "Nicht zugeordnet" uebernehmen - idempotent
-- durch ON CONFLICT DO NOTHING.
DO $$
DECLARE
    admin_user      UUID;
    unassigned_node UUID;
    rec             RECORD;
BEGIN
    SELECT id INTO admin_user FROM users WHERE active = true ORDER BY created_at LIMIT 1;
    IF admin_user IS NULL THEN
        RETURN;
    END IF;

    SELECT id INTO unassigned_node FROM storage_nodes WHERE type = 'lagerort' AND name = 'Nicht zugeordnet' LIMIT 1;
    IF unassigned_node IS NULL THEN
        INSERT INTO storage_nodes (parent_id, name, type, created_by)
        VALUES (NULL, 'Nicht zugeordnet', 'lagerort', admin_user)
        RETURNING id INTO unassigned_node;
    END IF;

    FOR rec IN SELECT id, stock_qty FROM spare_parts WHERE active = true AND stock_qty > 0 LOOP
        INSERT INTO spare_part_stock (part_id, storage_node_id, qty)
        VALUES (rec.id, unassigned_node, rec.stock_qty)
        ON CONFLICT (part_id, storage_node_id) DO NOTHING;
    END LOOP;
END $$;
