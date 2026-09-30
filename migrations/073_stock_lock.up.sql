-- 073_stock_lock.up.sql
--
-- Gesperrte Ersatzteile und Lagerplaetze (locked_at, Migration 072) duerfen
-- nicht mehr gebucht werden - egal ueber welchen Weg (Ersatzteilliste,
-- Detailseite, Materialentnahme in Vorgaengen, Schnittstelle).

CREATE OR REPLACE FUNCTION pdh_block_locked_stock() RETURNS trigger AS $$
DECLARE
    t TEXT;
BEGIN
    SELECT part_number || ' · ' || name INTO t FROM spare_parts WHERE id = NEW.part_id AND locked_at IS NOT NULL;
    IF FOUND THEN
        RAISE EXCEPTION 'Ersatzteil „%“ ist gesperrt – keine Buchung möglich', t USING ERRCODE = 'P0001';
    END IF;
    IF NEW.storage_node_id IS NOT NULL THEN
        SELECT name INTO t FROM storage_nodes WHERE id = NEW.storage_node_id AND locked_at IS NOT NULL;
        IF FOUND THEN
            RAISE EXCEPTION 'Lagerplatz „%“ ist gesperrt – keine Buchung möglich', t USING ERRCODE = 'P0001';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_block_locked_stock ON stock_movements;
CREATE TRIGGER trg_pdh_block_locked_stock BEFORE INSERT ON stock_movements
    FOR EACH ROW EXECUTE FUNCTION pdh_block_locked_stock();
