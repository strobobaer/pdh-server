-- IT-Kategorien: die festen Asset-Typen (Enum it_asset_type) werden zu einer
-- pflegbaren Tabelle. Karenz je Kategorie und je Asset, Recht it.categories.
-- Die Migration ist wiederholbar (bereits umgestellt → nichts mehr zu tun).

CREATE TABLE IF NOT EXISTS it_asset_categories (
    key             TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    icon            TEXT NOT NULL DEFAULT '',
    sort_order      INT  NOT NULL DEFAULT 0,
    active          BOOLEAN NOT NULL DEFAULT true,
    monitor_default BOOLEAN NOT NULL DEFAULT true,
    grace_minutes   INT CHECK (grace_minutes BETWEEN 1 AND 1440),
    builtin         BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Vorbelegung wie bisher (network und other sind fest und nicht löschbar).
INSERT INTO it_asset_categories (key, name, sort_order, monitor_default, builtin) VALUES
    ('server',      'Server',      10, true,  false),
    ('network',     'Netzwerk',    20, true,  true),
    ('workstation', 'Workstation', 30, false, false),
    ('printer',     'Drucker',     40, true,  false),
    ('phone',       'Telefon',     50, false, false),
    ('tablet',      'Tablet',      60, false, false),
    ('other',       'Sonstiges',   70, true,  true)
ON CONFLICT (key) DO NOTHING;

-- Typ der Assets: Enum → Text mit Fremdschlüssel. Alle Zeilen behalten ihren
-- Wert; der Index idx_it_type wird dabei neu aufgebaut.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'it_assets' AND column_name = 'type'
                  AND data_type <> 'text') THEN
        ALTER TABLE it_assets ALTER COLUMN type DROP DEFAULT;
        ALTER TABLE it_assets ALTER COLUMN type TYPE TEXT USING type::text;
        ALTER TABLE it_assets ALTER COLUMN type SET DEFAULT 'other';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'it_assets_type_fkey') THEN
        ALTER TABLE it_assets ADD CONSTRAINT it_assets_type_fkey
            FOREIGN KEY (type) REFERENCES it_asset_categories(key) ON UPDATE CASCADE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_it_type ON it_assets(type);

DROP TYPE IF EXISTS it_asset_type;

-- Karenz dieses Assets (Minuten bis zum Alarm); leer = Kategorie bzw. allgemein.
ALTER TABLE it_assets ADD COLUMN IF NOT EXISTS net_grace_minutes INT
    CHECK (net_grace_minutes BETWEEN 1 AND 1440);

-- ── Recht ─────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('it.categories', 'IT-Kategorien pflegen', 'IT')
ON CONFLICT (key) DO NOTHING;

-- Wer IT-Administrator ist, darf auch Kategorien pflegen. Die Abteilung der
-- Quellrolle wird übernommen; gilt ein Recht überall (NULL), gewinnt „überall“.
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'it.admin'
  CROSS JOIN permissions p
 WHERE p.key = 'it.categories'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;
