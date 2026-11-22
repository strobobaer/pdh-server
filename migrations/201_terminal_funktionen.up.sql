-- 201_terminal_funktionen.up.sql
--
-- Funktionen-Schalter je Terminal-Benutzer (Abgleich mit dem PDH-Terminal, je Schalter gewinnt die
-- jüngere Änderung): Störung, Ticket, Rüsten, Stillstand, HMI, Wartungen und die Folienschau als
-- Bildschirmschoner. Je Schalter ein Wert (fn_<Schlüssel>, Grundwert an) und der Zeitpunkt der
-- letzten Änderung (fn_<Schlüssel>_at; NULL = nie geändert, gilt als 1970-01-01). Die Folienschau
-- behält ihre Spalte saver_slides (Migration 195) und bekommt nur den Zeitpunkt dazu; ältere
-- Terminals lesen weiter das Feld saver_slides. theme_scheme wählt hell/dunkel der Terminalseiten.
-- Nur hinzufügend: vorhandene Werte und ältere Stände bleiben unberührt.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS fn_stoerung BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_stoerung_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fn_ticket BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_ticket_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fn_ruesten BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_ruesten_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fn_stillstand BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_stillstand_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fn_hmi BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_hmi_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fn_wartungen BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS fn_wartungen_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS saver_slides_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS theme_scheme TEXT NOT NULL DEFAULT 'dark' CHECK (theme_scheme IN ('dark', 'light'));

-- Bereits eingeschaltete Folienschau behält ihren Wert und bekommt den Zeitpunkt der letzten
-- Einstellungsänderung (ein späterer Abgleich vom Terminal überschreibt sie nur, wenn er jünger ist).
UPDATE terminal_settings SET saver_slides_at = updated_at WHERE saver_slides AND saver_slides_at IS NULL;

-- Version, mit der sich das gekoppelte Gerät zuletzt gemeldet hat (freiwillige Angabe des Terminals).
ALTER TABLE terminal_devices ADD COLUMN IF NOT EXISTS client_version TEXT NOT NULL DEFAULT '';
