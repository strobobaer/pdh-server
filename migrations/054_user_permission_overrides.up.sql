-- 054_user_permission_overrides.up.sql
--
-- Einzelberechtigungen pro Benutzer, zusaetzlich zur Rollen-Matrix.
-- granted=true  -> Berechtigung ist fuer diesen Benutzer explizit erlaubt,
--                  auch wenn die Rolle sie nicht hat.
-- granted=false -> Berechtigung ist fuer diesen Benutzer explizit
--                  entzogen, auch wenn die Rolle sie hat.
-- Kein Eintrag   -> es gilt die Rollen-Matrix (Standardfall).

CREATE TABLE IF NOT EXISTS user_permissions (
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    granted       BOOLEAN NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, permission_id)
);

CREATE INDEX IF NOT EXISTS idx_user_permissions_user ON user_permissions(user_id);
