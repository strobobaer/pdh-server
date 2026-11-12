-- 158_terminal_card_sessions.up.sql
--
-- Kartensitzung des Anlagenterminals (TP2).
-- Pro Terminal-Benutzer (users.is_system_user) höchstens eine Sitzung: Die
-- Person, die ihre RFID-Karte am Terminal vorgehalten hat. Das ist KEINE
-- PDH-Anmeldung; Rechte werden immer gegen die Kartenperson geprüft.
-- Die Sitzung läuft nach 2 Minuten ohne Aktivität ab (last_seen_at).

CREATE TABLE IF NOT EXISTS terminal_card_sessions (
    terminal_user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    person_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
