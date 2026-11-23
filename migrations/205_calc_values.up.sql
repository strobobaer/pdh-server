-- 205_calc_values.up.sql
-- Berechnete Werte (virtuelle Messwerte): ein berechneter Wert ist eine
-- Zuordnung an der fest eingebauten Import-Verbindung „Berechnete Werte“.
-- Formel und Eingangswerte liegen in Fassungen mit „gültig ab“.

-- Eingebaute Verbindung: genau eine, immer vorhanden.
INSERT INTO import_export_connections (direction, kind, name, config, enabled)
SELECT 'import', 'calc', 'Berechnete Werte', '{}'::jsonb, true
WHERE NOT EXISTS (SELECT 1 FROM import_export_connections WHERE kind = 'calc');

CREATE UNIQUE INDEX IF NOT EXISTS uq_import_export_connections_calc
    ON import_export_connections (kind) WHERE kind = 'calc';

-- Angaben zum berechneten Wert (1:1 zur Zuordnung; Bezeichnung, Anlage und
-- letzter Wert stehen an der Zuordnung selbst).
CREATE TABLE calc_values (
    mapping_id      UUID PRIMARY KEY REFERENCES import_mappings(id) ON DELETE CASCADE,
    unit            VARCHAR(20) NOT NULL DEFAULT '',
    decimals        SMALLINT NOT NULL DEFAULT 2 CHECK (decimals BETWEEN 0 AND 6),
    -- NULL = das Alter der Eingangswerte wird nicht geprüft
    max_age_minutes INTEGER CHECK (max_age_minutes BETWEEN 1 AND 44640),
    -- Grund, warum es gerade keinen Wert gibt (leer = Wert da)
    reason          TEXT NOT NULL DEFAULT '',
    calculated_at   TIMESTAMPTZ
);

-- Fassungen der Formel. Es gilt die Fassung mit dem spätesten valid_from, das
-- nicht in der Zukunft liegt; bei gleichem Datum die zuletzt angelegte.
CREATE TABLE calc_versions (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    mapping_id      UUID NOT NULL REFERENCES calc_values(mapping_id) ON DELETE CASCADE,
    valid_from      DATE NOT NULL,
    formula         VARCHAR(2000) NOT NULL,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by_name TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_calc_versions_value ON calc_versions (mapping_id, valid_from, created_at);

-- Eingangswerte je Fassung: Kurzname in der Formel → Zuordnung. Wird die
-- Zuordnung gelöscht, bleibt die Zeile mit ihrer Bezeichnung (label) stehen.
CREATE TABLE calc_inputs (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    version_id       UUID NOT NULL REFERENCES calc_versions(id) ON DELETE CASCADE,
    short_name       VARCHAR(20) NOT NULL,
    input_mapping_id UUID REFERENCES import_mappings(id) ON DELETE SET NULL,
    label            TEXT NOT NULL
);
CREATE UNIQUE INDEX uq_calc_inputs_short ON calc_inputs (version_id, lower(short_name));
CREATE INDEX idx_calc_inputs_mapping ON calc_inputs (input_mapping_id) WHERE input_mapping_id IS NOT NULL;
