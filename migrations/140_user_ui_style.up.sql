-- 140_user_ui_style.up.sql
-- Design-Stil je Benutzer (fluent = Windows 11, fiori = SAP Fiori).
-- Leer = Firmenstandard aus Server-Einstellungen → Erscheinungsbild.
ALTER TABLE users ADD COLUMN IF NOT EXISTS ui_style TEXT NOT NULL DEFAULT '';
