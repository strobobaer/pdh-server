-- 149_purchase_orders.up.sql
-- Bestellvorgänge (internal/web/purchases.go): Angebot → Bestellung →
-- Auftragsbestätigung (AB) → Lieferung → Rechnung OK. Ein Vorgang je
-- Lieferant mit mehreren Positionen; Teillieferungen buchen ins Lager.
-- Die Rechnung wird in ELO bearbeitet – „Rechnung OK“ schließt den
-- Vorgang, auch ohne hinterlegte Rechnung.

CREATE SEQUENCE IF NOT EXISTS purchase_order_seq;

CREATE TABLE IF NOT EXISTS purchase_orders (
    id                 UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    number             VARCHAR(30)  NOT NULL UNIQUE,            -- z. B. B-2026-0001
    title              VARCHAR(200) NOT NULL DEFAULT '',
    supplier_id        UUID REFERENCES business_partners(id) ON DELETE SET NULL,
    department_id      UUID REFERENCES departments(id) ON DELETE SET NULL,
    status             VARCHAR(20)  NOT NULL DEFAULT 'quote'
                       CHECK (status IN ('quote', 'ordered', 'confirmed', 'partial', 'delivered', 'closed', 'cancelled')),
    wanted_date        DATE,                                     -- Wunschtermin
    storage_node_id    UUID REFERENCES storage_nodes(id) ON DELETE SET NULL, -- Standard-Lagerort für den Wareneingang
    quote_no           VARCHAR(80)  NOT NULL DEFAULT '',
    quote_date         DATE,
    quote_valid_until  DATE,
    order_no           VARCHAR(80)  NOT NULL DEFAULT '',
    ordered_at         TIMESTAMPTZ,
    ordered_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    confirmation_no    VARCHAR(80)  NOT NULL DEFAULT '',
    confirmation_date  DATE,
    confirmed_delivery DATE,                                     -- Liefertermin laut AB
    invoice_ref        VARCHAR(120) NOT NULL DEFAULT '',         -- Rechnungs- oder ELO-Nummer (optional)
    invoice_note       TEXT         NOT NULL DEFAULT '',
    invoice_ok_at      TIMESTAMPTZ,
    invoice_ok_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    notes              TEXT         NOT NULL DEFAULT '',
    created_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_purchase_orders_supplier ON purchase_orders (supplier_id);
CREATE INDEX IF NOT EXISTS idx_purchase_orders_status ON purchase_orders (status);

CREATE TABLE IF NOT EXISTS purchase_order_items (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    order_id         UUID NOT NULL REFERENCES purchase_orders(id) ON DELETE CASCADE,
    pos              INTEGER NOT NULL DEFAULT 1,
    part_id          UUID REFERENCES spare_parts(id) ON DELETE SET NULL,
    description      VARCHAR(300) NOT NULL DEFAULT '',
    supplier_part_no VARCHAR(100) NOT NULL DEFAULT '',
    unit             VARCHAR(20)  NOT NULL DEFAULT 'Stk',
    qty_quoted       NUMERIC(14,3),
    price_quoted     NUMERIC(14,4),
    qty_ordered      NUMERIC(14,3),
    price_ordered    NUMERIC(14,4),
    qty_confirmed    NUMERIC(14,3),
    price_confirmed  NUMERIC(14,4),
    storage_node_id  UUID REFERENCES storage_nodes(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_purchase_items_order ON purchase_order_items (order_id);
CREATE INDEX IF NOT EXISTS idx_purchase_items_part ON purchase_order_items (part_id);

CREATE TABLE IF NOT EXISTS purchase_deliveries (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    order_id         UUID NOT NULL REFERENCES purchase_orders(id) ON DELETE CASCADE,
    delivery_note_no VARCHAR(80) NOT NULL DEFAULT '',
    delivered_on     DATE NOT NULL DEFAULT CURRENT_DATE,
    notes            TEXT NOT NULL DEFAULT '',
    received_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purchase_delivery_items (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    delivery_id     UUID NOT NULL REFERENCES purchase_deliveries(id) ON DELETE CASCADE,
    item_id         UUID NOT NULL REFERENCES purchase_order_items(id) ON DELETE CASCADE,
    qty             NUMERIC(14,3) NOT NULL,
    storage_node_id UUID REFERENCES storage_nodes(id) ON DELETE SET NULL,
    booked          BOOLEAN NOT NULL DEFAULT false             -- ins Lager gebucht
);
CREATE INDEX IF NOT EXISTS idx_purchase_delivery_items_item ON purchase_delivery_items (item_id);

-- Rechte (Kategorie Beschaffung)
INSERT INTO permissions (key, label, category) VALUES
    ('procurement.purchase', 'Beschaffung: Bestellvorgänge anlegen und bearbeiten (Angebot, Bestellung, AB)', 'Beschaffung'),
    ('procurement.receive',  'Beschaffung: Wareneingang zu Bestellvorgängen buchen', 'Beschaffung'),
    ('procurement.invoice',  'Beschaffung: Rechnung OK setzen (Bestellvorgang abschließen)', 'Beschaffung')
ON CONFLICT (key) DO NOTHING;

-- Wer bisher bestellen durfte, darf Bestellvorgänge führen und Rechnung OK setzen
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, p.id FROM role_permissions rp
JOIN permissions o ON o.id = rp.permission_id AND o.key = 'procurement.order'
CROSS JOIN permissions p WHERE p.key IN ('procurement.purchase', 'procurement.invoice', 'procurement.receive')
ON CONFLICT DO NOTHING;
-- Wer ins Lager buchen darf, darf auch den Wareneingang buchen
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, p.id FROM role_permissions rp
JOIN permissions o ON o.id = rp.permission_id AND o.key = 'inventory.book'
CROSS JOIN permissions p WHERE p.key = 'procurement.receive'
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key IN ('procurement.purchase', 'procurement.receive', 'procurement.invoice')
ON CONFLICT DO NOTHING;
