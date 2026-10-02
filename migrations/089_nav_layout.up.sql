-- 089_nav_layout.up.sql
-- Eigene Gruppierung der linken Navigation je Benutzer (nav.go):
-- [{"key": "...", "label": "...", "items": ["tickets", ...]}, ...]; NULL = Vorgabe.
ALTER TABLE users ADD COLUMN IF NOT EXISTS nav_layout JSONB;
