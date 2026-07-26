-- 039_task_color.up.sql
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS color VARCHAR(7);

