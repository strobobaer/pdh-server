-- 078_terminal_location_infra_edit.up.sql
-- 1) Standort eines Terminals (Systembenutzer): Beim Anlegen von Tickets,
--    Stoerungen usw. klappt der Infrastruktur-Picker bis hierhin auf.
-- 2) Eigene Berechtigung zum Anlegen und Bearbeiten der Infrastruktur
--    (bisher durfte das jeder angemeldete Benutzer). Admin und Manager
--    bekommen sie, damit bestehende Ablaeufe weiterlaufen; weitere Rollen
--    ueber die Rollen-Matrix.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS terminal_infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE SET NULL;

INSERT INTO permissions (key, label, category) VALUES
    ('infrastructure.edit', 'Infrastruktur anlegen, bearbeiten & deaktivieren', 'Infrastruktur')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key = 'infrastructure.edit'
ON CONFLICT DO NOTHING;
