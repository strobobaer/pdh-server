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
