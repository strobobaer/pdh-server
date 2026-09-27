-- 061_export_templates.up.sql
--
-- Echte Vorlagenverwaltung fuer PDF-/Excel-Exporte: ersetzt das bisherige
-- freie "Vorlage"-Textfeld (nur als PDF-Titel genutzt) durch benannte,
-- wiederverwendbare Vorlagen mit tatsaechlichem Einfluss auf die Ausgabe
-- (PDF: Titel/Ausrichtung; Excel: Tabellenblattname). Eine Export-
-- Verbindung waehlt per template_id (statt Freitext) eine Vorlage aus -
-- fehlt sie, greifen Standardwerte (kein Bruch fuer bestehende
-- Verbindungen ohne Auswahl).

CREATE TABLE IF NOT EXISTS export_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        VARCHAR(20) NOT NULL CHECK (kind IN ('pdf', 'excel')),
    name        VARCHAR(150) NOT NULL,
    title       VARCHAR(200) NOT NULL DEFAULT '',
    sheet_name  VARCHAR(100) NOT NULL DEFAULT '',
    orientation VARCHAR(10) NOT NULL DEFAULT 'P',
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_export_templates_kind ON export_templates (kind);
