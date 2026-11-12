//go:build integration

// Kartensitzung des Anlagenterminals: Speicher und Rechteprüfung gegen die Kartenperson.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalCard ./internal/web/
package web

import (
	"testing"

	"pdh/internal/core/users"
)

func newCardHandler(e *atEnv) *Handler {
	us := users.NewService(users.NewRepository(e.pool), "test-secret", 1)
	return &Handler{db: e.pool, rbac: e.rb, users: us}
}

func cardRows(e *atEnv, terminal string) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, terminal).Scan(&n))
	return n
}

func TestTerminalCardSessionLifecycle(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	term := e.user("kterm", "user", true)
	p1 := e.user("kp1", "user", false)
	p2 := e.user("kp2", "user", false)
	t.Cleanup(func() { e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term) })

	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("ohne Sitzung: %v %v", p, err)
	}
	e.must(h.cardSessionStart(e.ctx, term, p1))
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p == nil || p.ID != p1 {
		t.Fatalf("Person: %v %v", p, err)
	}

	// Zweiter Start mit anderer Person ersetzt.
	e.must(h.cardSessionStart(e.ctx, term, p2))
	if p, _ := h.cardSessionPerson(e.ctx, term); p == nil || p.ID != p2 {
		t.Fatalf("ersetzt: %v", p)
	}
	if cardRows(e, term) != 1 {
		t.Fatal("genau eine Zeile erwartet")
	}

	// Touch verlängert eine gültige Sitzung.
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '100 seconds' WHERE terminal_user_id = $1::uuid`, term)
	if ok, err := h.cardSessionTouch(e.ctx, term); err != nil || !ok {
		t.Fatalf("touch: %v %v", ok, err)
	}
	var age float64
	e.must(e.pool.QueryRow(e.ctx, `SELECT extract(epoch FROM now()-last_seen_at) FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term).Scan(&age))
	if age > 10 {
		t.Fatalf("last_seen_at nicht verlängert: %v s", age)
	}

	// Abgelaufen: nil, Zeile gelöscht, Touch false.
	e.exec(`UPDATE terminal_card_sessions SET last_seen_at = now() - interval '121 seconds' WHERE terminal_user_id = $1::uuid`, term)
	if ok, _ := h.cardSessionTouch(e.ctx, term); ok {
		t.Fatal("Touch darf abgelaufene Sitzung nicht verlängern")
	}
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("abgelaufen: %v %v", p, err)
	}
	if cardRows(e, term) != 0 {
		t.Fatal("abgelaufene Zeile nicht gelöscht")
	}

	// Inaktive Person: nil.
	e.must(h.cardSessionStart(e.ctx, term, p1))
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, p1)
	if p, err := h.cardSessionPerson(e.ctx, term); err != nil || p != nil {
		t.Fatalf("inaktiv: %v %v", p, err)
	}

	// End löscht.
	e.must(h.cardSessionStart(e.ctx, term, p2))
	e.must(h.cardSessionEnd(e.ctx, term))
	if cardRows(e, term) != 0 {
		t.Fatal("End hat nicht gelöscht")
	}
	if p, _ := h.cardSessionPerson(e.ctx, term); p != nil {
		t.Fatal("nach End keine Person")
	}
}

func TestTerminalCardPersonCan(t *testing.T) {
	e := newATEnv(t)
	h := newCardHandler(e)
	term := e.user("kcterm", "user", true)
	person := e.user("kcperson", "user", false)
	dx, dy := e.dept("kcx"), e.dept("kcy")
	ax := e.infra("kca-x", dx, "")
	ay := e.infra("kca-y", dy, "")
	e.grant(person, "faults.view", dx, true)
	e.warm()

	pu, err := h.userByID(e.ctx, person)
	e.must(err)
	tu, err := h.userByID(e.ctx, term)
	e.must(err)
	if !h.cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ax) {
		t.Fatal("Anlage in X: erwartet true")
	}
	if h.cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ay) {
		t.Fatal("Anlage in Y: erwartet false")
	}
	if h.cardPersonCan(e.ctx, tu, "faults.view", "infrastructure", ax) {
		t.Fatal("Terminal-Benutzer selbst: erwartet false")
	}
	if (&Handler{}).cardPersonCan(e.ctx, pu, "faults.view", "infrastructure", ax) {
		t.Fatal("ohne rbac: erwartet false")
	}
}
