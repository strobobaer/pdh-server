-- 065_record_external_parties.up.sql
--
-- Externe Beteiligte (Fremdfirmen und externe Mitarbeiter) an Aufgaben,
-- Tickets, Stoerungen, Projekten und Wartungen. Jeder Eintrag gehoert zu
-- genau einer Rolle: 'responsible' (Verantwortlich) oder 'assigned'
-- (Zustaendig). Interne Mitarbeiter bleiben in den bisherigen Spalten
-- (assigned_to / responsible_to / task_assignees), der Ersteller in
-- created_by - diese Tabelle ergaenzt sie nur um externe Parteien.
--
-- ref_type: ticket | fault | maintenance_task | task | project
-- kind:     company (Externe Firma) | person (Externer Mitarbeiter)
-- partner_id verweist optional auf das Partnerverzeichnis (064).

CREATE TABLE IF NOT EXISTS record_external_parties (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type   VARCHAR(50) NOT NULL,
    ref_id     UUID NOT NULL,
    role       VARCHAR(20) NOT NULL CHECK (role IN ('responsible', 'assigned')),
    kind       VARCHAR(20) NOT NULL CHECK (kind IN ('company', 'person')),
    name       VARCHAR(200) NOT NULL,
    company    VARCHAR(200) NOT NULL DEFAULT '',
    contact    VARCHAR(300) NOT NULL DEFAULT '',
    partner_id UUID REFERENCES business_partners(id) ON DELETE SET NULL,
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_record_external_parties_ref ON record_external_parties(ref_type, ref_id);
