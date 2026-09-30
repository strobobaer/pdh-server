-- 077_microsoft_private_account.up.sql
-- Pro Nutzer ein geschäftliches ('work') und zusätzlich ein privates
-- ('private') Microsoft-Konto. Kalender-Einstellungen, verknüpfte Termine und
-- importierte Busy-Blocker gelten je Konto. Bestehende Verknüpfungen werden
-- als geschäftlich übernommen.

ALTER TABLE microsoft_user_connections
    ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'work' CHECK (account_kind IN ('work','private'));
ALTER TABLE microsoft_user_connections DROP CONSTRAINT IF EXISTS microsoft_user_connections_pkey;
ALTER TABLE microsoft_user_connections ADD PRIMARY KEY (user_id, account_kind);

ALTER TABLE microsoft_oauth_states
    ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'work' CHECK (account_kind IN ('work','private'));

ALTER TABLE microsoft_calendar_preferences
    ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'work' CHECK (account_kind IN ('work','private'));
ALTER TABLE microsoft_calendar_preferences DROP CONSTRAINT IF EXISTS microsoft_calendar_preferences_pkey;
ALTER TABLE microsoft_calendar_preferences ADD PRIMARY KEY (user_id, account_kind);

ALTER TABLE microsoft_calendar_blocks
    ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'work' CHECK (account_kind IN ('work','private'));
ALTER TABLE microsoft_calendar_blocks DROP CONSTRAINT IF EXISTS microsoft_calendar_blocks_pkey;
ALTER TABLE microsoft_calendar_blocks ADD PRIMARY KEY (user_id, account_kind, microsoft_event_id);

ALTER TABLE microsoft_calendar_links
    ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'work' CHECK (account_kind IN ('work','private'));
ALTER TABLE microsoft_calendar_links DROP CONSTRAINT IF EXISTS microsoft_calendar_links_pkey;
ALTER TABLE microsoft_calendar_links DROP CONSTRAINT IF EXISTS microsoft_calendar_links_user_id_microsoft_event_id_key;
ALTER TABLE microsoft_calendar_links ADD PRIMARY KEY (user_id, account_kind, source_type, source_id);
ALTER TABLE microsoft_calendar_links ADD CONSTRAINT microsoft_calendar_links_account_event_key UNIQUE (user_id, account_kind, microsoft_event_id);
