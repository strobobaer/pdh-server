-- 219_energy_rebases.up.sql
-- Energiedaten (0.82.54): Zeitpunkte, an denen beim Speichern die Basis
-- berechneter Messstellen neu gesetzt wurde (Quelle, Faktor, Art oder Formel
-- eines Bezugs geändert). Der Minutentakt gibt in dieser Minute keinen Wert,
-- damit der Sprung zwischen altem und neuem Wert nicht als Verbrauch zählt –
-- die rückwirkende Neurechnung hält sich an dieselben Zeitpunkte. Aufgeräumt
-- wird mit den Minutenwerten (Aufbewahrung).
CREATE TABLE IF NOT EXISTS energy_rebases (
    meter_id UUID NOT NULL REFERENCES energy_meters(id) ON DELETE CASCADE,
    at       TIMESTAMPTZ NOT NULL,   -- Minutenende ohne Wert, ab hier zählt der Zuwachs
    PRIMARY KEY (meter_id, at)
);
