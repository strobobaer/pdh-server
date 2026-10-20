-- 129_infra_responsible_default.up.sql
-- Neue Tickets, Störungen, Wartungsaufträge und Aufgaben ohne Verantwortliche/n
-- übernehmen die Person der Anlage (eigene oder vom übergeordneten Element,
-- migrations/127). Gilt für jeden Weg des Anlegens (Assistent, Formulare,
-- Leitstand, Schnittstelle, Wartungspläne). Abschaltbar unter
-- Core-Einstellungen (app_settings infra_responsible_default = '0').
-- Eine ausdrücklich gesetzte Person (auch aus dem Wartungsplan) bleibt.

CREATE OR REPLACE FUNCTION pdh_infra_responsible_default() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.responsible_to IS NULL AND NEW.infrastructure_id IS NOT NULL
       AND COALESCE((SELECT value FROM app_settings WHERE key = 'infra_responsible_default'), '1') = '1' THEN
        NEW.responsible_to := (
            WITH RECURSIVE up AS (
                SELECT i.id, i.parent_id, i.responsible_to, 0 AS depth
                  FROM infrastructure i WHERE i.id = NEW.infrastructure_id
                UNION ALL
                SELECT p.id, p.parent_id, p.responsible_to, up.depth + 1
                  FROM infrastructure p JOIN up ON p.id = up.parent_id
                 WHERE up.responsible_to IS NULL AND up.depth < 50
            )
            SELECT up.responsible_to FROM up JOIN users u ON u.id = up.responsible_to AND u.active
             ORDER BY up.depth LIMIT 1);
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_tickets_infra_responsible ON tickets;
CREATE TRIGGER trg_tickets_infra_responsible BEFORE INSERT ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_infra_responsible_default();

DROP TRIGGER IF EXISTS trg_faults_infra_responsible ON faults;
CREATE TRIGGER trg_faults_infra_responsible BEFORE INSERT ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_infra_responsible_default();

DROP TRIGGER IF EXISTS trg_maintenance_tasks_infra_responsible ON maintenance_tasks;
CREATE TRIGGER trg_maintenance_tasks_infra_responsible BEFORE INSERT ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_infra_responsible_default();

DROP TRIGGER IF EXISTS trg_tasks_infra_responsible ON tasks;
CREATE TRIGGER trg_tasks_infra_responsible BEFORE INSERT ON tasks
    FOR EACH ROW EXECUTE FUNCTION pdh_infra_responsible_default();
