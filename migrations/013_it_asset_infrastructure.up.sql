-- 013_it_asset_infrastructure.up.sql
-- IT-Assets mit Infrastrukturbaum verknüpfen

ALTER TABLE it_assets
  ADD COLUMN IF NOT EXISTS infrastructure_id UUID REFERENCES infrastructure(id);

CREATE INDEX IF NOT EXISTS idx_it_infrastructure ON it_assets(infrastructure_id);
