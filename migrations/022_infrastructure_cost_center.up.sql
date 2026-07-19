-- 022_infrastructure_cost_center.up.sql
--
-- Fuegt eine Kostenstelle (Freitext, z.B. "KST-4711") zu jedem
-- Infrastruktur-Knoten (Gebaeude/Linie/Anlage/Geraet) hinzu.

ALTER TABLE infrastructure
    ADD COLUMN IF NOT EXISTS cost_center VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_infra_cost_center ON infrastructure(cost_center);
