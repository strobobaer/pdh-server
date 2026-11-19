package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/pkg/logger"
)

func TestServerLogSort(t *testing.T) {
	e := newHistEnv(t)
	store := logger.Store()
	oldDir := store.Dir()
	store.Configure(t.TempDir(), 14)
	t.Cleanup(func() { store.Close(); store.Configure(oldDir, 14) })
	tag := "sortprobe" + e.sfx
	now := time.Now()
	// Art (Stufe) als Text: error (r1) < info (r3) < warn (r2); Person: Anna r1/r3, Bert r2
	for i, l := range []struct{ msg, level, user string }{{"r1", "error", "Anna Alpha"}, {"r2", "warn", "Bert Beta"}, {"r3", "info", "Anna Alpha"}} {
		line, _ := json.Marshal(map[string]any{"time": now.Add(time.Duration(i-5) * time.Minute).Format(time.RFC3339Nano),
			"level": l.level, "message": l.msg, "bereich": tag, "user_name": l.user})
		if _, err := store.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r1|r3|r2"}} {
		req := httptest.NewRequest("GET", "/admin/server-config/logs?range=24h&component="+tag+"&ls_serverlog="+c.mode, nil)
		req = req.WithContext(context.WithValue(req.Context(), "user", e.user("Cora", "Core")))
		d := e.h.loadLogs(req)
		var msgs []string
		for _, r := range d.Rows {
			msgs = append(msgs, r.Msg)
		}
		if got := strings.Join(msgs, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		out := renderPage(t, tmpl, "server_logs", d)
		logBarOK(t, out, "serverlog", c.mode)
		// Filter bleiben beim Umschalten erhalten
		if !strings.Contains(out, "component="+tag) {
			t.Errorf("%s: Filter „Bereich“ geht in der Leiste verloren", c.mode)
		}
	}
}
