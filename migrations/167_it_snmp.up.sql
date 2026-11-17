-- 167_it_snmp.up.sql
-- IT-Assets aus dem Netz, Paket 3 (0.80.22): SNMP v3 als dritte Quelle des Netz-Abgleichs
-- (internal/web/snmp_store.go, it_net_snmp.go). Zusatz: Ohne Profile und Geräte ändert sich
-- nichts. Die Kennwörter der Profile liegen nur verschlüsselt in der Datenbank.

-- ── Zugangsprofile (ein Profil gilt für viele Geräte) ─────────────────────
CREATE TABLE IF NOT EXISTS it_snmp_profiles (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name            TEXT NOT NULL,
    username        TEXT NOT NULL DEFAULT '',
    auth_proto      TEXT NOT NULL DEFAULT 'sha256',
    auth_secret_enc TEXT NOT NULL DEFAULT '',
    priv_proto      TEXT NOT NULL DEFAULT 'aes128',
    priv_secret_enc TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS it_snmp_profiles_name ON it_snmp_profiles (lower(name));

-- ── Geräteliste (jedes Gerät wird einzeln gefragt) ────────────────────────
-- note: Hinweis des Abgleichs, z. B. „schon über UniFi bekannt“.
CREATE TABLE IF NOT EXISTS it_snmp_targets (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    host        TEXT NOT NULL,
    port        INT  NOT NULL DEFAULT 161,
    profile_id  UUID NOT NULL REFERENCES it_snmp_profiles(id) ON DELETE RESTRICT,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    last_ok     TIMESTAMPTZ,
    last_error  TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    created_by  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS it_snmp_targets_host_port ON it_snmp_targets (lower(host), port);
CREATE INDEX IF NOT EXISTS it_snmp_targets_profile ON it_snmp_targets (profile_id);

-- ── Recht ─────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('system.snmp', 'SNMP-Zugangsprofile verwalten', 'System & Sicherheit')
ON CONFLICT (key) DO NOTHING;

-- Wer heute die UniFi-Anbindung einrichten darf, darf auch die SNMP-Profile verwalten –
-- niemand wird durch das Update ausgesperrt und niemand bekommt mehr als bisher. Die
-- Abteilung der Quellzeile wird übernommen (je Rolle und Recht gibt es nur eine Zeile).
-- Eine schon vorhandene Zeile bleibt, wie sie ist: Ein erneuter Lauf weitet nichts aus.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.snmp'
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- Einzelrechte je Benutzer genauso – auch ein ausdrücklich entzogenes Recht (granted = false),
-- damit ein Benutzer ohne UniFi-Recht nicht über seine Rolle an die SNMP-Profile kommt.
INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key = 'system.unifi'
  CROSS JOIN permissions p
 WHERE p.key = 'system.snmp'
ON CONFLICT (user_id, permission_id) DO NOTHING;
