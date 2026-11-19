-- Sortieren (0.82.2): gemerkte Wahl je Benutzer und Listenart.
-- JSONB: Schlüssel = Listenart (z. B. "history"), Wert = Kennung (new, old, freq, same, person, kind, due).
ALTER TABLE users ADD COLUMN IF NOT EXISTS list_sort JSONB;
