-- 155_kiosk_terminals.up.sql
--
-- Kiosk-Terminals (hmi-kiosk am Bedienplatz): PDH schickt einen Fernbefehl,
-- das Terminal zeigt das HMI auf seinem eigenen Bildschirm (kiosk_terminals.go).
--   base_url      https://<terminal>:<port> (HTTPS-Admin-Listener des Terminals)
--   api_key_enc   Schlüssel hmik_… verschlüsselt (AES-GCM, wie VNC-Passwörter)
--   pubkey_sha256 SHA-256 über SubjectPublicKeyInfo des Terminal-Zertifikats,
--                 64 Hex-Zeichen klein; nur diesem Fingerabdruck wird vertraut.
-- Jede Anlage hat höchstens ein Terminal (infrastructure.kiosk_terminal_id).
-- Befehle an ein Terminal stehen im HMI-Zugriffsprotokoll mit dem Ziel-Terminal
-- (kiosk_terminal_id, Name zum Zeitpunkt des Befehls); Modus bleibt view/control.

CREATE TABLE IF NOT EXISTS kiosk_terminals (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 120),
    base_url      TEXT NOT NULL CHECK (base_url LIKE 'https://%'),
    api_key_enc   TEXT NOT NULL DEFAULT '',
    pubkey_sha256 TEXT NOT NULL CHECK (pubkey_sha256 ~ '^[0-9a-f]{64}$'),
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS kiosk_terminal_id UUID REFERENCES kiosk_terminals(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_infrastructure_kiosk_terminal ON infrastructure (kiosk_terminal_id) WHERE kiosk_terminal_id IS NOT NULL;

ALTER TABLE infrastructure_hmi_sessions ADD COLUMN IF NOT EXISTS kiosk_terminal_id UUID REFERENCES kiosk_terminals(id) ON DELETE SET NULL;
ALTER TABLE infrastructure_hmi_sessions ADD COLUMN IF NOT EXISTS kiosk_terminal_name TEXT NOT NULL DEFAULT '';

INSERT INTO permissions (key, label, category) VALUES
    ('hmi.kiosk', 'HMI am Kiosk-Terminal der Anlage zeigen', 'Infrastruktur & Anlagen')
ON CONFLICT (key) DO NOTHING;

-- Startbelegung wie „HMI ansehen“: Admin, Manager, Techniker
-- (dazu Anlagenverantwortliche, die alles dürfen, was Techniker dürfen).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'hmi.kiosk' AND r.key IN ('admin', 'manager', 'technician', 'asset_manager')
ON CONFLICT DO NOTHING;
