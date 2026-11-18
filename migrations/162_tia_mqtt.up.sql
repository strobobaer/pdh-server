-- TIA-Portal-Baustein „MQTT senden“ (0.80.13)

-- Heartbeat-Zuordnungen: Sendeintervall der Steuerung in Sekunden. Nur dort
-- gesetzt; daran erkennt PDH, wann sich die Steuerung nicht mehr meldet.
ALTER TABLE import_mappings
    ADD COLUMN IF NOT EXISTS heartbeat_seconds int
    CHECK (heartbeat_seconds IS NULL OR heartbeat_seconds BETWEEN 1 AND 86400);

-- ── Recht ─────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('import.tia', 'Steuerungsbaustein (TIA Portal) herunterladen', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Wer Importe sehen darf, darf den Baustein herunterladen. Die Abteilung der
-- Quellrolle wird übernommen – niemand wird durch das Update ausgesperrt.
-- Gilt ein Recht überall (NULL), gewinnt „überall“.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'import.read'
  CROSS JOIN permissions p
 WHERE p.key = 'import.tia'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;
