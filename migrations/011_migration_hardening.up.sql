-- 011_migration_hardening.up.sql
-- Idempotente Korrekturen fuer Legacy-Datenbanken ohne Migration-Historie.

ALTER TABLE shift_models
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE shift_definitions
    ADD COLUMN IF NOT EXISTS short_name VARCHAR(10),
    ADD COLUMN IF NOT EXISTS is_night BOOLEAN NOT NULL DEFAULT false;

UPDATE shift_definitions
SET short_name = COALESCE(NULLIF(short_name, ''), LEFT(name, 1))
WHERE short_name IS NULL OR short_name = '';

ALTER TABLE shift_definitions
    ALTER COLUMN short_name SET DEFAULT '',
    ALTER COLUMN short_name SET NOT NULL;

UPDATE shift_definitions
SET color = '#3B82F6'
WHERE color IS NULL;

ALTER TABLE shift_definitions
    ALTER COLUMN color SET DEFAULT '#3B82F6',
    ALTER COLUMN color SET NOT NULL;

ALTER TABLE shift_assignments
    ADD COLUMN IF NOT EXISTS note TEXT,
    ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id);

ALTER TYPE time_ref_type ADD VALUE IF NOT EXISTS 'fault';

CREATE INDEX IF NOT EXISTS idx_shift_def_model ON shift_definitions(model_id);
CREATE INDEX IF NOT EXISTS idx_shift_assign_date ON shift_assignments(date);
CREATE INDEX IF NOT EXISTS idx_shift_assign_user ON shift_assignments(user_id);
CREATE INDEX IF NOT EXISTS idx_time_running ON time_entries(user_id) WHERE ended_at IS NULL;
