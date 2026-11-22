-- ESPPDH: Firmware übers Netz aufspielen (0.82.24). Kennwort je Gerät für den Update-Baustein,
-- Aufträge zum Aufspielen (einzeln oder als Sammellauf) und das Recht esppdh.ota.

ALTER TABLE esppdh_devices ADD COLUMN IF NOT EXISTS ota_password_enc text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS esppdh_ota_runs (
    id           uuid PRIMARY KEY DEFAULT pdh_uuid_v7(),
    device_id    uuid NOT NULL REFERENCES esppdh_devices(id) ON DELETE CASCADE,
    build_id     uuid REFERENCES esppdh_builds(id) ON DELETE SET NULL,
    batch_id     uuid,
    status       text NOT NULL CHECK (status IN ('queued', 'running', 'ok', 'unconfirmed', 'failed', 'canceled', 'skipped')),
    log          text NOT NULL DEFAULT '',
    address      text NOT NULL DEFAULT '',
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);
CREATE INDEX IF NOT EXISTS esppdh_ota_runs_device_idx ON esppdh_ota_runs (device_id, created_at DESC);
CREATE INDEX IF NOT EXISTS esppdh_ota_runs_batch_idx ON esppdh_ota_runs (batch_id) WHERE batch_id IS NOT NULL;
-- ein wartender oder laufender Auftrag je Gerät
CREATE UNIQUE INDEX IF NOT EXISTS esppdh_ota_runs_active_uq ON esppdh_ota_runs (device_id) WHERE status IN ('queued', 'running');

INSERT INTO permissions (key, label, category) VALUES
    ('esppdh.ota', 'ESPPDH: Firmware übers Netz aufspielen', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Wer bisher Geräte bauen und flashen durfte (esppdh.edit), darf auch übers Netz aufspielen,
-- in derselben Abteilung. Zuerst, wo das Recht überall gilt, danach der Rest.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT DISTINCT ON (rp.role_id) rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'esppdh.edit'
  CROSS JOIN permissions p
 WHERE p.key = 'esppdh.ota'
 ORDER BY rp.role_id, rp.department_id NULLS FIRST
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
 WHERE EXCLUDED.department_id IS NULL;

INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
SELECT DISTINCT ON (up.user_id) up.user_id, p.id, up.granted, up.department_id
  FROM user_permissions up
  JOIN permissions o ON o.id = up.permission_id AND o.key = 'esppdh.edit'
  CROSS JOIN permissions p
 WHERE p.key = 'esppdh.ota' AND up.granted
 ORDER BY up.user_id, up.department_id NULLS FIRST
ON CONFLICT (user_id, permission_id) DO UPDATE SET department_id = NULL
 WHERE EXCLUDED.department_id IS NULL;
