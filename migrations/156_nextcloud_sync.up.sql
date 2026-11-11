-- 156_nextcloud_sync.up.sql
-- Abgleich PDH → Nextcloud (internal/web/nextcloud_sync.go): Das PDH hat Vorrang und
-- überträgt Benutzer, Rollen, Abteilungen und Gruppen; Netzlaufwerke erscheinen in
-- Nextcloud als externer Speicher. Dazu das Recht, DNS, Proxy und Tunnel-Ziele bei
-- Cloudflare aus dem PDH zu pflegen (internal/web/cloudflare_api.go).

-- Kennzeichen in der Benutzerverwaltung: Administrator auch in Nextcloud. Wirkt nur
-- bei Personen mit der PDH-Rolle Administrator.
ALTER TABLE users ADD COLUMN IF NOT EXISTS nextcloud_admin BOOLEAN NOT NULL DEFAULT false;

-- Je Laufwerk: in Nextcloud zeigen.
ALTER TABLE network_drives ADD COLUMN IF NOT EXISTS nextcloud_show BOOLEAN NOT NULL DEFAULT false;

INSERT INTO permissions (key, label, category) VALUES
    ('system.nextcloud_sync', 'Nextcloud-Abgleich einstellen und starten (Benutzer, Rollen, Gruppen, Laufwerke)', 'System & Sicherheit'),
    ('system.nextcloud_drives', 'Netzlaufwerke in Nextcloud nutzen (PDH-Daten nur lesen)', 'System & Sicherheit'),
    ('system.nextcloud_drives_write', 'Netzlaufwerke in Nextcloud: auch PDH-Daten bearbeiten', 'System & Sicherheit'),
    ('system.cloudflare', 'Cloudflare verwalten (DNS, Proxy, Tunnel)', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Die Rechte gelten systemweit. Sie gehen an die Rollen, die die Server-Einstellungen
-- (und damit bisher die Nextcloud-Anbindung) pflegen dürfen – die Laufwerke können
-- Sicherungen mit Zugangsdaten enthalten, darum bekommt sie sonst niemand von selbst.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('system.nextcloud_sync', 'system.nextcloud_drives', 'system.nextcloud_drives_write', 'system.cloudflare')
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
ON CONFLICT DO NOTHING;
