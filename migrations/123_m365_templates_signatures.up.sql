-- 123_m365_templates_signatures.up.sql
-- Microsoft 365 → Vorlagen & Signaturen (internal/web/m365_templates.go, m365_signatures.go):
--   * office_templates: zentrale Word-/Excel-/PowerPoint-Vorlagen, optional in
--     eine SharePoint-Bibliothek (Organisationsvorlagen) gespiegelt
--   * mail_signatures: E-Mail-Signaturen mit Platzhaltern, als Exchange-
--     Transportregel ausgerollt (je Abteilung oder für alle)
--   * microsoft_directory_users: weitere Entra-Felder als Rückfall für Platzhalter

CREATE TABLE IF NOT EXISTS office_templates (
    id                   UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name                 VARCHAR(255) NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    category             VARCHAR(100) NOT NULL DEFAULT '',
    file_name            VARCHAR(255) NOT NULL,
    file_path            TEXT NOT NULL,
    file_size            BIGINT NOT NULL DEFAULT 0,
    version              INTEGER NOT NULL DEFAULT 1,
    active               BOOLEAN NOT NULL DEFAULT true,
    sharepoint_item_id   TEXT NOT NULL DEFAULT '',
    sharepoint_synced_at TIMESTAMPTZ,
    updated_by           UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_office_templates_category ON office_templates(category, name);

CREATE TABLE IF NOT EXISTS mail_signatures (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name        VARCHAR(255) NOT NULL,
    department  VARCHAR(100) NOT NULL DEFAULT '', -- leer = alle übrigen
    html        TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT true,
    updated_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE microsoft_directory_users
    ADD COLUMN IF NOT EXISTS given_name      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS surname         TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS job_title       TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS business_phone  TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS mobile_phone    TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS office_location TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS company_name    TEXT NOT NULL DEFAULT '';

INSERT INTO permissions (key, label, category) VALUES
    ('m365.templates.manage', 'Microsoft-365-Vorlagen & E-Mail-Signaturen verwalten', 'System')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'm365.templates.manage'
ON CONFLICT DO NOTHING;
