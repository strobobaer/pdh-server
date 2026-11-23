-- 212_maintenance_yearplan.up.sql
-- Wartung: geplante Termine (Jahresplan), Vorlauf des Auftrags in Arbeitstagen,
-- „anstehend ab“ am Auftrag, Outlook-Art für geplante Termine.

CREATE TABLE maintenance_visits (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    plan_id           UUID NOT NULL REFERENCES maintenance_plans(id) ON DELETE CASCADE,
    calc_day          DATE NOT NULL,                 -- gerechneter Tag (Rhythmus)
    day               DATE NOT NULL,                 -- Tag (ggf. verschoben)
    assigned_to       UUID REFERENCES users(id) ON DELETE SET NULL,
    assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL,
    date_manual       BOOLEAN NOT NULL DEFAULT false, -- Tag von Hand geändert
    staff_manual      BOOLEAN NOT NULL DEFAULT false, -- Person/Gruppe von Hand geändert
    off_plan          BOOLEAN NOT NULL DEFAULT false, -- „weicht vom Plan ab“
    state             TEXT NOT NULL DEFAULT 'planned' CHECK (state IN ('planned', 'ordered', 'dropped')),
    task_id           UUID REFERENCES maintenance_tasks(id) ON DELETE SET NULL,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_maintenance_visits_planned ON maintenance_visits (plan_id, calc_day) WHERE state = 'planned';
CREATE UNIQUE INDEX uq_maintenance_visits_task ON maintenance_visits (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_maintenance_visits_day ON maintenance_visits (day) WHERE state <> 'ordered';
CREATE INDEX idx_maintenance_visits_plan ON maintenance_visits (plan_id, day);

-- Vorlauf in Arbeitstagen (NULL = Vorgabe der Wartungsregeln); lead_days bleibt als Rückweg
ALTER TABLE maintenance_plans ADD COLUMN lead_workdays SMALLINT CHECK (lead_workdays BETWEEN 0 AND 60);
ALTER TABLE maintenance_plans ADD COLUMN year_plan_at TIMESTAMPTZ;   -- Jahresplan erzeugt (NULL = nein)
UPDATE maintenance_plans SET lead_workdays = LEAST(60, CEIL(lead_days * 5 / 7.0))::smallint WHERE lead_days > 0;

ALTER TABLE maintenance_tasks ADD COLUMN planned_for DATE;            -- gerechneter Tag (NULL = due_date)
ALTER TABLE maintenance_tasks ADD COLUMN upcoming_from DATE;          -- „anstehend ab“ (NULL = ab Fälligkeit)
ALTER TABLE maintenance_tasks ADD COLUMN due_manual BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE maintenance_tasks ADD COLUMN staff_manual BOOLEAN NOT NULL DEFAULT false;
UPDATE maintenance_tasks mt SET upcoming_from = mt.due_date::date - mp.lead_days
  FROM maintenance_plans mp WHERE mp.id = mt.plan_id AND mt.status IN ('open', 'in_progress', 'pending');

ALTER TABLE microsoft_calendar_links DROP CONSTRAINT IF EXISTS microsoft_calendar_links_source_type_check;
ALTER TABLE microsoft_calendar_links ADD CONSTRAINT microsoft_calendar_links_source_type_check
  CHECK (source_type IN ('shift', 'task', 'maintenance', 'maintenance_planned', 'ticket', 'fault'));
