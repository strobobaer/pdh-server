-- 124_user_mail_notifications.up.sql
-- Statusmeldungen von PDH-System zusätzlich per E-Mail (Benutzerstamm →
-- Übersicht → E-Mail; internal/web/user_mail.go). Standard: aus.

ALTER TABLE users ADD COLUMN IF NOT EXISTS mail_notifications BOOLEAN NOT NULL DEFAULT false;
