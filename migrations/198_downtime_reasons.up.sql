-- 198_downtime_reasons.up.sql
--
-- Stillstandsgründe: Katalog der Gründe (wie die Rüsttasten), Zuordnung zu Anlagen
-- (gilt auch für alle Unteranlagen) und die Stillstände selbst (Beginn, Ende, Grund).
-- Rein hinzufügend: nichts Vorhandenes wird geändert. Der Grund eines Stillstands
-- ist absichtlich nicht zwingend (reason_id darf leer sein, auch wenn der Grund später
-- gelöscht wird), damit ältere Datensätze und ältere Stände weiter funktionieren.

CREATE TABLE IF NOT EXISTS downtime_reasons (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    key        TEXT NOT NULL UNIQUE,
    name       VARCHAR(80) NOT NULL,
    sort       INT NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO downtime_reasons (key, name, sort) VALUES
    ('breakdown',        'Störung / Defekt', 10),
    ('material_missing', 'Material fehlt', 20),
    ('no_staff',         'Kein Personal', 30),
    ('planned',          'Geplanter Stillstand', 40)
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS infra_downtime_reasons (
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    reason_id         UUID NOT NULL REFERENCES downtime_reasons(id) ON DELETE CASCADE,
    PRIMARY KEY (infrastructure_id, reason_id)
);
CREATE INDEX IF NOT EXISTS idx_infra_downtime_reasons_reason ON infra_downtime_reasons (reason_id);

CREATE TABLE IF NOT EXISTS downtimes (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    reason_id         UUID REFERENCES downtime_reasons(id) ON DELETE SET NULL,
    reason_name       VARCHAR(80) NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ,
    origin            TEXT NOT NULL DEFAULT 'terminal' CHECK (origin IN ('terminal', 'manual')),
    actor_name        VARCHAR(40) NOT NULL DEFAULT '',
    note              VARCHAR(1000) NOT NULL DEFAULT '',
    created_by        UUID REFERENCES users(id),
    ended_by          UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at       TIMESTAMPTZ,
    CHECK (ended_at IS NULL OR ended_at > started_at)
);
-- Höchstens ein laufender Stillstand je Anlage.
CREATE UNIQUE INDEX IF NOT EXISTS uq_downtimes_running ON downtimes (infrastructure_id) WHERE ended_at IS NULL AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_downtimes_infra ON downtimes (infrastructure_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_downtimes_reason ON downtimes (reason_id);
