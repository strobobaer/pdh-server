-- 100_maintenance_schedule_mode.up.sql
--
-- Wartungsplaene: wie der naechste Termin nach dem Abschluss berechnet wird.
--   completion – ab Durchfuehrung (Abschlussdatum + Intervall), wie bisher
--   fixed      – fester Rhythmus ab dem Faelligkeitstag (verschiebt sich nicht,
--                wenn frueher oder spaeter erledigt wird)
-- Intervalle werden kalendergenau gerechnet (Monat, Quartal, Jahr).

ALTER TABLE maintenance_plans
    ADD COLUMN IF NOT EXISTS schedule_mode TEXT NOT NULL DEFAULT 'completion'
        CHECK (schedule_mode IN ('completion', 'fixed'));
