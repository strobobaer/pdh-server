-- 087_department_scope.up.sql
-- Rechte je Abteilung: Die Abteilung eines Vorgangs kommt aus der Anlage
-- (Infrastruktur). Anlagen ohne eigene Abteilung erben sie vom naechsten
-- Elternknoten. Abteilungen bilden eine Hierarchie (Organigramm); Rollen mit
-- Abteilung sehen nur deren Vorgaenge inkl. Unterabteilungen. Uebergeordnete
-- Abteilungen (Instandhaltung, IT, Office, GL) sehen alles.

ALTER TABLE departments ADD COLUMN IF NOT EXISTS parent_id UUID REFERENCES departments(id) ON DELETE SET NULL;
ALTER TABLE departments ADD COLUMN IF NOT EXISTS is_global BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_departments_parent ON departments(parent_id);

ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS department_id UUID REFERENCES departments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_infrastructure_department ON infrastructure(department_id);

-- Vorbelegung der uebergeordneten Abteilungen
UPDATE departments SET is_global = true
 WHERE lower(name) IN ('instandhaltung', 'it', 'office', 'gl', 'geschäftsleitung', 'geschaeftsleitung');
INSERT INTO departments (name, is_global)
SELECT v.name, true FROM (VALUES ('Instandhaltung'), ('IT'), ('Office')) AS v(name)
ON CONFLICT ((lower(name))) DO UPDATE SET is_global = true, active = true;
INSERT INTO departments (name, is_global)
SELECT 'GL', true
 WHERE NOT EXISTS (SELECT 1 FROM departments WHERE lower(name) IN ('gl', 'geschäftsleitung', 'geschaeftsleitung'))
ON CONFLICT DO NOTHING;

-- Wirksame Abteilung je Anlage (eigene oder vom naechsten Elternknoten)
CREATE OR REPLACE VIEW infrastructure_department AS
WITH RECURSIVE chain AS (
    SELECT i.id, i.department_id, i.parent_id, 0 AS depth FROM infrastructure i
    UNION ALL
    SELECT c.id, p.department_id, p.parent_id, c.depth + 1
      FROM chain c JOIN infrastructure p ON p.id = c.parent_id
     WHERE c.department_id IS NULL AND c.depth < 50
)
SELECT DISTINCT ON (id) id AS infrastructure_id, department_id
  FROM chain
 WHERE department_id IS NOT NULL
 ORDER BY id, depth;
