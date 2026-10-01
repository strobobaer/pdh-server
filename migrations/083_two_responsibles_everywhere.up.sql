-- 083_two_responsibles_everywhere.up.sql
-- Einheitlich zwei Zustaendigkeiten in allen Modulen: "Zugewiesen" (wer es
-- macht) und "Verantwortlich" (wer dafuer geradesteht). Tickets, Stoerungen,
-- Wartungsauftraege und Aufgaben hatten beides schon; es fehlten
-- Wartungsplaene (nur zugewiesen), Projekte (nur verantwortlich) und
-- IT-Assets (nur zugewiesen).

ALTER TABLE maintenance_plans
    ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS assigned_to UUID REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE it_assets
    ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_projects_assigned ON projects(assigned_to);
CREATE INDEX IF NOT EXISTS idx_it_assets_responsible ON it_assets(responsible_to);
