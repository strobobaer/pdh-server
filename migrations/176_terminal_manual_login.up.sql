-- 176_terminal_manual_login.up.sql
--
-- Anmeldung am Terminal ohne Kartenleser (K1).
-- terminal_settings (je System-Benutzer / Terminal):
--   manual_login     card_pin | password | card_only | none   (Standard card_pin)
--                    Art der manuellen Anmeldung am Terminal.
--   password_button  Schaltfläche „Mit Passwort anmelden“ anzeigen (Standard an).
-- users:
--   terminal_pin_hash          bcrypt-Hash der Terminal-PIN (NULL = keine PIN)
--   terminal_pin_failures      Fehlversuche seit der letzten richtigen Eingabe
--   terminal_pin_locked_until  Sperre nach zu vielen Fehlversuchen (NULL = nicht gesperrt)

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS manual_login TEXT NOT NULL DEFAULT 'card_pin' CHECK (manual_login IN ('card_pin','password','card_only','none')),
    ADD COLUMN IF NOT EXISTS password_button BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS terminal_pin_hash TEXT,
    ADD COLUMN IF NOT EXISTS terminal_pin_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS terminal_pin_locked_until TIMESTAMPTZ;
