-- 019_maintenance_checklist_templates.up.sql
-- Wartungs-Checklisten-Vorlagen mit Intervall je Punkt und Durchfuehrungslog.

ALTER TABLE maintenance_plans
    ADD COLUMN IF NOT EXISTS checklist_template_id UUID NULL;

ALTER TABLE maintenance_plans
    ADD COLUMN IF NOT EXISTS default_duration_min INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS maintenance_checklist_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT true,
    created_by  UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_maintenance_checklist_templates_name_active
    ON maintenance_checklist_templates (lower(name))
    WHERE active=true;

CREATE TABLE IF NOT EXISTS maintenance_checklist_template_items (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id      UUID NOT NULL REFERENCES maintenance_checklist_templates(id) ON DELETE CASCADE,
    label            TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    item_type        TEXT NOT NULL DEFAULT 'checkbox',
    required         BOOLEAN NOT NULL DEFAULT true,
    interval_days    INTEGER NOT NULL,
    sort_order       INTEGER NOT NULL DEFAULT 100,
    active           BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_maintenance_template_item_interval_days_positive CHECK (interval_days > 0)
);

CREATE INDEX IF NOT EXISTS idx_maintenance_template_items_template
    ON maintenance_checklist_template_items(template_id, active, sort_order);

CREATE TABLE IF NOT EXISTS maintenance_task_checklist_results (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id          UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
    template_item_id UUID NOT NULL REFERENCES maintenance_checklist_template_items(id) ON DELETE CASCADE,
    value            TEXT NOT NULL DEFAULT '',
    done             BOOLEAN NOT NULL DEFAULT false,
    checked_by       UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    checked_at       TIMESTAMPTZ NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(task_id, template_item_id)
);

CREATE INDEX IF NOT EXISTS idx_maintenance_task_checklist_results_task
    ON maintenance_task_checklist_results(task_id);

CREATE INDEX IF NOT EXISTS idx_maintenance_task_checklist_results_item_checked
    ON maintenance_task_checklist_results(template_item_id, checked_at);
