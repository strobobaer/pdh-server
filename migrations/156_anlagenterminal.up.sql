-- 156_anlagenterminal.up.sql
--
-- Anlagenterminal (Teilprojekt 1).
-- HMI-Verbindung: wer sie am Terminal ansehen bzw. bedienen darf.
--   terminal_view     everyone | card | never   (Standard everyone)
--   terminal_control  never | card | everyone   (Standard card)
-- Ist allow_control aus, gilt am Terminal wirksam „never“ (hmi_terminal.go).

ALTER TABLE infrastructure_hmis
    ADD COLUMN IF NOT EXISTS terminal_view TEXT NOT NULL DEFAULT 'everyone' CHECK (terminal_view IN ('everyone','card','never')),
    ADD COLUMN IF NOT EXISTS terminal_control TEXT NOT NULL DEFAULT 'card' CHECK (terminal_control IN ('never','card','everyone'));

-- Terminal-Einstellungen je System-Benutzer. idle_seconds = Rückkehrzeit zum
-- Bildschirmschoner; NULL = globaler Wert (app_settings „terminal.idle_seconds“).
CREATE TABLE IF NOT EXISTS terminal_settings (
    user_id      UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    idle_seconds INT CHECK (idle_seconds BETWEEN 30 AND 3600),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
