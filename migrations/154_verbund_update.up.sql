-- 154_verbund_update.up.sql
-- Update im Verbund und Wartungsfenster: zwei neue Rechte (Kategorie System & Sicherheit).
INSERT INTO permissions (key, label, category) VALUES
    ('system.maintenance_window', 'Wartungsfenster einstellen', 'System & Sicherheit'),
    ('system.maintenance_abort', 'Automatische Wartung abbrechen', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Wartungsfenster einstellen: alle Rollen, die den Ausfallschutz einstellen dürfen
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'system.maintenance_window'
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND o.key = 'system.server_config')
ON CONFLICT DO NOTHING;

-- Wartung abbrechen: Rollen mit Rollenverwaltung und alle Bereichsadministratoren
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'system.maintenance_abort'
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND (o.key = 'system.manage_roles' OR o.key LIKE '%.admin'))
ON CONFLICT DO NOTHING;
