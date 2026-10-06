-- 101_maintenance_v2.up.sql
--
-- Wartungsmodul v2: Daten uebernehmen und umbauen.
--
--   Plaene        Intervall frei waehlbar (alle N Tage/Wochen/Monate/Jahre),
--                 Vorlauf in Tagen, geplante Dauer nur noch in estimated_min.
--   Checklisten   ein Plan hat mehrere Checklisten mit eigenem Takt
--                 (immer, alle N Tage/Wochen/Monate/Jahre). Bei der
--                 Durchfuehrung werden die faelligen zu EINER Liste
--                 zusammengefuehrt. Kein Intervall mehr je Punkt: bisherige
--                 Punkt-Intervalle werden in eigene Takt-Checklisten
--                 aufgeteilt, damit kein Takt verloren geht.
--   Auftraege     bekommen ihre Punkte als Schritte (Momentaufnahme). Alle
--                 bisherigen Ergebnisse werden mit GLEICHER id zu Schritten –
--                 Dokumentationsfotos (attachments ref_type
--                 'maint_check_result') bleiben dadurch gueltig.
--   Verlauf       record_history 'maintenance' -> 'maintenance_task'
--                 (bisher auf der Auftragsseite unsichtbar).
-- Die Tabellen maintenance_plans / maintenance_tasks und ihre Spalten
-- bleiben, weil Leitstand, Dashboard, Zeitstrahl usw. sie direkt lesen.

-- ── Plaene ─────────────────────────────────────────────────────
ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS interval_unit TEXT NOT NULL DEFAULT 'month'
    CHECK (interval_unit IN ('day', 'week', 'month', 'year'));
ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS interval_count INTEGER NOT NULL DEFAULT 1
    CHECK (interval_count BETWEEN 1 AND 1000);
ALTER TABLE maintenance_plans ADD COLUMN IF NOT EXISTS lead_days INTEGER NOT NULL DEFAULT 0
    CHECK (lead_days BETWEEN 0 AND 365);

UPDATE maintenance_plans SET
    interval_unit  = CASE interval_type WHEN 'daily' THEN 'day' WHEN 'weekly' THEN 'week' WHEN 'yearly' THEN 'year' ELSE 'month' END,
    interval_count = CASE interval_type WHEN 'quarterly' THEN 3 ELSE 1 END;

-- geplante Dauer: bisher teils in default_duration_min (Checklisten-Zuordnung)
UPDATE maintenance_plans SET estimated_min = default_duration_min WHERE COALESCE(default_duration_min, 0) > 0;

-- ── Checklisten je Plan mit Takt ───────────────────────────────
CREATE TABLE IF NOT EXISTS maintenance_plan_checklists (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id      UUID NOT NULL REFERENCES maintenance_plans(id) ON DELETE CASCADE,
    template_id  UUID NOT NULL REFERENCES maintenance_checklist_templates(id) ON DELETE CASCADE,
    sort_order   INTEGER NOT NULL DEFAULT 100,
    rhythm_unit  TEXT NOT NULL DEFAULT 'always' CHECK (rhythm_unit IN ('always', 'day', 'week', 'month', 'year')),
    rhythm_count INTEGER NOT NULL DEFAULT 1 CHECK (rhythm_count BETWEEN 1 AND 1000),
    last_done_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (plan_id, template_id)
);
CREATE INDEX IF NOT EXISTS idx_maintenance_plan_checklists_plan ON maintenance_plan_checklists (plan_id, sort_order);

-- Uebernahme: Zuordnungen (Mehrfach-Tabelle + alte Einzelspalte). Vorlagen,
-- deren Punkte unterschiedliche Intervalle haben, werden je Intervall in
-- eigene Vorlagen aufgeteilt (Punkte wandern mit gleicher id mit).
DO $$
DECLARE
    tpl RECORD;
    d   INTEGER;
    m   INTEGER;
    new_tpl UUID;
