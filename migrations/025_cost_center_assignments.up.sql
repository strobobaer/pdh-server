-- 025_cost_center_assignments.up.sql
--
-- Eigene, unabhängige Kostenstellen-Zuordnung (zusätzlich zur Infrastruktur-
-- Zuordnung) für Tickets, Störungen und Wartungspläne/-aufträge.

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id);
ALTER TABLE faults ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id);
ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id);
ALTER TABLE maintenance_tasks ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id);

CREATE INDEX IF NOT EXISTS idx_tickets_cost_center      ON tickets(cost_center_id);
CREATE INDEX IF NOT EXISTS idx_faults_cost_center       ON faults(cost_center_id);
CREATE INDEX IF NOT EXISTS idx_maint_plans_cost_center  ON maintenance_plans(cost_center_id);
CREATE INDEX IF NOT EXISTS idx_maint_tasks_cost_center  ON maintenance_tasks(cost_center_id);
