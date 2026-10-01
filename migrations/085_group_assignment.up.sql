-- 085_group_assignment.up.sql
-- Vorgaenge koennen zusaetzlich einer Gruppe zugewiesen werden (neben bzw.
-- statt einer Person). Mitglieder der Gruppe sehen den Vorgang unter
-- "Mir zugewiesen" und bekommen die Aenderungshinweise.

ALTER TABLE tickets           ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;
ALTER TABLE faults            ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;
ALTER TABLE maintenance_tasks ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;
ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;
ALTER TABLE tasks             ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;
ALTER TABLE projects          ADD COLUMN IF NOT EXISTS assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_tickets_group           ON tickets(assigned_group_id) WHERE assigned_group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_faults_group            ON faults(assigned_group_id) WHERE assigned_group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_group ON maintenance_tasks(assigned_group_id) WHERE assigned_group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_group             ON tasks(assigned_group_id) WHERE assigned_group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_projects_group          ON projects(assigned_group_id) WHERE assigned_group_id IS NOT NULL;
