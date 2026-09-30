-- 074_server_config.up.sql
-- Server-Einstellungen (.env) in der Oberflaeche - nur fuer Administratoren.
INSERT INTO permissions (key, label, category) VALUES
    ('system.server_config', 'Server-Einstellungen (.env) bearbeiten & Server neu starten', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'system.server_config'
ON CONFLICT DO NOTHING;
