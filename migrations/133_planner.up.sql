-- 133_planner.up.sql
-- Planer (Urlaub, Schichten, Wartung) – ersetzt die Seite „Schichtplan“:
--   * Rechte: shifts.view/shifts.edit werden jetzt wirklich geprüft,
--     neu absences.request / absences.approve / absences.view_all
--   * Abwesenheiten: halbe Tage, Vertretung, Entscheidung mit Begründung
--   * Urlaubsanspruch je Person und Jahr (mit Übertrag)
--   * Betriebsruhe / eigene freie Tage (gesetzliche Feiertage rechnet PDH selbst)
--   * Schichten: Mindestbesetzung, Pause; Rotationsmuster mit Mannschaften
--   * shift_assignments.note nie NULL (generierte Pläne brachen die Wochenansicht)

INSERT INTO permissions (key, label, category) VALUES
    ('absences.request',  'Eigene Abwesenheit (Urlaub) beantragen',                      'Schichtplanung'),
    ('absences.approve',  'Abwesenheiten genehmigen, für andere erfassen, Urlaubsanspruch pflegen', 'Schichtplanung'),
    ('absences.view_all', 'Abwesenheitsgrund aller sehen (sonst nur „abwesend“)',        'Schichtplanung')
ON CONFLICT (key) DO NOTHING;

UPDATE permissions SET label = 'Planer ansehen (Schichtplan, Abwesenheiten, Wartungskalender)' WHERE key = 'shifts.view';
UPDATE permissions SET label = 'Schichten planen (zuweisen, Modelle, Vorlagen, Pläne veröffentlichen)' WHERE key = 'shifts.edit';

-- Bisher sahen alle den Schichtplan, und alle konnten Abwesenheiten anlegen.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key IN ('shifts.view', 'absences.request')
ON CONFLICT DO NOTHING;

-- Planer waren bisher fest Admin und Manager
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key IN ('shifts.edit', 'absences.approve', 'absences.view_all')
ON CONFLICT DO NOTHING;

-- ── Abwesenheiten ─────────────────────────────────────────────
ALTER TABLE absences ALTER COLUMN days TYPE NUMERIC(5,1) USING days::numeric;
ALTER TABLE absences ADD COLUMN IF NOT EXISTS half_start     BOOLEAN NOT NULL DEFAULT false; -- erster Tag nur halb
ALTER TABLE absences ADD COLUMN IF NOT EXISTS half_end       BOOLEAN NOT NULL DEFAULT false; -- letzter Tag nur halb
ALTER TABLE absences ADD COLUMN IF NOT EXISTS substitute_id  UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE absences ADD COLUMN IF NOT EXISTS created_by     UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE absences ADD COLUMN IF NOT EXISTS decided_at     TIMESTAMPTZ;
ALTER TABLE absences ADD COLUMN IF NOT EXISTS decision_note  TEXT NOT NULL DEFAULT '';
UPDATE absences SET note = '' WHERE note IS NULL;
CREATE INDEX IF NOT EXISTS idx_absences_range ON absences (start_date, end_date);

CREATE TABLE IF NOT EXISTS vacation_entitlements (
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    year      INT  NOT NULL CHECK (year BETWEEN 2000 AND 2100),
    days      NUMERIC(5,1) NOT NULL DEFAULT 30,
    carryover NUMERIC(5,1) NOT NULL DEFAULT 0, -- Resturlaub aus dem Vorjahr
    PRIMARY KEY (user_id, year)
);

-- Betriebsruhe, Brückentage o. Ä. (gesetzliche Feiertage berechnet PDH)
CREATE TABLE IF NOT EXISTS planner_closures (
    day        DATE PRIMARY KEY,
    name       VARCHAR(120) NOT NULL,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO app_settings (key, value) VALUES
    ('planner_holiday_state', ''),       -- Bundesland für Feiertage (BW, BY, …), leer = nur bundesweite
    ('planner_vacation_default', '30')   -- Urlaubstage pro Jahr, wenn nichts hinterlegt ist
ON CONFLICT (key) DO NOTHING;

-- ── Schichten ─────────────────────────────────────────────────
UPDATE shift_assignments SET note = '' WHERE note IS NULL;
ALTER TABLE shift_assignments ALTER COLUMN note SET DEFAULT '';
ALTER TABLE shift_assignments ALTER COLUMN note SET NOT NULL;

ALTER TABLE shift_definitions ADD COLUMN IF NOT EXISTS min_staff     INT NOT NULL DEFAULT 0 CHECK (min_staff >= 0);
ALTER TABLE shift_definitions ADD COLUMN IF NOT EXISTS break_minutes INT NOT NULL DEFAULT 30 CHECK (break_minutes >= 0);
ALTER TABLE shift_definitions ADD COLUMN IF NOT EXISTS sort_order    INT NOT NULL DEFAULT 0;
ALTER TABLE shift_definitions ADD COLUMN IF NOT EXISTS active        BOOLEAN NOT NULL DEFAULT true;

-- Mannschaften: Mitarbeiter einer Mannschaft laufen gleich, jede weitere
-- Mannschaft um crew_step Tage versetzt (z. B. 4-Schicht-Konti: 4 × 2 Tage)
ALTER TABLE shift_rotation_patterns ADD COLUMN IF NOT EXISTS crews     INT NOT NULL DEFAULT 1 CHECK (crews BETWEEN 1 AND 12);
ALTER TABLE shift_rotation_patterns ADD COLUMN IF NOT EXISTS crew_step INT NOT NULL DEFAULT 0 CHECK (crew_step >= 0);
ALTER TABLE shift_rotation_patterns ADD COLUMN IF NOT EXISTS template_key VARCHAR(40) NOT NULL DEFAULT '';

ALTER TABLE shift_models ADD COLUMN IF NOT EXISTS template_key VARCHAR(40) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_shift_assignments_date ON shift_assignments (date);
