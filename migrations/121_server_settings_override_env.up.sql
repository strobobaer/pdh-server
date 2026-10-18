-- 121_server_settings_override_env.up.sql
-- Werte aus der Docker-/systemd-Umgebung (.env.docker) in die Datenbank
-- übernehmen (Server-Einstellungen → „In die Datenbank übernehmen“):
-- overrides_env = true → der Datenbankwert hat Vorrang vor der Umgebung,
-- der Eintrag in .env.docker kann danach entfallen (pkg/config/dbstore.go).
ALTER TABLE server_settings ADD COLUMN IF NOT EXISTS overrides_env BOOLEAN NOT NULL DEFAULT false;
