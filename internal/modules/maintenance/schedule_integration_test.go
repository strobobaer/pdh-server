//go:build integration

// Terminplanung gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Schedule ./internal/modules/maintenance/
package maintenance

import (
	"context"
	"testing"
	"time"
)

func TestScheduleIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	repo := NewRepository(pool)
	svc := NewService(repo)
	suffix := time.Now().Format("150405.000000")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var userID, infraID string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Paul', 'Plan', '', '') RETURNING id::text`, "sc"+suffix).Scan(&userID))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Linie '||$1::text, 'plant') RETURNING id::text`, suffix).Scan(&infraID))
	openTasks := func(planID string) []*MaintenanceTask {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT id::text, due_date FROM maintenance_tasks WHERE plan_id=$1::uuid AND status IN ('open','in_progress','pending')`, planID)
		must(err)
		defer rows.Close()
		var out []*MaintenanceTask
		for rows.Next() {
			x := &MaintenanceTask{}
			must(rows.Scan(&x.ID, &x.DueDate))
			out = append(out, x)
		}
		return out
	}

	// Plan ohne Auftrag → Abgleich legt genau einen an, wiederholt keinen weiteren
	due := dayStart(time.Now()).AddDate(0, 0, -3)
	plan := &MaintenancePlan{Name: "Monat " + suffix, Type: "inspection", InfrastructureID: infraID, Interval: IntervalMonthly,
		IntervalDays: 30, Priority: PrioMedium, NextDueAt: due, CreatedBy: userID}
	must(repo.CreatePlan(ctx, plan))
	_, err := repo.EnsurePlanTasks(ctx)
	must(err)
	_, err = repo.EnsurePlanTasks(ctx)
	must(err)
	must(svc.EnsurePlanTask(ctx, plan.ID, userID))
	tasks := openTasks(plan.ID)
	if len(tasks) != 1 || !dayStart(tasks[0].DueDate).Equal(due) {
		t.Fatalf("nach Abgleich: %d offene Aufträge", len(tasks))
	}

	// ab Durchfuehrung: Erledigt heute → naechster Termin heute + 1 Monat, Folgeauftrag sofort
	must(repo.CompleteTaskWithFlag(ctx, tasks[0].ID, userID, "ok", 20, true))
	want := NextDue(IntervalMonthly, 30, time.Now())
	next := openTasks(plan.ID)
	if len(next) != 1 || !dayStart(next[0].DueDate).Equal(want) {
		t.Fatalf("Folgeauftrag ab Durchführung: %d / %v, erwartet %s", len(next), next, want.Format("02.01.2006"))
	}
	if got := svc.NextDueForTask(ctx, tasks[0].ID); got == nil || !dayStart(*got).Equal(want) {
		t.Fatalf("Plan-Termin: %v", got)
	}

	// fester Rhythmus + Ueberspringen: Termin vom Faelligkeitstag weiter, nicht ab heute
	_, err = pool.Exec(ctx, `UPDATE maintenance_plans SET schedule_mode='fixed' WHERE id=$1::uuid`, plan.ID)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE maintenance_tasks SET status='skipped' WHERE id=$1::uuid`, next[0].ID)
	must(err)
	got, err := svc.TaskSkipped(ctx, next[0].ID, userID)
	must(err)
	wantFixed := NextDue(IntervalMonthly, 30, want)
	after := openTasks(plan.ID)
	if got == nil || !got.Equal(wantFixed) || len(after) != 1 || !dayStart(after[0].DueDate).Equal(wantFixed) {
		t.Fatalf("fester Rhythmus nach Überspringen: %v / %d", got, len(after))
	}

	// inaktiver Plan: kein Folgeauftrag
	_, err = pool.Exec(ctx, `UPDATE maintenance_plans SET active=false WHERE id=$1::uuid`, plan.ID)
	must(err)
	must(repo.CompleteTaskWithFlag(ctx, after[0].ID, userID, "ok", 0, true))
	if n := len(openTasks(plan.ID)); n != 0 {
		t.Fatalf("inaktiver Plan bekam %d Folgeaufträge", n)
	}
}
