-- 118_drying_chamber.up.sql
-- Anlagentyp „Trockenkammer“ mit Reiter „Bauteile“ (internal/web/chamber.go).
--
-- infra_components: je Bauteilgruppe (Motoren, Klappenmotoren, Heizungsstellventile,
-- Heizungspumpen, Torrollen, Tordichtungen) und Position eine Kachel mit Typ
-- (Ersatzteil), NOK, letztem Wechsel und Ursache. Die Anzahl je Gruppe ergibt
-- sich aus den Positionen 1..n.

-- neuer Enum-Wert; wird in dieser Migration nicht benutzt (Transaktion)
ALTER TYPE infra_type ADD VALUE IF NOT EXISTS 'drying_chamber';

CREATE TABLE IF NOT EXISTS infra_components (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    kind              VARCHAR(20) NOT NULL CHECK (kind IN ('motor', 'flap_motor', 'heating_valve', 'heating_pump', 'door_roller', 'door_seal')),
    position          INT NOT NULL CHECK (position BETWEEN 1 AND 99),
    spare_part_id     UUID REFERENCES spare_parts(id) ON DELETE SET NULL,
    nok               BOOLEAN NOT NULL DEFAULT false,
    last_change       DATE,
    cause             TEXT NOT NULL DEFAULT '',
    updated_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (infrastructure_id, kind, position)
);
CREATE INDEX IF NOT EXISTS idx_infra_components_part ON infra_components(spare_part_id) WHERE spare_part_id IS NOT NULL;
