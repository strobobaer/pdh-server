-- 155_file_mirror.up.sql
-- Datei-Spiegelung (Ausfallschutz Teil 6): zwei neue Rechte (Kategorie System & Sicherheit).
INSERT INTO permissions (key, label, category) VALUES
    ('system.file_mirror', 'Datei-Spiegelung einstellen', 'System & Sicherheit'),
    ('system.file_mirror_admin', 'Datei-Spiegelung: Löschen am Partner freigeben und Papierkorb leeren', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Die Rechte gelten systemweit: nur Rollen, deren Quellrecht überall gilt (department_id IS NULL),
-- bekommen sie; auf eine Abteilung begrenzte Quellrechte geben sie nicht.
-- Beide gehen an alle Rollen, die den Ausfallschutz einstellen dürfen (system.server_config),
-- damit niemand durch das Update ausgesperrt wird.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('system.file_mirror', 'system.file_mirror_admin')
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
ON CONFLICT DO NOTHING;
