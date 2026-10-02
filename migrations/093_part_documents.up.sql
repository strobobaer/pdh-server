-- Datenblaetter und Benutzerhandbuecher von Ersatzteilen aus dem Internet:
-- Herkunft merken, damit sie spaeter erneut abgerufen (aktualisiert) werden.
ALTER TABLE attachments
    ADD COLUMN IF NOT EXISTS doc_kind          VARCHAR(20),  -- 'datasheet' | 'manual'
    ADD COLUMN IF NOT EXISTS source_url        TEXT,
    ADD COLUMN IF NOT EXISTS source_sha256     VARCHAR(64),
    ADD COLUMN IF NOT EXISTS source_checked_at TIMESTAMPTZ;
