-- 203_terminal_slides_website.up.sql
--
-- Infobildschirm: neue Folienart website (eine Webseite im abgeschotteten Rahmen zeigen).
-- Die Adresse steht wie bei grafana in der Spalte url, deren Längengrenze von 1000 auf
-- 2048 Zeichen wächst; neu ist reload_minutes (0 = nie neu laden, sonst Minuten bis 1440).
-- Alles nur hinzufügend bzw. gelockert: vorhandene Folien und ältere PDH-Stände bleiben gültig.

ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS reload_minutes INT NOT NULL DEFAULT 0;

DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_reload_minutes_check CHECK (reload_minutes BETWEEN 0 AND 1440);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_url_check;
DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_url_len_check CHECK (char_length(url) <= 2048);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_kind_check;
ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_kind_check
    CHECK (kind IN ('image', 'text', 'vorgaenge', 'stats_faults', 'stats_tickets', 'stats_setup', 'stats_downtime', 'grafana', 'website'));
