//go:build integration

// Integrationstests gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration ./internal/modules/maintenance/
package maintenance

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustT(t *testing.T) func(error) {
	return func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// copyMigrations kopiert die Migrationen bis einschliesslich maxVersion.
func copyMigrations(t *testing.T, dst string, maxVersion int) {
	t.Helper()
	src := filepath.Join("..", "..", "..", "migrations")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		v := 0
		for _, c := range name {
			if c < '0' || c > '9' {
				break
			}
			v = v*10 + int(c-'0')
		}
		if v > maxVersion {
			continue
		}
		in, _ := os.Open(filepath.Join(src, name))
		out, _ := os.Create(filepath.Join(dst, name))
		_, _ = io.Copy(out, in)
		in.Close()
		out.Close()
	}
}

// Altdaten (Stand 100) → Migration 101: Plaene, Takt-Aufteilung, Ergebnisse
// als Schritte mit gleicher id (Fotos bleiben), Verlauf vereinheitlicht.
func TestMigration101WithOldData(t *testing.T) {
	admin := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	dbName := "pdh_mig_" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	_, err := admin.Exec(ctx, "CREATE DATABASE "+dbName)
	must(err)
	u, _ := url.Parse(os.Getenv("PDH_TEST_DSN"))
	u.Path = "/" + dbName
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	}()

	dir := t.TempDir()
	copyMigrations(t, dir, 100)
	must(database.RunMigrations(ctx, pool, dir))

	// ── Altdaten wie vor dem Umbau ──
	id := func(q string, args ...interface{}) string {
		t.Helper()
		var v string
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ('alt', 'alt@x', 'x', 'Alfred', 'Alt', '', '') RETURNING id::text`)
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ('Presse', 'plant') RETURNING id::text`)
	plan := id(`INSERT INTO maintenance_plans (name, type, infrastructure_id, interval_type, interval_days, next_due_at, created_by, default_duration_min)
		VALUES ('Wochenprüfung', 'inspection', $1::uuid, 'weekly', 7, NOW(), $2::uuid, 45) RETURNING id::text`, infra, user)
	tpl := id(`INSERT INTO maintenance_checklist_templates (name, created_by) VALUES ('Presse prüfen', $1::uuid) RETURNING id::text`, user)
	itemA := id(`INSERT INTO maintenance_checklist_template_items (template_id, label, item_type, required, interval_days, sort_order)
		VALUES ($1::uuid, 'Öl prüfen', 'checkbox', true, 7, 1) RETURNING id::text`, tpl)
	itemB := id(`INSERT INTO maintenance_checklist_template_items (template_id, label, item_type, required, interval_days, sort_order, unit, min_value, max_value)
		VALUES ($1::uuid, 'Druck', 'number', true, 7, 2, 'bar', 5, 7) RETURNING id::text`, tpl)
	itemC := id(`INSERT INTO maintenance_checklist_template_items (template_id, label, item_type, required, interval_days, sort_order)
		VALUES ($1::uuid, 'Keilriemen tauschen', 'checkbox', true, 90, 3) RETURNING id::text`, tpl)
	_, err = pool.Exec(ctx, `INSERT INTO maintenance_plan_checklist_templates (plan_id, template_id) VALUES ($1::uuid, $2::uuid)`, plan, tpl)
	must(err)
	done := id(`INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, status, due_date, completed_at, created_by)
		VALUES ($1::uuid, 'Wochenprüfung', 'inspection', $2::uuid, 'done', NOW() - INTERVAL '10 days', NOW() - INTERVAL '10 days', $3::uuid) RETURNING id::text`, plan, infra, user)
	resA := id(`INSERT INTO maintenance_task_checklist_results (task_id, template_item_id, value, done, checked_by, checked_at)
		VALUES ($1::uuid, $2::uuid, 'erledigt', true, $3::uuid, NOW() - INTERVAL '10 days') RETURNING id::text`, done, itemA, user)
	_ = id(`INSERT INTO maintenance_task_checklist_results (task_id, template_item_id, value, done, in_range, checked_by, checked_at)
		VALUES ($1::uuid, $2::uuid, '7,8', true, false, $3::uuid, NOW() - INTERVAL '10 days') RETURNING id::text`, done, itemB, user)
	_ = id(`INSERT INTO maintenance_task_checklist_results (task_id, template_item_id, value, done, checked_by, checked_at)
		VALUES ($1::uuid, $2::uuid, 'erledigt', true, $3::uuid, NOW() - INTERVAL '10 days') RETURNING id::text`, done, itemC, user)
	photo := id(`INSERT INTO attachments (ref_type, ref_id, filename, filepath, mimetype, created_by)
		VALUES ('maint_check_result', $1::uuid, 'f.jpg', 'maint_check_result/x/f.jpg', 'image/jpeg', $2::uuid) RETURNING id::text`, resA, user)
	open := id(`INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, status, due_date, created_by)
		VALUES ($1::uuid, 'Wochenprüfung', 'inspection', $2::uuid, 'open', NOW(), $3::uuid) RETURNING id::text`, plan, infra, user)
	_, err = pool.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, message, created_by) VALUES ('maintenance', $1::uuid, 'work', 'alt', $2::uuid)`, done, user)
	must(err)

	// ── Migration 101 ──
	copyMigrations(t, dir, 102) // inkl. 102 (Rundgang), das Repository braucht die neuen Spalten
	must(database.RunMigrations(ctx, pool, dir))

	var unit string
	var count, est int
	must(pool.QueryRow(ctx, `SELECT interval_unit, interval_count, estimated_min FROM maintenance_plans WHERE id=$1::uuid`, plan).Scan(&unit, &count, &est))
	if unit != UnitWeek || count != 1 || est != 45 {
		t.Fatalf("Plan: %s×%d, Dauer %d", unit, count, est)
	}
	repo := NewRepository(pool)
	lists, err := repo.PlanChecklists(ctx, plan)
	must(err)
	if len(lists) != 2 || lists[0].TemplateID != tpl || lists[0].RhythmUnit != RhythmAlways ||
		lists[1].RhythmUnit != UnitDay || lists[1].RhythmCount != 90 || lists[1].LastDoneAt == nil || !strings.Contains(lists[1].TemplateName, "90 Tage") {
		t.Fatalf("Takt-Checklisten: %+v / %+v", lists[0], lists[len(lists)-1])
	}
	var cTpl string
	must(pool.QueryRow(ctx, `SELECT template_id::text FROM maintenance_checklist_template_items WHERE id=$1::uuid`, itemC).Scan(&cTpl))
	if cTpl != lists[1].TemplateID {
		t.Fatal("Keilriemen-Punkt nicht in die 90-Tage-Checkliste gewandert")
	}
	steps, err := repo.Steps(ctx, done)
	must(err)
	if len(steps) != 3 || steps[0].ID != resA || len(steps[0].DocImages) != 1 || steps[0].DocImages[0].ID != photo ||
		steps[1].Value != "7,8" || steps[1].InRange == nil || *steps[1].InRange || steps[0].CheckedBy != "Alfred Alt" {
		t.Fatalf("Schritte aus Ergebnissen: %d / %+v", len(steps), steps[0])
	}
	for _, gone := range []string{"maintenance_task_checklist_results", "maintenance_plan_checklist_templates"} {
		var exists bool
		must(pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, gone).Scan(&exists))
		if exists {
			t.Errorf("%s sollte entfernt sein", gone)
		}
	}
	var hist int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM record_history WHERE ref_type='maintenance_task' AND ref_id=$1::uuid`, done).Scan(&hist))
	if hist != 1 {
		t.Fatalf("Verlauf vereinheitlicht: %d", hist)
	}
	// offener Auftrag: heute faellig → nur „immer“ (Keilriemen erst in 80 Tagen wieder)
	n, err := repo.BuildSteps(ctx, open, false)
	must(err)
	if n != 2 {
		t.Fatalf("offener Auftrag: %d Schritte, erwartet 2 (Keilriemen nicht fällig)", n)
	}
}
