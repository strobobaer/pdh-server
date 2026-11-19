package web

import (
	"strings"
	"testing"
)

func TestExportRunsSort(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{"ftp_password": "Geheim123"})
	// Art (Ergebnis) als Text: error (r1) < nothing (r3) < ok (r2)
	for i, r := range []struct{ msg, result string }{{"r1", "error"}, {"r2", "ok"}, {"r3", "nothing"}} {
		if _, err := env.pool.Exec(t.Context(), `INSERT INTO energy_export_runs (connection_id, started_at, result, message) VALUES ($1::uuid, $2, $3, $4)`,
			env.id, logSeedAt(i), r.result, r.msg); err != nil {
			t.Fatal(err)
		}
	}
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ query, want, mode string }{
		{"", "r3|r2|r1", "new"}, {"?ls_export=old", "r1|r2|r3", "old"}, {"?ls_export=kind", "r1|r3|r2", "kind"},
		{"?ls_export=person", "r3|r2|r1", "new"}, // „Nach Person“ gibt es für Export-Läufe nicht
	} {
		d, status := env.h.efficioPageData(efficioReq("GET", "/export/connections/"+env.id+"/efficio"+c.query, env.id, "", nil), env.id)
		if status != 0 {
			t.Fatalf("%s: Status %d", c.query, status)
		}
		var msgs []string
		for _, v := range d.Runs {
			msgs = append(msgs, v.Message)
		}
		if got := strings.Join(msgs, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.query, got, c.want)
		}
		logBarOK(t, renderPage(t, tmpl, "export_efficio", d), "export", c.mode)
	}
}
