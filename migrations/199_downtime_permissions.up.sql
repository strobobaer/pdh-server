-- 199_downtime_permissions.up.sql
--
-- Rechte der Stillstandsgründe (wie die Rüstzeiten, Migration 159):
-- downtime.view (ansehen und auswerten), downtime.edit (Stillstände erfassen,
-- Katalog pflegen), downtime.admin (Gründe löschen). Rein hinzufügend.

INSERT INTO permissions (key, label, category) VALUES
    ('downtime.view',  'Stillstände: ansehen und auswerten', 'Infrastruktur & Anlagen'),
    ('downtime.edit',  'Stillstände: erfassen, Katalog der Gründe pflegen', 'Infrastruktur & Anlagen'),
    ('downtime.admin', 'Stillstände: Bereichsadministrator (Gründe löschen)', 'Infrastruktur & Anlagen')
ON CONFLICT (key) DO NOTHING;

-- Wer Anlagen sehen darf, darf Stillstände sehen; wer Anlagen bearbeiten darf, auch erfassen
-- (Abteilung der Quellrolle wird übernommen; "überall" gewinnt, wie in Migration 159).
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'infrastructure.view'
  CROSS JOIN permissions p
 WHERE p.key = 'downtime.view'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'infrastructure.edit'
  CROSS JOIN permissions p
 WHERE p.key IN ('downtime.view', 'downtime.edit')
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

-- Bereichsadministrator: Rolle admin (weitere Rollen vergibt der Betreiber).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'downtime.admin' AND r.key = 'admin'
ON CONFLICT DO NOTHING;

-- Feste Rolle "Terminal" (Migration 190): Stillstände ansehen und erfassen, wie die Rüstzeiten.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'terminal' AND p.key IN ('downtime.view', 'downtime.edit')
ON CONFLICT DO NOTHING;
