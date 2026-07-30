-- 045_rbac_and_system_users.up.sql
--
-- Rollen-/Berechtigungs-Matrix, Systemnutzer-Kennzeichen und globale
-- App-Einstellungen (u.a. fuer Auto-Logout-Zeiten).
--
-- Bewusst additiv: users.role bleibt als String-Spalte bestehen (viele
-- bestehende RequireRole()-Aufrufe im Code haengen daran) - roles.key ist
-- derselbe Wertebereich, verknuepft per Join auf roles.key = users.role,
-- keine Fremdschluessel-Spalte auf users noetig.

CREATE TABLE IF NOT EXISTS roles (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key        VARCHAR(50) UNIQUE NOT NULL,
    label      VARCHAR(100) NOT NULL,
    is_builtin BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS permissions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key        VARCHAR(100) UNIQUE NOT NULL,
    label      VARCHAR(150) NOT NULL,
    category   VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE IF NOT EXISTS app_settings (
    key        VARCHAR(100) PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS is_system_user BOOLEAN NOT NULL DEFAULT false;

-- Die 5 bereits bestehenden Rollen als "builtin" (nicht loeschbar) anlegen
INSERT INTO roles (key, label, is_builtin) VALUES
    ('admin', 'Administrator', true),
    ('manager', 'Manager', true),
    ('technician', 'Techniker', true),
    ('worker', 'Mitarbeiter', true),
    ('viewer', 'Betrachter', true)
ON CONFLICT (key) DO NOTHING;

-- Startset an Berechtigungen. "system.override" ist die einzige, die vom
-- Code aktuell tatsaechlich geprueft wird (Override-Anmeldung am
-- Systemnutzer) - der Rest ist bewusst als Grundgeruest angelegt, damit
-- die Matrix von Anfang an sinnvoll befuellt ist und schrittweise an
-- weiteren Stellen im Code scharf geschaltet werden kann.
INSERT INTO permissions (key, label, category) VALUES
    ('system.override',      'Als Systemnutzer-Override anmelden', 'System'),
    ('system.manage_users',  'Benutzer verwalten',                  'System'),
    ('system.manage_roles',  'Rollen & Berechtigungen verwalten',   'System'),
    ('tickets.view',         'Tickets ansehen',                     'Tickets'),
    ('tickets.edit',         'Tickets bearbeiten',                  'Tickets'),
    ('tickets.resolve',      'Tickets abschließen',                 'Tickets'),
    ('faults.view',          'Störungen ansehen',                   'Störungen'),
    ('faults.edit',          'Störungen bearbeiten',                'Störungen'),
    ('faults.resolve',       'Störungen abschließen',               'Störungen'),
    ('maintenance.view',     'Wartung ansehen',                     'Wartung'),
    ('maintenance.edit',     'Wartung bearbeiten',                  'Wartung'),
    ('maintenance.complete', 'Wartung abschließen',                 'Wartung'),
    ('tasks.view',           'Aufgaben ansehen',                    'Aufgaben & Projekte'),
    ('tasks.edit',           'Aufgaben bearbeiten',                 'Aufgaben & Projekte'),
    ('projects.edit',        'Projekte bearbeiten',                 'Aufgaben & Projekte'),
    ('inventory.view',       'Lager ansehen',                       'Lager'),
    ('inventory.edit',       'Lager bearbeiten',                    'Lager'),
    ('it.view',              'IT-Infrastruktur ansehen',            'IT'),
    ('it.edit',              'IT-Infrastruktur bearbeiten',         'IT'),
    ('shifts.view',          'Schichtplan ansehen',                 'Schichtplanung'),
    ('shifts.edit',          'Schichtplan bearbeiten',               'Schichtplanung'),
    ('timetracking.view_all','Zeiterfassung aller Nutzer ansehen',  'Zeiterfassung')
ON CONFLICT (key) DO NOTHING;

-- Admin bekommt initial alles zugewiesen, damit das System sofort nutzbar
-- ist. Alle anderen Rollen starten bewusst leer - Policy wird ueber die
-- Matrix-UI selbst festgelegt.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.key = 'admin'
ON CONFLICT DO NOTHING;

-- Standardwerte fuer Auto-Logout (Minuten). ueber die Einstellungen-UI
-- aenderbar.
INSERT INTO app_settings (key, value) VALUES
    ('idle_timeout_minutes', '30'),
    ('override_timeout_minutes', '10')
ON CONFLICT (key) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_role_permissions_role ON role_permissions(role_id);
CREATE INDEX IF NOT EXISTS idx_role_permissions_perm ON role_permissions(permission_id);
