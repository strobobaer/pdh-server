-- 185_terminal_card_notify.up.sql
--
-- Benachrichtigung am Windows-Terminal beim Kartenlesen (W1, Betriebsart Zusatz).
-- terminal_settings.card_notify (je System-Benutzer / Terminal): Liste der Arten
--   toast    Windows-Mitteilung
--   overlay  Fenster in der Mitte
--   banner   Leiste oben
--   front    Terminal nach vorn
--   off      keine Benachrichtigung (nur allein)
-- Leere Liste = Vorgabe, am Terminal wirkt dann {overlay}. Ältere PDH-Stände und
-- Terminals kennen die Spalte nicht und bleiben unberührt.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS card_notify TEXT[] NOT NULL DEFAULT '{}';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'terminal_settings_card_notify_check') THEN
        ALTER TABLE terminal_settings ADD CONSTRAINT terminal_settings_card_notify_check CHECK (
            card_notify <@ ARRAY['toast','overlay','banner','front','off']::text[]
            AND (NOT ('off' = ANY(card_notify)) OR cardinality(card_notify) = 1)
        );
    END IF;
END $$;
