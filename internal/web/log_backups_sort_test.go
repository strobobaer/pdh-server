package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackupsSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	tag := "bs" + e.sfx
	e.t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM backup_runs WHERE message = $1`, tag) })
	// Art (kind) als Text: manual (r1) < restore (r3) < scheduled (r2)
	for i, b := range []struct{ file, kind, by string }{{"r1", "manual", anna.ID}, {"r2", "scheduled", bert.ID}, {"r3", "restore", anna.ID}} {
		e.exec(`INSERT INTO backup_runs (file_name, kind, status, message, created_by, started_at) VALUES ($1, $2, 'ok', $3, $4::uuid, $5)`,
			b.file, b.kind, tag, b.by, logSeedAt(i))
	}
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r1|r3|r2"}} {
		req := httptest.NewRequest("GET", "/admin/backup?tab=log&ls_backups="+c.mode, nil)
		mode := e.h.sortMode(req, "backups")
		runs, _ := e.h.backupRuns(e.ctx, mode)
		var files []string
		for _, r := range runs {
			if r.Message == tag {
				files = append(files, r.File)
			}
		}
		if got := strings.Join(files, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		d := BackupPageData{Tab: "log", Runs: runs, RunsBar: e.h.sortBar(req, "backups", mode, req.URL.Path+"?tab=log", "")}
		logBarOK(t, renderPage(t, tmpl, "backup", d), "backups", c.mode)
	}
}
