-- Passwort-Richtlinien (Server-Einstellungen → Passwörter) und Passwortwechsel-Zwang:
--   must_change_password  bei der naechsten Anmeldung mit Passwort muss es geaendert werden
--                         (Erstanmeldung, Passwort vom Administrator gesetzt, manuell verlangt)
--   password_changed_at   fuer das Hoechstalter eines Passworts
--   password_history      letzte Passwort-Hashes gegen Wiederverwendung
-- Die Richtlinie selbst steht als JSON in app_settings (Schluessel password_policy).

ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMPTZ;
-- bestehende Passwoerter gelten ab heute – sonst liefen sie bei einem Hoechstalter sofort ab
UPDATE users SET password_changed_at = NOW() WHERE password_changed_at IS NULL AND COALESCE(password_hash, '') <> '';

CREATE TABLE IF NOT EXISTS password_history (
    id            BIGSERIAL PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_password_history_user ON password_history (user_id, created_at DESC);
