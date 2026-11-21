-- 190_terminal_slides_stats.up.sql
--
-- Infobildschirm: neue Folienarten stats_faults, stats_tickets, stats_setup
-- (Statistik zu Störungen, Tickets, Rüstzeiten für die gewählten Anlagen samt
-- Unteranlagen, leer = alle) und grafana (ein Grafana-Dashboard einbetten).
-- Zusätzliche Spalten: stats_days (Zeitraum 7 oder 30 Tage) und url (Grafana-
-- Adresse). Die Folienart-Prüfung wird nur erweitert; alte Arten und alle
-- vorhandenen Folien bleiben gültig, ältere PDH-Stände ignorieren die Spalten.

ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS stats_days INT NOT NULL DEFAULT 7;
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS url TEXT NOT NULL DEFAULT '';

DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_stats_days_check CHECK (stats_days IN (7, 30));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_url_check CHECK (char_length(url) <= 1000);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_kind_check;
ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_kind_check
    CHECK (kind IN ('image', 'text', 'vorgaenge', 'stats_faults', 'stats_tickets', 'stats_setup', 'grafana'));
