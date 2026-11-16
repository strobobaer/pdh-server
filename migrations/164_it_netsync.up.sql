-- IT-Assets aus dem Netz (0.80.17): Abgleich mit UniFi, Vorschläge,
-- Überwachung und Alarm-Tickets.

-- ── Netz-Felder am IT-Asset ───────────────────────────────────────────────
ALTER TABLE it_assets
    ADD COLUMN IF NOT EXISTS net_source       TEXT,
    ADD COLUMN IF NOT EXISTS net_external_id  TEXT,
    ADD COLUMN IF NOT EXISTS net_monitored    BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS net_state        TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS net_last_seen    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS net_state_since  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS firmware         TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS firmware_update  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS net_uplink       TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS net_gone         BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS net_role         TEXT,
    -- „Quelle trennen“: Quelle und ID bleiben (kein Doppel beim nächsten
    -- Abgleich), das Asset wird aber nicht mehr abgeglichen und nicht überwacht.
    ADD COLUMN IF NOT EXISTS net_detached     BOOLEAN NOT NULL DEFAULT false;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'it_assets_net_role_check') THEN
        ALTER TABLE it_assets ADD CONSTRAINT it_assets_net_role_check
            CHECK (net_role IN ('device', 'client'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'it_assets_net_state_check') THEN
        ALTER TABLE it_assets ADD CONSTRAINT it_assets_net_state_check
            CHECK (net_state IN ('online', 'offline', 'unknown'));
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS it_assets_net_source_ext
    ON it_assets (net_source, net_external_id) WHERE net_source IS NOT NULL;
-- MAC-Vergleich unabhängig von Schreibweise (Trenner, Groß-/Kleinschreibung, Leerzeichen).
DROP INDEX IF EXISTS it_assets_mac_norm;
CREATE INDEX IF NOT EXISTS it_assets_mac_norm
    ON it_assets (regexp_replace(lower(mac_address), '[^0-9a-f]', '', 'g'));

-- ── Ports eines Netzgeräts (bei jedem Abruf ersetzt) ──────────────────────
CREATE TABLE IF NOT EXISTS it_asset_ports (
    asset_id       UUID NOT NULL REFERENCES it_assets(id) ON DELETE CASCADE,
    idx            INT  NOT NULL,
    connector      TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT 'UNKNOWN',
    speed_mbps     INT  NOT NULL DEFAULT 0,
    max_speed_mbps INT  NOT NULL DEFAULT 0,
    poe_state      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (asset_id, idx)
);

-- ── Vorschläge: Clients, die noch kein Asset sind ─────────────────────────
CREATE TABLE IF NOT EXISTS it_net_candidates (
    source      TEXT NOT NULL,
    mac         TEXT NOT NULL,
    external_id TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('wired', 'wireless')),
    uplink      TEXT NOT NULL DEFAULT '',
    last_seen   TIMESTAMPTZ NOT NULL,
    ignored     BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (source, mac)
);

-- ── Laufender Zustand des Wächters je Asset ───────────────────────────────
CREATE TABLE IF NOT EXISTS it_net_alarm_state (
    asset_id      UUID PRIMARY KEY REFERENCES it_assets(id) ON DELETE CASCADE,
    state         TEXT NOT NULL DEFAULT 'normal' CHECK (state IN ('normal', 'alarm')),
    offline_since TIMESTAMPTZ,
    ticket_id     UUID REFERENCES tickets(id) ON DELETE SET NULL
);

-- ── Recht ─────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('it.netsync', 'Netz-Abgleich einrichten und Vorschläge übernehmen', 'IT')
ON CONFLICT (key) DO NOTHING;

-- Wer IT-Infrastruktur bearbeiten darf, darf auch den Netz-Abgleich pflegen.
-- Die Abteilung der Quellrolle wird übernommen; gilt ein Recht überall (NULL),
-- gewinnt „überall“ (je Rolle und Recht gibt es nur eine Zeile).
INSERT INTO role_permissions (role_id, permission_id, department_id)
SELECT rp.role_id, p.id, rp.department_id
  FROM role_permissions rp
  JOIN permissions o ON o.id = rp.permission_id AND o.key = 'it.edit'
  CROSS JOIN permissions p
 WHERE p.key = 'it.netsync'
ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL
  WHERE EXCLUDED.department_id IS NULL;

-- Rolle im Abgleich: Netzgerät (Switch, AP …) oder übernommener Client.
-- Bereits abgeglichene Assets (Entwicklungsstände) bekommen die Rolle nachgetragen.
UPDATE it_assets
   SET net_role = CASE WHEN type = 'network' THEN 'device' ELSE 'client' END
 WHERE net_source IS NOT NULL AND net_role IS NULL;
