-- 213_plant_work_times.up.sql
-- Arbeitszeiten je Anlage: je Wochentag eine oder mehrere Schichten (Beginn, Ende; das Ende
-- darf nach Mitternacht liegen) mit beliebig vielen Pausen, dazu Ausnahmen je Datum
-- (geschlossen oder abweichende Schichten). Die Zeiten werden wie die Rüsttasten nach unten
-- vererbt: Eine Anlage ohne eigene Zeiten nutzt die der nächsten übergeordneten Anlage.
-- Rein hinzufügend: keine bestehende Tabelle wird verändert.

-- Eine Zeile = die Anlage hat eigene Zeiten (auch wenn sie keine einzige Schicht enthalten,
-- also „nie Arbeitszeit“). Ohne Zeile gilt die nächste übergeordnete Anlage mit Zeile.
CREATE TABLE IF NOT EXISTS plant_work_times (
    infrastructure_id UUID PRIMARY KEY REFERENCES infrastructure(id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Ausnahme für einen Kalendertag: geschlossen oder mit eigenen Schichten (plant_shifts.exception_id).
CREATE TABLE IF NOT EXISTS plant_work_exceptions (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    day               DATE NOT NULL,
    closed            BOOLEAN NOT NULL DEFAULT true,
    note              VARCHAR(80) NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (infrastructure_id, day)
);

-- Schicht: entweder an einem Wochentag (1 = Montag … 7 = Sonntag) oder an einer Ausnahme.
-- end_time <= start_time heißt: die Schicht endet am Folgetag (über Mitternacht).
CREATE TABLE IF NOT EXISTS plant_shifts (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    weekday           SMALLINT CHECK (weekday BETWEEN 1 AND 7),
    exception_id      UUID REFERENCES plant_work_exceptions(id) ON DELETE CASCADE,
    name              VARCHAR(60) NOT NULL DEFAULT '',
    start_time        TIME NOT NULL,
    end_time          TIME NOT NULL,
    sort              INT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((weekday IS NULL) <> (exception_id IS NULL)),
    CHECK (start_time <> end_time)
);
CREATE INDEX IF NOT EXISTS idx_plant_shifts_infra ON plant_shifts (infrastructure_id);
CREATE INDEX IF NOT EXISTS idx_plant_shifts_exception ON plant_shifts (exception_id);

-- Pause innerhalb einer Schicht (liegt die Schicht über Mitternacht, darf die Pause es auch).
CREATE TABLE IF NOT EXISTS plant_shift_breaks (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    shift_id   UUID NOT NULL REFERENCES plant_shifts(id) ON DELETE CASCADE,
    start_time TIME NOT NULL,
    end_time   TIME NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (start_time <> end_time)
);
CREATE INDEX IF NOT EXISTS idx_plant_shift_breaks_shift ON plant_shift_breaks (shift_id);
