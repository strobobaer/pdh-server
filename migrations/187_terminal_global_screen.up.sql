-- 187_terminal_global_screen.up.sql
--
-- Globales Terminal (Infobildschirm, W2+G): Ein Terminal-Benutzer mit
-- terminal_settings.global_screen zeigt unter /terminal die Folien aus
-- terminal_slides, ohne Anmeldung und ohne Kartenleser. Bestehende Terminals
-- behalten den Wert false; ältere PDH-Stände und Terminals kennen Spalte und
-- Tabelle nicht und bleiben unberührt.
--
-- terminal_slides: Folienarten image (Bild in der Datenbank, JPEG/PNG/WebP,
-- höchstens 5 MB), text (höchstens 2000 Zeichen, kein HTML) und vorgaenge
-- (Anlagen samt Unteranlagen, leer = alle; Störungen/Tickets ein/aus).
-- Dauer je Folie 5 bis 600 Sekunden.

ALTER TABLE terminal_settings
    ADD COLUMN IF NOT EXISTS global_screen BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS terminal_slides (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    position         INT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('image', 'text', 'vorgaenge')),
    title            TEXT NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    body             TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 2000),
    image            BYTEA CHECK (image IS NULL OR octet_length(image) <= 5242880),
    image_type       TEXT NOT NULL DEFAULT '' CHECK (image_type IN ('', 'image/jpeg', 'image/png', 'image/webp')),
    infra_ids        UUID[] NOT NULL DEFAULT '{}',
    show_faults      BOOLEAN NOT NULL DEFAULT true,
    show_tickets     BOOLEAN NOT NULL DEFAULT true,
    duration_seconds INT NOT NULL DEFAULT 15 CHECK (duration_seconds BETWEEN 5 AND 600),
    active           BOOLEAN NOT NULL DEFAULT true,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_terminal_slides_position ON terminal_slides (position);

INSERT INTO permissions (key, label, category) VALUES
    ('terminal.slides.edit', 'Infobildschirm bearbeiten', 'Infrastruktur & Anlagen')
ON CONFLICT (key) DO NOTHING;

-- Neues Recht: Startbelegung nur Admin und Manager (bisher gab es die Seite nicht).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE p.key = 'terminal.slides.edit' AND r.key IN ('admin', 'manager')
ON CONFLICT DO NOTHING;
