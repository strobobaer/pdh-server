-- 004_timetracking.up.sql

DO $$ BEGIN
    CREATE TYPE time_ref_type AS ENUM ('ticket','fault','project','maintenance','production');
EXCEPTION WHEN duplicate_object THEN null; END $$;

ALTER TYPE time_ref_type ADD VALUE IF NOT EXISTS 'fault';

CREATE TABLE IF NOT EXISTS time_entries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id),
    ref_type     time_ref_type NOT NULL,
    ref_id       UUID NOT NULL,
    description  TEXT,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at     TIMESTAMPTZ,
    duration_min INT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_time_user    ON time_entries(user_id);
CREATE INDEX IF NOT EXISTS idx_time_ref     ON time_entries(ref_type, ref_id);
CREATE INDEX IF NOT EXISTS idx_time_date    ON time_entries(started_at);
CREATE INDEX IF NOT EXISTS idx_time_running ON time_entries(user_id) WHERE ended_at IS NULL;
