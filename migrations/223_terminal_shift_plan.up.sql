-- 223_terminal_shift_plan.up.sql
--
-- Schichtplan am Terminal: Auswahl des Plans. Grundeinstellung je Terminal (terminal_settings.shift_plan_id)
-- und je Planungsfolie „Schichtplan“ (terminal_slides.shift_plan_id). NULL = alle veröffentlichten Pläne
-- (Verhalten wie bisher). Wird der Plan gelöscht, fällt die Einstellung auf „Alle“ zurück. Nur hinzufügend.

ALTER TABLE terminal_settings ADD COLUMN IF NOT EXISTS shift_plan_id UUID NULL REFERENCES shift_plans(id) ON DELETE SET NULL;
ALTER TABLE terminal_slides ADD COLUMN IF NOT EXISTS shift_plan_id UUID NULL REFERENCES shift_plans(id) ON DELETE SET NULL;
