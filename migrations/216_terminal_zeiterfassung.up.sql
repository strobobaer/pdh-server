-- 216_terminal_zeiterfassung.up.sql
--
-- Zeiterfassung am Terminal (Seite /terminal/zeit, Funktionsschalter „zeiterfassung“) und die PDH-Seite
-- „Anwesenheit & Arbeitszeit“. Alles nur hinzufügend; ältere Server ignorieren Tabellen, Spalten und Rechte.
--
-- terminal_settings.fn_zeiterfassung
--   Schalter der Terminalseite, Grundwert AUS (auch für bestehende Terminals), mit Zeitpunkt der letzten Änderung.
-- attendance_events
--   Anwesenheitsbuchungen: kommen, gehen, pause_beginn, pause_ende. origin_via: terminal_karte (mit Karte),
--   terminal_gewaehlt (am Terminal gewählt, nicht selbst angemeldet) oder pdh (Korrektur/Nachtrag in PDH).
-- work_sessions
--   Arbeit an einem Vorgang (fault, ticket, task, maintenance) oder an einer Anlage (plant). Laufend = ended_at IS NULL;
--   je Person höchstens eine laufende. Beim Beenden an einem Vorgang entsteht ein Zeiteintrag (time_entry_id).
-- time_entries.origin_via
--   Herkunftsmarke des Zeiteintrags (leer = wie bisher).
-- attendance_history
--   Verlauf der Korrekturen in PDH (Ändern, Anlegen, Löschen) mit Begründung; nichts geschieht still.
-- permissions: attendance.view, attendance.edit (abteilungsbezogen nach der Abteilung der Person)

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS fn_zeiterfassung BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS fn_zeiterfassung_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS attendance_events (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('kommen', 'gehen', 'pause_beginn', 'pause_ende')),
    at               TIMESTAMPTZ NOT NULL,
    terminal_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    origin_via       TEXT NOT NULL DEFAULT '',
    created_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attendance_events_user_at ON attendance_events (user_id, at);

CREATE TABLE IF NOT EXISTS work_sessions (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ref_type          TEXT NOT NULL CHECK (ref_type IN ('fault', 'ticket', 'task', 'maintenance', 'plant')),
    ref_id            UUID NOT NULL,
    infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE SET NULL,
    title             TEXT NOT NULL DEFAULT '',
    terminal_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    origin_via        TEXT NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ,
    time_entry_id     UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_work_sessions_running ON work_sessions (user_id) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_work_sessions_user_start ON work_sessions (user_id, started_at);

ALTER TABLE time_entries ADD COLUMN IF NOT EXISTS origin_via TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS attendance_history (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject    TEXT NOT NULL CHECK (subject IN ('attendance', 'work')),
    action     TEXT NOT NULL CHECK (action IN ('add', 'edit', 'delete')),
    ref_id     UUID,
    old_text   TEXT NOT NULL DEFAULT '',
    new_text   TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL DEFAULT '',
    changed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attendance_history_user ON attendance_history (user_id, changed_at);

INSERT INTO permissions (key, label, category) VALUES
    ('attendance.view', 'Anwesenheit & Arbeitszeit ansehen', 'Personal'),
    ('attendance.edit', 'Anwesenheit & Arbeitszeit korrigieren', 'Personal')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key IN ('attendance.view', 'attendance.edit')
ON CONFLICT DO NOTHING;
