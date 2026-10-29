-- 152_leitstand_rotate_grafana.up.sql
-- Weiterblättern mit Grafana-Dashboards (Statistik) als zusätzliche Stationen:
-- Leitstand → Dashboard 1 → Dashboard 2 → nächster Leitstand (leitstand_grafana.go).
-- Kommagetrennte IDs aus grafana_dashboards in der gewählten Reihenfolge.
ALTER TABLE leitstaende ADD COLUMN IF NOT EXISTS rotate_grafana TEXT NOT NULL DEFAULT '';
