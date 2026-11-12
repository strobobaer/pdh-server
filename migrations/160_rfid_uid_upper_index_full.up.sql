-- 160_rfid_uid_upper_index_full.up.sql
--
-- Der Teilindex aus 159 (WHERE rfid_uid <> '') passt nicht zur Abfrage in
-- users.GetByRFID: Der Planer kann aus upper(btrim(rfid_uid)) = upper(btrim($1))
-- nicht ableiten, dass rfid_uid nicht leer ist, und nutzt ihn daher nie.
-- Ersetzt durch einen Ausdrucksindex ohne Bedingung.
DROP INDEX IF EXISTS idx_users_rfid_uid_upper;
CREATE INDEX IF NOT EXISTS idx_users_rfid_uid_upper_all ON users (upper(btrim(rfid_uid)));
