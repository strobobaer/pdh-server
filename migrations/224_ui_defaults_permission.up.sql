-- 224_ui_defaults_permission.up.sql
-- Einheitliche Oberfläche (0.90.0 Teil 1): Recht für die Seite „Gestaltung“ und später für die
-- Vorgaben der Oberfläche für Firma und Rollen (Kategorie System & Sicherheit). Es gilt systemweit:
-- nur Rollen, deren Rollenverwaltung überall gilt (department_id IS NULL), bekommen es – so
-- verliert niemand einen Zugang, den die Verwaltung bisher bot.
INSERT INTO permissions (key, label, category) VALUES ('system.ui_defaults', 'Oberfläche: Vorgaben für Firma und Rollen', 'System & Sicherheit') ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id) SELECT DISTINCT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key='system.ui_defaults' AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id=rp.permission_id WHERE rp.role_id=r.id AND rp.department_id IS NULL AND o.key='system.manage_roles') ON CONFLICT DO NOTHING;
