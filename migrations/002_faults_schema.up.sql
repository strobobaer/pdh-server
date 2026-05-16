-- 002_faults_schema.up.sql

CREATE TYPE fault_severity AS ENUM ('low', 'medium', 'high', 'critical');
CREATE TYPE fault_status AS ENUM ('detected', 'analyzing', 'in_progress', 'resolved', 'closed');

-- ============================================================
-- STÖRUNGEN
-- ============================================================
CREATE TABLE faults (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title             VARCHAR(255) NOT NULL,
    description       TEXT,
    symptoms          JSONB NOT NULL DEFAULT '[]',
    severity          fault_severity NOT NULL DEFAULT 'medium',
    status            fault_status NOT NULL DEFAULT 'detected',
    infrastructure_id UUID REFERENCES infrastructure(id),
    assigned_to       UUID REFERENCES users(id),
    created_by        UUID NOT NULL REFERENCES users(id),
    resolution        TEXT,
    root_cause        TEXT,
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_faults_status   ON faults(status);
CREATE INDEX idx_faults_severity ON faults(severity);
CREATE INDEX idx_faults_infra    ON faults(infrastructure_id);
CREATE INDEX idx_faults_symptoms ON faults USING GIN(symptoms);

-- ============================================================
-- COPILOT ANALYSEN
-- ============================================================
CREATE TABLE fault_analyses (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fault_id        UUID NOT NULL REFERENCES faults(id) ON DELETE CASCADE,
    summary         TEXT NOT NULL,
    possible_causes JSONB NOT NULL DEFAULT '[]',
    steps           JSONB NOT NULL DEFAULT '[]',
    similar_faults  JSONB NOT NULL DEFAULT '[]',
    confidence      FLOAT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(fault_id)
);

-- ============================================================
-- WISSENSDATENBANK
-- ============================================================
CREATE TABLE knowledge_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       VARCHAR(255) NOT NULL,
    symptoms    JSONB NOT NULL DEFAULT '[]',
    solution    TEXT NOT NULL,
    category    VARCHAR(100),
    tags        JSONB NOT NULL DEFAULT '[]',
    created_by  UUID NOT NULL REFERENCES users(id),
    usage_count INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_knowledge_symptoms ON knowledge_entries USING GIN(symptoms);
CREATE INDEX idx_knowledge_tags     ON knowledge_entries USING GIN(tags);
