-- 217_attendance_followup.up.sql
--
-- Nachtrag zu 216 (nur hinzufügend bzw. lockernd):
-- work_sessions.forgotten
--   Eine vergessene laufende Arbeit (länger als 18 Stunden offen) wird am Terminal ohne Zeiteintrag geschlossen und
--   so gekennzeichnet; in PDH wird sie zur Prüfung angezeigt und kann korrigiert werden.
-- attendance_history
--   user_id bleibt beim Löschen der Person als Verlauf erhalten (ON DELETE SET NULL); person_name hält den Namen
--   zum Zeitpunkt der Korrektur fest (bestehende Zeilen werden nachgetragen).

ALTER TABLE work_sessions ADD COLUMN IF NOT EXISTS forgotten BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE attendance_history ADD COLUMN IF NOT EXISTS person_name TEXT NOT NULL DEFAULT '';
UPDATE attendance_history a SET person_name = TRIM(u.first_name || ' ' || u.last_name) FROM users u WHERE u.id = a.user_id AND a.person_name = '';
ALTER TABLE attendance_history ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE attendance_history DROP CONSTRAINT IF EXISTS attendance_history_user_id_fkey;
ALTER TABLE attendance_history ADD CONSTRAINT attendance_history_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;
