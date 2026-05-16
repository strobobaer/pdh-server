-- 005_infrastructure.up.sql

DO $$ BEGIN
    CREATE TYPE infra_type AS ENUM ('building','line','plant','device');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS infrastructure (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id    UUID REFERENCES infrastructure(id),
    name         VARCHAR(255) NOT NULL,
    type         infra_type NOT NULL,
    description  TEXT,
    location     VARCHAR(255),
    serial_no    VARCHAR(100),
    manufacturer VARCHAR(100),
    model        VARCHAR(100),
    installed_at DATE,
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_infra_parent ON infrastructure(parent_id);
CREATE INDEX IF NOT EXISTS idx_infra_type   ON infrastructure(type);
CREATE INDEX IF NOT EXISTS idx_infra_active ON infrastructure(active);

-- Beispiel-Topologie
INSERT INTO infrastructure (id, name, type, description, location) VALUES
  ('10000000-0000-0000-0000-000000000001', 'Werk Augsburg',    'building', 'Hauptwerk',           'Augsburg')
ON CONFLICT DO NOTHING;

INSERT INTO infrastructure (parent_id, name, type, description) VALUES
  ('10000000-0000-0000-0000-000000000001', 'Produktionslinie 1', 'line', 'Linie 1 – Montage'),
  ('10000000-0000-0000-0000-000000000001', 'Produktionslinie 2', 'line', 'Linie 2 – Fertigung'),
  ('10000000-0000-0000-0000-000000000001', 'Lager',              'line', 'Lagerbereich')
ON CONFLICT DO NOTHING;