BEGIN
    -- alle Zuordnungen (Plan, Vorlage) einsammeln
    CREATE TEMP TABLE _plan_tpl ON COMMIT DROP AS
        SELECT DISTINCT plan_id, template_id FROM (
            SELECT plan_id, template_id FROM maintenance_plan_checklist_templates
            UNION
            SELECT id, checklist_template_id FROM maintenance_plans WHERE checklist_template_id IS NOT NULL
        ) x
        WHERE EXISTS (SELECT 1 FROM maintenance_checklist_templates t WHERE t.id = x.template_id);

    -- Zuordnung -> (Vorlage, Intervall der Punkte in Tagen)
    CREATE TEMP TABLE _tpl_split (plan_id UUID, template_id UUID, days INTEGER) ON COMMIT DROP;

    FOR tpl IN SELECT DISTINCT template_id FROM _plan_tpl LOOP
        SELECT MIN(interval_days) INTO m FROM maintenance_checklist_template_items
            WHERE template_id = tpl.template_id AND active;
        m := COALESCE(m, 1);
        INSERT INTO _tpl_split SELECT plan_id, tpl.template_id, m FROM _plan_tpl WHERE template_id = tpl.template_id;
        FOR d IN SELECT DISTINCT interval_days FROM maintenance_checklist_template_items
                 WHERE template_id = tpl.template_id AND active AND interval_days > m ORDER BY 1 LOOP
            INSERT INTO maintenance_checklist_templates (name, description, created_by)
                SELECT t.name || ' (alle ' || d || ' Tage)', t.description, t.created_by
                FROM maintenance_checklist_templates t WHERE t.id = tpl.template_id
                RETURNING id INTO new_tpl;
            UPDATE maintenance_checklist_template_items SET template_id = new_tpl
                WHERE template_id = tpl.template_id AND active AND interval_days = d;
            INSERT INTO _tpl_split SELECT plan_id, new_tpl, d FROM _plan_tpl WHERE template_id = tpl.template_id;
        END LOOP;
    END LOOP;

    -- Takt: Punkt-Intervall bis zum Plan-Intervall = „immer“, sonst alle N Tage
    INSERT INTO maintenance_plan_checklists (plan_id, template_id, sort_order, rhythm_unit, rhythm_count, last_done_at)
    SELECT s.plan_id, s.template_id,
           ROW_NUMBER() OVER (PARTITION BY s.plan_id ORDER BY s.days, s.template_id) * 10,
           CASE WHEN s.days <= p.interval_days THEN 'always' ELSE 'day' END,
           CASE WHEN s.days <= p.interval_days THEN 1 ELSE LEAST(s.days, 1000) END,
           (SELECT MAX(r.checked_at) FROM maintenance_task_checklist_results r
              JOIN maintenance_checklist_template_items i ON i.id = r.template_item_id
              JOIN maintenance_tasks mt ON mt.id = r.task_id
             WHERE i.template_id = s.template_id AND mt.plan_id = s.plan_id AND r.done)
    FROM _tpl_split s JOIN maintenance_plans p ON p.id = s.plan_id
    ON CONFLICT (plan_id, template_id) DO NOTHING;
END $$;

-- ── Schritte am Auftrag ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS maintenance_task_steps (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id           UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
    plan_checklist_id UUID REFERENCES maintenance_plan_checklists(id) ON DELETE SET NULL,
    template_id       UUID REFERENCES maintenance_checklist_templates(id) ON DELETE SET NULL,
    template_item_id  UUID REFERENCES maintenance_checklist_template_items(id) ON DELETE SET NULL,
    checklist_name    TEXT NOT NULL DEFAULT '',
    sort_order        INTEGER NOT NULL DEFAULT 0,
    label             TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    item_type         TEXT NOT NULL DEFAULT 'checkbox' CHECK (item_type IN ('checkbox', 'number', 'text')),
    required          BOOLEAN NOT NULL DEFAULT true,
    unit              TEXT NOT NULL DEFAULT '',
    target_value      NUMERIC,
    min_value         NUMERIC,
    max_value         NUMERIC,
    value             TEXT NOT NULL DEFAULT '',
    done              BOOLEAN NOT NULL DEFAULT false,
    in_range          BOOLEAN,
    checked_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    checked_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_maintenance_task_steps_task ON maintenance_task_steps (task_id, sort_order);
CREATE INDEX IF NOT EXISTS idx_maintenance_task_steps_item ON maintenance_task_steps (template_item_id);

-- Ergebnisse -> Schritte (gleiche id, Fotos bleiben verknuepft)
INSERT INTO maintenance_task_steps (id, task_id, plan_checklist_id, template_id, template_item_id, checklist_name, sort_order,
    label, description, item_type, required, unit, target_value, min_value, max_value,
    value, done, in_range, checked_by, checked_at, created_at, updated_at)
SELECT r.id, r.task_id, pc.id, i.template_id, i.id, t.name,
       ROW_NUMBER() OVER (PARTITION BY r.task_id ORDER BY COALESCE(pc.sort_order, 1000), t.name, i.sort_order, i.label),
       i.label, i.description,
       CASE WHEN i.item_type IN ('checkbox', 'number', 'text') THEN i.item_type ELSE 'checkbox' END,
       i.required, i.unit, i.target_value, i.min_value, i.max_value,
       r.value, r.done, r.in_range, r.checked_by, r.checked_at, r.created_at, r.updated_at
FROM maintenance_task_checklist_results r
JOIN maintenance_checklist_template_items i ON i.id = r.template_item_id
JOIN maintenance_checklist_templates t ON t.id = i.template_id
JOIN maintenance_tasks mt ON mt.id = r.task_id
LEFT JOIN maintenance_plan_checklists pc ON pc.plan_id = mt.plan_id AND pc.template_id = i.template_id
ON CONFLICT (id) DO NOTHING;

-- ── Aufraeumen ─────────────────────────────────────────────────
DROP TABLE IF EXISTS maintenance_task_checklist_results;
DROP TABLE IF EXISTS maintenance_plan_checklist_templates;
ALTER TABLE maintenance_plans DROP COLUMN IF EXISTS checklist_template_id;
ALTER TABLE maintenance_plans DROP COLUMN IF EXISTS default_duration_min;
ALTER TABLE maintenance_checklist_template_items DROP CONSTRAINT IF EXISTS chk_maintenance_template_item_interval_days_positive;
ALTER TABLE maintenance_checklist_template_items DROP COLUMN IF EXISTS interval_days;

-- Verlauf eines Wartungsauftrags einheitlich unter 'maintenance_task'
UPDATE record_history SET ref_type = 'maintenance_task' WHERE ref_type = 'maintenance';
