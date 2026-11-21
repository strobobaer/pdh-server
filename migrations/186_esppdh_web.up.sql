-- 186_esppdh_web.up.sql
-- ESPPDH Teil 1 (0.82.11): Webseite des Geräts (web_server v3) je Gerät ein- oder ausschaltbar,
-- mit eigenem Kennwort (verschlüsselt wie MQTT- und Notzugang-Kennwort; das PDH legt es beim
-- Anlegen an, bei älteren Geräten beim nächsten Speichern oder Bauen). Neues Recht, die
-- Sensorwerte im Reiter „ESPPDH“ der Anlage und am Anlagenterminal zu sehen.

ALTER TABLE esppdh_devices ADD COLUMN IF NOT EXISTS web_enabled boolean NOT NULL DEFAULT true;
ALTER TABLE esppdh_devices ADD COLUMN IF NOT EXISTS web_password_enc text NOT NULL DEFAULT '';

INSERT INTO permissions (key, label, category) VALUES
    ('esppdh.values', 'ESPPDH: Sensorwerte ansehen (Anlage und Terminal)', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
 WHERE p.key = 'esppdh.values' AND r.key IN ('admin', 'manager', 'asset_manager', 'technician')
ON CONFLICT DO NOTHING;
