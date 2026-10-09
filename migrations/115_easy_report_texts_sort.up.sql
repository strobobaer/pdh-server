-- 115_easy_report_texts_sort.up.sql
-- Eigene Reihenfolge der Easy-Mode-Meldetexte (Katalog ▲/▼, easy_texts.go).
-- Bestehende Texte starten alphabetisch.

ALTER TABLE easy_report_texts ADD COLUMN IF NOT EXISTS sort INTEGER NOT NULL DEFAULT 0;

UPDATE easy_report_texts t SET sort = x.rn
FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY lower(text)) AS rn FROM easy_report_texts) x
WHERE t.id = x.id AND t.sort = 0;
