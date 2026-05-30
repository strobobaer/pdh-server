-- 020_maintenance_multi_checklist_templates.up.sql
-- Mehrere Wartungs-Checklisten je Wartungsplan plus weiches Loeschen.

CREATE TABLE IF NOT EXISTS maintenance_plan_checklist_templates (
    plan_id     UUID NOT NULL REFERENCES maintenance_plans(id) ON DELETE CASCADE,
    template_id UUID NOT NULL REFERENCES maintenance_checklist_templates(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (plan_id, template_id)
);

INSERT INTO maintenance_plan_checklist_templates (plan_id, template_id)
SELECT id, checklist_template_id
FROM maintenance_plans
WHERE checklist_template_id IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_maintenance_plan_checklist_templates_plan
    ON maintenance_plan_checklist_templates(plan_id);

CREATE INDEX IF NOT EXISTS idx_maintenance_plan_checklist_templates_template
    ON maintenance_plan_checklist_templates(template_id);
