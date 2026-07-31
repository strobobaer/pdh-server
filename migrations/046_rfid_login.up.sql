-- 046_rfid_login.up.sql
--
-- Optionale RFID-Anmeldung. users.username existiert bereits (UNIQUE NOT
-- NULL seit der allerersten Migration) und wird ab jetzt zusaetzlich zur
-- E-Mail als Login-Kennung akzeptiert - das braucht keine Schemaaenderung,
-- nur eine Anpassung der Lookup-Logik im Code.
--
-- RFID ist ein neues, rein optionales Feld: die UID der Karte/des
-- Transponders. Die meisten Nutzer werden keine haben; wenn gesetzt, muss
-- sie eindeutig sein (ein Tag kann nur einem Konto zugeordnet sein).

ALTER TABLE users ADD COLUMN IF NOT EXISTS rfid_uid VARCHAR(100) UNIQUE;
CREATE INDEX IF NOT EXISTS idx_users_rfid_uid ON users(rfid_uid);
