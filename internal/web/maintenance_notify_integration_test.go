//go:build integration

package web

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/modules/maintenance"
)

func TestPlanTaskRecipients(t *testing.T) {
	f := newJPWeb(t)
	ctx := context.Background()
	p, q, inactive := f.user("vf_p", f.role("vf_pr", nil)), f.user("vf_q", f.role("vf_qr", nil)), f.user("vf_r", f.role("vf_rr", nil))
	f.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, inactive)
	for _, u := range []string{p, q, inactive} {
		f.exec(`INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, f.groupA, u)
	}
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM user_group_members WHERE group_id = $1::uuid`, f.groupA)
	})
	task := func(assigned, group string) string {
		return f.id(`INSERT INTO maintenance_tasks (title, type, infrastructure_id, priority, status, due_date, created_by, assigned_to, assigned_group_id)
			VALUES ('Empfänger', 'inspection', $1::uuid, 'medium', 'open', CURRENT_DATE + 7, $2::uuid, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid) RETURNING id::text`,
			f.infraA, f.creator, assigned, group)
	}
	withBoth := task(p, f.groupA)
	none := task("", "")
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM maintenance_tasks WHERE id::text = ANY($1)`, []string{withBoth, none})
	})
	got := f.h.planTaskRecipients(ctx, withBoth, "")
	sort.Strings(got)
	want := []string{p, q}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Empfänger: %v, erwartet %v", got, want)
	}
	if got := f.h.planTaskRecipients(ctx, withBoth, q); len(got) != 1 || got[0] != p {
		t.Errorf("ohne Auslöser Q: %v", got)
	}
	if got := f.h.planTaskRecipients(ctx, none, ""); len(got) != 0 {
		t.Errorf("ohne Person und Gruppe: %v", got)
	}
}

func TestNotifyPlanTaskCreatedOnce(t *testing.T) {
	f := newJPWeb(t)
	ctx := context.Background()
	p := f.user("vf_np", f.role("vf_npr", nil))
	var on atomic.Bool
	on.Store(true)
	maintenance.OnPlanTaskCreated(func(ctx context.Context, taskID, actorID string) {
		if on.Load() {
			f.h.notifyPlanTaskCreated(ctx, taskID, actorID)
		}
	})
	t.Cleanup(func() { on.Store(false) })

	title := "Benachrichtigung " + f.sfx
	id, err := f.h.maint.CreatePlan(ctx, &maintenance.PlanInput{Name: title, InfrastructureID: f.infraA, IntervalUnit: "month", IntervalCount: 1,
		ScheduleMode: maintenance.ScheduleFixed, NextDueAt: time.Now().AddDate(0, 0, 1).Format("2006-01-02"), AssignedTo: &p}, f.creator)
	f.must(err)
	f.plans = append(f.plans, id)
	var taskID string
	var due time.Time
	f.must(f.pool.QueryRow(ctx, `SELECT id::text, due_date FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, id).Scan(&taskID, &due))

	count := func() (n int, body string) {
		f.must(f.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(MAX(m.body), '') FROM chat_messages m JOIN chat_members c ON c.conversation_id = m.conversation_id
			WHERE c.user_id = $1::uuid AND m.user_id = $2::uuid AND m.body LIKE '%' || $3 || '%'`, p, pdhSystemUserID, f.sfx).Scan(&n, &body))
		return n, body
	}
	n, body := count()
	if n != 1 {
		t.Fatalf("Nachrichten an die zugewiesene Person: %d", n)
	}
	var taskTitle string
	f.must(f.pool.QueryRow(ctx, `SELECT title FROM maintenance_tasks WHERE id = $1::uuid`, taskID).Scan(&taskTitle))
	if want := maintAssignedText(taskTitle, due, taskID); body != want {
		t.Errorf("Text: %q, erwartet %q", body, want)
	}
	// Anlege-Hinweis aus der Warteschlange: für Aufträge aus Plänen keine zweite Nachricht
	f.exec(`UPDATE record_change_queue SET created_at = NOW() - INTERVAL '1 minute' WHERE record_id = $1::uuid`, taskID)
	f.must(f.h.processChangeQueue(ctx))
	if n, _ := count(); n != 1 {
		t.Errorf("nach der Warteschlange: %d Nachrichten", n)
	}
}

func TestTaskDetailNextTerm(t *testing.T) {
	f := newJPWeb(t)
	ctx := context.Background()
	id := f.plan(f.infraA, time.Now().AddDate(0, 0, 1), nil)
	var taskID string
	f.must(f.pool.QueryRow(ctx, `SELECT id::text FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, id).Scan(&taskID))
	f.exec(`UPDATE maintenance_tasks SET status = 'done', completed_at = NOW() WHERE id = $1::uuid`, taskID)
	if _, err := f.h.maint.Repo().ScheduleNext(ctx, taskID, f.creator, true); err != nil {
		t.Fatal(err)
	}
	d := MaintenanceTaskDetailData{}
	f.h.maintDetailNext(ctx, &d, taskID)
	if d.NextTaskID != "" || d.NextDay == "" || d.NextOrderOn == "" {
		t.Errorf("erledigt ohne Folgeauftrag: %+v", d)
	}
}
