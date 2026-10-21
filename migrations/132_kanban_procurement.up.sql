-- 132_kanban_procurement.up.sql
-- Beschaffung & Einkauf:
--   * Rahmenverträge mit Positionen je Ersatzteil (Preis, Rahmenmenge, abgerufen)
--   * Kanban-Regelkreise je Ersatzteil (Reiter „Kanban“ im Ersatzteilstamm, zum
--     Aktivieren) mit Karten voll → leer → bestellt → (Wareneingang) voll
--   * Buchungserfassung per Etiketten-QR: Ausbuchung und Reservierung auch auf
--     eine Anlage aus dem Infrastruktur-Baum
-- Neue Rechte (Rollen & Rechte):
--   inventory.book        – Ersatzteile ein-/ausbuchen und reservieren
--   inventory.correct     – Bestandskorrekturen und sonstige Buchungen
--   procurement.view      – Beschaffung ansehen (Kanban, Bestellvorschlag, Verträge)
--   procurement.kanban    – Kanban-Regelkreise einrichten
--   procurement.order     – bestellen, Wareneingang zu Kanban-Karten
--   procurement.contracts – Rahmenverträge pflegen

INSERT INTO permissions (key, label, category) VALUES
    ('inventory.book',        'Ersatzteile ein-/ausbuchen und reservieren',                   'Lager'),
    ('inventory.correct',     'Bestandskorrekturen und sonstige Buchungen',                   'Lager'),
    ('procurement.view',      'Beschaffung ansehen (Kanban, Bestellvorschlag, Rahmenverträge)', 'Beschaffung'),
    ('procurement.kanban',    'Kanban-Regelkreise einrichten (Karten, Mengen)',               'Beschaffung'),
    ('procurement.order',     'Bestellen und Wareneingang zu Kanban-Karten melden',           'Beschaffung'),
    ('procurement.contracts', 'Rahmenverträge pflegen',                                      'Beschaffung')
ON CONFLICT (key) DO NOTHING;

-- Bisher durfte buchen, wer das Lager sehen durfte – Verhalten bleibt.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, n.id
FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
JOIN permissions n ON n.key = 'inventory.book'
WHERE o.key IN ('inventory.view', 'inventory.edit')
ON CONFLICT DO NOTHING;

-- Korrekturen, Kanban einrichten: wer das Lager bearbeiten darf
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, n.id
FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
JOIN permissions n ON n.key IN ('inventory.correct', 'procurement.view', 'procurement.kanban')
WHERE o.key = 'inventory.edit'
ON CONFLICT DO NOTHING;

-- Bestellen und Rahmenverträge: wer Hersteller & Lieferanten bearbeiten darf
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, n.id
FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
JOIN permissions n ON n.key IN ('procurement.view', 'procurement.order', 'procurement.contracts')
WHERE o.key = 'directory.edit'
ON CONFLICT DO NOTHING;

-- Administratoren: alles
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key IN ('inventory.book', 'inventory.correct', 'procurement.view',
                                    'procurement.kanban', 'procurement.order', 'procurement.contracts')
ON CONFLICT DO NOTHING;

