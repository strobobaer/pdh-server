-- 017_inventory_storage_warehouse.up.sql
-- Lagername fuer Ersatzteile separat speichern.

ALTER TABLE spare_parts
    ADD COLUMN IF NOT EXISTS storage_warehouse TEXT NOT NULL DEFAULT '';
