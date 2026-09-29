-- 069_field_sets.up.sql
--
-- Gemeinsames Feldsatz-System fuer alle Module (Ersatzteile, Tickets,
-- Aufgaben, Projekte, Stoerungen, Wartungen, Infrastruktur, Lagerplaetze
-- ...). Ein Feldsatz gehoert zu genau einem Modul und kann
--   * manuell einem Datensatz zugeordnet werden (record_field_sets) oder
--   * automatisch gelten: auto_match = '*' (alle Datensaetze des Moduls)
--     bzw. = Kategorie/Typ des Datensatzes (z. B. Ersatzteil-Kategorie
--     "Motoren", Anlagentyp "plant").
-- Die bisherigen Ersatzteil-Feldsaetze (013-016) werden mit gleichen IDs
-- uebernommen; die alten Tabellen bleiben als Altbestand unveraendert.

CREATE TABLE IF NOT EXISTS field_sets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    module      VARCHAR(40)  NOT NULL,
    name        VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    auto_match  VARCHAR(100) NOT NULL DEFAULT '',
    sort_order  INT NOT NULL DEFAULT 100,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_field_sets_module_name ON field_sets (module, lower(name)) WHERE active;

CREATE TABLE IF NOT EXISTS field_defs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    field_set_id UUID NOT NULL REFERENCES field_sets(id) ON DELETE CASCADE,
    name         VARCHAR(100) NOT NULL,
    field_type   VARCHAR(20)  NOT NULL DEFAULT 'text'
                 CHECK (field_type IN ('text', 'textarea', 'number', 'date', 'select', 'checkbox', 'url')),
    unit         VARCHAR(30)  NOT NULL DEFAULT '',
    required     BOOLEAN NOT NULL DEFAULT false,
    help         VARCHAR(300) NOT NULL DEFAULT '',
    sort_order   INT NOT NULL DEFAULT 100,
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_field_defs_set ON field_defs(field_set_id);

CREATE TABLE IF NOT EXISTS field_options (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    field_id   UUID NOT NULL REFERENCES field_defs(id) ON DELETE CASCADE,
    value      TEXT NOT NULL,
    sort_order INT NOT NULL DEFAULT 100,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_field_options_unique ON field_options (field_id, lower(value)) WHERE active;

CREATE TABLE IF NOT EXISTS record_field_sets (
    module       VARCHAR(40) NOT NULL,
    record_id    UUID NOT NULL,
    field_set_id UUID NOT NULL REFERENCES field_sets(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (module, record_id, field_set_id)
);

CREATE TABLE IF NOT EXISTS record_field_values (
    module     VARCHAR(40) NOT NULL,
    record_id  UUID NOT NULL,
    field_id   UUID NOT NULL REFERENCES field_defs(id) ON DELETE CASCADE,
    value      TEXT NOT NULL DEFAULT '',
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (module, record_id, field_id)
);
CREATE INDEX IF NOT EXISTS idx_record_field_values_field ON record_field_values(field_id);

-- ── Uebernahme der Ersatzteil-Feldsaetze ─────────────────────
DO $$
DECLARE
    general_set UUID;
BEGIN
    IF to_regclass('public.spare_part_field_defs') IS NULL THEN
        RETURN;
    END IF;

    IF to_regclass('public.spare_part_field_sets') IS NOT NULL THEN
        INSERT INTO field_sets (id, module, name, description, active, created_at)
        SELECT id, 'part', name, description, active, created_at FROM spare_part_field_sets
        ON CONFLICT (id) DO NOTHING;
    END IF;

    -- Felder ohne Feldsatz (Stand 013) landen im Feldsatz "Allgemein"
    IF EXISTS (SELECT 1 FROM spare_part_field_defs d
               WHERE d.field_set_id IS NULL
                  OR NOT EXISTS (SELECT 1 FROM field_sets s WHERE s.id = d.field_set_id)) THEN
        SELECT id INTO general_set FROM field_sets WHERE module = 'part' AND lower(name) = 'allgemein' AND active LIMIT 1;
        IF general_set IS NULL THEN
            INSERT INTO field_sets (module, name, description) VALUES ('part', 'Allgemein', 'Übernommene Stammdatenfelder')
            RETURNING id INTO general_set;
        END IF;
    END IF;

    INSERT INTO field_defs (id, field_set_id, name, field_type, sort_order, active, created_at)
    SELECT d.id,
           CASE WHEN EXISTS (SELECT 1 FROM field_sets s WHERE s.id = d.field_set_id) THEN d.field_set_id ELSE general_set END,
           d.name,
           CASE lower(d.field_type) WHEN 'number' THEN 'number' WHEN 'date' THEN 'date'
                WHEN 'select' THEN 'select' WHEN 'list' THEN 'select' WHEN 'choice' THEN 'select' ELSE 'text' END,
           d.sort_order, d.active, d.created_at
    FROM spare_part_field_defs d
    ON CONFLICT (id) DO NOTHING;

    IF to_regclass('public.spare_part_field_options') IS NOT NULL THEN
        INSERT INTO field_options (id, field_id, value, sort_order, active, created_at)
        SELECT o.id, o.field_id, o.value, o.sort_order, o.active, o.created_at
        FROM spare_part_field_options o
        WHERE EXISTS (SELECT 1 FROM field_defs f WHERE f.id = o.field_id)
        ON CONFLICT DO NOTHING;
    END IF;

    IF to_regclass('public.spare_part_field_set_assignments') IS NOT NULL THEN
        INSERT INTO record_field_sets (module, record_id, field_set_id, created_at)
        SELECT 'part', a.part_id, a.field_set_id, a.created_at
        FROM spare_part_field_set_assignments a
        WHERE EXISTS (SELECT 1 FROM field_sets s WHERE s.id = a.field_set_id)
        ON CONFLICT DO NOTHING;
    END IF;

    INSERT INTO record_field_values (module, record_id, field_id, value, updated_at)
    SELECT 'part', v.part_id, v.field_id, v.value, v.updated_at
    FROM spare_part_field_values v
    WHERE EXISTS (SELECT 1 FROM field_defs f WHERE f.id = v.field_id)
    ON CONFLICT DO NOTHING;

    -- Teile mit Werten in "Allgemein" bekommen den Feldsatz zugeordnet
    IF general_set IS NOT NULL THEN
        INSERT INTO record_field_sets (module, record_id, field_set_id)
        SELECT DISTINCT 'part', v.record_id, general_set
        FROM record_field_values v JOIN field_defs f ON f.id = v.field_id
        WHERE v.module = 'part' AND f.field_set_id = general_set
        ON CONFLICT DO NOTHING;
    END IF;
END $$;

-- ── Berechtigung: Feldsaetze verwalten (nur Admin) ────────────
INSERT INTO permissions (key, label, category) VALUES
    ('fieldsets.manage', 'Feldsätze & Stammdatenfelder verwalten', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'admin' AND p.key = 'fieldsets.manage'
ON CONFLICT DO NOTHING;
