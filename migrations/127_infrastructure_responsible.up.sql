-- 127_infrastructure_responsible.up.sql
-- Verantwortliche/r je Infrastruktur-Element; ohne eigenen Eintrag gilt die
-- Person des nächsten übergeordneten Elements (wie bei der Abteilung, 087).
-- Sicht infrastructure_responsible: wirksame Person und woher sie kommt.

ALTER TABLE infrastructure
    ADD COLUMN IF NOT EXISTS responsible_to UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_infrastructure_responsible ON infrastructure(responsible_to) WHERE responsible_to IS NOT NULL;

CREATE OR REPLACE VIEW infrastructure_responsible AS
WITH RECURSIVE chain AS (
    SELECT i.id, i.responsible_to, i.parent_id, i.id AS source_id, 0 AS depth FROM infrastructure i
    UNION ALL
    SELECT c.id, p.responsible_to, p.parent_id, p.id, c.depth + 1
      FROM chain c JOIN infrastructure p ON p.id = c.parent_id
     WHERE c.responsible_to IS NULL AND c.depth < 50
)
SELECT DISTINCT ON (id) id AS infrastructure_id, responsible_to, source_id
  FROM chain
 WHERE responsible_to IS NOT NULL
 ORDER BY id, depth;
