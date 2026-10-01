-- 086_trainings.up.sql
-- Schulungs- und Qualifikationsmatrix mit unterschriebenen Nachweisen.
--
-- training_topics        Katalog: Schulungen und Qualifikationen mit Intervall
-- training_requirements  wer sie braucht: je Rolle, Abteilung, Gruppe oder Person
-- training_sessions      ein Termin = ein Nachweis (Inhalt in Stichpunkten,
--                        Schulende/r, Teilnehmende); nach vollstaendiger
--                        Unterschrift archiviert und unveraenderlich
-- training_participants  Teilnehmende mit Unterschrift (PNG als Data-URL)

CREATE TABLE IF NOT EXISTS training_topics (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           VARCHAR(150) NOT NULL,
    kind           VARCHAR(20)  NOT NULL DEFAULT 'training' CHECK (kind IN ('training', 'qualification')),
    description    TEXT NOT NULL DEFAULT '',
    interval_months INT NOT NULL DEFAULT 12 CHECK (interval_months >= 0), -- 0 = einmalig, kein Ablauf
    lead_days      INT NOT NULL DEFAULT 30 CHECK (lead_days >= 0),        -- so viele Tage vor Ablauf faellig
    responsible_id UUID REFERENCES users(id) ON DELETE SET NULL,
    active         BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_training_topics_name ON training_topics (lower(name)) WHERE active;

CREATE TABLE IF NOT EXISTS training_requirements (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    topic_id      UUID NOT NULL REFERENCES training_topics(id) ON DELETE CASCADE,
    role_key      VARCHAR(50),
    department_id UUID REFERENCES departments(id) ON DELETE CASCADE,
    group_id      UUID REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id       UUID REFERENCES users(id) ON DELETE CASCADE,
    CHECK (num_nonnulls(role_key, department_id, group_id, user_id) = 1)
);
CREATE INDEX IF NOT EXISTS idx_training_requirements_topic ON training_requirements(topic_id);

CREATE TABLE IF NOT EXISTS training_sessions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    topic_id          UUID NOT NULL REFERENCES training_topics(id) ON DELETE RESTRICT,
    session_date      DATE,
    location          VARCHAR(200) NOT NULL DEFAULT '',
    trainer_id        UUID REFERENCES users(id) ON DELETE SET NULL,
    trainer_name      VARCHAR(200) NOT NULL DEFAULT '', -- externe/r Schulende/r
    content           TEXT NOT NULL DEFAULT '',         -- Stichpunkte, je Zeile einer
    notes             TEXT NOT NULL DEFAULT '',
    status            VARCHAR(20) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'archived')),
    auto_created      BOOLEAN NOT NULL DEFAULT false,
    trainer_signature TEXT,
    trainer_signed_at TIMESTAMPTZ,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at       TIMESTAMPTZ,
    archived_by       UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_training_sessions_topic ON training_sessions(topic_id, status);

CREATE TABLE IF NOT EXISTS training_participants (
    session_id UUID NOT NULL REFERENCES training_sessions(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT, -- Nachweise bleiben erhalten
    signature  TEXT,
    signed_at  TIMESTAMPTZ,
    signed_by  UUID REFERENCES users(id) ON DELETE SET NULL, -- wer am Geraet angemeldet war
    PRIMARY KEY (session_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_training_participants_user ON training_participants(user_id);

-- Archivierte Nachweise sind unveraenderlich
CREATE OR REPLACE FUNCTION pdh_training_session_locked() RETURNS trigger AS $$
BEGIN
    IF TG_TABLE_NAME = 'training_sessions' THEN
        IF OLD.status = 'archived' THEN
            RAISE EXCEPTION 'Archivierter Schulungsnachweis kann nicht geändert werden';
        END IF;
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;
    IF EXISTS (SELECT 1 FROM training_sessions s
                WHERE s.id = COALESCE(OLD.session_id, NEW.session_id) AND s.status = 'archived') THEN
        RAISE EXCEPTION 'Archivierter Schulungsnachweis kann nicht geändert werden';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_training_session_locked ON training_sessions;
CREATE TRIGGER trg_pdh_training_session_locked BEFORE UPDATE OR DELETE ON training_sessions
    FOR EACH ROW EXECUTE FUNCTION pdh_training_session_locked();
DROP TRIGGER IF EXISTS trg_pdh_training_participants_locked ON training_participants;
CREATE TRIGGER trg_pdh_training_participants_locked BEFORE INSERT OR UPDATE OR DELETE ON training_participants
    FOR EACH ROW EXECUTE FUNCTION pdh_training_session_locked();

INSERT INTO permissions (key, label, category) VALUES
    ('trainings.manage', 'Schulungen & Qualifikationen verwalten (Katalog, alle Nachweise, Matrix)', 'Personal')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key = 'trainings.manage'
ON CONFLICT DO NOTHING;
