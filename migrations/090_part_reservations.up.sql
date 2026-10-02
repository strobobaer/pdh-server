-- Ersatzteil-Reservierung: vorgemerkte Teile werden sofort vom Lagerort
-- abgebucht (Bewegungsart 'reserve') und erst beim Abschluss des Vorgangs
-- zum Verbrauch ('out'). Nicht benoetigte Mengen werden zurueckgebucht
-- ('unreserve') - auch automatisch, wenn eine Reservierung entfernt oder der
-- Vorgang geloescht wird (Trigger unten).
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'reserve';
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'unreserve';

ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS reservation_id UUID;
CREATE INDEX IF NOT EXISTS idx_stock_movements_reservation ON stock_movements(reservation_id);

-- reserved=false: alte Vormerkung (noch nicht abgebucht, wird beim Abschluss gebucht)
ALTER TABLE fault_pending_parts            ADD COLUMN IF NOT EXISTS reserved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ticket_pending_parts           ADD COLUMN IF NOT EXISTS reserved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE task_pending_parts             ADD COLUMN IF NOT EXISTS reserved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE maintenance_task_pending_parts ADD COLUMN IF NOT EXISTS reserved BOOLEAN NOT NULL DEFAULT false;

-- Alle aktiven Reservierungen ueber die vier Vorgangsarten.
CREATE OR REPLACE VIEW spare_part_reservations AS
    SELECT id, 'fault'::text AS kind, fault_id AS ref_id, part_id, storage_node_id, qty, created_by, created_at
      FROM fault_pending_parts WHERE reserved
    UNION ALL
    SELECT id, 'ticket', ticket_id, part_id, storage_node_id, qty, created_by, created_at
      FROM ticket_pending_parts WHERE reserved
    UNION ALL
    SELECT id, 'task', task_id, part_id, storage_node_id, qty, created_by, created_at
      FROM task_pending_parts WHERE reserved
    UNION ALL
    SELECT id, 'maintenance_task', task_id, part_id, storage_node_id, qty, created_by, created_at
      FROM maintenance_task_pending_parts WHERE reserved;

-- Rueckbuchung: beim Loeschen einer Reservierung (auch per Kaskade, wenn der
-- Vorgang geloescht wird) oder beim Verringern ihrer Menge geht die Differenz
-- an den Lagerort zurueck. Wer bucht: pdh.user_id (falls gesetzt), sonst der
-- Ersteller der Reservierung.
CREATE OR REPLACE FUNCTION pdh_reservation_return() RETURNS trigger AS $$
DECLARE
    back NUMERIC;
    cur  NUMERIC;
    uid  UUID;
    ref  UUID;
    refcol TEXT;
    f UUID; t UUID; a UUID; m UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT OLD.reserved THEN RETURN OLD; END IF;
        back := OLD.qty;
    ELSE
        IF NOT OLD.reserved OR NOT NEW.reserved OR NEW.qty >= OLD.qty THEN RETURN NEW; END IF;
        back := OLD.qty - NEW.qty;
    END IF;

    refcol := CASE TG_TABLE_NAME WHEN 'fault_pending_parts' THEN 'fault_id'
                                 WHEN 'ticket_pending_parts' THEN 'ticket_id'
                                 ELSE 'task_id' END;
    ref := (to_jsonb(OLD) ->> refcol)::uuid;
    -- Vorgang existiert nicht mehr (Kaskade): Bewegung ohne Verknuepfung
    CASE TG_TABLE_NAME
        WHEN 'fault_pending_parts' THEN IF EXISTS (SELECT 1 FROM faults WHERE id = ref) THEN f := ref; END IF;
        WHEN 'ticket_pending_parts' THEN IF EXISTS (SELECT 1 FROM tickets WHERE id = ref) THEN t := ref; END IF;
        WHEN 'task_pending_parts' THEN IF EXISTS (SELECT 1 FROM tasks WHERE id = ref) THEN a := ref; END IF;
        ELSE IF EXISTS (SELECT 1 FROM maintenance_tasks WHERE id = ref) THEN m := ref; END IF;
    END CASE;

    uid := COALESCE(NULLIF(current_setting('pdh.user_id', true), '')::uuid, OLD.created_by);

    SELECT qty INTO cur FROM spare_part_stock
     WHERE part_id = OLD.part_id AND storage_node_id = OLD.storage_node_id FOR UPDATE;
    cur := COALESCE(cur, 0);
    INSERT INTO spare_part_stock (part_id, storage_node_id, qty, updated_at)
    VALUES (OLD.part_id, OLD.storage_node_id, cur + back, NOW())
    ON CONFLICT (part_id, storage_node_id) DO UPDATE SET qty = EXCLUDED.qty, updated_at = NOW();

    INSERT INTO stock_movements (id, part_id, type, qty, qty_before, qty_after, storage_node_id, reference, notes,
                                 created_by, fault_id, ticket_id, task_id, maintenance_task_id, reservation_id)
    VALUES (gen_random_uuid(), OLD.part_id, 'unreserve', back, cur, cur + back, OLD.storage_node_id,
            'Rückbuchung Reservierung',
            CASE WHEN COALESCE(f, t, a, m) IS NULL THEN 'Vorgang gelöscht' ELSE '' END,
            uid, f, t, a, m, OLD.id);

    UPDATE spare_parts SET stock_qty = COALESCE((SELECT SUM(qty) FROM spare_part_stock WHERE part_id = OLD.part_id), 0),
                           updated_at = NOW()
     WHERE id = OLD.part_id;

    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_reservation_return ON fault_pending_parts;
CREATE TRIGGER trg_reservation_return AFTER DELETE OR UPDATE OF qty ON fault_pending_parts
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_return();
DROP TRIGGER IF EXISTS trg_reservation_return ON ticket_pending_parts;
CREATE TRIGGER trg_reservation_return AFTER DELETE OR UPDATE OF qty ON ticket_pending_parts
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_return();
DROP TRIGGER IF EXISTS trg_reservation_return ON task_pending_parts;
CREATE TRIGGER trg_reservation_return AFTER DELETE OR UPDATE OF qty ON task_pending_parts
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_return();
DROP TRIGGER IF EXISTS trg_reservation_return ON maintenance_task_pending_parts;
CREATE TRIGGER trg_reservation_return AFTER DELETE OR UPDATE OF qty ON maintenance_task_pending_parts
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_return();