-- ── Rahmenverträge ────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS framework_contracts (
    id           UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    contract_no  VARCHAR(60)  NOT NULL,
    title        VARCHAR(200) NOT NULL DEFAULT '',
    supplier_id  UUID NOT NULL REFERENCES business_partners(id) ON DELETE RESTRICT,
    valid_from   DATE,
    valid_until  DATE,
    total_value  NUMERIC(14,2) NOT NULL DEFAULT 0, -- Vertragsvolumen in €, 0 = ohne Obergrenze
    notes        TEXT NOT NULL DEFAULT '',
    active       BOOLEAN NOT NULL DEFAULT true,
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_framework_contracts_no ON framework_contracts (supplier_id, contract_no);

CREATE TABLE IF NOT EXISTS framework_contract_items (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    contract_id      UUID NOT NULL REFERENCES framework_contracts(id) ON DELETE CASCADE,
    part_id          UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    supplier_part_no VARCHAR(100) NOT NULL DEFAULT '',
    price            NUMERIC(12,4) NOT NULL DEFAULT 0,
    contract_qty     NUMERIC(12,3) NOT NULL DEFAULT 0, -- Rahmenmenge, 0 = ohne Obergrenze
    called_qty       NUMERIC(12,3) NOT NULL DEFAULT 0, -- abgerufen (über Kanban-Bestellungen)
    UNIQUE (contract_id, part_id)
);
CREATE INDEX IF NOT EXISTS idx_framework_contract_items_part ON framework_contract_items (part_id);

-- ── Kanban ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS kanban_loops (
    part_id         UUID PRIMARY KEY REFERENCES spare_parts(id) ON DELETE CASCADE,
    active          BOOLEAN NOT NULL DEFAULT true,
    card_count      INT NOT NULL CHECK (card_count BETWEEN 1 AND 99),
    card_qty        NUMERIC(12,3) NOT NULL CHECK (card_qty > 0),
    storage_node_id UUID REFERENCES storage_nodes(id) ON DELETE SET NULL,
    supplier_id     UUID REFERENCES business_partners(id) ON DELETE SET NULL,
    contract_id     UUID REFERENCES framework_contracts(id) ON DELETE SET NULL,
    lead_time_days  INT NOT NULL DEFAULT 0,
    auto_empty      BOOLEAN NOT NULL DEFAULT true, -- Karten bei sinkendem Bestand automatisch leer melden
    notes           TEXT NOT NULL DEFAULT '',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS kanban_cards (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    part_id     UUID NOT NULL REFERENCES kanban_loops(part_id) ON DELETE CASCADE,
    card_no     INT NOT NULL,
    status      VARCHAR(10) NOT NULL DEFAULT 'full' CHECK (status IN ('full', 'empty', 'ordered')),
    emptied_at  TIMESTAMPTZ,
    emptied_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    ordered_at  TIMESTAMPTZ,
    ordered_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    order_ref   VARCHAR(100) NOT NULL DEFAULT '',
    expected_at DATE,
    UNIQUE (part_id, card_no)
);
CREATE INDEX IF NOT EXISTS idx_kanban_cards_status ON kanban_cards (status);

-- ── Ausbuchung / Reservierung auf eine Anlage ─────────────────
ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_stock_movements_infra ON stock_movements (infrastructure_id) WHERE infrastructure_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS infrastructure_pending_parts (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    part_id           UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    storage_node_id   UUID NOT NULL REFERENCES storage_nodes(id),
    qty               NUMERIC NOT NULL CHECK (qty > 0),
    reserved          BOOLEAN NOT NULL DEFAULT true,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_infra_pending_parts_part ON infrastructure_pending_parts (part_id);

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
      FROM maintenance_task_pending_parts WHERE reserved
    UNION ALL
    SELECT id, 'infrastructure', infrastructure_id, part_id, storage_node_id, qty, created_by, created_at
      FROM infrastructure_pending_parts WHERE reserved;

-- Rückbuchung (Migration 090) kennt jetzt auch Anlagen-Reservierungen.
CREATE OR REPLACE FUNCTION pdh_reservation_return() RETURNS trigger AS $$
DECLARE
    back NUMERIC;
    cur  NUMERIC;
    uid  UUID;
    ref  UUID;
    refcol TEXT;
    f UUID; t UUID; a UUID; m UUID; i UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT OLD.reserved THEN RETURN OLD; END IF;
        back := OLD.qty;
    ELSE
        IF NOT OLD.reserved OR NOT NEW.reserved OR NEW.qty >= OLD.qty THEN RETURN NEW; END IF;
        back := OLD.qty - NEW.qty;
    END IF;
    IF OLD.storage_node_id IS NULL THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;

    refcol := CASE TG_TABLE_NAME WHEN 'fault_pending_parts' THEN 'fault_id'
                                 WHEN 'ticket_pending_parts' THEN 'ticket_id'
                                 WHEN 'infrastructure_pending_parts' THEN 'infrastructure_id'
                                 ELSE 'task_id' END;
    ref := (to_jsonb(OLD) ->> refcol)::uuid;
    CASE TG_TABLE_NAME
        WHEN 'fault_pending_parts' THEN IF EXISTS (SELECT 1 FROM faults WHERE id = ref) THEN f := ref; END IF;
        WHEN 'ticket_pending_parts' THEN IF EXISTS (SELECT 1 FROM tickets WHERE id = ref) THEN t := ref; END IF;
        WHEN 'task_pending_parts' THEN IF EXISTS (SELECT 1 FROM tasks WHERE id = ref) THEN a := ref; END IF;
        WHEN 'infrastructure_pending_parts' THEN IF EXISTS (SELECT 1 FROM infrastructure WHERE id = ref) THEN i := ref; END IF;
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
                                 created_by, fault_id, ticket_id, task_id, maintenance_task_id, infrastructure_id, reservation_id)
    VALUES (gen_random_uuid(), OLD.part_id, 'unreserve', back, cur, cur + back, OLD.storage_node_id,
            'Rückbuchung Reservierung',
            CASE WHEN COALESCE(f, t, a, m, i) IS NULL THEN 'Vorgang gelöscht' ELSE '' END,
            uid, f, t, a, m, i, OLD.id);

    UPDATE spare_parts SET stock_qty = COALESCE((SELECT SUM(qty) FROM spare_part_stock WHERE part_id = OLD.part_id), 0),
                           updated_at = NOW()
     WHERE id = OLD.part_id;

    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_reservation_return ON infrastructure_pending_parts;
CREATE TRIGGER trg_reservation_return AFTER DELETE OR UPDATE OF qty ON infrastructure_pending_parts
    FOR EACH ROW EXECUTE FUNCTION pdh_reservation_return();
