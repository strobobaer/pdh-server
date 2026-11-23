-- 207_terminal_schichtplan.up.sql
--
-- Funktionen-Schalter „Schichtplan“ am Terminal (Seite /terminal/schichtplan: heute und die Woche je
-- Abteilung des Terminals). Wie die übrigen Schalter: Wert (Grundwert an) und Zeitpunkt der letzten
-- Änderung (NULL = nie geändert). Nur hinzufügend; ältere Stände und ältere Server bleiben unberührt.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS fn_schichtplan BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_schichtplan_at TIMESTAMPTZ;
