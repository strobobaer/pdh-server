-- 120_grafana_statistics.up.sql
-- Statistik (/statistics): Kacheln, die je ein Grafana-Dashboard öffnen.
-- Gepflegt unter Core → Grafana & Statistik (internal/web/grafana.go).
-- Adresse von Grafana: app_settings 'grafana.base_url' (leer = Server-Adresse, Port 3000).

CREATE TABLE IF NOT EXISTS grafana_dashboards (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       VARCHAR(120) NOT NULL,
    url         TEXT NOT NULL,                       -- voll (https://…) oder Pfad (/d/uid/name)
    description TEXT NOT NULL DEFAULT '',
    icon        VARCHAR(60) NOT NULL DEFAULT 'ti-chart-line',
    color       VARCHAR(20) NOT NULL DEFAULT 'blue',
    kiosk       BOOLEAN NOT NULL DEFAULT true,       -- ohne Grafana-Menüs anzeigen
    open_mode   VARCHAR(10) NOT NULL DEFAULT 'embed' CHECK (open_mode IN ('embed', 'tab')),
    refresh     VARCHAR(10) NOT NULL DEFAULT '',     -- z. B. 30s, 5m (leer = wie im Dashboard)
    sort        INTEGER NOT NULL DEFAULT 0,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- mitgeliefertes Start-Dashboard (grafana/provisioning/dashboards/pdh-overview.json)
INSERT INTO grafana_dashboards (title, url, description, icon, color, sort)
SELECT 'PDH Übersicht', '/d/pdh-overview/pdh-ubersicht', 'Störungen, Tickets und Stunden je Kostenstelle', 'ti-chart-histogram', 'blue', 1
WHERE NOT EXISTS (SELECT 1 FROM grafana_dashboards);

INSERT INTO permissions (key, label, category) VALUES
    ('statistics.view', 'Statistik (Grafana-Dashboards) ansehen', 'Daten')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager', 'technician') AND p.key = 'statistics.view'
ON CONFLICT DO NOTHING;
