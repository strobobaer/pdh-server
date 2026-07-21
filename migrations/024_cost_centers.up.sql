-- 024_cost_centers.up.sql
--
-- Eigenständige Kostenstellen-Liste (Nummer + Name), unabhängig von der
-- Infrastruktur. Tickets, Störungen und Wartungsaufgaben bekommen in einer
-- Folge-Migration jeweils ein eigenes cost_center_id-Feld, das hierher zeigt.

CREATE TABLE IF NOT EXISTS cost_centers (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number     VARCHAR(20) NOT NULL,
    name       VARCHAR(255) NOT NULL,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(number)
);

CREATE INDEX IF NOT EXISTS idx_cost_centers_active ON cost_centers(active);
