-- Firmenstammdaten (0.82.18): eine Firma, ein Datensatz. Gepflegt im Reiter „Firma“ an
-- jedem obersten Knoten der Infrastruktur, verwendet in Mails, Druckkopf,
-- Wartungsprotokoll und M365-Signaturen.

CREATE TABLE IF NOT EXISTS company_profile (
    id                boolean PRIMARY KEY DEFAULT true CHECK (id),
    name              text NOT NULL DEFAULT '',
    name_addition     text NOT NULL DEFAULT '',
    street            text NOT NULL DEFAULT '',
    postal_code       text NOT NULL DEFAULT '',
    city              text NOT NULL DEFAULT '',
    country           text NOT NULL DEFAULT 'Deutschland',
    phone             text NOT NULL DEFAULT '',
    fax               text NOT NULL DEFAULT '',
    email             text NOT NULL DEFAULT '',
    website           text NOT NULL DEFAULT '',
    registered_office text NOT NULL DEFAULT '',
    register_court    text NOT NULL DEFAULT '',
    register_no       text NOT NULL DEFAULT '',
    management        text NOT NULL DEFAULT '',
    supervisory_chair text NOT NULL DEFAULT '',
    vat_id            text NOT NULL DEFAULT '',
    tax_no            text NOT NULL DEFAULT '',
    bank_name         text NOT NULL DEFAULT '',
    iban              text NOT NULL DEFAULT '',
    bic               text NOT NULL DEFAULT '',
    updated_at        timestamptz,
    updated_by        uuid REFERENCES users(id) ON DELETE SET NULL
);

INSERT INTO company_profile (id, name)
SELECT true, COALESCE((SELECT trim(value) FROM app_settings WHERE key = 'm365_company_name'), '')
ON CONFLICT (id) DO NOTHING;

DELETE FROM app_settings WHERE key = 'm365_company_name';

INSERT INTO permissions (key, label, category) VALUES
    ('company.edit', 'Firmenstammdaten bearbeiten', 'Stammdaten')
ON CONFLICT (key) DO NOTHING;

-- Wer bisher den Firmennamen (Signaturen) oder das Erscheinungsbild pflegen durfte,
-- behält das: Rollen und Benutzer mit einem dieser Rechte, samt Abteilung.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id
       AND o.key IN ('system.server_config', 'm365.templates.manage', 'system.manage_roles')
  CROSS JOIN permissions p
 WHERE p.key = 'company.edit'
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
 WHERE p.key = 'company.edit' AND r.key = 'admin'
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id
       AND o.key IN ('system.server_config', 'm365.templates.manage', 'system.manage_roles')
  CROSS JOIN permissions p
 WHERE p.key = 'company.edit' AND up.granted
ON CONFLICT (user_id, permission_id) DO NOTHING;
