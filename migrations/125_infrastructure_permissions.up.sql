-- 125_infrastructure_permissions.up.sql
-- Infrastruktur bekommt wie die übrigen Module getrennte Rechte:
--   * infrastructure.view        – Liste, Detail, Historie, Kommentare
--   * infrastructure.edit        – (bestand) anlegen, bearbeiten, deaktivieren …
--   * infrastructure.obligations – Prüfpflichten & Gefährdungsbeurteilungen
--                                  pflegen und als erledigt erfassen
-- Dazu die Rolle „Anlagenverantwortliche/r“ (Rang zwischen Techniker und Manager).
-- Bisher durften alle Infrastruktur ansehen – darum bekommen alle Rollen „view“.

INSERT INTO permissions (key, label, category) VALUES
    ('infrastructure.view',        'Infrastruktur ansehen',                                          'Infrastruktur'),
    ('infrastructure.obligations', 'Prüfpflichten & Gefährdungsbeurteilungen pflegen und erledigen', 'Infrastruktur')
ON CONFLICT (key) DO NOTHING;

-- ansehen: alle vorhandenen Rollen (Verhalten wie bisher)
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'infrastructure.view'
ON CONFLICT DO NOTHING;

-- Prüfpflichten: wer Infrastruktur bearbeiten darf, dazu Techniker
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'infrastructure.obligations'
  AND (r.key = 'technician' OR EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions e ON e.id = rp.permission_id
        WHERE rp.role_id = r.id AND e.key = 'infrastructure.edit'))
ON CONFLICT DO NOTHING;

-- Rolle „Anlagenverantwortliche/r“: alles, was Techniker dürfen, plus alle Infrastruktur-Rechte
INSERT INTO roles (key, label, is_builtin, level) VALUES ('asset_manager', 'Anlagenverantwortliche/r', false, 60)
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT (SELECT id FROM roles WHERE key = 'asset_manager'), rp.permission_id
FROM role_permissions rp JOIN roles t ON t.id = rp.role_id
WHERE t.key = 'technician'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT (SELECT id FROM roles WHERE key = 'asset_manager'), p.id FROM permissions p
WHERE p.key IN ('infrastructure.view', 'infrastructure.edit', 'infrastructure.obligations', 'hmi.view', 'hmi.control',
                'tickets.view', 'tickets.edit', 'tickets.resolve', 'faults.view', 'faults.edit', 'faults.resolve',
                'maintenance.view', 'maintenance.edit', 'maintenance.complete')
ON CONFLICT DO NOTHING;
