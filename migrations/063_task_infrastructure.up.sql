-- 063_task_infrastructure.up.sql
--
-- Aufgaben koennen jetzt optional einer Infrastruktur-Anlage zugeordnet
-- werden - Grundlage fuer die Stammkarten-Historie (Stoerungs-, Ticket-
-- und Aufgabenhistorie einer Anlage). Anders als bei Stoerung/Ticket/
-- Wartung ist die Zuordnung bewusst optional, da Aufgaben primaer an
-- Projekten haengen.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS infrastructure_id UUID REFERENCES infrastructure(id);
CREATE INDEX IF NOT EXISTS idx_tasks_infrastructure ON tasks(infrastructure_id);
