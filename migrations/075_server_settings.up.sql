-- 075_server_settings.up.sql
--
-- Server-Einstellungen (frueher nur .env) liegen jetzt in der Datenbank.
-- Nur der Datenbank-Zugang (PDH_DATABASE_*) und der Schluessel fuer die
-- verschluesselten Werte (PDH_SETTINGS_KEY) bleiben zwangslaeufig in der
-- Einstellungsdatei. Geheime Werte (Passwoerter, Schluessel, Tokens) sind
-- mit AES-GCM verschluesselt (value = "enc:v1:<base64>").
-- Beim Start werden Werte aus der .env, die hier noch fehlen, einmalig
-- uebernommen (source = 'env_import').
CREATE TABLE IF NOT EXISTS server_settings (
    key        VARCHAR(100) PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    secret     BOOLEAN NOT NULL DEFAULT false,
    source     VARCHAR(20) NOT NULL DEFAULT 'ui', -- ui | env_import | generated
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
