-- 139_users_text_defaults.up.sql
-- users.department und users.phone waren seit 001 ohne Standardwert. Ein NULL
-- (z. B. Administrator aus dem Einrichtungsassistenten) ließ die Anmeldung mit
-- „benutzer nicht gefunden“ scheitern. Vorhandene NULL-Werte leeren, künftig ''
-- als Standard. Bewusst kein NOT NULL: ältere ZIP-Sicherungen mit NULL sollen
-- sich weiter einspielen lassen (die Abfragen lesen mit COALESCE).
UPDATE users SET department = '' WHERE department IS NULL;
UPDATE users SET phone = '' WHERE phone IS NULL;
ALTER TABLE users ALTER COLUMN department SET DEFAULT '';
ALTER TABLE users ALTER COLUMN phone SET DEFAULT '';
