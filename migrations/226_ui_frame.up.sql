-- 225_ui_frame.up.sql
-- Einheitliche Oberfläche (0.90.0 Teil 2): Wahl des Rahmens je Benutzer ('' = Firmenvorgabe,
-- 'classic', 'new') und eigene Anordnung der Bereiche im neuen Rahmen (Reihenfolge, Favoriten).
-- Kein neues Recht: eigene Einstellungen wie die Menü-Anordnung (nav_layout).
ALTER TABLE users ADD COLUMN IF NOT EXISTS ui_frame text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS ui_areas jsonb;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_ui_frame_check;
ALTER TABLE users ADD CONSTRAINT users_ui_frame_check CHECK (ui_frame IN ('', 'classic', 'new'));
