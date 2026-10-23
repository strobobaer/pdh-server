//go:build integration

// Integrationstest Anlagen-Schnellübersicht gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run PlantWatch ./internal/web/
package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/infrastructure"
)

func TestPlantWatchIntegration(t *testing.T) {
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
	h := &Handler{db: pool, infra: infrastructure.NewService(infrastructure.NewRepository(pool))}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	newUser := func(name string) string {
		return id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', $2, 'W', 'technician', '', '') RETURNING id::text`, name+sfx, name)
	}
	owner, member, other := newUser("olga"), newUser("mia"), newUser("otto")
	group := id(`INSERT INTO user_groups (name) VALUES ('Kammern '||$1::text) RETURNING id::text`, sfx)
	_, err = pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, group, member)
	must(err)

	// Halle > Kammer (NOK-Motor) und Halle > Presse > Pumpe (kritische Störung)
	halle := id(`INSERT INTO infrastructure (name, type) VALUES ('Halle '||$1::text, 'building') RETURNING id::text`, sfx)
	kammer := id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ('Kammer '||$1::text, 'drying_chamber', $2::uuid) RETURNING id::text`, sfx, halle)
	presse := id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ('Presse '||$1::text, 'plant', $2::uuid) RETURNING id::text`, sfx, halle)
	pumpe := id(`INSERT INTO infrastructure (name, type, parent_id) VALUES ('Pumpe '||$1::text, 'device', $2::uuid) RETURNING id::text`, sfx, presse)
	_, err = pool.Exec(ctx, `INSERT INTO infra_components (infrastructure_id, kind, position, nok) VALUES ($1::uuid, 'motor', 1, false), ($1::uuid, 'motor', 2, true)`, kammer)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO faults (title, severity, status, infrastructure_id, created_by) VALUES ('Pumpe undicht', 'critical', 'detected', $1::uuid, $2::uuid)`, pumpe, owner)
	must(err)

	board := id(`INSERT INTO plant_watch_boards (name, created_by) VALUES ('Augenmerk '||$1::text, $2::uuid) RETURNING id::text`, sfx, owner)
	_, err = pool.Exec(ctx, `INSERT INTO plant_watch_items (board_id, infra_type) VALUES ($1::uuid, 'drying_chamber')`, board)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO plant_watch_items (board_id, infrastructure_id) VALUES ($1::uuid, $2::uuid)`, board, presse)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO plant_watch_access (board_id, group_id) VALUES ($1::uuid, $2::uuid)`, board, group)
	must(err)

	sees := func(uid string, manage bool) bool {
		for _, b := range h.watchBoards(ctx, uid, manage) {
			if b.ID == board {
				return true
			}
		}
		return false
	}
	if !sees(owner, false) || !sees(member, false) || sees(other, false) || !sees(other, true) {
		t.Fatalf("Sichtbarkeit: Ersteller %v, Gruppe %v, andere %v, Verwalter %v", sees(owner, false), sees(member, false), sees(other, false), sees(other, true))
	}

	b := watchBoard{ID: board}
	h.loadWatchContent(ctx, &b)
	body := h.watchBody(ctx, &b, false, "de")
	cards := map[string]watchCard{}
	for _, c := range body.Cards {
		cards[c.ID] = c
	}
	k, ok := cards[kammer]
	if !ok || k.NOK != 1 || k.State() != "red" || len(k.Chamber) == 0 || len(k.Chamber[0].Slots) != 2 {
		t.Errorf("Kammer: %+v", k)
	}
	p, ok := cards[presse]
	if !ok || p.Faults != 1 || p.Critical != 1 || p.State() != "red" || len(p.Children) != 1 || p.Children[0].State() != "red" {
		t.Errorf("Presse (Störung der Pumpe zählt mit): %+v", p)
	}
	if _, ok := cards[halle]; ok {
		t.Error("Halle ist nicht in der Übersicht")
	}

	_, err = pool.Exec(ctx, `UPDATE infra_components SET nok = false WHERE infrastructure_id = $1::uuid`, kammer)
	must(err)
	only := h.watchBody(ctx, &b, true, "de")
	for _, c := range only.Cards {
		if c.ID == kammer {
			t.Error("„Nur Auffälliges“ zeigt die wieder grüne Kammer")
		}
	}

	_, _ = pool.Exec(ctx, `DELETE FROM plant_watch_boards WHERE id = $1::uuid`, board)
	_, _ = pool.Exec(ctx, `DELETE FROM faults WHERE infrastructure_id = $1::uuid`, pumpe)
	_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = ANY($1::uuid[])`, []string{pumpe, presse, kammer, halle})
}
