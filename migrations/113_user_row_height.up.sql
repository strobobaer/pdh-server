-- 113_user_row_height.up.sql
-- Zeilenhoehe fuer Tabellen und Listen je Benutzer in % (0 = 100 %).
-- Wirkt per CSS-zoom auf Tabellen und Listenzeilen, die Schrift waechst mit
-- (internal/web/appearance.go, Benutzermenue → Darstellung).

ALTER TABLE users ADD COLUMN IF NOT EXISTS ui_row INTEGER NOT NULL DEFAULT 0;
