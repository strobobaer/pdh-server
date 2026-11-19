package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanupSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	tag := "cs" + e.sfx
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM deletion_requests WHERE reason = $1`, tag)
		_, _ = e.pool.Exec(bg, `DELETE FROM cleanup_runs WHERE started_by IN ($1::uuid, $2::uuid)`, anna.ID, bert.ID)
	})
	// Vormerkungen: Art (Status) als Text: cancelled (r1) < deleted (r3) < hidden (r2)
	for i, d := range []struct{ title, status, by string }{{"r1", "cancelled", anna.ID}, {"r2", "hidden", bert.ID}, {"r3", "deleted", anna.ID}} {
		e.exec(`INSERT INTO deletion_requests (module, record_id, title, reason, status, requested_by, requested_at, processed_at)
			VALUES ('ticket', gen_random_uuid(), $1, $2, $3, $4::uuid, $5, $5)`, d.title, tag, d.status, d.by, logSeedAt(i))
	}
	// Läufe: Art = Ergebnis: Fehler (r1) < ausgeblendet (r3) < gelöscht (r2)
	for i, r := range []struct {
		summary             string
		deleted, hidden, fl int
		by                  string
	}{{"r1", 0, 0, 1, anna.ID}, {"r2", 2, 0, 0, bert.ID}, {"r3", 0, 3, 0, anna.ID}} {
		e.exec(`INSERT INTO cleanup_runs (started_by, started_at, deleted, hidden, failed, summary) VALUES ($1::uuid, $2, $3, $4, $5, $6)`,
			r.by, logSeedAt(i), r.deleted, r.hidden, r.fl, r.summary)
	}
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r1|r3|r2"}} {
		req := httptest.NewRequest("GET", "/admin/cleanup?tab=done&ls_cleanup="+c.mode, nil)
		mode := e.h.sortMode(req, "cleanup")
		var titles []string
		done, err := e.h.cleanupDone(e.ctx, mode)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range done {
			if r.Reason == tag {
				titles = append(titles, r.Title)
			}
		}
		if got := strings.Join(titles, "|"); got != c.want {
			t.Errorf("Vormerkungen %s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		var sums []string
		runs := e.h.cleanupRuns(e.ctx, mode)
		for _, r := range runs {
			if r.Summary == "r1" || r.Summary == "r2" || r.Summary == "r3" {
				sums = append(sums, r.Summary)
			}
		}
		if got := strings.Join(sums, "|"); got != c.want {
			t.Errorf("Läufe %s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		d := CleanupPageData{Tab: "done", Done: done, Runs: runs, CanRun: true, CleanupBar: e.h.sortBar(req, "cleanup", mode, req.URL.Path+"?tab=done", "")}
		out := renderPage(t, tmpl, "cleanup", d)
		logBarOK(t, out, "cleanup", c.mode)
		if n := strings.Count(out, `class="sortbar"`); n != 2 {
			t.Errorf("%s: %d Leisten, erwartet 2 (Läufe und Vormerkungen)", c.mode, n)
		}
	}
}
