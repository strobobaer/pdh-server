-- 208_terminal_slides_planung.up.sql
--
-- Infobildschirm: neue Folienarten aus dem Planer – plan_shifts (Schichtplan), plan_absences (Abwesenheiten,
-- nur „abwesend“), plan_duties (Dienste/Bereitschaft) und plan_maint (Wartungsplanung).
-- Je Folie ein Zeitraum (plan_range: today, week, next_week, days; bei days plan_days = 1 bis 31) und eine
-- Auswahl von Abteilungen (department_ids; leer = alle). Alles nur hinzufügend bzw. gelockert: vorhandene
-- Folien und ältere PDH-Stände bleiben gültig, ältere Stände ignorieren die neuen Spalten.

ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS plan_range TEXT NOT NULL DEFAULT 'today';
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS plan_days INT NOT NULL DEFAULT 7;
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS department_ids UUID[];

DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_plan_range_check CHECK (plan_range IN ('today', 'week', 'next_week', 'days'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_plan_days_check CHECK (plan_days BETWEEN 1 AND 31);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_kind_check;
ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_kind_check
    CHECK (kind IN ('image', 'text', 'vorgaenge', 'stats_faults', 'stats_tickets', 'stats_setup', 'stats_downtime', 'grafana', 'website',
                    'plan_shifts', 'plan_absences', 'plan_duties', 'plan_maint'));
