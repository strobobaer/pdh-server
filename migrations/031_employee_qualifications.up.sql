-- 031_employee_qualifications.up.sql
--
-- Ankreuzbare Qualifikationen/Funktionen am Mitarbeiter, sichtbar z.B. im
-- Schichtplan:
-- - Bereitschaftsdienst: zeigt die PERSOENLICHE Handynummer (users.phone)
-- - Schichtschlosser 1/2: zeigt die FESTE Team-Handynummer des jeweils
--   zugeordneten Teams (unabhaengig davon, wer gerade markiert ist)
-- - Schaerferei, Heizungsbefuellung, Schichtleiter: reine Kennzeichnung
--
-- Teams sind eine eigene, erweiterbare Liste (Name + Telefonnummer), analog
-- zu den Kostenstellen - nicht auf genau zwei Eintraege begrenzt. Welches
-- Team die feste Nummer fuer "Schichtschlosser 1" bzw. "2" liefert, wird
-- separat zugeordnet (shift_locksmith_team_assignment) und kann jederzeit
-- geaendert werden, ohne dass sich an den Mitarbeiter-Kennzeichen etwas
-- aendert.

ALTER TABLE users ADD COLUMN IF NOT EXISTS on_call_duty       BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS shift_locksmith_1  BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS shift_locksmith_2  BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS sharpening         BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS heating_fill       BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS shift_leader       BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS shift_teams (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       VARCHAR(100) NOT NULL,
    phone      VARCHAR(50) NOT NULL DEFAULT '',
    active     BOOLEAN NOT NULL DEFAULT true,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Feste Zuordnung: welches Team liefert die Nummer fuer Schichtschlosser 1/2.
CREATE TABLE IF NOT EXISTS shift_locksmith_team_assignment (
    slot    INT PRIMARY KEY CHECK (slot IN (1, 2)),
    team_id UUID REFERENCES shift_teams(id)
);

INSERT INTO shift_locksmith_team_assignment (slot, team_id) VALUES (1, NULL), (2, NULL)
ON CONFLICT (slot) DO NOTHING;
