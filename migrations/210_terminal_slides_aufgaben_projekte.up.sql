-- 210_terminal_slides_aufgaben_projekte.up.sql
--
-- Infobildschirm: zwei neue Folienarten – tasks (offene Aufgaben nach Fälligkeit, überfällige oben) und
-- projects_year (Jahresband der Projekte mit Balken von Start bis Ende).
-- Je Folie task_range (week, 2w, 4w, 8w, 12w, date; bei date task_until = letzter Fälligkeitstag), year_mode
-- (current, next, rolling = ab heute 12 Monate) und only_active (nur Projekte mit Status aktiv). Die Abteilungen
-- stehen wie bei den Planungsfolien in department_ids (Migration 208; leer = alle).
-- Alles nur hinzufügend bzw. gelockert: vorhandene Folien und ältere PDH-Stände bleiben gültig, ältere Stände
-- ignorieren die neuen Spalten. Es entstehen keine neuen Tabellen und keine neuen Datensatz-IDs.

ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS task_range TEXT NOT NULL DEFAULT 'week';
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS task_until DATE;
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS year_mode TEXT NOT NULL DEFAULT 'current';
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS only_active BOOLEAN NOT NULL DEFAULT false;

DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_task_range_check CHECK (task_range IN ('week', '2w', '4w', '8w', '12w', 'date'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_year_mode_check CHECK (year_mode IN ('current', 'next', 'rolling'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_kind_check;
ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_kind_check
    CHECK (kind IN ('image', 'text', 'vorgaenge', 'stats_faults', 'stats_tickets', 'stats_setup', 'stats_downtime', 'grafana', 'website',
                    'plan_shifts', 'plan_absences', 'plan_duties', 'plan_maint', 'tasks', 'projects_year'));
