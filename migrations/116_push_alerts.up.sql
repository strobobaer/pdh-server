-- 116_push_alerts.up.sql
-- Push-Benachrichtigungen aufs Handy (internal/web/push.go, webpush.go).
--
-- plant_stopped       „Anlage steht“ an Stoerung/Ticket: Alarm wiederholt sich,
--                     bis jemand annimmt (Vollbild in der PDH-App)
-- push_subscriptions  ein Eintrag je Geraet/Browser (Web Push, RFC 8030)
-- push_alerts         eine Benachrichtigung je neuem Vorgang; recipients aus den
--                     in den Core-Einstellungen gewaehlten Benutzergruppen;
--                     accepted_by = wer zuerst angenommen hat

ALTER TABLE faults  ADD COLUMN IF NOT EXISTS plant_stopped BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS plant_stopped BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS push_subscriptions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint   TEXT NOT NULL UNIQUE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_ok_at TIMESTAMPTZ,
    failures   INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user ON push_subscriptions(user_id);

CREATE TABLE IF NOT EXISTS push_alerts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type         VARCHAR(10) NOT NULL CHECK (ref_type IN ('fault', 'ticket')),
    ref_id           UUID NOT NULL,
    title            TEXT NOT NULL,
    body             TEXT NOT NULL DEFAULT '',
    urgent           BOOLEAN NOT NULL DEFAULT false,
    recipients       UUID[] NOT NULL DEFAULT '{}',
    initial_assignee UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_sent_at     TIMESTAMPTZ,
    sends            INT NOT NULL DEFAULT 0,
    accepted_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    accepted_at      TIMESTAMPTZ,
    closed_at        TIMESTAMPTZ,
    close_reason     TEXT NOT NULL DEFAULT '',
    UNIQUE (ref_type, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_push_alerts_open ON push_alerts(created_at) WHERE closed_at IS NULL;
