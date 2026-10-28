-- 148_write_permissions.up.sql
-- Schreib-Aktionen prüfen jetzt ihr Recht (internal/web/perm_gate.go,
-- gateRules). Bisher prüften viele davon nur die Anmeldung. Damit niemand
-- durch das Update ausgesperrt wird, bekommen die bestehenden Rollen die
-- Rechte, die sie bisher stillschweigend genutzt haben. Danach lassen sie
-- sich unter Rollen & Rechte gezielt wegnehmen oder auf Abteilungen begrenzen.

-- Neues Recht: Ersatzteile und Lagerorte anlegen (bisher ohne Prüfung)
INSERT INTO permissions (key, label, category) VALUES
    ('inventory.create', 'Lager: Ersatzteile und Lagerorte anlegen', 'Lager')
ON CONFLICT (key) DO NOTHING;

-- Vorgänge bearbeiten und abschließen: alle Rollen außer „Betrachter“
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key <> 'viewer'
  AND p.key IN ('faults.edit', 'faults.resolve', 'tickets.edit', 'tickets.resolve',
                'maintenance.edit', 'maintenance.complete', 'tasks.edit', 'projects.edit', 'it.edit')
ON CONFLICT DO NOTHING;

-- Lager: wer das Lager sieht, durfte bisher Teile/Lagerorte anlegen,
-- buchen und an der Inventur zählen
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, p.id FROM role_permissions rp
JOIN permissions v ON v.id = rp.permission_id AND v.key = 'inventory.view'
CROSS JOIN permissions p
WHERE p.key IN ('inventory.create', 'inventory.book')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'inventory.create'
ON CONFLICT DO NOTHING;
