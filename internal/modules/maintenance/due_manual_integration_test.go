//go:build integration

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"pdh/pkg/database"
)

// Eine von Hand geänderte Fälligkeit wird gemerkt (due_manual), „anstehend ab“
// folgt dem neuen Tag (Vorlauf in Arbeitstagen).
func TestDueManual(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	must(database.RunMigrations(ctx, pool, "../../../migrations"))
	repo := NewRepository(pool)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var user, infra, plan, task, loose string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', 'Dora', 'Due', '', '') RETURNING id::text`, "dm"+sfx).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Fällig '||$1::text, 'plant') RETURNING id::text`, sfx).Scan(&infra))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM maintenance_tasks WHERE infrastructure_id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM maintenance_plans WHERE infrastructure_id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, user)
	})
	must(pool.QueryRow(ctx, `INSERT INTO maintenance_plans (name, type, infrastructure_id, interval_type, interval_days, next_due_at, created_by, lead_workdays)
		VALUES ('Fällig', 'inspection', $1::uuid, 'monthly', 30, '2026-11-30', $2::uuid, 5) RETURNING id::text`, infra, user).Scan(&plan))
	ins := `INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES (NULLIF($1, '')::uuid, 'Fällig', 'inspection', $2::uuid, 'medium', 'open', '2026-11-30', $3::uuid) RETURNING id::text`
	must(pool.QueryRow(ctx, ins, plan, infra, user).Scan(&task))
	must(pool.QueryRow(ctx, ins, "", infra, user).Scan(&loose))

	state := func(id string) (bool, *time.Time) {
		var manual bool
		var up *time.Time
		must(pool.QueryRow(ctx, `SELECT due_manual, upcoming_from FROM maintenance_tasks WHERE id = $1::uuid`, id).Scan(&manual, &up))
		return manual, up
	}
	must(repo.UpdateDueDate(ctx, task, time.Date(2026, 12, 14, 0, 0, 0, 0, time.Local)))
	if manual, up := state(task); !manual || up == nil || up.Format("2006-01-02") != "2026-12-07" {
		t.Fatalf("nach UpdateDueDate: von Hand %v, anstehend ab %v (erwartet 2026-12-07)", manual, up)
	}
	// gleiches Datum über UpdateTask: der Merker bleibt, wie er war
	_, err := pool.Exec(ctx, `UPDATE maintenance_tasks SET due_manual = false WHERE id = $1::uuid`, task)
	must(err)
	must(repo.UpdateTask(ctx, task, &UpdateTaskInput{Title: "Fällig", Priority: PrioMedium, DueDate: "2026-12-14"}))
	if manual, _ := state(task); manual {
		t.Error("UpdateTask mit gleichem Datum darf die Fälligkeit nicht als von Hand geändert merken")
	}
	must(repo.UpdateTask(ctx, task, &UpdateTaskInput{Title: "Fällig", Priority: PrioMedium, DueDate: "2026-12-21"}))
	if manual, up := state(task); !manual || up == nil || up.Format("2006-01-02") != "2026-12-14" {
		t.Errorf("UpdateTask mit neuem Datum: von Hand %v, anstehend ab %v (erwartet 2026-12-14)", manual, up)
	}
	// Auftrag ohne Plan: kein Vorlauf
	must(repo.UpdateDueDate(ctx, loose, time.Date(2026, 12, 14, 0, 0, 0, 0, time.Local)))
	if manual, up := state(loose); !manual || up != nil {
		t.Errorf("ohne Plan: von Hand %v, anstehend ab %v (erwartet NULL)", manual, up)
	}
}
