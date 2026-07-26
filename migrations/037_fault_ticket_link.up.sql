-- 037_fault_ticket_link.up.sql
--
-- Dauerhafte gegenseitige Verknuepfung zwischen Stoerung und automatisch
-- erzeugtem Ticket, damit Massnahmen und Status zwischen beiden
-- synchronisiert werden koennen (statt nur einmaliger Historien-Eintrag).

ALTER TABLE faults ADD COLUMN IF NOT EXISTS linked_ticket_id UUID REFERENCES tickets(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS linked_fault_id UUID REFERENCES faults(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_faults_linked_ticket ON faults(linked_ticket_id);
CREATE INDEX IF NOT EXISTS idx_tickets_linked_fault ON tickets(linked_fault_id);

