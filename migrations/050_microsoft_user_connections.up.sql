CREATE TABLE microsoft_user_connections (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    microsoft_user_id TEXT NOT NULL UNIQUE,
    microsoft_email TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    granted_scopes TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE microsoft_oauth_states (
    state_hash CHAR(64) PRIMARY KEY,
    code_verifier TEXT NOT NULL,
    scope_mode TEXT NOT NULL DEFAULT 'calendar',
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_microsoft_oauth_states_expiry ON microsoft_oauth_states (expires_at);