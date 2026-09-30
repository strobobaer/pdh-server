-- 072_backup_cleanup_notify_categories.up.sql
--
-- 1. PDH-System-Benutzer (Absender automatischer Chat-Hinweise, keine Anmeldung)
-- 2. Kategorien (#Energie, #Optimierung, ...) fuer alle Vorgaenge & Stammdaten
-- 3. Aenderungs-Warteschlange (Trigger) fuer Chat-Hinweise an Beteiligte
-- 4. Stammdaten-Status: sperren / deaktivieren / zum Loeschen vormerken
-- 5. Datensicherung: Zeitplaene und Sicherungslaeufe

-- ── 1. PDH-System ───────────────────────────────────────────
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_bot BOOLEAN NOT NULL DEFAULT false;

-- Inaktiv + ungueltiger Passwort-Hash: kann sich nie anmelden, taucht in
-- Auswahllisten (nur aktive Benutzer) nicht auf.
INSERT INTO users (id, username, email, password_hash, first_name, last_name, role, active, is_bot)
VALUES ('00000000-0000-4000-8000-00000000d0d1', 'pdh-system', 'pdh-system@localhost', '!', 'PDH', 'System', 'worker', false, true)
ON CONFLICT DO NOTHING;

-- ── 2. Kategorien ───────────────────────────────────────────
CREATE TABLE IF NOT EXISTS categories (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       VARCHAR(60) NOT NULL,
    color      VARCHAR(20) NOT NULL DEFAULT '#6366f1',
    icon       VARCHAR(40) NOT NULL DEFAULT 'ti-hash',
    sort_order INT NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_name ON categories (lower(name));

INSERT INTO categories (name, color, icon, sort_order) VALUES
    ('Energie',        '#f59e0b', 'ti-bolt',          10),
    ('Optimierung',    '#10b981', 'ti-trending-up',   20),
    ('Zufriedenheit',  '#ec4899', 'ti-mood-smile',    30),
    ('Versuch',        '#8b5cf6', 'ti-flask',         40),
    ('Produktiv',      '#3b82f6', 'ti-player-play',   50),
    ('Efficio',        '#0ea5e9', 'ti-rocket',        60)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS record_categories (
    module      VARCHAR(40) NOT NULL,
    record_id   UUID NOT NULL,
    category_id UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (module, record_id, category_id)
);
CREATE INDEX IF NOT EXISTS idx_record_categories_cat ON record_categories (category_id, module);

-- ── 3. Aenderungs-Warteschlange ─────────────────────────────
CREATE TABLE IF NOT EXISTS record_change_queue (
    id         BIGSERIAL PRIMARY KEY,
    module     VARCHAR(40) NOT NULL,
    record_id  UUID NOT NULL,
    kind       VARCHAR(20) NOT NULL DEFAULT 'update', -- update | comment | assignees
    fields     TEXT[],
    old_values JSONB,
    new_values JSONB,
    note       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION pdh_queue_record_update() RETURNS trigger AS $$
DECLARE
    cols TEXT[];
    o JSONB;
    n JSONB;
BEGIN
    SELECT array_agg(nv.key ORDER BY nv.key),
           jsonb_object_agg(nv.key, ov.value),
           jsonb_object_agg(nv.key, nv.value)
      INTO cols, o, n
      FROM jsonb_each(to_jsonb(NEW)) nv
      JOIN jsonb_each(to_jsonb(OLD)) ov ON ov.key = nv.key
     WHERE nv.value IS DISTINCT FROM ov.value
       AND nv.key NOT IN ('updated_at', 'search_vector', 'embedding', 'record_image_attachment_id')
       AND nv.key NOT LIKE 'ai\_%'
       AND nv.key NOT LIKE 'analysis%';
    IF cols IS NULL THEN
        RETURN NEW;
    END IF;
    INSERT INTO record_change_queue (module, record_id, kind, fields, old_values, new_values)
    VALUES (TG_ARGV[0], NEW.id, 'update', cols, o, n);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_change_tickets ON tickets;
CREATE TRIGGER trg_pdh_change_tickets AFTER UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_update('ticket');
DROP TRIGGER IF EXISTS trg_pdh_change_faults ON faults;
CREATE TRIGGER trg_pdh_change_faults AFTER UPDATE ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_update('fault');
DROP TRIGGER IF EXISTS trg_pdh_change_tasks ON tasks;
CREATE TRIGGER trg_pdh_change_tasks AFTER UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_update('task');
DROP TRIGGER IF EXISTS trg_pdh_change_projects ON projects;
CREATE TRIGGER trg_pdh_change_projects AFTER UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_update('project');
DROP TRIGGER IF EXISTS trg_pdh_change_maintenance ON maintenance_tasks;
CREATE TRIGGER trg_pdh_change_maintenance AFTER UPDATE ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_update('maintenance_task');

CREATE OR REPLACE FUNCTION pdh_queue_ticket_comment() RETURNS trigger AS $$
BEGIN
    INSERT INTO record_change_queue (module, record_id, kind, note)
    VALUES ('ticket', NEW.ticket_id, 'comment', left(NEW.text, 200));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_pdh_change_ticket_comments ON ticket_comments;
CREATE TRIGGER trg_pdh_change_ticket_comments AFTER INSERT ON ticket_comments
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_ticket_comment();

CREATE OR REPLACE FUNCTION pdh_queue_record_comment() RETURNS trigger AS $$
BEGIN
    IF NEW.ref_type IN ('ticket', 'fault', 'task', 'project', 'maintenance_task') AND NEW.ref_id IS NOT NULL THEN
        INSERT INTO record_change_queue (module, record_id, kind, note)
        VALUES (NEW.ref_type, NEW.ref_id, 'comment', left(NEW.text, 200));
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_pdh_change_infra_comments ON infrastructure_comments;
CREATE TRIGGER trg_pdh_change_infra_comments AFTER INSERT ON infrastructure_comments
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_comment();

CREATE OR REPLACE FUNCTION pdh_queue_task_assignees() RETURNS trigger AS $$
BEGIN
    INSERT INTO record_change_queue (module, record_id, kind)
    VALUES ('task', COALESCE(NEW.task_id, OLD.task_id), 'assignees');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_pdh_change_task_assignees ON task_assignees;
CREATE TRIGGER trg_pdh_change_task_assignees AFTER INSERT OR DELETE ON task_assignees
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_task_assignees();

-- Benutzer koennen Aenderungshinweise abschalten (Mein Konto)
ALTER TABLE users ADD COLUMN IF NOT EXISTS change_notifications BOOLEAN NOT NULL DEFAULT true;

-- ── 4. Stammdaten-Status ────────────────────────────────────
ALTER TABLE spare_parts       ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS locked_by UUID REFERENCES users(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS lock_reason TEXT, ADD COLUMN IF NOT EXISTS hidden_at TIMESTAMPTZ;
ALTER TABLE business_partners ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS locked_by UUID REFERENCES users(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS lock_reason TEXT, ADD COLUMN IF NOT EXISTS hidden_at TIMESTAMPTZ;
ALTER TABLE infrastructure    ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS locked_by UUID REFERENCES users(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS lock_reason TEXT, ADD COLUMN IF NOT EXISTS hidden_at TIMESTAMPTZ;
ALTER TABLE storage_nodes     ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS locked_by UUID REFERENCES users(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS lock_reason TEXT, ADD COLUMN IF NOT EXISTS hidden_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS deletion_requests (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    module       VARCHAR(40) NOT NULL,
    record_id    UUID NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    was_active   BOOLEAN NOT NULL DEFAULT true,
    status       VARCHAR(20) NOT NULL DEFAULT 'pending', -- pending | deleted | hidden | cancelled
    result_note  TEXT,
    requested_by UUID REFERENCES users(id) ON DELETE SET NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    run_id       UUID
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_deletion_requests_pending ON deletion_requests (module, record_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_deletion_requests_status ON deletion_requests (status, requested_at DESC);

CREATE TABLE IF NOT EXISTS cleanup_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    started_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    deleted     INT NOT NULL DEFAULT 0,
    hidden      INT NOT NULL DEFAULT 0,
    failed      INT NOT NULL DEFAULT 0,
    summary     TEXT
);

-- ── 5. Datensicherung ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS backup_schedules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(100) NOT NULL,
    frequency    VARCHAR(10) NOT NULL DEFAULT 'daily', -- daily | weekly | monthly
    time_of_day  VARCHAR(5) NOT NULL DEFAULT '02:00',
    weekday      INT NOT NULL DEFAULT 1,  -- 0 = Sonntag
    monthday     INT NOT NULL DEFAULT 1,
    components   TEXT[] NOT NULL DEFAULT ARRAY['database'],
    keep_count   INT NOT NULL DEFAULT 7,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    last_run_at  TIMESTAMPTZ,
    last_status  VARCHAR(20),
    last_message TEXT,
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS backup_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    file_name   TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    components  TEXT[] NOT NULL DEFAULT '{}',
    kind        VARCHAR(20) NOT NULL DEFAULT 'manual', -- manual | scheduled | safety | uploaded
    status      VARCHAR(20) NOT NULL DEFAULT 'running', -- running | ok | failed
    message     TEXT,
    schedule_id UUID REFERENCES backup_schedules(id) ON DELETE SET NULL,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_backup_runs_started ON backup_runs (started_at DESC);

-- ── Berechtigungen ──────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('system.backup',      'Datensicherung & Wiederherstellung', 'System'),
    ('system.cleanup',     'Löschvormerkungen prüfen & Bereinigungslauf starten', 'System'),
    ('categories.manage',  'Kategorien (#) verwalten', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key IN ('system.backup', 'system.cleanup', 'categories.manage')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'manager' AND p.key = 'categories.manage'
ON CONFLICT DO NOTHING;
