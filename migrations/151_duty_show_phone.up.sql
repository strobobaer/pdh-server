-- 151_duty_show_phone.up.sql
-- Sonderdienste: auf Wunsch die Telefonnummer der eingeteilten Person
-- anzeigen (Schichtplan, Kalender, Schichtplan-Widget, Leitstand) – z. B. bei
-- Rufbereitschaft. Nummer: dienstliches Handy, sonst Durchwahl, sonst Telefon.
ALTER TABLE duty_types ADD COLUMN IF NOT EXISTS show_phone BOOLEAN NOT NULL DEFAULT false;
UPDATE duty_types SET show_phone = true WHERE kind = 'standby' AND NOT show_phone;
