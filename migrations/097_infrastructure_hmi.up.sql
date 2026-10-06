-- 097_infrastructure_hmi.up.sql
--
-- HMI-Fernzugriff (VNC) direkt aus der Anlage: je Anlage beliebig viele
-- HMIs (Bediengeraete) mit Adresse und VNC-Passwort. Der Browser verbindet
-- sich nur ueber den PDH-Server (WebSocket-Proxy); das Passwort verlaesst
-- den Server nie. Stufen ueber Berechtigungen:
--   hmi.view    – Bild ansehen (Eingaben werden serverseitig verworfen)
--   hmi.control – bedienen (Tastatur/Maus), sofern am HMI erlaubt
--   hmi.manage  – HMIs einer Anlage einrichten
-- Jeder Zugriff wird in infrastructure_hmi_sessions protokolliert.

CREATE TABLE IF NOT EXISTS infrastructure_hmis (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    host              TEXT NOT NULL,
    port              INTEGER NOT NULL DEFAULT 5900 CHECK (port BETWEEN 1 AND 65535),
    password_enc      TEXT NOT NULL DEFAULT '',
    allow_control     BOOLEAN NOT NULL DEFAULT true,
    notes             TEXT NOT NULL DEFAULT '',
    sort_order        INTEGER NOT NULL DEFAULT 100,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_infrastructure_hmis_infra ON infrastructure_hmis (infrastructure_id, sort_order);

CREATE TABLE IF NOT EXISTS infrastructure_hmi_sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hmi_id     UUID NOT NULL REFERENCES infrastructure_hmis(id) ON DELETE CASCADE,
    user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    mode       TEXT NOT NULL CHECK (mode IN ('view', 'control')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at   TIMESTAMPTZ,
    error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_infrastructure_hmi_sessions_hmi ON infrastructure_hmi_sessions (hmi_id, started_at DESC);

INSERT INTO permissions (key, label, category) VALUES
    ('hmi.view',    'HMI ansehen (Fernzugriff, nur Bild)', 'Infrastruktur'),
    ('hmi.control', 'HMI bedienen (Fernzugriff mit Tastatur & Maus)', 'Infrastruktur'),
    ('hmi.manage',  'HMI-Verbindungen einrichten', 'Infrastruktur')
ON CONFLICT (key) DO NOTHING;

-- Startbelegung: ansehen fuer Admin/Manager/Techniker, bedienen und
-- einrichten nur Admin (weitere Rollen ueber die Rollen-Matrix).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE (p.key = 'hmi.view' AND r.key IN ('admin', 'manager', 'technician'))
   OR (p.key IN ('hmi.control', 'hmi.manage') AND r.key = 'admin')
ON CONFLICT DO NOTHING;
