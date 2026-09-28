-- 064_business_partners.up.sql
--
-- Systemweites Hersteller-/Lieferantenverzeichnis. Ein Eintrag kann
-- Hersteller, Lieferant oder beides sein (kind), da ein Unternehmen oft
-- beide Rollen gleichzeitig einnimmt. Ersatzteile, Infrastruktur und
-- IT-Geraete bekommen ein neues manufacturer_id-Feld, das hierher zeigt -
-- das bisherige Freitextfeld "manufacturer" bleibt als Fallback fuer
-- Altbestand erhalten (wird nur noch angezeigt, wenn keine Verzeichnis-
-- Verknuepfung gesetzt ist).

CREATE TABLE IF NOT EXISTS business_partners (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(200) NOT NULL,
    kind         VARCHAR(20) NOT NULL DEFAULT 'manufacturer' CHECK (kind IN ('manufacturer', 'supplier', 'both')),
    contact_name VARCHAR(150) NOT NULL DEFAULT '',
    email        VARCHAR(150) NOT NULL DEFAULT '',
    phone        VARCHAR(50)  NOT NULL DEFAULT '',
    website      VARCHAR(200) NOT NULL DEFAULT '',
    address      VARCHAR(300) NOT NULL DEFAULT '',
    notes        VARCHAR(1000) NOT NULL DEFAULT '',
    active       BOOLEAN NOT NULL DEFAULT true,
    created_by   UUID REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_business_partners_active ON business_partners(active);
CREATE INDEX IF NOT EXISTS idx_business_partners_kind ON business_partners(kind);

ALTER TABLE spare_parts ADD COLUMN IF NOT EXISTS manufacturer_id UUID REFERENCES business_partners(id);
ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS manufacturer_id UUID REFERENCES business_partners(id);
ALTER TABLE it_assets ADD COLUMN IF NOT EXISTS manufacturer_id UUID REFERENCES business_partners(id);
