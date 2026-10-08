-- Automatisches Ticket aus einer Störung (PDH_FAULT_CREATE_TICKET) erst nach der
-- Zuweisung: Ist die Störung beim Anlegen niemandem zugewiesen (weder Person noch
-- Gruppe), wird sie markiert; ein Hintergrundlauf legt das Ticket an, sobald sie
-- zugewiesen ist (faults/deck_sync.go). Erledigte Störungen bekommen keins mehr.
ALTER TABLE faults ADD COLUMN IF NOT EXISTS ticket_pending BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_faults_ticket_pending ON faults(id) WHERE ticket_pending;
