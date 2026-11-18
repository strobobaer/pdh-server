-- 173_rfid_uid_upper_index.up.sql
--
-- Kartensuche (users.GetByRFID) vergleicht die UID ohne Groß-/Kleinschreibung
-- und Leerraum: upper(btrim(rfid_uid)). Passender Ausdrucksindex, damit die
-- Suche nicht sequenziell über users läuft.
CREATE INDEX IF NOT EXISTS idx_users_rfid_uid_upper ON users (upper(btrim(rfid_uid)))
    WHERE rfid_uid IS NOT NULL AND rfid_uid <> '';
