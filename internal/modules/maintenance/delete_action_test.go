package maintenance

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

// Eine Maßnahme lässt sich nur über den Wartungsauftrag löschen, zu dem sie gehört.
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
		VALUES ($1::text, $1::text || '@x', 'x', 'Dora', 'Delete', '', '') RETURNING id::text`, "dm"+sfx)
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ('Linie '||$1::text, 'plant') RETURNING id::text`, sfx)
	mk := func(title string) string {
		return id(`INSERT INTO maintenance_tasks (title, type, infrastructure_id, priority, status, due_date, created_by)
			VALUES ($1, 'preventive', $2::uuid, 'medium', 'open', NOW(), $3::uuid) RETURNING id::text`, title, infra, uid)
	}
	m1, m2 := mk("Eins"), mk("Zwei")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM maintenance_tasks WHERE id IN ($1::uuid, $2::uuid)`, m1, m2)
		_, _ = pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	svc := NewService(NewRepository(pool))
	a, err := svc.AddAction(ctx, m1, "Filter gereinigt", uid)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_actions WHERE id = $1::uuid`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := svc.DeleteAction(ctx, m2, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("fremder Auftrag: %v", err)
	}
	if count() != 1 {
		t.Fatal("Maßnahme über den Pfad eines anderen Auftrags gelöscht")
	}
	r := chi.NewRouter()
	r.Delete("/tasks/{id}/actions/{actionID}", NewHandler(svc).DeleteAction)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/tasks/"+m2+"/actions/"+a.ID, nil))
	if w.Code != http.StatusNotFound || count() != 1 {
		t.Fatalf("Schnittstelle, fremder Auftrag: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/tasks/"+m1+"/actions/"+a.ID, nil))
	if w.Code != http.StatusOK || count() != 0 {
		t.Fatalf("Schnittstelle, eigener Auftrag: %d %s", w.Code, w.Body.String())
	}
	if err := svc.DeleteAction(ctx, m1, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("zweites Löschen: %v", err)
	}
}
