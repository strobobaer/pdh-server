-- 006_maintenance.up.sql

DO $$ BEGIN
    CREATE TYPE maintenance_type AS ENUM ('preventive','inspection','calibration','cleaning');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE maintenance_interval AS ENUM ('daily','weekly','monthly','quarterly','yearly');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE maintenance_status AS ENUM ('open','in_progress','done','skipped');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE maintenance_priority AS ENUM ('low','medium','high','critical');
EXCEPTION WHEN duplicate_object THEN null; END $$;

-- ============================================================
-- WARTUNGSPLÄNE (wiederkehrend)
-- ============================================================
CREATE TABLE IF NOT EXISTS maintenance_plans (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              VARCHAR(255) NOT NULL,
    description       TEXT,
    type              maintenance_type NOT NULL DEFAULT 'preventive',
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    interval_type     maintenance_interval NOT NULL DEFAULT 'monthly',
    interval_days     INT NOT NULL DEFAULT 30,
    estimated_min     INT NOT NULL DEFAULT 60,
    priority          maintenance_priority NOT NULL DEFAULT 'medium',
    assigned_to       UUID REFERENCES users(id),
    active            BOOLEAN NOT NULL DEFAULT true,
    last_executed_at  TIMESTAMPTZ,
    next_due_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by        UUID NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mplan_infra  ON maintenance_plans(infrastructure_id);
CREATE INDEX IF NOT EXISTS idx_mplan_due    ON maintenance_plans(next_due_at);

-- ============================================================
-- WARTUNGSAUFTRÄGE (einmalig)
-- ============================================================
CREATE TABLE IF NOT EXISTS maintenance_tasks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id           UUID REFERENCES maintenance_plans(id),
    title             VARCHAR(255) NOT NULL,
    description       TEXT,
    type              maintenance_type NOT NULL DEFAULT 'preventive',
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    priority          maintenance_priority NOT NULL DEFAULT 'medium',
    status            maintenance_status NOT NULL DEFAULT 'open',
    assigned_to       UUID REFERENCES users(id),
    due_date          TIMESTAMPTZ NOT NULL,
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    duration_min      INT,
    notes             TEXT,
    created_by        UUID NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mtask_status ON maintenance_tasks(status);
CREATE INDEX IF NOT EXISTS idx_mtask_due    ON maintenance_tasks(due_date);
CREATE INDEX IF NOT EXISTS idx_mtask_infra  ON maintenance_tasks(infrastructure_id);
CREATE INDEX IF NOT EXISTS idx_mtask_plan   ON maintenance_tasks(plan_id);
