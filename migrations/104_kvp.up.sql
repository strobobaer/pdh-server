-- 104_kvp.up.sql
-- KVP (Kontinuierlicher Verbesserungsprozess) mit PDCA-Zyklus.
--
-- kvp_ideas    ein Verbesserungsvorschlag: Ist-Zustand, Vorschlag, Nutzen,
--              Bewertung (Nutzen/Aufwand), Entscheidung, Ursache/Ziel (Plan),
--              Wirksamkeitspruefung (Check), Standardisierung (Act),
--              Einsparung und Praemie
-- kvp_members  Miteinreichende (Team-Vorschlag)
-- kvp_actions  Massnahmenplan: wer macht was bis wann
-- kvp_comments Kommentare
--
-- Ablauf (status): submitted -> review -> plan -> do -> check -> act -> done
--                  submitted/review -> rejected | parked
-- Abgeschlossene (done) und abgelehnte (rejected) Vorschlaege wandern ins Archiv.

CREATE SEQUENCE IF NOT EXISTS kvp_number_seq;

CREATE TABLE IF NOT EXISTS kvp_ideas (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number            INT NOT NULL DEFAULT nextval('kvp_number_seq') UNIQUE,
    title             VARCHAR(200) NOT NULL,
    category          VARCHAR(20)  NOT NULL DEFAULT 'other'
                      CHECK (category IN ('safety', 'quality', 'cost', 'productivity', 'environment', 'ergonomics', 'order', 'other')),
    problem           TEXT NOT NULL DEFAULT '',  -- Ist-Zustand
    proposal          TEXT NOT NULL DEFAULT '',  -- Loesungsvorschlag / Soll-Zustand
    benefit           TEXT NOT NULL DEFAULT '',  -- erwarteter Nutzen
    status            VARCHAR(20) NOT NULL DEFAULT 'submitted'
                      CHECK (status IN ('submitted', 'review', 'plan', 'do', 'check', 'act', 'done', 'rejected', 'parked')),
    submitter_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE SET NULL,
    cost_center_id    UUID REFERENCES cost_centers(id) ON DELETE SET NULL,
    reviewer_id       UUID REFERENCES users(id) ON DELETE SET NULL, -- bewertet / KVP-Moderation
    responsible_to    UUID REFERENCES users(id) ON DELETE SET NULL, -- setzt um
    -- Bewertung
    benefit_score     SMALLINT NOT NULL DEFAULT 0 CHECK (benefit_score BETWEEN 0 AND 5),
    effort_score      SMALLINT NOT NULL DEFAULT 0 CHECK (effort_score BETWEEN 0 AND 5),
    est_savings       NUMERIC(12,2) NOT NULL DEFAULT 0, -- EUR/Jahr geschaetzt
    est_cost          NUMERIC(12,2) NOT NULL DEFAULT 0, -- einmalige Kosten geschaetzt
    decision_note     TEXT NOT NULL DEFAULT '',
    decided_at        TIMESTAMPTZ,
    decided_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Plan
    root_cause        TEXT NOT NULL DEFAULT '',
    target            TEXT NOT NULL DEFAULT '',  -- messbares Ziel
    due_date          DATE,                      -- Umsetzung bis
    -- Check
    check_result      VARCHAR(20) NOT NULL DEFAULT '' CHECK (check_result IN ('', 'effective', 'not_effective')),
    check_note        TEXT NOT NULL DEFAULT '',
    checked_at        TIMESTAMPTZ,
    checked_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Act
    standardization   TEXT NOT NULL DEFAULT '',
    actual_savings    NUMERIC(12,2) NOT NULL DEFAULT 0, -- EUR/Jahr tatsaechlich
    actual_cost       NUMERIC(12,2) NOT NULL DEFAULT 0,
    bonus             NUMERIC(10,2) NOT NULL DEFAULT 0, -- Praemie EUR
    -- Fristen und Verlauf
    feedback_due      DATE,                      -- Rueckmeldung an Einreichende bis
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at         TIMESTAMPTZ,
    archived_at       TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_kvp_ideas_status ON kvp_ideas(status) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_kvp_ideas_created ON kvp_ideas(created_at);
CREATE INDEX IF NOT EXISTS idx_kvp_ideas_submitter ON kvp_ideas(submitter_id);

CREATE TABLE IF NOT EXISTS kvp_members (
    idea_id UUID NOT NULL REFERENCES kvp_ideas(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (idea_id, user_id)
);

CREATE TABLE IF NOT EXISTS kvp_actions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idea_id        UUID NOT NULL REFERENCES kvp_ideas(id) ON DELETE CASCADE,
    description    TEXT NOT NULL,
    responsible_id UUID REFERENCES users(id) ON DELETE SET NULL,
    due_date       DATE,
    done_at        TIMESTAMPTZ,
    done_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_kvp_actions_idea ON kvp_actions(idea_id);

CREATE TABLE IF NOT EXISTS kvp_comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idea_id    UUID NOT NULL REFERENCES kvp_ideas(id) ON DELETE CASCADE,
    user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    text       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_kvp_comments_idea ON kvp_comments(idea_id);

INSERT INTO permissions (key, label, category) VALUES
    ('kvp.manage', 'KVP steuern (Vorschläge bewerten, entscheiden, Prämien, alle bearbeiten)', 'KVP')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key = 'kvp.manage'
ON CONFLICT DO NOTHING;
