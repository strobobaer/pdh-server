-- Live-Übersetzung (internal/web/live_translate.go): je Benutzer an/aus und
-- Zielsprache (leer = Sprache der Oberfläche). Übersetzte Texte werden je
-- Zielsprache zwischengespeichert, damit jeder Text nur einmal übersetzt wird.
ALTER TABLE users ADD COLUMN IF NOT EXISTS live_translate BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS translate_lang VARCHAR(10) NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS translation_cache (
    lang       VARCHAR(10) NOT NULL,
    hash       CHAR(64)    NOT NULL, -- sha256 des Ausgangstexts
    source     TEXT        NOT NULL,
    text       TEXT        NOT NULL,
    provider   VARCHAR(20) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (lang, hash)
);
CREATE INDEX IF NOT EXISTS idx_translation_cache_used ON translation_cache(used_at);
