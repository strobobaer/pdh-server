-- Server-Seite „Netzwerk“ (0.81.4): die Netze (VLANs) und WLANs des Betriebs an einer
-- Stelle. Andere Bereiche (z. B. ESPPDH) wählen daraus, statt Namen und Kennwörter
-- selbst zu führen. WLAN-Kennwörter stehen nur verschlüsselt in der Datenbank.

CREATE TABLE IF NOT EXISTS net_segments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    vlan_id    INTEGER CHECK (vlan_id BETWEEN 1 AND 4094),
    subnet     TEXT NOT NULL DEFAULT '',
    purpose    TEXT NOT NULL DEFAULT '',
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS net_segments_name_uq ON net_segments (lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS net_segments_vlan_uq ON net_segments (vlan_id) WHERE vlan_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS net_wlans (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ssid         TEXT NOT NULL,
    segment_id   UUID REFERENCES net_segments(id) ON DELETE SET NULL,
    password_enc TEXT NOT NULL DEFAULT '',
    hidden       BOOLEAN NOT NULL DEFAULT false,
    note         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS net_wlans_ssid_uq ON net_wlans (ssid);

INSERT INTO permissions (key, label, category) VALUES
    ('system.network', 'Netzwerk: VLANs und WLANs pflegen', 'System & Sicherheit'),
    ('system.network_admin', 'Netzwerk: VLANs und WLANs löschen (Bereichsadministrator)', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Pflegen dürfen alle Rollen, die heute die UniFi-Anbindung (Namen im Haus) verwalten.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.network'
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.network'
ON CONFLICT (user_id, permission_id) DO NOTHING;

-- Löschen: Rollen, die UniFi verwalten UND schon Bereichsadministrator der Zertifikate
-- sind (beides überall) – wie bei den SNMP-Profilen (Migration 168).
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id
  FROM roles r
  CROSS JOIN permissions p
 WHERE p.key = 'system.network_admin'
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.unifi')
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.certs_admin')
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO user_permissions (user_id, permission_id, granted)
SELECT DISTINCT up.user_id, p.id, false
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key IN ('system.unifi', 'system.certs_admin')
  CROSS JOIN permissions p
 WHERE p.key = 'system.network_admin' AND NOT up.granted
ON CONFLICT (user_id, permission_id) DO NOTHING;
