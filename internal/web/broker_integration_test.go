//go:build integration

// Integrationstest Broker-Verteilung (Leitstand) fuer alle Vorgangsarten.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Broker ./internal/web/
package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBrokerDispatchIntegration(t *testing.T) {
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
	user := func(name string, tasks, maint bool) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone, broker_tasks, broker_maintenance)
			VALUES ($1::text, $1::text || '@x', 'x', $2, 'Brk', '', '', $3, $4) RETURNING id::text`, name+sfx, name, tasks, maint)
	}
	// andere Test-Broker abschalten, damit nur unsere zaehlen
	_, err = pool.Exec(ctx, `UPDATE users SET broker_tasks = false, broker_maintenance = false`)
	must(err)
	reporter := user("melder", false, false)
	bt := user("btask", true, false)
	bm := user("bmaint", false, true)
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ('Linie B'||$1::text, 'plant') RETURNING id::text`, sfx)

	task := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('Leitstand-Aufgabe', 'x', 'medium', 'open', $1::uuid) RETURNING id::text`, reporter)
	got := h.dispatchToBrokers(ctx, "task", task, "Leitstand-Aufgabe", "high", "Linie B", reporter)
	if !strings.Contains(got, "An Broker verteilt") || !strings.Contains(got, "btask") || strings.Contains(got, "bmaint") {
		t.Fatalf("Aufgabe: %q", got)
	}
	var msgs int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages m JOIN chat_members c ON c.conversation_id = m.conversation_id
		WHERE c.user_id = $1::uuid AND m.body LIKE '%Leitstand-Aufgabe%'`, bt).Scan(&msgs))
	if msgs != 1 {
		t.Fatalf("Chat-Nachricht an Aufgaben-Broker: %d", msgs)
	}
	var hist string
	must(pool.QueryRow(ctx, `SELECT new_value FROM record_history WHERE ref_type = 'task' AND ref_id = $1::uuid AND action = 'broker'`, task).Scan(&hist))
	if !strings.Contains(hist, "btask") {
		t.Fatalf("Verlauf Aufgabe: %q", hist)
	}

	mt := id(`INSERT INTO maintenance_tasks (title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES ('Leitstand-Wartung', 'inspection', $1::uuid, 'medium', 'open', CURRENT_DATE + 7, $2::uuid) RETURNING id::text`, infra, reporter)
	if got := h.dispatchToBrokers(ctx, "maintenance_task", mt, "Leitstand-Wartung", "medium", "Linie B", reporter); !strings.Contains(got, "bmaint") || strings.Contains(got, "btask") {
		t.Fatalf("Wartung: %q", got)
	}
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages m JOIN chat_members c ON c.conversation_id = m.conversation_id
		WHERE c.user_id = $1::uuid AND m.body LIKE '%Leitstand-Wartung%'`, bm).Scan(&msgs))
	if msgs != 1 {
		t.Fatalf("Chat-Nachricht an Wartungs-Broker: %d", msgs)
	}

	// bereits zugewiesen (Aufgabe ueber Beteiligte): nichts zu verteilen
	task2 := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('Zugewiesen', 'x', 'medium', 'open', $1::uuid) RETURNING id::text`, reporter)
	_, err = pool.Exec(ctx, `INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid, $2::uuid)`, task2, bt)
	must(err)
	if got := h.dispatchToBrokers(ctx, "task", task2, "Zugewiesen", "low", "", reporter); got != "" {
		t.Fatalf("zugewiesene Aufgabe verteilt: %q", got)
	}
	// keine Broker: Vorgang bleibt unzugewiesen, Hinweis kommt zurueck
	_, err = pool.Exec(ctx, `UPDATE users SET broker_maintenance = false`)
	must(err)
	mt2 := id(`INSERT INTO maintenance_tasks (title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES ('Ohne Broker', 'inspection', $1::uuid, 'medium', 'open', CURRENT_DATE + 7, $2::uuid) RETURNING id::text`, infra, reporter)
	if got := h.dispatchToBrokers(ctx, "maintenance_task", mt2, "Ohne Broker", "low", "", reporter); !strings.Contains(got, "Kein Broker") {
		t.Fatalf("ohne Broker: %q", got)
	}
}
