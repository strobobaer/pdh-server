-- 200_terminal_slides_stats_downtime.up.sql
--
-- Infobildschirm: neue Folienart stats_downtime (Stillstände je Grund). Die
-- Folienart-Prüfung wird nur erweitert, alle vorhandenen Folien bleiben gültig.

ALTER TABLE terminal_slides DROP CONSTRAINT IF EXISTS terminal_slides_kind_check;
ALTER TABLE terminal_slides ADD CONSTRAINT terminal_slides_kind_check
    CHECK (kind IN ('image', 'text', 'vorgaenge', 'stats_faults', 'stats_tickets', 'stats_setup', 'stats_downtime', 'grafana'));
