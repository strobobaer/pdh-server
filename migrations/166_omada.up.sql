-- 166_omada.up.sql
-- Anbindung an einen TP-Link-Omada-Controller im Haus (0.80.21, internal/web/omada_web.go).
-- Zusatz: Ohne Einrichtung ändert sich nichts. Die Zugangsdaten liegen in app_settings
-- (omada.*), das Client-Secret nur verschlüsselt.

INSERT INTO permissions (key, label, category) VALUES
    ('system.omada', 'Omada-Controller anbinden', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Wer heute die UniFi-Anbindung einrichten darf, darf auch Omada anbinden – niemand wird
-- durch das Update ausgesperrt und niemand bekommt mehr als bisher. Die Abteilung der
-- Quellzeile wird übernommen (je Rolle und Recht gibt es nur eine Zeile). Eine schon
-- vorhandene Zeile bleibt, wie sie ist: Ein erneuter Lauf weitet nichts aus.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.omada'
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- Einzelrechte je Benutzer genauso – auch ein ausdrücklich entzogenes Recht (granted = false),
-- damit ein Benutzer ohne UniFi-Recht nicht über seine Rolle an Omada kommt.
INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.omada'
ON CONFLICT (user_id, permission_id) DO NOTHING;
