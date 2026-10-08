-- Prüfpflichten und Gefährdungsbeurteilungen je Anlage (infra_obligations.go)
--
-- kind 'inspection'       wiederkehrende Prüfung (BetrSichV, DGUV V3, UVV, Druckbehälter …)
-- kind 'risk_assessment'  Gefährdungsbeurteilung (ArbSchG §5) mit Überprüfungsintervall
--
-- next_due ergibt sich beim Erfassen einer Durchführung aus done_on + interval_months.
-- Die Erinnerung geht remind_days vor next_due an responsible_to (sonst an die
-- Admins) und noch einmal bei Überfälligkeit; reminded_due/overdue_reminded_due
-- merken sich, für welche Fälligkeit schon erinnert wurde.

CREATE TABLE IF NOT EXISTS infra_obligations (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    infrastructure_id    UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    kind                 VARCHAR(20) NOT NULL CHECK (kind IN ('inspection', 'risk_assessment')),
    title                VARCHAR(200) NOT NULL,
    basis                VARCHAR(200) NOT NULL DEFAULT '',
    inspector            VARCHAR(200) NOT NULL DEFAULT '',
    interval_months      INT NOT NULL DEFAULT 12 CHECK (interval_months BETWEEN 0 AND 240),
    last_done            DATE,
    next_due             DATE,
    remind_days          INT NOT NULL DEFAULT 30 CHECK (remind_days BETWEEN 0 AND 365),
    responsible_to       UUID REFERENCES users(id) ON DELETE SET NULL,
    notes                TEXT NOT NULL DEFAULT '',
    reminded_due         DATE,
    overdue_reminded_due DATE,
    created_by           UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_infra_obligations_infra ON infra_obligations(infrastructure_id);
CREATE INDEX IF NOT EXISTS idx_infra_obligations_due ON infra_obligations(next_due);

-- Nachweis: jede Durchführung mit Ergebnis
CREATE TABLE IF NOT EXISTS infra_obligation_log (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    obligation_id UUID NOT NULL REFERENCES infra_obligations(id) ON DELETE CASCADE,
    done_on       DATE NOT NULL,
    result        VARCHAR(20) NOT NULL DEFAULT 'ok' CHECK (result IN ('ok', 'defects')),
    note          TEXT NOT NULL DEFAULT '',
    done_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_infra_obligation_log_ob ON infra_obligation_log(obligation_id, done_on DESC);
