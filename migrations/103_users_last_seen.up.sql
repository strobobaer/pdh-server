-- Anwesenheit: wann war ein Benutzer zuletzt in PDH aktiv (Seitenleiste „Aktivität“).
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
