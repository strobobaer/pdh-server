-- 147_department_permissions.up.sql
-- Rechte auf Abteilungsebene und Bereichsadministratoren (internal/core/rbac/scope.go).
--
-- Jedes Recht einer Rolle und jede Einzel-Erlaubnis eines Benutzers kann auf
-- eine Abteilung samt Unterabteilungen begrenzt werden (NULL = in allen
-- Abteilungen, wie bisher).
ALTER TABLE role_permissions ADD COLUMN IF NOT EXISTS department_id UUID REFERENCES departments(id) ON DELETE CASCADE;
ALTER TABLE user_permissions ADD COLUMN IF NOT EXISTS department_id UUID REFERENCES departments(id) ON DELETE CASCADE;

-- Je Bereich ein Administrator-Recht: alle Rechte des Bereichs und endgültig
-- löschen (ohne Löschantrag). Anfangs nur die Rolle admin.
INSERT INTO permissions (key, label, category) VALUES
    ('faults.admin',         'Störungen: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Störungen'),
    ('tickets.admin',        'Tickets: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Tickets'),
    ('maintenance.admin',    'Wartung: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Wartung'),
    ('tasks.admin',          'Aufgaben: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Aufgaben & Projekte'),
    ('projects.admin',       'Projekte: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Aufgaben & Projekte'),
    ('kvp.admin',            'KVP: Bereichsadministrator (alle Rechte, endgültig löschen)', 'KVP'),
    ('infrastructure.admin', 'Infrastruktur: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Infrastruktur & Anlagen'),
    ('it.admin',             'IT: Bereichsadministrator (alle Rechte, endgültig löschen)', 'IT'),
    ('inventory.admin',      'Lager: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Lager'),
    ('procurement.admin',    'Beschaffung: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Beschaffung'),
    ('directory.admin',      'Hersteller & Lieferanten: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Hersteller & Lieferanten'),
    ('shifts.admin',         'Schichten: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Schichten & Abwesenheit'),
    ('absences.admin',       'Abwesenheit: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Schichten & Abwesenheit'),
    ('ms.admin',             'Managementsysteme: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Managementsysteme')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key LIKE '%.admin'
ON CONFLICT DO NOTHING;
