INSERT INTO app_settings (key, value) VALUES
    ('update_auto_check_enabled', 'true'),
    ('update_check_interval_hours', '24'),
    ('update_latest_commit', ''),
    ('update_comparison_status', ''),
    ('update_last_checked_at', ''),
    ('update_last_check_error', '')
ON CONFLICT (key) DO NOTHING;