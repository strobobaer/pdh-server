-- 214_terminal_vorgangsleiste.up.sql
--
-- Terminal „Vorgangsleiste“: Auswahl der Mitarbeitenden am Terminal aus mehreren Quellen und Wartungen in der Leiste.
-- Alles nur hinzufügend bzw. lockernd; ältere Server ignorieren die neue Tabelle und das neue Recht.
--
-- terminal_pick_sources
--   Zusätzliche Quellen der Personenauswahl je Terminal-Benutzer (Stammdaten, Abschnitt „Freier Terminal-Benutzer“):
--   kind = group (Mitglieder einer Gruppe), department (Personen einer Abteilung) oder user (eine einzelne Person);
--   ref_id verweist auf user_groups, departments bzw. users (ohne Fremdschlüssel: ein gelöschter Verweis fällt
--   beim Lesen still weg).
-- permissions: terminal.selectable
--   „Am Terminal auswählbar“: macht die Person nur in der Personenauswahl der Terminals sichtbar (abteilungsbezogen
--   wie andere Rechte: gilt an Terminals, deren Anlage zur Abteilung gehört). Gibt sonst nichts und wird keiner Rolle
--   vorbelegt.
-- terminal_vorgang_people
--   ref_type darf nun auch 'maintenance' (Wartungsauftrag) sein; die alte Einschränkung wird gelockert.

CREATE TABLE IF NOT EXISTS terminal_pick_sources (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    terminal_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('group', 'department', 'user')),
    ref_id           UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (terminal_user_id, kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_terminal_pick_sources_user ON terminal_pick_sources (terminal_user_id);

INSERT INTO permissions (key, label, category) VALUES
    ('terminal.selectable', 'Am Terminal auswählbar', 'Infrastruktur & Anlagen')
ON CONFLICT (key) DO NOTHING;

ALTER TABLE terminal_vorgang_people DROP CONSTRAINT IF EXISTS terminal_vorgang_people_ref_type_check;
ALTER TABLE terminal_vorgang_people ADD CONSTRAINT terminal_vorgang_people_ref_type_check
    CHECK (ref_type IN ('fault', 'ticket', 'task', 'maintenance'));
