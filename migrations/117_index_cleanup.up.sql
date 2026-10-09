-- 117_index_cleanup.up.sql
-- Ergebnis der Pruefung auf Dopplungen und Bremskloetze (0.48.0).

-- Push-Dienst (push.go): neue Stoerungen/Tickets der letzten Minuten, alle 5 s
CREATE INDEX IF NOT EXISTS idx_faults_created  ON faults(created_at);
CREATE INDEX IF NOT EXISTS idx_tickets_created ON tickets(created_at);

-- „Meine Störungen“, Push-Abschluss: wie bei tickets (idx_tickets_assigned)
CREATE INDEX IF NOT EXISTS idx_faults_assigned ON faults(assigned_to);

-- doppelt: idx_shift_assign_date / idx_shift_assign_user (003/011) decken dieselben Spalten ab
DROP INDEX IF EXISTS idx_shifts_date;
DROP INDEX IF EXISTS idx_shifts_user;
