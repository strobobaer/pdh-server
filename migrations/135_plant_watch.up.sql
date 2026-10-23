-- 135_plant_watch.up.sql
-- Anlagen-Schnellübersicht (internal/web/plant_watch.go): Übersichten mit
-- Anlagen, auf die ein besonderes Augenmerk gelegt wird – einzeln gewählt oder
-- „alle Anlagen eines Typs“ (z. B. alle Trockenkammern). Jede Übersicht zeigt
-- Ampelstatus, offene Störungen, überfällige Wartungen/Prüfungen, NOK-Bauteile
-- und die Unteranlagen.
--
-- Sichtbar ist eine Übersicht für die Person, die sie angelegt hat, und für
-- alle, denen sie freigegeben ist (einzelne Benutzer oder Gruppen).
-- Recht infrastructure.watch: Übersichten für andere einrichten und freigeben,
-- alle Übersichten bearbeiten.

INSERT INTO permissions (key, label, category) VALUES
    ('infrastructure.watch', 'Anlagen-Schnellübersichten einrichten und für Benutzer/Gruppen freigeben', 'Infrastruktur')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager', 'asset_manager') AND p.key = 'infrastructure.watch'
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS plant_watch_boards (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    name        VARCHAR(80) NOT NULL,
    icon        VARCHAR(40) NOT NULL DEFAULT 'ti-eye',
    description TEXT NOT NULL DEFAULT '',
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Inhalt: eine bestimmte Anlage (mit ihren Unteranlagen) oder alle aktiven Anlagen eines Typs
CREATE TABLE IF NOT EXISTS plant_watch_items (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    board_id          UUID NOT NULL REFERENCES plant_watch_boards(id) ON DELETE CASCADE,
    infrastructure_id UUID REFERENCES infrastructure(id) ON DELETE CASCADE,
    infra_type        VARCHAR(30),
    CHECK ((infrastructure_id IS NULL) <> (infra_type IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plant_watch_items_infra ON plant_watch_items(board_id, infrastructure_id) WHERE infrastructure_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_plant_watch_items_type ON plant_watch_items(board_id, infra_type) WHERE infra_type IS NOT NULL;

-- Freigabe: an einzelne Benutzer oder an Gruppen (user_groups)
CREATE TABLE IF NOT EXISTS plant_watch_access (
    id       UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    board_id UUID NOT NULL REFERENCES plant_watch_boards(id) ON DELETE CASCADE,
    user_id  UUID REFERENCES users(id) ON DELETE CASCADE,
    group_id UUID REFERENCES user_groups(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (group_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plant_watch_access_user ON plant_watch_access(board_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_plant_watch_access_group ON plant_watch_access(board_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_plant_watch_access_board ON plant_watch_access(board_id);
