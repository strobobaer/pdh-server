-- 076_server_logs.up.sql
-- Protokollansicht unter Server-Einstellungen (nur Administratoren).
INSERT INTO permissions (key, label, category) VALUES
    ('system.logs', 'Server-Protokoll ansehen & herunterladen', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'system.logs'
ON CONFLICT DO NOTHING;
