-- Kontrollrundgang: ein Wartungsplan, der nacheinander mehrere Anlagen
-- (Stationen) mit je einer Checkliste abfragt. Termine, Auftrag, Assistent,
-- Protokoll und Leitstand laufen ueber die normale Wartung.

ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS is_round BOOLEAN NOT NULL DEFAULT false;

-- Station = Plan-Checkliste mit eigener Anlage (leer = Anlage des Plans)
ALTER TABLE maintenance_plan_checklists
    ADD COLUMN IF NOT EXISTS infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE CASCADE;

-- dieselbe Checkliste darf an mehreren Stationen stehen
ALTER TABLE maintenance_plan_checklists DROP CONSTRAINT IF EXISTS maintenance_plan_checklists_plan_id_template_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_maintenance_plan_checklists_station
    ON maintenance_plan_checklists (plan_id, template_id, COALESCE(infrastructure_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- Schritte merken sich ihre Station (Name als Momentaufnahme fuer das Protokoll)
ALTER TABLE maintenance_task_steps
    ADD COLUMN IF NOT EXISTS station_infra_id UUID REFERENCES infrastructure(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS station_name TEXT NOT NULL DEFAULT '';
