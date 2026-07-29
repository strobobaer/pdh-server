-- 044_fault_due_date.up.sql
--
-- Faelligkeitsdatum fuer Stoerungen, analog zu tickets.due_date.
-- Noetig fuer die kombinierte Gantt-Ansicht im Dashboard: bisher hatten
-- Stoerungen ueberhaupt kein Datumsfeld ausser dem Erkennungszeitpunkt.

ALTER TABLE faults
    ADD COLUMN IF NOT EXISTS due_date TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_faults_due_date ON faults(due_date);
