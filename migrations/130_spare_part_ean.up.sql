-- 130_spare_part_ean.up.sql
-- EAN/GTIN je Ersatzteil (Stammdaten, Barcode-Scan in der Ersatzteil-Liste,
-- Abgleich beim Lieferschein- und Bauteil-Scan – internal/web/part_scan.go).

ALTER TABLE spare_parts ADD COLUMN IF NOT EXISTS ean VARCHAR(20) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_spare_parts_ean ON spare_parts(ean) WHERE ean <> '';
