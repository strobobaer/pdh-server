-- Rüstzeiten: freiwilliges Kürzel/Name der Person, die am Easy-Modus rüstet.
-- Nur hinzufügend; leer bleibt erlaubt. Bestehende Zeilen bekommen ''.
ALTER TABLE setup_times ADD COLUMN IF NOT EXISTS operator_name VARCHAR(80) NOT NULL DEFAULT '';
