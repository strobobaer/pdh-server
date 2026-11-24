-- 222_os_update_permission.up.sql
-- Betriebssystem-Updates im PDH (Teil A): neues Recht (Kategorie System & Sicherheit).
-- Es gilt systemweit: nur Rollen, deren Rollenverwaltung überall gilt (department_id IS NULL), bekommen es –
-- so verliert niemand durch das Update einen Zugang, den die Core-Einstellungen bisher boten.
INSERT INTO permissions (key, label, category) VALUES ('system.os_update', 'Betriebssystem aktualisieren', 'System & Sicherheit') ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id) SELECT DISTINCT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key='system.os_update' AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id=rp.permission_id WHERE rp.role_id=r.id AND rp.department_id IS NULL AND o.key='system.manage_roles') ON CONFLICT DO NOTHING;
