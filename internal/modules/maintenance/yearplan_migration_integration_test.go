//go:build integration

package maintenance

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

// Migration 213 auf Altdaten: Vorlauf in Arbeitstage umgerechnet, „anstehend ab“
// für offene Aufträge, neue Outlook-Art, ein geplanter Termin je Plan und Tag.
func TestYearplanMigration(t *testing.T) {
	admin := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	dbName := "pdh_mig_yp_" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
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
	copyMigrations(t, dir, 212)
	must(database.RunMigrations(ctx, pool, dir))
	id := func(q string, args ...any) string {
		t.Helper()
		var v string
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ('alt', 'alt@x', 'x', 'Alfred', 'Alt', '', '') RETURNING id::text`)
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ('Presse', 'plant') RETURNING id::text`)
	plan := func(name string, lead int) string {
		return id(`INSERT INTO maintenance_plans (name, type, infrastructure_id, interval_type, interval_days, next_due_at, created_by, lead_days)
			VALUES ($1, 'inspection', $2::uuid, 'monthly', 30, '2026-12-20', $3::uuid, $4) RETURNING id::text`, name, infra, user, lead)
	}
	pa, pb, pc := plan("A", 0), plan("B", 14), plan("C", 365)
	open := id(`INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES ($1::uuid, 'B offen', 'inspection', $2::uuid, 'medium', 'open', '2026-12-20', $3::uuid) RETURNING id::text`, pb, infra, user)
	done := id(`INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES ($1::uuid, 'B erledigt', 'inspection', $2::uuid, 'medium', 'done', '2026-11-20', $3::uuid) RETURNING id::text`, pb, infra, user)

	copyMigrations(t, dir, 213)
	must(database.RunMigrations(ctx, pool, dir))

	lead := func(p string) *int {
		var v *int
		must(pool.QueryRow(ctx, `SELECT lead_workdays FROM maintenance_plans WHERE id = $1::uuid`, p).Scan(&v))
		return v
	}
	if v := lead(pa); v != nil {
		t.Errorf("Plan A (0 Tage): %d, erwartet Vorgabe (NULL)", *v)
	}
	if v := lead(pb); v == nil || *v != 10 {
		t.Errorf("Plan B (14 Tage): %v, erwartet 10", v)
	}
	if v := lead(pc); v == nil || *v != 60 {
		t.Errorf("Plan C (365 Tage): %v, erwartet 60", v)
	}
	upcoming := func(task string) *time.Time {
		var v *time.Time
		must(pool.QueryRow(ctx, `SELECT upcoming_from FROM maintenance_tasks WHERE id = $1::uuid`, task).Scan(&v))
		return v
	}
	if v := upcoming(open); v == nil || v.Format("2006-01-02") != "2026-12-06" {
		t.Errorf("offener Auftrag: anstehend ab %v, erwartet 2026-12-06", v)
	}
	if v := upcoming(done); v != nil {
		t.Errorf("erledigter Auftrag: anstehend ab %v, erwartet NULL", v)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO microsoft_calendar_links (user_id, source_type, source_id, microsoft_event_id)
		VALUES ($1::uuid, 'maintenance_planned', gen_random_uuid(), 'e1')`, user); err != nil {
		t.Errorf("Outlook-Art maintenance_planned: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO microsoft_calendar_links (user_id, source_type, source_id, microsoft_event_id)
		VALUES ($1::uuid, 'foo', gen_random_uuid(), 'e2')`, user); err == nil {
		t.Error("unbekannte Outlook-Art muss scheitern")
	}

	visit := func(state string) error {
		_, err := pool.Exec(ctx, `INSERT INTO maintenance_visits (plan_id, calc_day, day, state) VALUES ($1::uuid, '2027-01-20', '2027-01-20', $2)`, pb, state)
		return err
	}
	must(visit("planned"))
	if err := visit("planned"); err == nil {
		t.Error("zweiter geplanter Termin am selben gerechneten Tag muss scheitern")
	}
	if err := visit("dropped"); err != nil {
		t.Errorf("entfallener Termin am selben Tag: %v", err)
	}
}

// Review Focus 5: Ältere Schnittstellen-Aufrufe mit lead_days bekommen den
// umgerechneten Vorlauf, nicht still die Vorgabe.
func TestPlanAPIAcceptsLeadDays(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	must(database.RunMigrations(ctx, pool, "../../../migrations"))
	repo := NewRepository(pool)
	svc := NewService(repo)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var user, infra string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', 'Lea', 'Lauf', '', '') RETURNING id::text`, "ld"+sfx).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Vorlauf '||$1::text, 'plant') RETURNING id::text`, sfx).Scan(&infra))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM maintenance_tasks WHERE infrastructure_id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM maintenance_plans WHERE infrastructure_id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, user)
	})
	in := &PlanInput{Name: "Vorlauf " + sfx, InfrastructureID: infra, IntervalUnit: UnitMonth, IntervalCount: 1,
		NextDueAt: time.Now().AddDate(0, 2, 0).Format("2006-01-02"), LeadDays: 14}
	id, err := svc.CreatePlan(ctx, in, user)
	must(err)
	p, err := svc.GetPlan(ctx, id)
	must(err)
	if p.LeadWorkdays == nil || *p.LeadWorkdays != 10 {
		t.Fatalf("lead_days 14: %v, erwartet 10 Arbeitstage", p.LeadWorkdays)
	}
	upd := &PlanInput{Name: in.Name, InfrastructureID: infra, IntervalUnit: UnitMonth, IntervalCount: 1, NextDueAt: in.NextDueAt}
	must(svc.UpdatePlan(ctx, id, upd, user))
	if p, _ = svc.GetPlan(ctx, id); p.LeadWorkdays != nil {
		t.Errorf("ohne Angabe gilt die Vorgabe (NULL), bekam %d", *p.LeadWorkdays)
	}
}
