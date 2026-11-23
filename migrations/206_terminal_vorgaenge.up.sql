-- 206_terminal_vorgaenge.up.sql
--
-- Terminal „Vorgänge“ (Störungen, Tickets und Aufgaben am Terminal starten, warten lassen und beenden).
-- Alles nur hinzufügend; vorhandene Werte und ältere Stände bleiben unberührt, ältere Server ignorieren
-- die neuen Spalten und Tabellen.
--
-- terminal_settings
--   abteilungen      Abteilungen des Terminals (Auswahl der Mitarbeitenden am Terminal; auch für weitere
--                    Terminalfunktionen). NULL = Vorgabe: die Abteilung(en) der Anlage des Terminals.
--   vorgaenge_modus  ohne_karte = Eingewöhnung: ohne Karte darf jede Person am Terminal starten, warten lassen
--                    und beenden (Auswahl der Mitarbeitenden); nur_karte = ohne Karte nur Ansicht.
--   fn_vorgaenge     Funktionen-Schalter „Vorgänge“ (Grundwert an) mit Zeitpunkt der letzten Änderung.
-- terminal_vorgang_people
--   Mitarbeitende, die am Terminal für einen Vorgang gewählt wurden (Störung, Ticket, Aufgabe).
-- record_history
--   origin_via  Herkunft des Schritts: '' (nicht am Terminal), terminal_gewaehlt („am Terminal gewählt“,
--               ohne Karte) oder terminal_karte („mit Karte“); place = Name des Terminals;
--               people_ids = die gewählten Personen. Für Auswertungen filterbar.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS abteilungen UUID[],
    ADD COLUMN IF NOT EXISTS vorgaenge_modus TEXT NOT NULL DEFAULT 'ohne_karte' CHECK (vorgaenge_modus IN ('ohne_karte', 'nur_karte')),
    ADD COLUMN IF NOT EXISTS fn_vorgaenge BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_vorgaenge_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS terminal_vorgang_people (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    ref_type         TEXT NOT NULL CHECK (ref_type IN ('fault', 'ticket', 'task')),
    ref_id           UUID NOT NULL,
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    terminal_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    origin_via       TEXT NOT NULL DEFAULT 'terminal_gewaehlt' CHECK (origin_via IN ('terminal_gewaehlt', 'terminal_karte')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (ref_type, ref_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_terminal_vorgang_people_user ON terminal_vorgang_people (user_id);

ALTER TABLE record_history
    ADD COLUMN IF NOT EXISTS origin_via TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS place TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS people_ids UUID[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_record_history_origin_via ON record_history (origin_via) WHERE origin_via <> '';
