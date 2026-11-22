-- 195_terminal_saver_slides.up.sql
--
-- Folienschau als Bildschirmschoner: Ein normales (nicht globales) Terminal kann statt des
-- Platzhalter-Schoners die Folien des Infobildschirms spielen. Berührung außerhalb eines Vorgangs
-- und jede Karte wecken es wie bisher den Schoner. Bestehende Terminals behalten den Wert false;
-- ältere PDH-Stände und Terminals kennen die Spalte nicht und bleiben unberührt.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS saver_slides BOOLEAN NOT NULL DEFAULT false;
