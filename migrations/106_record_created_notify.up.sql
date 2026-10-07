-- Hinweis beim Anlegen: jeder neue Vorgang landet als 'created' in der
-- Aenderungs-Warteschlange (migrations/072). Der Server schickt daraus dem
-- Ersteller eine Bestaetigung, informiert die Zugewiesenen und verteilt
-- nicht zugewiesene Vorgaenge an die Broker - unabhaengig davon, ueber
-- welchen Weg (Neu anlegen, Modul, Leitstand, API, Copilot) angelegt wurde.

CREATE OR REPLACE FUNCTION pdh_queue_record_created() RETURNS trigger AS $$
BEGIN
    INSERT INTO record_change_queue (module, record_id, kind)
    VALUES (TG_ARGV[0], NEW.id, 'created');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_created_tickets ON tickets;
CREATE TRIGGER trg_pdh_created_tickets AFTER INSERT ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('ticket');
DROP TRIGGER IF EXISTS trg_pdh_created_faults ON faults;
CREATE TRIGGER trg_pdh_created_faults AFTER INSERT ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('fault');
DROP TRIGGER IF EXISTS trg_pdh_created_tasks ON tasks;
CREATE TRIGGER trg_pdh_created_tasks AFTER INSERT ON tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('task');
DROP TRIGGER IF EXISTS trg_pdh_created_projects ON projects;
CREATE TRIGGER trg_pdh_created_projects AFTER INSERT ON projects
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('project');
DROP TRIGGER IF EXISTS trg_pdh_created_maintenance ON maintenance_tasks;
CREATE TRIGGER trg_pdh_created_maintenance AFTER INSERT ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('maintenance_task');
DROP TRIGGER IF EXISTS trg_pdh_created_kvp ON kvp_ideas;
CREATE TRIGGER trg_pdh_created_kvp AFTER INSERT ON kvp_ideas
    FOR EACH ROW EXECUTE FUNCTION pdh_queue_record_created('kvp');
