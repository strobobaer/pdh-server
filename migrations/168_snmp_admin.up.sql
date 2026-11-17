-- SNMP v3 (0.80.22): Bereichsadministrator der SNMP-Zugangsprofile. Ein Profil endgültig
-- löschen darf nur, wer dieses Recht hat (internal/web/snmp_web.go, Regel in gateRules) –
-- so wie bei den Zertifikaten (system.certs_admin, Migration 157).

INSERT INTO permissions (key, label, category) VALUES
    ('system.snmp_admin', 'SNMP: Zugangsprofile löschen (Bereichsadministrator)', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Das Recht gilt systemweit. Es geht an alle Rollen, die die SNMP-Profile verwalten dürfen
-- (system.snmp) UND schon Bereichsadministrator der Zertifikate sind (system.certs_admin) –
-- beides überall (department_id IS NULL). So bekommt niemand über dieses Recht Zugang zu
-- den Profilen, der ihn nicht ohnehin hat, und löschen darf nur, wer in den
-- Server-Einstellungen schon heute endgültig entfernen darf.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id
  FROM roles r
  CROSS JOIN permissions p
 WHERE p.key = 'system.snmp_admin'
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.snmp')
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.certs_admin')
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- Wem eines der beiden Rechte einzeln entzogen ist (granted = false), dem ist auch dieses
-- entzogen – sonst käme er über seine Rolle daran.
INSERT INTO user_permissions (user_id, permission_id, granted)
SELECT DISTINCT up.user_id, p.id, false
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key IN ('system.snmp', 'system.certs_admin')
  CROSS JOIN permissions p
 WHERE p.key = 'system.snmp_admin' AND NOT up.granted
ON CONFLICT (user_id, permission_id) DO NOTHING;

-- Wer beide Rechte einzeln und überall hat, bekommt auch dieses einzeln.
INSERT INTO user_permissions (user_id, permission_id, granted)
SELECT DISTINCT a.user_id, p.id, true
  FROM user_permissions a
  JOIN permissions pa ON pa.id = a.permission_id AND pa.key = 'system.snmp'
  JOIN user_permissions b ON b.user_id = a.user_id
  JOIN permissions pb ON pb.id = b.permission_id AND pb.key = 'system.certs_admin'
  CROSS JOIN permissions p
 WHERE p.key = 'system.snmp_admin'
   AND a.granted AND b.granted AND a.department_id IS NULL AND b.department_id IS NULL
ON CONFLICT (user_id, permission_id) DO NOTHING;
