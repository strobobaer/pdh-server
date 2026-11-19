package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestSyncRunsSort(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "herein")
	// Richtung als Art: herein (r1) < hinaus (r3) < test (r2)
	for i, r := range []struct{ reason, dir string }{{"r1", "herein"}, {"r2", "test"}, {"r3", "hinaus"}} {
		if _, err := h.db.Exec(t.Context(), `INSERT INTO sync_runs (profile_id, direction, started_at, skipped_reason) VALUES ($1::uuid, $2, $3, $4)`,
			id, r.dir, logSeedAt(i), r.reason); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ query, want string }{
		{"", "r3|r2|r1"}, {"?ls_sync=new", "r3|r2|r1"}, {"?ls_sync=old", "r1|r2|r3"}, {"?ls_sync=kind", "r1|r3|r2"},
		{"?ls_sync=person", "r3|r2|r1"}, // „Nach Person“ gibt es für Abgleich-Läufe nicht: gilt als nicht gesetzt
	} {
		w := syncDo(h.SyncRunsFragment, "GET", "/sync/"+id+"/runs"+c.query, id, nil, true)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.query, w.Code, w.Body.String())
		}
		out := w.Body.String()
		if got := logOrder(out); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.query, got, c.want)
		}
		mode := strings.TrimPrefix(c.query, "?ls_sync=")
		if mode == "" || mode == "person" {
			mode = "new"
		}
		logBarOK(t, out, "sync", mode)
		// htmx-Leiste: tauscht den Lauf-Block selbst aus
		if !strings.Contains(out, `hx-target="#sy-runs"`) || !strings.Contains(out, `id="sy-runs"`) {
			t.Errorf("%s: Leiste tauscht nicht den Block #sy-runs aus", c.query)
		}
	}
}
