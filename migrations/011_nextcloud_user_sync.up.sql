-- 011_nextcloud_user_sync.up.sql
-- Store Nextcloud identity metadata for PDH users synced from the Nextcloud group `pdh`.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS nextcloud_user_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS nextcloud_display_name VARCHAR(255),
    ADD COLUMN IF NOT EXISTS nextcloud_synced BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS nextcloud_last_sync TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_nextcloud_user_id
    ON users(nextcloud_user_id)
    WHERE nextcloud_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_users_nextcloud_synced
    ON users(nextcloud_synced);
