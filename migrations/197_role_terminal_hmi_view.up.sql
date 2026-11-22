-- 197_role_terminal_hmi_view.up.sql
--
-- Die feste Rolle „Terminal“ (Migration 190) darf zusätzlich HMIs ansehen (hmi.view), damit HMIs mit
-- „Ansehen am Terminal: jeder“ am Terminal sichtbar sind. Bedienen (hmi.control) bekommt die Rolle nicht.
-- Wie die übrigen Rechte der Rolle gilt es zunächst überall (department_id NULL, als Vorlage); der Betreiber
-- begrenzt es auf eine Abteilung. Rein hinzufügend: andere Rollen und Rechte bleiben unberührt.

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'terminal' AND p.key = 'hmi.view'
ON CONFLICT DO NOTHING;
