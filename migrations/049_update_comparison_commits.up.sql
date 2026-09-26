INSERT INTO app_settings (key, value) VALUES
    ('update_comparison_commits', '[]'),
    ('update_comparison_commit_count', '0')
ON CONFLICT (key) DO NOTHING;