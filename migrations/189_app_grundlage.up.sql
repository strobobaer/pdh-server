-- 189_app_grundlage.up.sql
--
-- PDH-App, Paket 1 (Server-Grundlage): Rechte, Tabellen für Geräteanfragen und
-- gekoppelte Telefone, Systembenutzer „PDH-App“ und Gruppe „App-Administratoren“.
--   app_requests  Anfragen aus der App (Name, Telefon, öffentlicher Schlüssel);
--                 pro Schlüssel höchstens eine offene Anfrage
--   app_devices   freigegebene Telefone; gespeichert wird nur der SHA-256-Hash
--                 des Tokens (token_sha256), Einschränkungen je Gerät
--   tickets.app_request_id  verknüpft das APP-Ticket mit der Anfrage
--
-- Rechte (Kategorie „App“): app.use, app.create, app.cockpit, app.sensors.view,
-- app.admin. Niemand wird ausgesperrt: app.use bekommen alle bestehenden Rollen,
-- app.create und app.admin Administrator und Manager, Cockpits und Sensoren der
-- Administrator. Später angelegte Rollen bekommen die Rechte nicht von selbst.
-- Alle neuen Kennungen sind UUIDv7 (pdh_uuid_v7()).

-- ── Systembenutzer „PDH-App“ (wie pdh-system, Migration 072) ──────────
-- Inaktiv + ungültiger Kennwort-Hash: kann sich nie anmelden.
INSERT INTO users (id, username, email, password_hash, first_name, last_name, role, active, is_bot)
VALUES ('00000000-0000-4000-8000-0000000a0001', 'pdh-app', 'pdh-app@localhost', '!', 'PDH-App', '', 'worker', false, true)
ON CONFLICT DO NOTHING;

-- ── Gruppe „App-Administratoren“ ──────────────────────────────────────
INSERT INTO user_groups (id, name, description)
VALUES ('00000000-0000-4000-8000-0000000a0002', 'App-Administratoren', 'Prüfen Anfragen für die PDH-App und geben Geräte frei')
ON CONFLICT DO NOTHING;

-- ── Tabellen ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS app_requests (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name            TEXT NOT NULL,
    department_id   UUID REFERENCES departments(id) ON DELETE SET NULL,
    device_label    TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    app_version     TEXT NOT NULL,
    public_key      TEXT NOT NULL,
    key_fingerprint TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    ticket_id       UUID REFERENCES tickets(id) ON DELETE SET NULL,
    device_id       UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at      TIMESTAMPTZ,
    decided_by      UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS app_requests_pending_fp ON app_requests (key_fingerprint) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS app_devices (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    request_id      UUID REFERENCES app_requests(id) ON DELETE SET NULL,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key      TEXT NOT NULL,
    key_fingerprint TEXT NOT NULL UNIQUE,
    token_sha256    TEXT UNIQUE,
    token_issued_at TIMESTAMPTZ,
    first_used_at   TIMESTAMPTZ,
    allowed_types   TEXT[] NOT NULL DEFAULT '{fault,ticket,task,maintenance}',
    may_create      BOOLEAN NOT NULL DEFAULT false,
    platform        TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    label           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    last_seen_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    revoked_by      UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS app_devices_user ON app_devices (user_id);

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS app_request_id UUID REFERENCES app_requests(id) ON DELETE SET NULL;

-- ── Rechte ────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('app.use',          'App: nutzen', 'App'),
    ('app.create',       'App: Vorgänge anlegen', 'App'),
    ('app.cockpit',      'App: Cockpits verwalten', 'App'),
    ('app.sensors.view', 'App: Telefon-Sensoren ansehen', 'App'),
    ('app.admin',        'App: Geräte freigeben und sperren (Bereichsadministrator)', 'App')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
 WHERE p.key = 'app.use'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
 WHERE r.key IN ('admin', 'manager') AND p.key IN ('app.create', 'app.admin')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
 WHERE r.key = 'admin' AND p.key IN ('app.cockpit', 'app.sensors.view')
ON CONFLICT DO NOTHING;
