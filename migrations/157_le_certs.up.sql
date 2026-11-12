-- 157_le_certs.up.sql
-- Zertifikate von Let's Encrypt (DNS-01 über Cloudflare, internal/web/certs_store.go).
-- Optionales Merkmal: Ohne Einrichtung bleibt die Tabelle leer und nichts ändert sich.
-- Der private Schlüssel liegt nur verschlüsselt in der Datenbank (key_enc, prev_key_enc).

CREATE TABLE IF NOT EXISTS le_certs (
    name           TEXT PRIMARY KEY,
    staging        BOOLEAN NOT NULL,
    chain_pem      TEXT,
    key_enc        TEXT,
    not_before     TIMESTAMPTZ,
    not_after      TIMESTAMPTZ,
    fingerprint    TEXT,
    prev_chain_pem TEXT,
    prev_key_enc   TEXT,
    state          TEXT NOT NULL DEFAULT 'neu',
    step           TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    retry_after    TIMESTAMPTZ,
    challenge_id   TEXT NOT NULL DEFAULT '',
    installed_fp   TEXT NOT NULL DEFAULT '',
    lan_record_id  TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO permissions (key, label, category) VALUES
    ('system.certs', 'Zertifikate verwalten (Let''s Encrypt)', 'System & Sicherheit'),
    ('system.unifi', 'Namen im Haus eintragen (UniFi)', 'System & Sicherheit'),
    ('system.certs_admin', 'Zertifikate: entfernen und widerrufen (Bereichsadministrator)', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Die Rechte gelten systemweit. Sie gehen an alle Rollen, die bisher Cloudflare
-- verwalten dürfen – niemand wird durch das Update ausgesperrt (wie Migration 155
-- mit system.file_mirror_admin).
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('system.certs', 'system.unifi', 'system.certs_admin')
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.cloudflare')
ON CONFLICT DO NOTHING;
