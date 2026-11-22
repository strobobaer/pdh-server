-- 196_terminal_pdh_sessions.up.sql
--
-- „PDH öffnen“ am Anlagenterminal: Eine per Karte angemeldete Person öffnet das normale PDH im
-- Browser des Terminals, angemeldet als sie selbst. Dafür gibt es eine kurzlebige, serverseitig
-- widerrufbare Sitzung (höchstens 15 Minuten, keine Verlängerung), die an das Gerät gebunden ist.
-- Gespeichert wird nur der Hash des Einmal-Tickets (60 Sekunden gültig, einmal einlösbar).
-- Die Tabelle ist neu; ältere PDH-Stände und Terminals kennen sie nicht und bleiben unberührt.

CREATE TABLE IF NOT EXISTS terminal_pdh_sessions (
    id                 UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    terminal_user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    person_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id          UUID NOT NULL,
    next_path          TEXT NOT NULL DEFAULT '/',
    ticket_sha256      TEXT NOT NULL,
    ticket_expires_at  TIMESTAMPTZ NOT NULL,
    redeemed_at        TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS terminal_pdh_sessions_ticket ON terminal_pdh_sessions(ticket_sha256);
CREATE INDEX IF NOT EXISTS terminal_pdh_sessions_terminal ON terminal_pdh_sessions(terminal_user_id) WHERE revoked_at IS NULL;
