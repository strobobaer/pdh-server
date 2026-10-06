//go:build integration

// Integrationstest „Warten“ am Leitstand gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run BoardWaiting ./internal/web/
package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBoardWaitingSetsPendingIntegration(t *testing.T) {
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
	h := &Handler{db: pool}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var userID, faultID, maintID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
		VALUES ($1::text, $1::text || '@x', 'x', 'Wa', 'Rte') RETURNING id::text`, "wt"+sfx).Scan(&userID))
	must(pool.QueryRow(ctx, `INSERT INTO faults (title, description, severity, status, created_by)
		VALUES ('Wartet '||$1::text, 'x', 'medium', 'in_progress', $2::uuid) RETURNING id::text`, sfx, userID).Scan(&faultID))
	must(pool.QueryRow(ctx, `INSERT INTO maintenance_tasks (title, type, priority, status, due_date, created_by)
		VALUES ('Wartet '||$1::text, 'inspection', 'medium', 'open', NOW(), $2::uuid) RETURNING id::text`, sfx, userID).Scan(&maintID))

	r := httptest.NewRequest("POST", "/global/actions", nil)
	until := time.Now().AddDate(0, 0, 3)
	for typ, id := range map[string]string{"fault": faultID, "maintenance": maintID} {
		must(h.setBoardWaiting(r, typ, id, userID, until))
	}
	var fs, ms string
	must(pool.QueryRow(ctx, `SELECT status::text FROM faults WHERE id=$1::uuid`, faultID).Scan(&fs))
	must(pool.QueryRow(ctx, `SELECT status::text FROM maintenance_tasks WHERE id=$1::uuid`, maintID).Scan(&ms))
	if fs != "pending" || ms != "pending" {
		t.Fatalf("Status nach Warten: Störung %s, Wartung %s", fs, ms)
	}
	// wartende Vorgaenge bleiben am Leitstand und gelten als offen
	if !h.globalBoardRecordActive(r, "fault", faultID) || !h.globalBoardRecordActive(r, "maintenance", maintID) {
		t.Fatal("wartende Vorgänge müssen offen bleiben")
	}
	// Annehmen holt sie zurück in Arbeit
	must(h.applyGlobalBoardAction(r, GlobalBoardActionInput{Type: "maintenance", ID: maintID, Action: "accept", AssignedTo: userID}, userID, nil))
	must(pool.QueryRow(ctx, `SELECT status::text FROM maintenance_tasks WHERE id=$1::uuid`, maintID).Scan(&ms))
	if ms != "in_progress" {
		t.Fatalf("nach Annehmen: %s", ms)
	}
}
