package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/users"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Welcher Leitstand gilt für wen, und welche Vorgänge zeigt er (leitstand_boards.go).
func TestLeitstandResolutionIntegration(t *testing.T) {
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
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	parent := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Werk "+sfx)
	child := id(`INSERT INTO departments (name, parent_id) VALUES ($1, $2::uuid) RETURNING id::text`, "Halle "+sfx, parent)
	other := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Büro "+sfx)
	group := id(`INSERT INTO user_groups (name) VALUES ($1) RETURNING id::text`, "Team "+sfx)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, department_id)
		VALUES ($1::text, $1::text || '@x', 'x', 'L', 'S', 'user', '', '', $2::uuid) RETURNING id::text`, "ls"+sfx, child)
	stranger := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, department_id)
		VALUES ($1::text, $1::text || '@x', 'x', 'F', 'R', 'user', '', '', $2::uuid) RETURNING id::text`, "fr"+sfx, other)
	board := func(slug string, dept, grp any) string {
		return id(`INSERT INTO leitstaende (slug, name, department_id, group_id) VALUES ($1, $1, $2::uuid, $3::uuid) RETURNING id::text`, slug+sfx, dept, grp)
	}
	deptBoard := board("werk", parent, nil)
	groupBoard := board("team", nil, group)
	direct := board("terminal", nil, nil)
	inHall := id(`INSERT INTO faults (title, created_by, assigned_to) VALUES ('in der Halle', $1::uuid, $1::uuid) RETURNING id::text`, user)
	elsewhere := id(`INSERT INTO faults (title, created_by, assigned_to) VALUES ('im Büro', $1::uuid, $1::uuid) RETURNING id::text`, stranger)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM faults WHERE id IN ($1::uuid, $2::uuid)`, inHall, elsewhere)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid)`, user, stranger)
		_, _ = pool.Exec(ctx, `DELETE FROM leitstaende WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, deptBoard, groupBoard, direct)
		_, _ = pool.Exec(ctx, `DELETE FROM user_groups WHERE id = $1::uuid`, group)
		_, _ = pool.Exec(ctx, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, child, parent, other)
	}()
	h := &Handler{db: pool}
	u := &users.User{ID: user}
	got := func() string {
		if b := h.leitstandForUser(ctx, u); b != nil {
			return b.ID
		}
		return ""
	}
	// Abteilung der übergeordneten Ebene gilt auch für Unterabteilungen
	if g := got(); g != deptBoard {
		t.Errorf("über die Abteilung: %q, erwartet %q", g, deptBoard)
	}
	_, err = pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, group, user)
	must(err)
	if g := got(); g != groupBoard {
		t.Errorf("Gruppe geht vor Abteilung: %q, erwartet %q", g, groupBoard)
	}
	_, err = pool.Exec(ctx, `UPDATE users SET leitstand_id = $1::uuid WHERE id = $2::uuid`, direct, user)
	must(err)
	if g := got(); g != direct {
		t.Errorf("direkte Wahl geht vor: %q, erwartet %q", g, direct)
	}
	_, err = pool.Exec(ctx, `UPDATE leitstaende SET active = false WHERE id = $1::uuid`, direct)
	must(err)
	if g := got(); g != groupBoard {
		t.Errorf("abgeschalteter Leitstand wird übersprungen: %q, erwartet %q", g, groupBoard)
	}
	if g := (&Handler{db: pool}).leitstandForUser(ctx, &users.User{ID: stranger}); g != nil {
		t.Errorf("ohne Zuordnung kein eigener Leitstand (Standard greift): %q", g.ID)
	}

	// Vorgänge: der Abteilungs-Leitstand zeigt nur, was in seinen Bereich fällt
	ids := h.boardAllowedIDs(ctx, h.leitstandWhere(ctx, `l.id = $1::uuid`, deptBoard))
	if !ids["fault"][inHall] || ids["fault"][elsewhere] {
		t.Errorf("Filter Abteilung: in der Halle=%v, im Büro=%v", ids["fault"][inHall], ids["fault"][elsewhere])
	}
	ids = h.boardAllowedIDs(ctx, h.leitstandWhere(ctx, `l.id = $1::uuid`, groupBoard))
	if !ids["fault"][inHall] || ids["fault"][elsewhere] {
		t.Errorf("Filter Gruppe: in der Halle=%v, im Büro=%v", ids["fault"][inHall], ids["fault"][elsewhere])
	}
	if h.boardAllowedIDs(ctx, h.defaultLeitstand(ctx)) != nil {
		t.Error("der Standard-Leitstand filtert nicht")
	}

	// Terminal startet auf seinem Leitstand, ?dash=1 zeigt die Startseite
	start := func(path string, system bool) string {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: user, IsSystemUser: system}))
		return h.terminalLeitstandStart(r)
	}
	slug := h.leitstandWhere(ctx, `l.id = $1::uuid`, groupBoard).Slug
	if g := start("/", true); g != "/global/b/"+slug {
		t.Errorf("Terminal-Start: %q", g)
	}
	if g := start("/?dash=1", true); g != "" {
		t.Errorf("Terminal mit ?dash=1: %q", g)
	}
	if g := start("/", false); g != "" {
		t.Errorf("normaler Benutzer wird nicht umgeleitet: %q", g)
	}
}
