-- 204_copilot_proposals.up.sql
--
-- Vorschläge des Copilot zum Zuordnen (Import- und Export-Verbindungen): Der
-- Copilot schlägt über das Werkzeug propose_mappings eine Änderungsliste vor,
-- das PDH prüft sie und legt sie hier ab. Angelegt wird erst, wenn die Person
-- in der Seitenleiste „Übernehmen“ drückt (internal/web/copilot_proposals.go).
-- Ein Vorschlag gilt 24 Stunden und gehört der Person, die gefragt hat.

CREATE TABLE IF NOT EXISTS copilot_proposals (
    id            UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connection_id UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    direction     TEXT NOT NULL CHECK (direction IN ('import', 'export')),
    summary       TEXT NOT NULL DEFAULT '',
    rows          JSONB NOT NULL,
    links         JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
    applied_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_copilot_proposals_user ON copilot_proposals (user_id, created_at DESC);
