-- 146_calendar_widget_permission.up.sql
-- Recht für das Dashboard-Widget „Mein Kalender“ (dashboard_widgets_calendar.go).
-- Jede Person sieht dort nur die eigenen Termine; anfangs haben es alle Rollen.
-- Das Widget „Schichtplan Team“ nutzt das vorhandene Recht shifts.view.
INSERT INTO permissions (key, label, category) VALUES
    ('calendar.view', 'Dashboard: Widget „Mein Kalender“ (eigene Termine der nächsten 7 Tage)', 'Leitstand & Dashboards')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'calendar.view'
ON CONFLICT DO NOTHING;
