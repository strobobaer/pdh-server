-- 035_maintenance_actions_and_parts_usage.up.sql
--
-- Uebertraegt die Massnahmen-Verlauf + Ersatzteil-Merkliste-Routine auf
-- Wartungsauftraege (maintenance_tasks). Das bestehende "notes"-Feld
-- (Abschluss-Notiz) bleibt unveraendert bestehen; die Massnahmen sind ein
-- zusaetzlicher, strukturierter Verlauf mit mehreren Eintraegen.

CREATE TABLE IF NOT EXISTS maintenance_task_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_maintenance_task_actions_task ON maintenance_task_actions(task_id);

CREATE TABLE IF NOT EXISTS maintenance_task_pending_parts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
    part_id         UUID NOT NULL REFERENCES spare_parts(id),
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    qty             NUMERIC NOT NULL,
    created_by      UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_maintenance_task_pending_parts_task ON maintenance_task_pending_parts(task_id);

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS maintenance_task_id UUID REFERENCES maintenance_tasks(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_stock_movements_maintenance_task ON stock_movements(maintenance_task_id);

ALTER TABLE maintenance_tasks ADD COLUMN IF NOT EXISTS no_parts_needed BOOLEAN NOT NULL DEFAULT false;
