-- 144_permissions_cleanup.up.sql
-- Rollen & Rechte aufgeräumt (internal/web/rbac_guard.go, roles.gohtml):
--  * neues Recht „Leitstand verwalten“ statt festem admin/manager-Vergleich
--    (Verwerfen, Aufgaben/Wartung anlegen, künftig Leitstände bearbeiten)
--  * „Zeiterfassung: alle sehen“ wird jetzt wirklich geprüft – bisher galt
--    fest admin/manager, daher bekommt manager das Recht dazu
--  * Kategorien verständlicher gegliedert (Reihenfolge in der Matrix: Go)

INSERT INTO permissions (key, label, category) VALUES
    ('leitstand.manage', 'Leitstand verwalten: verwerfen, Aufgaben und Wartung anlegen, Leitstände bearbeiten', 'Leitstand & Dashboards')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE (r.key IN ('admin', 'manager') AND p.key = 'leitstand.manage')
   OR (r.key = 'manager' AND p.key = 'timetracking.view_all')
ON CONFLICT DO NOTHING;

UPDATE permissions SET label = 'Zeiterfassung: Zeiten aller sehen, bearbeiten und löschen' WHERE key = 'timetracking.view_all';
UPDATE permissions SET label = 'Projekte: Zusatzfelder (Feldsätze) bearbeiten' WHERE key = 'projects.edit';

UPDATE permissions SET category = 'System & Sicherheit'
WHERE key IN ('system.override', 'system.manage_users', 'system.manage_roles', 'system.backup', 'system.cleanup',
              'system.server_config', 'system.logs', 'system.defaults', 'users.private_data', 'pki.manage');
UPDATE permissions SET category = 'Stammdaten'
WHERE key IN ('fieldsets.manage', 'categories.manage', 'costcenters.manage', 'm365.templates.manage');
UPDATE permissions SET category = 'Daten & Schnittstellen' WHERE category IN ('Import', 'Export', 'Daten');
UPDATE permissions SET category = 'Schichten & Abwesenheit' WHERE category = 'Schichtplanung';
UPDATE permissions SET category = 'Hersteller & Lieferanten' WHERE category = 'Partner';
UPDATE permissions SET category = 'Infrastruktur & Anlagen' WHERE category = 'Infrastruktur';
