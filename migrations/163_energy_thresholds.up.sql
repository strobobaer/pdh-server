-- Schwellwerte und Alarme an Messstellen (0.80.15).
-- Je Messstelle beliebig viele Schwellwerte; der Wächter legt bei Überschreitung
-- ein Ticket an, das nur mit Ursache geschlossen werden darf.

CREATE TABLE IF NOT EXISTS energy_thresholds (
    id                   UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    meter_id             UUID NOT NULL REFERENCES energy_meters(id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    condition            TEXT NOT NULL CHECK (condition IN ('above', 'below')),
    limit_value          DOUBLE PRECISION NOT NULL,
    window_minutes       INT NOT NULL CHECK (window_minutes BETWEEN 1 AND 1440),
    reset_minutes        INT NOT NULL DEFAULT 15 CHECK (reset_minutes BETWEEN 1 AND 1440),
    tag                  TEXT NOT NULL DEFAULT '',
    priority             TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'critical')),
    responsible_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    responsible_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL,
    active               BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT (responsible_user_id IS NOT NULL AND responsible_group_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_energy_thresholds_meter ON energy_thresholds (meter_id);

-- Laufender Zustand des Wächters je Schwellwert.
CREATE TABLE IF NOT EXISTS energy_threshold_state (
    threshold_id  UUID PRIMARY KEY REFERENCES energy_thresholds(id) ON DELETE CASCADE,
    state         TEXT NOT NULL DEFAULT 'normal' CHECK (state IN ('normal', 'alarm')),
    alarm_since   TIMESTAMPTZ,
    clear_since   TIMESTAMPTZ,
    checked_until TIMESTAMPTZ,
    ticket_id     UUID REFERENCES tickets(id) ON DELETE SET NULL,
    last_value    DOUBLE PRECISION
);

-- Alarm-Tickets lassen sich nur mit beschriebener Ursache schließen.
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requires_root_cause BOOLEAN NOT NULL DEFAULT false;

-- ── Recht ─────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('energy.thresholds', 'Schwellwerte pflegen', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Wer Messstellen pflegen darf, darf auch Schwellwerte pflegen. Die Abteilung
-- der Quellrolle wird übernommen; gilt ein Recht überall (NULL), gewinnt
-- „überall“ (je Rolle und Recht gibt es nur eine Zeile).
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'energy.edit'
  CROSS JOIN permissions p
 WHERE p.key = 'energy.thresholds'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;
