-- 145_leitstaende.up.sql
-- Mehrere Leitstände (internal/web/leitstand_boards.go): je Abteilung, Gruppe
-- oder Team ein eigener, aus Bausteinen zusammengestellter Leitstand; der
-- bisherige Leitstand wird zum Standard-Leitstand (immer dann, wenn kein
-- anderer gewählt ist). Terminals (Systembenutzer) können einen Leitstand
-- direkt zugeordnet bekommen.

CREATE TABLE IF NOT EXISTS leitstaende (
    id             UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    slug           VARCHAR(60)  NOT NULL UNIQUE,          -- Adresse: /global/b/<slug>
    name           VARCHAR(120) NOT NULL,
    subtitle       VARCHAR(200) NOT NULL DEFAULT '',
    is_default     BOOLEAN      NOT NULL DEFAULT false,
    active         BOOLEAN      NOT NULL DEFAULT true,
    department_id  UUID REFERENCES departments(id) ON DELETE SET NULL,
    group_id       UUID REFERENCES user_groups(id) ON DELETE SET NULL,
    types          TEXT         NOT NULL DEFAULT 'fault,ticket,maintenance,task', -- gezeigte Vorgangsarten
    blocks         JSONB        NOT NULL DEFAULT '[]',     -- [{id,type,size,config}]
    rotate_seconds INTEGER      NOT NULL DEFAULT 0,        -- 0 = nicht weiterblättern
    rotate_next    UUID REFERENCES leitstaende(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS leitstaende_one_default ON leitstaende (is_default) WHERE is_default;

-- Standard-Leitstand mit Überschrift aus den bisherigen Einstellungen
INSERT INTO leitstaende (slug, name, subtitle, is_default, blocks)
SELECT 'standard',
       COALESCE((SELECT value FROM app_settings WHERE key = 'global_dashboard_title'), 'PDH · Leitstand'),
       COALESCE((SELECT value FROM app_settings WHERE key = 'global_dashboard_subtitle'), 'Störungen · Tickets · Wartungen · Aufgaben'),
       true,
       '[{"id":"s1","type":"create","size":4},{"id":"s2","type":"vorgaenge","size":4},{"id":"s3","type":"zeitstrahl","size":4}]'
WHERE NOT EXISTS (SELECT 1 FROM leitstaende WHERE is_default);

ALTER TABLE users ADD COLUMN IF NOT EXISTS leitstand_id UUID REFERENCES leitstaende(id) ON DELETE SET NULL;
