-- 142_defaults_permission.up.sql
-- Recht „Standards für alle festlegen“: eigene Darstellung, eigenes Menü und
-- eigenes Dashboard als Firmenstandard übernehmen (Core-Einstellungen,
-- internal/web/user_defaults.go). Anfangs nur Administratoren.
INSERT INTO permissions (key, label, category) VALUES
    ('system.defaults', 'Standards für alle festlegen (Darstellung, Menü, Dashboard)', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'system.defaults'
ON CONFLICT DO NOTHING;
