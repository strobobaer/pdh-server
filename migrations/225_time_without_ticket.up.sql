-- 225_time_without_ticket.up.sql
--
-- Zeit ohne Vorgang buchen: auf eine Anlage, auf ein IT-Asset oder als „Sonstiges“ mit Freitext – am Terminal
-- (Seite /terminal/zeit) und in PDH (/time). Alles nur hinzufügend; ältere Server ignorieren die neuen Spalten.
--
-- time_entries.target_kind
--   Art des Ziels einer Buchung ohne Vorgang: plant (Anlage), it_asset, other (Sonstiges). NULL bei allen bisherigen
--   Einträgen. Die Buchung behält ref_type = 'production' (wie bisher „Sonstiges“); ältere PDH-Versionen zeigen sie
--   deshalb weiter als Sonstiges.
-- time_entries.it_asset_id
--   Das gebuchte IT-Asset (ref_id trägt ebenfalls die Asset-Kennung).
-- work_sessions
--   ref_type erlaubt zusätzlich it_asset und other (der bisherige Prüfwert wird nur erweitert: alle alten Werte
--   bleiben gültig, ältere Server schreiben nichts Neues). Neue Spalten: it_asset_id, activity_id (optionale
--   Tätigkeit) und description (Freitext bei „Sonstiges“, optionaler Zusatz bei Anlage und IT-Asset).
ALTER TABLE time_entries ADD COLUMN IF NOT EXISTS target_kind TEXT;
ALTER TABLE time_entries DROP CONSTRAINT IF EXISTS time_entries_target_kind_check;
ALTER TABLE time_entries ADD CONSTRAINT time_entries_target_kind_check CHECK (target_kind IS NULL OR target_kind IN ('plant', 'it_asset', 'other'));
ALTER TABLE time_entries ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_time_entries_it_asset ON time_entries (it_asset_id) WHERE it_asset_id IS NOT NULL;

ALTER TABLE work_sessions DROP CONSTRAINT IF EXISTS work_sessions_ref_type_check;
ALTER TABLE work_sessions ADD CONSTRAINT work_sessions_ref_type_check CHECK (ref_type IN ('fault', 'ticket', 'task', 'maintenance', 'plant', 'it_asset', 'other'));
ALTER TABLE work_sessions ADD COLUMN IF NOT EXISTS it_asset_id UUID REFERENCES it_assets(id) ON DELETE SET NULL;
ALTER TABLE work_sessions ADD COLUMN IF NOT EXISTS activity_id UUID REFERENCES cost_activities(id) ON DELETE SET NULL;
ALTER TABLE work_sessions ADD COLUMN IF NOT EXISTS description TEXT;
