-- 220_terminal_local_port.up.sql
--
-- Lokaler Port der Startseite des PDH-Terminals (ab Terminal 0.4.6 im Header X-Terminal-Local-Port bei der
-- Statusabfrage, nur wenn er vom Grundwert 8080 abweicht; die lokale Admin-Seite liegt dann auf Port + 1).
-- Die Seiten im Kioskbrowser sprechen den Kartendienst und die Bildschirmtastatur auf diesem Port an.
-- NULL = Grundwert 8080 (alte Terminals ohne Kopfzeile). Nur hinzufügend.

ALTER TABLE terminal_devices ADD COLUMN IF NOT EXISTS local_port INTEGER NULL
	CHECK (local_port IS NULL OR (local_port BETWEEN 1024 AND 65534));
