package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/internal/core/listsort"
	"pdh/internal/core/users"
)

func TestMovesSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	part := e.row("spare_parts", `INSERT INTO spare_parts (part_number, name, created_by) VALUES ($2, 'S-Teil', $1::uuid) RETURNING id::text`, anna.ID, "S"+e.sfx)
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM stock_movements WHERE part_id = $1::uuid`, part)
	})
	for i, m := range []struct {
		ref, typ string
		u        *users.User
	}{{"r1", "in", anna}, {"r2", "out", bert}, {"r3", "correction", anna}} {
		e.exec(`INSERT INTO stock_movements (part_id, type, qty, qty_before, qty_after, reference, created_by, created_at)
			VALUES ($1::uuid, $2::stock_movement_type, 1, 0, 1, $3, $4::uuid, $5)`, part, m.typ, m.ref, m.u.ID, logSeedAt(i))
	}
	// Art als Text: correction (r3) < in (r1) < out (r2)
	cases := []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r3|r1|r2"}}
	tmpl := loadTestTemplates(t)
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/inventory/"+part+"?tab=stock&ls_moves="+c.mode, nil)
		mode := e.h.sortMode(req, "moves")
		list := e.h.partMovements(e.ctx, part, map[string]string{}, 150, mode)
		var refs []string
		for _, m := range list {
			refs = append(refs, m.Reference)
		}
		if got := strings.Join(refs, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		bar := e.h.sortBar(req, "moves", mode, req.URL.Path+"?tab=stock", "")
		logBarOK(t, renderPage(t, tmpl, "inventory_detail", PartDetailData{Tab: "stock", Movements: list, RecentMoves: list, MovesBar: bar}), "moves", c.mode)
		logBarOK(t, renderPage(t, tmpl, "storage_detail", StorageDetailData{Movements: list, MovesBar: bar}), "moves", c.mode)
	}
	// „Letzte Bewegungen“ bleibt unabhängig von der Wahl neueste zuerst
	recent := e.h.partMovements(e.ctx, part, map[string]string{}, 5, listsort.Newest)
	if len(recent) != 3 || recent[0].Reference != "r3" {
		t.Errorf("Letzte Bewegungen: %+v", recent)
	}
}
