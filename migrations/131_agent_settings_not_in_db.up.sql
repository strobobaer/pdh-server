-- 131_agent_settings_not_in_db.up.sql
-- Update-Agent-Token und -Adresse gehören nur in die Einstellungsdatei, aus der
-- auch der Agent liest (install-update-agent.sh). Über die Server-Einstellungen
-- gespeicherte Datenbank-Werte hatten Vorrang und führten zu „401 Unauthorized“.
DELETE FROM server_settings WHERE key IN ('PDH_UPDATE_AGENT_TOKEN', 'PDH_UPDATE_AGENT_URL');
