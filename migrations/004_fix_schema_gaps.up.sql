-- 004_fix_schema_gaps.up.sql
--
-- Behebt zwei Probleme:
-- 1) 001_core_schema.up.sql und 003_shifts_schema.up.sql widersprechen sich:
--    Falls 001 zuerst gelaufen ist, existieren shift_models/shift_definitions/
--    shift_assignments bereits OHNE die Spalten, die 003 (und der Go-Code)
--    erwartet. "CREATE TABLE IF NOT EXISTS" in 003 tut in dem Fall nichts,
--    darum werden die fehlenden Spalten hier per ALTER TABLE nachgezogen.
-- 2) tickets.tags fehlte komplett, wurde im Go-Code aber nie geschrieben/
--    gelesen -> Daten gingen verloren.
--
-- Alle ALTER TABLE ... ADD COLUMN IF NOT EXISTS sind idempotent und sicher
-- erneut auszuführen, auch wenn 003 schon korrekt gelaufen ist.

ALTER TABLE shift_models
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE shift_definitions
    ADD COLUMN IF NOT EXISTS short_name VARCHAR(10) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_night   BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE shift_assignments
    ADD COLUMN IF NOT EXISTS note       TEXT,
    ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id);
-- created_by bleibt NULLABLE (nicht NOT NULL), weil bestehende Zeilen aus
-- Migration 001 sonst den ALTER TABLE Befehl zum Scheitern bringen würden.

ALTER TABLE tickets
    ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]';
