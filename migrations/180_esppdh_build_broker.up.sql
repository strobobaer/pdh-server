-- ESPPDH (0.82.0): Bauen der Firmware mit Schlange.
-- Adresse des Brokers, für die ein Bau die Firmware erzeugt. Sie wird beim Einreihen einmal
-- festgelegt – hat der Broker keinen Namen, hinge sie sonst von der Adresse ab, unter der das
-- PDH gerade aufgerufen wird – und steht auf der Geräteseite bei jedem Lauf.
ALTER TABLE esppdh_builds
    ADD COLUMN IF NOT EXISTS broker_host TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS broker_port INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS broker_tls BOOLEAN NOT NULL DEFAULT false;

-- Je Gerät höchstens ein wartender oder laufender Bau (auch bei zwei gleichzeitigen Klicks).
CREATE UNIQUE INDEX IF NOT EXISTS esppdh_builds_one_active_idx
    ON esppdh_builds (device_id) WHERE status IN ('queued', 'running');

-- Schlange: der älteste wartende Bau zuerst.
CREATE INDEX IF NOT EXISTS esppdh_builds_queue_idx
    ON esppdh_builds (created_at, id) WHERE status = 'queued';
