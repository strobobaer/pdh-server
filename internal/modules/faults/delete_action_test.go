package faults

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

// Eine Maßnahme lässt sich nur über die Störung löschen, zu der sie gehört.
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
		VALUES ($1::text, $1::text || '@x', 'x', 'Dora', 'Delete', '', '') RETURNING id::text`, "da"+sfx)
	f1 := id(`INSERT INTO faults (title, created_by) VALUES ('Eins', $1::uuid) RETURNING id::text`, uid)
	f2 := id(`INSERT INTO faults (title, created_by) VALUES ('Zwei', $1::uuid) RETURNING id::text`, uid)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM faults WHERE id IN ($1::uuid, $2::uuid)`, f1, f2)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	svc := NewService(NewRepository(pool), nil)
	a, err := svc.AddAction(ctx, f1, "Filter gereinigt", uid)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM fault_actions WHERE id = $1::uuid`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// über den Pfad einer anderen Störung: nicht gefunden, die Maßnahme bleibt
	if err := svc.DeleteAction(ctx, f2, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("fremde Störung: %v", err)
	}
	if count() != 1 {
		t.Fatal("Maßnahme über den Pfad einer anderen Störung gelöscht")
	}
	// die Schnittstelle antwortet 404 und lässt sie stehen
	r := chi.NewRouter()
	r.Delete("/{id}/actions/{actionID}", NewHandler(svc).DeleteAction)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+f2+"/actions/"+a.ID, nil))
	if w.Code != http.StatusNotFound || count() != 1 {
		t.Fatalf("Schnittstelle, fremde Störung: %d %s", w.Code, w.Body.String())
	}
	// über die eigene Störung geht es
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+f1+"/actions/"+a.ID, nil))
	if w.Code != http.StatusOK || count() != 0 {
		t.Fatalf("Schnittstelle, eigene Störung: %d %s", w.Code, w.Body.String())
	}
	// und ein zweites Mal: nicht mehr da
	if err := svc.DeleteAction(ctx, f1, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("zweites Löschen: %v", err)
	}
}
