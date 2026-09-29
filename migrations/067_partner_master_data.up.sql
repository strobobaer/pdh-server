-- 067_partner_master_data.up.sql
--
-- Hersteller-/Lieferanten-Coreboard: vollstaendige Stammdaten fuer das
-- Partnerverzeichnis (064), mehrere Ansprechpartner je Partner,
-- Lieferanten-Konditionen je Ersatzteil sowie Lieferant/Servicepartner
-- an Infrastruktur und IT-Geraeten. Die bisherigen Felder (contact_name,
-- address, ...) bleiben fuer Altbestand und API-Kompatibilitaet bestehen.

-- ── Stammdaten ────────────────────────────────────────────────
CREATE SEQUENCE IF NOT EXISTS business_partner_no_seq;

ALTER TABLE business_partners
    ADD COLUMN IF NOT EXISTS partner_no           VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS short_name           VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS category             VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approval_status      VARCHAR(20)  NOT NULL DEFAULT 'approved',
    ADD COLUMN IF NOT EXISTS rating               VARCHAR(1)   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS street               VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS postal_code          VARCHAR(20)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS city                 VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS country              VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fax                  VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS order_email          VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS service_phone        VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS service_email        VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS emergency_phone      VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS support_hotline      VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS support_hours        VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS support_phone        VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS support_email        VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS spare_parts_phone    VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS spare_parts_email    VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS portal_url           VARCHAR(300) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS vat_id               VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tax_no               VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS commercial_register  VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS creditor_no          VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS customer_no          VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS iban                 VARCHAR(40)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bic                  VARCHAR(20)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bank_name            VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS payment_terms        VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_terms       VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS currency             VARCHAR(3)   NOT NULL DEFAULT 'EUR',
    ADD COLUMN IF NOT EXISTS min_order_value      NUMERIC(12,2),
    ADD COLUMN IF NOT EXISTS lead_time_days       INT,
    ADD COLUMN IF NOT EXISTS contract_no          VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS contract_valid_until DATE,
    ADD COLUMN IF NOT EXISTS certificates         VARCHAR(500) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_evaluation_at   DATE;

ALTER TABLE business_partners ALTER COLUMN notes TYPE TEXT;

DO $$ BEGIN
    ALTER TABLE business_partners ADD CONSTRAINT business_partners_approval_status_check
        CHECK (approval_status IN ('approved', 'conditional', 'new', 'blocked'));
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    ALTER TABLE business_partners ADD CONSTRAINT business_partners_rating_check
        CHECK (rating IN ('', 'A', 'B', 'C'));
EXCEPTION WHEN duplicate_object THEN null; END $$;

-- Bestehende Partner bekommen fortlaufende Partnernummern
UPDATE business_partners bp SET partner_no = 'P-' || lpad(nextval('business_partner_no_seq')::text, 5, '0')
FROM (SELECT id FROM business_partners WHERE partner_no = '' ORDER BY created_at, name) x
WHERE bp.id = x.id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_business_partners_partner_no ON business_partners(partner_no) WHERE partner_no <> '';
CREATE INDEX IF NOT EXISTS idx_business_partners_category ON business_partners(category);

-- ── Ansprechpartner ──────────────────────────────────────────
CREATE TABLE IF NOT EXISTS business_partner_contacts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id  UUID NOT NULL REFERENCES business_partners(id) ON DELETE CASCADE,
    name        VARCHAR(150) NOT NULL,
    position    VARCHAR(100) NOT NULL DEFAULT '',
    department  VARCHAR(100) NOT NULL DEFAULT '',
    phone       VARCHAR(50)  NOT NULL DEFAULT '',
    mobile      VARCHAR(50)  NOT NULL DEFAULT '',
    email       VARCHAR(150) NOT NULL DEFAULT '',
    is_primary  BOOLEAN NOT NULL DEFAULT false,
    notes       VARCHAR(500) NOT NULL DEFAULT '',
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_bp_contacts_partner ON business_partner_contacts(partner_id);

-- bisherige Kontaktperson als Hauptansprechpartner uebernehmen
INSERT INTO business_partner_contacts (partner_id, name, phone, email, is_primary, created_by)
SELECT bp.id, bp.contact_name, bp.phone, bp.email, true, bp.created_by
FROM business_partners bp
WHERE bp.contact_name <> ''
  AND NOT EXISTS (SELECT 1 FROM business_partner_contacts c WHERE c.partner_id = bp.id);

