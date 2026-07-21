-- 023_time_entries_infrastructure.up.sql
--
-- Ordnet Zeiteinträge zusätzlich einem Infrastruktur-Knoten zu (der als
-- "Kostenstelle" dient - Gebäude/Linie/Anlage/Gerät). Unabhängig von
-- ref_type/ref_id, das weiterhin "welche Aufgabe" (Störung/Ticket/Wartung/
-- Sonstiges) beschreibt. Nullable, weil bestehende Einträge keine Kostenstelle
-- haben und das nicht rückwirkend erzwungen werden soll.

ALTER TABLE time_entries
    ADD COLUMN IF NOT EXISTS infrastructure_id UUID REFERENCES infrastructure(id);

CREATE INDEX IF NOT EXISTS idx_time_infra ON time_entries(infrastructure_id);
