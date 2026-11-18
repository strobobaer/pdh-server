-- 160_datanet.up.sql
-- Datennetz (Paket 1): PDH-Daten-Clients melden sich beim Server und geben
-- Datenbankziele frei. Der Server speichert je Client nur den Hash des Tokens.
-- Optionales Merkmal: Ohne Client bleiben die Tabellen leer.

CREATE TABLE IF NOT EXISTS datanet_clients (
    id           UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name         VARCHAR(80) NOT NULL,
    net          VARCHAR(10) NOT NULL DEFAULT 'cloud' CHECK (net IN ('cloud', 'intern')),
    token_hash   CHAR(64) NOT NULL,
    addr         VARCHAR(64) NOT NULL DEFAULT '',
    hostname     VARCHAR(120) NOT NULL DEFAULT '',
    version      VARCHAR(40) NOT NULL DEFAULT '',
    os           VARCHAR(40) NOT NULL DEFAULT '',
    -- Letzte Statusmeldung der Ziele (datanet.TargetStatus als Liste).
    targets      JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_seen_at TIMESTAMPTZ,
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ,
    revoked_by   UUID REFERENCES users(id) ON DELETE SET NULL
);
-- Ein Name nur einmal unter den aktiven Clients; gesperrte geben ihn frei.
CREATE UNIQUE INDEX IF NOT EXISTS idx_datanet_clients_name ON datanet_clients (name) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS datanet_events (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    client_id   UUID REFERENCES datanet_clients(id) ON DELETE SET NULL,
    client_name VARCHAR(80) NOT NULL DEFAULT '',
    action      VARCHAR(20) NOT NULL,
    note        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_datanet_events_at ON datanet_events (at DESC);

-- ── Rechte ────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('datanet.view',  'Datennetz: Clients und Ziele sehen', 'Datennetz'),
    ('datanet.edit',  'Datennetz: Clients anlegen und sperren', 'Datennetz'),
    ('datanet.admin', 'Datennetz: Clients löschen (Bereichsadministrator)', 'Datennetz')
ON CONFLICT (key) DO NOTHING;

-- Sehen und Einrichten bekommen nur Rollen, die schon die Server-Verbindungen
-- verwalten: system.server_config oder sync.admin, jeweils ohne Abteilung (ein nur
-- abteilungsweites Recht ergibt kein globales datanet.*). Rollen, die
-- nur vpn.use haben, bekommen nichts – vpn.use erlaubt lediglich die Nutzung
-- eigener VPN-Geräte. Niemand wird ausgesperrt, niemand bekommt mehr als nötig.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('datanet.view', 'datanet.edit')
  AND (EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
    OR EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'sync.admin'))
ON CONFLICT DO NOTHING;

-- Bereichsadministrator (einziges Löschrecht): Rolle admin, Rollen mit
-- sync.admin (Bereichsadministrator der Verbindungen/Abgleich) und Rollen mit
-- globalem system.server_config, die schon in einem anderen Bereich
-- Bereichsadministrator sind.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'datanet.admin'
  AND (r.key = 'admin'
       OR EXISTS (
            SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
            WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'sync.admin')
       OR (EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
           AND EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND o.key LIKE '%.admin' AND o.key <> 'datanet.admin')))
ON CONFLICT DO NOTHING;
