-- 053_role_hierarchy.up.sql
--
-- Rang fuer Rollen: hoehere Zahl = hoehere Rangstufe. Damit koennen
-- Benutzer- und Rollenverwaltung durchsetzen, dass eine Rolle nur
-- Benutzer und Rollen unterhalb der eigenen Rangstufe verwalten darf
-- (Ausnahme: die ranghoechste Rolle darf alles verwalten, auch
-- gleichrangige Rollen).

ALTER TABLE roles ADD COLUMN IF NOT EXISTS level INTEGER NOT NULL DEFAULT 0;

UPDATE roles SET level = 100 WHERE key = 'admin';
UPDATE roles SET level = 70  WHERE key = 'manager';
UPDATE roles SET level = 50  WHERE key = 'technician';
UPDATE roles SET level = 30  WHERE key = 'worker';
UPDATE roles SET level = 10  WHERE key = 'viewer';

CREATE INDEX IF NOT EXISTS idx_roles_level ON roles(level DESC);
