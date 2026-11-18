-- ESPPDH (0.82.0): eigene Mess- und Zählgeräte auf ESP-Basis (Import → ESPPDH).
-- Je Gerät eine Zeile mit Anlage, eingebautem Broker (Verbindung), Platine, Netz und
-- verschlüsselten Kennwörtern (MQTT-Zugang und Notzugang-WLAN, „v1:…“); dazu seine
-- Bausteine und die Bauläufe der Firmware. Klartext-Kennwörter stehen nie in der Datenbank.

CREATE TABLE IF NOT EXISTS esppdh_devices (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    -- Kennung: Thema esppdh/<slug>/…, Benutzer am Broker esppdh-<slug>
    slug              TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{3,32}$'),
    name              TEXT NOT NULL,
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    connection_id     UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE RESTRICT,
    board_key         TEXT NOT NULL,
    custom_board      TEXT NOT NULL DEFAULT '',
    wired             BOOLEAN NOT NULL DEFAULT false,
    wlan_id           UUID REFERENCES net_wlans(id) ON DELETE SET NULL,
    -- Netz bei Kabel; bei WLAN gilt das Netz des WLANs
    segment_id        UUID REFERENCES net_segments(id) ON DELETE SET NULL,
    -- false: ESP8266 verbindet über den Port ohne TLS, obwohl der Broker TLS hat
    -- (ESP8266 kann kein TLS); ESP32 nutzt TLS immer, wenn der Broker es hat
    use_tls           BOOLEAN NOT NULL DEFAULT true,
    mqtt_password_enc TEXT NOT NULL,
    ap_password_enc   TEXT NOT NULL DEFAULT '',
    locked            BOOLEAN NOT NULL DEFAULT false,
    built_hash        TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS esppdh_devices_connection_idx ON esppdh_devices (connection_id);
CREATE INDEX IF NOT EXISTS esppdh_devices_infra_idx ON esppdh_devices (infrastructure_id);

-- Bausteine: nr ist die feste Nummer je Art (Thema <art>-<nr>), position nur die Reihenfolge
-- auf der Seite. Die Nummer ändert sich nie, auch nicht beim Löschen oder Verschieben anderer.
CREATE TABLE IF NOT EXISTS esppdh_blocks (
    id        UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    device_id UUID NOT NULL REFERENCES esppdh_devices(id) ON DELETE CASCADE,
    position  INT NOT NULL,
    kind      TEXT NOT NULL,
    nr        INT NOT NULL CHECK (nr >= 1),
    params    JSONB NOT NULL DEFAULT '{}',
    UNIQUE (device_id, kind, nr)
);

CREATE TABLE IF NOT EXISTS esppdh_builds (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    device_id       UUID NOT NULL REFERENCES esppdh_devices(id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('queued', 'running', 'ok', 'failed', 'canceled')),
    log             TEXT NOT NULL DEFAULT '',
    config_hash     TEXT NOT NULL,
    esphome_version TEXT NOT NULL DEFAULT '',
    firmware_sha256 TEXT NOT NULL DEFAULT '',
    firmware_size   BIGINT NOT NULL DEFAULT 0,
    requested_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS esppdh_builds_device_idx ON esppdh_builds (device_id, created_at DESC);

INSERT INTO permissions (key, label, category) VALUES
    ('esppdh.view',  'ESPPDH: Geräte und Zustand ansehen', 'Daten & Schnittstellen'),
    ('esppdh.edit',  'ESPPDH: Geräte anlegen, ändern, sperren, bauen und flashen', 'Daten & Schnittstellen'),
    ('esppdh.admin', 'ESPPDH: Geräte löschen (Bereichsadministrator)', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Ansehen und pflegen: alle Rollen und Einzelrechte, die heute Import-Verbindungen verwalten
-- (import.write) – in derselben Abteilung wie dieses Recht.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'import.write'
  CROSS JOIN permissions p
 WHERE p.key IN ('esppdh.view', 'esppdh.edit')
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key = 'import.write'
  CROSS JOIN permissions p
 WHERE p.key IN ('esppdh.view', 'esppdh.edit')
ON CONFLICT (user_id, permission_id) DO NOTHING;

-- Löschen (Bereichsadministrator): Rollen, die Import-Verbindungen verwalten UND die
-- Server-Einstellungen ändern dürfen – beides überall.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id
  FROM roles r
  CROSS JOIN permissions p
 WHERE p.key = 'esppdh.admin'
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'import.write')
   AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
                WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- Wem eines der beiden Rechte einzeln verboten ist, dem bleibt auch esppdh.admin verboten.
INSERT INTO user_permissions (user_id, permission_id, granted)
SELECT DISTINCT up.user_id, p.id, false
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key IN ('import.write', 'system.server_config')
  CROSS JOIN permissions p
 WHERE p.key = 'esppdh.admin' AND NOT up.granted
ON CONFLICT (user_id, permission_id) DO NOTHING;
