-- 066_infrastructure_comments.up.sql
--
-- Kommentare direkt an einer Infrastruktur-Anlage (Stammkarte). Ein
-- Kommentar kann optional auf einen Eintrag der Anlagen-Historie verweisen
-- (Stoerung, Ticket, Aufgabe, Wartungsauftrag, Projekt), damit Hinweise
-- wie "Lager tauschen wir beim naechsten Stillstand" am richtigen
-- Vorgang haengen und trotzdem in der Anlagen-Historie auftauchen.
--
-- ref_type: fault | ticket | task | maintenance_task | project (oder NULL)

CREATE TABLE IF NOT EXISTS infrastructure_comments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    ref_type          VARCHAR(50),
    ref_id            UUID,
    text              TEXT NOT NULL,
    pinned            BOOLEAN NOT NULL DEFAULT false,
    created_by        UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_infra_comments_infra ON infrastructure_comments(infrastructure_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_infra_comments_ref ON infrastructure_comments(ref_type, ref_id);

-- Historien-Abfragen der Stammkarte laufen ueber infrastructure_id bzw.
-- ueber die Vorgangs-Fremdschluessel der Lagerbewegungen.
CREATE INDEX IF NOT EXISTS idx_projects_infrastructure ON projects(infrastructure_id);
