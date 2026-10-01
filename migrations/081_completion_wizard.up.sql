-- 081_completion_wizard.up.sql
-- Abschluss-Assistent: Zeiteintraege, die beim Abschluss eines Vorgangs fuer
-- mitarbeitende Kolleginnen und Kollegen angelegt werden, sind zunaechst
-- unbestaetigt (gelb) und zaehlen erst nach Bestaetigung.

ALTER TABLE time_entries
    ADD COLUMN IF NOT EXISTS pending BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS pending_from UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_time_entries_pending ON time_entries(user_id) WHERE pending;

-- Aufgaben koennen Zeiten haben (bei manchen Installationen fehlte der Wert)
ALTER TYPE time_ref_type ADD VALUE IF NOT EXISTS 'task';

-- Abteilungen, deren Mitarbeitende beim Abschluss zur Auswahl stehen
INSERT INTO app_settings (key, value) VALUES ('completion.departments', 'Instandhaltung, Elektro, Mechanik')
ON CONFLICT (key) DO NOTHING;
