//go:build integration

// Integrationstest Zeitstrahl: nicht zugewiesene Vorgaenge und gespeicherte
// Darstellung. Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Timeline ./internal/web/
package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimelineIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(q string, args ...interface{}) string {
		t.Helper()
		var v string
		must(pool.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	h := &Handler{db: pool}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Tim', 'Line', '', '') RETURNING id::text`, "tl"+sfx)
	grp := id(`INSERT INTO user_groups (name) VALUES ('Schicht '||$1::text) RETURNING id::text`, sfx)

	open := id(`INSERT INTO tickets (title, description, priority, status, created_by) VALUES ('offen', '', 'medium', 'open', $1::uuid) RETURNING id::text`, uid)
	person := id(`INSERT INTO tickets (title, description, priority, status, created_by, assigned_to) VALUES ('person', '', 'medium', 'open', $1::uuid, $1::uuid) RETURNING id::text`, uid)
	group := id(`INSERT INTO tickets (title, description, priority, status, created_by, assigned_group_id) VALUES ('gruppe', '', 'medium', 'open', $1::uuid, $2::uuid) RETURNING id::text`, uid, grp)
	done := id(`INSERT INTO tickets (title, description, priority, status, created_by) VALUES ('fertig', '', 'medium', 'resolved', $1::uuid) RETURNING id::text`, uid)
	task := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('aufgabe', '', 'medium', 'in_progress', $1::uuid) RETURNING id::text`, uid)
	taskA := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('mit Beteiligten', '', 'medium', 'open', $1::uuid) RETURNING id::text`, uid)
	_, err = pool.Exec(ctx, `INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid, $2::uuid)`, taskA, uid)
	must(err)

	set := h.unassignedRecords(ctx)
	for k, want := range map[string]bool{"ticket:" + open: true, "ticket:" + person: false, "ticket:" + group: false, "ticket:" + done: false, "task:" + task: true, "task:" + taskA: false} {
		if set[k] != want {
			t.Errorf("%s: nicht zugewiesen = %v, erwartet %v", k, set[k], want)
		}
	}

	// gespeicherte Darstellung wird geladen und geprueft
	must(h.setAppSetting(ctx, keyBrandTimeline, `{"width":4,"brightness":-20,"bar_height":30,"due_days":3,"running":"#123456","done":"#00aa00","done_fill":"#999999","due":"#ee0000","overdue":"kaputt","unassigned":"#aa00ff"}`))
	s := h.loadTimelineStyle(ctx)
	if s.Width != 4 || s.Brightness != -20 || s.BarHeight != 30 || s.DueDays != 3 || s.Running != "#123456" || s.Overdue != defaultTimelineStyle.Overdue {
		t.Errorf("geladen: %+v", s)
	}
	must(h.setAppSetting(ctx, keyBrandTimeline, ""))
	resetBrandingCache()
}
