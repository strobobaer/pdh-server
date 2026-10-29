-- 153_display_docs.up.sql
-- Widget / Leitstand-Baustein „Dokumente“: Bilder, PDF, Word und Excel anzeigen
-- (display_docs.go, Recht leitstand.upload). Die Dateien liegen als Anhänge mit ref_type 'display'.
INSERT INTO permissions (key, label, category) VALUES
    ('leitstand.upload', 'Dokumente für Dashboard und Leitstand hochladen (Bilder, PDF, Word, Excel)', 'Leitstand & Dashboards')
ON CONFLICT (key) DO NOTHING;

-- Wer Leitstände verwaltet, dazu Administratoren und Manager
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'leitstand.upload'
  AND (r.key IN ('admin', 'manager') OR EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND o.key = 'leitstand.manage'))
ON CONFLICT DO NOTHING;

-- Bereichsadministrator für Leitstand & Dashboards (wie bei den anderen Bereichen nur die Rolle admin)
INSERT INTO permissions (key, label, category) VALUES
    ('leitstand.admin', 'Leitstand & Dashboards: Bereichsadministrator (alle Rechte, endgültig löschen)', 'Leitstand & Dashboards')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'leitstand.admin'
ON CONFLICT DO NOTHING;
