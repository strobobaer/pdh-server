-- 184_network_sources.up.sql
--
-- Seite „Netzwerk“: Vorschläge aus UniFi und Omada.
--
-- 1. Herkunft der Einträge: net_segments und net_wlans merken, aus welchem
--    Controller (source = 'unifi' oder 'omada') und mit welcher Kennung beim
--    Controller (ext_id) ein Eintrag übernommen wurde. source = '' und
--    adopted_at IS NULL bedeuten: von Hand angelegt. Je Controller gibt es eine
--    Kennung nur einmal.
-- 2. net_hidden_suggestions: Vorschläge, die der Anwender ausgeblendet hat.
--    Der Fingerabdruck stammt nur aus nicht geheimen Feldern; ändert sich der
--    Vorschlag am Controller, erscheint er wieder. WLAN-Kennwörter stehen nie
--    in dieser Tabelle.
ALTER TABLE net_segments
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '' CHECK (source IN ('', 'unifi', 'omada')),
    ADD COLUMN IF NOT EXISTS ext_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS adopted_at TIMESTAMPTZ;
ALTER TABLE net_wlans
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '' CHECK (source IN ('', 'unifi', 'omada')),
    ADD COLUMN IF NOT EXISTS ext_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS adopted_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS net_segments_source_ext_uq ON net_segments (source, ext_id) WHERE ext_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS net_wlans_source_ext_uq ON net_wlans (source, ext_id) WHERE ext_id <> '';

CREATE TABLE IF NOT EXISTS net_hidden_suggestions (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    source      TEXT NOT NULL CHECK (source IN ('unifi', 'omada')),
    kind        TEXT NOT NULL CHECK (kind IN ('segment', 'wlan')),
    ext_id      TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    hidden_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    hidden_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source, kind, ext_id)
);
