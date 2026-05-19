-- 003_shifts_schema.up.sql

-- ============================================================
-- SCHICHTMODELLE
-- ============================================================
CREATE TABLE IF NOT EXISTS shift_models (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE shift_models
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;

-- ============================================================
-- SCHICHTDEFINITIONEN
-- ============================================================
CREATE TABLE IF NOT EXISTS shift_definitions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id    UUID NOT NULL REFERENCES shift_models(id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    short_name  VARCHAR(10) NOT NULL,
    start_time  TIME NOT NULL,
    end_time    TIME NOT NULL,
    color       VARCHAR(7) NOT NULL DEFAULT '#3B82F6',
    is_night    BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_shift_def_model ON shift_definitions(model_id);

ALTER TABLE shift_definitions
    ADD COLUMN IF NOT EXISTS short_name VARCHAR(10),
    ADD COLUMN IF NOT EXISTS is_night BOOLEAN NOT NULL DEFAULT false;

UPDATE shift_definitions
SET short_name = COALESCE(NULLIF(short_name, ''), LEFT(name, 1))
WHERE short_name IS NULL OR short_name = '';

ALTER TABLE shift_definitions
    ALTER COLUMN short_name SET DEFAULT '',
    ALTER COLUMN short_name SET NOT NULL;

-- ============================================================
-- SCHICHTZUWEISUNGEN
-- ============================================================
CREATE TABLE IF NOT EXISTS shift_assignments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id),
    shift_id    UUID NOT NULL REFERENCES shift_definitions(id),
    date        DATE NOT NULL,
    note        TEXT,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, date)
);

CREATE INDEX IF NOT EXISTS idx_shift_assign_date ON shift_assignments(date);
CREATE INDEX IF NOT EXISTS idx_shift_assign_user ON shift_assignments(user_id);

ALTER TABLE shift_assignments
    ADD COLUMN IF NOT EXISTS note TEXT,
    ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id);

-- ============================================================
-- ABWESENHEITEN
-- ============================================================
DO $$ BEGIN
    CREATE TYPE absence_type AS ENUM ('vacation','sick','training','other');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE absence_status AS ENUM ('pending','approved','rejected');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS absences (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id),
    type        absence_type NOT NULL DEFAULT 'vacation',
    status      absence_status NOT NULL DEFAULT 'pending',
    start_date  DATE NOT NULL,
    end_date    DATE NOT NULL,
    days        INT NOT NULL,
    note        TEXT,
    approved_by UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_absences_user ON absences(user_id);
CREATE INDEX IF NOT EXISTS idx_absences_dates ON absences(start_date, end_date);

-- ============================================================
-- STANDARD-SCHICHTMODELL: 3-Schicht
-- ============================================================
INSERT INTO shift_models (id, name, description) VALUES
    ('00000000-0000-0000-0000-000000000001',
     '3-Schicht Modell',
     'Früh-, Spät- und Nachtschicht')
ON CONFLICT DO NOTHING;

INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time, color, is_night) VALUES
    ('00000000-0000-0000-0000-000000000001', 'Frühschicht',  'F', '06:00', '14:00', '#10B981', false),
    ('00000000-0000-0000-0000-000000000001', 'Spätschicht',  'S', '14:00', '22:00', '#F59E0B', false),
    ('00000000-0000-0000-0000-000000000001', 'Nachtschicht', 'N', '22:00', '06:00', '#6366F1', true)
ON CONFLICT DO NOTHING;
