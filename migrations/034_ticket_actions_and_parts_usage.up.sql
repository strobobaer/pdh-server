-- 034_ticket_actions_and_parts_usage.up.sql
--
-- Uebertraegt die Stoerungs-Routine (Massnahmen-Verlauf + Ersatzteil-
-- Merkliste, Pflicht beim Schliessen) auf Tickets.

CREATE TABLE IF NOT EXISTS ticket_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id   UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ticket_actions_ticket ON ticket_actions(ticket_id);

CREATE TABLE IF NOT EXISTS ticket_pending_parts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id       UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    part_id         UUID NOT NULL REFERENCES spare_parts(id),
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    qty             NUMERIC NOT NULL,
    created_by      UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ticket_pending_parts_ticket ON ticket_pending_parts(ticket_id);

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS ticket_id UUID REFERENCES tickets(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_stock_movements_ticket ON stock_movements(ticket_id);

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS resolution       TEXT;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS root_cause       TEXT;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS no_parts_needed  BOOLEAN NOT NULL DEFAULT false;
