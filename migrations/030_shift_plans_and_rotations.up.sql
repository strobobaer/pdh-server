-- 030_shift_plans_and_rotations.up.sql
--
-- Erweitert die Schichtplanung um:
-- 1. Rotationsmuster (wiederkehrender Zyklus, z.B. "Fruh-Fruh-Spat-Frei")
-- 2. Schichtplaene (konkreter Zeitraum + Team, Status Entwurf/Veroeffentlicht)
-- 3. shift_assignments bekommt eine optionale Plan-Referenz - Zuweisungen
--    ohne Plan (plan_id IS NULL, z.B. bestehende manuelle Eintraege) bleiben
--    wie bisher sofort sichtbar; Zuweisungen mit Plan sind erst nach
--    Veroeffentlichung des Plans fuer normale Mitarbeiter sichtbar.

DO $$ BEGIN
    CREATE TYPE shift_plan_status AS ENUM ('draft', 'published');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS shift_rotation_patterns (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id     UUID NOT NULL REFERENCES shift_models(id),
    name         VARCHAR(100) NOT NULL,
    cycle_length INT NOT NULL,
    active       BOOLEAN NOT NULL DEFAULT true,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ein Eintrag je Tag im Zyklus. shift_id = NULL bedeutet "frei" an diesem Tag.
CREATE TABLE IF NOT EXISTS shift_rotation_pattern_days (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pattern_id UUID NOT NULL REFERENCES shift_rotation_patterns(id) ON DELETE CASCADE,
    day_index  INT NOT NULL,
    shift_id   UUID REFERENCES shift_definitions(id),
    UNIQUE(pattern_id, day_index)
);

CREATE TABLE IF NOT EXISTS shift_plans (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(255) NOT NULL,
    model_id     UUID NOT NULL REFERENCES shift_models(id),
    start_date   DATE NOT NULL,
    end_date     DATE NOT NULL,
    status       shift_plan_status NOT NULL DEFAULT 'draft',
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_by UUID REFERENCES users(id),
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_shift_plans_status ON shift_plans(status);
CREATE INDEX IF NOT EXISTS idx_shift_plans_dates   ON shift_plans(start_date, end_date);

ALTER TABLE shift_assignments ADD COLUMN IF NOT EXISTS plan_id UUID REFERENCES shift_plans(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_shift_assignments_plan ON shift_assignments(plan_id);
