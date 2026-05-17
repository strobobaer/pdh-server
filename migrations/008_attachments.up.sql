-- 008_attachments.up.sql

-- ============================================================
-- DATEIANHÄNGE (universal für alle Module)
-- ============================================================
CREATE TABLE IF NOT EXISTS attachments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type    VARCHAR(50) NOT NULL,  -- 'fault','ticket','inventory','maintenance_task'
    ref_id      UUID NOT NULL,
    filename    VARCHAR(255) NOT NULL,
    filepath    VARCHAR(500) NOT NULL,
    mimetype    VARCHAR(100) NOT NULL DEFAULT 'image/jpeg',
    size_bytes  INT NOT NULL DEFAULT 0,
    caption     VARCHAR(500),
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_attach_ref ON attachments(ref_type, ref_id);

-- ============================================================
-- WARTUNGS-CHECKLISTEN-VORLAGEN
-- ============================================================
CREATE TABLE IF NOT EXISTS maintenance_checklists (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255) NOT NULL,
    description TEXT,
    category    VARCHAR(100),
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Einzelne Felder einer Vorlage
CREATE TABLE IF NOT EXISTS checklist_items (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    checklist_id UUID NOT NULL REFERENCES maintenance_checklists(id) ON DELETE CASCADE,
    sort_order  INT NOT NULL DEFAULT 0,
    item_type   VARCHAR(20) NOT NULL DEFAULT 'checkbox',
    -- Types: checkbox, text, number, image, select, compare
    label       VARCHAR(255) NOT NULL,
    description TEXT,
    required    BOOLEAN NOT NULL DEFAULT false,
    -- Für compare: Sollwert + Toleranz
    compare_value    NUMERIC,
    compare_unit     VARCHAR(50),
    compare_tolerance NUMERIC,
    -- Für select: Optionen als JSON
    options     JSONB,
    -- Für number: Min/Max
    min_value   NUMERIC,
    max_value   NUMERIC
);

CREATE INDEX IF NOT EXISTS idx_checklist_items ON checklist_items(checklist_id, sort_order);

-- Verknüpfung Plan → Vorlage
ALTER TABLE maintenance_plans
    ADD COLUMN IF NOT EXISTS checklist_id UUID REFERENCES maintenance_checklists(id);

-- ============================================================
-- AUSGEFÜLLTE PROTOKOLLE (bei Durchführung)
-- ============================================================
CREATE TABLE IF NOT EXISTS task_checklist_logs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id      UUID NOT NULL REFERENCES maintenance_tasks(id) ON DELETE CASCADE,
    item_id      UUID NOT NULL REFERENCES checklist_items(id),
    -- Wert je nach Typ
    checked      BOOLEAN,                    -- checkbox
    text_value   TEXT,                       -- text
    number_value NUMERIC,                    -- number, compare
    selected     VARCHAR(255),               -- select
    -- Bild als Attachment (ref_type='checklist_log', ref_id=id)
    ok           BOOLEAN,                    -- Vergleichswert OK?
    note         TEXT,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(task_id, item_id)
);
