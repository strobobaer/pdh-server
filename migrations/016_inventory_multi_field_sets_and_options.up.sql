-- 016_inventory_multi_field_sets_and_options.up.sql
-- Mehrere Feldsaetze je Ersatzteil und Auswahllisten-Optionen fuer Stammdatenfelder.

CREATE TABLE IF NOT EXISTS spare_part_field_set_assignments (
    part_id      UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    field_set_id UUID NOT NULL REFERENCES spare_part_field_sets(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (part_id, field_set_id)
);

INSERT INTO spare_part_field_set_assignments (part_id, field_set_id)
SELECT id, custom_field_set_id
FROM spare_parts
WHERE custom_field_set_id IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS spare_part_field_options (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    field_id   UUID NOT NULL REFERENCES spare_part_field_defs(id) ON DELETE CASCADE,
    value      TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 100,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_options_unique_active
    ON spare_part_field_options (field_id, lower(value))
    WHERE active=true;

CREATE INDEX IF NOT EXISTS idx_spare_part_field_set_assignments_part
    ON spare_part_field_set_assignments(part_id);

CREATE INDEX IF NOT EXISTS idx_spare_part_field_options_field
    ON spare_part_field_options(field_id);
