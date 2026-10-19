-- 126_pki_certificates.up.sql
-- Eigene Zertifizierungsstelle (CA) des PDH für Nextcloud (internal/web/pki.go):
--   * client: persönliches Zertifikat einer Person für die Nextcloud-App (mTLS)
--   * device: Geräte-/Firmenzertifikat ohne Person (z. B. für Firmenhandys)
--   * server: Server-Zertifikat für Nextcloud (eigener Name/IP), Geräte vertrauen der CA
-- Die CA selbst liegt verschlüsselt in app_settings (pki.ca_cert / pki.ca_key).
-- p12-Datei und Passwort bleiben verschlüsselt gespeichert, damit man sie
-- erneut herunterladen kann, bis das Zertifikat gesperrt wird.

CREATE TABLE IF NOT EXISTS pki_certificates (
    id            UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    kind          VARCHAR(10) NOT NULL CHECK (kind IN ('client', 'device', 'server')),
    serial        TEXT NOT NULL UNIQUE,              -- hex
    common_name   VARCHAR(255) NOT NULL,
    label         VARCHAR(100) NOT NULL DEFAULT '',  -- z. B. „Handy“, „Laptop“
    user_id       UUID REFERENCES users(id) ON DELETE CASCADE, -- nur client
    hostnames     TEXT NOT NULL DEFAULT '',          -- nur server, kommagetrennt
    not_before    TIMESTAMPTZ NOT NULL,
    not_after     TIMESTAMPTZ NOT NULL,
    cert_pem      TEXT NOT NULL,
    key_enc       TEXT NOT NULL DEFAULT '',          -- verschlüsselter PEM-Schlüssel (nur server, für nginx)
    p12_enc       TEXT NOT NULL DEFAULT '',          -- verschlüsselte .p12 (Base64)
    password_enc  TEXT NOT NULL DEFAULT '',          -- verschlüsseltes p12-Passwort
    revoked_at    TIMESTAMPTZ,
    revoked_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pki_certificates_user ON pki_certificates(user_id) WHERE user_id IS NOT NULL;

INSERT INTO permissions (key, label, category) VALUES
    ('pki.manage', 'Zertifikate (Nextcloud-App, Geräte, Server) ausstellen & sperren', 'System')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'pki.manage'
ON CONFLICT DO NOTHING;
