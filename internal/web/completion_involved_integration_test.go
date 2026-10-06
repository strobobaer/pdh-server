//go:build integration

// Fertigmelden ohne Rollenrecht: nur wer beteiligt ist (oder bei niemandem zugewiesen).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run CompletionInvolved ./internal/web/
package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/users"
)

func TestCompletionInvolvedIntegration(t *testing.T) {
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
	mk := func(name, role string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', $1::text, 'T', $2, '', '') RETURNING id::text`, name+sfx, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tech, other, viewer := mk("tech", "technician"), mk("other", "technician"), mk("view", "viewer")
	var infra, assigned, free string
	pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('I '||$1::text, 'plant') RETURNING id::text`, sfx).Scan(&infra)
	pool.QueryRow(ctx, `INSERT INTO maintenance_tasks (title, type, priority, status, due_date, created_by, infrastructure_id, assigned_to)
		VALUES ('zugewiesen', 'inspection', 'medium', 'open', NOW(), $1::uuid, $2::uuid, $1::uuid) RETURNING id::text`, tech, infra).Scan(&assigned)
	pool.QueryRow(ctx, `INSERT INTO maintenance_tasks (title, type, priority, status, due_date, created_by, infrastructure_id)
		VALUES ('frei', 'inspection', 'medium', 'open', NOW(), $1::uuid, $2::uuid) RETURNING id::text`, tech, infra).Scan(&free)
	k := completionKinds["maintenance"]
	check := func(uid, role, task string, want bool) {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: uid, Role: users.Role(role)}))
		if got := h.completionInvolved(req, k, task); got != want {
			t.Errorf("user %s (%s) bei %s: %v, erwartet %v", uid[:8], role, task[:8], got, want)
		}
	}
	check(tech, "technician", assigned, true)   // zugewiesen
	check(other, "technician", assigned, false) // fremder Auftrag
	check(other, "technician", free, true)      // niemand zugewiesen → darf übernehmen
	check(viewer, "viewer", free, false)        // Betrachter nie
}
