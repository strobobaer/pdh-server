-- 029_addins.up.sql
--
-- Grundlage fuer das Add-in-System: interne (Skript, Stufe 2) und externe
-- (Webhook) Erweiterungen, die auf PDH-Ereignisse reagieren koennen.
-- Neu angelegte/geaenderte Add-ins wirken sofort (keine Neustart noetig),
-- da der Ereignis-Bus bei jedem Ereignis frisch aus der Datenbank liest.

CREATE TABLE IF NOT EXISTS addins (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           VARCHAR(255) NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    type           VARCHAR(20) NOT NULL DEFAULT 'webhook', -- 'webhook' oder 'script' (Stufe 2)
    enabled        BOOLEAN NOT NULL DEFAULT true,
    webhook_url    TEXT,
    webhook_secret TEXT,
    script_code    TEXT,
    created_by     UUID NOT NULL REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS addin_event_subscriptions (
    addin_id   UUID NOT NULL REFERENCES addins(id) ON DELETE CASCADE,
    event_name VARCHAR(100) NOT NULL,
    PRIMARY KEY (addin_id, event_name)
);

CREATE TABLE IF NOT EXISTS addin_run_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    addin_id    UUID NOT NULL REFERENCES addins(id) ON DELETE CASCADE,
    event_name  VARCHAR(100) NOT NULL,
    payload     JSONB NOT NULL DEFAULT '{}',
    success     BOOLEAN NOT NULL,
    error       TEXT,
    duration_ms INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_addin_event_subs_event ON addin_event_subscriptions(event_name);
CREATE INDEX IF NOT EXISTS idx_addin_run_log_addin    ON addin_run_log(addin_id);
CREATE INDEX IF NOT EXISTS idx_addin_run_log_created  ON addin_run_log(created_at);
