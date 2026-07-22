path = "internal/modules/maintenance/resolve.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

sig = "func (r *Repository) CompleteTaskWithFlag(ctx context.Context, id, userID, notes string, durationMin int, noPartsNeeded bool) error {"
if sig not in c:
    raise SystemExit("FEHLER: CompleteTaskWithFlag nicht gefunden")
idx = c.index(sig)
brace_start = c.index("{", idx)
depth = 0
i = brace_start
while i < len(c):
    if c[i] == "{":
        depth += 1
    elif c[i] == "}":
        depth -= 1
        if depth == 0:
            break
    i += 1
end = i + 1
old_func = c[idx:end]

new_func = '''func (r *Repository) CompleteTaskWithFlag(ctx context.Context, id, userID, notes string, durationMin int, noPartsNeeded bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE maintenance_tasks SET status='done', completed_at=NOW(),
		 notes=$1, duration_min=$2, no_parts_needed=$3 WHERE id=$4`,
		notes, durationMin, noPartsNeeded, id)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE maintenance_plans mp
		SET last_executed_at=NOW(),
		    next_due_at=NOW() + (interval_days || ' days')::interval
		FROM maintenance_tasks mt
		WHERE mt.id=$1 AND mt.plan_id=mp.id`, id)
	if err != nil {
		return err
	}
	// Naechsten Auftrag fuer diesen Plan sofort anlegen (falls Plan vorhanden
	// und noch kein offener/laufender Auftrag existiert).
	_ = r.GenerateNextTaskAfterCompletion(ctx, id, userID)
	return nil
}

// GenerateNextTaskAfterCompletion legt fuer den zum abgeschlossenen Auftrag
// gehoerenden Wartungsplan sofort den naechsten Auftrag an (faellig am neu
// berechneten next_due_at). Wird nur ausgefuehrt, wenn noch kein offener
// oder laufender Auftrag fuer diesen Plan existiert, um Duplikate zu vermeiden.
func (r *Repository) GenerateNextTaskAfterCompletion(ctx context.Context, taskID, createdBy string) error {
	var planID *string
	if err := r.db.QueryRow(ctx, `SELECT plan_id FROM maintenance_tasks WHERE id=$1`, taskID).Scan(&planID); err != nil {
		return err
	}
	if planID == nil {
		return nil
	}

	var existing int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_tasks WHERE plan_id=$1 AND status IN ('open','in_progress')`, *planID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	var name, infraID, priority string
	var assigned *string
	var nextDue time.Time
	var planType PlanType
	var costCenterID *string
	err := r.db.QueryRow(ctx, `
		SELECT name, type, infrastructure_id, priority, assigned_to, next_due_at, cost_center_id
		FROM maintenance_plans WHERE id=$1 AND active=true`, *planID).Scan(
		&name, &planType, &infraID, &priority, &assigned, &nextDue, &costCenterID)
	if err != nil {
		return err
	}

	t := &MaintenanceTask{
		PlanID: planID, Title: name, Type: planType,
		InfrastructureID: infraID, Priority: Priority(priority),
		AssignedTo: assigned, DueDate: nextDue, CreatedBy: createdBy,
		CostCenterID: costCenterID,
	}
	return r.CreateTask(ctx, t)
}'''

c = c[:idx] + new_func + c[end:]

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: " + path + " gepatcht (automatische Folge-Auftrag-Erzeugung).")


