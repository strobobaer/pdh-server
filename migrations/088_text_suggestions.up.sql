-- 088_text_suggestions.up.sql
-- Lernende Autovervollstaendigung: Die Vorschlaege entstehen aus den
-- gespeicherten Texten (Titel, Beschreibungen, Massnahmen, Ursachen,
-- Loesungen, Schulungsinhalte). Hier wird zusaetzlich gezaehlt, welche
-- Vorschlaege tatsaechlich uebernommen wurden – die steigen in der Rangfolge.

CREATE TABLE IF NOT EXISTS text_suggestion_feedback (
    kind        VARCHAR(30)  NOT NULL,
    phrase_key  VARCHAR(300) NOT NULL, -- normalisiert (Kleinschreibung, Umlaute)
    phrase      VARCHAR(300) NOT NULL,
    accepted    INT NOT NULL DEFAULT 1,
    last_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (kind, phrase_key)
);
