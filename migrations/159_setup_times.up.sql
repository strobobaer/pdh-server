-- 159_setup_times.up.sql
-- Rüstzeiten: neue Vorgangsart "Rüstzeit" (Datensatztyp setup). Eine Rüstzeit
-- gehört zu einer Anlage, hat einen Grund (z. B. Produktumstellung), einen
-- Beginn und ein Ende und wird mit einer Höchstdauer überwacht. Wird sie
-- überschritten, entsteht eine Störmeldung (Kategorie "Störung durch Rüsten").
-- Zusatzfunktion: Ohne Rüsttasten an einer Anlage bleibt alles wie bisher.
-- Bewusst KEINE Auslöser auf setup_times (keine Benachrichtigungen).

-- ── Gründe (Katalog) ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS setup_reasons (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    key        TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    grp        TEXT NOT NULL DEFAULT 'setup' CHECK (grp = 'setup'),
    sort       INT NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO setup_reasons (key, name, sort) VALUES
    ('changeover', 'Produktumstellung', 10),
    ('aids',       'Hilfsmittel-Tausch', 20),
    ('cleaning',   'Reinigung', 30)
ON CONFLICT (key) DO NOTHING;

-- ── Rüsttasten (Katalog) und Zuordnung zu Anlagen ─────────────────────────
CREATE TABLE IF NOT EXISTS setup_buttons (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name       VARCHAR(60) NOT NULL,
    reason_id  UUID NOT NULL REFERENCES setup_reasons(id),
    sort       INT NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS infra_setup_buttons (
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    button_id         UUID NOT NULL REFERENCES setup_buttons(id) ON DELETE CASCADE,
    PRIMARY KEY (infrastructure_id, button_id)
);
CREATE INDEX IF NOT EXISTS idx_infra_setup_buttons_button ON infra_setup_buttons (button_id);

-- ── Höchstdauer je Anlage (leer = vererbt / globaler Wert) ────────────────
ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS setup_max_minutes INT;
DO $$ BEGIN
    ALTER TABLE infrastructure
        ADD CONSTRAINT infrastructure_setup_max_minutes_check CHECK (setup_max_minutes > 0);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ── Rüstzeiten ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS setup_times (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    number            SERIAL UNIQUE,
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    started_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ,
    reason_id         UUID NOT NULL REFERENCES setup_reasons(id),
    button_id         UUID REFERENCES setup_buttons(id) ON DELETE SET NULL,
    button_name       VARCHAR(60) NOT NULL DEFAULT '',
    origin            TEXT NOT NULL CHECK (origin IN ('easy', 'manual')),
    status            TEXT NOT NULL CHECK (status IN ('running', 'done', 'fault')),
    max_minutes       INT NOT NULL,
    grace_minutes     INT NOT NULL,
    fault_id          UUID REFERENCES faults(id) ON DELETE SET NULL,
    note              VARCHAR(1000) NOT NULL DEFAULT '',
    responsible_to    UUID REFERENCES users(id),
    assigned_to       UUID REFERENCES users(id),
    created_by        UUID REFERENCES users(id),
    updated_by        UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at       TIMESTAMPTZ,
    archived_by       UUID REFERENCES users(id),
    CHECK (ended_at IS NULL OR ended_at > started_at),
    CHECK ((status = 'running') = (ended_at IS NULL))
);
-- Höchstens eine laufende Rüstzeit je Anlage.
CREATE UNIQUE INDEX IF NOT EXISTS uq_setup_times_running ON setup_times (infrastructure_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_setup_times_infra ON setup_times (infrastructure_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_setup_times_status ON setup_times (status);

-- ── Kategorie für die Störmeldung bei Überschreitung ──────────────────────
INSERT INTO categories (name, color, icon, sort_order)
SELECT 'Störung durch Rüsten', '#ef4444', 'ti-tool', 90
WHERE NOT EXISTS (SELECT 1 FROM categories WHERE lower(name) = lower('Störung durch Rüsten'));

-- ── Rechte ────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('setup.view',  'Rüstzeiten: ansehen', 'Infrastruktur & Anlagen'),
    ('setup.edit',  'Rüstzeiten: erfassen, korrigieren, Katalog und Höchstdauer pflegen', 'Infrastruktur & Anlagen'),
    ('setup.admin', 'Rüstzeiten: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Infrastruktur & Anlagen')
ON CONFLICT (key) DO NOTHING;

-- Wer Anlagen sehen darf, darf Rüstzeiten sehen; wer Anlagen bearbeiten darf,
-- darf sie auch erfassen. Die Abteilung der Quellrolle wird übernommen –
-- niemand wird durch das Update ausgesperrt, niemand bekommt mehr als bisher.
-- Gilt ein Quellrecht überall (NULL) und ein anderes nur in einer Abteilung,
-- gewinnt „überall“ (je Rolle und Recht gibt es nur eine Zeile).
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'infrastructure.view'
  CROSS JOIN permissions p
 WHERE p.key = 'setup.view'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'infrastructure.edit'
  CROSS JOIN permissions p
 WHERE p.key IN ('setup.view', 'setup.edit')
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

-- Bereichsadministrator: gleiche Regel wie sync.admin in Migration 158
-- (Rolle admin und Rollen, die Import verwalten dürfen und schon in einem
-- anderen Bereich Bereichsadministrator sind).
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'setup.admin'
  AND (r.key = 'admin'
       OR (EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'import.write')
           AND EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND o.key LIKE '%.admin' AND o.key <> 'setup.admin')))
ON CONFLICT DO NOTHING;
