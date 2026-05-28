-- 013_inventory_custom_fields.up.sql
-- Frei benennbare Stammdatenfelder fuer Ersatzteile.

CREATE TABLE IF NOT EXISTS spare_part_field_defs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    field_type  VARCHAR(30) NOT NULL DEFAULT 'text',
    sort_order  INTEGER NOT NULL DEFAULT 100,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_part_field_defs_name_active
    ON spare_part_field_defs (lower(name))
    WHERE active=true;

CREATE TABLE IF NOT EXISTS spare_part_field_values (
    part_id     UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    field_id    UUID NOT NULL REFERENCES spare_part_field_defs(id) ON DELETE CASCADE,
    value       TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (part_id, field_id)
);
