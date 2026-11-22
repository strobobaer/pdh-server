-- Herkunft von Tickets und Störungen (0.82.31): woher ein Datensatz kam – von Hand im Web,
-- Easy-Modus, Anlagenterminal, App, Abgleich mit einem Fremdsystem, Alarm oder System.
-- origin_label ergänzt z. B. den Namen des Terminals oder „Energiedaten“. Leer = unbekannt
-- (älter als 0.82.31 und nicht erkennbar). Wiederholbar.

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT ''
    CHECK (origin IN ('', 'web', 'easy', 'terminal', 'app', 'sync', 'alarm', 'system'));
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS origin_label text NOT NULL DEFAULT '';
ALTER TABLE faults ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT ''
    CHECK (origin IN ('', 'web', 'easy', 'terminal', 'app', 'sync', 'alarm', 'system'));
ALTER TABLE faults ADD COLUMN IF NOT EXISTS origin_label text NOT NULL DEFAULT '';

-- Nachtrag, soweit erkennbar (nur, wo noch nichts steht)
UPDATE tickets t SET origin = 'sync'
 WHERE t.origin = '' AND EXISTS (SELECT 1 FROM sync_links l WHERE l.ref_type = 'ticket' AND l.ref_id = t.id);
UPDATE faults f SET origin = 'sync'
 WHERE f.origin = '' AND EXISTS (SELECT 1 FROM sync_links l WHERE l.ref_type = 'fault' AND l.ref_id = f.id);

UPDATE tickets SET origin = 'app' WHERE origin = '' AND app_request_id IS NOT NULL;

UPDATE tickets SET origin = 'alarm', origin_label = CASE WHEN title LIKE 'Netzgerät offline:%' THEN 'IT-Netz' ELSE 'Energiedaten' END
 WHERE origin = '' AND requires_root_cause;

UPDATE tickets t SET origin = 'system', origin_label = 'Firewall-Freigabe'
 WHERE t.origin = '' AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(
       CASE WHEN jsonb_typeof(t.tags) = 'array' THEN t.tags ELSE '[]'::jsonb END) x WHERE x LIKE 'esppdh-freigabe:%');

UPDATE faults f SET origin = 'system', origin_label = 'Rüstzeit'
 WHERE f.origin = '' AND EXISTS (SELECT 1 FROM record_history h
     WHERE h.ref_type = 'fault' AND h.ref_id = f.id AND h.action = 'create' AND h.field_name = 'setup_time');

UPDATE tickets SET origin = 'terminal' WHERE origin = '' AND description LIKE '%Gemeldet am Anlagenterminal%';
UPDATE faults SET origin = 'terminal' WHERE origin = '' AND description LIKE '%Gemeldet am Anlagenterminal%';
UPDATE tickets SET origin = 'easy' WHERE origin = '' AND description LIKE '%Gemeldet per QR-Code (Easy-Mode)%';
UPDATE faults SET origin = 'easy' WHERE origin = '' AND description LIKE '%Gemeldet per QR-Code (Easy-Mode)%';

-- Kacheln der Startseite zählen offene Vorgänge je Herkunft
CREATE INDEX IF NOT EXISTS idx_tickets_origin ON tickets (origin) WHERE origin <> '';
CREATE INDEX IF NOT EXISTS idx_faults_origin ON faults (origin) WHERE origin <> '';
