-- 218_energy_formula_versions.up.sql
-- Energiedaten (0.82.54): Fassungen der Formel berechneter Messstellen mit
-- „gültig ab“, rückwirkende Neurechnungen und der Zeitbereich der Efficio-Dateien.

-- ── Fassungen der Formel ──────────────────────────────────────────────────
-- Es gilt die Fassung mit dem spätesten valid_from, das nicht nach dem Tag
-- liegt; am selben Tag die zuletzt angelegte. energy_meters.formula bleibt die
-- geltende Formel, der Minutentakt stellt sie auf die Fassung des Tages.
CREATE TABLE IF NOT EXISTS energy_formula_versions (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    meter_id        UUID NOT NULL REFERENCES energy_meters(id) ON DELETE CASCADE,
    valid_from      DATE NOT NULL,
    formula         VARCHAR(2000) NOT NULL CHECK (formula <> ''),
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by_name TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_energy_formula_versions_meter
    ON energy_formula_versions (meter_id, valid_from, created_at);

-- ── Neurechnungen ─────────────────────────────────────────────────────────
-- Ein Lauf rechnet die Minutenwerte der Messstellen meter_ids ab from_date
-- Tag für Tag neu (done_until = zuletzt geschriebener Tag). Solange er
-- läuft (running), lässt der Minutentakt diese Messstellen aus; in exporting
-- erzeugt er die nicht verschickten Efficio-Dateien neu.
CREATE TABLE IF NOT EXISTS energy_recalc_runs (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    meter_id        UUID REFERENCES energy_meters(id) ON DELETE SET NULL,
    meter_ids       UUID[] NOT NULL,
    meter_keys      TEXT[] NOT NULL,
    from_date       DATE NOT NULL,
    done_until      DATE,
    days_total      INT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('running', 'exporting', 'done', 'failed')),
    attempts        INT NOT NULL DEFAULT 0,
    message         TEXT NOT NULL DEFAULT '',
    values_written  BIGINT NOT NULL DEFAULT 0,
    missing         BIGINT NOT NULL DEFAULT 0,
    files_rebuilt   TEXT NOT NULL DEFAULT '',
    files_kept      TEXT NOT NULL DEFAULT '',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by_name TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ
);
-- Höchstens ein Lauf gleichzeitig.
CREATE UNIQUE INDEX IF NOT EXISTS uq_energy_recalc_runs_active
    ON energy_recalc_runs ((true)) WHERE state IN ('running', 'exporting');

-- ── Zeitbereich der Efficio-Dateien ───────────────────────────────────────
-- first_end/last_end: erstes und letztes Intervallende in der Datei (für das
-- Neuerzeugen nach einer Neurechnung); fetched_at: erster Download beim Ziel
-- „Zum Abholen im PDH“ (ab dann gilt die Datei als verschickt).
ALTER TABLE energy_export_files ADD COLUMN IF NOT EXISTS first_end TIMESTAMPTZ;
ALTER TABLE energy_export_files ADD COLUMN IF NOT EXISTS last_end TIMESTAMPTZ;
ALTER TABLE energy_export_files ADD COLUMN IF NOT EXISTS fetched_at TIMESTAMPTZ;

-- ── Erste Fassung je berechneter Messstelle ───────────────────────────────
-- Für die Zeit vor dem Update kennt das PDH nur die damals geltende Formel.
INSERT INTO energy_formula_versions (meter_id, valid_from, formula, created_by_name, created_at)
SELECT id, created_at::date, formula, 'Stand beim Update', created_at FROM energy_meters m
 WHERE formula <> '' AND NOT EXISTS (SELECT 1 FROM energy_formula_versions v WHERE v.meter_id = m.id);
