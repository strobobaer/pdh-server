-- 161_energy.up.sql
-- Energiedaten (0.80.12): Messstellen, Minutenwerte und Export an Berg Efficio.
-- Eine Messstelle gehört zu einer Anlage und bezieht ihren Wert entweder aus
-- einer Import-Zuordnung (Quelle) oder aus einer Formel über andere Messstellen.
-- Zusatzfunktion: Ohne Messstellen bleiben die Tabellen leer.

-- ── Messstellen ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS energy_meters (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    meter_key         VARCHAR(64) NOT NULL UNIQUE,
    name              VARCHAR(150) NOT NULL,
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id),
    import_mapping_id UUID REFERENCES import_mappings(id) ON DELETE SET NULL,
    formula           VARCHAR(2000) NOT NULL DEFAULT '',
    unit              VARCHAR(20) NOT NULL DEFAULT '',
    kind              TEXT NOT NULL DEFAULT 'counter' CHECK (kind IN ('counter', 'consumption', 'instant')),
    factor            DOUBLE PRECISION NOT NULL DEFAULT 1 CHECK (factor <> 0),
    export            BOOLEAN NOT NULL DEFAULT true,
    active            BOOLEAN NOT NULL DEFAULT true,
    last_value        DOUBLE PRECISION,
    last_at           TIMESTAMPTZ,
    -- Basiszeitpunkt berechneter Zählermessstellen: ab hier zählt der Zuwachs der Bezüge.
    base_at           TIMESTAMPTZ,
    created_by        UUID REFERENCES users(id),
    updated_by        UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Entweder Quelle oder Formel, nie beides.
    CHECK (formula = '' OR import_mapping_id IS NULL)
);
CREATE INDEX IF NOT EXISTS idx_energy_meters_infra ON energy_meters (infrastructure_id);
-- Für die Lesungs-Erfassung (Trigger unten) und das Löschen einer Zuordnung.
CREATE INDEX IF NOT EXISTS idx_energy_meters_mapping ON energy_meters (import_mapping_id) WHERE import_mapping_id IS NOT NULL;

-- ── Minutenwerte ──────────────────────────────────────────────────────────
-- ts ist der Minutenanfang: der Wert gilt für die Minute [ts, ts + 1 min).
CREATE TABLE IF NOT EXISTS energy_values (
    meter_id UUID NOT NULL REFERENCES energy_meters(id) ON DELETE CASCADE,
    ts       TIMESTAMPTZ NOT NULL,
    value    DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (meter_id, ts)
);
-- Für das tägliche Aufräumen (DELETE … WHERE ts < …). Die Werte kommen
-- zeitlich geordnet herein, ein BRIN-Index bleibt dafür sehr klein.
CREATE INDEX IF NOT EXISTS idx_energy_values_ts ON energy_values USING brin (ts);

-- ── Lesungen der Quellen ──────────────────────────────────────────────────
-- Jede Lesung einer Import-Zuordnung, die eine aktive Messstelle speist, wird
-- hier festgehalten (Trigger auf import_mappings). Eine Zuordnung hält selbst
-- nur die letzte Lesung; so geht keine verloren, auch wenn die Quelle öfter
-- als einmal je Minute liefert. counted: bei Verbrauch schon gezählt.
-- Der Minutentakt löscht Lesungen, die nicht mehr gebraucht werden.
CREATE TABLE IF NOT EXISTS energy_readings (
    id          BIGSERIAL PRIMARY KEY,
    mapping_id  UUID NOT NULL REFERENCES import_mappings(id) ON DELETE CASCADE,
    received_at TIMESTAMPTZ NOT NULL,
    raw         TEXT NOT NULL,
    counted     BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_energy_readings_mapping ON energy_readings (mapping_id, received_at);
CREATE INDEX IF NOT EXISTS idx_energy_readings_received ON energy_readings (received_at);

CREATE OR REPLACE FUNCTION energy_capture_reading() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.last_received_at IS NOT NULL AND NEW.last_value IS NOT NULL
       AND (NEW.last_received_at IS DISTINCT FROM OLD.last_received_at
            OR NEW.last_value IS DISTINCT FROM OLD.last_value)
       AND EXISTS (SELECT 1 FROM energy_meters
                    WHERE import_mapping_id = NEW.id AND active) THEN
        INSERT INTO energy_readings (mapping_id, received_at, raw)
        VALUES (NEW.id, NEW.last_received_at, NEW.last_value);
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_energy_capture_reading ON import_mappings;
CREATE TRIGGER trg_energy_capture_reading
    AFTER UPDATE OF last_value, last_received_at ON import_mappings
    FOR EACH ROW EXECUTE FUNCTION energy_capture_reading();

-- ── Efficio-Export ────────────────────────────────────────────────────────
-- Bis wohin je Verbindung schon exportiert wurde.
CREATE TABLE IF NOT EXISTS energy_export_state (
    connection_id     UUID PRIMARY KEY REFERENCES import_export_connections(id) ON DELETE CASCADE,
    last_interval_end TIMESTAMPTZ NOT NULL
);

-- Erzeugte CSV-Dateien (Tagesdateien) samt Zustellstand.
CREATE TABLE IF NOT EXISTS energy_export_files (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    connection_id     UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    name              VARCHAR(200) NOT NULL,
    period_start      TIMESTAMPTZ NOT NULL,
    period_end        TIMESTAMPTZ NOT NULL,
    content           BYTEA NOT NULL,
    rows              INT NOT NULL DEFAULT 0,
    version           INT NOT NULL DEFAULT 1,
    delivered_version INT NOT NULL DEFAULT 0,
    delivered_at      TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, name)
);

-- Protokoll der Exportläufe.
CREATE TABLE IF NOT EXISTS energy_export_runs (
    id            BIGSERIAL PRIMARY KEY,
    connection_id UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ,
    intervals     INT NOT NULL DEFAULT 0,
    rows          INT NOT NULL DEFAULT 0,
    missing       INT NOT NULL DEFAULT 0,
    target        TEXT NOT NULL DEFAULT '',
    files         TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL CHECK (result IN ('ok', 'partial', 'error', 'nothing')),
    message       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_energy_export_runs_conn ON energy_export_runs (connection_id, started_at DESC);

-- ── Rechte ────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('energy.view',  'Messstellen sehen', 'Daten & Schnittstellen'),
    ('energy.edit',  'Messstellen pflegen', 'Daten & Schnittstellen'),
    ('energy.admin', 'Messstellen löschen (Bereichsadministrator)', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Wer bisher Export ansehen darf, darf Messstellen sehen; wer Export verwalten
-- darf, darf sie auch pflegen. Die Abteilung der Quellrolle wird übernommen –
-- niemand wird durch das Update ausgesperrt, niemand bekommt mehr als bisher.
-- Gilt ein Quellrecht überall (NULL) und ein anderes nur in einer Abteilung,
-- gewinnt „überall“ (je Rolle und Recht gibt es nur eine Zeile).
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'export.read'
  CROSS JOIN permissions p
 WHERE p.key = 'energy.view'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'export.write'
  CROSS JOIN permissions p
 WHERE p.key IN ('energy.view', 'energy.edit')
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

-- Bereichsadministrator (einziges Löschrecht): Rolle admin und Rollen, die
-- Export global verwalten dürfen und schon in einem anderen Bereich
-- Bereichsadministrator sind (wie setup.admin in Migration 159).
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'energy.admin'
  AND (r.key = 'admin'
       OR (EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'export.write')
           AND EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND o.key LIKE '%.admin' AND o.key <> 'energy.admin')))
ON CONFLICT DO NOTHING;
