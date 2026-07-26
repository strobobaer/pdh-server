-- 038_tasks_and_projects.up.sql
--
-- Neues Aufgaben- und Projektplanungs-Modul. Aufgaben koennen einem
-- Projekt zugeordnet sein (oder frei stehen), und optional aus einer
-- Stoerung oder einem Ticket entstanden sein (Synchronisation analog zu
-- Stoerung<->Ticket).

CREATE TABLE IF NOT EXISTS projects (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              VARCHAR(255) NOT NULL,
    description       TEXT,
    start_date        DATE,
    end_date          DATE,
    status            VARCHAR(20) NOT NULL DEFAULT 'planning',
    responsible_to    UUID REFERENCES users(id),
    infrastructure_id UUID REFERENCES infrastructure(id),
    cost_center_id    UUID REFERENCES cost_centers(id),
    created_by        UUID NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);

CREATE TABLE IF NOT EXISTS tasks (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title            VARCHAR(255) NOT NULL,
    description      TEXT,
    status           VARCHAR(20) NOT NULL DEFAULT 'open',
    priority         VARCHAR(20) NOT NULL DEFAULT 'medium',
    assigned_to      UUID REFERENCES users(id),
    responsible_to   UUID REFERENCES users(id),
    due_date         DATE,
    start_date       DATE,
    project_id       UUID REFERENCES projects(id) ON DELETE SET NULL,
    linked_fault_id  UUID REFERENCES faults(id) ON DELETE SET NULL,
    linked_ticket_id UUID REFERENCES tickets(id) ON DELETE SET NULL,
    resolution       TEXT,
    root_cause       TEXT,
    no_parts_needed  BOOLEAN NOT NULL DEFAULT false,
    resolved_at      TIMESTAMPTZ,
    archived_at      TIMESTAMPTZ,
    created_by       UUID NOT NULL REFERENCES users(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_linked_fault ON tasks(linked_fault_id);
CREATE INDEX IF NOT EXISTS idx_tasks_linked_ticket ON tasks(linked_ticket_id);

CREATE TABLE IF NOT EXISTS task_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_task_actions_task ON task_actions(task_id);

CREATE TABLE IF NOT EXISTS task_pending_parts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    part_id         UUID NOT NULL REFERENCES spare_parts(id),
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    qty             NUMERIC NOT NULL,
    created_by      UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_task_pending_parts_task ON task_pending_parts(task_id);

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_stock_movements_task ON stock_movements(task_id);

-- Rueckverknuepfung: Stoerung/Ticket koennen eine direkt erzeugte Aufgabe referenzieren
ALTER TABLE faults ADD COLUMN IF NOT EXISTS linked_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS linked_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_faults_linked_task ON faults(linked_task_id);
CREATE INDEX IF NOT EXISTS idx_tickets_linked_task ON tickets(linked_task_id);


