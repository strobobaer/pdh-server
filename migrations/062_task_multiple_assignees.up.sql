-- 062_task_multiple_assignees.up.sql
--
-- Aufgaben koennen jetzt mehreren Mitarbeitern gleichzeitig zugewiesen
-- werden (z.B. mehrere arbeiten gemeinsam daran) - die Verantwortung
-- (tasks.responsible_to) bleibt bewusst auf eine Person beschraenkt und
-- unveraendert. Ersetzt die bisherige Einzelspalte tasks.assigned_to.

CREATE TABLE IF NOT EXISTS task_assignees (
    task_id    UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (task_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_task_assignees_user ON task_assignees(user_id);

-- Bestehende Einzelzuweisungen uebernehmen, bevor die alte Spalte faellt.
INSERT INTO task_assignees (task_id, user_id)
SELECT id, assigned_to FROM tasks WHERE assigned_to IS NOT NULL
ON CONFLICT DO NOTHING;

ALTER TABLE tasks DROP COLUMN IF EXISTS assigned_to;
