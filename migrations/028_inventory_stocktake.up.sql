-- 028_inventory_stocktake.up.sql
--
-- Inventur (Stocktake): Zählsitzungen über einen Lagerort-Bereich
-- ("von"-"bis", Geschwister unter demselben Elternknoten). Waehrend eine
-- Sitzung offen ist ('open'), sind die betroffenen Lagerorte gegen normale
-- Buchungen gesperrt (siehe Service.Book in inventory.go) - die Sperre
-- betrifft NUR den gezaehlten Bereich, nicht das gesamte Lager.

DO $$ BEGIN
    CREATE TYPE stocktake_status AS ENUM ('open', 'booked', 'cancelled');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS inventory_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    to_node_id   UUID NOT NULL REFERENCES storage_nodes(id),
    status       stocktake_status NOT NULL DEFAULT 'open',
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    booked_by    UUID REFERENCES users(id),
    booked_at    TIMESTAMPTZ
);

-- Zaehlliste: eine Zeile je (Lagerort, Ersatzteil). Leere Plaetze bekommen
-- eine Platzhalterzeile mit part_id = NULL, expected_qty = 0.
CREATE TABLE IF NOT EXISTS inventory_session_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES inventory_sessions(id) ON DELETE CASCADE,
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    part_id         UUID REFERENCES spare_parts(id),
    expected_qty    NUMERIC(12,3) NOT NULL DEFAULT 0,
    counted_qty     NUMERIC(12,3),
    counted_by      UUID REFERENCES users(id),
    counted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_inv_session_items_session ON inventory_session_items(session_id);
CREATE INDEX IF NOT EXISTS idx_inv_session_items_node    ON inventory_session_items(storage_node_id);
CREATE INDEX IF NOT EXISTS idx_inv_sessions_status       ON inventory_sessions(status);
