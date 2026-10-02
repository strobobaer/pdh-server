-- Wird ein Vorgang ohne Materialbuchung geschlossen (Verwerfen, Archivieren,
-- Statuswechsel), gehen noch offene Reservierungen an den Lagerplatz zurueck.
-- Der regulaere Abschluss macht Reservierungen vorher zum Verbrauch
-- (inventory.Service.ConsumeReservations), dann ist hier nichts mehr offen.
CREATE OR REPLACE FUNCTION pdh_reservation_release_on_close() RETURNS trigger AS $$
BEGIN
    IF NEW.status::text NOT IN ('resolved', 'closed', 'done', 'skipped')
       OR OLD.status::text IN ('resolved', 'closed', 'done', 'skipped') THEN
        RETURN NEW;
    END IF;
    CASE TG_TABLE_NAME
        WHEN 'faults' THEN DELETE FROM fault_pending_parts WHERE fault_id = NEW.id AND reserved;
        WHEN 'tickets' THEN DELETE FROM ticket_pending_parts WHERE ticket_id = NEW.id AND reserved;
        WHEN 'tasks' THEN DELETE FROM task_pending_parts WHERE task_id = NEW.id AND reserved;
        ELSE DELETE FROM maintenance_task_pending_parts WHERE task_id = NEW.id AND reserved;
    END CASE;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_reservation_release ON faults;
CREATE TRIGGER trg_reservation_release AFTER UPDATE OF status ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_release_on_close();
DROP TRIGGER IF EXISTS trg_reservation_release ON tickets;
CREATE TRIGGER trg_reservation_release AFTER UPDATE OF status ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_release_on_close();
DROP TRIGGER IF EXISTS trg_reservation_release ON tasks;
CREATE TRIGGER trg_reservation_release AFTER UPDATE OF status ON tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_release_on_close();
DROP TRIGGER IF EXISTS trg_reservation_release ON maintenance_tasks;
CREATE TRIGGER trg_reservation_release AFTER UPDATE OF status ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_release_on_close();
