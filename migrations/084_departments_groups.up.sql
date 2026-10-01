-- 084_departments_groups.up.sql
-- Personalstamm: Abteilungen als Stammdaten (statt Freitext), Gruppen mit
-- Mitgliedern und Abteilungszugehoerigkeit von Rollen.
--
-- users.department (Text) bleibt bestehen, weil viele Stellen ihn lesen
-- (Abschluss-Assistent, Microsoft-Verzeichnisabgleich, Anzeigen). Ein
-- Trigger haelt Text und users.department_id synchron: wer die ID setzt,
-- bekommt den Namen; wer nur den Text schreibt (z. B. der Microsoft-Abgleich),
-- bekommt die passende Abteilung - fehlt sie, wird sie angelegt.

CREATE TABLE IF NOT EXISTS departments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    manager_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_departments_name ON departments (lower(name));

ALTER TABLE users ADD COLUMN IF NOT EXISTS department_id UUID REFERENCES departments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_users_department ON users(department_id);

-- vorhandene Freitext-Abteilungen uebernehmen
INSERT INTO departments (name)
SELECT DISTINCT ON (lower(btrim(department))) btrim(department)
  FROM users
 WHERE btrim(COALESCE(department, '')) <> ''
ORDER BY lower(btrim(department)), btrim(department)
ON CONFLICT DO NOTHING;

UPDATE users u SET department_id = d.id
  FROM departments d
 WHERE u.department_id IS NULL AND lower(btrim(u.department)) = lower(d.name);

CREATE OR REPLACE FUNCTION pdh_users_department_sync() RETURNS trigger AS $$
DECLARE
    dep_name TEXT;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.department_id IS DISTINCT FROM OLD.department_id THEN
        -- Abteilung per ID gewaehlt: Text nachziehen
        SELECT name INTO dep_name FROM departments WHERE id = NEW.department_id;
        NEW.department := COALESCE(dep_name, '');
    ELSIF TG_OP = 'INSERT' AND NEW.department_id IS NOT NULL THEN
        SELECT name INTO dep_name FROM departments WHERE id = NEW.department_id;
        NEW.department := COALESCE(dep_name, NEW.department);
    ELSIF TG_OP = 'INSERT' OR NEW.department IS DISTINCT FROM OLD.department THEN
        -- nur Text geschrieben: passende Abteilung suchen bzw. anlegen
        IF btrim(COALESCE(NEW.department, '')) = '' THEN
            NEW.department_id := NULL;
        ELSE
            NEW.department := btrim(NEW.department);
            SELECT id INTO NEW.department_id FROM departments WHERE lower(name) = lower(NEW.department);
            IF NEW.department_id IS NULL THEN
                INSERT INTO departments (name) VALUES (NEW.department) RETURNING id INTO NEW.department_id;
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_users_department_sync ON users;
CREATE TRIGGER trg_pdh_users_department_sync BEFORE INSERT OR UPDATE OF department, department_id ON users
    FOR EACH ROW EXECUTE FUNCTION pdh_users_department_sync();

-- Umbenennen einer Abteilung auf alle Mitarbeitenden uebertragen
CREATE OR REPLACE FUNCTION pdh_departments_rename() RETURNS trigger AS $$
BEGIN
    IF NEW.name IS DISTINCT FROM OLD.name THEN
        UPDATE users SET department = NEW.name WHERE department_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_departments_rename ON departments;
CREATE TRIGGER trg_pdh_departments_rename AFTER UPDATE OF name ON departments
    FOR EACH ROW EXECUTE FUNCTION pdh_departments_rename();

-- Gruppen (z. B. "Elektro-Team Frühschicht", "Ersthelfer")
CREATE TABLE IF NOT EXISTS user_groups (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          VARCHAR(100) NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    lead_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name ON user_groups (lower(name)) WHERE active;

CREATE TABLE IF NOT EXISTS user_group_members (
    group_id   UUID NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_user_group_members_user ON user_group_members(user_id);

-- Rollen gehoeren optional zu einer Abteilung
ALTER TABLE roles ADD COLUMN IF NOT EXISTS department_id UUID REFERENCES departments(id) ON DELETE SET NULL;
