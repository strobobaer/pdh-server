-- 055_user_manager_hierarchy.up.sql
--
-- Vorgesetzten-Beziehung pro Benutzer (Berichtslinie), zusaetzlich zur
-- Rollen-Rangordnung: jeder Benutzer darf unabhaengig von seiner Rolle
-- seine direkten und indirekten Unteruser bearbeiten und deren Zeiten
-- auswerten. manager_id verweist auf den direkten Vorgesetzten; die
-- vollstaendige Unterbaum-Ermittlung erfolgt per rekursiver Abfrage
-- (siehe users.Repository.IsSubordinate/SubordinateIDs).

ALTER TABLE users ADD COLUMN IF NOT EXISTS manager_id UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_users_manager_id ON users(manager_id);
