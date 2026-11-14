
		SELECT r.id::text, r.title, COALESCE(r.description, ''), r.status::text, r.severity::text, r.due_date,
		       COALESCE(r.infrastructure_id::text, ''), COALESCE(i.name, ''), COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), g.name, ''), COALESCE((r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN (SELECT gm.group_id FROM user_group_members gm WHERE gm.user_id = $1::uuid)), false), (r.assigned_to IS NULL AND r.assigned_group_id IS NULL), '', '', r.created_at, COALESCE(false, false)
		  FROM faults r
		  LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		  LEFT JOIN users u ON u.id = r.assigned_to
		  LEFT JOIN user_groups g ON g.id = r.assigned_group_id