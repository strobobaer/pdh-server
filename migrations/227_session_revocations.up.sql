-- 227_session_revocations.up.sql
-- Abmelden zieht das Sitzungs-Token am Server zurück (0.82.66): Fingerabdruck (SHA-256)
-- des Tokens bis zu seinem Ablauf. Gemeinsam für beide Server im Verbund; abgelaufene
-- Einträge räumt das nächste Abmelden auf. Kein neues Recht.
CREATE TABLE IF NOT EXISTS session_revocations (
    id         uuid PRIMARY KEY DEFAULT pdh_uuid_v7(),
    token_hash text NOT NULL UNIQUE,
    user_id    uuid REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS session_revocations_expires_idx ON session_revocations (expires_at);
