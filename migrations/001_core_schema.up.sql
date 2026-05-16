-- 001_core_schema.up.sql
-- PDH Core Schema: Users, Shifts, Calendar, Infrastructure

-- UUID Erweiterung
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- BENUTZER
-- ============================================================
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      VARCHAR(50) UNIQUE NOT NULL,
    email         VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    first_name    VARCHAR(100) NOT NULL,
    last_name     VARCHAR(100) NOT NULL,
    role          VARCHAR(50) NOT NULL DEFAULT 'worker',
    department    VARCHAR(100),
    phone         VARCHAR(50),
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_role ON users(role);

-- ============================================================
-- SCHICHTMODELLE
-- ============================================================
CREATE TABLE shift_models (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    shifts_per_day INT NOT NULL DEFAULT 3,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE shift_definitions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id       UUID NOT NULL REFERENCES shift_models(id) ON DELETE CASCADE,
    name           VARCHAR(50) NOT NULL,  -- Frühschicht, Spätschicht, Nachtschicht
    start_time     TIME NOT NULL,
    end_time       TIME NOT NULL,
    color          VARCHAR(7) DEFAULT '#3B82F6'
);

CREATE TABLE shift_assignments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    shift_id   UUID NOT NULL REFERENCES shift_definitions(id),
    date       DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, date)
);

CREATE INDEX idx_shifts_date ON shift_assignments(date);
CREATE INDEX idx_shifts_user ON shift_assignments(user_id);

-- ============================================================
-- URLAUB & ABWESENHEIT
-- ============================================================
CREATE TYPE absence_type AS ENUM ('vacation', 'sick', 'training', 'other');
CREATE TYPE absence_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE absences (
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

CREATE INDEX idx_absences_user ON absences(user_id);
CREATE INDEX idx_absences_dates ON absences(start_date, end_date);

-- ============================================================
-- KALENDER
-- ============================================================
CREATE TABLE calendar_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       VARCHAR(255) NOT NULL,
    description TEXT,
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ NOT NULL,
    all_day     BOOLEAN NOT NULL DEFAULT false,
    color       VARCHAR(7) DEFAULT '#3B82F6',
    is_public   BOOLEAN NOT NULL DEFAULT false,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_time ON calendar_events(start_time, end_time);

-- ============================================================
-- INFRASTRUKTUR: Bauwerke → Linien → Anlagen → Geräte
-- ============================================================
CREATE TYPE infra_type AS ENUM ('building', 'line', 'plant', 'device');

CREATE TABLE infrastructure (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id   UUID REFERENCES infrastructure(id),
    name        VARCHAR(255) NOT NULL,
    type        infra_type NOT NULL,
    description TEXT,
    location    VARCHAR(255),
    serial_no   VARCHAR(100),
    manufacturer VARCHAR(100),
    model       VARCHAR(100),
    installed_at DATE,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_infra_parent ON infrastructure(parent_id);
CREATE INDEX idx_infra_type ON infrastructure(type);

-- ============================================================
-- TICKETS
-- ============================================================
CREATE TYPE ticket_priority AS ENUM ('low', 'medium', 'high', 'critical');
CREATE TYPE ticket_status AS ENUM ('open', 'in_progress', 'pending', 'resolved', 'closed');

CREATE TABLE tickets (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title             VARCHAR(255) NOT NULL,
    description       TEXT,
    priority          ticket_priority NOT NULL DEFAULT 'medium',
    status            ticket_status NOT NULL DEFAULT 'open',
    assigned_to       UUID REFERENCES users(id),
    created_by        UUID NOT NULL REFERENCES users(id),
    infrastructure_id UUID REFERENCES infrastructure(id),
    due_date          TIMESTAMPTZ,
    resolved_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tickets_status ON tickets(status);
CREATE INDEX idx_tickets_priority ON tickets(priority);
CREATE INDEX idx_tickets_assigned ON tickets(assigned_to);
CREATE INDEX idx_tickets_infra ON tickets(infrastructure_id);

CREATE TABLE ticket_comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id  UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id),
    text       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================
-- ZEITERFASSUNG
-- ============================================================
CREATE TYPE time_ref_type AS ENUM ('ticket', 'task', 'project', 'maintenance', 'production');

CREATE TABLE time_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id),
    ref_type    time_ref_type NOT NULL,
    ref_id      UUID NOT NULL,
    description TEXT,
    started_at  TIMESTAMPTZ NOT NULL,
    ended_at    TIMESTAMPTZ,
    duration_min INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_time_user ON time_entries(user_id);
CREATE INDEX idx_time_ref ON time_entries(ref_type, ref_id);
CREATE INDEX idx_time_date ON time_entries(started_at);
