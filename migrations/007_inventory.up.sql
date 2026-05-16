-- 007_inventory.up.sql

DO $$ BEGIN
    CREATE TYPE stock_movement_type AS ENUM ('in','out','transfer','correction');
EXCEPTION WHEN duplicate_object THEN null; END $$;

-- ============================================================
-- ERSATZTEILE
-- ============================================================
CREATE TABLE IF NOT EXISTS spare_parts (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    part_number       VARCHAR(100) NOT NULL,
    name              VARCHAR(255) NOT NULL,
    description       TEXT,
    category          VARCHAR(100),
    manufacturer      VARCHAR(100),
    manufacturer_part VARCHAR(100),
    unit              VARCHAR(20) NOT NULL DEFAULT 'Stück',
    stock_qty         NUMERIC(12,3) NOT NULL DEFAULT 0,
    min_qty           NUMERIC(12,3) NOT NULL DEFAULT 1,
    critical_qty      NUMERIC(12,3) NOT NULL DEFAULT 0,
    reorder_qty       NUMERIC(12,3) NOT NULL DEFAULT 5,
    storage_location  VARCHAR(100),
    storage_place     VARCHAR(100),
    price             NUMERIC(10,2) NOT NULL DEFAULT 0,
    infrastructure_id UUID REFERENCES infrastructure(id),
    active            BOOLEAN NOT NULL DEFAULT true,
    created_by        UUID NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_spare_category ON spare_parts(category);
CREATE INDEX IF NOT EXISTS idx_spare_infra    ON spare_parts(infrastructure_id);
CREATE INDEX IF NOT EXISTS idx_spare_stock    ON spare_parts(stock_qty);
CREATE UNIQUE INDEX IF NOT EXISTS idx_spare_partno ON spare_parts(part_number) WHERE active=true;

-- ============================================================
-- LAGERBEWEGUNGEN
-- ============================================================
CREATE TABLE IF NOT EXISTS stock_movements (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    part_id     UUID NOT NULL REFERENCES spare_parts(id),
    type        stock_movement_type NOT NULL,
    qty         NUMERIC(12,3) NOT NULL,
    qty_before  NUMERIC(12,3) NOT NULL,
    qty_after   NUMERIC(12,3) NOT NULL,
    reference   VARCHAR(255),
    notes       TEXT,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_movement_part ON stock_movements(part_id);
CREATE INDEX IF NOT EXISTS idx_movement_date ON stock_movements(created_at);

-- ============================================================
-- BEISPIEL-DATEN
-- ============================================================
-- Werden nach Benutzeranlage eingefügt
