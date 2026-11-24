-- 212_terminal_free_user.up.sql
-- "Freier Terminal-Benutzer": Flag am Systembenutzer des Terminals. Gesetzt, darf das Terminal (nur in seiner
-- Gerätesitzung, nur für seinen Anlagenbaum, nur die Terminalfunktionen) ohne die einzelnen Rollenrechte
-- Störungen, Tickets, Aufgaben, Rüsten, Stillstand ansehen/bearbeiten und HMIs ansehen. Nur hinzufügend.
ALTER TABLE users ADD COLUMN IF NOT EXISTS terminal_free BOOLEAN NOT NULL DEFAULT false;
