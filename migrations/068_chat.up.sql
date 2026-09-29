-- 068_chat.up.sql
--
-- Interner Nachrichtendienst im Teams-Stil:
--   * Chats: 1:1 (direct) und Gruppen (group) mit festen Mitgliedern
--   * Teams mit Kanaelen (channel): Mitglieder des Teams sind Mitglieder
--     aller Kanaele; Kanal-Beitraege haben Antworten (parent_id)
-- chat_members fuehrt fuer alle Unterhaltungen Lesestand, Stummschaltung
-- und Mitgliedschaft (auch fuer Kanaele, damit Ungelesen-Zaehler einheitlich
-- berechnet werden). Dateien liegen NICHT im oeffentlichen /uploads,
-- sondern werden nur an Mitglieder ausgeliefert.

CREATE TABLE IF NOT EXISTS chat_teams (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    color       VARCHAR(20)  NOT NULL DEFAULT '',
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS chat_team_members (
    team_id   UUID NOT NULL REFERENCES chat_teams(id) ON DELETE CASCADE,
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      VARCHAR(20) NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_team_members_user ON chat_team_members(user_id);

CREATE TABLE IF NOT EXISTS chat_conversations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            VARCHAR(20) NOT NULL CHECK (kind IN ('direct', 'group', 'channel')),
    name            VARCHAR(100) NOT NULL DEFAULT '',
    description     VARCHAR(500) NOT NULL DEFAULT '',
    team_id         UUID REFERENCES chat_teams(id) ON DELETE CASCADE,
    direct_key      VARCHAR(80) UNIQUE,           -- sortierte Benutzer-IDs bei 1:1
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at     TIMESTAMPTZ,
    CHECK ((kind = 'channel') = (team_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_chat_conversations_team ON chat_conversations(team_id);

CREATE TABLE IF NOT EXISTS chat_members (
    conversation_id UUID NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_read_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    muted           BOOLEAN NOT NULL DEFAULT false,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at         TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_members_user ON chat_members(user_id) WHERE left_at IS NULL;

CREATE TABLE IF NOT EXISTS chat_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
    parent_id       UUID REFERENCES chat_messages(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    kind            VARCHAR(20) NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'system')),
    body            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at       TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_conv ON chat_messages(conversation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_chat_messages_parent ON chat_messages(parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS chat_mentions (
    message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_mentions_user ON chat_mentions(user_id);

CREATE TABLE IF NOT EXISTS chat_reactions (
    message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    emoji      VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, user_id, emoji)
);

CREATE TABLE IF NOT EXISTS chat_files (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id      UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    conversation_id UUID NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
    filename        VARCHAR(255) NOT NULL,
    storage_path    VARCHAR(500) NOT NULL,
    mimetype        VARCHAR(150) NOT NULL DEFAULT 'application/octet-stream',
    size_bytes      BIGINT NOT NULL DEFAULT 0,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_chat_files_message ON chat_files(message_id);
CREATE INDEX IF NOT EXISTS idx_chat_files_conv ON chat_files(conversation_id, created_at DESC);

-- In Nachrichten erkannte PDH-Datensaetze (Ticket, Stoerung, Anlage ...)
CREATE TABLE IF NOT EXISTS chat_message_links (
    message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    ref_type   VARCHAR(40) NOT NULL,
    ref_id     UUID NOT NULL,
    title      VARCHAR(300) NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, ref_type, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_message_links_ref ON chat_message_links(ref_type, ref_id);

-- Berechtigung: Chat nutzen - standardmaessig fuer alle bestehenden Rollen
INSERT INTO permissions (key, label, category) VALUES
    ('chat.use', 'Chat & Teams nutzen', 'Chat')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key = 'chat.use'
ON CONFLICT DO NOTHING;
