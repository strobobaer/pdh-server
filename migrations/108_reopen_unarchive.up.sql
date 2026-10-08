-- Wiedergeoeffnete Vorgaenge: Beim Erledigen setzen Tickets, Stoerungen und
-- Aufgaben archived_at/resolved_at. Ging der Status danach zurueck auf offen,
-- in Arbeit oder wartend, blieben beide stehen – der Vorgang fehlte dann in
-- den Listen und Zaehlern, die Archiviertes ausblenden, tauchte aber im
-- Leitstand auf. Ab jetzt hebt jeder Wechsel auf einen offenen Status die
-- Archivierung auf; bestehende Faelle werden einmalig bereinigt.

CREATE OR REPLACE FUNCTION pdh_reopen_unarchive() RETURNS trigger AS $$
BEGIN
    IF NEW.status::text NOT IN ('resolved', 'closed') AND (NEW.archived_at IS NOT NULL OR NEW.resolved_at IS NOT NULL) THEN
        NEW.archived_at := NULL;
        NEW.resolved_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_reopen_tickets ON tickets;
CREATE TRIGGER trg_pdh_reopen_tickets BEFORE INSERT OR UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_reopen_unarchive();
DROP TRIGGER IF EXISTS trg_pdh_reopen_faults ON faults;
CREATE TRIGGER trg_pdh_reopen_faults BEFORE INSERT OR UPDATE ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_reopen_unarchive();
DROP TRIGGER IF EXISTS trg_pdh_reopen_tasks ON tasks;
CREATE TRIGGER trg_pdh_reopen_tasks BEFORE INSERT OR UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_reopen_unarchive();

-- Bestand bereinigen (der Trigger setzt die Felder dabei zurueck)
UPDATE tickets SET updated_at = updated_at WHERE status::text NOT IN ('resolved', 'closed') AND (archived_at IS NOT NULL OR resolved_at IS NOT NULL);
UPDATE faults  SET updated_at = updated_at WHERE status::text NOT IN ('resolved', 'closed') AND (archived_at IS NOT NULL OR resolved_at IS NOT NULL);
UPDATE tasks   SET updated_at = updated_at WHERE status::text NOT IN ('resolved', 'closed') AND (archived_at IS NOT NULL OR resolved_at IS NOT NULL);

-- die Bereinigung ist keine Aenderung durch Benutzer – keine Chat-Hinweise dafuer
DELETE FROM record_change_queue
 WHERE kind = 'update' AND fields <@ ARRAY['archived_at', 'resolved_at']::text[]
   AND created_at > NOW() - INTERVAL '10 minutes';
