-- 098_status_pending.up.sql
--
-- Status „Wartet“ (pending) auch fuer Stoerungen und Wartungsauftraege
-- (Tickets haben ihn schon, Aufgaben speichern den Status als Text).
-- Gesetzt u. a. durch „Warten“ am Leitstand. Der neue Wert darf in derselben
-- Transaktion noch nicht benutzt werden – die Umstellung folgt in 099.

ALTER TYPE fault_status ADD VALUE IF NOT EXISTS 'pending';
ALTER TYPE maintenance_status ADD VALUE IF NOT EXISTS 'pending';
