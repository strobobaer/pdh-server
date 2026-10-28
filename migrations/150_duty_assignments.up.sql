-- 150_duty_assignments.up.sql
-- Sonderdienste (internal/web/planner_duties.go): Bereitschaft, besondere
-- Tätigkeiten und Zusatzaufgaben, die einzelnen Personen tageweise
-- zugeteilt werden – unabhängig von der Schicht. Eine Dienstart kann eine
-- Qualifikation aus dem Personalstamm verlangen (user_qualifications.title,
-- noch gültig); dann bietet der Planer nur passende Personen an.

CREATE TABLE IF NOT EXISTS duty_types (
    id            UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name          VARCHAR(80)  NOT NULL,
    short_name    VARCHAR(10)  NOT NULL DEFAULT '',
    color         VARCHAR(7)   NOT NULL DEFAULT '#F59E0B',
    kind          VARCHAR(20)  NOT NULL DEFAULT 'duty' CHECK (kind IN ('standby', 'duty', 'task')), -- Bereitschaft, besondere Tätigkeit, Zusatzaufgabe
    qualification VARCHAR(200) NOT NULL DEFAULT '',            -- nötige Qualifikation (Titel im Personalstamm), leer = alle
    min_staff     INTEGER      NOT NULL DEFAULT 0,
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    sort_order    INTEGER      NOT NULL DEFAULT 0,
    active        BOOLEAN      NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS duty_assignments (
    id           UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    duty_type_id UUID NOT NULL REFERENCES duty_types(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date         DATE NOT NULL,
    note         VARCHAR(300) NOT NULL DEFAULT '',
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (duty_type_id, user_id, date)
);
CREATE INDEX IF NOT EXISTS idx_duty_assignments_date ON duty_assignments (date);
CREATE INDEX IF NOT EXISTS idx_duty_assignments_user ON duty_assignments (user_id, date);

INSERT INTO permissions (key, label, category) VALUES
    ('shifts.duties', 'Schichtplan: Sonderdienste, Bereitschaft und Zusatzaufgaben einteilen und verwalten', 'Schichten & Abwesenheit')
ON CONFLICT (key) DO NOTHING;

-- wer Schichten planen darf, darf auch Sonderdienste einteilen
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, p.id FROM role_permissions rp
JOIN permissions s ON s.id = rp.permission_id AND s.key = 'shifts.edit'
CROSS JOIN permissions p WHERE p.key = 'shifts.duties'
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.key = 'admin' AND p.key = 'shifts.duties'
ON CONFLICT DO NOTHING;

-- Beispiel-Dienstarten (lassen sich unter Planer → Einstellungen ändern)
INSERT INTO duty_types (name, short_name, color, kind, sort_order)
SELECT v.name, v.short, v.color, v.kind, v.sort FROM (VALUES
    ('Rufbereitschaft', 'RB', '#F59E0B', 'standby', 1),
    ('Ersthelfer', 'EH', '#EF4444', 'duty', 2),
    ('Zusatzaufgabe', 'ZA', '#8B5CF6', 'task', 3)
) AS v(name, short, color, kind, sort)
WHERE NOT EXISTS (SELECT 1 FROM duty_types);
