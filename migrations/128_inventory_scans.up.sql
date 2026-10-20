-- 128_inventory_scans.up.sql
-- Ersatzteile per Smartphone erfassen (internal/web/part_scan.go):
--   * delivery: Lieferschein fotografiert/als PDF – Positionen erkannt, daraus
--     Zugänge zu vorhandenen Teilen bzw. Neuanlagen vorgeschlagen
--   * part: Bauteil fotografiert – erkannt und mit dem Bestand abgeglichen
-- Fotos/PDFs liegen unter uploads/inventory-scans/<id>/, das erkannte Ergebnis als JSON.

CREATE TABLE IF NOT EXISTS inventory_scans (
    id            UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    kind          VARCHAR(10) NOT NULL CHECK (kind IN ('delivery', 'part')),
    status        VARCHAR(10) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'error')),
    error         TEXT NOT NULL DEFAULT '',        -- Grund, wenn die Erkennung scheiterte
    files         JSONB NOT NULL DEFAULT '[]',   -- [{"path":"inventory-scans/…","name":"…","type":"image/jpeg"}]
    result        JSONB NOT NULL DEFAULT '{}',   -- Antwort der Bilderkennung
    supplier_id   UUID REFERENCES business_partners(id) ON DELETE SET NULL,
    note_no       VARCHAR(100) NOT NULL DEFAULT '', -- Lieferschein-Nr.
    applied_at    TIMESTAMPTZ,                     -- Vorschläge übernommen
    applied_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    applied_note  TEXT NOT NULL DEFAULT '',        -- z. B. „5 Zugänge, 2 neue Teile“
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_inventory_scans_created ON inventory_scans(created_at DESC);
