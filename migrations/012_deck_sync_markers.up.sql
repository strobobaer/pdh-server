-- 012_deck_sync_markers.up.sql
-- Idempotency table for PDH -> Nextcloud Deck mirroring jobs.

CREATE TABLE IF NOT EXISTS deck_sync_markers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type        VARCHAR(50) NOT NULL,
    ref_id          UUID NOT NULL,
    destination     VARCHAR(50) NOT NULL DEFAULT 'nextcloud_deck',
    external_id     VARCHAR(100),
    external_url    TEXT,
    synced_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    UNIQUE(ref_type, ref_id, destination)
);

CREATE INDEX IF NOT EXISTS idx_deck_sync_markers_ref
    ON deck_sync_markers(ref_type, ref_id);
