-- 010_it_infrastructure.up.sql

DO $$ BEGIN
    CREATE TYPE it_asset_type AS ENUM ('server','network','workstation','printer','phone','tablet','other');
    CREATE TYPE it_asset_status AS ENUM ('active','inactive','maintenance','retired');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS it_assets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(255) NOT NULL,
    type         it_asset_type NOT NULL DEFAULT 'other',
    status       it_asset_status NOT NULL DEFAULT 'active',
    hostname     VARCHAR(255),
    ip_address   VARCHAR(50),
    mac_address  VARCHAR(50),
    manufacturer VARCHAR(100),
    model        VARCHAR(100),
    serial_no    VARCHAR(100),
    location     VARCHAR(255),
    os           VARCHAR(100),
    purchased_at DATE,
    warranty_until DATE,
    assigned_to  UUID REFERENCES users(id),
    notes        TEXT,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_it_type   ON it_assets(type);
CREATE INDEX IF NOT EXISTS idx_it_status ON it_assets(status);
CREATE INDEX IF NOT EXISTS idx_it_user   ON it_assets(assigned_to);
