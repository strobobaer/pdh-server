-- 026_infrastructure_cost_center_structured.up.sql
--
-- Stellt die Infrastruktur-Kostenstelle von Freitext (cost_center, aus
-- Migration 022) auf eine strukturierte Referenz zur cost_centers-Liste um
-- (Nummer + Name), damit Tickets/Störungen/Wartung die Kostenstelle einer
-- verknüpften Infrastruktur automatisch als Vorschlag übernehmen können.
--
-- Bestehende Freitext-Werte werden automatisch in echte cost_centers-
-- Einträge überführt (Nummer = Name = alter Text), damit keine Daten
-- verloren gehen. Die alte Spalte wird nur umbenannt, nicht gelöscht.

ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id);

DO $$
DECLARE
    rec RECORD;
    new_cc_id UUID;
    admin_user UUID;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'infrastructure' AND column_name = 'cost_center'
    ) THEN
        SELECT id INTO admin_user FROM users WHERE active = true ORDER BY created_at LIMIT 1;

        IF admin_user IS NOT NULL THEN
            FOR rec IN
                SELECT DISTINCT trim(cost_center) AS val
                FROM infrastructure
                WHERE cost_center IS NOT NULL AND trim(cost_center) <> ''
            LOOP
                SELECT id INTO new_cc_id FROM cost_centers WHERE number = rec.val LIMIT 1;
                IF new_cc_id IS NULL THEN
                    INSERT INTO cost_centers (number, name, created_by)
                    VALUES (rec.val, rec.val, admin_user)
                    RETURNING id INTO new_cc_id;
                END IF;

                UPDATE infrastructure
                SET cost_center_id = new_cc_id
                WHERE trim(cost_center) = rec.val;
            END LOOP;
        END IF;

        ALTER TABLE infrastructure RENAME COLUMN cost_center TO cost_center_legacy_text;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_infra_cost_center_id ON infrastructure(cost_center_id);
