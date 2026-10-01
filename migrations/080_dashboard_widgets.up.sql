-- 080_dashboard_widgets.up.sql
-- Persoenliches Dashboard: Liste der Widgets je Benutzer (Reihenfolge,
-- Groesse, Einstellungen) als JSON. NULL = Standard-Zusammenstellung.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS dashboard_widgets JSONB;
