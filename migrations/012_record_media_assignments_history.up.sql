-- 012_record_media_assignments_history.up.sql
-- Datensatzbild, Verantwortliche/Zuweisung, Archiv und Historie

ALTER TABLE tickets
  ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS record_image_attachment_id UUID REFERENCES attachments(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS archived_by UUID REFERENCES users(id);

ALTER TABLE faults
  ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS record_image_attachment_id UUID REFERENCES attachments(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS archived_by UUID REFERENCES users(id);

ALTER TABLE maintenance_tasks
  ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS record_image_attachment_id UUID REFERENCES attachments(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS archived_by UUID REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE TABLE IF NOT EXISTS record_history (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type    VARCHAR(50) NOT NULL,
    ref_id      UUID NOT NULL,
    action      VARCHAR(60) NOT NULL,
    field_name  VARCHAR(100),
    old_value   TEXT,
    new_value   TEXT,
    message     TEXT,
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_record_history_ref ON record_history(ref_type, ref_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tickets_archive ON tickets(archived_at) WHERE archived_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_faults_archive ON faults(archived_at) WHERE archived_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_archive ON maintenance_tasks(archived_at) WHERE archived_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tickets_responsible ON tickets(responsible_to);
CREATE INDEX IF NOT EXISTS idx_faults_responsible ON faults(responsible_to);
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_responsible ON maintenance_tasks(responsible_to);

UPDATE tickets
SET archived_at = COALESCE(resolved_at, updated_at, NOW())
WHERE status IN ('resolved','closed') AND archived_at IS NULL;

UPDATE faults
SET archived_at = COALESCE(resolved_at, updated_at, NOW())
WHERE status IN ('resolved','closed') AND archived_at IS NULL;
