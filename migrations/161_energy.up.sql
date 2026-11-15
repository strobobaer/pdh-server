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

-- ── Minutenwerte ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS energy_values (
    meter_id UUID NOT NULL REFERENCES energy_meters(id) ON DELETE CASCADE,
    ts       TIMESTAMPTZ NOT NULL,
    value    DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (meter_id, ts)
);

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
