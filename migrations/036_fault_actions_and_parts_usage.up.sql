-- 036_fault_actions_and_parts_usage.up.sql
--
-- Massnahmen-Verlauf und Ersatzteil-Merkliste fuer Stoerungen (faults),
-- analog zu Tickets (034) und Wartungsauftraegen (032/035).

CREATE TABLE IF NOT EXISTS fault_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fault_id    UUID NOT NULL REFERENCES faults(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_fault_actions_fault ON fault_actions(fault_id);

CREATE TABLE IF NOT EXISTS fault_pending_parts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fault_id        UUID NOT NULL REFERENCES faults(id) ON DELETE CASCADE,
    part_id         UUID NOT NULL REFERENCES spare_parts(id),
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    qty             NUMERIC NOT NULL,
    created_by      UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_fault_pending_parts_fault ON fault_pending_parts(fault_id);

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS fault_id UUID REFERENCES faults(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_stock_movements_fault ON stock_movements(fault_id);

ALTER TABLE faults ADD COLUMN IF NOT EXISTS no_parts_needed BOOLEAN NOT NULL DEFAULT false;

