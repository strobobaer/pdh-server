
		SELECT r.id::text, r.title, COALESCE(r.description, ''), r.status::text, r.priority::text, r.due_date,
		       COALESCE(r.infrastructure_id::text, ''), COALESCE(i.name, ''), COALESCE(NULLIF((SELECT string_agg(TRIM(au.first_name || ' ' || au.last_name), ', ' ORDER BY au.last_name)
		          FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = r.id), ''), g.name, ''), COALESCE((r.responsible_to = $1::uuid OR r.assigned_group_id IN (SELECT gm.group_id FROM user_group_members gm WHERE gm.user_id = $1::uuid) OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $1::uuid)), false), (r.assigned_group_id IS NULL AND NOT EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id)), COALESCE(r.project_id::text, ''), COALESCE(p.name, ''), r.created_at, COALESCE(false, false)
		  FROM tasks r
		  LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		  LEFT JOIN users u ON u.id = NULL::uuid
		  LEFT JOIN user_groups g ON g.id = r.assigned_group_id LEFT JOIN projects p ON p.id = r.project_id