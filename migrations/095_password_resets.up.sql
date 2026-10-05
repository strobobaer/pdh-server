-- 095_password_resets.up.sql
--
-- "Passwort vergessen": einmalige Ruecksetz-Links per E-Mail. Gespeichert
-- wird nur der SHA-256-Hash des Tokens; der Link gilt eine Stunde und nur
-- einmal. requested_ip dient der Nachvollziehbarkeit und Begrenzung.

CREATE TABLE IF NOT EXISTS password_resets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   VARCHAR(64) NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ,
    requested_ip VARCHAR(64) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_password_resets_user ON password_resets (user_id, created_at);
