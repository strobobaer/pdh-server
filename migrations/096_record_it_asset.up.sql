-- 096_record_it_asset.up.sql
--
-- QR-Infoseiten (/a/<id>): Vorgaenge koennen zusaetzlich zur Anlage einem
-- IT-Asset zugeordnet werden, damit die Infoseite eines Geraets genau seine
-- offenen Tickets, Stoerungen, Aufgaben und Wartungen zeigt.

ALTER TABLE tickets           ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;
ALTER TABLE faults            ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;
ALTER TABLE tasks             ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;
ALTER TABLE maintenance_tasks ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_tickets_it_asset           ON tickets (it_asset_id) WHERE it_asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_faults_it_asset            ON faults (it_asset_id) WHERE it_asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_it_asset             ON tasks (it_asset_id) WHERE it_asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_it_asset ON maintenance_tasks (it_asset_id) WHERE it_asset_id IS NOT NULL;