-- ── Portale, Onlineshops & Zugangsdaten ──────────────────────
-- Links zu Support-Portalen, Onlineshops, Dokumentation usw. Die
-- Einbettung (Anzeige im PDH-Viewer statt neuem Tab) ist je Link
-- schaltbar, da viele Portale Einbettung per X-Frame-Options verbieten.
-- Passwoerter werden ausschliesslich AES-GCM-verschluesselt gespeichert.
CREATE TABLE IF NOT EXISTS business_partner_links (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id          UUID NOT NULL REFERENCES business_partners(id) ON DELETE CASCADE,
    kind                VARCHAR(30)  NOT NULL DEFAULT 'support_portal'
                        CHECK (kind IN ('support_portal', 'shop', 'documentation', 'download', 'ticket_system', 'other')),
    label               VARCHAR(150) NOT NULL,
    url                 VARCHAR(500) NOT NULL,
    embed_enabled       BOOLEAN NOT NULL DEFAULT false,
    username            VARCHAR(150) NOT NULL DEFAULT '',
    password_enc        TEXT NOT NULL DEFAULT '',
    password_updated_at TIMESTAMPTZ,
    account_no          VARCHAR(100) NOT NULL DEFAULT '',
    notes               VARCHAR(500) NOT NULL DEFAULT '',
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_bp_links_partner ON business_partner_links(partner_id);

-- bisherige Portal-URL als Link uebernehmen
INSERT INTO business_partner_links (partner_id, kind, label, url)
SELECT bp.id, 'support_portal', 'Kundenportal', bp.portal_url
FROM business_partners bp
WHERE bp.portal_url <> ''
  AND NOT EXISTS (SELECT 1 FROM business_partner_links l WHERE l.partner_id = bp.id);

-- ── Lieferanten-Konditionen je Ersatzteil ────────────────────
CREATE TABLE IF NOT EXISTS spare_part_suppliers (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    part_id          UUID NOT NULL REFERENCES spare_parts(id) ON DELETE CASCADE,
    partner_id       UUID NOT NULL REFERENCES business_partners(id) ON DELETE CASCADE,
    supplier_part_no VARCHAR(100) NOT NULL DEFAULT '',
    price            NUMERIC(12,2),
    min_order_qty    NUMERIC(12,3),
    lead_time_days   INT,
    preferred        BOOLEAN NOT NULL DEFAULT false,
    notes            VARCHAR(500) NOT NULL DEFAULT '',
    created_by       UUID REFERENCES users(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (part_id, partner_id)
);
CREATE INDEX IF NOT EXISTS idx_spare_part_suppliers_partner ON spare_part_suppliers(partner_id);

-- ── Einkauf im Ersatzteilstamm ───────────────────────────────
-- Verantwortlicher Einkaeufer je Ersatzteil; er erhaelt den taeglichen
-- Bestellvorschlag (Teile unter Mindestbestand, gruppiert nach
-- Lieferant). Hersteller (manufacturer_id) und Lieferant(en)
-- (spare_part_suppliers) sind bewusst getrennt.
ALTER TABLE spare_parts ADD COLUMN IF NOT EXISTS buyer_id UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_spare_parts_buyer ON spare_parts(buyer_id);

INSERT INTO app_settings (key, value) VALUES
    ('purchase_report_enabled', '1'),
    ('purchase_report_time', '06:00'),
    ('purchase_report_weekdays_only', '1'),
    ('purchase_report_default_buyer', '')
ON CONFLICT (key) DO NOTHING;

-- ── Lieferant / Servicepartner an Anlagen und IT ─────────────
ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS supplier_id UUID REFERENCES business_partners(id);
ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS service_partner_id UUID REFERENCES business_partners(id);
ALTER TABLE it_assets ADD COLUMN IF NOT EXISTS supplier_id UUID REFERENCES business_partners(id);

CREATE INDEX IF NOT EXISTS idx_infra_manufacturer ON infrastructure(manufacturer_id);
CREATE INDEX IF NOT EXISTS idx_infra_supplier ON infrastructure(supplier_id);
CREATE INDEX IF NOT EXISTS idx_infra_service_partner ON infrastructure(service_partner_id);
CREATE INDEX IF NOT EXISTS idx_spare_parts_manufacturer ON spare_parts(manufacturer_id);
CREATE INDEX IF NOT EXISTS idx_it_assets_manufacturer ON it_assets(manufacturer_id);
CREATE INDEX IF NOT EXISTS idx_it_assets_supplier ON it_assets(supplier_id);
CREATE INDEX IF NOT EXISTS idx_record_external_parties_partner ON record_external_parties(partner_id);

-- ── Berechtigungen ───────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('directory.view', 'Hersteller & Lieferanten ansehen',    'Partner'),
    ('directory.edit', 'Hersteller & Lieferanten bearbeiten', 'Partner'),
    ('directory.credentials', 'Portal-/Shop-Zugangsdaten anzeigen & pflegen', 'Partner')
ON CONFLICT (key) DO NOTHING;

-- Admin bekommt alles; wer Ersatzteile sehen/bearbeiten darf, darf das
-- bisher (ohne eigene Berechtigung) auch im Verzeichnis. Zugangsdaten
-- bewusst nur Admin - weitere Rollen gezielt ueber die Matrix freigeben.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key IN ('directory.view', 'directory.edit', 'directory.credentials')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT rp.role_id, np.id
FROM role_permissions rp
JOIN permissions op ON op.id = rp.permission_id
JOIN permissions np ON (op.key = 'inventory.view' AND np.key = 'directory.view')
                    OR (op.key = 'inventory.edit' AND np.key = 'directory.edit')
ON CONFLICT DO NOTHING;
