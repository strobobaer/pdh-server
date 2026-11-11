-- 157_terminal_devices.up.sql
--
-- Gerätekopplung für das Anlagenterminal (TP0b).
-- Ein Raspberry Pi („PDH-Terminal“) wird mit einem Einmalcode an einen
-- Terminal-Benutzer (users.is_system_user) gekoppelt und meldet sich danach
-- mit einem Geräte-Geheimnis an.
--   terminal_devices        gekoppelte Geräte; gespeichert wird nur der
--                           SHA-256-Hash des Geheimnisses (secret_sha256)
--   terminal_pairing_codes  Kopplungscodes (10 Minuten gültig, einmal
--                           verwendbar); gespeichert wird nur der Hash

CREATE TABLE IF NOT EXISTS terminal_devices (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    secret_sha256 TEXT NOT NULL UNIQUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS terminal_devices_user ON terminal_devices(user_id);

CREATE TABLE IF NOT EXISTS terminal_pairing_codes (
    code_sha256 TEXT PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ
);
