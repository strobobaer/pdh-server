-- 079_user_appearance.up.sql
-- Darstellung je Benutzer (Farbschema, Schriftart, Schriftgroesse in %).
-- Leer bzw. 0 = Firmenstandard aus Server-Einstellungen → Erscheinungsbild.
-- Gespeichert am Konto, gilt damit auf allen Geraeten (bei Terminals fuer
-- alle, die den Systembenutzer nutzen).

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS ui_palette TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ui_font    TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ui_scale   INTEGER NOT NULL DEFAULT 0;
