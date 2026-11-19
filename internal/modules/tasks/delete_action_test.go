package tasks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Eine Maßnahme lässt sich nur über die Aufgabe löschen, zu der sie gehört.
func TestDeleteActionOtherParent(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	id := func(q string, args ...any) string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Dora', 'Delete', '', '') RETURNING id::text`, "dt"+sfx)
	t1 := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('Eins', 'x', 'medium', 'open', $1::uuid) RETURNING id::text`, uid)
	t2 := id(`INSERT INTO tasks (title, description, priority, status, created_by) VALUES ('Zwei', 'x', 'medium', 'open', $1::uuid) RETURNING id::text`, uid)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM tasks WHERE id IN ($1::uuid, $2::uuid)`, t1, t2)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	svc := NewService(NewRepository(pool))
	a, err := svc.AddAction(ctx, t1, "Filter gereinigt", uid)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_actions WHERE id = $1::uuid`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := svc.DeleteAction(ctx, t2, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("fremde Aufgabe: %v", err)
	}
	if count() != 1 {
		t.Fatal("Maßnahme über den Pfad einer anderen Aufgabe gelöscht")
	}
	r := chi.NewRouter()
	r.Delete("/{id}/actions/{actionID}", NewHandler(svc).DeleteAction)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+t2+"/actions/"+a.ID, nil))
	if w.Code != http.StatusNotFound || count() != 1 {
		t.Fatalf("Schnittstelle, fremde Aufgabe: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+t1+"/actions/"+a.ID, nil))
	if w.Code != http.StatusOK || count() != 0 {
		t.Fatalf("Schnittstelle, eigene Aufgabe: %d %s", w.Code, w.Body.String())
	}
	if err := svc.DeleteAction(ctx, t1, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("zweites Löschen: %v", err)
	}
}
