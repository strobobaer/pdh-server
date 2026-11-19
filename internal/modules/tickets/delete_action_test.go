package tickets

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Eine Maßnahme lässt sich nur über das Ticket löschen, zu dem sie gehört.
func TestDeleteActionOtherParent(t *testing.T) {
	e := rcSetup(t)
	t1 := e.id(`INSERT INTO tickets (title, created_by) VALUES ('Eins', $1::uuid) RETURNING id::text`, e.uid)
	t2 := e.id(`INSERT INTO tickets (title, created_by) VALUES ('Zwei', $1::uuid) RETURNING id::text`, e.uid)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM tickets WHERE id IN ($1::uuid, $2::uuid)`, t1, t2)
	})
	a, err := e.svc.AddAction(e.ctx, t1, "Filter gereinigt", e.uid)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM ticket_actions WHERE id = $1::uuid`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := e.svc.DeleteAction(e.ctx, t2, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("fremdes Ticket: %v", err)
	}
	if count() != 1 {
		t.Fatal("Maßnahme über den Pfad eines anderen Tickets gelöscht")
	}
	r := chi.NewRouter()
	r.Delete("/{id}/actions/{actionID}", NewHandler(e.svc).DeleteAction)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+t2+"/actions/"+a.ID, nil))
	if w.Code != http.StatusNotFound || count() != 1 {
		t.Fatalf("Schnittstelle, fremdes Ticket: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+t1+"/actions/"+a.ID, nil))
	if w.Code != http.StatusOK || count() != 0 {
		t.Fatalf("Schnittstelle, eigenes Ticket: %d %s", w.Code, w.Body.String())
	}
	if err := e.svc.DeleteAction(e.ctx, t1, a.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("zweites Löschen: %v", err)
	}
}
