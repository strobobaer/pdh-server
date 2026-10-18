-- 119_cost_center_structure.up.sql
-- Kostenstellen-Dashboard im Core-Bereich (internal/web/costcenters_web.go):
--   * Unterstellen: cost_centers.parent_id (eine Kostenstelle unter einer anderen)
--   * Verantwortliche/r und Beschreibung je Kostenstelle
--   * Tätigkeiten (cost_activities) – für alle oder für eine Kostenstelle
--   * Zeiterfassung: time_entries.activity_id und cost_center_id

ALTER TABLE cost_centers
    ADD COLUMN IF NOT EXISTS parent_id      UUID REFERENCES cost_centers(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS description    TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS responsible_id UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_cost_centers_parent ON cost_centers(parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS cost_activities (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number         VARCHAR(20) NOT NULL DEFAULT '',
    name           VARCHAR(255) NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    cost_center_id UUID REFERENCES cost_centers(id) ON DELETE CASCADE, -- NULL = für alle Kostenstellen
    active         BOOLEAN NOT NULL DEFAULT true,
    sort           INTEGER NOT NULL DEFAULT 0,
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cost_activities_cc ON cost_activities(cost_center_id);

ALTER TABLE time_entries
    ADD COLUMN IF NOT EXISTS activity_id    UUID REFERENCES cost_activities(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_time_entries_activity ON time_entries(activity_id) WHERE activity_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_time_entries_cost_center ON time_entries(cost_center_id) WHERE cost_center_id IS NOT NULL;

INSERT INTO permissions (key, label, category) VALUES
    ('costcenters.manage', 'Kostenstellen, Unterstellen & Tätigkeiten verwalten', 'System')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key = 'costcenters.manage'
ON CONFLICT DO NOTHING;
