-- 099_status_pending_backfill.up.sql
--
-- Vorgaenge, die am Leitstand auf „Warten“ gesetzt wurden und deren
-- Wiedervorlage noch aussteht, bekommen den echten Status „Wartet“.
-- Bisher stand „Wartet“ nur in der Leitstand-Tabelle.

WITH last AS (
    SELECT DISTINCT ON (ref_type, ref_id) ref_type, ref_id, action, follow_up_date
    FROM global_dashboard_actions ORDER BY ref_type, ref_id, created_at DESC
)
UPDATE faults x SET status = 'pending', updated_at = NOW() FROM last
WHERE last.ref_type = 'fault' AND last.ref_id = x.id AND last.action = 'wait'
  AND last.follow_up_date > CURRENT_DATE AND x.status IN ('detected', 'analyzing', 'in_progress');

WITH last AS (
    SELECT DISTINCT ON (ref_type, ref_id) ref_type, ref_id, action, follow_up_date
    FROM global_dashboard_actions ORDER BY ref_type, ref_id, created_at DESC
)
UPDATE tickets x SET status = 'pending', updated_at = NOW() FROM last
WHERE last.ref_type = 'ticket' AND last.ref_id = x.id AND last.action = 'wait'
  AND last.follow_up_date > CURRENT_DATE AND x.status IN ('open', 'in_progress');

WITH last AS (
    SELECT DISTINCT ON (ref_type, ref_id) ref_type, ref_id, action, follow_up_date
    FROM global_dashboard_actions ORDER BY ref_type, ref_id, created_at DESC
)
UPDATE maintenance_tasks x SET status = 'pending' FROM last
WHERE last.ref_type = 'maintenance' AND last.ref_id = x.id AND last.action = 'wait'
  AND last.follow_up_date > CURRENT_DATE AND x.status IN ('open', 'in_progress');

WITH last AS (
    SELECT DISTINCT ON (ref_type, ref_id) ref_type, ref_id, action, follow_up_date
    FROM global_dashboard_actions ORDER BY ref_type, ref_id, created_at DESC
)
UPDATE tasks x SET status = 'pending', updated_at = NOW() FROM last
WHERE last.ref_type = 'task' AND last.ref_id = x.id AND last.action = 'wait'
  AND last.follow_up_date > CURRENT_DATE AND x.status IN ('open', 'in_progress');
