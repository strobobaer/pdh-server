CREATE TABLE microsoft_directory_users (
    tenant_id TEXT NOT NULL,
    microsoft_user_id TEXT NOT NULL,
    pdh_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    department TEXT NOT NULL DEFAULT '',
    account_enabled BOOLEAN NOT NULL DEFAULT true,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, microsoft_user_id),
    UNIQUE (tenant_id, pdh_user_id)
);

CREATE INDEX idx_microsoft_directory_users_email ON microsoft_directory_users (tenant_id, lower(email));