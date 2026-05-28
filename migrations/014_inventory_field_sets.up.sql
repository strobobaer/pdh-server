-- 014_inventory_field_sets.up.sql
-- Feldsaetze fuer Ersatzteil-Stammdaten, z.B. Motoren, Pumpen, Sensoren.

CREATE TABLE IF NOT EXISTS spare_part_field_sets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_sets_name_active
    ON spare_part_field_sets (lower(name))
    WHERE active=true;

ALTER TABLE spare_part_field_defs
    ADD COLUMN IF NOT EXISTS field_set_id UUID REFERENCES spare_part_field_sets(id) ON DELETE SET NULL;

ALTER TABLE spare_parts
    ADD COLUMN IF NOT EXISTS custom_field_set_id UUID REFERENCES spare_part_field_sets(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_spare_part_field_defs_set
    ON spare_part_field_defs(field_set_id);

CREATE INDEX IF NOT EXISTS idx_spare_parts_custom_field_set
    ON spare_parts(custom_field_set_id);
