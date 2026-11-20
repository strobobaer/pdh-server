-- 183_view_projects_kvp.up.sql
-- Ansichtsrecht für Projekte und KVP-Vorschläge (0.82.3).
-- Bisher sah jeder Angemeldete alle Projekte und alle Vorschläge. Jetzt
-- entscheidet das Recht "projects.view" bzw. "kvp.view" (je Abteilung
-- einschränkbar) – oder die Beteiligung am Vorgang.
--
-- Niemand wird durch das Update ausgesperrt: Jede bestehende Rolle bekommt
-- beide Rechte, ohne Abteilung. Einschränken ist danach Sache des
-- Administrators. Einzelrechte von Benutzern (user_permissions) bleiben
-- unberührt; später angelegte Rollen bekommen die Rechte nicht von selbst.

INSERT INTO permissions (key, label, category) VALUES
    ('projects.view', 'Projekte: ansehen', 'Aufgaben & Projekte'),
    ('kvp.view',      'KVP: alle Vorschläge ansehen', 'KVP')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM roles r
  CROSS JOIN permissions p
 WHERE p.key IN ('projects.view', 'kvp.view')
ON CONFLICT DO NOTHING;
