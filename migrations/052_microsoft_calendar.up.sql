CREATE TABLE microsoft_calendar_preferences (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    sync_shifts BOOLEAN NOT NULL DEFAULT false,
    sync_tasks BOOLEAN NOT NULL DEFAULT false,
    sync_maintenance BOOLEAN NOT NULL DEFAULT false,
    sync_tickets_faults BOOLEAN NOT NULL DEFAULT false,
    import_busy_events BOOLEAN NOT NULL DEFAULT false,
    last_sync_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE microsoft_calendar_blocks (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    microsoft_event_id TEXT NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    show_as TEXT NOT NULL,
    is_all_day BOOLEAN NOT NULL DEFAULT false,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, microsoft_event_id)
);

CREATE INDEX idx_microsoft_calendar_blocks_time ON microsoft_calendar_blocks (user_id, starts_at, ends_at);

CREATE TABLE microsoft_calendar_links (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('shift','task','maintenance','ticket','fault')),
    source_id UUID NOT NULL,
    microsoft_event_id TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, source_type, source_id),
    UNIQUE (user_id, microsoft_event_id)
);